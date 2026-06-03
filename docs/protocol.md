# Voxgate Protocol Notes

This document summarizes the protocol behavior implemented by `voxgate`, based on public reverse-engineering references and local probes.

## API Notice

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

`voxgate` refreshes tokens after 12 hours and falls back to the cached token if refresh fails.

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
  "audio_info": {"channel": 1, "format": "raw", "sample_rate": 16000},
  "enable_punctuation": true,
  "enable_speech_rejection": false,
  "extra": {
    "app_name": "com.android.chrome",
    "cell_compress_rate": 8,
    "did": "<device_id>",
    "input_mode": "tool",
    "enable_asr_threepass": true,
    "enable_asr_twopass": true,
    "use_twopass_retry": true,
    "strong_ddc": true,
    "end_smooth_window_ms": 800,
    "remove_space_between_han_num": true,
    "remove_space_between_han_eng": true,
    "enable_print_chinese": false,
    "disable_user_words": false,
    "context": "<base64 session context>"
  }
}
```

`audio_info.format` is `raw` for default builds and `speech_opus` for `-tags
opus` builds. The `extra` object tunes recognition and output formatting:

| Field | Effect |
|---|---|
| `enable_asr_twopass` / `enable_asr_threepass` | enable the second and third recognition passes; the third pass produces the most accurate final text |
| `use_twopass_retry` | retry the second pass when the first result is low confidence |
| `strong_ddc` | strengthen the disfluency/text-correction model; the main lever for cleaner, better-corrected output |
| `end_smooth_window_ms` | VAD end-of-speech smoothing window in milliseconds; affects how completely an utterance is closed before finalizing |
| `remove_space_between_han_num` / `remove_space_between_han_eng` | drop spaces between Han characters and digits / Latin letters in the output |
| `enable_print_chinese` | when false, keep digits as Arabic numerals instead of spelling them in Chinese |
| `disable_user_words` | when false, apply the account's uploaded personal lexicon during recognition (see Personalization) |
| `cell_compress_rate` | upstream cellular-network compression hint |
| `context` | base64 session context (see Session Context) |

### Session Context

`extra.context` is a base64-encoded JSON document describing prior input and
client info, used to bias recognition with conversational context:

```json
{
  "chat": [
    {
      "type": "user_input",
      "data": "{\"cursor_position\":9,\"text\":\"prior text\"}",
      "time": "<unix_millis>",
      "app_apk_name": "com.android.chrome"
    }
  ],
  "ime_info": {"app_apk_name": "com.android.chrome", "input_type": ""}
}
```

`chat` holds up to the last 20 input entries (`asr_input` or `user_input`),
where each `data` is itself a JSON string carrying the entry `text` and cursor
position. A continuous-input client fills `chat` from earlier utterances; a
one-shot transcription has no history, so `voxgate` leaves `chat` empty and
injects any `--prompt` text as a single `user_input` entry. This is a soft
context bias, not a hard term boost — strong vocabulary correction comes from
the personal lexicon below.

## Personalization (Hotwords / Personal Lexicon)

Strong vocabulary correction comes from an account-level personal word list,
uploaded out of band over HTTPS and then applied during recognition when
`extra.disable_user_words` is `false`. `voxgate` exposes this through the
`--hotwords` flag, the `asr.hotwords` config key, and `VOXGATE_ASR_HOTWORDS`.

Flow:

1. **Context token.** `POST https://ime.oceancloudapi.com/api/v1/user/get_config`
   with body `{"sami_app_key":"<app key>"}` returns `data.sami_token`. This
   token authenticates the context APIs and is cached in-process.
2. **Encrypted upload.** `POST https://speech.bytedance.com/api/v3/context/ime/user_words`
   with `{"user": {...}, "user_words": [{"word": "..."}]}`. The body is sealed
   with the Wave envelope below; the response, if present, is opened the same way.
3. **Apply.** Subsequent ASR sessions send `disable_user_words=false`, so the
   uploaded words bias recognition.

