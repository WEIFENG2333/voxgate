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
| `enable_asr_twopass` / `enable_asr_threepass` | enable the second and third recognition passes (internal quality stages). They do not change the event/frame structure; the optional refined `nonstream_result` they can produce is emitted only intermittently by the backend |
| `use_twopass_retry` | retry the second pass when the first result is low confidence |
| `strong_ddc` | strengthen the disfluency/text-correction model; the main lever for cleaner, better-corrected output |
| `remove_space_between_han_num` / `remove_space_between_han_eng` | drop spaces between Han characters and digits / Latin letters in the output |
| `enable_print_chinese` | when false, keep digits as Arabic numerals instead of spelling them in Chinese |
| `disable_user_words` | when false, apply the account's uploaded personal lexicon during recognition (see Personalization) |
| `cell_compress_rate` | upstream cellular-network compression hint |
| `context` | base64 session context (see Session Context) |

`voxgate` intentionally omits the upstream `end_smooth_window_ms` VAD field. A
fixed end-of-speech window tuned for live microphone input prematurely closes
utterances during faster-than-realtime file upload and drops trailing segments;
the server default handles both file and live pacing.

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

Recognition responses carry JSON in response field 7. A frame normally has a
single `results` entry (`n=1`); this is the common case observed live:

```json
{"results":[{"text":"甚至出现交易几乎停滞的情况","is_interim":true,"index":0}]}
{"results":[{"text":"甚至出现交易几乎停滞的情况，甚至。","is_interim":false,"is_vad_finished":true,"index":0,
             "alternatives":[{"words":[{"word":"甚","start_time":0.5,"end_time":0.7}]}]}]}
```

Per-result fields:

- `text` — recognized text. In `results[0]` this is the **cumulative full
  transcript so far** (see Transcript Model).
- `is_interim` — `true` while still composing, `false` when settled.
- `is_vad_finished` — `true` only on the very last frame of the whole stream
  (silence detected / session ending). It does **not** mark mid-stream sentence
  boundaries.
- `stream_asr_finish` — `true` when a sentence has been finalized. Fires once per
  sentence (see Per-Sentence Structure). Carried on the sentence-level results,
  not on `results[0]`.
- `index` — a VAD counter; **not a reliable boundary signal** (stays `0` across
  long dictation). Not used.
- `confidence`, `alternatives[].words[]` — per-result confidence and per-character
  audio timestamps.

Intermittent shapes (emitted only sometimes — do not depend on them):
`{"extra":{"vad_start":true}}`, a per-result `extra.nonstream_result:true`
(threepass refinement), and multi-result frames (`n>=2`); see Per-Sentence
Structure for what the extra results mean.

Classification → native events:

- `extra.vad_start=true` → `speech.started`
- any recognition frame → `transcript.partial` (`text` = the cumulative full
  transcript so far, from `results[0]`); unchanged repeats are dropped
- input/session end → `transcript.done` (`text` = the final full transcript)

## Transcript Model (cumulative full text)

Every recognition frame carries the **whole transcript so far** in `results[0]`
(`start_time` is always `0`); the backend grows it and revises wording and
punctuation in place as it refines. `voxgate` forwards this faithfully: each
distinct snapshot becomes one `transcript.partial`, and the last snapshot is the
final full text delivered by `transcript.done`. There are no segments and no
boundary guessing — `text` is always the complete transcript.

### Example

Real native NDJSON for a clip "今天天气真不错" + pause + "我们一起去公园散步吧":

```json
{"type":"task.started"}
{"type":"session.started"}
{"type":"speech.started"}
{"type":"transcript.partial","text":"今天","end":0.34}
{"type":"transcript.partial","text":"今天天气真不错","end":1.54}
{"type":"transcript.partial","text":"今天天气真不错。","end":2.50}
{"type":"transcript.partial","text":"今天天气真不错。我们","end":7.02}
{"type":"transcript.partial","text":"今天天气真不错。我们一起去公园散步吧","end":8.78}
{"type":"transcript.partial","text":"今天天气真不错，我们一起去公园散步吧。","end":9.59}
{"type":"transcript.done","text":"今天天气真不错，我们一起去公园散步吧。","duration":10.85}
```

Note the pause renders as punctuation inside one growing transcript, and late
revisions (the `。`→`，` rewrite) happen in place. To render live output, overwrite
the display with each partial; to get append-only deltas, diff successive partials.

For `/v1/audio/transcriptions?stream=true`, only the OpenAI-compatible SSE
events are exposed:

```text
event: transcript.text.delta
data: {"type":"transcript.text.delta","delta":"今天天气真不错。"}

event: transcript.text.delta
data: {"type":"transcript.text.delta","delta":"我们一起去公园散步吧"}

event: transcript.text.done
data: {"type":"transcript.text.done","text":"今天天气真不错，我们一起去公园散步吧。"}
```

The SSE and Realtime endpoints derive these append-only deltas from the
cumulative stream via `internal/asr/assembler.go` (a delta is emitted only when a
partial extends the previous text; in-place revisions settle on done). SSE's
`transcript.text.done` carries the full text; Realtime emits one
`conversation.item.input_audio_transcription.completed` per item, at session end.

## Per-Sentence Structure (observed, not surfaced)

The backend internally knows sentence boundaries even though it never VAD-splits
file/streamed audio into separate finals. When more than one result is present,
they partition as: `results[0]` = cumulative full text; the remaining results are
the sentence breakdown — already-finalized sentences (`stream_asr_finish:true`,
`extra.nonstream_result:true`, frozen `end_time`) plus the in-flight current
sentence (`stream_asr_finish:false`, its own `start_time`). For the example above,
mid-second-sentence the frame held:

```json
{"results":[
  {"text":"今天天气真不错。我们","start_time":0,"end_time":7.02},
  {"text":"今天天气真不错。","start_time":0,"end_time":2.50,"stream_asr_finish":true,"extra":{"nonstream_result":true}},
  {"text":"我们","start_time":6.52,"end_time":7.02,"stream_asr_finish":false}
]}
```

So `stream_asr_finish` + per-sentence `start_time`/`end_time` would allow
sentence-by-sentence commit (input-method style). `voxgate` **deliberately does
not surface this** — the event model is intentionally the cumulative full text
only. The raw results array is still passed through verbatim on each event for
consumers that want to use it.

## Go State Machine

Single-session core flow:

1. `CredentialManager.Ensure`
2. WebSocket connect
3. `StartTask`
4. `StartSession`
5. Start send goroutine and receive goroutine
6. Send audio as `TaskRequest` frames (raw PCM by default, Opus with `-tags opus`); the last frame carries `force_asr_twopass`/`finish_audio`
7. Parse response JSON into typed events
8. Forward the cumulative full text as `transcript.partial` (dedup repeats), then `transcript.done` with the final full text
9. SSE/Realtime derive append-only deltas from the cumulative stream for OpenAI compatibility

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
