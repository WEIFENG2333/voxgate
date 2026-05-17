#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN="$ROOT/bin/ime-asr"
AUDIO_DIR="$ROOT/tests/audio"
mkdir -p "$AUDIO_DIR"

if [[ ! -x "$BIN" ]]; then
  (cd "$ROOT" && make build)
fi

SAMPLE="$AUDIO_DIR/tone.wav"
if [[ ! -f "$SAMPLE" ]]; then
  ffmpeg -hide_banner -loglevel error -f lavfi -i "sine=frequency=440:duration=1" -ac 1 -ar 16000 "$SAMPLE"
fi

"$BIN" doctor

echo "Local smoke sample generated at $SAMPLE"
if [[ -n "${IME_ASR_REAL_SAMPLE:-}" && -f "${IME_ASR_REAL_SAMPLE:-}" ]]; then
  ffmpeg -hide_banner -loglevel error -i "$IME_ASR_REAL_SAMPLE" -t 5 -ac 1 -ar 16000 "$AUDIO_DIR/real_5s.wav" -y
  ffmpeg -hide_banner -loglevel error -i "$AUDIO_DIR/real_5s.wav" "$AUDIO_DIR/real_5s.mp3" -y
  ffmpeg -hide_banner -loglevel error -i "$AUDIO_DIR/real_5s.wav" -c:a aac -strict experimental -b:a 64k "$AUDIO_DIR/real_5s.m4a" -y
  ffmpeg -hide_banner -loglevel error -f lavfi -i color=c=black:s=320x240:d=5 -i "$AUDIO_DIR/real_5s.wav" -shortest -c:v libx264 -c:a aac -strict experimental -b:a 64k "$AUDIO_DIR/real_5s.mp4" -y
  for f in "$AUDIO_DIR"/real_5s.{wav,mp3,m4a,mp4}; do
    echo "transcribing $(basename "$f")"
    "$BIN" transcribe "$f" --format json
  done
fi
echo "Real ASR e2e requires the non-public backend to be reachable:"
echo "  IME_ASR_REAL_SAMPLE=/path/to/chinese.wav make test-e2e"
