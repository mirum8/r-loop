#!/usr/bin/env python3
import json, os, re, sys
from datetime import datetime
run = sys.argv[1].rstrip("/")
bad = []
def say(ok, msg):
    print(("OK   " if ok else "FAIL ") + msg)
    if not ok:
        bad.append(msg)
def ignored(what):
    print("NOT RUN: the agent ignored the steering: " + what)
    sys.exit(3)
def ts(s): return datetime.fromisoformat(re.sub(r"(\.\d{6})\d+", r"\1", s.replace("Z", "+00:00")))
def lines(name):
    p = os.path.join(run, name)
    return [json.loads(l) for l in open(p, encoding="utf-8") if l.strip()] if os.path.exists(p) else []
recs, hooks = lines("events.jsonl"), lines("hooks.jsonl")
states = [(i, r.get("State"), r["Step"].get("Attempt")) for i, r in enumerate(recs)
          if r.get("Kind") == "step" and (r.get("Step") or {}).get("Phase") == "1" and r["Step"].get("Kind") == "implement"]
def ev(kind):
    return [i for i, r in enumerate(recs) if r.get("Kind") == "event" and (r.get("Event") or {}).get("Kind") == kind
            and r["Event"].get("Phase") == "1" and r["Event"].get("Step") == "implement"]
R = (ev("review-round") or [len(recs)])[0]
impl = [h for h in hooks if re.search(r"-p1-implement$", h.get("agent", ""))]

first = next((h for h in impl if h.get("event") == "Stop"), None)
if first is None:
    say(False, "no Stop hook from the implement agent")
    sys.exit(1)
first_at = ts(first["at"])
sentinel = os.path.join(run, "phase-1", "implement-a1.sentinel")
sent = os.path.getmtime(sentinel) if os.path.exists(sentinel) else None
if sent is not None and sent <= first_at.timestamp():
    ignored("the implement agent wrote its sentinel in its first turn")
worked = [h for h in impl if h.get("event") == "PreToolUse" and h.get("tool") in ("Edit", "Write", "MultiEdit", "NotebookEdit")
          and ts(h["at"]) < first_at]
if worked:
    ignored(f"the implement agent worked in its first turn ({', '.join(sorted({h['tool'] for h in worked}))})")

work = [(i, s) for i, s, a in states if a == 1 and i < R]
nudges = [i for i in ev("nudge") if i < R]
i = next((x for x, s in work if s == "stalled"), None)
j = next((x for x in nudges if x > i), None) if i is not None else None
k = next((x for x, s in work if s == "running" and x > j), None) if j is not None else None
say(i is not None, "stalled in the work half")
say(j is not None, "nudge after stalled")
say(k is not None, "running after the nudge")
a1 = [(x, s) for x, s, a in states if a == 1]
say(k is not None and a1[-1][1] == "ok" and a1[-1][0] > k, "ok after running")
say("no response to nudge" not in open(run + "/events.jsonl").read(), "no 'no response to nudge'")
sys.exit(1 if bad else 0)
