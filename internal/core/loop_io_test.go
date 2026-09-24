package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type countingStore struct {
	loopStore
	loads atomic.Int64
	read  atomic.Int64
}

type failedFollowStore struct {
	loopStore
	fail bool
}

func (s *failedFollowStore) Load(runID string) (RunState, error) {
	if s.fail {
		s.fail = false
		return RunState{}, errors.New("temporary load error")
	}
	return s.loopStore.Load(runID)
}

func TestALandedSignalIsRejectedAfterFollowFails(t *testing.T) {
	store := &failedFollowStore{loopStore: loopStore{dir: t.TempDir()}, fail: true}
	g := &RecordGuard{Store: store}
	if _, err := g.Follow("run-1"); err == nil {
		t.Fatal("Follow succeeded")
	}
	if err := g.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: "1"}}); err != nil {
		t.Fatal(err)
	}
	w := &Watch{Store: g, Face: &fakeFace{}, Poll: time.Hour}
	w.StepStarted(implementRef(2, 1), &Session{})
	defer w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})
	sig, err := w.Accept(Signal{Kind: SignalHalt, Source: SourceDriver, Step: StepKey{Phase: "1", Kind: "implement"}})
	if err != nil || !sig.Rejected || sig.RejectReason != "phase 1 has landed" {
		t.Fatalf("signal = %+v, %v", sig, err)
	}
}

func TestALandedHaltIsRecognizedAfterFollowFails(t *testing.T) {
	store := &failedFollowStore{loopStore: loopStore{dir: t.TempDir()}, fail: true}
	g := &RecordGuard{Store: store}
	if _, err := g.Follow("run-1"); err == nil {
		t.Fatal("Follow succeeded")
	}
	if err := g.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: "1"}}); err != nil {
		t.Fatal(err)
	}
	l := &RunLoop{Plan: threePhasePlan(), Store: g, Face: &fakeFace{}, RunID: "run-1", runDir: store.dir}
	l.haltEnded(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Phase: "1", Kind: "implement"}, Reason: "late"})
	if !l.haltLanded || len(l.blocked) != 0 {
		t.Fatalf("haltLanded = %v, blocked = %v", l.haltLanded, l.blocked)
	}
}

func (s *countingStore) Load(runID string) (RunState, error) {
	s.loads.Add(1)
	s.mu.Lock()
	s.read.Add(int64(len(s.Records[runID])))
	s.mu.Unlock()
	return s.loopStore.Load(runID)
}

type gatedQuestionStore struct {
	loopStore
	entered, gate chan struct{}
	once          sync.Once
}

func (s *gatedQuestionStore) Append(runID string, rec Record) error {
	if rec.Kind == RecordQuestion {
		s.once.Do(func() { close(s.entered) })
		<-s.gate
	}
	return s.loopStore.Append(runID, rec)
}

func withReportWriter(t *testing.T, fn func(string, []byte) error) {
	t.Helper()
	old := writeReportFile
	writeReportFile = fn
	t.Cleanup(func() { writeReportFile = old })
}

func withReportEvery(t *testing.T, d time.Duration) {
	t.Helper()
	old := reportEvery
	reportEvery = d
	t.Cleanup(func() { reportEvery = old })
}

type gateFace struct {
	*fakeFace
	entered, gate chan struct{}
}

func (f *gateFace) Emit(ev Event) {
	if ev.Kind == "stuck" {
		close(f.entered)
		<-f.gate
	}
	f.fakeFace.Emit(ev)
}

type drainWatcher struct {
	nopWatcher
	sigs []Signal
}

func (w *drainWatcher) Drain() []Signal {
	sigs := w.sigs
	w.sigs = nil
	return sigs
}

type gatedNotifier struct {
	*fakeNotifier
	block         string
	gate, entered chan struct{}
	once          sync.Once
	mu            sync.Mutex
	reports       map[string]string
}

