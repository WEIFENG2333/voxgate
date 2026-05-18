# Validation Report

Last local validation in this workspace used:

- Go 1.24.4 local toolchain with `go 1.22` module target
- system `libopus` 1.3.1
- system ffmpeg 2.4.3
- real cached IME ASR credentials

## Unit And Integration

```text
go test ./...
```

Passed packages:

- protobuf byte encoding/decoding
- response JSON parser
- VAD reset aggregator
- Opus frame encoding
- ffmpeg PCM frame source
- output format timestamps
- config priority
- mock WebSocket ASR client
- HTTP JSON and SSE endpoints with mock WebSocket backend
- CLI help and early format validation

## Real Protocol Probe

Probe command:

```bash
make probe
```

Latest observed matrix:

| Case | Exit | Elapsed | Output |
|---|---:|---:|---:|
| 5 s fast | 0 | 2 s | 63 bytes |
| 5 s fast without three-pass | 0 | 1 s | 63 bytes |
| 60 s fast | 0 | 9 s | 621 bytes |
| 60 s realtime paced | 0 | 63 s | 621 bytes |
| 90 s single session | 0 | 30 s | 927 bytes |
| 120 s single session | 0 | 22 s | 1218 bytes |
| 180 s single session | 0 | 34 s | 1821 bytes |
| 300 s single session | 0 | 55 s | 3012 bytes |
| 480 s single session | 0 | 97 s | 4479 bytes |
| 540 s single session | 1 | 74 s | no transcript |
| 570 s single session | 1 | 72 s | no transcript |
| 10 min single session | 1 | 78 s | no transcript |
| 10 min auto chunk | 0 | 94 s | 6042 bytes |

Interpretation:

- short and medium files are accepted in one session
- 300 s chunks are below the observed failure point and reduce handshake overhead compared with the previous 55 s conservative default
- realtime pacing is not necessary for 60 s file input
- long single-session file transcription is not reliable
- automatic chunking is required for long file mode

## Real Format Probe

Using a real 5 s Chinese sample, the CLI successfully transcribed:

- WAV
- MP3
- M4A
- MP4 video with AAC audio

The text differed slightly between lossless WAV and lossy encoded variants, which is expected for ASR over compressed audio. All formats produced non-empty Chinese text.

## Public AISHELL Probe

Additional validation used public AISHELL-1 samples from speaker `S0002` downloaded from Hugging Face. The transcript file was used only as a rough reference; punctuation and Arabic numerals are backend-normalized and were not scored as formal WER.

CLI JSON on five separate real utterances:

| ID | Duration | Exit | Elapsed | Note |
|---|---:|---:|---:|---|
| `BAC009S0002W0122` | 5.999 s | 0 | 1 s | returned non-empty text |
| `BAC009S0002W0123` | 3.865 s | 0 | 1 s | returned non-empty text; backend dropped part of the phrase |
| `BAC009S0002W0124` | 5.409 s | 0 | 2 s | returned non-empty text |
| `BAC009S0002W0125` | 3.167 s | 0 | 1 s | returned non-empty text |
| `BAC009S0002W0126` | 2.576 s | 0 | 1 s | returned non-empty text |

Format/container coverage from `BAC009S0002W0122`:

| Input | Exit | Elapsed | Result |
|---|---:|---:|---|
| WAV | 0 | 2 s | JSON text returned |
| MP3 | 0 | 2 s | JSON text returned |
| M4A/AAC | 0 | 1 s | JSON text returned |
| MP4/H.264 + AAC | 0 | 2 s | JSON text returned |
| SRT | 0 | 2 s | fallback cue now uses full audio duration |
| NDJSON stream | 0 | 2 s | emitted task/session events and transcript deltas |

HTTP server validation on the same sample:

- `/health` returned `{"status":"ok"}`.
- `/v1/models` returned `ime-asr`.
- `/v1/audio/transcriptions` returned JSON text for MP3 input.
- `stream=true` returned SSE `transcript.text.delta` events.
- The OpenAI Python SDK smoke client returned text.
- The OpenAI Go SDK smoke client returned text.

Long-file validation from 71 concatenated AISHELL utterances:

| Duration | Mode | Exit | Elapsed | Output |
|---:|---|---:|---:|---:|
| 360 s | default auto chunk (`300s + 60s`) | 0 | 40 s | 2812 bytes JSON |

## OpenAI SDK Probe

Both SDK clients were run against local `ime-asr serve` with bearer auth:

```bash
python3 tests/e2e/openai_python_client.py \
  --base-url http://127.0.0.1:18082/v1 \
  --api-key test-token \
  tests/audio/zh_5s.wav

(cd tests/e2e/openai-go-client && \
  go run . --base-url http://127.0.0.1:18083/v1 \
  --api-key test-token \
  ../../../tests/audio/zh_5s.wav)
```

Both returned:

```text
甚至出现交易几乎停滞的情况，甚至。
```

## Remaining Gaps

- Need a larger licensed fixture set committed or downloaded by script rather than relying on a local workspace sample.
- Need OpenAI Python and Go SDK smoke tests wired into one command for CI-safe mock mode.
- Need WER calculation once fixtures include reference transcripts.
- Need a future subtitle-timing layer if precise SRT/VTT alignment is required.
