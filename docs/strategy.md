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

Based on local probes, a single session handles short utterances and file inputs up to at least 480 seconds for the repeated Chinese probe sample. 540 and 570 second single-session probes closed without text, and a 10-minute single session also failed. That is treated as a backend/session limit until disproven.

## Default Policy

| Input | Policy |
|---|---|
| file <= 300 s | one session, fast upload |
| file > 300 s | automatic 300 s chunks, one session per chunk |
| realtime | one session, 20 ms pacing |
| HTTP non-stream | same chunking policy as CLI |
| HTTP stream | single session for now; long-stream chunked SSE is not marked stable |

The chunker is currently time based: after ffmpeg normalization to 16 kHz mono PCM, it slices by PCM byte offset and aligns each boundary to a 20 ms Opus frame. It is not silence-aware, does not run VAD before splitting, and does not add overlap.

Chunked file transcription is serial. The client opens one WebSocket session for chunk 0, waits for its final result, then opens the next WebSocket session for chunk 1. Chunks are not sent concurrently, and a multi-chunk file is not kept inside one long WebSocket session. This keeps ordering, retry handling, and backend throttling behavior predictable.

Segment timestamps returned for chunked long files are chunk ranges in the original file timeline, for example `0-300`, `300-600`. They are real offsets for the chunk boundaries, but they are not word-level or utterance-level ASR timestamps. SRT/VTT output is therefore usable as coarse captions today, not as precision subtitle alignment.

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
VOXGATE_PROBE_SOURCE=/path/to/chinese.wav tests/e2e/protocol_probe.sh report.jsonl
```

The report records exit code, elapsed time, and whether text was returned for each mode. This is the source of truth for changing chunk size, pacing, or retry policy.

`long-single-session` is intentionally included as a research control. If it starts passing consistently across different real long recordings, the default chunk threshold can be revisited.