func (n *gatedNotifier) Fire(hook string, env map[string]string) {
	if hook == n.block {
		if n.entered != nil {
			n.once.Do(func() { close(n.entered) })
		}
		<-n.gate
	}
	data, _ := os.ReadFile(env["R_LOOP_REPORT"])
	n.mu.Lock()
	n.reports[hook] = string(data)
	n.mu.Unlock()
	n.fakeNotifier.Fire(hook, env)
}

type panicNotifier struct{}

func (panicNotifier) Fire(string, map[string]string) { panic("hook boom") }

func failingPlan(r *loopRig) {
	r.loop.Plan.Phases = r.loop.Plan.Phases[:1]
	r.loop.Runners = map[string]StepRunner{"plan-file": stepRunnerFunc(func(context.Context, StepRef, Observer) Outcome {
		return Outcome{State: StepFailed, Reason: "tests red"}
	})}
}

func waitReport(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), want) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("report missing %q: %s", want, data)
		case <-tick.C:
		}
	}
}

func waitDone(t *testing.T, done <-chan struct{}, d time.Duration) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("operation timed out")
	}
}

func TestSpacedEmitsReadTheStoreLinearly(t *testing.T) {
	withReportEvery(t, time.Millisecond)
	store := &countingStore{loopStore: loopStore{dir: t.TempDir()}}
	l := &RunLoop{Plan: threePhasePlan(), Store: &RecordGuard{Store: store}, Face: &fakeFace{}, RunID: "run-1", runDir: store.dir}
	l.startReport()
	for i := 1; i <= 100; i++ {
		l.emit(Event{Kind: "human"})
		waitReport(t, l.reportPath(), "human touches: "+strconv.Itoa(i))
	}
	l.closeReport()
	if n := store.read.Load(); n > 300 {
		t.Fatalf("store read %d records", n)
	}
}

func TestARecordAppendedOutsideTheLoopReachesTheLiveReport(t *testing.T) {
	store := &loopStore{dir: t.TempDir()}
	g := &RecordGuard{Store: store}
	l := &RunLoop{Plan: threePhasePlan(), Store: g, Face: &fakeFace{}, RunID: "run-1", runDir: store.dir}
	l.startReport()
	if err := g.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: "9", MergeSHA: "m9"}}); err != nil {
		t.Fatal(err)
	}
	waitReport(t, l.reportPath(), "phase 9 m9")
	l.closeReport()
}

func TestTheReportCatchesUpWithinTheRefreshInterval(t *testing.T) {
	store := &loopStore{dir: t.TempDir()}
	l := &RunLoop{Plan: threePhasePlan(), Store: &RecordGuard{Store: store}, Face: &fakeFace{}, RunID: "run-1", runDir: store.dir}
	l.startReport()
	l.emit(Event{Kind: "human"})
	waitReport(t, l.reportPath(), "human touches: 1")
	l.emit(Event{Kind: "human"})
	waitReport(t, l.reportPath(), "human touches: 2")
	l.closeReport()
}

