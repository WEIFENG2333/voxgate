# voxgate — Agent Guide

`voxgate` is a Go CLI + local OpenAI-compatible server that wraps ByteDance's
non-public Doubao input-method ASR backend. **Learning/research only.** This
file is the fast on-ramp for the next agent: architecture, flows, exposed
surface, conventions, and the non-obvious knowledge that isn't visible in code.

`AGENT.md` and `AGENTS.md` are symlinks to this file.

## Working rules (read first)

- **Never `git commit`/`push` without the user's explicit go-ahead.** Build, test,
  and edit freely; just don't commit until told to.
- Reply to the user in **Chinese**.
- **No reverse-engineering provenance in code comments** — never write "来自X.apk
  逆向", doc/line references, or "对齐APK". Describe behavior functionally. (Docs
  under `docs/` may describe the protocol; code comments may not cite sources.)
- **Never commit** the APK, `docs/reverse-src/`, or credential files (all
  gitignored).
- Commit messages end with: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`
- Comments explain **why / the non-obvious only** — do not comment every function.
- Investigate with tools before asking; when you fix a bug, check for siblings.

## Architecture (layering)

```
cmd/voxgate (CLI)  ─┐
internal/server     ├─> internal/transcription.Service ─> internal/transcriber.Runner ─> internal/asr.Client ─> internal/proto ─> upstream WS
                    │        (app config, hotwords)            (chunking, encoder pick)        (wire protocol)      (protobuf frames)
internal/audio ─────┘  ffmpeg decode · LiveSource (stdin) · PCM/Opus encoders
internal/output       render text/json/verbose_json/srt/vtt/ndjson/protocol
internal/config·paths credential paths, flags>env>yaml>defaults
```

## Main flows

- **File**: decode → 16 kHz mono PCM → chunk if > 30s (serial) → one WS session per chunk → concat.
- **Live (mic/stdin)**: `--input-format pcm16|raw --stream -` → `audio.LiveSource` → single WS session, frames sent as they arrive (no artificial pacing; `Realtime` is forced false for live, see `transcribe.go`).
- **Server**: multipart upload → same `transcription.Service`.

## Upstream protocol (full detail in `docs/protocol.md`)

- WS: `wss://frontier-audio-ime-ws.doubao.com/ocean/api/v1/ws` (default). A `…-quic…` host exists but it is only a transport; same model, same result. Our client is WS-over-TCP only.
- Handshake: `StartTask` → `StartSession`(JSON) → `TaskRequest`* (20 ms frames) → `FinishSession`.
- Two distinct credentials: `creds.Token` (ASR auth) vs `sami_token` (context HTTP APIs, from `get_config`).
- `audio_info.format`: `raw` (PCM) or `speech_opus` (Opus).

## Event flow (`asr.Event` → CLI/SSE/Realtime)

`task.started` · `session.started` · `speech.started` (VAD) · `transcript.text.delta` (append-only) · `transcript.text.update` (snapshot revised) · `transcript.segment.stable` (a VAD-finished phase) · `transcript.text.done` (final, immutable). The threepass `nonstream_result` is the most accurate final; the parser prefers it over `vad_finished`.

## Build & test

- `make build` → default build, **PCM upstream**. `-tags opus` → Opus (needs `libopus`/pkg-config).
- `make test` / `make vet`. `make probe` / `make test-e2e` (e2e).
- CI matrix: ubuntu, macos-14, macos-15-intel, windows — runs `vet` / `test` / `test -race` / `build` on the **default (PCM) build** (no `-tags opus`; libopus is installed but the commands don't pass the tag). `gofmt` is enforced on Linux (`gofmt -l` must be empty).

## Gotchas / implicit knowledge (the part you can't read off the code)

1. **Opus is a build tag.** Default build (no `-tags opus`) → Opus unavailable → `audio_format=auto` falls back to PCM (`raw`). See `audio/opus_enabled.go` vs `opus_disabled.go`.
2. **macOS SIGKILL (exit 137) when reinstalling.** `cp` over an existing CGO binary reuses the inode → stale code-signature cache → kernel kills the new process with no output. Fix: `rm` then `cp`, or use `install` (`make install` already does). Don't misdiagnose as OOM/quarantine.
3. **Live mic lag accumulates with ffmpeg.** `ffmpeg -f avfoundation` falls progressively behind realtime (≈0.3s → 0.7s+ over 10s) — this is the #1 cause of "laggy mic", not the server or our code. Use the native capture `tests/realtime/miccap.swift` (build to `bin/miccap`): constant ≈0.26s, no drift. Recommended: `bin/miccap | voxgate transcribe --input-format pcm16 --stream -`.
4. **Do NOT send `end_smooth_window_ms`.** A fixed VAD end-of-speech window truncates faster-than-realtime file uploads (drops trailing segments). The server default handles both file and live pacing. (Deliberately omitted in `asr/client.go`.)
5. **StartSession `extra`** carries correction/format flags (`strong_ddc`, `use_twopass_retry`, `disable_user_words`, `context`). `strong_ddc`'s upstream default depends on an obfuscated `r.x()` helper that could not be resolved statically — verify empirically before changing it.
6. **`context` ≠ personalization.** It is base64(`{chat, ime_info}`) = conversational *before-context*. For one-shot CLI `chat` is empty; `--prompt` is injected as one `user_input` entry. Real vocabulary personalization is the **`user_words` upload** (`--hotwords`).
7. **`user_words` is append/accumulate** (server dedups); there is no "clear" API. Reported words are cached per device in `<credential>.hotwords.json`.
8. **Wave encryption** (ECDH + HKDF-SHA256 + ChaCha20, `keyhub` handshake) wraps the context HTTP endpoints (`asr/wave.go`).
9. **Credentials** live at `~/Library/Application Support/voxgate/credentials.json` (macOS). Delete to regenerate a fresh device identity. The token prefix is a shared app key — only `device_id`/`install_id` are per-device.

## Hidden test tooling

- `tests/realtime/rttest.py` — realtime fidelity harness. `build` makes a per-character TTS "clock" clip with known timestamps; `measure` streams it and reports realtime lag, timestamp fidelity, per-char arrival, and a timeline. `build` needs macOS `say` + ffmpeg; `measure` needs the live backend. **Use voice `Tingting`** — `Flo`/`Eddy` are English homonyms that synthesize ~16 ms of nothing for Chinese.
- `tests/realtime/miccap.swift` — low-latency native mic capture (see gotcha 3).
- `tests/e2e/` — deterministic mock-upstream E2E (`make test-e2e`), real-endpoint probes (`make probe`), OpenAI Python/Go SDK smoke clients.

## Exposed surface

- CLI: `transcribe` · `serve` · `doctor` · `auth` · `version` · `update`.
- Server: `POST /v1/audio/transcriptions` (+ `stream=true` SSE) · `GET /v1/models` · `WS /v1/realtime` · `GET /health`.
- Config precedence: flags > `VOXGATE_*` env > YAML > defaults.

## Docs

- `docs/protocol.md` — wire protocol, StartSession params, personalization/`user_words`, Wave envelope.
- `docs/strategy.md` — chunking strategy and limits.
- `docs/validation.md` — probe/validation results.
