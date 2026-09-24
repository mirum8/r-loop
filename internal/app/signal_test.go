package app

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/face/tui"
)

func waitForSignalTest(t *testing.T, limit time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("run did not reach the expected state")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func resultOfSignalTest(t *testing.T, done <-chan int, limit time.Duration) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(limit):
		t.Fatal("run did not stop")
		return -1
	}
}

func assertInterruptedSignalTest(t *testing.T, f *fixture, runID, reason string) {
	t.Helper()
	state := f.load(runID)
	if state.Status != core.RunHalted {
		t.Fatalf("run status = %s", state.Status)
	}
	halts := stepEvents(state, "halt")
	if len(halts) != 1 || halts[0].Fields["reason"] != reason {
		t.Fatalf("halt events = %+v; want %q", halts, reason)
	}
}

func installSignalTestDisplay(w *Wiring) (*io.PipeWriter, *bytes.Buffer) {
	in, writer := io.Pipe()
	out := &bytes.Buffer{}
	w.TUI = &tui.Face{In: in, Out: out}
	w.Face = w.TUI
	w.Loop.Face = w.TUI
	w.Gate.Face = w.TUI
	w.Gate.Boundary.Face = w.TUI
	w.Probe.Face = w.TUI
	w.Watch.Face = w.TUI
	w.Dog.Face = w.TUI
	w.Remedies.Face = w.TUI
	w.Notify.Emit = w.TUI.Emit
	return writer, out
}

type panicNotifier struct{}

func (panicNotifier) Fire(string, map[string]string) { panic("boom") }

type panicCleanupHost struct{ core.SessionHost }

func (panicCleanupHost) Close(string) error     { panic("boom") }
func (panicCleanupHost) ClosePane(string) error { panic("boom") }

type panicEventFace struct{ core.Face }

func (f panicEventFace) Emit(ev core.Event) {
	if ev.Kind == "phase-blocked" || ev.Kind == "halt" {
		panic("boom")
	}
	f.Face.Emit(ev)
}

func TestAPanicInTheDriverStopsTheDisplayAndHaltsTheRun(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.fail["rloop-p1-plan"] = true
	w, err := f.preflight(f.todo, "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	writer, out := installSignalTestDisplay(w)
	defer writer.Close()
	w.Loop.Notifier = panicNotifier{}
	var stderr bytes.Buffer
	w.Env.Stderr = &stderr
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	if code := resultOfSignalTest(t, done, 10*time.Second); code != 2 {
		t.Fatalf("exit = %d", code)
	}
	if state := f.load(w.Loop.RunID); state.Status != core.RunHalted {
		t.Fatalf("run status = %s", state.Status)
	}
	if !strings.Contains(stderr.String(), "r-loop: panic: boom") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if !strings.Contains(out.String(), "\x1b[?1049l") {
		t.Fatalf("terminal was not restored: %q", out.String())
	}
}

func TestAPanicInExecuteCleanupStillRestoresTheTerminal(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	w, err := f.preflight(f.todo, "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	sim := newSim()
	f.sim(w, sim)
	writer, out := installSignalTestDisplay(w)
	defer writer.Close()
	w.Dog.Host = panicCleanupHost{SessionHost: w.Dog.Host}
	var stderr bytes.Buffer
	w.Env.Stderr = &stderr
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	if code := resultOfSignalTest(t, done, 10*time.Second); code != 2 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "r-loop: panic: boom") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if !strings.Contains(out.String(), "\x1b[?1049l") {
		t.Fatalf("terminal was not restored: %q", out.String())
	}
}

func TestAPanicInTheFaceStillRestoresTheTerminal(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.fail["rloop-p1-plan"] = true
	w, err := f.preflight(f.todo, "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	writer, out := installSignalTestDisplay(w)
	defer writer.Close()
	pf := panicEventFace{Face: w.TUI}
	w.Face = pf
	w.Loop.Face = pf
	var stderr bytes.Buffer
	w.Env.Stderr = &stderr
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	if code := resultOfSignalTest(t, done, 10*time.Second); code != 2 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stderr.String(), "r-loop: panic: boom") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if !strings.Contains(out.String(), "\x1b[?1049l") {
		t.Fatalf("terminal was not restored: %q", out.String())
	}
	if state := f.load(w.Loop.RunID); state.Status != core.RunHalted {
		t.Fatalf("run status = %s", state.Status)
	}
}

func TestEachSignalHaltsALiveStepAsInterrupted(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{
		{"SIGINT", syscall.SIGINT},
		{"SIGTERM", syscall.SIGTERM},
		{"SIGHUP", syscall.SIGHUP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newResumeFixture(t, noReviewConfig)
			sim := newSim()
			sim.hang["rloop-p1-plan"] = true
			w, err := f.preflight(f.todo, "--plain", "--phases", "1")
			if err != nil {
				t.Fatal(err)
			}
			f.sim(w, sim)
			done := make(chan int, 1)
			go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
			waitForSignalTest(t, 5*time.Second, func() bool { return slices.Contains(sim.promptedAgents(), "rloop-p1-plan") })
			if err := syscall.Kill(os.Getpid(), tc.sig); err != nil {
				t.Fatal(err)
			}
			if code := resultOfSignalTest(t, done, 10*time.Second); code != 4 {
				t.Fatalf("exit = %d", code)
			}
			assertInterruptedSignalTest(t, f, w.Loop.RunID, "interrupted: "+tc.name)
		})
	}
}

