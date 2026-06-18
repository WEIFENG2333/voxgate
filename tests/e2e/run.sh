#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN="$ROOT/bin/voxgate"
AUDIO_DIR="$ROOT/tests/audio"
mkdir -p "$AUDIO_DIR"

if [[ ! -x "$BIN" ]]; then
  (cd "$ROOT" && make build)
fi

SAMPLE="$AUDIO_DIR/tone.wav"
if [[ ! -f "$SAMPLE" ]]; then
  ffmpeg -hide_banner -loglevel error -f lavfi -i "sine=frequency=440:duration=1" -ac 1 -ar 16000 "$SAMPLE"
fi

echo "running deterministic E2E harness with a protocol-faithful mock upstream"
(cd "$ROOT" && go run ./tests/e2e/harness.go --bin "$BIN" --audio "$AUDIO_DIR/zh_clean_6s.wav")

echo "running local doctor smoke"
"$BIN" doctor

echo "local smoke sample generated at $SAMPLE"
if [[ -n "${VOXGATE_REAL_SAMPLE:-}" && -f "${VOXGATE_REAL_SAMPLE:-}" ]]; then
  echo "running optional real-upstream E2E against $VOXGATE_REAL_SAMPLE"
  ffmpeg -hide_banner -loglevel error -i "$VOXGATE_REAL_SAMPLE" -t 5 -ac 1 -ar 16000 "$AUDIO_DIR/real_5s.wav" -y
  ffmpeg -hide_banner -loglevel error -i "$AUDIO_DIR/real_5s.wav" "$AUDIO_DIR/real_5s.mp3" -y
  ffmpeg -hide_banner -loglevel error -i "$AUDIO_DIR/real_5s.wav" -c:a aac -strict experimental -b:a 64k "$AUDIO_DIR/real_5s.m4a" -y
  ffmpeg -hide_banner -loglevel error -f lavfi -i color=c=black:s=320x240:d=5 -i "$AUDIO_DIR/real_5s.wav" -shortest -c:v libx264 -c:a aac -strict experimental -b:a 64k "$AUDIO_DIR/real_5s.mp4" -y
  for f in "$AUDIO_DIR"/real_5s.{wav,mp3,m4a,mp4}; do
    echo "transcribing $(basename "$f")"
    "$BIN" transcribe "$f" --format json
  done
fi
echo "Real ASR e2e requires the non-public backend to be reachable:"
echo "  VOXGATE_REAL_SAMPLE=/path/to/chinese.wav make test-e2e"
