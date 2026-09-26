package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type orderStore struct {
	Store
	log *callLog
}

func (s orderStore) Append(runID string, rec Record) error {
	label := rec.Kind
	if rec.Event != nil {
		label += "/" + rec.Event.Kind
	} else if rec.Kind == RecordStep {
		label += "/" + string(rec.State)
	}
	s.log.record("append %s", label)
	return s.Store.Append(runID, rec)
}

type failingStore struct {
	Store
	fail func(Record) bool
}

type recordFailureWatcher struct {
	*fakeWatcher
	holds []StepKey
}

func (w *recordFailureWatcher) Hold(key StepKey) {
	w.holds = append(w.holds, key)
}

func (w *recordFailureWatcher) Release(StepKey) {}

func (s failingStore) Append(runID string, rec Record) error {
	if s.fail(rec) {
		return errors.New("disk full")
	}
	return s.Store.Append(runID, rec)
}

func isEvent(rec Record, kind string) bool {
	return rec.Kind == RecordEvent && rec.Event != nil && rec.Event.Kind == kind
}

func hasCall(calls []string, prefix string) bool {
	return slices.ContainsFunc(calls, func(call string) bool { return strings.HasPrefix(call, prefix) })
}

func hasCommitMessage(calls []string, message string) bool {
	return slices.ContainsFunc(calls, func(call string) bool {
		return strings.HasPrefix(call, "Repo.CommitAll ") && strings.Contains(call, message)
	})
}

func orderedCalls(t *testing.T, calls []string, prefixes ...string) {
	t.Helper()
	start := 0
	for _, prefix := range prefixes {
		found := false
		for start < len(calls) {
			if strings.HasPrefix(calls[start], prefix) {
				found = true
				start++
				break
			}
			start++
		}
		if !found {
			t.Fatalf("missing %q in order; calls = %q", prefix, calls)
		}
	}
}

func TestFatalRecordPolicyForEveryRecordKind(t *testing.T) {
	for _, kind := range []string{RecordStep, RecordRun, RecordLanding} {
		if !FatalRecord(Record{Kind: kind}) {
			t.Errorf("%s should be fatal", kind)
		}
	}
	for _, kind := range []string{"merge-intent", "commit-intent", "run-list", "step", "baseline", "snapshot", "review-round", "agent-named", "restart", "item-skipped", "gate-discovered"} {
		if !FatalRecord(Record{Kind: RecordEvent, Event: &Event{Kind: kind}}) {
			t.Errorf("event/%s should be fatal", kind)
		}
	}
	for _, kind := range []string{RecordQuestion, RecordSignal, RecordRemedy} {
		if FatalRecord(Record{Kind: kind}) {
			t.Errorf("%s should warn", kind)
		}
	}
	for _, kind := range []string{"warning", "phase-blocked", "nudge"} {
		if FatalRecord(Record{Kind: RecordEvent, Event: &Event{Kind: kind}}) {
			t.Errorf("event/%s should warn", kind)
		}
	}
	guard := &RecordGuard{Store: failingStore{Store: &fakeStore{}, fail: func(Record) bool { return true }}}
	if err := guard.Append("run-1", Record{Kind: RecordEvent, Event: &Event{Kind: "warning"}}); err == nil || err.Error() != "disk full" || guard.Failed() != nil {
		t.Fatalf("warning append = %v, latch = %v", err, guard.Failed())
	}
	if err := guard.Append("run-1", Record{Kind: RecordRun}); err == nil || err.Error() != "disk full" || guard.Failed() == nil {
		t.Fatalf("fatal append = %v, latch = %v", err, guard.Failed())
	}
	first := guard.Failed()
	guard.Append("run-1", Record{Kind: RecordStep})
	if guard.Failed() != first {
		t.Fatalf("first failure replaced: %v -> %v", first, guard.Failed())
	}
}

