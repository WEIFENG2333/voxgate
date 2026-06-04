#!/usr/bin/env python3
"""Realtime fidelity harness for voxgate.

Builds a "clock" audio clip where every character sits at a known timestamp
(per-character TTS, silence-trimmed, concatenated with fixed gaps), then streams
it through voxgate in realtime and compares what comes back:

  1. timestamp fidelity  - server-reported word start/end vs ground truth
  2. realtime lag        - how far behind realtime each result arrives
  3. per-char arrival    - when each character's text first shows up

Usage:
  python3 tests/realtime/rttest.py build   [--voice Flo] [--chars "..."] [--gap 0.08]
  python3 tests/realtime/rttest.py measure [--voxgate voxgate] [--format opus|pcm] [--json out.json]

`build` requires macOS `say` + ffmpeg/ffprobe. `measure` hits the real backend.
Artifacts (clock_audio.wav, clock_truth.json) are written next to this script.
"""
import argparse
import json
import os
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.abspath(__file__))
AUDIO = os.path.join(HERE, "clock_audio.wav")
TRUTH = os.path.join(HERE, "clock_truth.json")
SR = 16000


def run(cmd, **kw):
    return subprocess.run(cmd, check=True, capture_output=True, text=True, **kw)


def probe_duration(path):
    out = run(["ffprobe", "-v", "error", "-show_entries", "format=duration",
               "-of", "default=nw=1:nk=1", path]).stdout.strip()
    return float(out)


# Punctuation is not spoken; it inserts a natural pause (seconds) instead, so a
# multi-sentence script gets realistic phrase and sentence breaks.
PAUSE = {"，": 0.6, "、": 0.4, "；": 0.7, "：": 0.6, "。": 1.2, "！": 1.2,
         "？": 1.2, " ": 0.8, "/": 2.0}


def build(args):
    import tempfile
    chars = list(args.chars)
    workdir = tempfile.mkdtemp(prefix="rtclock-")

    def silence(seconds, name):
        path = os.path.join(workdir, name)
        run(["ffmpeg", "-y", "-f", "lavfi", "-i", f"anullsrc=r={SR}:cl=mono",
             "-t", str(seconds), path])
        return path

    gap_wav = silence(args.gap, "gap.wav")

    parts, truth, cursor = [], [], args.lead
    if args.lead > 0:
        parts.append(silence(args.lead, "lead.wav"))
    for i, ch in enumerate(chars):
        if ch in PAUSE:
            parts.append(silence(PAUSE[ch], f"p{i}.wav"))
            cursor += PAUSE[ch]
            continue
        txt = os.path.join(workdir, f"c{i}.txt")
        aiff = os.path.join(workdir, f"c{i}.aiff")
        raw = os.path.join(workdir, f"c{i}.wav")
        clip = os.path.join(workdir, f"t{i}.wav")
        # Feed the character via a UTF-8 file; passing it as an argv can be
        # mangled by the shell locale. afconvert reads CoreAudio output reliably
        # (ffmpeg trusts say's broken wav header and reads only a few ms).
        with open(txt, "w", encoding="utf-8") as f:
            f.write(ch)
        run(["say", "-v", args.voice, "-f", txt, "-o", aiff])
        run(["afconvert", "-f", "WAVE", "-d", f"LEI16@{SR}", "-c", "1", aiff, raw])
        # Trim leading/trailing silence so the clip tightly bounds the spoken char.
        trim = ("silenceremove=start_periods=1:start_silence=0.02:start_threshold=-40dB,"
                "areverse,"
                "silenceremove=start_periods=1:start_silence=0.02:start_threshold=-40dB,"
                "areverse")
        run(["ffmpeg", "-y", "-i", raw, "-af", trim, clip])
        dur = probe_duration(clip)
        truth.append({"index": len(truth), "char": ch,
                      "start_ms": round(cursor * 1000),
                      "end_ms": round((cursor + dur) * 1000)})
        parts.append(clip)
        parts.append(gap_wav)
        cursor += dur + args.gap

    concat_list = os.path.join(workdir, "list.txt")
    with open(concat_list, "w") as f:
        for p in parts:
            f.write(f"file '{p}'\n")
    run(["ffmpeg", "-y", "-f", "concat", "-safe", "0", "-i", concat_list,
         "-ar", str(SR), "-ac", "1", AUDIO])

    meta = {"voice": args.voice, "gap_s": args.gap, "lead_s": args.lead,
            "sample_rate": SR, "total_ms": round(cursor * 1000),
            "text": "".join(chars), "chars": truth}
    with open(TRUTH, "w") as f:
        json.dump(meta, f, ensure_ascii=False, indent=2)
    print(f"wrote {AUDIO} ({probe_duration(AUDIO):.2f}s) and {TRUTH}")
    for c in truth:
        print(f"  [{c['index']:2d}] {c['char']}  {c['start_ms']:>6}–{c['end_ms']:>6} ms")