User words are append/accumulate semantics: the server keeps prior words and
dedups. There is no public "clear lexicon" API — only report/upload. `voxgate`
caches already-reported words per device (`*.hotwords.json` next to the
credential file) to skip redundant uploads.

### Wave Encryption Envelope

The context endpoints encrypt request and response bodies:

- A one-time `POST https://keyhub.zijieapi.com/handshake` performs an ECDH key
  exchange (ephemeral ECDSA P-256 keys) and returns a session ticket.
- The shared secret is run through HKDF-SHA256 to derive a ChaCha20 key.
- Each request body is encrypted with ChaCha20 under a fresh 12-byte nonce.
- Headers carry the envelope: `x-tt-e-b: 1`, `x-tt-e-t: <ticket>`,
  `x-tt-e-p: <base64 nonce>`. A response with `x-tt-e-p` is decrypted with the
  same key and the response nonce.

## Protobuf Schema

Field numbers are critical and are manually encoded to avoid protoc requirements.

Request fields:

| field | type | meaning |
|---:|---|---|
| 2 | string | token |
| 3 | string | service_name |
| 5 | string | method_name |
| 6 | string | JSON payload |
| 7 | bytes | audio data (`raw` PCM by default, Opus when built with `-tags opus`) |
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
| 9 | int32 | response phase marker observed as `0` for start/finish control responses, `3` for interim recognition results, and `9` for final recognition result |
| 11 | bytes | opaque backend metadata, observed as a 53-byte field on real responses; currently preserved for diagnostics only |

Frame states:

- `1`: first
- `3`: middle
- `9`: last

## Audio Frames

Input is normalized to 16 kHz mono signed 16-bit little-endian PCM through ffmpeg. Frames are 20 ms:

- 320 samples
- 640 bytes

Each frame is wrapped in `TaskRequest` and sent as a binary WebSocket message.
Default builds send the 20 ms PCM frame directly and declare
`audio_info.format=raw`. Builds compiled with `-tags opus` may send Opus frames
and declare `audio_info.format=speech_opus`. The final marker is a last frame
with frame_state `9`, followed by `FinishSession`.

Observed `audio_info.format` behavior on the IME protobuf WebSocket:

| Declared format | Wire payload tested | Result |
|---|---|---|
| `raw` | 20 ms PCM frames | works |
| `pcm` | 20 ms PCM frames | works |
| `wav` | 20 ms PCM frames | works, but no WAV container was sent |
| `aac` | 20 ms PCM frames | works, but no AAC frames were sent |
| `acc` | 20 ms PCM frames | works, treated like the APK enum spelling observed by user research |
| `opus` | 20 ms PCM frames | works, but this is not the official Opus wire shape |
| `speech_opus` | Opus frames | works |
| `speech_opus` | 20 ms PCM frames | fails |
| `raw`/`pcm`/`wav`/`aac`/`acc`/`opus` | Opus frames | no transcript |

The practical choices are therefore `raw` with PCM payload for portable builds,
or `speech_opus` with Opus payload for compressed upstream audio. The other
declared values are compatibility observations, not recommended client modes.

## Observed Runtime Limits

These are empirical findings from local probes against the real endpoint, not guaranteed protocol documentation:

| Probe | Result | Implication |
|---|---|---|
| Device registration + settings token | succeeds | automatic credential bootstrap is viable |
| 4.2 s Chinese WAV | succeeds, returns final text | basic protobuf/audio/WS flow is correct |
| 60 s Chinese WAV, sent as fast as possible | succeeds, returns multiple VAD segments | file-mode faster-than-realtime upload can work for moderate lengths |
| 60 s Chinese WAV, realtime paced | succeeds in about 63 s, same output length as fast mode | realtime pacing is not required for moderate file chunks |
| 90/120/180/300/480 s Chinese WAV, one WS session | succeeds | the practical single-session limit is higher than one minute |
| 540/570 s Chinese WAV, one WS session | exits with no transcript text | failure appears near the 9-minute range for the probe sample |
| 10 min repeated Chinese WAV, one WS session | exits after about 78 s with no transcript text | one long batch session is not reliable |
| 10 min repeated Chinese WAV, split into bounded sessions | succeeds, returns concatenated transcript | long-file CLI/server mode should chunk and stitch |

