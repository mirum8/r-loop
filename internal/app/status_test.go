package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/notify"
	"r-loop/internal/store"
)

const statusTodo = `# t

### Phase 1 — one
**Depends on:** —
- [x] a

### Phase 2 — two
**Depends on:** Phase 1
- [ ] b

### Phase 3 — three
**Depends on:** Phase 2
- [ ] c

### Phase 4 — four
**Depends on:** Phase 1
- [ ] d

### Phase 5 — five
**Depends on:** Phase 1
- [ ] e
`

var t0 = time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

func TestStatusAndAbortLeaveAFreshRepositoryUntouched(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{{"status", "--plain"}, {"abort"}} {
		f.out.Reset()
		f.err.Reset()
		code := f.main(args...)
		if args[0] == "status" && (code != 0 || f.out.String() != "no run\n") {
			t.Fatalf("status code=%d stdout=%q stderr=%q", code, f.out.String(), f.err.String())
		}
		if args[0] == "abort" && code != 2 {
			t.Fatalf("abort code=%d stderr=%q", code, f.err.String())
		}
		if _, err := os.Stat(filepath.Join(f.root, ".r-loop")); !os.IsNotExist(err) {
			t.Fatalf("%s created .r-loop: %v", args[0], err)
		}
	}
}

func TestStatusSelectsANewerCreatedRunWithRecordedEvents(t *testing.T) {
	f := newFixture(t)
	older := f.seedRun(core.Record{Kind: core.RecordRun, Run: core.RunHalted})
	newer := f.seedRun(core.Record{Kind: core.RecordEvent, Event: &core.Event{Kind: "triage-start"}})
	if older >= newer {
		t.Fatalf("ids %s %s", older, newer)
	}
	if code := f.main("status", "--plain"); code != 0 || !strings.HasPrefix(f.out.String(), "run "+newer+" created\n") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, f.out.String(), f.err.String())
	}
}

func TestStatusSkipsANewerCreatedRunWithOnlyWatchdogStart(t *testing.T) {
	f := newFixture(t)
	older := f.seedRun(core.Record{Kind: core.RecordRun, Run: core.RunHalted})
	newer := f.seedRun(
		core.Record{Kind: core.RecordEvent, Event: &core.Event{Kind: "watchdog-stale-closed"}},
		core.Record{Kind: core.RecordEvent, Event: &core.Event{Kind: "watchdog-start"}},
	)
	if older >= newer {
		t.Fatalf("ids %s %s", older, newer)
	}
	if code := f.main("status", "--plain"); code != 0 || !strings.HasPrefix(f.out.String(), "run "+older+" halted\n") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, f.out.String(), f.err.String())
	}
}

func (f *fixture) seedRun(recs ...core.Record) string {
	f.t.Helper()
	f.write("docs/topic/todo.md", statusTodo)
	st := store.New(f.root)
	id, err := st.Create(core.RunMeta{Todo: f.todo, Started: t0})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, r := range recs {
		if err := st.Append(id, r); err != nil {
			f.t.Fatal(err)
		}
	}
	return id
}

func ev(at time.Time, kind string, phase int, step string, fields map[string]string) core.Record {
	return core.Record{Kind: core.RecordEvent, At: at, Event: &core.Event{At: at, Kind: kind, Phase: strconv.Itoa(phase), Step: step, Fields: fields}}
}

func stepRec(phase int, kind string, attempt int, state core.StepState) core.Record {
	return core.Record{Kind: core.RecordStep, Step: &core.StepKey{Phase: strconv.Itoa(phase), Kind: kind, Attempt: attempt}, State: state}
}