func TestABlockedFaceDoesNotHoldBackOtherEmitsOrRecords(t *testing.T) {
	store := &loopStore{dir: t.TempDir()}
	face := &gateFace{fakeFace: &fakeFace{}, entered: make(chan struct{}), gate: make(chan struct{})}
	l := &RunLoop{Plan: threePhasePlan(), Store: &RecordGuard{Store: store}, Face: face, RunID: "run-1", runDir: store.dir}
	go l.emit(Event{Kind: "stuck"})
	waitDone(t, face.entered, time.Second)
	done := make(chan struct{})
	go func() {
		l.emit(Event{Kind: "note"})
		l.emitStep(StepRef{Key: StepKey{Run: "run-1", Phase: "1", Kind: "plan", Attempt: 1}, Kind: StepKind{Name: "plan"}}, StepRunning, "", nil)
		l.setRun(RunRunning, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(face.gate)
		t.Fatal("blocked face held loop")
	}
	close(face.gate)
	store.mu.Lock()
	defer store.mu.Unlock()
	var note, step, run bool
	for _, rec := range store.Records["run-1"] {
		note = note || isEvent(rec, "note")
		step = step || isEvent(rec, "step")
		run = run || rec.Kind == RecordRun && rec.Run == RunRunning
	}
	if !note || !step || !run {
		t.Fatalf("records note=%t step=%t run=%t", note, step, run)
	}
}

func TestASlowReportWriteDoesNotHoldBackEmits(t *testing.T) {
	store := &loopStore{dir: t.TempDir()}
	entered, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	withReportWriter(t, func(path string, data []byte) error {
		once.Do(func() { close(entered) })
		<-gate
		return writeFileAtomic(path, data)
	})
	l := &RunLoop{Plan: threePhasePlan(), Store: &RecordGuard{Store: store}, Face: &fakeFace{}, RunID: "run-1", runDir: store.dir}
	l.startReport()
	l.emit(Event{Kind: "human"})
	waitDone(t, entered, time.Second)
	done := make(chan struct{})
	go func() {
		l.emit(Event{Kind: "human"})
		l.emit(Event{Kind: "human"})
		l.emitStep(StepRef{Key: StepKey{Run: "run-1", Phase: "1", Kind: "plan", Attempt: 1}, Kind: StepKind{Name: "plan"}}, StepRunning, "", nil)
		l.setRun(RunRunning, "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		close(gate)
		t.Fatal("report writer held loop")
	}
	store.mu.Lock()
	var humans int
	var step, run bool
	for _, rec := range store.Records["run-1"] {
		if isEvent(rec, "human") {
			humans++
		}
		step = step || isEvent(rec, "step")
		run = run || rec.Kind == RecordRun && rec.Run == RunRunning
	}
	store.mu.Unlock()
	close(gate)
	l.closeReport()
	if humans != 3 || !step || !run {
		t.Fatalf("records human=%d step=%t run=%t", humans, step, run)
	}
}

func TestTheLastReportWaitsForTheEndHooks(t *testing.T) {
	store := &loopStore{dir: t.TempDir()}
	g := &RecordGuard{Store: store}
	if _, err := g.Follow("run-1"); err != nil {
		t.Fatal(err)
	}
	n := &gatedNotifier{fakeNotifier: &fakeNotifier{}, block: "done-hook", gate: make(chan struct{}), entered: make(chan struct{}), reports: map[string]string{}}
	l := &RunLoop{Plan: threePhasePlan(), Store: g, Face: &fakeFace{}, Notifier: n, RunID: "run-1", runDir: store.dir}
	l.fire("done-hook", "finished", "", "", "")
	waitDone(t, n.entered, time.Second)
	done := make(chan struct{})
	go func() { l.endReport(); close(done) }()
	select {
	case <-done:
		t.Fatal("endReport returned before hook")
	case <-time.After(100 * time.Millisecond):
	}
	if err := g.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: "9", MergeSHA: "m9"}}); err != nil {
		t.Fatal(err)
	}
	close(n.gate)
	waitDone(t, done, time.Second)
	data, _ := os.ReadFile(l.reportPath())
	if !strings.Contains(string(data), "phase 9 m9") {
		t.Fatalf("report: %s", data)
	}
}

func TestTheLastReportWaitsForAQuestionWorker(t *testing.T) {
	store := &gatedQuestionStore{loopStore: loopStore{dir: t.TempDir()}, entered: make(chan struct{}), gate: make(chan struct{})}
	ask := &fakeAskChannel{Asked: make(chan Question, 1)}
	l := &RunLoop{Plan: threePhasePlan(), Store: &RecordGuard{Store: store}, Face: &fakeFace{}, Kinds: []StepKind{{Name: "plan"}}, Ask: ask, RunID: "run-1", runDir: store.dir}
	ctx, cancel := context.WithCancel(context.Background())
	l.ServeQuestions(ctx)
	ask.Asked <- Question{ID: "q1", Step: StepKey{Run: "run-1", Phase: "1", Kind: "land"}, Text: "which db?"}
	waitDone(t, store.entered, time.Second)
	cancel()
	done := make(chan struct{})
	go func() { l.endReport(); close(done) }()
	select {
	case <-done:
		t.Fatal("endReport returned before question")
	case <-time.After(100 * time.Millisecond):
	}
	close(store.gate)
	waitDone(t, done, time.Second)
	data, _ := os.ReadFile(l.reportPath())
	if !strings.Contains(string(data), "q1 phase 1 land: which db? → land-stage step (withdrawn") {
		t.Fatalf("report: %s", data)
	}
}

