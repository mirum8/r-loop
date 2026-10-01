package core

import (
	"context"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func onLand(r *loopRig, phase string, fn func()) {
	r.loop.Lander = landerFunc(func(ctx context.Context, ph Phase) (Landing, error) {
		if ph.ID == phase {
			fn()
		}
		return r.lander.Land(ctx, ph)
	})
}

func waitForPause(t *testing.T, r *loopRig) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r.face.seen("paused") {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("run never paused: %v", r.kinds())
}

func runAsync(r *loopRig) chan int {
	done := make(chan int, 1)
	go func() { done <- r.run(RunOptions{}) }()
	return done
}

func exitOf(t *testing.T, done chan int) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(5 * time.Second):
		t.Fatal("run did not end")
		return -1
	}
}

func TestStopAfterPhaseLetsThePhaseLandAndStartsNoOther(t *testing.T) {
	r := newLoopRig(t)
	onLand(r, "1", func() {
		if ok, reason := r.loop.StopRun(StopAfterPhase, "review phase 1", "stop after this phase"); !ok {
			t.Errorf("refused: %s", reason)
		}
	})

	code := r.run(RunOptions{})

	if code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("landed %v", got)
	}
	for _, ev := range r.events("phase-start") {
		if ev.Phase != "1" {
			t.Errorf("phase started after the stop: %+v", ev)
		}
	}
	runs := r.runRecords()
	if last := runs[len(runs)-1]; last.Run != RunHalted || last.Reason != "stopped by the watchdog: review phase 1" {
		t.Errorf("last run record %+v", last)
	}
	if got := r.hooks(); !reflect.DeepEqual(got, []string{"halt-hook halted"}) {
		t.Errorf("hooks %v", got)
	}
}

func TestStopNowAbortsTheRun(t *testing.T) {
	r := newLoopRig(t)
	onLand(r, "1", func() { r.loop.StopRun(StopNow, "wrong plan", "stop now") })

	code := r.run(RunOptions{})

	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !r.store.Aborted("run-1") {
		t.Error("abort not marked")
	}
	for _, ev := range r.events("phase-start") {
		if ev.Phase != "1" {
			t.Errorf("phase started after the stop: %+v", ev)
		}
	}
}

func TestPauseWaitsAfterThePhaseUntilContinued(t *testing.T) {
	r := newLoopRig(t)
	onLand(r, "1", func() { r.loop.PauseRun("lunch", "pause after this one") })

	done := runAsync(r)
	waitForPause(t, r)
	started := len(r.events("phase-start"))
	time.Sleep(20 * time.Millisecond)
	if n := len(r.events("phase-start")); n != started || n != 1 {
		t.Fatalf("%d phases started while paused, want 1", n)
	}
	if ok, reason := r.loop.ContinueRun("go on"); !ok {
		t.Fatalf("continue refused: %s", reason)
	}

	if code := exitOf(t, done); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Errorf("landed %v", got)
	}
	if len(r.events("paused")) != 1 || len(r.events("continued")) != 1 {
		t.Errorf("events %v", r.kinds())
	}
	var statuses []RunStatus
	for _, rec := range r.runRecords() {
		statuses = append(statuses, rec.Run)
	}
	if !reflect.DeepEqual(statuses, []RunStatus{RunRunning, RunPaused, RunRunning, RunFinished}) {
		t.Errorf("run records %v", statuses)
	}
}

func TestStopWhilePausedHaltsWithoutAnotherPhase(t *testing.T) {
	r := newLoopRig(t)
	onLand(r, "1", func() { r.loop.PauseRun("lunch", "pause") })

	done := runAsync(r)
	waitForPause(t, r)
	r.loop.StopRun(StopAfterPhase, "done for today", "stop")

	if code := exitOf(t, done); code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("landed %v", got)
	}
}

type goneWatcher struct {
	*fakeWatcher
	gone atomic.Bool
}

func (g *goneWatcher) Gone() bool { return g.gone.Load() }

