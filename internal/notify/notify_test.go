package notify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/store"
)

type events struct {
	mu  sync.Mutex
	got []core.Event
}

func (e *events) Emit(ev core.Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.got = append(e.got, ev)
}

func (e *events) Close() {}

func (e *events) kind(kind string) []core.Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []core.Event
	for _, ev := range e.got {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func hookEnv() map[string]string {
	return map[string]string{
		"R_LOOP_RUN":    "20260918-101500",
		"R_LOOP_STATUS": "halted",
		"R_LOOP_PHASE":  "4",
		"R_LOOP_STEP":   "implement",
		"R_LOOP_REASON": "diff is empty",
		"R_LOOP_TODO":   "/p/docs/t/todo.md",
		"R_LOOP_REPORT": "/p/.r-loop/runs/20260918-101500/report.md",
	}
}

func TestHookSeesTheRunEnvironment(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "env")
	t.Setenv("R_LOOP_INHERITED", "yes")
	sh := &Shell{Log: filepath.Join(dir, "notify.log"), Emit: (&events{}).Emit}

	sh.Fire("env > "+out, hookEnv())

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	want := []string{
		"R_LOOP_RUN=20260918-101500",
		"R_LOOP_STATUS=halted",
		"R_LOOP_PHASE=4",
		"R_LOOP_STEP=implement",
		"R_LOOP_REASON=diff is empty",
		"R_LOOP_TODO=/p/docs/t/todo.md",
		"R_LOOP_REPORT=/p/.r-loop/runs/20260918-101500/report.md",
		"R_LOOP_INHERITED=yes",
	}
	for _, w := range want {
		found := false
		for _, l := range lines {
			found = found || l == w
		}
		if !found {
			t.Errorf("hook env lacks %q:\n%s", w, data)
		}
	}
}

func TestEmptyHookIsANoOp(t *testing.T) {
	dir := t.TempDir()
	ev := &events{}
	sh := &Shell{Log: filepath.Join(dir, "notify.log"), Emit: ev.Emit}

	sh.Fire("", hookEnv())

	if _, err := os.Stat(sh.Log); !os.IsNotExist(err) {
		t.Errorf("notify.log written: %v", err)
	}
	if len(ev.got) != 0 {
		t.Errorf("events %v", ev.got)
	}
}

func TestFailingHookIsLoggedWithItsOutputAndEmitted(t *testing.T) {
	dir := t.TempDir()
	ev := &events{}
	sh := &Shell{Log: filepath.Join(dir, "notify.log"), Emit: ev.Emit}

	sh.Fire("echo no phone; exit 1", hookEnv())

	data, err := os.ReadFile(sh.Log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "echo no phone; exit 1") || !strings.Contains(string(data), "exit status 1") || !strings.Contains(string(data), "no phone") {
		t.Errorf("notify.log:\n%s", data)
	}
	failed := ev.kind("notify-failed")
	if len(failed) != 1 || failed[0].Fields["status"] != "halted" || failed[0].Fields["reason"] != "exit status 1" {
		t.Errorf("events %+v", ev.got)
	}
}

func TestHookPastItsTimeoutIsKilledAndLogged(t *testing.T) {
	dir := t.TempDir()
	ev := &events{}
	sh := &Shell{Log: filepath.Join(dir, "notify.log"), Emit: ev.Emit, Timeout: 100 * time.Millisecond}

	start := time.Now()
	sh.Fire("sleep 5", hookEnv())

	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("Fire took %v", d)
	}
	data, _ := os.ReadFile(sh.Log)
	if !strings.Contains(string(data), "timed out after 100ms") {
		t.Errorf("notify.log:\n%s", data)
	}
	if failed := ev.kind("notify-failed"); len(failed) != 1 || failed[0].Fields["reason"] != "timed out after 100ms" {
		t.Errorf("events %+v", ev.got)
	}
}

func TestAHookThatExitsZeroButLeavesABackgroundChildIsNotAFailure(t *testing.T) {
	dir := t.TempDir()
	ev := &events{}
	sh := &Shell{Log: filepath.Join(dir, "notify.log"), Emit: ev.Emit}
	start := time.Now()
	sh.Fire("sleep 30 &", hookEnv())
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Fire took %v", time.Since(start))
	}
	if got := ev.kind("notify-failed"); len(got) != 0 {
		t.Fatalf("events = %+v", got)
	}
	if _, err := os.Stat(sh.Log); !os.IsNotExist(err) {
		t.Fatalf("notify log exists: %v", err)
	}
}

