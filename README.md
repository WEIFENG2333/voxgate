# ime-asr

`ime-asr` is a Go CLI and local HTTP server that exposes a research-only IME ASR backend through an OpenAI-compatible transcription API.

中文：`ime-asr` 是一个 Go 编写的命令行工具和本地 HTTP 服务，把社区逆向的输入法 ASR WebSocket 接口封装成 OpenAI Audio Transcription 兼容接口。

## Compliance

This project uses a non-public input method ASR API without official authorization. It is for learning and research only, must not be used commercially, and may be blocked or changed at any time.

本项目使用非公开输入法 ASR 接口，没有官方授权。仅供学习研究，不得商用；接口可能随时变更、封禁或失效。

## Install

Requirements:

- Go 1.22+
- `libopus` development package
- `ffmpeg`

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

Install ffmpeg and libopus through MSYS2 or vcpkg, then build with CGO enabled.

## CLI

```bash
ime-asr transcribe <file|->
ime-asr serve
ime-asr doctor
ime-asr auth
ime-asr version
```

Examples:

```bash
ime-asr transcribe sample.wav
ime-asr transcribe sample.mp3 --format json
ime-asr transcribe sample.m4a --format srt -o sample.srt
ime-asr transcribe sample.flac --format vtt
cat sample.wav | ime-asr transcribe - --input-format wav --stream
ime-asr transcribe raw.pcm --input-format raw --sample-rate 16000 --format verbose_json
```

Default output:

- stdout is TTY: `text`
- stdout is pipe/file: `json`
- `--stream`: `ndjson`

## Server

```bash
ime-asr serve --host 127.0.0.1 --port 8080
ime-asr serve --auth-token local-token
ime-asr serve --max-concurrency 8 --request-timeout 120s
ime-asr serve --host 0.0.0.0 --port 8080
ime-asr serve --enable-realtime
```

Endpoints:

| Path | Method | Status |
|---|---|---|
| `/v1/audio/transcriptions` | POST multipart | implemented |
| `/v1/audio/translations` | POST | returns 400 |
| `/v1/models` | GET | implemented |
| `/v1/realtime` | WebSocket | placeholder |
| `/health` | GET | implemented |
| `/metrics` | GET | implemented |

Python OpenAI SDK example:

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8080/v1", api_key="local-token")
with open("sample.wav", "rb") as f:
    result = client.audio.transcriptions.create(
        model="ime-asr",
        file=f,
        response_format="json",
    )
print(result.text)
```

Go OpenAI SDK smoke client:

```bash
cd tests/e2e/openai-go-client
go run . --base-url http://127.0.0.1:8080/v1 --api-key local-token ../../../sample.wav
```

SSE:

```bash
curl -N http://127.0.0.1:8080/v1/audio/transcriptions \
  -H 'Authorization: Bearer local-token' \
  -F model=ime-asr \
  -F stream=true \
  -F file=@sample.wav
```

## OpenAI Compatibility

| OpenAI parameter | Behavior |
|---|---|
| `file` | supported |
| `model` | accepted, ignored |
| `response_format` | `json`, `text`, `srt`, `vtt`, `verbose_json` |
| `stream=true` | SSE with `transcript.text.delta` / `transcript.text.done` |
| `language` | accepted, backend effectively ignores it |
| `prompt` | accepted for compatibility, not sent to backend |
| `temperature` | ignored |
| translations endpoint | unsupported |

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
```

Environment variables use `IME_ASR_*`. `DOUBAO_ASR_*` aliases are also accepted for compatibility.

## Development

```bash
make build
make test
make test-e2e
make doctor
```

The integration tests use a mock WebSocket server and do not require the real backend. Real endpoint tests are in `tests/e2e/` and require network access plus a still-working backend.

## Known Limits

- The backend is non-public and unstable.
- Word timestamps are not provided; SRT/VTT cue timing is coarse until richer timing extraction is added.
- Realtime WebSocket compatibility is reserved but not fully implemented yet.
- Cross-platform release uses native CGO builds because libopus is a system dependency.
