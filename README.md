# ime-asr

`ime-asr` is a research CLI and local HTTP server for Chinese speech transcription. It wraps a non-public input-method ASR WebSocket backend and exposes an OpenAI-compatible `/v1/audio/transcriptions` API.

中文：`ime-asr` 是一个中文语音转文字工具，支持命令行转写本地文件，也可以启动本地 OpenAI 兼容 HTTP 服务。

## Important Notice

This project uses a non-public input-method ASR API without official authorization. It is for learning and research only, must not be used commercially, and may stop working at any time.

本项目使用非公开输入法 ASR 接口，没有官方授权。仅供学习研究，不得商用；接口可能随时变更、封禁或失效。

## Install

Requirements:

- Go 1.22+
- `ffmpeg`
- `libopus` development package
- CGO enabled

Linux:

```bash
sudo apt-get install -y ffmpeg libopus-dev pkg-config
go install github.com/WEIFENG2333/ime-asr/cmd/ime-asr@latest
```

macOS:

```bash
brew install ffmpeg opus pkg-config
go install github.com/WEIFENG2333/ime-asr/cmd/ime-asr@latest
```

Windows:

Install `ffmpeg`, `pkg-config`, a C compiler, and `libopus` through MSYS2 or vcpkg, then build with `CGO_ENABLED=1`.

## Quick Start

Transcribe a file:

```bash
ime-asr transcribe speech.wav
```

Return OpenAI-style JSON:

```bash
ime-asr transcribe speech.mp3 --format json
```

Generate coarse subtitles:

```bash
ime-asr transcribe speech.m4a --format srt -o speech.srt
ime-asr transcribe speech.mp4 --format vtt -o speech.vtt
```

Stream events as NDJSON:

```bash
ime-asr transcribe speech.wav --stream
```

Start the OpenAI-compatible local server:

```bash
ime-asr serve --host 127.0.0.1 --port 8080 --auth-token local-token
```

## CLI

Commands:

```bash
ime-asr transcribe <file|->
ime-asr serve
ime-asr doctor
ime-asr auth
ime-asr version
```

Common `transcribe` options:

| Option | Description |
|---|---|
| `--format text|json|verbose_json|srt|vtt|ndjson` | output format |
| `--stream` | stream incremental output |
| `--output <file>` / `-o <file>` | write to file |
| `--input-format wav|pcm16|raw` | stdin input format |
| `--sample-rate <hz>` | raw PCM sample rate |
| `--request-timeout <duration>` | per-session timeout |

Advanced compatibility options:

| Option | Description |
|---|---|
| `--language <code>` / `-l <code>` | accepted for OpenAI compatibility; backend effectively ignores it |
| `--prompt <text>` | accepted for compatibility; not sent to the backend |
| `--no-punctuation` | disable punctuation |
| `--disable-three-pass` | disable the third recognition pass |
| `--realtime` | send audio at 20 ms pacing |
| `--no-chunk` | disable long-file chunking for protocol probing |
| `--chunk-duration <duration>` | default `300s` |

Default output format:

| Situation | Default |
|---|---|
| stdout is a terminal | `text` |
| stdout is piped or redirected | `json` |
| `--stream` is set | `ndjson` |

Examples:

```bash
ime-asr transcribe speech.wav
ime-asr transcribe speech.mp3 --format json
ime-asr transcribe speech.m4a --format verbose_json
ime-asr transcribe speech.flac --format srt -o speech.srt
ime-asr transcribe speech.mp4 --format vtt -o speech.vtt
cat speech.wav | ime-asr transcribe - --input-format wav --stream
ime-asr transcribe raw.pcm --input-format raw --sample-rate 16000 --format json
```

## Long Audio Strategy

The backend is optimized for IME-style speech, not arbitrary long batch transcription in one WebSocket session.

Current policy:

| Input length | Behavior |
|---|---|
| `<= 300s` | one WebSocket session |
| `> 300s` | split into serial 300-second chunks, one WebSocket session per chunk |

Chunking is time based after ffmpeg converts audio to `16kHz mono PCM`. Boundaries are aligned to 20 ms Opus frames. The chunker is not silence-aware yet and does not add overlap.

SRT/VTT timestamps are coarse. For chunked long files, cue ranges are chunk offsets in the original file timeline, not word-level or sentence-level ASR timestamps.

## Server

Start:

```bash
ime-asr serve --host 127.0.0.1 --port 8080
ime-asr serve --auth-token local-token
ime-asr serve --max-concurrency 8 --request-timeout 120s
```

Implemented endpoints:

| Path | Method | Notes |
|---|---|---|
| `/v1/audio/transcriptions` | POST multipart | OpenAI-compatible transcription |
| `/v1/audio/translations` | POST | returns 400; translation is unsupported |
| `/v1/models` | GET | returns `ime-asr` |
| `/health` | GET | health check |
| `/metrics` | GET | minimal Prometheus text |

`/v1/realtime` is reserved for future work and currently returns an error.

Python OpenAI SDK:

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8080/v1", api_key="local-token")

with open("speech.wav", "rb") as f:
    result = client.audio.transcriptions.create(
        model="ime-asr",
        file=f,
        response_format="json",
    )

print(result.text)
```

SSE streaming:

```bash
curl -N http://127.0.0.1:8080/v1/audio/transcriptions \
  -H 'Authorization: Bearer local-token' \
  -F model=ime-asr \
  -F stream=true \
  -F file=@speech.wav
```

For HTTP file uploads, the client uploads the multipart body first. The local server then decodes the file and streams transcription events back as SSE. This is output streaming, not bidirectional upload streaming.

## OpenAI Compatibility

| OpenAI field | Behavior |
|---|---|
| `file` | supported |
| `model` | accepted, ignored |
| `response_format` | `json`, `text`, `srt`, `vtt`, `verbose_json` |
| `stream=true` | SSE events `transcript.text.delta` and `transcript.text.done` |
| `language` | accepted, backend effectively ignores it |
| `prompt` | accepted, not sent to backend |
| `temperature` | ignored |
| translations | unsupported |

Errors use OpenAI-style JSON:

```json
{"error":{"message":"...","type":"invalid_request_error","code":"..."}}
```

## Configuration

Priority:

```text
flags > environment variables > YAML config > defaults
```

Example:

```yaml
credential_path: ~/.config/ime-asr/credentials.json
asr:
  enable_punctuation: true
  enable_three_pass: true
  enable_two_pass: true
server:
  host: 127.0.0.1
  port: 8080
  auth_token: ""
  max_concurrency: 4
  request_timeout: 60s
```

Environment variables use `IME_ASR_*`. `DOUBAO_ASR_*` aliases are accepted for compatibility with older local setups.

## Development

```bash
make build
make test
make test-e2e
make probe
make doctor
```

CI runs on Linux, macOS Intel, macOS Apple Silicon, and Windows. It checks formatting, runs `go vet`, runs unit/integration tests, runs Linux race tests, and builds the CLI.

Real endpoint probes are under `tests/e2e/` and require network access plus a still-working backend.

## Known Limits

- The backend is non-public and unstable.
- Long-file chunking is serial, not parallel.
- SRT/VTT timing is coarse and not suitable for precise subtitle alignment yet.
- `/v1/realtime` is not implemented.
- Cross-platform release uses native CGO builds because `libopus` is a system dependency.