func TestAHookThatFailsAndLeavesABackgroundChildIsStillAFailure(t *testing.T) {
	dir := t.TempDir()
	ev := &events{}
	sh := &Shell{Log: filepath.Join(dir, "notify.log"), Emit: ev.Emit}
	start := time.Now()
	sh.Fire("sleep 30 & exit 3", hookEnv())
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Fire took %v", time.Since(start))
	}
	got := ev.kind("notify-failed")
	if len(got) != 1 || !strings.Contains(got[0].Fields["reason"], "exit status 3") {
		t.Fatalf("events = %+v", got)
	}
}

func TestDefaultTimeoutIsSixtySeconds(t *testing.T) {
	if (&Shell{}).timeout() != 60*time.Second {
		t.Fatal((&Shell{}).timeout())
	}
}

type repo struct{ core.Repo }

func (repo) HeadBranch() (string, error) { return "main", nil }
func (r repo) Root() string              { return "/nowhere" }

type failingRunner struct{}

func (failingRunner) Run(ctx context.Context, ref core.StepRef, obs core.Observer) core.Outcome {
	return core.Outcome{State: core.StepFailed, Reason: "tests red"}
}

func runFakeLoop(t *testing.T, n core.Notifier) (int, *events) {
	t.Helper()
	st := store.New(t.TempDir())
	id, err := st.Create(core.RunMeta{Todo: "todo.md"})
	if err != nil {
		t.Fatal(err)
	}
	face := &events{}
	loop := &core.RunLoop{
		Plan:     core.Plan{Phases: []core.Phase{{ID: "1", Items: []core.Item{{Text: "a"}}}}},
		TodoPath: "todo.md",
		Kinds:    []core.StepKind{{Name: "implement", Check: "diff"}},
		Sessions: &core.SessionManager{Repo: repo{}},
		Store:    st,
		Face:     face,
		Notifier: n,
		Hooks:    core.Hooks{OnHalt: "exit 1", OnWarn: "exit 1"},
		Runners:  map[string]core.StepRunner{"diff": failingRunner{}},
		RunID:    id,
	}
	return loop.Run(context.Background(), core.RunOptions{}), face
}

func TestFailingHookLeavesTheLoopsExitCodeUnchanged(t *testing.T) {
	without, _ := runFakeLoop(t, nil)
	face := &events{}
	sh := &Shell{Log: filepath.Join(t.TempDir(), "notify.log"), Emit: face.Emit}

	with, _ := runFakeLoop(t, sh)

	if without == 0 || with != without {
		t.Fatalf("exit with failing hook %d, without hooks %d", with, without)
	}
	if got := len(face.kind("notify-failed")); got != 2 {
		t.Fatalf("notify-failed events %d, want 2 (blocked and halted)", got)
	}
}

func TestASlowWarnHookDoesNotDelayTheHalt(t *testing.T) {
	st := store.New(t.TempDir())
	id, err := st.Create(core.RunMeta{Todo: "todo.md"})
	if err != nil {
		t.Fatal(err)
	}
	face := &events{}
	loop := &core.RunLoop{
		Plan:     core.Plan{Phases: []core.Phase{{ID: "1", Items: []core.Item{{Text: "a"}}}}},
		TodoPath: "todo.md",
		Kinds:    []core.StepKind{{Name: "implement", Check: "diff"}},
		Sessions: &core.SessionManager{Repo: repo{}},
		Store:    st,
		Face:     face,
		Notifier: &Shell{Log: filepath.Join(t.TempDir(), "notify.log"), Emit: face.Emit, Timeout: 2 * time.Second},
		Hooks:    core.Hooks{OnWarn: "sleep 30", OnHalt: "exit 1"},
		Runners:  map[string]core.StepRunner{"diff": failingRunner{}},
		RunID:    id,
	}
	returned := make(chan int, 1)
	go func() { returned <- loop.Run(context.Background(), core.RunOptions{}) }()
	deadline := time.After(time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for len(face.kind("halt")) == 0 {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("halt was delayed by the warn hook")
		}
	}
	select {
	case code := <-returned:
		if code == 0 {
			t.Fatal("failed run returned success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not finish after hook timeout")
	}
	failed := face.kind("notify-failed")
	if len(failed) != 2 || failed[0].Fields["status"] != "blocked" || failed[0].Fields["reason"] != "timed out after 2s" || failed[1].Fields["status"] != "halted" || !strings.Contains(failed[1].Fields["reason"], "exit status 1") {
		t.Fatalf("notify-failed events = %+v", failed)
	}
}

func TestTimeoutKillsTheHooksChildrenToo(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	sh := &Shell{Log: filepath.Join(dir, "notify.log"), Emit: (&events{}).Emit, Timeout: 100 * time.Millisecond}

	sh.Fire("(sleep 1; touch "+marker+") & wait", hookEnv())
	time.Sleep(1500 * time.Millisecond)

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("hook child outlived the timeout: %v", err)
	}
}