func TestSIGTERMDuringALongGateKillsTheGateAbortsTheMergeAndHaltsTheRun(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	pidPath := filepath.Join(t.TempDir(), "gate.pid")
	f.write("docs/topic/todo.md", "# t\n\n### Phase 1 — one\n**Depends on:** —\n- [ ] a\n**Done when:** `echo $$ > '"+pidPath+"'; sleep 300`\n")
	f.commit()
	before := git(t, f.root, "rev-parse", "HEAD")
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, newSim())
	w.Loop.Lander = w.Gate
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	var pid int
	waitForSignalTest(t, 20*time.Second, func() bool {
		b, err := os.ReadFile(pidPath)
		if err != nil {
			return false
		}
		pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		return pid > 0
	})
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := resultOfSignalTest(t, done, 30*time.Second); code != 4 {
		t.Fatalf("exit = %d", code)
	}
	waitForSignalTest(t, 5*time.Second, func() bool { return syscall.Kill(-pid, 0) == syscall.ESRCH })
	if got := git(t, f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("primary tree dirty: %s", got)
	}
	if got := git(t, f.root, "rev-parse", "HEAD"); got != before {
		t.Fatalf("HEAD moved from %s to %s", before, got)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".git", "MERGE_HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("MERGE_HEAD still exists: %v", err)
	}
	assertInterruptedSignalTest(t, f, w.Loop.RunID, "interrupted: SIGTERM")
}

func TestAClosedDisplayHaltsTheRunAsInterrupted(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.hang["rloop-p1-plan"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	writer, _ := installSignalTestDisplay(w)
	defer writer.Close()
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	waitForSignalTest(t, 5*time.Second, func() bool { return slices.Contains(sim.promptedAgents(), "rloop-p1-plan") })
	w.TUI.Stop()
	if code := resultOfSignalTest(t, done, 5*time.Second); code != 4 {
		t.Fatalf("exit = %d", code)
	}
	assertInterruptedSignalTest(t, f, w.Loop.RunID, "interrupted: display closed")
}

func TestADisplayErrorHaltsTheRunNamingIt(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.hang["rloop-p1-plan"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	writer, _ := installSignalTestDisplay(w)
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	waitForSignalTest(t, 5*time.Second, func() bool { return slices.Contains(sim.promptedAgents(), "rloop-p1-plan") })
	writer.CloseWithError(errors.New("tty gone"))
	if code := resultOfSignalTest(t, done, 5*time.Second); code != 4 {
		t.Fatalf("exit = %d", code)
	}
	halts := stepEvents(f.load(w.Loop.RunID), "halt")
	if len(halts) != 1 || !strings.HasPrefix(halts[0].Fields["reason"], "interrupted: display exited:") || !strings.Contains(halts[0].Fields["reason"], "tty gone") {
		t.Fatalf("halt events = %+v", halts)
	}
}

func TestASignalWhileTheDisplayWaitsForQuitEndsExecute(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.fail["rloop-p1-plan"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	writer, _ := installSignalTestDisplay(w)
	defer writer.Close()
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	waitForSignalTest(t, 10*time.Second, func() bool {
		state, err := w.Store.Load(w.Loop.RunID)
		return err == nil && state.Status == core.RunHalted
	})
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := resultOfSignalTest(t, done, 30*time.Second); code != 1 {
		t.Fatalf("exit = %d", code)
	}
}

func TestAForceQuitFromTheDisplayRecordsTheRunAborted(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.hang["rloop-p1-plan"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	writer, _ := installSignalTestDisplay(w)
	defer writer.Close()
	w.TUI.Abort = func() error { return w.Store.MarkAbort(w.Loop.RunID) }
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	waitForSignalTest(t, 5*time.Second, func() bool { return slices.Contains(sim.promptedAgents(), "rloop-p1-plan") })
	if _, err := writer.Write([]byte("\x03\x03")); err != nil {
		t.Fatal(err)
	}
	if code := resultOfSignalTest(t, done, 5*time.Second); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	state := f.load(w.Loop.RunID)
	if state.Status != core.RunHalted || len(stepEvents(state, "aborted")) != 1 {
		t.Fatalf("status = %s, aborted events = %+v", state.Status, stepEvents(state, "aborted"))
	}
}

func TestSignalAfterTUIStartsPrintsHaltReasonAndResume(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.hang["rloop-p1-plan"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	writer, out := installSignalTestDisplay(w)
	defer writer.Close()
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	waitForSignalTest(t, 5*time.Second, func() bool { return slices.Contains(sim.promptedAgents(), "rloop-p1-plan") })
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := resultOfSignalTest(t, done, 5*time.Second); code != 4 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"halted: interrupted: SIGTERM", "r-loop resume", "report:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("terminal lacks %q: %q", want, out.String())
		}
	}
}

func TestForceQuitPrintsAbortedAndResume(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.hang["rloop-p1-plan"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	writer, out := installSignalTestDisplay(w)
	defer writer.Close()
	w.TUI.Abort = func() error { return w.Store.MarkAbort(w.Loop.RunID) }
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	waitForSignalTest(t, 5*time.Second, func() bool { return slices.Contains(sim.promptedAgents(), "rloop-p1-plan") })
	if _, err := writer.Write([]byte("\x03\x03")); err != nil {
		t.Fatal(err)
	}
	if code := resultOfSignalTest(t, done, 5*time.Second); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"aborted", "r-loop resume", "report:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("terminal lacks %q: %q", want, out.String())
		}
	}
}
