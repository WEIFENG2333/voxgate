#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN="$ROOT/bin/voxgate"
AUDIO_DIR="$ROOT/tests/audio"
REPORT="${1:-$ROOT/tests/e2e/protocol_probe.jsonl}"
SOURCE="${VOXGATE_PROBE_SOURCE:-/data00/home/liangweifeng/qwen3-forced-aligner/samples/zh.wav}"

mkdir -p "$AUDIO_DIR" "$(dirname "$REPORT")"

if [[ ! -x "$BIN" ]]; then
  (cd "$ROOT" && make build >/dev/null)
fi

if [[ ! -f "$SOURCE" ]]; then
  echo "probe source missing: $SOURCE" >&2
  exit 2
fi

: > "$REPORT"

make_sample() {
  local name="$1"
  local seconds="$2"
  local out="$AUDIO_DIR/$name.wav"
  local list
  list="$(mktemp)"
  local duration
  duration="$(ffprobe -v error -show_entries format=duration -of default=nk=1:nw=1 "$SOURCE" | awk '{printf("%d", $1 == 0 ? 1 : $1)}')"
  local count=$(( seconds / duration + 2 ))
  for _ in $(seq 1 "$count"); do
    printf "file '%s'\n" "$SOURCE" >> "$list"
  done
  ffmpeg -hide_banner -loglevel error -f concat -safe 0 -i "$list" -t "$seconds" -ac 1 -ar 16000 "$out" -y
  rm -f "$list"
  printf "%s" "$out"
}

probe() {
  local label="$1"
  local file="$2"
  shift 2
  local tmp err start elapsed code bytes text
  tmp="$(mktemp)"
  err="$(mktemp)"
  start="$(date +%s)"
  set +e
  "$BIN" transcribe "$file" --format json --request-timeout 240s "$@" > "$tmp" 2> "$err"
  code=$?
  set -e
  elapsed=$(( $(date +%s) - start ))
  bytes="$(wc -c < "$tmp" | tr -d ' ')"
  text="$(tr -d '\n' < "$tmp" | head -c 200 | sed 's/"/\\"/g')"
  local error
  error="$(tr -d '\n' < "$err" | head -c 300 | sed 's/"/\\"/g')"
  printf '{"label":"%s","file":"%s","args":"%s","exit_code":%s,"elapsed_sec":%s,"stdout_bytes":%s,"stdout_head":"%s","stderr_head":"%s"}\n' \
    "$label" "$(basename "$file")" "$*" "$code" "$elapsed" "$bytes" "$text" "$error" >> "$REPORT"
  rm -f "$tmp" "$err"
}

short="$(make_sample zh_5s 5)"
medium="$(make_sample zh_60s 60)"
long="$(make_sample zh_10min 600)"

probe "short-fast" "$short"
probe "short-no-three-pass" "$short" --disable-three-pass
probe "medium-fast" "$medium"
probe "medium-realtime" "$medium" --realtime --request-timeout 180s
probe "long-single-session" "$long" --no-chunk --request-timeout 240s
probe "long-auto-chunk" "$long"

echo "protocol probe report: $REPORT"
