package app

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/store"
)

const askConfig = `steps:
  plan:
    rounds: 0
  implement:
    rounds: 0
    timeout: 1m
`

func buildBinary(t *testing.T, pkg string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), filepath.Base(pkg))
	if out, err := exec.Command("go", "build", "-o", bin, pkg).CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", pkg, err, out)
	}
	return bin
}

type askHost struct {
	*simHost
	t      *testing.T
	agent  string
	mu     sync.Mutex
	url    string
	stderr bytes.Buffer
	cmd    *exec.Cmd
}

func (h *askHost) Start(pane, name, kind string, args []string) (core.Agent, error) {
	if name == "rloop-p1-implement" {
		for _, a := range args {
			if u, ok := strings.CutPrefix(a, "mcp_servers.r-loop.url="); ok {
				h.mu.Lock()
				h.url = u
				h.mu.Unlock()
			}
		}
	}
	return h.simHost.Start(pane, name, kind, args)
}

func (h *askHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	if agent != "rloop-p1-implement" || strings.HasPrefix(text, "r-loop:") {
		return h.simHost.Prompt(agent, text, wait, timeout)
	}
	h.simHost.mu.Lock()
	spec := h.opened[agent]
	h.simHost.mu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.url == "" {
		h.t.Error("implement started without an ask url")
		return nil
	}
	h.cmd = exec.Command(h.agent, h.url)
	h.cmd.Dir = spec.CWD
	h.cmd.Env = append(os.Environ(), "R_LOOP_SENTINEL="+spec.Env["R_LOOP_SENTINEL"])
	h.cmd.Stderr = &h.stderr
	return h.cmd.Start()
}

func (h *askHost) wait() string {
	h.mu.Lock()
	cmd := h.cmd
	h.mu.Unlock()
	if cmd != nil {
		cmd.Wait()
	}
	return h.stderr.String()
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type askRun struct {
	f     *fixture
	w     *Wiring
	host  *askHost
	clock *fakeClock
	code  chan int
}

func startAskRun(t *testing.T, dog func(w *Wiring) *dogHost) *askRun {
	t.Helper()
	f := newResumeFixture(t, askConfig)
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	host := &askHost{simHost: newSim(), t: t, agent: buildBinary(t, "./testdata/ask-agent.go")}
	f.sim(w, host.simHost)
	w.Dog.Host = dog(w)
	w.Loop.Sessions.Host = host
	clock := &fakeClock{now: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)}
	w.Loop.Sessions.Now = clock.Now
	r := &askRun{f: f, w: w, host: host, clock: clock, code: make(chan int, 1)}
	go func() { r.code <- w.Execute(core.RunOptions{Phases: []int{1}}) }()
	t.Cleanup(func() {
		host.mu.Lock()
		if host.cmd != nil && host.cmd.Process != nil {
			host.cmd.Process.Kill()
		}
		host.mu.Unlock()
	})
	return r
}

func (r *askRun) implement() core.StepState {
	st, err := store.New(r.f.root).Load(r.w.Loop.RunID)
	if err != nil {
		return ""
	}
	return st.Steps[core.StepKey{Run: r.w.Loop.RunID, Phase: 1, Kind: "implement", Attempt: 1}]
}

func (r *askRun) waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition never held; agent stderr: %s\n%s", r.host.stderr.String(), r.f.out)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (r *askRun) finish(t *testing.T) core.RunState {
	t.Helper()
	select {
	case code := <-r.code:
		if code != 0 {
			t.Fatalf("exit %d; agent stderr: %s\n%s", code, r.host.wait(), r.f.out)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("run never finished; agent stderr: %s", r.host.stderr.String())
	}
	if stderr := r.host.wait(); stderr != "" {
		t.Errorf("agent stderr: %s", stderr)
	}
	return r.f.load(r.w.Loop.RunID)
}

func (r *askRun) answerFile() string {
	b, _ := exec.Command("git", "-C", r.f.root, "show", "r-loop/phase-1:answer.txt").Output()
	return string(b)
}

func implementStates(st core.RunState) []string {
	var out []string
	for _, e := range stepEvents(st, "step") {
		if e.Step == "implement" {
			out = append(out, e.Fields["state"])
		}
	}
	return out
}