func TestStatusPlainPrintsRunPhasesLiveStepQuestionAndWarning(t *testing.T) {
	f := newFixture(t)
	f.env.Now = func() time.Time { return t0.Add(12*time.Minute + 5*time.Second) }
	id := f.seedRun(
		core.Record{Kind: core.RecordRun, Run: core.RunRunning},
		ev(t0, "phase-start", 2, "", map[string]string{"phase": "2"}),
		ev(t0, "phase-blocked", 2, "implement", map[string]string{"phase": "2", "reason": "diff is empty"}),
		ev(t0, "phase-skipped", 3, "", map[string]string{"phase": "3", "because": "2"}),
		ev(t0, "phase-start", 4, "", map[string]string{"phase": "4"}),
		ev(t0, "phase-state", 4, "", map[string]string{"phase": "4", "state": "planned"}),
		stepRec(4, "implement", 1, core.StepQueued),
		ev(t0.Add(time.Minute), "step", 4, "implement", map[string]string{"state": "queued", "attempt": "1", "provider": "codex"}),
		stepRec(4, "implement", 1, core.StepSpawned),
		stepRec(4, "implement", 1, core.StepRunning),
		ev(t0.Add(2*time.Minute), "step", 4, "implement", map[string]string{"state": "running", "attempt": "1", "provider": "codex", "workspace": "w7"}),
		core.Record{Kind: core.RecordQuestion, Question: &core.Question{ID: "q1", Step: core.StepKey{Phase: "4", Kind: "implement"}, Text: "which db?"}},
		core.Record{Kind: core.RecordQuestion, Question: &core.Question{ID: "q2", Step: core.StepKey{Phase: "4", Kind: "implement"}, Text: "keep api?", Answer: "yes", AnsweredBy: "maintainer"}},
		ev(t0, "warning", 4, "implement", map[string]string{"reason": "diff grew past 3x the estimate"}),
	)

	code := f.main("status", "--plain")

	want := strings.Join([]string{
		"run " + id + " running",
		"phase 1 landed",
		"phase 2 blocked diff is empty",
		"phase 3 blocked waits on phase 2",
		"phase 4 planned",
		"phase 5 unticked",
		"live implement codex w7 11m5s",
		"question q1 which db?",
		"warning diff grew past 3x the estimate",
	}, "\n") + "\n"
	if code != 0 || f.out.String() != want {
		t.Fatalf("code=%d stderr=%q\ngot:\n%s\nwant:\n%s", code, f.err.String(), f.out.String(), want)
	}
}

func TestStatusPrintsOneLinePerBlocker(t *testing.T) {
	f := newFixture(t)
	f.seedRun(
		core.Record{Kind: core.RecordRun, Run: core.RunRunning},
		core.Record{Kind: core.RecordQuestion, Question: &core.Question{ID: "b1", Kind: core.QuestionBlocker, Step: core.StepKey{Phase: "4", Kind: "land"}, Text: "blocker b1 from phase-4/land (land): merge conflict\nactions: retry, block, stop; resolve it with resolve_blocker\n\nCONFLICT a.go", Answer: "block", AnsweredBy: "watchdog", Citation: "always"}},
		core.Record{Kind: core.RecordQuestion, Question: &core.Question{ID: "b2", Kind: core.QuestionBlocker, Step: core.StepKey{Phase: "5", Kind: "implement"}, Text: "blocker b2 from phase-5/implement (step): stalled\nactions: retry, switch, block, stop; resolve it with resolve_blocker"}},
	)

	code := f.main("status", "--plain")

	out := f.out.String()
	for _, want := range []string{"\nb1 phase-4/land (land): merge conflict → block (watchdog)\n", "\nb2 phase-5/implement (step): stalled (open)\n"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("code=%d, output lacks %q:\n%s", code, want, out)
		}
	}
	if strings.Contains(out, "CONFLICT") || strings.Contains(out, "question b") {
		t.Errorf("a blocker printed as a question or with its excerpt:\n%s", out)
	}
}

