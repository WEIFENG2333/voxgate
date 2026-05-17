# Client Strategy

The backend behaves like an input-method ASR service, not a general long-form batch transcription API. The client strategy is therefore deliberately conservative.

## Confirmed

- Device bootstrap and token retrieval are real and repeatable.
- WebSocket protobuf field numbers `2,3,5,6,7,8,9` are required.
- Audio must be 16 kHz mono PCM encoded to 20 ms Opus frames.
- `StartTask` and `StartSession` are separate protobuf round trips.
- Recognition JSON has interim, definite, and final variants.
- VAD text can reset between utterance segments; previous text must be preserved.

## Working Limits

Based on local probes, a single session handles short utterances and roughly one-minute file inputs. A 10-minute single session can close normally without text. That is treated as a backend/session limit until disproven.

## Default Policy

| Input | Policy |
|---|---|
| short file | one session, fast upload |
| long file | automatic 55 s chunks, one session per chunk |
| realtime | one session, 20 ms pacing |
| HTTP non-stream | same chunking policy as CLI |
| HTTP stream | single session for now; long-stream chunked SSE is not marked stable |

## Why Not Always Realtime

Realtime pacing is useful for microphone input but makes a 10-minute file take at least 10 minutes. The observed endpoint accepts faster-than-realtime uploads for moderate chunks, so chunked fast upload is the best default for file transcription.

## Probe Script

Run:

```bash
make build
tests/e2e/protocol_probe.sh
```

Optional source:

```bash
IME_ASR_PROBE_SOURCE=/path/to/chinese.wav tests/e2e/protocol_probe.sh report.jsonl
```

The report records exit code, elapsed time, and whether text was returned for each mode. This is the source of truth for changing chunk size, pacing, or retry policy.

`long-single-session` is intentionally included as a research control. If it starts passing consistently across different real long recordings, the default chunk threshold can be revisited.