The current working assumption is that the endpoint is optimized for IME utterances and moderate continuous dictation, not arbitrary long offline batch transcription in one session. A normal WebSocket close with no text must be treated as a protocol failure, not as successful empty transcription.

## Client Strategy

The client should choose strategy by input mode:

| Mode | Strategy |
|---|---|
| short file, default | one WS session, send frames as fast as server tolerates |
| long file | split PCM into bounded chunks, run multiple sessions, concatenate results |
| live microphone/realtime stream | one WS session, send frames every 20 ms |
| stream output for file | single-session event stream; HTTP multipart upload completes before SSE output starts |

Recommended defaults:

- chunk long files at 30 seconds by default; longer chunks can succeed but often return only the current IME transcript window instead of complete text
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
- whether chunked streaming should be exposed once boundary events and timestamp semantics are stable
- whether `enable_asr_threepass=false` changes latency or long-session stability

## Response Parsing

Recognition responses carry JSON in response field 7. Useful shapes:

```json
{"extra":{"vad_start":true}}
{"results":[{"text":"今天","is_interim":true}]}
{"results":[{"text":"今天。","is_interim":false,"is_vad_finished":true}]}
{"results":[{"text":"今天。","is_interim":false,"is_vad_finished":true,"extra":{"nonstream_result":true}}]}
{"results":[
  {"text":"今天。明天","is_interim":true},
  {"text":"今天。","is_interim":true,"extra":{"nonstream_result":true}},
  {"text":"明天","start_time":2.1,"is_interim":true}
]}
```

Mapping:

- `extra.vad_start=true` -> native `speech.started`
- the first non-empty `results[]` entry is treated as the full editable display snapshot
- append-only display changes -> `transcript.text.delta`
- non-append display revisions -> `transcript.text.update` with the replacement snapshot
- `extra.nonstream_result=true` or `!is_interim && is_vad_finished` -> `transcript.segment.stable`
- input source/session end -> `transcript.text.done` with the full immutable transcript

## Utterance Boundaries

The upstream result payload exposes stable recognition phases, but those phases
are not necessarily permanent commits. In continuous microphone input the
backend can later revise punctuation or merge a stable phrase with following
speech. For that reason, `voxgate` treats `transcript.segment.stable` as
metadata and keeps the authoritative transcript in the full display snapshot.
Only `transcript.text.done` is immutable.

For `transcript.segment.stable`, `text` and `snapshot` both carry the upstream
stable full transcript view after applying that result. `voxgate` does not infer
the newly stable suffix or sentence boundary because the upstream protocol does
not expose that as an explicit field. Consumers that render subtitle lines
should derive line breaks at the application layer from `snapshot` and `results`.

When one upstream payload contains multiple `results`, those entries are treated
as alternate views of the recognition state, not as strings to concatenate.
In observed live traces, `results[0]` is the full display snapshot,
`results[1]` often repeats the previous stable phrase, and `results[2]` can be
the current in-progress phrase. Native NDJSON preserves all raw entries in
verbose metadata and exposes `transcript.text.update` for non-append revisions
that cannot be represented as OpenAI text deltas.

Example native NDJSON stream for an upstream sequence that mixes cumulative and
current-phrase results:

```json
{"type":"speech.started","request_id":"req_...","timestamp_ms":2172}
{"type":"transcript.text.delta","request_id":"req_...","revision":1,"text":"最近","delta":"最近","snapshot":"最近"}
{"type":"transcript.text.delta","request_id":"req_...","revision":2,"text":"我","delta":"我","snapshot":"最近我"}
{"type":"transcript.text.update","request_id":"req_...","revision":3,"text":"最近我在使用 Unsopee 的 CloudCall。","snapshot":"最近我在使用 Unsopee 的 CloudCall。"}
{"type":"transcript.segment.stable","request_id":"req_...","utterance_id":"seg_000000","text":"最近我在使用 Unsopee 的 CloudCall。","snapshot":"最近我在使用 Unsopee 的 CloudCall。"}
{"type":"transcript.text.delta","request_id":"req_...","revision":4,"text":"确实","delta":"确实","snapshot":"最近我在使用 Unsopee 的 CloudCall。确实"}
{"type":"transcript.text.update","request_id":"req_...","revision":5,"text":"最近我在使用 Unsopee 的 CloudCall，确实","snapshot":"最近我在使用 Unsopee 的 CloudCall，确实"}
{"type":"transcript.text.delta","request_id":"req_...","revision":6,"text":"感觉挺好用的呢。","delta":"感觉挺好用的呢。","snapshot":"最近我在使用 Unsopee 的 CloudCall，确实感觉挺好用的呢。"}
{"type":"transcript.text.done","request_id":"req_...","text":"最近我在使用 Unsopee 的 CloudCall，确实感觉挺好用的呢。","duration":9.71}
```

For `/v1/audio/transcriptions?stream=true`, only the OpenAI-compatible SSE
events are exposed:

```text
event: transcript.text.delta
data: {"type":"transcript.text.delta","delta":"你好呀。"}

event: transcript.text.delta
data: {"type":"transcript.text.delta","delta":"我觉得今天的天气不错"}

event: transcript.text.delta
data: {"type":"transcript.text.delta","delta":"。"}

event: transcript.text.done
data: {"type":"transcript.text.done","text":"你好呀。我觉得今天的天气不错。"}
```

If upstream revises earlier text instead of appending, native NDJSON carries the
replacement as a snapshot update:

```json
{"type":"transcript.text.delta","delta":"天气不","snapshot":"天气不","revision":1}
{"type":"transcript.text.update","text":"天气很好","snapshot":"天气很好","revision":2}
{"type":"transcript.text.delta","delta":"。","snapshot":"天气很好。","revision":3}
{"type":"transcript.segment.stable","text":"天气很好。","snapshot":"天气很好。","utterance_id":"seg_000000","revision":3}
```

The OpenAI-compatible SSE and Realtime outputs expose only append-only deltas;
their final `done` / `completed` event is authoritative when a non-append
revision occurred internally. Realtime may use `transcript.segment.stable` to
produce low-latency `conversation.item.input_audio_transcription.completed`
events because the official event model has no replacement-update event.

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
9. Emit OpenAI-compatible terminal output

Long-file batch flow:

1. Decode whole input to 16 kHz mono PCM.
2. Split into bounded PCM chunks on frame boundaries.
3. Run the single-session state machine for each chunk.
4. Concatenate chunk transcript.text.done values.
5. In a later hardening pass, add small overlap and duplicate-boundary trimming.

## Test Matrix

- Protobuf field number byte tests
- Token cache and config priority tests
- Opus frame encode test
- Timestamp formatting tests for SRT/VTT
- Stable-segment and transcript-done tests
- Mock WebSocket three-pass flow
- HTTP server JSON/SSE tests with a mock WebSocket backend
- CLI command-surface tests for help and early format validation
- Deterministic E2E harness with a protocol-faithful mock upstream, covering
  CLI formats, stdin streaming, HTTP transcription, SSE, Realtime WebSocket,
  and raw trace validation
- Real endpoint e2e scripts are included but require network access and a working non-public endpoint

## Current Strategy Decision

Until the unanswered limits above are measured more thoroughly, `voxgate` should not promise "one WebSocket session can transcribe arbitrary long files." The stable product behavior should be:

- CLI and HTTP file transcription transparently chunk long inputs.
- Realtime mode remains single-session and bounded by backend behavior.
- Tests should lock down chunk stitching, multi-utterance stable handling, and empty-normal-close failure handling.
- E2E validation should report the chosen chunk size, total audio duration, elapsed wall time, and whether every chunk produced non-empty text.
