status: planned

## Summary

The phase gives every step worker and reviewer one non-fatal `agent-transcript` event in the run log. The event names the agent's transcript file, and `report.md` lists those files on the Halt line of a blocked or aborted step.

- **claude:** the hook tap keeps the `session_id` and `transcript_path` that every claude hook carries. The first hook that carries a path pins them on the agent's `hookAgent`.
- **codex:** a new core port, `RolloutFinder`, finds the agent's rollout under `~/.codex/sessions`. Its adapter is a new package, `internal/rollout`. A rollout matches when:
  - its `session_meta` `payload.cwd` is the session's worktree;
  - its `payload.timestamp` is no earlier than the session's `Host.Start`;
  - one of its **user** messages contains the session's own sentinel path;
  - its `payload.source` is a string (`cli`), not a codex sub-agent's `{"subagent": …}` object.

  The first match in path order wins; rollout file names begin with the start time.
- **Recording:** one helper, `SessionManager.recordTranscript(s)`, records the event once per session. Every worker and reviewer session is tracked in `SessionManager` from just before its `Host.Start`. `recordTranscripts()` sweeps all tracked sessions at two points:
  - on every `waitAll` tick, after the hook drain — so a worker not being waited, such as a resumed worker during its reviewers' wait, still records promptly;
  - in `Finish`, after a final hook drain, before the step's sessions are untracked.

  Every step runner ends in `Finish`: `runners.go:13`, `runners.go:26`, `gate.go:110-112` and `milestone.go:146-148`. So the sweep in `Finish` also covers these exits:
  - a failed `Host.Start`;
  - a failed prompt;
  - a sibling reviewer's failure that skips the wait;
  - a step cancelled before its first tick.

Choices taken:

1. **Where the event is recorded:** a set of tracked sessions in `SessionManager`, swept on each `waitAll` tick and in `Finish`. The alternatives were:
   - per-call-site recording in `hookStart`, `start`, `ReviewHalf.prompt` and `waitAll`'s cancel branch, tried in round 1 — every early exit needed its own call, and review round 2 found three more exits that call sites miss (a sibling reviewer's failure, a resumed worker, a start error);
   - recording inside `drainHooks` — that holds `hooks.mu`, which `recordTranscript` takes again through `hookAgent`, and it has no session to name.

   One sweep at the single exit every runner passes through cannot miss an exit.
2. **Where the codex lookup lives:** a core port plus the adapter package `internal/rollout`. The alternatives were stdlib code inside `internal/core`, or a function in `internal/quota`. The port won because core stays provider-agnostic: it holds no `"codex"` string today. It also follows the one-package-per-port layout, and `quota` is about usage limits.
3. **The codex matching key:** cwd, plus `payload.timestamp` ≥ start, plus a user message naming the session's sentinel, plus a root (non-sub-agent) session. The alternative was cwd and start time alone. The sentinel won because it is unique per worker attempt, reviewer, round and attempt. cwd and time alone cannot tell three reviewers apart that start seconds apart in one worktree (the watchdog's warning).
4. **How core knows a session writes a rollout:** a new `ProviderArgs.Rollout` flag, set by `app.resolve` for `kind: codex`. The alternative was core comparing `args.Kind == "codex"`. The flag won because it mirrors `ProviderArgs.Hooks` (`internal/app/wire.go:1042`) and keeps provider names out of core.
5. **Where the report lists them:** on the step's line in the `## Halt` section (`haltLines`). The alternatives were the phase-level "blocked" line under Automatic decisions, or a new section. `haltLines` won because:
   - its lines are the per-step lines the item names (`phase N <step>: <reason> — workspace …`);
   - any blocked phase ends the run `halted` (`internal/core/loop.go:266-276`), so a blocked step's line always renders there.
6. **A failed append:** one warning, and the event is never retried. The alternative was retrying every tick. One warning won because retrying would warn every 2 s, and `agent-transcript` is non-fatal by `FatalRecord` (`internal/core/records.go:28-37`).

## Why this approach

**What was proposed.** The phase asks for:
- one non-fatal `agent-transcript` event at a claude agent's first hook;
- the same event for codex, naming its rollout "matched by the session's worktree and start time, not by newest file";
- transcript paths on a blocked or aborted step's `report.md` line.

The triage proposed:
- the tap captures `session_id` and `transcript_path`;
- core appends the event at an agent's first hook;
- for codex, an adapter-side lookup over `~/.codex/sessions/*/*/*/rollout-*.jsonl`, matching `session_meta` cwd and timestamp, behind a core port;
- `renderReport` lists the paths.

**Where this plan follows the triage.** It follows on the tap, the port and the report.

- **Why not `agent-named`.** That event is appended before `Host.Start` (`internal/core/session.go:287-291`, `internal/core/review.go:545-548`), so no hook can have fired yet.
- **Why the report change sits in `haltLines`.** `renderReport` (`internal/core/loop.go:1920-1923`) only calls `Report`. The step lines are built by `haltLines` (`internal/core/report.go:156-178`), so the change goes there.

**Where it departs: the codex match.** Matching on worktree and start time alone is ambiguous:
- Reviewer sessions run in the worker's own directory (`Dir: worker.Dir`, `internal/core/review.go:807`).
- The `implement` row has several codex reviewers, which start seconds apart.
- The driver's own `codex review --uncommitted` runs in the same cwd and writes a rollout too.

So the lookup also requires a **user** message (`type: response_item`, `payload.type: message`, `role: user`) that contains the session's sentinel path. The sentinel path is unique:
- it embeds kind, reviewer, round and attempt (`internal/core/session.go:176-180`, `internal/core/review.go:811`);
- it is in every prompt the driver types, through the footer at `internal/prompts/render.go:24`;
- it appears in real rollouts — this run's phase-3 codex reviewer rollout carries `phase-3/implement-rv-codex-r1-a1.sentinel` in its user message.