func TestAFollowingGuardFoldsEveryAppendIntoItsView(t *testing.T) {
	store := &fakeStore{}
	if err := store.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: "2"}}); err != nil {
		t.Fatal(err)
	}
	g := &RecordGuard{Store: store}
	ch, err := g.Follow("run-1")
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		key := StepKey{Run: "run-1", Phase: "1", Kind: "plan", Attempt: attempt}
		if err := g.Append("run-1", Record{Kind: RecordStep, Step: &key, State: StepRunning}); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: "1"}}); err != nil {
		t.Fatal(err)
	}
	if !g.Landed("1") || !g.Landed("2") || g.Landed("3") {
		t.Fatal("landed view is stale")
	}
	if n, ok := g.Latest("1", "plan"); !ok || n != 2 {
		t.Fatalf("latest = %d, %t", n, ok)
	}
	select {
	case <-ch:
	default:
		t.Fatal("append did not wake follower")
	}
	st, ok := g.Snapshot()
	if !ok || len(st.Landed) != 2 {
		t.Fatalf("snapshot = %+v, %t", st, ok)
	}
}

func TestAGuardThatIsNotFollowingKnowsNothing(t *testing.T) {
	store := &fakeStore{}
	g := &RecordGuard{Store: store}
	if g.Landed("1") {
		t.Fatal("landed before follow")
	}
	if n, ok := g.Latest("1", "plan"); ok || n != 0 {
		t.Fatalf("latest before follow = %d, %t", n, ok)
	}
	if _, ok := g.Snapshot(); ok {
		t.Fatal("snapshot before follow")
	}
	store.Err = errors.New("loaded")
	if _, err := g.Follow("run-1"); !errors.Is(err, store.Err) {
		t.Fatalf("follow = %v", err)
	}
	if n, ok := g.Latest("1", "plan"); ok || n != 0 {
		t.Fatalf("latest after failed follow = %d, %t", n, ok)
	}
}

func TestFinishRecordsTheCommitIntentThenCommitsThenRecordsOk(t *testing.T) {
	r := newRig(t)
	r.sm.Store = &RecordGuard{Store: orderStore{Store: r.store, log: r.shared}}
	s := r.spawn(t, 1)
	out := r.sm.Finish(s, Outcome{State: StepOK, Session: s})
	if out.State != StepOK {
		t.Fatalf("finish = %+v", out)
	}
	orderedCalls(t, r.shared.Calls(), "append event/commit-intent", "Repo.CommitAll "+s.Dir+" \"r-loop: phase 3 implement\"", "append step/ok")
	events := r.events("commit-intent")
	if len(events) != 1 {
		t.Fatalf("intents = %+v", events)
	}
	want := map[string]string{"attempt": "1", "head": "sha-start", "tree": "tree-start", "dir": s.Dir, "message": "r-loop: phase 3 implement"}
	for key, value := range want {
		if events[0].Fields[key] != value {
			t.Errorf("intent %s = %q, want %q", key, events[0].Fields[key], value)
		}
	}
}

func TestFinishCommitsNothingWhenTheCommitIntentCannotBeRecorded(t *testing.T) {
	r := newRig(t)
	r.sm.Store = &RecordGuard{Store: failingStore{Store: r.store, fail: func(rec Record) bool { return isEvent(rec, "commit-intent") }}}
	s := r.spawn(t, 1)
	out := r.sm.Finish(s, Outcome{State: StepOK, Session: s})
	if out.State != StepFailed || out.Reason != "record: disk full" {
		t.Fatalf("finish = %+v", out)
	}
	if hasCall(r.shared.Calls(), "Repo.CommitAll ") {
		t.Fatalf("committed after failed intent: %q", r.shared.Calls())
	}
}