func TestAGoneWatchdogHaltsAPausedRun(t *testing.T) {
	r := newLoopRig(t)
	w := &goneWatcher{fakeWatcher: &fakeWatcher{log: r.shared, signals: make(chan Signal), restarts: make(chan Restart, 8)}}
	r.loop.Watcher = w
	onLand(r, "1", func() { r.loop.PauseRun("lunch", "pause") })

	done := runAsync(r)
	waitForPause(t, r)
	w.gone.Store(true)

	if code := exitOf(t, done); code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("landed %v", got)
	}
}

func TestRunControlWithoutTheMaintainersWordIsRefused(t *testing.T) {
	r := newLoopRig(t)

	for name, call := range map[string]func() (bool, string){
		"stop":  func() (bool, string) { return r.loop.StopRun(StopAfterPhase, "x", " ") },
		"pause": func() (bool, string) { return r.loop.PauseRun("x", "") },
	} {
		if ok, reason := call(); ok || reason != noConsent {
			t.Errorf("%s = %v %q", name, ok, reason)
		}
	}
	for name, call := range map[string]func() (bool, string){
		"stop":  func() (bool, string) { return r.loop.StopRun(StopAfterPhase, "  ", "stop") },
		"pause": func() (bool, string) { return r.loop.PauseRun("", "pause") },
	} {
		if ok, reason := call(); ok || reason != noReason {
			t.Errorf("%s without a reason = %v %q", name, ok, reason)
		}
	}
	if ok, reason := r.loop.StopRun("later", "x", "stop"); ok || reason != `when "later" is not after-phase or now` {
		t.Errorf("stop later = %v %q", ok, reason)
	}
	if ok, reason := r.loop.ContinueRun("go on"); ok || reason != "the run is not paused" {
		t.Errorf("continue = %v %q", ok, reason)
	}
	if code := r.run(RunOptions{}); code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
}

func TestAStopRequestedInTheLastPhaseLetsTheRunFinish(t *testing.T) {
	r := newLoopRig(t)
	onLand(r, "3", func() { r.loop.StopRun(StopAfterPhase, "enough", "stop") })

	if code := r.run(RunOptions{}); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if len(r.events("warning")) != 1 {
		t.Errorf("events %v", r.kinds())
	}
}

func TestContinueCancelsAPendingPause(t *testing.T) {
	r := newLoopRig(t)
	onLand(r, "1", func() {
		r.loop.PauseRun("lunch", "pause")
		if ok, reason := r.loop.ContinueRun("never mind"); !ok {
			t.Errorf("continue refused: %s", reason)
		}
	})

	if code := r.run(RunOptions{}); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if len(r.events("paused")) != 0 {
		t.Errorf("paused anyway: %v", r.kinds())
	}
}

type emitHook struct {
	Face
	fn func(Event)
}

func (h emitHook) Emit(ev Event) {
	h.Face.Emit(ev)
	h.fn(ev)
}

func TestAContinueAsThePauseBeginsStillEndsIt(t *testing.T) {
	r := newLoopRig(t)
	onLand(r, "1", func() { r.loop.PauseRun("lunch", "pause") })
	r.loop.Face = emitHook{Face: r.face, fn: func(ev Event) {
		if ev.Kind == "paused" {
			r.loop.ContinueRun("go on")
		}
	}}

	if code := exitOf(t, runAsync(r)); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Errorf("landed %v", got)
	}
}

func TestAStopAfterPhaseNamesItsReasonInTheHaltEventAndTheReport(t *testing.T) {
	r := newLoopRig(t)
	onLand(r, "1", func() { r.loop.StopRun(StopAfterPhase, "review phase 1", "stop after this phase") })

	if code := r.run(RunOptions{}); code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}

	halts := r.events("halt")
	if len(halts) != 1 || halts[0].Fields["reason"] != "stopped by the watchdog: review phase 1" {
		t.Errorf("halt events %+v", halts)
	}
	st, err := r.store.Load("run-1")
	if err != nil {
		t.Fatal(err)
	}
	report := Report(st, r.loop.Plan)
	if n := strings.Count(report, "- stopped by the watchdog: review phase 1\n"); n != 1 {
		t.Errorf("halt reason in the report %d times, want once:\n%s", n, report)
	}
}
