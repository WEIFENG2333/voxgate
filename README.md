# voxgate

`voxgate` is a Chinese speech-to-text CLI and local OpenAI-compatible transcription server.

中文：`voxgate` 是一个中文语音转文字工具，可以转写本地音频/视频，也可以启动本地 OpenAI 兼容接口，方便其它程序调用。

## Notice

This project wraps a non-public input-method ASR backend. It is for learning and research only, must not be used commercially, and may stop working if the upstream service changes or rejects requests.

本项目使用非公开输入法 ASR 后端，仅供学习研究，不得商用；上游服务可能随时变更、封禁或失效。

## What You Can Do

- Transcribe local audio and video files.
- Generate text, JSON, NDJSON, SRT, or VTT output.
- Stream live PCM audio from another program.
- Run a local `/v1/audio/transcriptions` endpoint for OpenAI SDK clients.
- Run a local `/v1/realtime` WebSocket endpoint for realtime transcription.

## Install

### Linux and macOS

Install the latest release:

```bash
curl -fsSL https://raw.githubusercontent.com/WEIFENG2333/voxgate/main/scripts/install.sh | sh
```

The installer writes to `~/.local/bin` by default. To install somewhere else:

```bash
curl -fsSL https://raw.githubusercontent.com/WEIFENG2333/voxgate/main/scripts/install.sh | VOXGATE_INSTALL_DIR=/usr/local/bin sh
```

Runtime dependencies:

```bash
# Ubuntu/Debian
sudo apt-get install -y ffmpeg libopus0

# macOS
brew install ffmpeg opus
```

### Windows

Install `ffmpeg`:

```powershell
winget install Gyan.FFmpeg
```

Install `voxgate`:

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

To persist it for future terminals:

```powershell
[Environment]::SetEnvironmentVariable("Path", "$env:LOCALAPPDATA\Programs\voxgate;" + [Environment]::GetEnvironmentVariable("Path", "User"), "User")
```

### Update

Run the same install command again to upgrade. If the installed version is already current, the installer exits without downloading.

Check for a newer release:

```bash
voxgate version --check
voxgate update
```

`voxgate update` does not update silently on startup. It checks the latest GitHub release and prints the exact install command to run when an update is available. This keeps normal CLI startup offline, fast, and predictable.

### Verify

```bash
voxgate doctor
```

`doctor` checks `ffmpeg`, Opus support, and cached credentials.

## Quick Start

Transcribe an audio or video file:

```bash
voxgate transcribe speech.wav
voxgate transcribe video.mp4
```

Write JSON:

```bash
voxgate transcribe meeting.mp3 --format json -o meeting.json
```

Generate subtitles:

```bash
voxgate transcribe lecture.mp4 --format srt -o lecture.srt
voxgate transcribe lecture.mp4 --format vtt -o lecture.vtt
```

Stream incremental events:

```bash
voxgate transcribe speech.wav --stream --format ndjson
```

Boost project-specific words before transcription:

```bash
voxgate transcribe meeting.wav --hotwords "Claude Code,Anthropic,VoxGate"
VOXGATE_ASR_HOTWORDS="Claude Code,Anthropic,VoxGate" voxgate transcribe meeting.wav
```

Hotwords are reported best-effort to the upstream personal-word context service
using the same cached device identity. A reporting failure is shown as a warning
and does not stop transcription. Successfully reported words are cached per
device, so later runs only report newly added words.

Streaming NDJSON output uses `speech.started` for VAD start,
`transcript.text.delta` for append-only text deltas,
`transcript.text.update` when the editable transcript snapshot is revised,
`transcript.segment.stable` when upstream reports a stable recognition phase,
and `transcript.text.done` with the immutable full transcript when the input
source ends. In `transcript.segment.stable`, `text` and `snapshot` both carry
the upstream stable full transcript view.

## Live Audio

For live input, pipe 16 kHz mono PCM16 into stdin:

```bash
voxgate transcribe - --input-format pcm16 --stream
```

Examples:

```bash
# Linux ALSA microphone
arecord -f S16_LE -r 16000 -c 1 | voxgate transcribe - --input-format pcm16 --stream

# Any file or capture command through ffmpeg, paced like playback
ffmpeg -re -i speech.wav -ac 1 -ar 16000 -f s16le - | voxgate transcribe - --input-format pcm16 --stream
```

macOS:

```bash
ffmpeg -f avfoundation -list_devices true -i ""
ffmpeg -f avfoundation -i ":0" -ac 1 -ar 16000 -f s16le - | voxgate transcribe - --input-format pcm16 --stream
```

