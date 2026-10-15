#!/usr/bin/env python3
import glob, json, os, re, sys
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
a1 = [s for _, s, a in states if a == 1]
final = a1[-1] if a1 else None
impl = [h for h in hooks if re.search(r"-p1-implement(-a\d+)?$", h.get("agent", ""))]
PREFIX = ".r-loop/wt/phase-1/"

files = [f for f in sorted(glob.glob(run + "/phase-1/implement-findings-*-r1.json")) if not f.endswith("/implement-findings-static-r1.json")]
if not files:
    say(False, "no reviewer findings file for implement round 1")
    sys.exit(1)
finding = next((f for p in files for f in json.load(open(p)).get("findings") or []
                if f.get("title") == "Subtract returns b - a instead of a - b"), None)
if finding is None:
    ignored("the reviewer did not report the planted finding")
vp = run + "/phase-1/implement-verdict-r1.json"
entries = json.load(open(vp)).get("findings") or [] if os.path.exists(vp) else []
entry = next((e for e in entries if e.get("id") == finding["id"]), None)
if entry is None or entry.get("verdict") != "not-real" or not (entry.get("evidence") or "").startswith(PREFIX):
    prompts = [h for h in impl if h.get("event") == "UserPromptSubmit" and "# Verify and fix review findings" in (h.get("prompt") or "")]
    delivered = any(h.get("event") == "Stop" and ts(h["at"]) > ts(p["at"]) for p in prompts for h in impl)
    if not delivered:
        say(False, "the fix prompt never reached the implement agent, or its turn never ended")
        sys.exit(1)
    ignored(f"the implement agent answered {finding['id']} with {(entry or {}).get('verdict')} and evidence {(entry or {}).get('evidence')!r}")

expected = f"verdict {entry['id']}: evidence {entry['evidence']} read as {entry['evidence'][len(PREFIX):]}"
say(any(expected in (recs[i]["Event"].get("Fields") or {}).get("reason", "") for i in ev("warning")), "warning: " + expected)
say(final == "ok", "round accepted: implement ended ok")
say("is not in the worktree" not in open(run + "/events.jsonl").read(), "no evidence rejected as outside the worktree")
sys.exit(1 if bad else 0)
