# IME ASR Protocol Notes

This document summarizes the implementation notes extracted from:

- `missuo/koe`: `koe-asr/src/doubaoime.rs`, `koe-asr/src/transcript.rs`
- `WEIFENG2333/doubao-asr`: `doubaoime_asr/asr.proto`, `device.py`, `asr.py`, `config.py`

## Compatibility Notice

The backend is a non-public input-method ASR endpoint. It is not an official public API, may change or stop working without notice, and should be used only for learning and research.

## Device Registration

The client first registers a synthetic Android device through:

- `POST https://log.snssdk.com/service/2/device_register/`
- `aid=401734`, `app_name=oime`, package `com.bytedance.android.doubaoime`
- User-Agent mimics the Android input method client.

Important generated identifiers:

- `cdid`: UUID
- `openudid`: 8 random bytes encoded as hex
- `clientudid`: UUID

The response yields `device_id` and `install_id`. `device_id` is required for both token retrieval and WebSocket connection.

## Token Retrieval

ASR auth token is fetched through:

- `POST https://is.snssdk.com/service/settings/v3/`
- Request body is the literal string `body=null`
- Header `x-ss-stub` is uppercase `MD5("body=null")`
- Response path: `data.settings.asr_config.app_key`

The Rust reference refreshes after 12 hours and falls back to the cached token if refresh fails. `ime-asr` follows that behavior.

## WebSocket Handshake

Endpoint:

```text
wss://frontier-audio-ime-ws.doubao.com/ocean/api/v1/ws?aid=401734&device_id=<device_id>
```

Headers:

- `User-Agent`: Android IME UA
- `proto-version: v2`
- `x-custom-keepalive: true`

Handshake:

1. Send protobuf `StartTask` with `token`, `service_name=ASR`, `request_id`.
2. Expect `TaskStarted`; `TaskFailed` is fatal.
3. Send protobuf `StartSession` with session JSON.
4. Expect `SessionStarted`; `SessionFailed` is fatal.

Session JSON:

```json
{
  "audio_info": {"channel": 1, "format": "speech_opus", "sample_rate": 16000},
  "enable_punctuation": true,
  "enable_speech_rejection": false,
  "extra": {
    "app_name": "com.android.chrome",
    "cell_compress_rate": 8,
    "did": "<device_id>",
    "enable_asr_threepass": true,
    "enable_asr_twopass": true,
    "input_mode": "tool"
  }
}
```

## Protobuf Schema

Field numbers are critical and are manually encoded to avoid protoc requirements.

Request fields:

| field | type | meaning |
|---:|---|---|
| 2 | string | token |
| 3 | string | service_name |
| 5 | string | method_name |
| 6 | string | JSON payload |
| 7 | bytes | Opus audio data |
| 8 | string | request_id |
| 9 | enum/int32 | frame_state |

Response fields:

| field | type | meaning |
|---:|---|---|
| 1 | string | request_id |
| 2 | string | task_id |
| 3 | string | service_name |
| 4 | string | message_type |
| 5 | int32 | status_code |
| 6 | string | status_message |
| 7 | string | result_json |
| 9 | int32 | unknown |

Frame states:

- `1`: first
- `3`: middle
- `9`: last

## Audio Frames

Input is normalized to 16 kHz mono signed 16-bit little-endian PCM through ffmpeg. Frames are 20 ms:

- 320 samples
- 640 bytes

Each frame is Opus encoded with application `audio`, wrapped in `TaskRequest`, and sent as a binary WebSocket message. The final marker is a last frame with frame_state `9`, followed by `FinishSession`.

## Observed Runtime Limits

These are empirical findings from local probes against the real endpoint, not guaranteed protocol documentation:

| Probe | Result | Implication |
|---|---|---|
| Device registration + settings token | succeeds | automatic credential bootstrap is viable |
| 4.2 s Chinese WAV | succeeds, returns final text | basic protobuf/Opus/WS flow is correct |
| 60 s Chinese WAV, sent as fast as possible | succeeds, returns multiple VAD segments | file-mode faster-than-realtime upload can work for moderate lengths |
| 60 s Chinese WAV, realtime paced | succeeds in about 63 s, same output length as fast mode | realtime pacing is not required for moderate file chunks |
| 10 min repeated Chinese WAV, one WS session | exits after about 78 s with no transcript text | one long batch session is not reliable |
| 10 min repeated Chinese WAV, split into ~55 s sessions | succeeds in about 94 s, returns concatenated transcript | long-file CLI/server mode should chunk and stitch |