def measure(args):
    if not (os.path.exists(AUDIO) and os.path.exists(TRUTH)):
        sys.exit("missing clock artifacts; run `build` first")
    meta = json.load(open(TRUTH))
    pipeline = (
        f"ffmpeg -loglevel quiet -re -i {AUDIO} -ar {SR} -ac 1 -f s16le - | "
        f"{args.voxgate} transcribe --input-format pcm16 --stream --format ndjson -"
    )
    env = dict(os.environ)
    env["VOXGATE_ASR_AUDIO_FORMAT"] = "opus" if args.format == "opus" else "pcm"

    events = []
    t0 = time.monotonic()
    proc = subprocess.Popen(["bash", "-c", pipeline], stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL, text=True, env=env)
    for line in proc.stdout:
        line = line.strip()
        if not line:
            continue
        arr = time.monotonic() - t0
        try:
            ev = json.loads(line)
        except json.JSONDecodeError:
            continue
        ev["_arr_s"] = arr
        events.append(ev)
    proc.wait()
    report(meta, events, args)


def report(meta, events, args):
    transcripts = [e for e in events if e.get("type", "").startswith("transcript.text")]
    done = next((e for e in events if e.get("type") == "transcript.text.done"), None)
    final_text = (done or {}).get("text", "")

    # Realtime lag: how far behind realtime each result is, with the unknown
    # audio-start offset removed by anchoring to the least-delayed event.
    pts = [(e["_arr_s"] * 1000, e.get("audio_end_ms")) for e in transcripts
           if e.get("audio_end_ms") is not None]
    lag = None
    if pts:
        baseline = min(arr - ae for arr, ae in pts)
        lags = [(arr - ae) - baseline for arr, ae in pts]
        lag = {"max_ms": round(max(lags)), "mean_ms": round(sum(lags) / len(lags)),
               "samples": len(lags)}

    # Per-char arrival: wall-clock (relative) when each ground-truth char first
    # appears in a snapshot, vs when it finished being spoken.
    per_char = []
    for c in meta["chars"]:
        need = c["index"] + 1
        hit = next((e for e in transcripts
                    if len(e.get("snapshot", e.get("text", ""))) >= need), None)
        if hit:
            arr_ms = hit["_arr_s"] * 1000
            per_char.append({"char": c["char"], "spoken_end_ms": c["end_ms"],
                             "first_seen_arr_ms": round(arr_ms)})

    # Timestamp fidelity: server-reported word start vs ground truth. Recognition
    # may drop, merge, or reformat tokens (e.g. 二三->"23"), so match each word to
    # the nearest ground-truth char start rather than by index. end_time from the
    # backend is an unreliable utterance estimate and is not scored.
    def words_of(ev):
        w = []
        for r in ev.get("results", []) or []:
            for alt in r.get("alternatives", []) or []:
                w.extend(alt.get("words", []) or [])
        return w
    richest = max((words_of(e) for e in transcripts), key=len, default=[])
    starts = [w["start_time"] * 1000 for w in richest if w.get("start_time", 0) > 0]
    gt = [c["start_ms"] for c in meta["chars"]]
    fid = None
    if starts:
        errs = [min(abs(s - g) for g in gt) for s in starts]
        fid = {"mean_abs_start_err_ms": round(sum(errs) / len(errs)),
               "max_abs_start_err_ms": round(max(errs)), "matched_words": len(errs)}

    # Timeline: when each result arrived (wall clock) and the audio position it
    # covered, so behaviour across pauses is visible.
    timeline = [{"arr_s": round(e["_arr_s"], 2),
                 "audio_end_ms": e.get("audio_end_ms"),
                 "type": e.get("type", "").rsplit(".", 1)[-1],
                 "snapshot": e.get("snapshot", e.get("text", ""))}
                for e in transcripts]

    first = transcripts[0]["_arr_s"] if transcripts else None
    out = {
        "audio_format": "opus" if args.format == "opus" else "pcm",
        "ground_truth_text": meta["text"],
        "recognized_text": final_text,
        "first_token_arr_s": round(first, 3) if first else None,
        "total_events": len(events),
        "realtime_lag": lag,
        "timestamp_fidelity": fid,
        "per_char_arrival": per_char,
        "timeline": timeline,
    }
    print(f"format={out['audio_format']}  truth={meta['text']!r}")
    print(f"recognized={final_text!r}")
    print(f"first_token={out['first_token_arr_s']}s  realtime_lag={lag}  fidelity={fid}")
    print("timeline (arrival_s | audio_pos_s | snapshot):")
    for t in timeline:
        ae = f"{t['audio_end_ms']/1000:.2f}" if t["audio_end_ms"] is not None else " - "
        print(f"  {t['arr_s']:6.2f} | {ae:>5} | {t['snapshot']}")
    if args.json:
        with open(args.json, "w") as f:
            json.dump(out, f, ensure_ascii=False, indent=2)
        print(f"\nwrote {args.json}", file=sys.stderr)


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("build")
    b.add_argument("--voice", default="Tingting")
    b.add_argument("--chars", default="今天天气很好我们一起去公园散步")
    b.add_argument("--gap", type=float, default=0.08)
    b.add_argument("--lead", type=float, default=0.3)
    b.set_defaults(fn=build)
    m = sub.add_parser("measure")
    m.add_argument("--voxgate", default="voxgate")
    m.add_argument("--format", choices=["opus", "pcm"], default="opus")
    m.add_argument("--json", default="")
    m.set_defaults(fn=measure)
    args = ap.parse_args()
    args.fn(args)


if __name__ == "__main__":
    main()