func TestStatusPrintsOneLinePerDialog(t *testing.T) {
	f := newFixture(t)
	key := core.StepKey{Phase: "4", Kind: "implement"}
	f.seedRun(
		core.Record{Kind: core.RecordRun, Run: core.RunRunning},
		core.Record{Kind: core.RecordQuestion, Question: &core.Question{ID: "q1", Step: key, Text: "which db?"}},
		core.Record{Kind: core.RecordQuestion, Question: &core.Question{ID: "d1", Kind: core.QuestionDialog, Step: key, Text: "Allow write?", Answer: "1 enter", AnsweredBy: "watchdog", Citation: "decline"}},
		core.Record{Kind: core.RecordQuestion, Question: &core.Question{ID: "d2", Kind: core.QuestionDialog, Step: key, Text: "Allow network?"}},
	)

	code := f.main("status", "--plain")

	out := f.out.String()
	for _, want := range []string{"\nquestion q1 which db?\n", "\nd1 phase-4/implement: dialog → 1 enter (watchdog, decline)\n", "\nd2 phase-4/implement: dialog (open)\n"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("code=%d, output lacks %q:\n%s", code, want, out)
		}
	}
	if strings.Contains(out, "Allow") || strings.Contains(out, "question d") {
		t.Errorf("a dialog printed as a question or with its screen:\n%s", out)
	}
}

func TestStatusReadsTheCurrentRunOverTheNewest(t *testing.T) {
	f := newFixture(t)
	older := f.seedRun(core.Record{Kind: core.RecordRun, Run: core.RunHalted})
	newer := f.seedRun(core.Record{Kind: core.RecordRun, Run: core.RunFinished})
	if older >= newer {
		t.Fatalf("ids %s %s", older, newer)
	}

	f.main("status", "--plain")
	if !strings.HasPrefix(f.out.String(), "run "+newer+" finished\n") {
		t.Fatalf("newest: %q", f.out.String())
	}

	st := store.New(f.root)
	lock, err := st.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release("", 0)
	if err := st.SetCurrent(older, 1); err != nil {
		t.Fatal(err)
	}
	lock.Publish()
	f.out.Reset()
	f.main("status", "--plain")
	if !strings.HasPrefix(f.out.String(), "run "+older+" halted\n") {
		t.Fatalf("current: %q", f.out.String())
	}
}

func TestStatusShowsNoLiveLineOnceTheStepEnded(t *testing.T) {
	f := newFixture(t)
	f.seedRun(
		core.Record{Kind: core.RecordRun, Run: core.RunRunning},
		stepRec(2, "plan", 1, core.StepRunning),
		ev(t0, "step", 2, "plan", map[string]string{"state": "running", "provider": "claude", "workspace": "w1"}),
		stepRec(2, "plan", 1, core.StepOK),
	)

	f.main("status", "--plain")

	if strings.Contains(f.out.String(), "live ") {
		t.Fatalf("got live line:\n%s", f.out.String())
	}
}

func TestStatusOfARunWhoseDriverDiedSaysSoAndClaimsNoLiveStep(t *testing.T) {
	f := newFixture(t)
	id := f.seedRun(
		core.Record{Kind: core.RecordRun, Run: core.RunRunning},
		stepRec(2, "implement", 1, core.StepRunning),
		ev(t0, "step", 2, "implement", map[string]string{"state": "running", "attempt": "1", "provider": "claude", "workspace": "w1"}),
	)
	if err := store.New(f.root).SetCurrent(id, 999999); err != nil {
		t.Fatal(err)
	}

	f.main("status", "--plain")

	out := f.out.String()
	if !strings.HasPrefix(out, "run "+id+" running (driver pid 999999 not alive — r-loop resume)\n") {
		t.Fatalf("first line:\n%s", out)
	}
	if strings.Contains(out, "\nlive ") {
		t.Fatalf("claims a live step:\n%s", out)
	}
}

func TestStatusMarksPhasesOutsideTheRunList(t *testing.T) {
	f := newFixture(t)
	f.seedRun(
		ev(t0, "run-list", 0, "", map[string]string{"phases": "2,4"}),
		core.Record{Kind: core.RecordRun, Run: core.RunHalted},
	)

	f.main("status", "--plain")

	for _, want := range []string{"phase 1 landed\n", "phase 2 unticked\n", "phase 3 not in this run\n", "phase 4 unticked\n", "phase 5 not in this run\n"} {
		if !strings.Contains(f.out.String(), want) {
			t.Errorf("%q missing:\n%s", want, f.out)
		}
	}
}

