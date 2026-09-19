package core

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var remedyT0 = time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)

type blockingFace struct {
	fakeFace
	asked     chan Question
	withdrawn []string
}

func (f *blockingFace) Ask(q Question) (string, error) {
	f.asked <- q
	select {}
}

func (f *blockingFace) Withdraw(id string) { f.withdrawn = append(f.withdrawn, id) }

func failedImplement(t *testing.T, store Store) (*Watch, StepKey) {
	t.Helper()
	key := StepKey{Run: "run-1", Phase: 2, Kind: "implement", Attempt: 1}
	w := &Watch{Store: store, Now: func() time.Time { return remedyT0 }}
	w.StepStarted(StepRef{Key: key}, nil)
	w.StepEnded(StepRef{Key: key}, Outcome{State: StepFailed, Reason: "backstop"})
	w.Hold(key)
	return w, key
}

func newRemedies(w *Watch, store Store, face Face, allow ...string) *Remedies {
	return &Remedies{Allow: allow, Face: face, Store: store, Window: time.Minute, Now: func() time.Time { return remedyT0 }, Watch: w, MaxRestarts: 2}
}

func remedyRecords(store *fakeStore) []Remedy {
	var out []Remedy
	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordRemedy {
			out = append(out, *rec.Remedy)
		}
	}
	return out
}

func TestAnAllowListedClassIsAuthorisedWithoutAskingTheFace(t *testing.T) {
	store := &fakeStore{}
	face := &fakeFace{}
	w, key := failedImplement(t, store)
	rem := newRemedies(w, store, face, "locks")

	got, _ := rem.Propose("locks", "rm -f .git/index.lock", "a stale git lock")

	if got != "authorised" {
		t.Fatalf("decision %q", got)
	}
	if calls := face.Calls(); len(calls) != 0 {
		t.Errorf("face called: %q", calls)
	}
	want := []Remedy{{ID: "remedy-1", Step: key, Class: "locks", Command: "rm -f .git/index.lock", Why: "a stale git lock", Consent: "allow-list", ProposedAt: remedyT0, DecidedAt: remedyT0}}
	if got := remedyRecords(store); !reflect.DeepEqual(got, want) {
		t.Errorf("records\n got %+v\nwant %+v", got, want)
	}
}

func TestAnUnlistedClassIsAskedYesNoAndAYesIsAMaintainerConsent(t *testing.T) {
	store := &fakeStore{}
	face := &fakeFace{Answers: map[string]string{"remedy-1": "yes"}}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, face, "locks")

	got, _ := rem.Propose("deps", "go mod download", "module cache is empty")

	if got != "authorised" {
		t.Fatalf("decision %q", got)
	}
	if calls := asks(face); !reflect.DeepEqual(calls, []string{"Face.Ask remedy-1"}) {
		t.Errorf("face calls %q", calls)
	}
	if recs := remedyRecords(store); len(recs) != 1 || recs[0].Consent != "maintainer" {
		t.Errorf("records %+v", recs)
	}
}

func TestAnUnlistedClassAnsweredNoOrWithNoInputIsRefused(t *testing.T) {
	for name, face := range map[string]*fakeFace{
		"no":       {Answers: map[string]string{"remedy-1": "no"}},
		"no input": {AskErr: ErrNoInput},
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeStore{}
			w, _ := failedImplement(t, store)
			rem := newRemedies(w, store, face)

			got, _ := rem.Propose("ports", "kill $(lsof -ti :8080)", "port 8080 is taken")

			if got != "refused" {
				t.Fatalf("decision %q", got)
			}
			if recs := remedyRecords(store); len(recs) != 1 || recs[0].Consent != "refused" {
				t.Errorf("records %+v", recs)
			}
		})
	}
}