Three more details:
- **Only user messages count.** A reviewer's tool output can print another session's sentinel path; a user message cannot.
- **The start is `payload.timestamp`.** A real rollout's top-level `timestamp` (16:55:29Z) was written six minutes after its `payload.timestamp` (16:49:04Z), so the plan matches on `payload.timestamp`, as the watchdog warned.
- **Sub-agent sessions are excluded.** codex's multi-agent mode spawns sub-sessions in the same cwd, and they can carry the parent's messages. Picking the earliest file would still pin a child whose rollout becomes readable before its parent's. Instead, `session_meta` marks every sub-agent with an object `source`. On this machine's rollouts that is `{"subagent":"review"}`, `{"subagent":{"thread_spawn":{"parent_thread_id":…}}}` or `{"subagent":{"other":"guardian"}}`, while a TUI session's `source` is the string `"cli"`. So only a string `source` matches.
- **Only the decoded text is matched.** The marker is compared against the decoded user message text, never the raw JSON line. A sentinel under a directory holding `"` or `\` is JSON-escaped in the file and would miss a byte search.

**Where it departs: ADR-93.** ADR-93 lists the "nine things only" the driver uses hooks for (`docs/task-loop-driver/spec.html:4156`). The transcript record is a tenth, so the plan amends ADR-93. Phases 1 and 3 amended it the same way in their own commits (`git show --stat 5c075b6`: `docs/task-loop-driver/spec.html`, `docs/task-loop-driver/tech-design.md`). For the same reason, `tech-design.md` gets the new port, the tap's two fields and the report rule.

**Files outside an explicit `Files:` line.** The phase has none, so this plan names every file. The new package `internal/rollout` exists because the codex lookup is file-format I/O that `internal/core` must not own: the boundary test is at `internal/core/boundary_test.go:9-21`, and the pattern is one adapter package per port. `internal/app/wire.go` has to wire the finder and set the new flag.

## Changes

Build in this order.

### 1. `internal/core/ports.go` — modify

- Add two fields to `HookEvent` (`:19-26`), after `PromptCut`:
  ```go
  Session, Transcript string
  ```
- Add the port after `HookFeed` (`:28-30`):
  ```go
  type RolloutFinder interface {
  	Find(dir, marker string, since time.Time) (path, session string)
  }
  ```
  Obligations: claude fields (item 1) and the codex port (item 2).

### 2. `internal/core/types.go` — modify

Add `Rollout bool` to `ProviderArgs` (`:349-356`), after `Hooks`. It is true when the provider writes codex rollouts. Obligation: item 2, codex sessions only.

### 3. `internal/hooktap/hooktap.go` — modify

- **`line` (`:17-28`):** add
  ```go
  Session    string `json:"session_id,omitempty"`
  Transcript string `json:"transcript_path,omitempty"`
  ```
- **The anonymous `hook` struct (`:36-50`):** add
  ```go
  Session    string `json:"session_id"`
  Transcript string `json:"transcript_path"`
  ```
- **`json.Marshal(line{...})` (`:60`):** add `Session: hook.Session, Transcript: hook.Transcript`, uncapped (see Assumptions).
- **`Feed.Read` (`:131`):** add `Session: l.Session, Transcript: l.Transcript` to the `core.HookEvent` literal.

Obligation: item 1, the tap captures both values.

### 4. `internal/core/hooks.go` — modify

- **`hookAgent` (`:13-28`):** add the field `session, transcript string`.
- **`(*hookLog).apply` (`:64`):** right after `a.heard, a.typed = true, false` (`:69`), add:
  ```go
  if a.transcript == "" && ev.Transcript != "" {
  	a.session, a.transcript = ev.Session, ev.Transcript
  }
  ```
  - Only the first hook that carries a path counts. A later `/clear` SessionStart with a new transcript does not replace it.
  - Events before `a.since` are already dropped (`:66-68`). `hookReset` (`:324-334`) replaces the whole struct. So a reopened reviewer with the same agent name never inherits its predecessor's transcript.
- `hookStart` is not changed.

Obligations: item 1 (first hook, one event) and the reopen edge.

### 5. `internal/core/session.go` — modify

- **`SessionManager` (`:32-54`):** add the exported field `Rollouts RolloutFinder` after `HookTap`, and the unexported field `tracked []*Session` after `typing`, guarded by the existing `mu`.
- **`Session` (`:73-89`):** add three unexported fields:
  ```go
  rollout     bool
  startedAt   time.Time
  transcribed atomic.Bool
  ```
- **`start` (`:265`):** right before `m.hookReset(s.Agent)` (`:291`), add:
  ```go
  s.rollout, s.startedAt = args.Rollout, m.now()
  m.track(s)
  ```
  The session is tracked before `Host.Start`. herdr's `Start` can fail in `awaitReady` after the agent process is already running (`internal/herdr/client.go:252-254`), and a hook it sent by then is still swept in `Finish`.
- **`waitAll` (`:343`):** right after `hookErr := m.drainHooks()` (`:376`), before the per-watch loop, add `m.recordTranscripts()`. The lookup runs on every tick before any sentinel is judged, for every tracked session — not only the ones being waited. So a session that ends on its first tick still gets its event, and so does a resumed worker (`ReviewFrom > 0`, which `singleRunner` never waits, `internal/core/runners.go:17-19`) whose first hook arrives while its reviewers run.
- **`Finish` (`:721`):** as its first statements, before `s.end()`, add:
  ```go
  _ = m.drainHooks()
  m.recordTranscripts()
  m.untrack(s.Ref.Key)
  ```
  This is the final collection for the worker and every reviewer of that step key, whatever exit the step took. That covers:
  - a failed `Host.Start`, through `Spawn`'s error → `runners.go:13`;
  - a failed prompt, of the worker or of a reviewer;
  - a sibling reviewer's failure that returns before `waitAll` (`internal/core/review.go:441-456`);
  - a cancelled wait — `waitAll`'s `ctx.Done` branch returns, and the runner still calls `Finish`.
- **Three new methods,** placed after `start`:
  ```go
  func (m *SessionManager) track(s *Session) {
  	_ = m.drainHooks()
  	m.mu.Lock()
  	var gone []*Session
  	m.tracked = slices.DeleteFunc(m.tracked, func(t *Session) bool {
  		if t.Agent != s.Agent {
  			return false
  		}
  		gone = append(gone, t)
  		return true
  	})
  	m.tracked = append(m.tracked, s)
  	m.mu.Unlock()
  	for _, t := range gone {
  		m.recordTranscript(t)
  	}
  }
  ```
  `track` runs before `hookReset`. A reopened reviewer reuses its predecessor's agent name (`internal/core/review.go:704-721`). So `track` records the predecessor from the hook state that is still its own, then drops it. Otherwise, after `hookReset`, the predecessor's `Session` would read the successor's transcript. Workers never share a name: the attempt suffix comes from `internal/core/session.go:181-185`.
  ```go

  func (m *SessionManager) untrack(key StepKey) {
  	m.mu.Lock()
  	defer m.mu.Unlock()
  	m.tracked = slices.DeleteFunc(m.tracked, func(s *Session) bool { return s.Ref.Key == key })
  }

  func (m *SessionManager) recordTranscripts() {
  	m.mu.Lock()
  	sessions := slices.Clone(m.tracked)
  	m.mu.Unlock()
  	for _, s := range sessions {
  		m.recordTranscript(s)
  	}
  }
  ```
  `slices` is already imported (`:11`).
- **New method `recordTranscript`,** placed after them:
  ```go
  func (m *SessionManager) recordTranscript(s *Session) {
  	if s.transcribed.Load() {
  		return
  	}
  	var id, path string
  	m.hookAgent(s.Agent, func(a *hookAgent) { id, path = a.session, a.transcript })
  	if path == "" && s.rollout && m.Rollouts != nil {
  		path, id = m.Rollouts.Find(s.Dir, s.Sentinel, s.startedAt)
  	}
  	if path == "" || !s.transcribed.CompareAndSwap(false, true) {
  		return
  	}
  	key := s.Ref.Key
  	fields := map[string]string{"agent": s.Agent, "attempt": strconv.Itoa(key.Attempt), "session_id": id, "transcript_path": path}
  	if s.Reviewer != "" {
  		fields["reviewer"], fields["round"] = s.Reviewer, fmt.Sprint(s.Ref.Vars["Round"])
  	}
  	at := m.now()
  	ev := Event{At: at, Kind: "agent-transcript", Phase: key.Phase, Step: key.Kind, Fields: fields}
  	if err := m.Store.Append(key.Run, Record{Kind: RecordEvent, At: at, Event: &ev}); err != nil && m.Face != nil {
  		m.Face.Emit(Event{At: at, Kind: "warning", Phase: key.Phase, Step: key.Kind, Fields: map[string]string{"reason": "transcript: " + err.Error()}})
  	}
  }
  ```
  - It follows the `agent-named` append (`:286-290`) and the `m.Face` nil guard in `hookStart` (`internal/core/hooks.go:360-362`).
  - For a reviewer, `s.Ref.Key` is the worker's key (`internal/core/review.go:834-835`). So `Phase`/`Step` are the worker's phase and kind, as `ReviewHalf.event` records them (`internal/core/review.go:873-877`).
  - The event goes to the Store only, never to the Face.
  - `s.Sentinel` is the sentinel of the prompt last typed. For a worker resumed into its review half (`ReviewFrom > 0`, `:296-298`), that is the fix prompt's sentinel during the fix wait (`internal/core/review.go:198-199`), and the fix prompt is the user message that names it.

Obligations: items 1 and 2 (one event each), non-fatal recording, and never retrying a failed append.

### 6. `internal/core/review.go` — modify

In `launch` (`:533`), right before `sm.hookReset(s.Agent)` (`:548`), add:
```go
s.rollout, s.startedAt = r.args.Rollout, sm.now()
sm.track(s)
```
`s` is the fresh `*Session` that `h.reviewer` builds (`:795-835`), both for a first start and for a reopen (`:704`). Its `Ref.Key` is the worker's key, so the worker's `Finish` untracks it. Obligations: items 1 and 2 for reviewers, on every exit.

### 7. `internal/core/report.go` — modify

- **New function `transcripts`:**
  ```go
  func transcripts(st RunState) map[string][]string {
  	out := map[string][]string{}
  	for _, ev := range st.Events {
  		if ev.Kind != "agent-transcript" {
  			continue
  		}
  		f := ev.Fields
  		who := "worker a" + f["attempt"]
  		if f["reviewer"] != "" {
  			who = "reviewer " + f["reviewer"] + " a" + f["attempt"] + " r" + f["round"]
  		}
  		key := ev.Phase + "\x00" + ev.Step
  		out[key] = append(out[key], who+" "+f["transcript_path"])
  	}
  	return out
  }
  ```
- **New function `transcriptList`:**
  ```go
  func transcriptList(paths []string) string {
  	if len(paths) == 0 {
  		return ""
  	}
  	return " — transcripts: " + strings.Join(paths, ", ")
  }
  ```
- **`haltLines` (`:156-178`):**
  - At the top, add `seen := transcripts(st)`.
  - The `phase-blocked` line becomes:
    ```go
    where(ev.Phase, ev.Step)+": "+f["reason"]+place(f)+transcriptList(seen[ev.Phase+"\x00"+ev.Step])
    ```
  - The `aborted` line becomes:
    ```go
    "aborted during "+where(ev.Phase, ev.Step)+place(f)+transcriptList(seen[ev.Phase+"\x00"+ev.Step])
    ```
- **Behaviour:**
  - Transcripts are collected from all events, in log order: every attempt and round of that phase and step, and those recorded after the blocked event.
  - A step with none, or an abort with an empty step (`internal/core/control.go:192`), adds nothing, so its line renders exactly as today.

Obligation: item 3.

### 8. `internal/rollout/rollout.go` — create

The package is `rollout` and is the adapter for `core.RolloutFinder`. It imports only the standard library.

```go
type Finder struct{ Home string }