func TestAStepQuestionReachesTheWatchdogNotTheFaceAndWaitsPastTheBackstop(t *testing.T) {
	release := make(chan struct{})
	var dog *dogHost
	r := startAskRun(t, func(w *Wiring) *dogHost {
		dog = answeringDog(w, "postgres", "maintainer")
		answer := dog.question
		dog.question = func(id string) { <-release; answer(id) }
		return dog
	})
	r.waitFor(t, func() bool { return r.implement() == core.StepWaitingInput })
	var status bytes.Buffer
	env := r.f.env
	env.Stdout = &status
	if code := Status([]string{"--plain"}, env); code != 0 || !strings.Contains(status.String(), "question q1 Which database?") {
		t.Errorf("status exit %d:\n%s", code, status.String())
	}

	r.clock.Advance(2 * time.Hour)
	time.Sleep(100 * time.Millisecond)
	if got := r.implement(); got != core.StepWaitingInput {
		t.Fatalf("after the backstop the step is %s", got)
	}
	close(release)
	st := r.finish(t)

	if got := r.answerFile(); got != "postgres" {
		t.Errorf("agent received %q", got)
	}
	if !dog.prompted("question q1 from phase-1/implement: Which database? options: sqlite, postgres recommended: sqlite") {
		t.Errorf("the watchdog never got the question: %q", dog.Calls())
	}
	states := implementStates(st)
	at := slices.Index(states, "waiting-input")
	if at < 0 || !slices.Equal(states[at:], []string{"waiting-input", "running", "ok"}) {
		t.Errorf("implement states %v", states)
	}
	if len(st.Questions) != 1 || st.Questions[0].Answer != "postgres" || st.Questions[0].AnsweredBy != "maintainer" {
		t.Errorf("questions %+v", st.Questions)
	}
	if human := stepEvents(st, "human"); len(human) != 1 || human[0].Fields["id"] != "q1" {
		t.Errorf("human %+v", human)
	}
	if out := r.f.out.String(); strings.Contains(out, "?  q1") || strings.Contains(out, "answer q1>") {
		t.Errorf("the face asked:\n%s", out)
	}
}

func TestAWatchdogAnswerCitingTheRepositoryReachesTheAgent(t *testing.T) {
	r := startAskRun(t, func(w *Wiring) *dogHost { return answeringDog(w, "sqlite", "docs/topic/todo.md:1") })

	st := r.finish(t)

	if got := r.answerFile(); got != "sqlite" {
		t.Errorf("agent received %q", got)
	}
	if len(st.Questions) != 1 || st.Questions[0].AnsweredBy != "watchdog" || st.Questions[0].Citation != "docs/topic/todo.md:1" {
		t.Errorf("questions %+v", st.Questions)
	}
	if human := stepEvents(st, "human"); len(human) != 0 {
		t.Errorf("human %+v", human)
	}
}

func TestAGoneWatchdogHaltsTheRunAndTheQuestionIsNeverAnswered(t *testing.T) {
	r := startAskRun(t, func(w *Wiring) *dogHost {
		w.Dog.Sleep = func(time.Duration) {}
		return &dogHost{blocked: "question "}
	})

	var code int
	select {
	case code = <-r.code:
	case <-time.After(20 * time.Second):
		t.Fatal("run never halted")
	}

	if code != 5 {
		t.Fatalf("exit %d, want 5\n%s", code, r.f.out)
	}
	st := r.f.load(r.w.Loop.RunID)
	if len(st.Signals) != 1 || st.Signals[0].Source != core.SourceDriver || st.Signals[0].Kind != core.SignalHalt || st.Signals[0].Reason != "the watchdog is gone" {
		t.Errorf("signals %+v", st.Signals)
	}
	if got := stepEvents(st, "question-answered"); len(got) != 0 {
		t.Errorf("answered %+v", got)
	}
	if len(st.Questions) != 1 || st.Questions[0].AnsweredBy == "watchdog" || st.Questions[0].AnsweredBy == "maintainer" {
		t.Errorf("questions %+v", st.Questions)
	}
}

func TestExecuteServesTheAskChannelToTheLoopAndTheSessions(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	w, err := f.preflight(f.todo, "--plain", "--phases", "1")
	if err != nil {
		t.Fatal(err)
	}
	if w.Loop.Ask != w.Ask || w.Loop.Sessions.Ask != w.Ask {
		t.Fatalf("ask channel not handed over: loop %v sessions %v", w.Loop.Ask, w.Loop.Sessions.Ask)
	}
	f.sim(w, newSim())

	if code := w.Execute(core.RunOptions{Phases: []int{1}}); code != 0 {
		t.Fatalf("exit %d\n%s", code, f.out)
	}

	if _, err := os.Stat(filepath.Join(w.Store.Dir(w.Loop.RunID), "token")); err != nil {
		t.Errorf("no ask server token: %v", err)
	}
}

func TestDryRunStartsNoAskServer(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)

	if code := f.main(f.todo, "--dry-run", "--plain"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.err)
	}

	if tokens, _ := filepath.Glob(filepath.Join(f.root, ".r-loop/runs/*/token")); len(tokens) != 0 {
		t.Errorf("token written: %v", tokens)
	}
}