func TestAnUnansweredConsentIsRefusedAfterTheWindowAndWithdrawn(t *testing.T) {
	store := &fakeStore{}
	face := &blockingFace{asked: make(chan Question, 1)}
	w, key := failedImplement(t, store)
	rem := newRemedies(w, store, face)
	rem.Window = 10 * time.Millisecond

	got, _ := rem.Propose("containers", "docker compose up -d db", "the db container is down")

	if got != "refused" {
		t.Fatalf("decision %q", got)
	}
	want := Question{ID: "remedy-1", Step: key, Text: "watchdog proposes (containers): docker compose up -d db — the db container is down", Options: []string{"yes", "no"}, AskedAt: remedyT0}
	select {
	case q := <-face.asked:
		if !reflect.DeepEqual(q, want) {
			t.Errorf("asked %+v", q)
		}
	case <-time.After(time.Second):
		t.Error("never asked")
	}
	if !reflect.DeepEqual(face.withdrawn, []string{"remedy-1"}) {
		t.Errorf("withdrawn %v", face.withdrawn)
	}
	if recs := remedyRecords(store); len(recs) != 1 || recs[0].Consent != "refused" {
		t.Errorf("records %+v", recs)
	}
}

func TestAnUnknownClassIsRefusedNamingItAndNothingIsRecorded(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, &fakeFace{}, "locks")

	got, reason := rem.Propose("git", "git reset --hard", "tree is dirty")

	if got != "refused" || !strings.Contains(reason, `"git"`) {
		t.Errorf("decision %q reason %q", got, reason)
	}
	if recs := remedyRecords(store); len(recs) != 0 {
		t.Errorf("records %+v", recs)
	}
}

func TestAProposalWithNoHeldOrLiveStepIsRefused(t *testing.T) {
	store := &fakeStore{}
	rem := newRemedies(&Watch{Store: store}, store, &fakeFace{}, "locks")

	if got, reason := rem.Propose("locks", "rm .lock", "stale"); got != "refused" || reason != "no step to remedy" {
		t.Errorf("decision %q reason %q", got, reason)
	}
}

func TestAProposalAttachesToTheLiveStepWhenNothingIsHeld(t *testing.T) {
	store := &fakeStore{}
	key := StepKey{Run: "run-1", Phase: 3, Kind: "plan", Attempt: 1}
	w := &Watch{Store: store}
	w.StepStarted(StepRef{Key: key}, nil)
	defer w.StepEnded(StepRef{Key: key}, Outcome{State: StepOK})
	rem := newRemedies(w, store, &fakeFace{}, "ports")

	rem.Propose("ports", "fuser -k 5432/tcp", "port held")

	if recs := remedyRecords(store); len(recs) != 1 || recs[0].Step != key {
		t.Errorf("records %+v", recs)
	}
}

func TestTheRecordHoldsTheCommandVerbatimAndIsAppendedBeforeTheDecisionReturns(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, &fakeFace{Answers: map[string]string{"remedy-1": "yes"}})
	cmd := "  pkill -f 'vite --port 5173' && rm -rf node_modules/.vite  "

	rem.Propose("ports", cmd, "vite still holds the port")
	seen := remedyRecords(store)

	if len(seen) != 1 || seen[0].Command != cmd || seen[0].Why != "vite still holds the port" || seen[0].Class != "ports" {
		t.Errorf("record %+v", seen)
	}
	if seen[0].ProposedAt.IsZero() || seen[0].DecidedAt.IsZero() {
		t.Errorf("times %+v", seen[0])
	}
}

func TestARestartAfterAnAuthorisedRestartRemedyIsQueuedForTheHeldStep(t *testing.T) {
	store := &fakeStore{}
	face := &fakeFace{Answers: map[string]string{"remedy-1": "yes"}}
	w, key := failedImplement(t, store)
	rem := newRemedies(w, store, face)
	rem.Propose("restart", "herdr pane close stuck", "the agent froze")

	got := make(chan Restart, 1)
	go func() { got <- <-w.Restarts() }()

	ok, reason := rem.Restart("phase-2/implement", "use the fake", "")

	if !ok || reason != "" {
		t.Fatalf("restart %v %q", ok, reason)
	}
	if want := (Restart{Step: key, Addendum: "use the fake", Remedy: "remedy-1"}); <-got != want {
		t.Errorf("restart, want %+v", want)
	}
}

func TestARestartTheLoopNeverTakesBeforeTheWindowClosesIsNotAccepted(t *testing.T) {
	store := &fakeStore{}
	w, key := failedImplement(t, store)
	rem := newRemedies(w, store, &fakeFace{}, "restart")
	go func() {
		time.Sleep(20 * time.Millisecond)
		w.Release(key)
	}()

	ok, reason := rem.Restart("phase-2/implement", "", "")

	if ok || reason != "run halted" {
		t.Errorf("restart %v %q", ok, reason)
	}
	select {
	case rs := <-w.Restarts():
		t.Errorf("stale restart left queued %+v", rs)
	default:
	}
}