func TestStatusWithNoRunPrintsNoRun(t *testing.T) {
	f := newFixture(t)

	code := f.main("status", "--plain")

	if code != 0 || f.out.String() != "no run\n" {
		t.Fatalf("code=%d out=%q err=%q", code, f.out.String(), f.err.String())
	}
}

func TestWirePassesTheShellNotifierAndTheConfiguredHooks(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "notify:\n  onHalt: say halted\n  onWarn: say warn\n  onDone: say done\n")
	f.commit()

	w, err := f.preflight(f.todo, "--plain")
	if err != nil {
		t.Fatal(err)
	}

	sh, ok := w.Loop.Notifier.(*notify.Shell)
	if !ok || sh.Emit == nil || sh.Log != filepath.Join(w.Store.Dir(w.Loop.RunID), "notify.log") {
		t.Fatalf("notifier %#v", w.Loop.Notifier)
	}
	if w.Loop.Hooks != (core.Hooks{OnHalt: "say halted", OnWarn: "say warn", OnDone: "say done"}) {
		t.Fatalf("hooks %+v", w.Loop.Hooks)
	}
}

func TestStatusShowsANamedRunOverANewerOne(t *testing.T) {
	f := newFixture(t)
	older := f.seedRun(core.Record{Kind: core.RecordRun, Run: core.RunHalted})
	newer := f.seedRun(core.Record{Kind: core.RecordRun, Run: core.RunFinished})
	if older >= newer {
		t.Fatalf("ids %s %s", older, newer)
	}
	if code := f.main("status", "--plain", older); code != 0 || !strings.HasPrefix(f.out.String(), "run "+older+" halted\n") {
		t.Fatalf("code=%d out=%q err=%q", code, f.out, f.err)
	}
}

func TestStatusUnknownRunIDExits2NamingIt(t *testing.T) {
	for _, id := range []string{"20990101-000000", "../x"} {
		t.Run(id, func(t *testing.T) {
			f := newFixture(t)
			if code := f.main("status", id); code != 2 || !strings.Contains(f.err.String(), "no run "+id) {
				t.Fatalf("code=%d err=%q", code, f.err)
			}
		})
	}
}

func TestStatusSkipsANewerRunThatRecordedNoProgress(t *testing.T) {
	f := newFixture(t)
	older := f.seedRun(core.Record{Kind: core.RecordRun, Run: core.RunHalted})
	f.seedRun()
	if code := f.main("status", "--plain"); code != 0 || !strings.HasPrefix(f.out.String(), "run "+older+" halted\n") {
		t.Fatalf("code=%d out=%q err=%q", code, f.out, f.err)
	}
}

func TestStatusSkipsARunWithUnreadableMetaWithAWarning(t *testing.T) {
	f := newFixture(t)
	older := f.seedRun(core.Record{Kind: core.RecordRun, Run: core.RunHalted})
	newer := f.seedRun()
	if err := os.Remove(filepath.Join(store.New(f.root).Dir(newer), "meta.json")); err != nil {
		t.Fatal(err)
	}
	if code := f.main("status", "--plain"); code != 0 || !strings.HasPrefix(f.out.String(), "run "+older+" halted\n") || !strings.Contains(f.err.String(), "skipped run "+newer) {
		t.Fatalf("code=%d out=%q err=%q", code, f.out, f.err)
	}
}

func TestStatusDoesNotSkipARunWithACorruptRecordLine(t *testing.T) {
	f := newFixture(t)
	f.seedRun(core.Record{Kind: core.RecordRun, Run: core.RunHalted})
	newer := f.seedRun()
	path := filepath.Join(store.New(f.root).Dir(newer), "events.jsonl")
	if err := os.WriteFile(path, []byte("garbage\n{\"Kind\":\"run\",\"Run\":\"running\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := f.main("status", "--plain"); code != 2 || !strings.Contains(f.err.String(), "events.jsonl line 1") {
		t.Fatalf("code=%d err=%q", code, f.err)
	}
}