func TestAReportWriteKeepsTheUmaskAndAnExistingMode(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })
	p := filepath.Join(t.TempDir(), "report.md")
	if err := writeFileAtomic(p, []byte("a")); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("new file: %v, %v", st, err)
	}
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(p, []byte("b")); err != nil {
		t.Fatal(err)
	}
	st, err = os.Stat(p)
	if err != nil || st.Mode().Perm() != 0o640 {
		t.Fatalf("replacement: %v, %v", st, err)
	}
	data, _ := os.ReadFile(p)
	entries, _ := os.ReadDir(filepath.Dir(p))
	if string(data) != "b" || len(entries) != 1 {
		t.Fatalf("data=%q entries=%v", data, entries)
	}
}

func TestWriteReportBeforeRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	face := &fakeFace{}
	(&RunLoop{Store: &loopStore{dir: dir}, Face: face}).WriteReport()
	if _, err := os.Stat(filepath.Join(dir, "report.md")); !os.IsNotExist(err) {
		t.Fatalf("report exists: %v", err)
	}
	if len(face.Events) != 0 {
		t.Fatalf("events: %+v", face.Events)
	}
}

func TestHandlingWatchdogSignalsLoadsNoRunState(t *testing.T) {
	store := &countingStore{loopStore: loopStore{dir: t.TempDir()}}
	g := &RecordGuard{Store: store}
	if _, err := g.Follow("run-1"); err != nil {
		t.Fatal(err)
	}
	if err := g.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: "1"}}); err != nil {
		t.Fatal(err)
	}
	store.loads.Store(0)
	face := &fakeFace{}
	l := &RunLoop{Plan: threePhasePlan(), Store: g, Face: face, RunID: "run-1", runDir: store.dir, Watcher: &drainWatcher{sigs: []Signal{
		{Kind: SignalHalt, Source: SourceDriver, Step: StepKey{Run: "run-1", Phase: "1", Kind: "implement", Attempt: 1}, Reason: "stale"},
		{Kind: SignalWarn, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}, Reason: "slow"},
	}}}
	l.drainSignals()
	if n := store.loads.Load(); n != 0 {
		t.Fatalf("loads=%d", n)
	}
	if !l.haltLanded || l.halted == nil {
		t.Fatalf("halt state: landed=%t halted=%+v", l.haltLanded, l.halted)
	}
	var stale, slow bool
	for _, ev := range face.Events {
		stale = stale || ev.Kind == "warning" && strings.HasPrefix(ev.Fields["reason"], "halt for phase-1/implement after it ended")
		slow = slow || ev.Kind == "warning" && ev.Fields["reason"] == "slow"
	}
	if !stale || !slow {
		t.Fatalf("warnings: %+v", face.Events)
	}
}