func TestAZeroWindowRefusesAnUnlistedClassWithoutAsking(t *testing.T) {
	store := &fakeStore{}
	face := &blockingFace{asked: make(chan Question, 1)}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, face)
	rem.Window = 0

	got, _ := rem.Propose("deps", "go mod download", "cache empty")

	if got != "refused" {
		t.Errorf("decision %q", got)
	}
	if recs := remedyRecords(store); len(recs) != 1 || recs[0].Consent != "refused" {
		t.Errorf("records %+v", recs)
	}
}

type probeHost struct {
	fakeSessionHost
	watch *Watch
	held  chan bool
}

func (h *probeHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	if strings.HasPrefix(text, "step ended phase-2/implement failed") {
		_, ok := h.watch.holding()
		h.held <- ok
	}
	return nil
}

func TestTheRemedyWindowIsOpenWhenTheWatchdogHearsStepEnded(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = 20 * time.Millisecond
	r.host.behaviour["rloop-p2-implement"] = "fail"
	w := &Watch{Store: r.store}
	host := &probeHost{watch: w, held: make(chan bool, 4)}
	w.Dog = &Watchdog{Host: host, Store: r.store, RunID: "run-1"}
	r.loop.Watcher = w

	r.run(RunOptions{Phases: []int{2}})

	select {
	case held := <-host.held:
		if !held {
			t.Error("the watchdog heard step ended before the remedy window opened")
		}
	default:
		t.Fatal("the watchdog never heard step ended")
	}
	if _, ok := w.holding(); ok {
		t.Error("the window is still held after the run")
	}
}

func TestARetryNeedsAnAddendumAndAProviderRemedyNeedsAProvider(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, &fakeFace{}, "retry", "provider")
	rem.Fallbacks = map[string]Fallback{"implement": {Provider: "claude"}}
	rem.Propose("retry", "none", "flaky")
	rem.Propose("provider", "none", "codex is down")

	if ok, reason := rem.Restart("phase-2/implement", "", ""); ok || reason != "no authorised remedy" {
		t.Errorf("bare restart %v %q", ok, reason)
	}
	go func() { <-w.Restarts() }()
	if ok, reason := rem.Restart("phase-2/implement", "", "claude"); !ok {
		t.Errorf("provider restart %v %q", ok, reason)
	}
}

