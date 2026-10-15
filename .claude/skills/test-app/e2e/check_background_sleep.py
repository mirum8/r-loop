#!/usr/bin/env python3
import json, os, re, sys
from datetime import datetime, timedelta
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
a1 = [s for _, s, a in states if a == 1]
final = a1[-1] if a1 else None
impl = [h for h in hooks if re.search(r"-p1-implement$", h.get("agent", ""))]

grace = None
cfg = os.path.join(run, "config.resolved.yaml")
if os.path.exists(cfg):
    m = re.search(r"^watchdog:\n(?:[ \t]+.*\n|\n)*?[ \t]+stallgrace: *(\S+)", open(cfg).read(), re.M)
    if m:
        units = {"h": 3600, "m": 60, "s": 1, "ms": 0.001}
        grace = sum(float(n) * units[u] for n, u in re.findall(r"([\d.]+)(ms|h|m|s)", m.group(1)))
if grace is None:
    say(False, "no watchdog stallgrace in config.resolved.yaml")
    sys.exit(1)
if grace <= 0:
    grace = 120
bound = 3 * grace
if bound <= 60:
    print(f"NOT RUN: the background bound {bound:.0f}s is shorter than the 60s sleep; run with a stallGrace above 20s")
    sys.exit(3)

if not any(h.get("event") == "Stop" for h in impl):
    say(False, "no Stop hook from the implement agent")
    sys.exit(1)
bg = next((h for h in impl if h.get("event") == "PreToolUse" and h.get("tool") == "Bash" and h.get("background") is True
           and (h.get("target") or "").startswith("sleep 60")), None)
if bg is None:
    ignored("the implement agent started no background sleep 60 (hooks.jsonl)")
start = ts(bg["at"])
end = start + timedelta(seconds=60)
stop = next((h for h in impl if h.get("event") == "Stop" and start < ts(h["at"]) < end), None)
if stop is None:
    ignored("the implement agent did not end its turn while the sleep ran")

marks = [i for i, s, _ in states if s == "stalled"] + ev("stalled") + ev("nudge")
stall = min(marks) if marks else None
if stall is not None:
    at = ts(recs[stall]["At"])
    before = [h for h in impl if h.get("event") != "Notification" and ts(h["at"]) < at]
    quiet = (at - ts(before[-1]["at"])).total_seconds() if before else 0
    if quiet < bound:
        say(False, f"implement stalled after {quiet:.0f}s of quiet, inside the {bound:.0f}s background bound")
    elif not any(h.get("event") == "UserPromptSubmit" and (h.get("prompt") or "").lstrip().startswith("<task-notification>")
                 and ts(stop["at"]) < ts(h["at"]) < at for h in impl):
        ignored(f"the implement agent did not resume when the sleep finished; the stall after {quiet:.0f}s of quiet is the bounded-background stall")
    else:
        ignored(f"the implement agent resumed after the sleep, then went quiet for {quiet:.0f}s without a sentinel")
else:
    upper = ts(recs[R]["At"]) if R < len(recs) else None
    resumed = [h for h in impl if h.get("event") != "Notification" and end <= ts(h["at"]) and (upper is None or ts(h["at"]) < upper)]
    if not resumed and final != "ok":
        say(False, f"implement attempt 1 ended {final} before the work half finished")
        sys.exit(1)
    if not resumed:
        ignored("the implement agent wrote its sentinel before the sleep finished")
    say(True, "hooks.jsonl background line: " + json.dumps(bg)[:200])
    say(True, f"turn ended {(ts(stop['at']) - start).seconds}s into the sleep, work resumed after it")
    say(True, "no stall and no nudge for implement")
say(final == "ok", f"implement ended ok (states {[s for _, s, _ in states]})")
sys.exit(1 if bad else 0)