func TestASlowWarnHookDoesNotHoldTheLoop(t *testing.T) {
	r := newLoopRig(t)
	failingPlan(r)
	n := &gatedNotifier{fakeNotifier: r.notifier, block: "warn-hook", gate: make(chan struct{}), entered: make(chan struct{}), reports: map[string]string{}}
	r.loop.Notifier = n
	done := make(chan int, 1)
	go func() { done <- r.run(RunOptions{}) }()
	waitDone(t, n.entered, 2*time.Second)
	deadline := time.After(2 * time.Second)
	for !r.face.seen("halt") {
		select {
		case <-deadline:
			t.Fatal("halt delayed by warn hook")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case code := <-done:
		t.Fatalf("run ended before hook: %d", code)
	case <-time.After(100 * time.Millisecond):
	}
	close(n.gate)
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not finish")
	}
	if got := r.hooks(); !reflect.DeepEqual(got, []string{"warn-hook blocked", "halt-hook halted"}) {
		t.Fatalf("hooks: %v", got)
	}
	want := map[string]string{"R_LOOP_RUN": "run-1", "R_LOOP_STATUS": "blocked", "R_LOOP_PHASE": "1", "R_LOOP_STEP": "plan", "R_LOOP_REASON": "tests red", "R_LOOP_TODO": "docs/x/todo.md", "R_LOOP_REPORT": filepath.Join(r.store.dir, "report.md")}
	if !reflect.DeepEqual(r.notifier.Fired[0], want) {
		t.Fatalf("env: %+v", r.notifier.Fired[0])
	}
	n.mu.Lock()
	report := n.reports["halt-hook"]
	n.mu.Unlock()
	if !strings.Contains(report, "## Halt") {
		t.Fatalf("halt hook report: %s", report)
	}
}

func TestAStepCompletesWhileASlowWarnHookRuns(t *testing.T) {
	r := newLoopRig(t)
	r.loop.Plan.Phases = r.loop.Plan.Phases[:1]
	w := &fakeWatcher{log: r.shared, signals: make(chan Signal), restarts: make(chan Restart, 8)}
	r.loop.Watcher = w
	planEntered, release, implEntered := make(chan struct{}), make(chan struct{}), make(chan struct{})
	r.loop.Runners = map[string]StepRunner{
		"plan-file": stepRunnerFunc(func(context.Context, StepRef, Observer) Outcome {
			close(planEntered)
			<-release
			return Outcome{State: StepOK}
		}),
		"diff": stepRunnerFunc(func(context.Context, StepRef, Observer) Outcome { close(implEntered); return Outcome{State: StepOK} }),
	}
	n := &gatedNotifier{fakeNotifier: r.notifier, block: "warn-hook", gate: make(chan struct{}), entered: make(chan struct{}), reports: map[string]string{}}
	r.loop.Notifier = n
	done := make(chan int, 1)
	go func() { done <- r.run(RunOptions{}) }()
	waitDone(t, planEntered, 2*time.Second)
	sent := make(chan struct{})
	go func() {
		w.signals <- Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: "1", Kind: "plan", Attempt: 1}, Reason: "slow"}
		close(sent)
	}()
	waitDone(t, sent, time.Second)
	waitDone(t, n.entered, time.Second)
	close(release)
	waitDone(t, implEntered, time.Second)
	close(n.gate)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not finish")
	}
}

func TestAnAbortIsHandledWhileASlowWarnHookRuns(t *testing.T) {
	r := newLoopRig(t)
	r.loop.Plan.Phases = r.loop.Plan.Phases[:1]
	w := &fakeWatcher{log: r.shared, signals: make(chan Signal), restarts: make(chan Restart, 8)}
	r.loop.Watcher = w
	planEntered := make(chan struct{})
	r.loop.Runners = map[string]StepRunner{"plan-file": stepRunnerFunc(func(ctx context.Context, _ StepRef, _ Observer) Outcome {
		close(planEntered)
		<-ctx.Done()
		return Outcome{State: StepFailed, Reason: "stopped"}
	})}
	n := &gatedNotifier{fakeNotifier: r.notifier, block: "warn-hook", gate: make(chan struct{}), entered: make(chan struct{}), reports: map[string]string{}}
	r.loop.Notifier = n
	done := make(chan int, 1)
	go func() { done <- r.run(RunOptions{}) }()
	waitDone(t, planEntered, 2*time.Second)
	sent := make(chan struct{})
	go func() {
		w.signals <- Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: "1", Kind: "plan", Attempt: 1}, Reason: "slow"}
		close(sent)
	}()
	waitDone(t, sent, time.Second)
	waitDone(t, n.entered, time.Second)
	if err := r.store.MarkAbort("run-1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for !r.face.seen("aborted") {
		select {
		case <-deadline:
			close(n.gate)
			t.Fatal("abort delayed by hook")
		case <-time.After(time.Millisecond):
		}
	}
	close(n.gate)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not finish")
	}
}

