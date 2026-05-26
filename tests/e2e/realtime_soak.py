#!/usr/bin/env python3
import argparse
import base64
import json
import os
import queue
import signal
import subprocess
import sys
import threading
import time

import websocket


BYTES_PER_SECOND = 16000 * 2


def parse_args():
    p = argparse.ArgumentParser(description="Realtime WebSocket soak tester for voxgate.")
    p.add_argument("--url", default="ws://127.0.0.1:8080/v1/realtime")
    p.add_argument("--token", default="")
    p.add_argument("--audio", required=True)
    p.add_argument("--seconds", type=float, default=0, help="limit source duration; 0 means full file")
    p.add_argument("--loop-source", action="store_true", help="loop input media until --seconds is reached")
    p.add_argument("--chunk-ms", type=int, default=100)
    p.add_argument("--mode", choices=["realtime", "burst"], default="realtime")
    p.add_argument("--commit-at-end", action="store_true")
    p.add_argument("--quiet-seconds", type=float, default=5)
    p.add_argument("--settle-timeout", type=float, default=30, help="max seconds to wait for completed after final commit")
    p.add_argument("--output-jsonl", default="")
    return p.parse_args()


def ffmpeg_pcm(audio, seconds, loop_source):
    cmd = [
        "ffmpeg",
        "-hide_banner",
        "-loglevel",
        "error",
        "-re",
    ]
    if loop_source:
        cmd += ["-stream_loop", "-1"]
    cmd += [
        "-i",
        audio,
    ]
    if seconds > 0:
        cmd += ["-t", str(seconds)]
    cmd += ["-vn", "-ac", "1", "-ar", "16000", "-f", "s16le", "-"]
    return subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, preexec_fn=os.setsid)


def percentile(values, pct):
    if not values:
        return None
    values = sorted(values)
    idx = int(round((len(values) - 1) * pct))
    return values[idx]


def main():
    args = parse_args()
    headers = []
    if args.token:
        headers.append("Authorization: Bearer " + args.token)
    ws = websocket.create_connection(args.url, header=headers, timeout=10)
    ws.settimeout(None)
    start = time.monotonic()
    events = []
    event_q = queue.Queue()
    stop = threading.Event()
    out = open(args.output_jsonl, "w") if args.output_jsonl else None

    def record(ev):
        events.append(ev)
        if out:
            out.write(json.dumps(ev, ensure_ascii=False) + "\n")
            out.flush()

    def observe(raw):
        try:
            ev = json.loads(raw)
        except Exception:
            ev = {"raw": raw}
        ev["_t"] = round(time.monotonic() - start, 3)
        record(ev)

    def recv_loop():
        while not stop.is_set():
            try:
                raw = ws.recv()
            except Exception as e:
                event_q.put({"recv_error": str(e), "_t": round(time.monotonic() - start, 3)})
                return
            event_q.put(raw)

    recv_thread = threading.Thread(target=recv_loop, daemon=True)
    recv_thread.start()
    ws.send(json.dumps({"type": "session.update", "session": {"type": "transcription"}}))

    ff = ffmpeg_pcm(args.audio, args.seconds, args.loop_source)
    chunk_bytes = max(1, BYTES_PER_SECOND * args.chunk_ms // 1000)
    sent = 0
    send_errors = []
    try:
        while True:
            while True:
                try:
                    item = event_q.get_nowait()
                except queue.Empty:
                    break
                if isinstance(item, str):
                    observe(item)
                else:
                    record(item)
            data = ff.stdout.read(chunk_bytes)
            if not data:
                break
            try:
                ws.send(json.dumps({"type": "input_audio_buffer.append", "audio": base64.b64encode(data).decode()}))
                sent += len(data)
            except Exception as e:
                send_errors.append({"_t": round(time.monotonic() - start, 3), "error": str(e)})
                break
            if args.mode == "realtime":
                time.sleep(args.chunk_ms / 1000)
        ff_stderr = ff.stderr.read().decode(errors="ignore")
        ff.wait()
        completed_before_commit = 0
        if args.commit_at_end:
            completed_before_commit = sum(
                1 for e in events if e.get("type") == "conversation.item.input_audio_transcription.completed"
            )
            try:
                ws.send(json.dumps({"type": "input_audio_buffer.commit"}))
            except Exception as e:
                send_errors.append({"_t": round(time.monotonic() - start, 3), "error": "commit: " + str(e)})
        deadline = time.monotonic() + (args.settle_timeout if args.commit_at_end else args.quiet_seconds)
        while time.monotonic() < deadline:
            try:
                item = event_q.get(timeout=0.2)
            except queue.Empty:
                continue
            if isinstance(item, str):
                observe(item)
            else:
                record(item)
            if args.commit_at_end:
                completed_now = sum(
                    1 for e in events if e.get("type") == "conversation.item.input_audio_transcription.completed"
                )
                if completed_now > completed_before_commit:
                    break
    finally:
        stop.set()
        try:
            ws.close()
        except Exception:
            pass
        if ff.poll() is None:
            os.killpg(os.getpgid(ff.pid), signal.SIGTERM)
        if out:
            out.close()

    deltas = [e for e in events if e.get("type") == "conversation.item.input_audio_transcription.delta"]
    completed = [e for e in events if e.get("type") == "conversation.item.input_audio_transcription.completed"]
    committed = [e for e in events if e.get("type") == "input_audio_buffer.committed"]
    failed = [e for e in events if e.get("type") == "conversation.item.input_audio_transcription.failed"]
    errors = [e for e in events if e.get("type") == "error"]
    delta_gaps = [deltas[i]["_t"] - deltas[i - 1]["_t"] for i in range(1, len(deltas))]
    item_ids = sorted({e.get("item_id", "") for e in deltas + completed if e.get("item_id")})
    summary = {
        "audio": args.audio,
        "loop_source": args.loop_source,
        "mode": args.mode,
        "chunk_ms": args.chunk_ms,
        "sent_audio_s": round(sent / BYTES_PER_SECOND, 3),
        "wall_s": round(time.monotonic() - start, 3),
        "events": len(events),
        "deltas": len(deltas),
        "completed": len(completed),
        "committed": len(committed),
        "items": len(item_ids),
        "first_delta_s": deltas[0]["_t"] if deltas else None,
        "last_delta_s": deltas[-1]["_t"] if deltas else None,
        "max_delta_gap_s": max(delta_gaps) if delta_gaps else None,
        "p95_delta_gap_s": percentile(delta_gaps, 0.95),
        "gaps_over_2s": sum(1 for x in delta_gaps if x > 2),
        "errors": len(errors),
        "failed": len(failed),
        "send_errors": send_errors,
        "completed_items": [
            {"t": e.get("_t"), "item_id": e.get("item_id"), "chars": len(e.get("transcript", ""))}
            for e in completed[:20]
        ],
        "ffmpeg_stderr_tail": ff_stderr[-500:],
    }
    print(json.dumps(summary, ensure_ascii=False))
    if send_errors or errors or failed:
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
