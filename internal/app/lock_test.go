package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/store"
)

func TestALoserNamesAWinnerThatPublishesLate(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	st := store.New(f.root)
	lock, err := st.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release("", 0)
	ready := make(chan error, 1)
	go func() {
		time.Sleep(2500 * time.Millisecond)
		err := st.SetCurrent("20260918-120000", 4242)
		lock.Publish()
		ready <- err
	}()
	_, err = f.preflight(f.todo, "--plain")
	if got := <-ready; got != nil {
		t.Fatal(got)
	}
	if code := exitCode(t, err); code != 4 || !strings.Contains(err.Error(), "run 20260918-120000 is live in pid 4242") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestALoserNamesTheLiveRunNotAStalePointer(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	st := store.New(f.root)
	if err := st.SetCurrent("20260918-100000", 999999); err != nil {
		t.Fatal(err)
	}
	lock, err := st.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release("", 0)
	ready := make(chan error, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		err := st.ClearCurrent("20260918-100000", 999999)
		if err == nil {
			err = st.SetCurrent("20260918-120000", 4242)
		}
		lock.Publish()
		ready <- err
	}()
	_, err = f.preflight(f.todo, "--plain")
	if got := <-ready; got != nil {
		t.Fatal(got)
	}
	if code := exitCode(t, err); code != 4 || !strings.Contains(err.Error(), "run 20260918-120000 is live in pid 4242") || strings.Contains(err.Error(), "20260918-100000") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestALoserWhoseWinnerGivesUpProceeds(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	st := store.New(f.root)
	lock, err := st.Lock()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { time.Sleep(200 * time.Millisecond); done <- lock.Release("", 0) }()
	w, err := f.preflight(f.todo, "--plain")
	if got := <-done; got != nil {
		t.Fatal(got)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer w.release()
	if id, _, ok := st.Current(); !ok || id != w.Loop.RunID {
		t.Fatalf("current %s %t", id, ok)
	}
}

func TestTwoConcurrentStartsOneProceedsAndTheOtherExits4NamingIt(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	if err := store.EnsureExcluded(f.root); err != nil {
		t.Fatal(err)
	}
	type result struct {
		w   *Wiring
		err error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			env := f.env
			env.Stdout, env.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
			opts, err := ParseArgs([]string{f.todo, "--plain"})
			if err != nil {
				results <- result{err: err}
				return
			}
			w, err := Wire(opts, env)
			if err == nil {
				err = Preflight(w)
			}
			results <- result{w, err}
		}()
	}
	close(start)
	a, b := <-results, <-results
	if a.err != nil {
		a, b = b, a
	}
	if a.err != nil || a.w == nil {
		t.Fatalf("no winner: %v %v", a.err, b.err)
	}
	defer a.w.release()
	if code := exitCode(t, b.err); code != 4 || !strings.Contains(b.err.Error(), "run "+a.w.Loop.RunID+" is live in pid") {
		t.Fatalf("loser code=%d err=%v", code, b.err)
	}
	entries, err := os.ReadDir(filepath.Join(f.root, ".r-loop", "runs"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("run dirs %d", count)
	}
}

func TestAReusedPidInCurrentDoesNotBlockANewStart(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	st := store.New(f.root)
	if err := st.SetCurrent("20260918-101500", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	w, err := f.preflight(f.todo, "--plain")
	if err != nil {
		t.Fatal(err)
	}
	defer w.release()
	want := fmt.Sprintf("cleared stale run pointer 20260918-101500: pid %d holds no run lock", os.Getpid())
	if !strings.Contains(f.out.String(), want) {
		t.Fatalf("out %q", f.out)
	}
	if id, _, ok := st.Current(); !ok || id != w.Loop.RunID {
		t.Fatalf("current %s %t", id, ok)
	}
}

func TestAReusedPidInCurrentDoesNotBlockResume(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	id, code := f.firstRun(first, "--phases", "1")
	if code != 1 {
		t.Fatalf("first exit %d", code)
	}
	if err := store.New(f.root).SetCurrent(id, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	code, _, err := f.resume(newSim())
	if err != nil || code != 0 || f.load(id).Status != core.RunFinished {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestAbortIgnoresACurrentWhosePidIsReused(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	st := store.New(f.root)
	id, err := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCurrent(id, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if code := Main([]string{"abort"}, f.env); code != 2 || st.Aborted(id) {
		t.Fatalf("code=%d aborted=%t", code, st.Aborted(id))
	}
}

func TestAbortDuringAnotherRunsStartAbortsTheLiveRunNotAStaleOne(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	st := store.New(f.root)
	a, err := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCurrent(a, 999999); err != nil {
		t.Fatal(err)
	}
	lock, err := st.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release(b, 4242)
	ready := make(chan error, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		err := st.ClearCurrent(a, 999999)
		if err == nil {
			err = st.SetCurrent(b, 4242)
		}
		lock.Publish()
		ready <- err
	}()
	code := Main([]string{"abort"}, f.env)
	if got := <-ready; got != nil {
		t.Fatal(got)
	}
	if code != 0 || st.Aborted(a) || !st.Aborted(b) {
		t.Fatalf("code=%d aborted A=%t B=%t", code, st.Aborted(a), st.Aborted(b))
	}
}

func TestStatusCallsTheDriverGoneWhenItsPidIsReused(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	st := store.New(f.root)
	id, err := st.Create(core.RunMeta{Todo: f.todo, Started: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Append(id, core.Record{Kind: core.RecordRun, Run: core.RunRunning}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCurrent(id, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("run %s running (driver pid %d not alive — r-loop resume)\n", id, os.Getpid())
	if code := f.main("status", "--plain"); code != 0 || !strings.HasPrefix(f.out.String(), want) {
		t.Fatalf("code=%d out=%q err=%q", code, f.out, f.err)
	}
}

func TestAStartAfterTheLockHolderCrashedTakesTheLock(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	st := store.New(f.root)
	lock, err := st.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCurrent("20260918-090000", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release("", 0); err != nil {
		t.Fatal(err)
	}
	w, err := f.preflight(f.todo, "--plain")
	if err != nil {
		t.Fatal(err)
	}
	defer w.release()
	if !strings.Contains(f.out.String(), "cleared stale run pointer 20260918-090000") {
		t.Fatalf("out %q", f.out)
	}
}

func TestResumeAfterTheLockHolderCrashedTakesTheLock(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	id, code := f.firstRun(first, "--phases", "1")
	if code != 1 {
		t.Fatalf("first exit %d", code)
	}
	st := store.New(f.root)
	lock, err := st.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCurrent(id, os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release("", 0); err != nil {
		t.Fatal(err)
	}
	code, _, err = f.resume(newSim())
	if code != 0 || err != nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestAnExitingDriverLeavesAnotherRunsPointer(t *testing.T) {
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
	if err := w.Store.SetCurrent("20990101-000000", 7); err != nil {
		t.Fatal(err)
	}
	if err := w.Store.MarkAbort(w.Loop.RunID); err != nil {
		t.Fatal(err)
	}
	if code := resultOfSignalTest(t, done, 5*time.Second); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if id, pid, ok := w.Store.Current(); !ok || id != "20990101-000000" || pid != 7 {
		t.Fatalf("current=%s %d %t", id, pid, ok)
	}
	if _, _, ok := w.Store.Live(); ok {
		t.Fatal("lock remains held")
	}
}

func finishedTUIWaitingForQ(t *testing.T) (*fixture, *Wiring, *io.PipeWriter, <-chan int, *bytes.Buffer) {
	t.Helper()
	f := newResumeFixture(t, noReviewConfig)
	sim := newSim()
	sim.fail["rloop-p1-plan"] = true
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	f.sim(w, sim)
	writer, out := installSignalTestDisplay(w)
	done := make(chan int, 1)
	go func() { done <- w.Execute(core.RunOptions{Phases: []string{"1"}}) }()
	waitForSignalTest(t, 10*time.Second, func() bool {
		state, err := store.New(f.root).Load(w.Loop.RunID)
		if err != nil {
			return false
		}
		_, _, current := w.Store.Current()
		return state.Status == core.RunHalted && !current
	})
	return f, w, writer, done, out
}

func TestAFinishedTUIRunReleasesTheRunBeforeWaitingForQ(t *testing.T) {
	_, w, writer, done, out := finishedTUIWaitingForQ(t)
	defer writer.Close()
	if _, _, live := w.Store.Live(); live {
		t.Fatal("run remains live")
	}
	select {
	case code := <-done:
		t.Fatalf("Execute returned before q: %d", code)
	default:
	}
	if _, err := writer.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if code := resultOfSignalTest(t, done, 5*time.Second); code != 1 {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"r-loop resume", "report:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q: %q", want, out)
		}
	}
}

func TestResumeFromAnotherTerminalWhileAFinishedTUIWaitsForQ(t *testing.T) {
	f, w, writer, done, _ := finishedTUIWaitingForQ(t)
	defer writer.Close()
	env := f.env
	env.Stdout, env.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	resumed, _, err := PrepareResume([]string{"--plain"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Loop.RunID != w.Loop.RunID {
		t.Fatalf("resumed %s, want %s", resumed.Loop.RunID, w.Loop.RunID)
	}
	resumed.release()
	if _, err := writer.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if code := resultOfSignalTest(t, done, 5*time.Second); code != 1 {
		t.Fatalf("code=%d", code)
	}
}

func TestANewRunFromAnotherTerminalWhileAFinishedTUIWaitsForQ(t *testing.T) {
	f, _, writer, done, _ := finishedTUIWaitingForQ(t)
	defer writer.Close()
	git(t, f.root, "worktree", "remove", "--force", ".r-loop/wt/phase-1")
	git(t, f.root, "branch", "-D", "r-loop/phase-1")
	env := f.env
	env.Stdout, env.Stderr = &bytes.Buffer{}, &bytes.Buffer{}
	opts, err := ParseArgs([]string{f.todo, "--plain"})
	if err != nil {
		t.Fatal(err)
	}
	next, err := Wire(opts, env)
	if err == nil {
		err = Preflight(next)
	}
	if err != nil {
		t.Fatal(err)
	}
	next.release()
	if _, err := writer.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if code := resultOfSignalTest(t, done, 5*time.Second); code != 1 {
		t.Fatalf("code=%d", code)
	}
}