func TestRunWaitsForASlowDoneHook(t *testing.T) {
	r := newLoopRig(t)
	n := &gatedNotifier{fakeNotifier: r.notifier, block: "done-hook", gate: make(chan struct{}), entered: make(chan struct{}), reports: map[string]string{}}
	r.loop.Notifier = n
	done := make(chan int, 1)
	go func() { done <- r.run(RunOptions{}) }()
	waitDone(t, n.entered, 5*time.Second)
	if !r.face.seen("finished") {
		t.Fatal("finished event missing")
	}
	select {
	case code := <-done:
		t.Fatalf("run ended before hook: %d", code)
	case <-time.After(100 * time.Millisecond):
	}
	close(n.gate)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not finish")
	}
	if got := r.hooks(); len(got) == 0 || got[len(got)-1] != "done-hook finished" {
		t.Fatalf("hooks: %v", got)
	}
	n.mu.Lock()
	report := n.reports["done-hook"]
	n.mu.Unlock()
	if !strings.Contains(report, "## Landed") {
		t.Fatalf("done hook report: %s", report)
	}
}

func TestAWatchdogSignalIsHandledWhileAHookRuns(t *testing.T) {
	store := &loopStore{dir: t.TempDir()}
	face := &fakeFace{}
	w := &Watch{Store: store, Face: face, Poll: time.Hour}
	n := &gatedNotifier{fakeNotifier: &fakeNotifier{}, block: "warn-hook", gate: make(chan struct{}), entered: make(chan struct{}), reports: map[string]string{}}
	l := &RunLoop{Plan: threePhasePlan(), Store: store, Face: face, Notifier: n, Hooks: Hooks{OnWarn: "warn-hook"}, Watcher: w, RunID: "run-1", runDir: store.dir}
	fired := make(chan struct{})
	go func() { l.fire("warn-hook", "warning", "1", "implement", "first"); close(fired) }()
	waitDone(t, fired, time.Second)
	waitDone(t, n.entered, time.Second)
	w.StepStarted(implementRef(1, 1), &Session{})
	accepted := make(chan bool, 1)
	go func() {
		ok, reason := w.Handle(Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: StepKey{Phase: "1", Kind: "implement"}, Reason: "slow"})
		accepted <- ok && reason == ""
	}()
	select {
	case ok := <-accepted:
		if !ok {
			t.Fatal("signal rejected")
		}
	case <-time.After(time.Second):
		t.Fatal("signal blocked")
	}
	l.drainSignals()
	if !face.seen("warning") {
		t.Fatal("warning not emitted")
	}
	close(n.gate)
	l.waitHooks()
	if got := n.fakeNotifier.Fired; len(got) != 2 || got[0]["R_LOOP_REASON"] != "first" || got[1]["R_LOOP_REASON"] != "slow" {
		t.Fatalf("hooks: %+v", got)
	}
}

func TestAPanickingHookIsReportedAndTheRunEnds(t *testing.T) {
	r := newLoopRig(t)
	failingPlan(r)
	r.loop.Notifier = panicNotifier{}
	done := make(chan int, 1)
	go func() { done <- r.run(RunOptions{}) }()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("exit %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run did not finish")
	}
	var failed []Event
	r.face.mu.Lock()
	for _, ev := range r.face.Events {
		if ev.Kind == "notify-failed" {
			failed = append(failed, ev)
		}
	}
	r.face.mu.Unlock()
	if len(failed) != 2 || failed[0].Fields["status"] != "blocked" || failed[1].Fields["status"] != "halted" || !strings.Contains(failed[0].Fields["reason"], "hook boom") || !strings.Contains(failed[1].Fields["reason"], "hook boom") {
		t.Fatalf("notify failures: %+v", failed)
	}
}