func newRecordLand(t *testing.T, store Store) (*LandGate, *fakeRepo, *fakeStore, *callLog, string) {
	t.Helper()
	root := t.TempDir()
	todo := filepath.Join(root, "docs", "todo.md")
	if err := os.MkdirAll(filepath.Dir(todo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(todo, []byte("- [ ] work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := &callLog{}
	repo := &fakeRepo{callLog: callLog{Shared: log}, RootDir: root, SHA: "base", Tree: "tree", Touched: []string{"docs/todo.md", "a.go"}}
	base := &fakeStore{callLog: callLog{Shared: log}}
	if store == nil {
		store = base
	}
	g := &LandGate{Repo: repo, Plan: &fakePlanSource{callLog: callLog{Shared: log}}, Store: store, RunID: "run-1", TodoPath: todo}
	return g, repo, base, log, todo
}

func TestLandRecordsMergeIntentMergesThenCommitIntentCommitsThenLanding(t *testing.T) {
	g, repo, base, log, _ := newRecordLand(t, nil)
	g.Store = &RecordGuard{Store: orderStore{Store: base, log: log}}
	_, err := g.Land(context.Background(), Phase{ID: "1", Title: "First", DoneWhen: "`true`"})
	if err != nil {
		t.Fatal(err)
	}
	orderedCalls(t, log.Calls(), "append event/merge-intent", "Repo.MergeNoFF r-loop/phase-1", "append event/commit-intent", "Repo.Commit \"phase 1: First\"", "append landing")
	var merge, commit Event
	for _, rec := range base.Records["run-1"] {
		if isEvent(rec, "merge-intent") {
			merge = *rec.Event
		}
		if isEvent(rec, "commit-intent") {
			commit = *rec.Event
		}
	}
	if merge.Fields["branch"] != "r-loop/phase-1" || merge.Fields["base"] != repo.SHA || merge.Fields["message"] != "phase 1: First" || commit.Fields["tree"] != repo.Tree {
		t.Fatalf("merge = %+v, commit = %+v", merge, commit)
	}
}

func TestLandMergesNothingWhenTheMergeIntentCannotBeRecorded(t *testing.T) {
	g, _, base, log, _ := newRecordLand(t, nil)
	g.Store = &RecordGuard{Store: failingStore{Store: base, fail: func(rec Record) bool { return isEvent(rec, "merge-intent") }}}
	_, err := g.Land(context.Background(), Phase{ID: "1", Title: "First", DoneWhen: "`true`"})
	if err == nil || !strings.HasPrefix(err.Error(), "record:") || hasCall(log.Calls(), "Repo.MergeNoFF ") {
		t.Fatalf("err = %v, calls = %q", err, log.Calls())
	}
}

type recordSuite struct{ store Store }

func (s recordSuite) Command(context.Context, Phase) (string, error) {
	recordEvent(s.store, nil, "run-1", Event{Kind: gateDiscovered})
	return "true", nil
}

func TestLandMergesNothingWhenGateDiscoveryCannotBeRecorded(t *testing.T) {
	g, _, base, log, _ := newRecordLand(t, nil)
	guard := &RecordGuard{Store: failingStore{Store: base, fail: func(rec Record) bool { return isEvent(rec, gateDiscovered) }}}
	g.Store, g.Suite = guard, recordSuite{store: guard}
	_, err := g.Land(context.Background(), Phase{ID: "1", Title: "First"})
	if err == nil || !strings.HasPrefix(err.Error(), "record:") || hasCall(log.Calls(), "Repo.MergeNoFF ") || hasCall(log.Calls(), "Repo.Commit ") {
		t.Fatalf("err = %v, calls = %q", err, log.Calls())
	}
}

func TestLandUndoesTheTickWhenItsCommitIntentCannotBeRecorded(t *testing.T) {
	g, _, base, log, todo := newRecordLand(t, nil)
	before, _ := os.ReadFile(todo)
	g.Store = &RecordGuard{Store: failingStore{Store: base, fail: func(rec Record) bool { return isEvent(rec, "commit-intent") }}}
	_, err := g.Land(context.Background(), Phase{ID: "1", Title: "First", DoneWhen: "`true`"})
	after, _ := os.ReadFile(todo)
	if err == nil || !strings.HasPrefix(err.Error(), "record:") || hasCall(log.Calls(), "Repo.Commit ") || !hasCall(log.Calls(), "Repo.AbortMerge") || string(after) != string(before) {
		t.Fatalf("err = %v, calls = %q, todo = %q", err, log.Calls(), after)
	}
}

func TestAStalledRecordThatCannotBeAppendedEndsTheStepFailed(t *testing.T) {
	r := newRig(t)
	r.sm.Store = &RecordGuard{Store: failingStore{Store: r.store, fail: func(rec Record) bool { return rec.Kind == RecordStep && rec.State == StepStalled }}}
	s := r.spawn(t, 1)
	r.host.script = func(int) AgentState { return AgentIdle }
	out := r.sm.Wait(context.Background(), s, &recObserver{})
	if out.State != StepFailed || out.Reason != "record: disk full" || hasCall(r.shared.Calls(), "SessionHost.Prompt "+s.Agent+" \""+nudgeText) {
		t.Fatalf("out = %+v, calls = %q", out, r.shared.Calls())
	}
}

func TestAResumedRunningRecordThatCannotBeAppendedEndsTheStepFailed(t *testing.T) {
	r := newRig(t)
	r.sm.Store = &RecordGuard{Store: failingStore{Store: r.store, fail: func(rec Record) bool {
		return rec.Kind == RecordStep && rec.State == StepRunning && slices.ContainsFunc(r.store.Records["run-1"], func(old Record) bool { return old.Kind == RecordStep && old.State == StepStalled })
	}}}
	s := r.spawn(t, 1)
	r.host.script = func(n int) AgentState {
		if n <= 3 {
			return AgentIdle
		}
		return AgentWorking
	}
	out := r.sm.Wait(context.Background(), s, &recObserver{})
	if out.State != StepFailed || out.Reason != "record: disk full" {
		t.Fatalf("out = %+v", out)
	}
}

func TestRunHaltsWhenTheRunningRunRecordCannotBeAppended(t *testing.T) {
	r := newLoopRig(t)
	guard := &RecordGuard{Store: failingStore{Store: r.store, fail: func(Record) bool { return true }}}
	r.loop.Store, r.loop.Sessions.Store = guard, guard
	code := r.run(RunOptions{})
	if code != 2 || hasCall(r.shared.Calls(), "SessionHost.Open ") || hasCall(r.shared.Calls(), "Repo.CommitAll ") || hasCall(r.shared.Calls(), "Land ") || len(r.events("error")) == 0 || !strings.Contains(r.events("error")[0].Fields["reason"], "record: disk full") || !hasCall(r.shared.Calls(), "Notifier.Fire halt-hook") {
		t.Fatalf("code = %d, calls = %q, errors = %+v", code, r.shared.Calls(), r.events("error"))
	}
}

func TestRunExitsNonZeroWhenTheFinishedRunRecordCannotBeAppended(t *testing.T) {
	r := newLoopRig(t)
	guard := &RecordGuard{Store: failingStore{Store: r.store, fail: func(rec Record) bool { return rec.Kind == RecordRun && rec.Run == RunFinished }}}
	r.loop.Store, r.loop.Sessions.Store = guard, guard
	code := r.run(RunOptions{Phases: []string{"4"}})
	if code != 2 || len(r.events("error")) == 0 || hasCall(r.shared.Calls(), "Notifier.Fire done-hook") || len(r.events("finished")) != 0 {
		t.Fatalf("code = %d, calls = %q, events = %+v", code, r.shared.Calls(), r.face.Events)
	}
}

func TestSpawnRefusesOnceAFatalRecordHasFailed(t *testing.T) {
	r := newRig(t)
	guard := &RecordGuard{Store: failingStore{Store: r.store, fail: func(Record) bool { return true }}}
	r.sm.Store = guard
	guard.Append("run-1", Record{Kind: RecordRun})
	_, err := r.sm.Spawn(context.Background(), r.ref(1))
	if err == nil || !strings.Contains(err.Error(), "record: disk full") || hasCall(r.shared.Calls(), "Repo.AddWorktree ") || hasCall(r.shared.Calls(), "SessionHost.Open ") {
		t.Fatalf("err = %v, calls = %q", err, r.shared.Calls())
	}
}

func TestAStepEventThatCannotBeAppendedHaltsTheRun(t *testing.T) {
	r := newLoopRig(t)
	guard := &RecordGuard{Store: failingStore{Store: r.store, fail: func(rec Record) bool {
		return rec.Kind == RecordEvent && rec.Event != nil && rec.Event.Kind == "step" && rec.Event.Phase == "1" && rec.Event.Step == "implement"
	}}}
	r.loop.Store, r.loop.Sessions.Store = guard, guard
	code := r.run(RunOptions{})
	if code != 2 || hasCommitMessage(r.shared.Calls(), "phase 1 implement") || hasCall(r.shared.Calls(), "Land ") || len(r.host.Opened) > 2 || len(r.events("error")) == 0 || !strings.Contains(r.events("error")[0].Fields["reason"], "record: disk full") {
		t.Fatalf("code = %d, opened = %d, calls = %q, errors = %+v", code, len(r.host.Opened), r.shared.Calls(), r.events("error"))
	}
}

func TestRunStopsWithinOneStepWhenTheStoreStartsFailingMidPhase(t *testing.T) {
	r := newLoopRig(t)
	failing := false
	guard := &RecordGuard{Store: failingStore{Store: r.store, fail: func(rec Record) bool {
		if rec.Kind == RecordStep && rec.Step != nil && rec.Step.Phase == "1" && rec.Step.Kind == "implement" && rec.State == StepSpawned {
			failing = true
		}
		return failing
	}}}
	r.loop.Store, r.loop.Sessions.Store = guard, guard
	code := r.run(RunOptions{})
	if code != 2 || len(r.host.Opened) != 1 || hasCommitMessage(r.shared.Calls(), "phase 1 implement") || hasCall(r.shared.Calls(), "Land ") {
		t.Fatalf("code = %d, opened = %d, calls = %q", code, len(r.host.Opened), r.shared.Calls())
	}
}

func TestAWaitingInputRecordThatCannotBeAppendedHaltsTheRunWithARecordReason(t *testing.T) {
	r := newEventsRig(t)
	r.host.behaviour["rloop-p2-implement"] = "ask"
	guard := &RecordGuard{Store: failingStore{Store: r.store, fail: func(rec Record) bool {
		return rec.Kind == RecordStep && rec.State == StepWaitingInput
	}}}
	r.loop.Store, r.loop.Sessions.Store = guard, guard
	code := r.run(RunOptions{Phases: []string{"2"}})
	if code != 2 || len(r.events("error")) == 0 || !strings.Contains(r.events("error")[0].Fields["reason"], "record: disk full") || hasCall(r.shared.Calls(), "Land ") || !hasCall(r.shared.Calls(), "SessionHost.Interrupt ") {
		t.Fatalf("code = %d, calls = %q, errors = %+v", code, r.shared.Calls(), r.events("error"))
	}
}

func TestReviewRecordFailureStopsEveryPaneWithoutHoldingForRemedy(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(map[bool]string{true: "live", false: "cleared"}[live], func(t *testing.T) {
			r := newLoopRig(t)
			r.loop.runDir = t.TempDir()
			guard := &RecordGuard{Store: failingStore{Store: r.store, fail: func(rec Record) bool { return isEvent(rec, "review-round") }}}
			r.loop.Store, r.loop.Sessions.Store = guard, guard
			watcher := &recordFailureWatcher{fakeWatcher: &fakeWatcher{log: r.shared}}
			r.loop.Watcher = watcher
			r.loop.BlockerTimeout = time.Hour
			r.loop.Runners = map[string]StepRunner{"diff": stepRunnerFunc(func(ctx context.Context, ref StepRef, obs Observer) Outcome {
				s := &Session{Ref: ref, Agent: "step-agent", Pane: "step-pane"}
				s.setReviewers([]*Session{{Agent: "reviewer-one", Pane: "review-pane-one"}, {Agent: "reviewer-two", Pane: "review-pane-two"}})
				obs.Started(s)
				if !live {
					r.loop.setLive(nil)
				}
				guard.Append(ref.Key.Run, Record{Kind: RecordEvent, Event: &Event{Kind: "review-round"}})
				<-ctx.Done()
				return Outcome{State: StepFailed, Session: s}
			})}
			ref := r.loop.ref(r.loop.Plan.Phases[0], r.loop.Kinds[1], 1, "sha")
			out, aborted := r.loop.runStep(context.Background(), ref)
			if aborted || out.State != StepFailed || out.Reason != "record: disk full" || !out.Halted {
				t.Fatalf("out = %+v, aborted = %t", out, aborted)
			}
			calls := r.calls("SessionHost.Interrupt ")
			for _, agent := range []string{"step-agent", "reviewer-one", "reviewer-two"} {
				if !slices.Contains(calls, agent) {
					t.Errorf("missing interrupt for %s: %v", agent, calls)
				}
			}
			if len(watcher.holds) != 0 {
				t.Errorf("remedy holds = %+v", watcher.holds)
			}
		})
	}
}
