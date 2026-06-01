# voxgate

`voxgate` is a Chinese speech-to-text CLI and local HTTP server. It transcribes local audio/video files and can expose an OpenAI-compatible `/v1/audio/transcriptions` endpoint for existing SDK clients.

中文：`voxgate` 是一个中文语音转文字工具，可以直接转写本地音频/视频，也可以启动一个本地 OpenAI 兼容接口。

## Notice

This project wraps a non-public input-method ASR backend. It is for learning and research only, must not be used commercially, and may stop working if the upstream service changes or rejects requests.

本项目使用非公开输入法 ASR 后端，仅供学习研究，不得商用；上游服务可能随时变更、封禁或失效。

## Install

### Linux/macOS one-line install

Install the latest GitHub Release binary:

```bash
curl -fsSL https://raw.githubusercontent.com/WEIFENG2333/voxgate/main/scripts/install.sh | sh
```

By default this installs to `~/.local/bin`. To choose another directory:

```bash
curl -fsSL https://raw.githubusercontent.com/WEIFENG2333/voxgate/main/scripts/install.sh | VOXGATE_INSTALL_DIR=/usr/local/bin sh
```

### Windows PowerShell install

Install `ffmpeg` first:

```powershell
winget install Gyan.FFmpeg
```

Then install `voxgate` from the latest GitHub Release:

```powershell
irm https://raw.githubusercontent.com/WEIFENG2333/voxgate/main/scripts/install.ps1 | iex
```

The default install directory is:

```text
%LOCALAPPDATA%\Programs\voxgate
```

If `voxgate` is not found after install, add it to the current PowerShell session:

```powershell
$env:Path = "$env:LOCALAPPDATA\Programs\voxgate;$env:Path"
```

Persist it for future terminals:

```powershell
[Environment]::SetEnvironmentVariable("Path", "$env:LOCALAPPDATA\Programs\voxgate;" + [Environment]::GetEnvironmentVariable("Path", "User"), "User")
```

Or install to a directory already on PATH:

```powershell
$env:VOXGATE_INSTALL_DIR = "$env:USERPROFILE\bin"
irm https://raw.githubusercontent.com/WEIFENG2333/voxgate/main/scripts/install.ps1 | iex
```

Manual Windows install:

1. Download `voxgate_windows_amd64.zip` from GitHub Releases.
2. Extract it to a folder, for example `%LOCALAPPDATA%\Programs\voxgate`.
3. Keep `voxgate.exe` and the bundled `.dll` files in the same folder.
4. Add that folder to PATH.

Runtime dependencies:

| Platform | Command |
|---|---|
| Ubuntu/Debian | `sudo apt-get install -y ffmpeg libopus0` |
| macOS | `brew install ffmpeg opus` |
| Windows | `winget install Gyan.FFmpeg`; release zip includes `voxgate.exe` and libopus DLLs |

Run a health check:

```bash
voxgate doctor
```

### Install from source

Use this when you want the latest source or need to build for your own system:

```bash
sudo apt-get install -y ffmpeg libopus-dev pkg-config
go install github.com/WEIFENG2333/voxgate/cmd/voxgate@latest
```

macOS:

```bash
brew install ffmpeg opus pkg-config
go install github.com/WEIFENG2333/voxgate/cmd/voxgate@latest
```

Windows source builds require `ffmpeg`, `pkg-config`, a C compiler, and `libopus` through MSYS2 or vcpkg. Most Windows users should prefer the release zip or PowerShell installer.

Requirements for source builds:

- Go 1.22+
- `ffmpeg`
- `libopus` development package
- CGO enabled

## Quick Start

Transcribe an audio or video file:

```bash
voxgate transcribe speech.wav
voxgate transcribe video.mp4
```

Write OpenAI-style JSON:

```bash
voxgate transcribe meeting.mp3 --format json -o meeting.json
```

Generate coarse subtitle files:

```bash
voxgate transcribe lecture.mp4 --format srt -o lecture.srt
voxgate transcribe lecture.mp4 --format vtt -o lecture.vtt
```

Stream incremental events as NDJSON:

```bash
voxgate transcribe speech.wav --stream --format ndjson
```

Stream live PCM from another program:

```bash
# Linux ALSA microphone
arecord -f S16_LE -r 16000 -c 1 | voxgate transcribe - --input-format pcm16 --stream

# Any file or capture command through ffmpeg, output as 16 kHz mono PCM16
ffmpeg -re -i speech.wav -ac 1 -ar 16000 -f s16le - | voxgate transcribe - --input-format pcm16 --stream
```

Common capture commands:

```bash
# macOS: list devices, then replace ":0" with the microphone index you want
ffmpeg -f avfoundation -list_devices true -i ""
ffmpeg -f avfoundation -i ":0" -ac 1 -ar 16000 -f s16le - | voxgate transcribe - --input-format pcm16 --stream
```

```powershell
# Windows: list DirectShow devices, then replace audio="Microphone (...)"
ffmpeg -list_devices true -f dshow -i dummy
ffmpeg -f dshow -i audio="Microphone (Realtek Audio)" -ac 1 -ar 16000 -f s16le - | voxgate transcribe - --input-format pcm16 --stream
```

For stdin, `pcm16`/`raw` with `--stream` is sent to the ASR service as it arrives. `wav` stdin is still read to EOF first because container decoding needs `ffmpeg`.