The current working assumption is that the endpoint is optimized for IME utterances and moderate continuous dictation, not arbitrary long offline batch transcription in one session. A normal WebSocket close with no text must be treated as a protocol failure, not as successful empty transcription.

## Client Strategy

The client should choose strategy by input mode:

| Mode | Strategy |
|---|---|
| short file, default | one WS session, send frames as fast as server tolerates |
| long file | split PCM into bounded chunks, run multiple sessions, concatenate results |
| live microphone/realtime stream | one WS session, send frames every 20 ms |
| stream output for file | single-session event stream for short files; long-file streaming needs chunk boundary events before it should be exposed as stable API |

Recommended defaults:

- chunk long files at 45-60 seconds until more probing proves a larger safe window
- use fresh `request_id` per chunk/session
- reuse cached device credentials and token across chunks
- retry one failed session after token refresh
- treat `TaskFailed` and `SessionFailed` as server errors
- treat normal close without any transcript as protocol/server failure
- keep send/recv decoupled so server events are processed while audio is still being uploaded

Open design questions:

- whether a server-side maximum frame count, payload rate, or session duration causes empty normal closes
- whether realtime throttling allows single sessions beyond 60 seconds reliably
- whether repeated identical audio triggers de-duplication or abuse heuristics
- whether chunk boundaries need silence padding or overlap to reduce dropped boundary syllables
- whether `enable_asr_threepass=false` changes latency or long-session stability

## Response Parsing

Recognition responses carry JSON in response field 7. Useful shapes:

```json
{"extra":{"vad_start":true}}
{"results":[{"text":"今天","is_interim":true}]}
{"results":[{"text":"今天。","is_interim":false,"is_vad_finished":true}]}
{"results":[{"text":"今天。","is_interim":false,"is_vad_finished":true,"extra":{"nonstream_result":true}}]}
```

Mapping:

- `extra.vad_start=true` -> `vad.start`
- `is_interim=true` -> interim delta
- `is_interim=false` without final marker -> definite/two-pass delta
- `extra.nonstream_result=true` or `!is_interim && is_vad_finished` -> final/three-pass result

## VAD Segment Reset

Long audio may reset text at a new VAD segment. The Python reference loses earlier text because it replaces the final text with the new short segment. The Rust reference preserves previous segments with this heuristic:

```text
if previous segment text is non-empty
and current text length < previous length / 2
and previous does not start with current
then append previous text to confirmed_text
```

`ime-asr` implements the same heuristic in `internal/asr/aggregator.go` and covers it with unit and mock WebSocket integration tests.

## Go State Machine

Single-session core flow:

1. `CredentialManager.Ensure`
2. WebSocket connect
3. `StartTask`
4. `StartSession`
5. Start send goroutine and receive goroutine
6. Send PCM frames as Opus `TaskRequest`
7. Parse response JSON into typed events
8. Aggregate reset segments
9. Emit OpenAI-compatible final output

Long-file batch flow:

1. Decode whole input to 16 kHz mono PCM.
2. Split into bounded PCM chunks on frame boundaries.
3. Run the single-session state machine for each chunk.
4. Concatenate chunk final texts.
5. In a later hardening pass, add small overlap and duplicate-boundary trimming.

## Test Matrix

- Protobuf field number byte tests
- Token cache and config priority tests
- Opus frame encode test
- Timestamp formatting tests for SRT/VTT
- VAD reset boundary tests
- Mock WebSocket three-pass flow with reset
- HTTP server JSON/SSE tests planned as next hardening target
- Real endpoint e2e scripts are included but require network access and a working non-public endpoint

## Current Strategy Decision

Until the unanswered limits above are measured more thoroughly, `ime-asr` should not promise "one WebSocket session can transcribe arbitrary long files." The stable product behavior should be:

- CLI and HTTP file transcription transparently chunk long inputs.
- Realtime mode remains single-session and bounded by backend behavior.
- Tests should lock down chunk stitching, VAD reset preservation, and empty-normal-close failure handling.
- E2E validation should report the chosen chunk size, total audio duration, elapsed wall time, and whether every chunk produced non-empty text.
