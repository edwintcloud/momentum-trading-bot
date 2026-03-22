#!/usr/bin/env python3
"""Analyze entry rejects from system logs to find blocked winners."""
import json
import sys

logfile = sys.argv[1] if len(sys.argv) > 1 else "logs/system_logs_20260321_045002.jsonl"

new_filters = [
    "premature-breakout-entry", "flush-thin-volume", "premarket-short-thin-tape",
    "premarket-short-shallow-collapse", "short-opportunity-exhausted", "flush-and-run",
]

rejects = []
with open(logfile) as f:
    for line in f:
        try:
            d = json.loads(line)
            if d.get("reason") in new_filters and d.get("event") == "entry-rejected":
                rejects.append(d)
        except Exception:
            pass

print(f"Total rejects from new filters: {len(rejects)}")
print()

by_symbol = {}
for r in rejects:
    sym = r.get("symbol", "?")
    if sym not in by_symbol:
        by_symbol[sym] = []
    by_symbol[sym].append(r)

for sym, rlist in sorted(by_symbol.items()):
    for r in rlist[:3]:
        ts = r.get("timestamp", "")
        reason = r.get("reason", "")
        score = r.get("score", 0)
        vr = r.get("volumeRate", 0)
        bp = r.get("breakoutPct", 0)
        dh = r.get("distanceFromHighPct", 0)
        print(f"  {sym} @ {ts} reason={reason} score={score:.1f} vr={vr:.2f} breakoutPct={bp:.2f} distHighPct={dh:.1f}")