Windows:

```powershell
ffmpeg -list_devices true -f dshow -i dummy
ffmpeg -f dshow -i audio="Microphone (Realtek Audio)" -ac 1 -ar 16000 -f s16le - | voxgate transcribe - --input-format pcm16 --stream
```

For stdin, `pcm16`/`raw` with `--stream` is sent upstream as it arrives. `wav` stdin is still read to EOF first because container decoding needs `ffmpeg`.

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

Endpoints:

| Path | Method | Notes |
|---|---|---|
| `/v1/audio/transcriptions` | POST multipart | OpenAI-compatible transcription |
| `/v1/audio/translations` | POST | returns 400; translation is unsupported |
| `/v1/models` | GET | returns `voxgate` |
| `/v1/realtime` | WebSocket | OpenAI Realtime-style transcription subset |
| `/health` | GET | health check |

Errors use OpenAI-style JSON: `{"error":{"message":"...","type":"...","code":"..."}}`.
When `stream=true`, `/v1/audio/transcriptions` returns server-sent events:
`transcript.text.delta` and `transcript.text.done`.

## Realtime WebSocket

`/v1/realtime` accepts OpenAI-style transcription events over WebSocket:

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
| `input_audio_buffer.append` | append base64-encoded 16 kHz mono PCM16 |
| `input_audio_buffer.commit` | finish the current item |
| `input_audio_buffer.clear` | clear buffered audio |

Result events:

```json
{"type":"conversation.item.input_audio_transcription.delta","delta":"..."}
{"type":"conversation.item.input_audio_transcription.completed","transcript":"..."}
```

Audio appended to the buffer is sent upstream immediately. Clients do not need to call `commit` to receive partial transcripts; call `commit` when the capture source stops and you want the current item to settle.

## CLI Reference

Commands:

```bash
voxgate transcribe <file|->
voxgate serve
voxgate doctor
voxgate auth
voxgate version
voxgate update
```

Common `transcribe` options:

| Option | Description |
|---|---|
| `--format text|json|verbose_json|srt|vtt|ndjson` | output format |
| `--output <file>` / `-o <file>` | write output to file |
| `--stream` | stream incremental output |
| `--hotwords <words>` | comma-separated personal words to report before transcription |
| `--input-format wav|pcm16|raw` | stdin input format |
| `--sample-rate <hz>` | raw PCM sample rate |
| `--request-timeout <duration>` | per-session timeout |
| `--chunk-duration <duration>` | long-file chunk size, default `300s` |
| `--trace-asr <file>` | write raw upstream ASR WebSocket frames as NDJSON |
| `--no-punctuation` | disable punctuation |
| `--disable-three-pass` | disable the third recognition pass |
| `--realtime` | send file audio at 20 ms pacing |

Default output format:

| Situation | Default |
|---|---|
| stdout is a terminal | `text` |
| stdout is piped or redirected | `json` |
| `--stream` is set | `ndjson` |

## Configuration

Config priority:

```text
flags > environment variables > YAML config > defaults
```

Example config:

```yaml
# Optional. Defaults to the OS user config directory:
# Linux: ~/.config/voxgate/credentials.json
# macOS: ~/Library/Application Support/voxgate/credentials.json
# Windows: %AppData%\voxgate\credentials.json
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

Logging is for local debugging and troubleshooting. Use `-v` or `--log-level debug` for realtime lifecycle details, `--json-logs` for structured logs, and `-q` to keep only errors.

For protocol debugging, `--trace-asr <file>` records raw upstream WebSocket protobuf frames. The trace includes credentials and audio payloads, so keep it local and do not share it publicly.

## Long Audio

The upstream backend behaves like an input-method ASR service, not a dedicated long-form batch transcription API. `voxgate` splits long files automatically.

| Input length | Behavior |
|---|---|
| `<= 300s` | one WebSocket session |
| `> 300s` | serial 300-second chunks |

SRT/VTT timestamps are coarse chunk/segment ranges, not precise word-level subtitle timing.

## Source Builds

Most users should install release binaries. Build from source only if you need the latest code or a custom build.

Requirements:

- Go 1.22+
- CGO enabled
- `ffmpeg`
- `pkg-config`
- `libopus` development package

Linux:

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

## Development

```bash
make build
make test
make vet
make doctor
```

Real endpoint probes are under `tests/e2e/` and require network access plus a working upstream backend.

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