## Local OpenAI-Compatible Server

Start the server:

```bash
voxgate serve --host 127.0.0.1 --port 8080 --auth-token local-token
```

Use it with the OpenAI Python SDK:

```python
from openai import OpenAI

client = OpenAI(base_url="http://127.0.0.1:8080/v1", api_key="local-token")

with open("speech.wav", "rb") as f:
    result = client.audio.transcriptions.create(
        model="voxgate",
        file=f,
        response_format="json",
    )

print(result.text)
```

Useful endpoints:

| Path | Method | Notes |
|---|---|---|
| `/v1/audio/transcriptions` | POST multipart | OpenAI-compatible transcription |
| `/v1/audio/translations` | POST | returns 400; translation is unsupported |
| `/v1/models` | GET | returns `voxgate` |
| `/v1/realtime` | WebSocket | OpenAI Realtime-style transcription subset |
| `/health` | GET | health check |
| `/metrics` | GET | minimal Prometheus text |

## Realtime Transcription

`voxgate` exposes an OpenAI Realtime-style WebSocket transcription endpoint:

```bash
voxgate serve --auth-token local-token
```

Connect to:

```text
ws://127.0.0.1:8080/v1/realtime
```

Supported client events:

| Event | Notes |
|---|---|
| `session.update` | accepted; returns `session.updated` |
| `input_audio_buffer.append` | append base64-encoded PCM16 audio |
| `input_audio_buffer.commit` | finish the current item, useful when the input stream stops |
| `input_audio_buffer.clear` | clear buffered audio |

The current compatibility subset expects 16 kHz mono PCM16 input. Audio appended to the buffer is sent upstream immediately; clients do not need to call `commit` to start receiving partial transcripts. When the upstream service finishes an utterance, `voxgate` emits `completed` and automatically opens a new upstream ASR item for later `append` events on the same WebSocket connection. Send `commit` when your capture source stops and you want the current item to settle.

Realtime result events use OpenAI-style names:

```json
{"type":"conversation.item.input_audio_transcription.delta","delta":"..."}
{"type":"conversation.item.input_audio_transcription.completed","transcript":"..."}
```

## CLI Reference

Commands:

```bash
voxgate transcribe <file|->
voxgate serve
voxgate doctor
voxgate auth
voxgate version
```

Common `transcribe` options:

| Option | Description |
|---|---|
| `--format text|json|verbose_json|srt|vtt|ndjson` | output format |
| `--output <file>` / `-o <file>` | write output to file |
| `--stream` | stream incremental output |
| `--input-format wav|pcm16|raw` | stdin input format |
| `--sample-rate <hz>` | raw PCM sample rate |
| `--request-timeout <duration>` | per-session timeout |
| `--chunk-duration <duration>` | long-file chunk size, default `300s` |
| `--no-punctuation` | disable punctuation |
| `--disable-three-pass` | disable the third recognition pass |
| `--realtime` | send audio at 20 ms pacing |

Default output format:

| Situation | Default |
|---|---|
| stdout is a terminal | `text` |
| stdout is piped or redirected | `json` |
| `--stream` is set | `ndjson` |

## Long Audio

The upstream backend behaves like an input-method ASR service, not a dedicated long-form batch transcription API. `voxgate` therefore splits long files automatically.

| Input length | Behavior |
|---|---|
| `<= 300s` | one WebSocket session |
| `> 300s` | serial 300-second chunks |

SRT/VTT timestamps are coarse chunk/segment ranges, not precise word-level subtitle timing.

## Configuration

Config priority:

```text
flags > environment variables > YAML config > defaults
```

Example config:

```yaml
credential_path: ~/.config/voxgate/credentials.json
asr:
  enable_punctuation: true
  enable_three_pass: true
  enable_two_pass: true
server:
  host: 127.0.0.1
  port: 8080
  auth_token: ""
  max_concurrency: 4
  request_timeout: 10m
```

Environment variables use the `VOXGATE_*` prefix, for example `VOXGATE_CREDENTIAL_PATH` and `VOXGATE_SERVER_AUTH_TOKEN`.

## Development

```bash
make build
make test
make vet
make doctor
```

Release maintainer checks:

```bash
make release-check
make release-snapshot
```

These targets require GoReleaser locally. Normal development does not.

Real endpoint probes are under `tests/e2e/` and require network access plus a working upstream backend.

## Distribution

Recommended user-facing distribution:

1. Tag a release: `git tag v0.2.7 && git push origin v0.2.7`
2. GitHub Actions builds native Linux, macOS, and Windows archives.
3. Users install with `scripts/install.sh` or download the archive from GitHub Releases.

`pip` is not a good primary channel for this project because `voxgate` is a Go CLI with CGO and runtime system dependencies (`ffmpeg`, `libopus`). `go install` remains useful for developers, while GitHub Releases are the cleanest path for normal users.

## More Docs

- [Protocol notes](docs/protocol.md)
- [Client strategy](docs/strategy.md)
- [Validation report](docs/validation.md)

## Known Limits

- The backend is non-public and unstable.
- Long-file chunking is serial, not parallel.
- Subtitle timing is coarse.
- `/v1/realtime` implements a transcription-focused subset, not the full OpenAI Realtime API.
- Release binaries still require system `ffmpeg`; Linux/macOS also need system `libopus`.