type meta struct {
	Type    string `json:"type"`
	Payload struct {
		ID        string          `json:"id"`
		Timestamp time.Time       `json:"timestamp"`
		Cwd       string          `json:"cwd"`
		Source    json.RawMessage `json:"source"`
	} `json:"payload"`
}

type item struct {
	Type    string `json:"type"`
	Payload struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"payload"`
}
```

**`func (f Finder) Find(dir, marker string, since time.Time) (string, string)`:**
1. `sessions := filepath.Join(f.Home, "sessions")`.
2. `from := since.UTC().AddDate(0, 0, -1).Format("2006/01/02")`. codex names day directories by local date, which can be a day behind UTC.
3. `days, _ := filepath.Glob(filepath.Join(sessions, "*", "*", "*"))`, the same layout as `internal/quota/quota.go:70`. Skip a day whose `filepath.ToSlash(filepath.Rel(sessions, day))` errors or is `< from`.
4. For each kept day, in `Glob` order, `files, _ := filepath.Glob(filepath.Join(day, "rollout-*.jsonl"))`. Return `(file, id)` for the first file where `match` returns `(id, true)`.
5. Return `("", "")` when nothing matched.

**`func match(path, dir, marker string, since time.Time) (string, bool)`:**
1. `os.Open`; on error return false. Read with a `bufio.Scanner` and `sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)`, as in `internal/quota/quota.go:95-96`.
2. **First line.** It must decode as `meta` with:
   - `Type == "session_meta"`;
   - `filepath.Clean(Payload.Cwd) == filepath.Clean(dir)`;
   - `!Payload.Timestamp.Before(since.Truncate(time.Millisecond))`;
   - `Payload.Source` decodes into a Go `string`. `json.Unmarshal(Payload.Source, &src) == nil`, with `var src string`, rejects a sub-agent's object source and a missing source.

   Otherwise return false. codex writes `payload.timestamp` at millisecond precision, and `since` is taken just before `Host.Start`.
3. **Remaining lines.** Decode every line as `item`, with no raw-byte pre-filter, and return `(meta.Payload.ID, true)` when all of these hold:
   - `Type == "response_item"`;
   - `Payload.Type == "message"`;
   - `Payload.Role == "user"`;
   - some `Content[i].Text` contains `marker`.
4. Otherwise return false.

Obligations: item 2, the match rules and "not newest file", plus the watchdog's warning.

### 9. `internal/app/wire.go` — modify

- **New function** (`os` and `filepath` are already imported):
  ```go
  func codexHome(home string) string {
  	if h := os.Getenv("CODEX_HOME"); h != "" {
  		return h
  	}
  	return filepath.Join(home, ".codex")
  }
  ```
- **`pollUsage` (`:822`):** replace the inline `codexHome` lines (`:824-827`) with `home := codexHome(w.Env.Home)`, and pass `home` to `quota.Codex` (`:840`).
- **The `core.SessionManager` literal (`:523-537`):** add `Rollouts: rollout.Finder{Home: codexHome(env.Home)},` and import `r-loop/internal/rollout`.
- **`resolve`:** after `args.Hooks = …` (`:1042`), add `args.Rollout = p.Kind == "codex"`.

Obligation: item 2, the finder is live in a real run, only for codex.

### 10. `docs/task-loop-driver/spec.html` — modify (ADR-93)

- **Status line (`:4146`):** append ` · amended 2026-10-13 — transcripts for post-mortems`.
- **`:4156`:** change `nine things only` to `ten things only`.
- **`:4182`:** after `<code>clear</code> and <code>resume</code> are ignored`, insert before the following `. Prompts`:
  ```
  ; the transcript — the first hook of a session that carries a <code>transcript_path</code> appends one non-fatal <code>agent-transcript</code> event (agent, attempt, <code>session_id</code>, <code>transcript_path</code>, and reviewer and round for a reviewer); a codex session gets the same event for its rollout under <code>~/.codex/sessions</code>, the one whose <code>session_meta</code> cwd is the session's worktree, whose <code>payload.timestamp</code> is no earlier than its start, whose <code>payload.source</code> is not a sub-agent, and one of whose user messages names its sentinel; <code>report.md</code>'s Halt line for a blocked or aborted step lists every transcript recorded for that phase and step
  ```

Obligation: the ADR-93 invariant ("for nine things only").

### 11. `docs/task-loop-driver/tech-design.md` — modify

- **`hooks.jsonl` (`:175-176`):** change `(one line per claude hook event from \`r-loop hook-tap\`, read by the driver each tick; ADR-93)` to `(one line per claude hook event from \`r-loop hook-tap\`, carrying the hook's \`session_id\` and \`transcript_path\`, read by the driver each tick; ADR-93)`.
- **New bullet,** after the `core.ReviewRunner` bullet (`:1115-1118`):
  ```
  - **`core.RolloutFinder`** — `Find(dir, marker string, since time.Time) (path, session string)`, on `SessionManager.Rollouts`; the adapter `internal/rollout` (`Finder{Home}`, `$CODEX_HOME` or `~/.codex`) returns the first `sessions/<y>/<m>/<d>/rollout-*.jsonl` in path order, day directories from the day before `since` (UTC) on, whose `session_meta` `payload.cwd` is `dir`, whose `payload.timestamp` is no earlier than `since` (to the millisecond), whose `payload.source` is a string (a sub-agent's object source never matches), and one of whose decoded `response_item` user messages contains `marker` (the session's sentinel path); `ProviderArgs.Rollout` is true for `kind: codex`. A session's first hook transcript, or its rollout, is journalled once as the non-fatal `Event{Kind: "agent-transcript", Phase, Step, Fields{agent, attempt, session_id, transcript_path[, reviewer, round]}}`; a failed append warns `transcript: <err>` and is not retried.
  ```
- **Report bullet (`:507-513`):** append the sentence: `A blocked or aborted step's line under **Halt** ends \`— transcripts: worker a<attempt> <path>, reviewer <id> a<attempt> r<round> <path>, …\`, every \`agent-transcript\` of that phase and step in log order; a step with none renders unchanged.`

Obligation: tech-design contracts stay true.

## Tests

Write these first. Each uses `// given`, `// when`, `// then` blocks, the existing helpers (`assertEqual`, `assertCalls`, `assertState`, `hookStepRig`, `newRig`, `newReviewRig`, `hookedResolve`, `startingHost`, `failCodexOnce`, `then`, `warnings`, `failingStore`, `stepClock`, `waitCtx`) and fixed data.

### New core fake — `internal/core/fakes_test.go`

```go
type rolloutFile struct{ path, session string }

type fakeRollouts struct {
	mu     sync.Mutex
	files  map[string]rolloutFile
	misses int
	calls  []string
}

func (f *fakeRollouts) Find(dir, marker string, since time.Time) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, dir+" "+marker+" "+since.Format(time.RFC3339))
	if len(f.calls) <= f.misses {
		return "", ""
	}
	r := f.files[marker]
	return r.path, r.session
}
```

- `files` is keyed by marker.
- Add `_ RolloutFinder = (*fakeRollouts)(nil)` to the `var` block at `:592-603`.
- Add a helper `rolloutResolve(r *reviewRig)` beside `hookedResolve` (`internal/core/hooks_test.go:1041`). It wraps `r.sm.Resolve` and sets `args.Rollout = provider == "codex"`.

### `internal/core/transcript_test.go` (new file)

The step rigs use `stepAgent = "rloop-2kuxv-p3-implement"`. Their sentinel is `<runDir>/phase-3/implement-a1.sentinel`.

1. **`TestAClaudeAgentsFirstHookRecordsOneAgentTranscriptEvent`** — item 1, first hook, exactly one event.
   - **Given:**
     - `hookStepRig(t, HookEvent{At: t0, Agent: stepAgent, Event: "SessionStart", Session: "s-1", Transcript: "/home/.claude/projects/-repo/s-1.jsonl"})`;
     - after `spawn`, push `HookEvent{At: t1, Agent: stepAgent, Event: "SessionStart", Session: "s-2", Transcript: "/home/.claude/projects/-repo/s-2.jsonl"}` and `stop(stepAgent)`;
     - an ok sentinel, `TreeChanges = []string{"a.go"}`, and herdr idle.
   - **When:** `Wait`.
   - **Then:** `r.events("agent-transcript")` equals exactly one `Event{Kind: "agent-transcript", Phase: "3", Step: "implement", Fields: {"agent": stepAgent, "attempt": "1", "session_id": "s-1", "transcript_path": "/home/.claude/projects/-repo/s-1.jsonl"}}`. Compare `Kind`, `Phase`, `Step` and `Fields`; zero `At` first.

2. **`TestAClaudeAgentWithoutASessionStartRecordsTheTranscriptOfItsFirstLaterHook`** — item 1 when the first hook comes during the wait (the `waitAll` call site).
   - **Given:**
     - `hookStepRig(t)` with no SessionStart, and `shortHookStartWait`;
     - after `spawn`, push `HookEvent{At: t1, Agent: stepAgent, Event: "PreToolUse", Tool: "Read", Target: "/repo/a.go", Session: "s-1", Transcript: "/home/.claude/projects/-repo/s-1.jsonl"}`;
     - an ok sentinel, `TreeChanges`, and herdr idle.
   - **When:** `Wait`.
   - **Then:** exactly one `agent-transcript` event, with `transcript_path` `/home/.claude/projects/-repo/s-1.jsonl` and `session_id` `s-1`.

3. **`TestAClaudeWorkerWhosePromptFailsRecordsItsTranscriptWhenTheStepFinishes`** — item 1 on the worker prompt-failure path (`Finish` sweep; codex-r1-1). Table, one row per first hook that `startingHost` pushes at the worker's start:

   | name | hook |
   |---|---|
   | session start | `HookEvent{At: t0, Agent: stepAgent, Event: "SessionStart", Session: "s-1", Transcript: "/home/.claude/projects/-repo/s-1.jsonl"}` |
   | another hook, no session start | `HookEvent{At: t0, Agent: stepAgent, Event: "PreToolUse", Tool: "Read", Target: "/repo/a.go", Session: "s-1", Transcript: "/home/.claude/projects/-repo/s-1.jsonl"}` |

   - **Given:** `hookStepRig(t, tt.hook)`, `shortHookStartWait`, and `r.prompts.Err = errors.New("prompt template missing")`.
   - **When:** `out := singleRunner{sm: r.sm}.Run(context.Background(), r.ref(1), &recObserver{})`.
   - **Then:**
     - `assertState(t, out, StepFailed, "spawn: prompt template missing")`;
     - `r.events("agent-transcript")` is exactly one event with `Fields: {"agent": stepAgent, "attempt": "1", "session_id": "s-1", "transcript_path": "/home/.claude/projects/-repo/s-1.jsonl"}`.

4. **`TestAClaudeReviewersTranscriptNamesItsReviewerAndRound`** — items 1 and 3 (reviewer fields).
   - **Given:**
     - `newReviewRig(t, Reviewer{Provider: "claude"})`, a `fakeHookFeed`, and `hookedResolve`;
     - `startingHost` pushes `{{At: t1, Agent: "rloop-2kuxv-p3-implemen-q1s68-r1", Event: "SessionStart", Session: "s-rv", Transcript: "/home/.claude/projects/-repo/s-rv.jsonl"}}`;
     - `r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }`.
   - **When:** `r.run()`.
   - **Then:** the `agent-transcript` events are exactly one, with `Phase: "3"`, `Step: "implement"` and `Fields: {"agent": "rloop-2kuxv-p3-implemen-q1s68-r1", "attempt": "1", "reviewer": "claude", "round": "1", "session_id": "s-rv", "transcript_path": "/home/.claude/projects/-repo/s-rv.jsonl"}`.

5. **`TestAReopenedReviewerRecordsItsOwnTranscriptNotItsPredecessors`** — the reopen edge (`internal/core/review.go:704-721`, same agent name).
   - **Given:**
     - `newReviewRig(t, Reviewer{Provider: "codex"})`, `failCodexOnce`, a feed, and `hookedResolve`;
     - `startingHost` pushes `{{At: t1, Agent: codexReviewer, Event: "SessionStart", Session: "s-old", Transcript: "/t/s-old.jsonl"}}` and then `{{At: t1, Agent: codexReviewer, Event: "SessionStart", Session: "s-new", Transcript: "/t/s-new.jsonl"}}`.
   - **When:** `r.runRaising(then(Resolution{Action: "retry", By: "watchdog"}, Resolution{Action: "block", By: "watchdog"}))`.
   - **Then:** the `transcript_path` values of the `agent-transcript` events equal `[]string{"/t/s-old.jsonl", "/t/s-new.jsonl"}`.

5a. **`TestAReviewerReopenedAfterItsPromptFailedRecordsItsPredecessorsTranscriptOnce`** — the reopen edge when the predecessor was never waited (`track` records and drops a same-name predecessor before `hookReset`).
   - **Given:**
     - `newReviewRig(t, Reviewer{Provider: "claude"})`, a feed, and `hookedResolve`;
     - `startingHost` pushes `{{At: t1, Agent: "rloop-2kuxv-p3-implemen-q1s68-r1", Event: "SessionStart", Session: "s-old", Transcript: "/t/s-old.jsonl"}}` and then `{{At: t1, Agent: "rloop-2kuxv-p3-implemen-q1s68-r1", Event: "SessionStart", Session: "s-new", Transcript: "/t/s-new.jsonl"}}`;
     - `r.sm.Prompts` wraps the rig's prompts and returns `errors.New("review template missing")` for the first `review` render only;
     - `behave` writes ok reviews.
   - **When:** `reviewed, _ := r.runRaising(then(Resolution{Action: "retry", By: "watchdog"}))`, then `out := r.sm.Finish(r.worker, reviewed)`.
   - **Then:**
     - `assertState(t, out, StepOK, "")`;
     - the `transcript_path` values of the `agent-transcript` events equal `[]string{"/t/s-old.jsonl", "/t/s-new.jsonl"}`.

6. **`TestAFailedTranscriptRecordWarnsAndDoesNotFailTheStep`** — non-fatal.
   - **Given:**
     - `hookStepRig(t, <SessionStart s-1 as in test 1>)`;
     - `guard := &RecordGuard{Store: failingStore{Store: r.store, fail: func(rec Record) bool { return isEvent(rec, "agent-transcript") }}}`, assigned to `r.sm.Store`;
     - after `spawn`, push `stop(stepAgent)`, then an ok sentinel, `TreeChanges`, and herdr idle.
   - **When:** `Wait`.
   - **Then:**
     - `assertState(t, out, StepOK, "")`;
     - `guard.Failed()` is nil;
     - `warnings(face)` equals `[]string{"transcript: disk full"}`.

7. **`TestACodexWorkersRolloutIsRecordedAsItsTranscript`** — item 2, the worker's event and the lookup arguments.
   - **Given:**
     - `newRig(t)`, with `r.sm.Now = (&stepClock{t: t0}).Now`;
     - `Resolve` returns `ProviderArgs{Kind: "codex", Rollout: true}`;
     - `rollouts := &fakeRollouts{files: map[string]rolloutFile{filepath.Join(r.runDir, "phase-3", "implement-a1.sentinel"): {"/home/.codex/sessions/2026/10/13/rollout-2026-10-13T12-00-01-019a.jsonl", "019a"}}}`, set as `r.sm.Rollouts`;
     - after `spawn`, an ok sentinel, `TreeChanges`, and herdr idle.
   - **When:** `Wait`.
   - **Then:**
     - exactly one event with `Phase: "3"`, `Step: "implement"` and `Fields: {"agent": stepAgent, "attempt": "1", "session_id": "019a", "transcript_path": "/home/.codex/sessions/2026/10/13/rollout-2026-10-13T12-00-01-019a.jsonl"}`;
     - `rollouts.calls[0]` is `"/repo/.r-loop/wt/phase-3 " + <that sentinel> + " 2026-10-13T12:00:00Z"`.

8. **`TestACodexRolloutFoundOnALaterTickIsRecorded`** — item 2: the rollout is written lazily, after the prompt.
   - **Given:**
     - the rig of test 7, with `rollouts.misses = 1`;
     - `r.host.script` writes the ok sentinel on its 2nd call and returns idle.
   - **When:** `Wait`.
   - **Then:** exactly one `agent-transcript` event, with the test-7 path.

9. **`TestACodexRolloutIsNotLookedUpAgainOnceRecorded`** — one event, no repeated scans.
   - **Given:** the rig of test 7, with `r.host.script` writing the ok sentinel on its 3rd call.
   - **When:** `Wait`.
   - **Then:**
     - `len(rollouts.calls)` is 1;
     - `len(r.events("agent-transcript"))` is 1.

10. **`TestCodexReviewersInOneWorktreeEachRecordTheRolloutNamingTheirOwnSentinel`** — item 2 and the watchdog warning: never one file for two agents.
    - **Given:**
      - `newReviewRig(t, Reviewer{Provider: "codex", Name: "codex-a"}, Reviewer{Provider: "codex", Name: "codex-b"})`, with `r.sm.Now = (&stepClock{t: t0}).Now` and `rolloutResolve(r)`;
      - `r.sm.Rollouts` maps `<runDir>/phase-3/implement-rv-codex-a-r1-a1.sentinel` → `{"/x/rollout-a.jsonl", "a"}` and `…implement-rv-codex-b-r1-a1.sentinel` → `{"/x/rollout-b.jsonl", "b"}`;
      - `behave` writes ok reviews.
    - **When:** `r.run()`.
    - **Then:** the `reviewer`→`transcript_path` pairs of the `agent-transcript` events, as a map, equal `{"codex-a": "/x/rollout-a.jsonl", "codex-b": "/x/rollout-b.jsonl"}`.

11. **`TestNoRolloutLookupForASessionWhoseProviderWritesNone`** — the `Rollout` flag guard.
    - **Given:** `newRig(t)` (its `Resolve` returns `Rollout: false`), `rollouts := &fakeRollouts{}` set as `r.sm.Rollouts`, then spawn with an ok sentinel, `TreeChanges`, and herdr idle.
    - **When:** `Wait`.
    - **Then:**
      - `rollouts.calls` is nil;
      - `r.events("agent-transcript")` is nil.

11b. **`TestAClaudeReviewerWhosePromptFailsRecordsItsTranscriptWhenTheStepFinishes`** — item 1 on the reviewer prompt-failure path (`Finish` sweep; codex-r1-1).
   - **Given:**
     - `newReviewRig(t, Reviewer{Provider: "claude"})`, a `fakeHookFeed`, `hookedResolve` and `shortHookStartWait`;
     - `startingHost` pushes `{{At: t1, Agent: "rloop-2kuxv-p3-implemen-q1s68-r1", Event: "PreToolUse", Tool: "Read", Target: "/repo/a.go", Session: "s-rv", Transcript: "/home/.claude/projects/-repo/s-rv.jsonl"}}`;
     - `r.sm.Prompts = promptsFunc(func(string, map[string]any) (string, string, error) { return "", "", errors.New("review template missing") })`.
   - **When:** `out := r.sm.Finish(r.worker, r.run())`.
   - **Then:**
     - `assertState(t, out, StepFailed, "reviewer claude: review template missing")`;
     - the `agent-transcript` events are exactly one, with `Fields: {"agent": "rloop-2kuxv-p3-implemen-q1s68-r1", "attempt": "1", "reviewer": "claude", "round": "1", "session_id": "s-rv", "transcript_path": "/home/.claude/projects/-repo/s-rv.jsonl"}`.

11c. **`TestAStepCancelledBeforeItsFirstTickRecordsItsCodexRolloutWhenItFinishes`** — item 2 for an aborted step (codex-r1-2, codex-r2-4).
   - **Given:**
     - the rig of test 7, with `r.sm.Poll = time.Hour` so no tick ever fires. Nothing in `Spawn` looks a rollout up, so the only lookup is `Finish`'s.
     - `ctx, cancel := context.WithCancel(context.Background())`, then `cancel()`.
   - **When:** `out := singleRunner{sm: r.sm}.Run(ctx, ref, &recObserver{})`, with `ref` the test-7 ref.
   - **Then:**
     - `assertState(t, out, StepFailed, "interrupted: context canceled")`;
     - `rollouts.calls` equals `[]string{"/repo/.r-loop/wt/phase-3 " + filepath.Join(r.runDir, "phase-3", "implement-a1.sentinel") + " 2026-10-13T12:00:00Z"}`;
     - `r.events("agent-transcript")` is exactly one event with `Fields: {"agent": stepAgent, "attempt": "1", "session_id": "019a", "transcript_path": "/home/.codex/sessions/2026/10/13/rollout-2026-10-13T12-00-01-019a.jsonl"}`.

11d. **`TestAStepCancelledBeforeItsFirstTickRecordsAClaudeHookQueuedAfterItsStart`** — item 1 for an aborted step: the final drain in `Finish` (codex-r2-4).
   - **Given:**
     - `hookStepRig(t)` with no SessionStart, `shortHookStartWait` and `r.sm.Poll = time.Hour`;
     - `s := r.spawn(t, 1)`;
     - then `feed.push(HookEvent{At: t1, Agent: stepAgent, Event: "PreToolUse", Tool: "Read", Target: "/repo/a.go", Session: "s-1", Transcript: "/home/.claude/projects/-repo/s-1.jsonl"})`;
     - an already-cancelled `ctx`.
   - **When:** `out := r.sm.Finish(s, r.sm.Wait(ctx, s, &recObserver{}))` — the runner's own sequence (`internal/core/runners.go:21-26`).
   - **Then:**
     - `assertState(t, out, StepFailed, "interrupted: context canceled")`;
     - `r.events("agent-transcript")` is exactly one event with `Fields: {"agent": stepAgent, "attempt": "1", "session_id": "s-1", "transcript_path": "/home/.claude/projects/-repo/s-1.jsonl"}`.

11e. **`TestAnEarlierReviewersRolloutIsRecordedWhenASiblingsPromptFailureEndsTheRound`** — codex-r2-1.
   - **Given:**
     - `newReviewRig(t, Reviewer{Provider: "codex", Name: "codex-a"}, Reviewer{Provider: "codex", Name: "codex-b"})`, with `r.sm.Now = (&stepClock{t: t0}).Now` and `rolloutResolve(r)`;
     - `r.sm.Rollouts` maps `<runDir>/phase-3/implement-rv-codex-a-r1-a1.sentinel` → `{"/x/rollout-a.jsonl", "a"}`;
     - `r.sm.Prompts` wraps the rig's prompts and returns `errors.New("review template missing")` when `vars["FindingsPath"]` contains `codex-b`.
   - **When:** `out := r.sm.Finish(r.worker, r.run())`.
   - **Then:**
     - `assertState(t, out, StepFailed, "reviewer codex-b: review template missing")`;
     - the `reviewer`→`transcript_path` pairs of the `agent-transcript` events, as a map, equal `{"codex-a": "/x/rollout-a.jsonl"}`.

11f. **`TestAResumedWorkersFirstHookIsRecordedWhileOnlyItsReviewersAreWaited`** — codex-r2-2 (`ReviewFrom > 0`, no worker wait, clean round).
   - **Given:**
     - `newReviewRig(t, Reviewer{Provider: "codex"})` and `feed := &fakeHookFeed{}` as `r.sm.Hooks`;
     - `ref := r.worker.Ref`, with `ref.Key.Attempt = 2` and `ref.ReviewFrom = 1`;
     - `behave` pushes `HookEvent{At: t1, Agent: "rloop-2kuxv-p3-implement-a2", Event: "PreToolUse", Tool: "Read", Target: "/repo/a.go", Session: "s-w", Transcript: "/home/.claude/projects/-repo/s-w.jsonl"}`, then `writeReview(t, vars, "ok", 0)`.
   - **When:** `out := singleRunner{sm: r.sm, review: ReviewHalf{Sessions: r.sm, Store: r.store}.Run}.Run(context.Background(), ref, &recObserver{})`.
   - **Then:**
     - `assertState(t, out, StepOK, "")`;
     - `r.events("agent-transcript")` is exactly one event with `Fields: {"agent": "rloop-2kuxv-p3-implement-a2", "attempt": "2", "session_id": "s-w", "transcript_path": "/home/.claude/projects/-repo/s-w.jsonl"}`.

11g. **`TestAWorkerWhoseStartFailsAfterItsFirstHookRecordsItsTranscript`** — codex-r2-3, workers.
   - **Given:**
     - `hookStepRig(t)`, then `r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, pushes: [][]HookEvent{{{At: t0, Agent: stepAgent, Event: "SessionStart", Session: "s-1", Transcript: "/home/.claude/projects/-repo/s-1.jsonl"}}}, errs: []error{errors.New("herdr: agent rloop-2kuxv-p3-implement never became ready (state blocked)")}}`;
     - `startingHost.Start` is changed to push `h.pushes[n]` **before** it returns `h.errs[n]`, as herdr's `Start` can fail after the agent runs (`internal/herdr/client.go:252-254`). The one existing use of `errs` (`internal/core/hooks_test.go:1220`) has no push at the failing index, so it is unaffected.
   - **When:** `out := singleRunner{sm: r.sm}.Run(context.Background(), r.ref(1), &recObserver{})`.
   - **Then:**
     - `assertState(t, out, StepFailed, "spawn: herdr: agent rloop-2kuxv-p3-implement never became ready (state blocked)")`;
     - `r.events("agent-transcript")` is exactly one event with `Fields: {"agent": stepAgent, "attempt": "1", "session_id": "s-1", "transcript_path": "/home/.claude/projects/-repo/s-1.jsonl"}`.

11h. **`TestAReviewerWhoseStartFailsAfterItsFirstHookRecordsItsTranscript`** — codex-r2-3, reviewers.
   - **Given:**
     - `newReviewRig(t, Reviewer{Provider: "claude"})`, a feed, and `hookedResolve`;
     - `r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, pushes: [][]HookEvent{{{At: t1, Agent: "rloop-2kuxv-p3-implemen-q1s68-r1", Event: "SessionStart", Session: "s-rv", Transcript: "/home/.claude/projects/-repo/s-rv.jsonl"}}}, errs: []error{errors.New("start refused")}}`.
   - **When:** `out := r.sm.Finish(r.worker, r.run())`.
   - **Then:**
     - `assertState(t, out, StepFailed, "reviewer claude: start refused")`;
     - the `agent-transcript` events are exactly one, with `Fields: {"agent": "rloop-2kuxv-p3-implemen-q1s68-r1", "attempt": "1", "reviewer": "claude", "round": "1", "session_id": "s-rv", "transcript_path": "/home/.claude/projects/-repo/s-rv.jsonl"}`.

### `internal/core/report_test.go` — add

12. **`TestAHaltLineOfABlockedStepListsTheTranscriptsOfItsWorkerAndReviewers`** — item 3.
    - **Given:** a `RunState` with `Status: RunHalted` and these events:
      - `agent-transcript` phase 2 `implement`: `{attempt 1, agent w, transcript_path /home/.claude/projects/-wt/s-1.jsonl}`;
      - `agent-transcript` phase 2 `implement`: `{attempt 1, reviewer codex, round 1, transcript_path /home/.codex/sessions/2026/10/13/rollout-a.jsonl}`;
      - `agent-transcript` phase 2 `implement`: `{attempt 1, reviewer claude, round 1, transcript_path /home/.claude/projects/-wt/s-2.jsonl}`;
      - `agent-transcript` phase 2 `plan`: `{attempt 1, transcript_path /home/.claude/projects/-wt/s-0.jsonl}`;
      - `agent-transcript` phase 3 `implement`: `{attempt 1, transcript_path /home/.claude/projects/-wt/s-9.jsonl}`;
      - `phase-blocked` phase 2 `implement`: `{phase 2, reason tests red, workspace ws-2, worktree /wt/phase-2}`.
    - **When:** `Report(st, threePhasePlan())`.
    - **Then:** the output contains `"- phase 2 implement: tests red — workspace ws-2, worktree /wt/phase-2 — transcripts: worker a1 /home/.claude/projects/-wt/s-1.jsonl, reviewer codex a1 r1 /home/.codex/sessions/2026/10/13/rollout-a.jsonl, reviewer claude a1 r1 /home/.claude/projects/-wt/s-2.jsonl\n"`.

13. **`TestAHaltLineOfAnAbortedStepListsItsTranscripts`** — item 3 (aborted).
    - **Given:** `Status: RunHalted`; `agent-transcript` phase 2 `implement` `{attempt 1, transcript_path /t/w.jsonl}`; then `aborted` phase 2 `implement` `{workspace ws-2, worktree /wt/phase-2}`.
    - **When:** `Report`.
    - **Then:** the output contains `"- aborted during phase 2 implement — workspace ws-2, worktree /wt/phase-2 — transcripts: worker a1 /t/w.jsonl\n"`.

14. **`TestAHaltLineOfAStepWithoutTranscriptsRendersAsBefore`** — item 3 (none).
    - **Given:** `Status: RunHalted`; `agent-transcript` phase 1 `plan` `{attempt 1, transcript_path /t/p.jsonl}`; then `phase-blocked` phase 2 `implement` `{phase 2, reason tests red, workspace ws-2, worktree /wt/phase-2}`.
    - **When:** `Report`.
    - **Then:** the output contains `"- phase 2 implement: tests red — workspace ws-2, worktree /wt/phase-2\n"`.

### `internal/hooktap/hooktap_test.go` — add

15. **`TestTapRecordsTheSessionIdAndTranscriptPath`** — item 1, the tap.
    - **Given:** `out` in `t.TempDir()`, and `feed := NewFeed(out)`.
    - **When:** `Tap(strings.NewReader(`{"hook_event_name":"SessionStart","session_id":"s-1","transcript_path":"/home/.claude/projects/-wt/s-1.jsonl"}`), out, env("rloop-a"))`, then `feed.Read()`.
    - **Then:** one event with `Session` `s-1` and `Transcript` `/home/.claude/projects/-wt/s-1.jsonl`.

### `internal/rollout/rollout_test.go` (new)

Fixture helpers in the test file:
- `writeRollout(t, home, day, name string, lines ...string) string` writes `home/sessions/<day>/<name>` and returns its path.
- `metaLine(id, start, cwd string) string` returns `{"timestamp":"2026-10-13T13:00:00.000Z","type":"session_meta","payload":{"id":<id>,"timestamp":<start>,"cwd":<cwd>,"source":"cli"}}`.
- `subagentMetaLine(id, start, cwd string) string` returns the same line with `"source":{"subagent":{"thread_spawn":{"parent_thread_id":"root","depth":1}}}`.
- `userLine(text string) string` and `itemLine(role, payloadType, text string) string` JSON-encode a `response_item`. Use `json.Marshal` for the text.

All tests use cwd `/wt/phase-3`, `since = 2026-10-13T12:00:00Z`, and marker `/runs/r/phase-3/implement-rv-codex-r1-a1.sentinel`.

16. **`TestFindReturnsTheRolloutWhoseUserMessageNamesTheMarker`** — item 2.
    - **Given:** one file `2026/10/13/rollout-1.jsonl` with `metaLine("019a", "2026-10-13T12:00:01.000Z", "/wt/phase-3")` and `userLine("write the sentinel to `" + marker + "`")`.
    - **When:** `Finder{Home: home}.Find("/wt/phase-3", marker, since)`.
    - **Then:** `(path, "019a")`.

17. **`TestFindTellsApartReviewersStartedSecondsApartInOneWorktree`** — the watchdog warning, not "newest file".
    - **Given:** four files in `2026/10/13`, all with cwd `/wt/phase-3`:
      - starts `12:00:01`, `12:00:02` and `12:00:03`, naming the markers for `codex-a`, `codex-b` and `codex-c` (`…implement-rv-codex-a-r1-a1.sentinel` and so on);
      - start `12:00:04`, a native-review rollout whose only user line is `review the uncommitted changes`.
    - **When:** `Find` with the `codex-b` marker.
    - **Then:** the `12:00:02` file and its id.

18. **`TestFindSkipsARolloutInAnotherWorktree`** — the item 2 cwd rule.
    - **Given:** one file naming the marker with cwd `/wt/phase-4`.
    - **When:** `Find("/wt/phase-3", …)`.
    - **Then:** `("", "")`.

19. **`TestFindJudgesTheStartByThePayloadTimestamp`** — the item 2 start rule and the watchdog's `payload.timestamp` note. Table, one file per row:

    | name | payload start | expected |
    |---|---|---|
    | started a second before, written after | `2026-10-13T11:59:59.000Z` | `""` |
    | started in the same millisecond as since | `2026-10-13T12:00:00.000Z`, with `since = 2026-10-13T12:00:00.000400Z` | the file |
    | started after since | `2026-10-13T12:00:02.000Z` | the file |

    The top-level timestamp is always `13:00Z`.

20. **`TestFindIgnoresTheMarkerOutsideAUserMessage`** — another session's sentinel in tool output.
    - **Given:** table rows, each a file whose only line naming the marker is:
      - `itemLine("developer", "message", marker)`;
      - `itemLine("assistant", "message", marker)`;
      - a `function_call_output` line: `{"type":"response_item","payload":{"type":"function_call_output","output":"<marker>"}}`.
    - **When:** `Find`.
    - **Then:** `("", "")`.

21. **`TestFindNeverReturnsASubagentRolloutNamingTheMarker`** — a child that is readable before its parent is never pinned (codex-r1-4). Table, same marker and cwd `/wt/phase-3` in every row:

    | name | files | expected |
    |---|---|---|
    | child readable, parent not yet written | `rollout-2026-10-13T12-00-01-child.jsonl` with `subagentMetaLine("child", "2026-10-13T12:00:01.000Z", …)` and a user line naming the marker | `("", "")` |
    | child sorts before its parent | the same child file, plus `rollout-2026-10-13T12-00-02-root.jsonl` with `metaLine("root", "2026-10-13T12:00:02.000Z", …)` and a user line naming the marker | the root file and `root` |

    - **When:** `Find`.
    - **Then:** the expected pair.

22. **`TestFindMatchesAMarkerThatIsEscapedInTheRolloutJSON`** — a sentinel under a directory holding `"` or `\` (codex-r1-3). Table, one file per row, the user line built with `userLine` (which `json.Marshal`s the text, escaping `"` as `\"` and `\` as `\\`):

    | name | marker |
    |---|---|
    | quote in the repository path | `/repo"demo/.r-loop/runs/r/phase-3/implement-a1.sentinel` |
    | backslash in the repository path | `/repo\demo/.r-loop/runs/r/phase-3/implement-a1.sentinel` |

    - **When:** `Find("/wt/phase-3", marker, since)`.
    - **Then:** that file and its id.

23. **`TestFindLooksInTheDayBeforeSinceForALocalDateBehindUTC`** — the day-directory window.
    - **Given:** `since = 2026-10-13T00:30:00Z` and a file in `2026/10/12` with start `2026-10-13T00:30:05.000Z` naming the marker.
    - **When:** `Find`.
    - **Then:** that file.

24. **`TestFindSkipsAMalformedRollout`** — bad files never stop the search.
    - **Given:** `rollout-0.jsonl` whose first line is `not json`, and a valid matching `rollout-1.jsonl`.
    - **When:** `Find`.
    - **Then:** `rollout-1.jsonl`.

25. **`TestFindReturnsNothingWithoutASessionsDirectory`** — a fresh machine with no codex sessions.
    - **Given:** `home` is an empty `t.TempDir()`.
    - **When:** `Find`.
    - **Then:** `("", "")`.

### `internal/app/app_test.go` — add

26. **`TestResolveMarksOnlyACodexProviderAsWritingARollout`** — item 2, codex only. Modelled on `TestResolveMarksOnlyAClaudeProviderGivenTheHookSettingsAsHooked` (`:1314`). Table:

    | provider | expected |
    |---|---|
    | `codex` | true |
    | `claude` | false |
    | `pdev` (`registryDirWithPdev`) | false |

    - **When:** `w.resolve(provider, "", "", "", "", "")`.
    - **Then:** `args.Rollout` matches the expected value.

## Left out

- **Recording inside `drainHooks`.** `drainHooks` holds `hooks.mu`, which `recordTranscript` takes again through `hookAgent`. The tracked-session sweep after each drain on a tick, and in `Finish`, reaches every session without it.
- **A claimed-file set in `SessionManager`.** Each session's marker is its own sentinel path, unique per kind, reviewer, round and attempt. A file can only match the session whose prompt it holds, a same-name predecessor is excluded by the start filter (test 19), and a sub-agent that inherits the prompt is excluded by its source (test 21); see also tests 10 and 17. The set would guard a state the matching rule already rules out.
- **Showing `agent-transcript` in the faces.** The item asks for the run log, and `report.md` carries the paths.
- **Transcripts on the Automatic-decisions "phase N blocked" line.** A blocked phase always ends the run halted (`internal/core/loop.go:266-276`), so the Halt step line is always rendered for it.
- **Transcripts for the watchdog and the fixer.** The watchdog's pane sets no `R_LOOP_AGENT` (`internal/hooktap/hooktap.go:31-34`; only `session.go:200`, `review.go:503` and `fixer.go:376` set it), so its hooks are never tapped. The fixer is not a phase step and is not driven by `SessionManager`. The report lists workers and reviewers only.
- **An mtime pre-filter, or caching scanned files, in `internal/rollout`.** The scan touches only the day directories from `since` on, and stops for a session once found.
- **A wiring test for `SessionManager.Rollouts`.** It is one struct-literal line. Its two inputs are tested: the `Rollout` flag (test 26) and `Finder` (tests 16-25).

## Assumptions

- **The watchdog's warning on matching.** Resolved by requiring a user message that contains the session's sentinel path, plus `payload.cwd`, `payload.timestamp ≥ Host.Start` and a non-sub-agent `payload.source`, the first match in path order winning:
  - reviewers sharing a cwd are told apart (test 17);
  - a native `codex review` rollout never names a sentinel (test 17);
  - one file is never handed to two agents (tests 10, 17 and 21; no claimed-set needed, see Left out);
  - the start is `payload.timestamp`, not the later top-level timestamp (test 19).
- **The tap does not cap `session_id` or `transcript_path`.** A path a cap cuts points nowhere. Both values are short: a UUID, and a path bounded by `PATH_MAX`.
- **The worktree comparison is `filepath.Clean` equality, with no symlink resolution.** `Repo.Root()` comes from `git rev-parse --show-toplevel`, which is already the real path, and codex records its real cwd.
- **The finder decodes every line after `session_meta`.** There is no raw-byte pre-filter, so a JSON-escaped sentinel path still matches (test 22). The cost is bounded: only files in the session's cwd that started after it are read past their first line, and a session stops looking once found.
- **codex marks a sub-agent with an object `payload.source`.** This machine's rollouts show `"cli"` and `"exec"` for root sessions and `{"subagent": …}` for every sub-agent. A missing or non-string source is treated as not a root session.
- **`RolloutFinder.Find` returns no error.** An unreadable or malformed file is skipped, and a missing `sessions` directory is "not found yet". There is no caller action for either, and the lookup retries on the next tick.
- **The first transcript wins.** Only the first hook that carries a `transcript_path` is recorded. A later `/clear` (a new SessionStart with a new transcript) adds no second event, per "one event".
- **A codex session whose prompt never reaches codex gets no event.** Its rollout has no user message naming the sentinel yet. That is expected, not a failure.
- **Reviewer events carry the worker's `Phase`/`Step`.** They also carry `reviewer` and `round` fields, as `ReviewHalf.event` does, so "that phase and step" in the report collects worker and reviewers together.
- **The report label is `worker a<attempt>` or `reviewer <id> a<attempt> r<round>`, followed by the path, joined with `, `.**

## Gate

`go test ./internal/core/ ./internal/hooktap/ ./internal/rollout/ ./internal/app/ -run '^(TestAClaudeAgentsFirstHookRecordsOneAgentTranscriptEvent|TestAClaudeAgentWithoutASessionStartRecordsTheTranscriptOfItsFirstLaterHook|TestAClaudeWorkerWhosePromptFailsRecordsItsTranscriptWhenTheStepFinishes|TestAClaudeReviewersTranscriptNamesItsReviewerAndRound|TestAReopenedReviewerRecordsItsOwnTranscriptNotItsPredecessors|TestAReviewerReopenedAfterItsPromptFailedRecordsItsPredecessorsTranscriptOnce|TestAFailedTranscriptRecordWarnsAndDoesNotFailTheStep|TestACodexWorkersRolloutIsRecordedAsItsTranscript|TestACodexRolloutFoundOnALaterTickIsRecorded|TestACodexRolloutIsNotLookedUpAgainOnceRecorded|TestCodexReviewersInOneWorktreeEachRecordTheRolloutNamingTheirOwnSentinel|TestNoRolloutLookupForASessionWhoseProviderWritesNone|TestAClaudeReviewerWhosePromptFailsRecordsItsTranscriptWhenTheStepFinishes|TestAStepCancelledBeforeItsFirstTickRecordsItsCodexRolloutWhenItFinishes|TestAStepCancelledBeforeItsFirstTickRecordsAClaudeHookQueuedAfterItsStart|TestAnEarlierReviewersRolloutIsRecordedWhenASiblingsPromptFailureEndsTheRound|TestAResumedWorkersFirstHookIsRecordedWhileOnlyItsReviewersAreWaited|TestAWorkerWhoseStartFailsAfterItsFirstHookRecordsItsTranscript|TestAReviewerWhoseStartFailsAfterItsFirstHookRecordsItsTranscript|TestAHaltLineOfABlockedStepListsTheTranscriptsOfItsWorkerAndReviewers|TestAHaltLineOfAnAbortedStepListsItsTranscripts|TestAHaltLineOfAStepWithoutTranscriptsRendersAsBefore|TestTapRecordsTheSessionIdAndTranscriptPath|TestFindReturnsTheRolloutWhoseUserMessageNamesTheMarker|TestFindTellsApartReviewersStartedSecondsApartInOneWorktree|TestFindSkipsARolloutInAnotherWorktree|TestFindJudgesTheStartByThePayloadTimestamp|TestFindIgnoresTheMarkerOutsideAUserMessage|TestFindNeverReturnsASubagentRolloutNamingTheMarker|TestFindMatchesAMarkerThatIsEscapedInTheRolloutJSON|TestFindLooksInTheDayBeforeSinceForALocalDateBehindUTC|TestFindSkipsAMalformedRollout|TestFindReturnsNothingWithoutASessionsDirectory|TestResolveMarksOnlyACodexProviderAsWritingARollout)$'`