func TestARestartOfAnOkStepIsRefused(t *testing.T) {
	store := &fakeStore{}
	key := StepKey{Run: "run-1", Phase: 1, Kind: "plan", Attempt: 1}
	w := &Watch{Store: store}
	w.StepStarted(StepRef{Key: key}, nil)
	w.StepEnded(StepRef{Key: key}, Outcome{State: StepOK})
	rem := newRemedies(w, store, &fakeFace{}, "restart")

	ok, reason := rem.Restart("phase-1/plan", "", "")

	if ok || reason != "step is ok" {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestARestartWithNoOpenRemedyWindowIsRefusedAsRunHalted(t *testing.T) {
	store := &fakeStore{}
	w, key := failedImplement(t, store)
	w.Release(key)
	rem := newRemedies(w, store, &fakeFace{}, "restart")

	ok, reason := rem.Restart("phase-2/implement", "", "")

	if ok || reason != "run halted" {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestARefusedRemedyLeadsToNoRestart(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, &fakeFace{Answers: map[string]string{"remedy-1": "no"}})
	rem.Propose("restart", "herdr pane close stuck", "the agent froze")

	ok, reason := rem.Restart("phase-2/implement", "", "")

	if ok || reason != "no authorised remedy" {
		t.Errorf("restart %v %q", ok, reason)
	}
	select {
	case rs := <-w.Restarts():
		t.Errorf("restart queued %+v", rs)
	default:
	}
}

func TestARestartPastMaxRestartsIsRefused(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	for range 2 {
		store.Append("run-1", Record{Kind: RecordEvent, Event: &Event{Kind: "restart", Fields: map[string]string{"step": "phase-2/implement"}}})
	}
	rem := newRemedies(w, store, &fakeFace{}, "restart")

	ok, reason := rem.Restart("phase-2/implement", "", "")

	if ok || reason != "restart limit 2 reached" {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestAnAuthorisedRemedyIsSpentByOneRestart(t *testing.T) {
	store := &fakeStore{}
	w, _ := failedImplement(t, store)
	rem := newRemedies(w, store, &fakeFace{Answers: map[string]string{"remedy-1": "yes"}})
	rem.Propose("restart", "herdr pane close stuck", "froze")
	store.Append("run-1", Record{Kind: RecordEvent, Event: &Event{Kind: "restart", Fields: map[string]string{"step": "phase-2/implement", "remedy": "remedy-1"}}})

	if ok, reason := rem.Restart("phase-2/implement", "", ""); ok || reason != "no authorised remedy" {
		t.Errorf("restart %v %q", ok, reason)
	}
}

func TestAnAuthorisedRestartRerunsTheStepAsANewAttemptWithTheAddendum(t *testing.T) {
	r := newEventsRig(t)
	r.loop.RemedyWindow = time.Minute
	r.host.behaviour["rloop-p2-implement"] = "fail"
	w := &Watch{Store: r.store}
	r.loop.Watcher = w
	rem := newRemedies(w, r.store, &fakeFace{Answers: map[string]string{"remedy-1": "yes"}})
	decided := make(chan string, 2)
	go func() {
		for {
			if key, ok := w.holding(); ok && key.Kind == "implement" {
				break
			}
			time.Sleep(time.Millisecond)
		}
		decided <- decisionOf(rem.Propose("restart", "herdr pane close stuck", "froze"))
		_, reason := rem.Restart("phase-2/implement", "use the fake", "")
		decided <- reason
	}()

	code := r.run(RunOptions{Phases: []int{2}})

	if code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if d, reason := <-decided, <-decided; d != "authorised" || reason != "" {
		t.Fatalf("decision %q restart %q", d, reason)
	}
	want := []string{"rloop-p2-plan", "rloop-p2-implement", "rloop-p2-implement-a2"}
	if got := r.agents(); !reflect.DeepEqual(got, want) {
		t.Errorf("spawned %v, want %v", got, want)
	}
	if got := r.prompts.addendums[r.host.Opened[2].Env["R_LOOP_SENTINEL"]]; got != "use the fake" {
		t.Errorf("attempt 2 addendum %q", got)
	}
	restarts := r.events("restart")
	wantFields := map[string]string{"step": "phase-2/implement", "attempt": "2", "addendum": "use the fake", "provider": "", "remedy": "remedy-1"}
	if len(restarts) != 1 || !reflect.DeepEqual(restarts[0].Fields, wantFields) {
		t.Errorf("restart events %+v", restarts)
	}
	rep := r.report(t)
	for _, line := range []string{
		"- phase 2 implement: restart as attempt 2 — use the fake (remedy: herdr pane close stuck)\n",
		"- phase 2 implement: restart `herdr pane close stuck` — maintainer; restart followed\n",
	} {
		if !strings.Contains(rep, line) {
			t.Errorf("report missing %q:\n%s", line, rep)
		}
	}
}

func decisionOf(decision, _ string) string { return decision }

func TestAConsentAnswerSettlesTheQuestionOnTheFaceNamingTheMaintainer(t *testing.T) {
	store := &fakeStore{}
	face := &fakeFace{Answers: map[string]string{"remedy-1": "no"}}
	w, key := failedImplement(t, store)
	rem := newRemedies(w, store, face)

	rem.Propose("deps", "go mod download", "module cache is empty")

	want := []Event{{At: remedyT0, Kind: "human", Phase: key.Phase, Step: key.Kind, Fields: map[string]string{"what": "answer", "id": "remedy-1", "by": "maintainer"}}}
	if !reflect.DeepEqual(face.Events, want) {
		t.Errorf("face events\n got %+v\nwant %+v", face.Events, want)
	}
}

func asks(face *fakeFace) []string {
	var out []string
	for _, c := range face.Calls() {
		if strings.HasPrefix(c, "Face.Ask ") {
			out = append(out, c)
		}
	}
	return out
}
