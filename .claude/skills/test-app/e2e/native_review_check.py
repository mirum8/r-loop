#!/usr/bin/env python3
# usage: native_review_check.py <sandbox> <run-dir>
# For every claude reviewer whose review-find ran a slash command, its artifacts folder under <run-dir>/phase-*/ checks native-review.txt exists, is non-empty,
# and equals the forked /code-review's <result> (the last assistant text of its subagents/agent-*.jsonl);
# that the fork's tool calls read the uncommitted changes (git diff HEAD / status / untracked);
# and that no native-review.txt sits directly in a phase folder. Prints OK/FAIL lines; exit 1 on any FAIL.
import glob, json, os, re, sys
sandbox, run = sys.argv[1].rstrip("/"), sys.argv[2].rstrip("/")
fails = 0
def ok(m): print("OK   " + m)
def fail(m):
    global fails; fails += 1; print("FAIL " + m)
def norm(s): return re.sub(r"\s+", " ", s).strip()
def texts(path):
    out = []
    for line in open(path):
        try: r = json.loads(line)
        except ValueError: continue
        c = (r.get("message") or {}).get("content")
        if isinstance(c, str): out.append((r.get("type"), c))
        elif isinstance(c, list):
            for x in c:
                if isinstance(x, dict):
                    if x.get("type") == "text": out.append((r.get("type"), x.get("text", "")))
                    elif x.get("type") == "tool_result":
                        t = x.get("content")
                        out.append(("tool_result", t if isinstance(t, str) else " ".join(y.get("text", "") for y in t or [] if isinstance(y, dict))))
    return out
munged = re.sub(r"[^A-Za-z0-9]", "-", sandbox)
native = set()
for line in open(run + "/events.jsonl"):
    e = (json.loads(line).get("Event") or {})
    f = e.get("Fields") or {}
    if e.get("Kind") == "review-find" and f.get("command", "").startswith("/") and f.get("reviewer", "").startswith("claude"):
        native.add((e.get("Phase"), e.get("Step"), f["reviewer"], f.get("round"), f.get("state")))
        print("     review-find phase %s %s reviewer %s round %s state %s command %s findings %s" % (e.get("Phase"), e.get("Step"), f["reviewer"], f.get("round"), f.get("state"), f["command"], f.get("findings")))
for n in native:
    if n[4] != "ok": fail("claude reviewer %s/%s round %s ended %s" % (n[1], n[2], n[3], n[4]))
tx = glob.glob(os.path.expanduser("~/.claude/projects/") + munged + "*/*.jsonl")
for phase in sorted(glob.glob(run + "/phase-*")):
    stray = os.path.join(phase, "native-review.txt")
    if os.path.exists(stray): fail("stray native-review.txt directly in %s" % phase)
    else: ok("no native-review.txt directly in %s" % os.path.basename(phase))
    ph = os.path.basename(phase).split("-", 1)[1]
    dirs = sorted(d for (p, step, rv, rd, st) in native if p == ph for d in glob.glob("%s/%s-rv-%s-r%s-a*" % (phase, step, rv, rd)) if os.path.isdir(d))
    if not dirs: print("NOTE no claude reviewer folders in %s" % phase)
    for d in dirs:
        name = os.path.relpath(d, run)
        f = os.path.join(d, "native-review.txt")
        if not os.path.exists(f) or os.path.getsize(f) == 0:
            fail("%s/native-review.txt missing or empty" % name); continue
        body = open(f).read()
        ok("%s/native-review.txt %d bytes, starts %r" % (name, len(body), norm(body)[:80]))
        owner = [t for t in tx if d in open(t).read() or os.path.realpath(d) in open(t).read()]
        if not owner: fail("%s: no claude transcript mentions this folder" % name); continue
        results, forks = [], []
        for t in owner:
            for kind, s in texts(t):
                if kind == "user" and "<task-notification>" in s:
                    results += re.findall(r"<result>(.*?)</result>", s, re.S)
            sid = os.path.splitext(t)[0]
            for a in glob.glob(sid + "/subagents/agent-*.jsonl"):
                last = [s for kind, s in texts(a) if kind == "assistant" and s.strip()]
                if last: results.append(last[-1])
                forks.append(a)
        cmds = [json.dumps(x.get("input")) for a in forks for line in open(a) for x in ((json.loads(line).get("message") or {}).get("content") or []) if isinstance(x, dict) and x.get("type") == "tool_use"]
        changed = set()
        for a in forks:
            for line in open(a):
                st = (((json.loads(line).get("attachment") or {}).get("context") or {}).get("gitStatus") or "")
                changed |= set(re.findall(r"^(?:\?\?|[ MADRCU]{2}) (\S+)$", st, re.M))
        changed = {p for p in changed if not re.search(r"(_test\.|\.test\.|/test/|testdata/|fixtures/)", p)}
        if any(re.search(r"git diff HEAD|git status|ls-files --others", c) for c in cmds): ok("%s: /code-review read the uncommitted changes" % name)
        elif changed and all(any(p in c for c in cmds) for p in changed): ok("%s: /code-review read every changed non-test file its git status listed: %s" % (name, sorted(changed)))
        else: fail("%s: /code-review never read the uncommitted changes: %s" % (name, [c[:160] for c in cmds]))
        if not results:
            fail("%s: no forked <result> or subagent final text found in %s" % (name, owner)); continue
        if any(norm(r) == norm(body) for r in results):
            ok("%s/native-review.txt matches the fork's <result> verbatim (%d candidate(s))" % (name, len(results)))
        elif any(norm(r) and (norm(r) in norm(body) or norm(body) in norm(r)) for r in results):
            ok("%s/native-review.txt contains/is contained in the fork's <result> (not byte-exact)" % name)
        else:
            fail("%s/native-review.txt differs from the fork's <result>: file=%r result=%r" % (name, norm(body)[:200], norm(results[-1])[:200]))
sys.exit(1 if fails else 0)
