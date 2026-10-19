#!/usr/bin/env python3
# Reference M11 numbers from the frozen review-1016 extract (calls.jsonl, events.jsonl), its own definitions:
# main = sub is None, $ = in+cr+cw+out list price, weekday = UTC date Mon-Fri, compaction = k "c".
# Bounds and stamps are compared as UTC times, per-call numbers are 0 with no calls, nothing is rounded: as in
# internal/drivers, so the same window selects the same calls.
# Known difference: a request copied into several transcripts (resumed sessions) keeps the first copy extract.py
# walks to; the store keeps the largest usage. 10-11 has one (prompt 0 vs 280K): 0.01% on the P4 window.
# usage: ref.py <cost dir> <from YYYY-MM-DD> <to RFC3339 exclusive>
# P4 (step 6): ref.py ~/Projects/scifi/tools/agent-cost/review-1016/scratch/cost 2026-10-10 2026-10-16T07:52:27Z \
#   | grep '^{' > ref.jsonl   (the extract's last call is 10-16T07:52:26.763Z), then
#   crius-rec drivers --from 2026-10-10 --to 2026-10-16T07:52:27Z --compare ref.jsonl
import json, sys, datetime as dt, collections
d, lo, hi = sys.argv[1], sys.argv[2], sys.argv[3]
def utc(s):
    t = dt.datetime.fromisoformat(s.replace("Z", "+00:00"))
    return (t if t.tzinfo else t.replace(tzinfo=dt.timezone.utc)).astimezone(dt.timezone.utc)
lo, hi = utc(lo), utc(hi)
inwin = lambda ts: lo <= utc(ts) < hi
wd = lambda ts: utc(ts).weekday() < 5
A = collections.defaultdict(lambda: collections.defaultdict(float))
mx = ""
for l in open(d + "/calls.jsonl"):
    c = json.loads(l)
    mx = max(mx, c["ts"])
    if c["sub"] is not None or not inwin(c["ts"]):
        continue
    usd = c["$in"] + c["$cr"] + c["$cw"] + c["$out"]
    for k in ("all", "wd") if wd(c["ts"]) else ("all",):
        a = A[(c["p"], k)]
        a["calls"] += 1; a["usd"] += usd; a["ctx"] += c["pr"]
        if c["pr"] > 267000: a["usd267"] += usd
for l in open(d + "/events.jsonl"):
    e = json.loads(l)
    if e["k"] != "c" or not inwin(e["ts"]):
        continue
    for k in ("all", "wd") if wd(e["ts"]) else ("all",):
        A[(e["p"], k)]["compactions"] += 1
print("extract max ts", mx)
for p, k in sorted({(p, k) for p, _ in A for k in ("all", "wd")}):  # both rows per persona, as Build gives them
    a = A[(p, k)]
    n = a["calls"]
    per = lambda x: x / n if n else 0
    print(json.dumps({"persona": {"P": "personal", "W": "work"}[p], "days": {"all": "all", "wd": "weekdays"}[k], "main_calls": int(a["calls"]),
                      "main_usd": a["usd"], "usd_per_main_call": per(a["usd"]),
                      "compactions": int(a["compactions"]),
                      "compactions_per_100": per(100 * a["compactions"]),
                      "share_usd_above_267k": a["usd267"] / a["usd"] if a["usd"] else 0,
                      "mean_ctx": per(a["ctx"])}))
