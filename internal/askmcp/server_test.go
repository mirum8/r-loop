package askmcp

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"r-loop/internal/core"
)

var _ core.AskChannel = (*Server)(nil)

func serve(t *testing.T) (*Server, string, context.CancelFunc) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run-7")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &Server{RunDir: dir}
	base, err := s.Serve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return s, base, cancel
}

func connect(t *testing.T, url string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: url, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

type result struct {
	res *mcp.CallToolResult
	err error
}

func ask(cs *mcp.ClientSession, args map[string]any) <-chan result {
	out := make(chan result, 1)
	go func() {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ask_watchdog", Arguments: args})
		out <- result{res, err}
	}()
	return out
}

func next(t *testing.T, s *Server) core.Question {
	t.Helper()
	select {
	case q := <-s.Questions():
		return q
	case <-time.After(5 * time.Second):
		t.Fatal("no question arrived")
		return core.Question{}
	}
}

func asked(t *testing.T, r result) (string, string) {
	t.Helper()
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.res.IsError {
		t.Fatalf("tool error: %+v", r.res.Content)
	}
	m, _ := r.res.StructuredContent.(map[string]any)
	id, _ := m["id"].(string)
	status, _ := m["status"].(string)
	return id, status
}

func text(r result) string {
	var b strings.Builder
	for _, c := range r.res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func returned(t *testing.T, done <-chan result) result {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the call never returned")
		return result{}
	}
}

func TestWaitReturnsOnlyAfterAnInFlightHandlerHasReturned(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run-7")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &Server{RunDir: dir, RunID: "run-7", Store: &memStore{steps: map[core.StepKey]core.StepState{
		{Run: "run-7", Phase: "3", Kind: "implement", Attempt: 1}: core.StepRunning,
	}}}
	if _, err := s.Serve(ctx); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var finished atomic.Bool
	s.Handle(WatchdogHandlers{Signal: func(core.Signal) (bool, string) {
		close(entered)
		<-release
		finished.Store(true)
		return true, ""
	}})
	cs := connect(t, s.WatchdogURL())
	callDone := make(chan result, 1)
	go func() {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "signal", Arguments: map[string]any{"kind": "halt", "step": "phase-3/implement", "reason": "test", "evidence": "test"}})
		callDone <- result{res, err}
	}()
	select {
	case <-entered:
	case got := <-callDone:
		if got.res != nil {
			t.Fatalf("signal call returned before the handler was entered: %s, %v", text(got), got.err)
		}
		t.Fatalf("signal call returned before the handler was entered: %v", got.err)
	case <-time.After(2 * time.Second):
		t.Fatal("signal handler was not entered")
	}

	waitDone := make(chan struct{})
	cancel()
	go func() {
		s.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("Wait returned while the signal handler was still running")
	case <-time.After(200 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-waitDone:
		if !finished.Load() {
			t.Fatal("Wait returned before the signal handler finished")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after the handler finished")
	}
	select {
	case <-callDone:
	case <-time.After(2 * time.Second):
		t.Fatal("signal call did not return")
	}
}

func TestWaitWithoutServeReturns(t *testing.T) {
	done := make(chan struct{})
	go func() {
		(&Server{}).Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait blocked before Serve")
	}
}

func TestWaitTracksAToolHandlerAfterTheClientDisconnects(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run-7")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancelServer := context.WithCancel(context.Background())
	t.Cleanup(cancelServer)
	s := &Server{RunDir: dir, RunID: "run-7", Store: &memStore{steps: map[core.StepKey]core.StepState{
		{Run: "run-7", Phase: "3", Kind: "implement", Attempt: 1}: core.StepRunning,
	}}}
	if _, err := s.Serve(ctx); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var finished atomic.Bool
	s.Handle(WatchdogHandlers{Signal: func(core.Signal) (bool, string) {
		close(entered)
		<-release
		finished.Store(true)
		return true, ""
	}})
	cs := connect(t, s.WatchdogURL())
	callCtx, cancelCall := context.WithCancel(context.Background())
	callDone := make(chan struct{})
	go func() {
		cs.CallTool(callCtx, &mcp.CallToolParams{Name: "signal", Arguments: map[string]any{"kind": "halt", "step": "phase-3/implement", "reason": "test", "evidence": "test"}})
		close(callDone)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("signal handler was not entered")
	}
	cancelCall()
	select {
	case <-callDone:
	case <-time.After(2 * time.Second):
		t.Fatal("client call did not disconnect")
	}
	cancelServer()
	waitDone := make(chan struct{})
	go func() {
		s.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("Wait returned while the disconnected client's tool handler was still running")
	case <-time.After(1500 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case <-waitDone:
		if !finished.Load() {
			t.Fatal("Wait returned before the tool handler finished")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait did not return after the tool handler finished")
	}
}

func TestServeWritesAPrivateTokenAndReturnsTheBaseURL(t *testing.T) {
	s, base, _ := serve(t)

	data, err := os.ReadFile(filepath.Join(s.RunDir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	token := string(data)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(token) {
		t.Fatalf("token = %q", token)
	}
	info, _ := os.Stat(filepath.Join(s.RunDir, "token"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	if !regexp.MustCompile(`^http://127\.0\.0\.1:\d+/mcp/` + token + `$`).MatchString(base) {
		t.Fatalf("base = %q", base)
	}
}

func TestStepURLNamesThePhaseKindAndAttempt(t *testing.T) {
	s, base, _ := serve(t)

	if got := s.StepURL(core.StepKey{Run: "run-7", Phase: "3", Kind: "implement", Attempt: 2}); got != base+"/3/implement/2" {
		t.Fatalf("step url = %q", got)
	}
	if got := s.StepURL(core.StepKey{Run: "run-7", Phase: "3", Kind: "implement-rv-codex", Attempt: 1}); got != base+"/3/implement-rv-codex/1" {
		t.Fatalf("reviewer url = %q", got)
	}
}

func TestAskReturnsAtOnceWithTheIDAndTellsTheAgentToEndItsTurn(t *testing.T) {
	s, _, _ := serve(t)
	key := core.StepKey{Run: "run-7", Phase: "3", Kind: "plan", Attempt: 1}
	cs := connect(t, s.StepURL(key))

	done := ask(cs, map[string]any{"question": "Which store?", "options": []string{"jsonl", "sqlite"}, "recommended": "jsonl"})
	q := next(t, s)
	r := returned(t, done)

	if q.ID != "q1" || q.Step != key || q.Text != "Which store?" || q.Recommended != "jsonl" || strings.Join(q.Options, ",") != "jsonl,sqlite" || q.AskedAt.IsZero() {
		t.Fatalf("question = %+v", q)
	}
	if id, status := asked(t, r); id != "q1" || status != "asked" {
		t.Fatalf("result = %q %q", id, status)
	}
	if got := text(r); !strings.Contains(got, "the answer will arrive as your next message; end your turn now and do nothing else until it arrives") {
		t.Fatalf("text = %q", got)
	}
}

func TestASecondAskFromTheSameStepWhileOneIsOpenIsAToolErrorNamingIt(t *testing.T) {
	s, _, _ := serve(t)
	key := core.StepKey{Run: "run-7", Phase: "3", Kind: "implement-rv-claude", Attempt: 1}
	cs := connect(t, s.StepURL(key))
	done := ask(cs, map[string]any{"question": "Which store?"})
	next(t, s)
	asked(t, returned(t, done))

	r := returned(t, ask(cs, map[string]any{"question": "Which port?"}))

	if r.err != nil || !r.res.IsError {
		t.Fatalf("second ask = %+v %v", r.res, r.err)
	}
	if got := text(r); !strings.Contains(got, "q1") || !strings.Contains(got, "end your turn") {
		t.Fatalf("error text = %q", got)
	}
	noQuestion(t, s)
}

func TestAnAnsweredQuestionLetsTheStepAskAgain(t *testing.T) {
	s, _, _ := serve(t)
	cs := connect(t, s.StepURL(core.StepKey{Run: "run-7", Phase: "3", Kind: "plan", Attempt: 1}))
	done := ask(cs, map[string]any{"question": "Which store?"})
	next(t, s)
	asked(t, returned(t, done))

	if err := s.Answer("q1", "jsonl", "watchdog", "spec.html:3"); err != nil {
		t.Fatal(err)
	}
	done = ask(cs, map[string]any{"question": "Which port?"})

	if q := next(t, s); q.ID != "q2" || q.Text != "Which port?" {
		t.Fatalf("question = %+v", q)
	}
	if id, _ := asked(t, returned(t, done)); id != "q2" {
		t.Fatalf("id = %q", id)
	}
}

func TestAnswerRejectsAnUnknownOrAlreadyClosedID(t *testing.T) {
	s, _, _ := serve(t)
	cs := connect(t, s.StepURL(core.StepKey{Run: "run-7", Phase: "1", Kind: "implement", Attempt: 1}))

	if err := s.Answer("q9", "x", "person", ""); err == nil {
		t.Fatal("answered an unknown id")
	}
	done := ask(cs, map[string]any{"question": "Go on?"})
	next(t, s)
	returned(t, done)
	if err := s.Answer("q1", "yes", "watchdog", "spec §3"); err != nil {
		t.Fatal(err)
	}
	if err := s.Answer("q1", "no", "person", ""); err == nil {
		t.Fatal("answered the same id twice")
	}
}

func TestAWrongTokenOrUnknownPathIs404(t *testing.T) {
	s, base, _ := serve(t)
	good := s.StepURL(core.StepKey{Run: "run-7", Phase: "3", Kind: "plan", Attempt: 1})
	wrong := strings.Replace(good, "/mcp/", "/mcp/0", 1)
	for _, url := range []string{wrong, base, base + "/3/plan", base + "/x/plan/1", base + "/3/plan/one", base + "/3/plan/1/extra", base + "/999/bogus/1", base + "/3/plan/2", base + "/3/plan-rv-codex/1"} {
		resp, err := http.Post(url, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status = %d", url, resp.StatusCode)
		}
	}
}

func TestEachStepHasItsOwnOpenQuestion(t *testing.T) {
	s, _, _ := serve(t)
	planKey := core.StepKey{Run: "run-7", Phase: "2", Kind: "plan", Attempt: 1}
	rvKey := core.StepKey{Run: "run-7", Phase: "5", Kind: "implement-rv-claude", Attempt: 1}
	planDone := ask(connect(t, s.StepURL(planKey)), map[string]any{"question": "plan?"})
	rvDone := ask(connect(t, s.StepURL(rvKey)), map[string]any{"question": "review?"})

	byStep := map[core.StepKey]core.Question{}
	for range 2 {
		q := next(t, s)
		byStep[q.Step] = q
	}
	planID, _ := asked(t, returned(t, planDone))
	rvID, _ := asked(t, returned(t, rvDone))

	if byStep[planKey].Text != "plan?" || byStep[rvKey].Text != "review?" || planID == rvID || byStep[planKey].ID != planID || byStep[rvKey].ID != rvID {
		t.Fatalf("questions = %+v, ids %q %q", byStep, planID, rvID)
	}
}

func TestAResumedServerContinuesTheRunsQuestionSequence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run-7")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s := &Server{RunDir: dir, Seq: 4}
	if _, err := s.Serve(ctx); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, s.StepURL(core.StepKey{Run: "run-7", Phase: "3", Kind: "plan", Attempt: 2}))

	done := ask(cs, map[string]any{"question": "again?"})

	if q := next(t, s); q.ID != "q5" {
		t.Fatalf("id = %q", q.ID)
	}
	if id, _ := asked(t, returned(t, done)); id != "q5" {
		t.Fatalf("returned id = %q", id)
	}
}

func noQuestion(t *testing.T, s *Server) {
	t.Helper()
	select {
	case q := <-s.Questions():
		t.Fatalf("another question was sent: %s", q.ID)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestACallThatEndsBeforeTheDriverTakesItsQuestionDropsIt(t *testing.T) {
	s, _, _ := serve(t)
	key := core.StepKey{Run: "run-7", Phase: "10c", Kind: "plan", Attempt: 1}
	callCtx, hangUp := context.WithCancel(context.Background())
	gone := make(chan error, 1)
	go func() {
		_, err := connect(t, s.StepURL(key)).CallTool(callCtx, &mcp.CallToolParams{Name: "ask_watchdog", Arguments: map[string]any{"question": "Which store?"}})
		gone <- err
	}()
	time.Sleep(200 * time.Millisecond)
	hangUp()
	<-gone
	time.Sleep(50 * time.Millisecond)

	done := ask(connect(t, s.StepURL(key)), map[string]any{"question": "Which store, again?"})

	if q := next(t, s); q.ID != "q2" || q.Text != "Which store, again?" {
		t.Fatalf("question = %+v", q)
	}
	if id, _ := asked(t, returned(t, done)); id != "q2" {
		t.Fatalf("id = %q", id)
	}
}

func TestStoppingTheServerBeforeTheQuestionIsTakenFailsTheCall(t *testing.T) {
	s, _, cancel := serve(t)
	cs := connect(t, s.StepURL(core.StepKey{Run: "run-7", Phase: "3", Kind: "plan", Attempt: 1}))
	done := ask(cs, map[string]any{"question": "Which store?"})
	time.Sleep(100 * time.Millisecond)

	cancel()

	if r := returned(t, done); r.err == nil && !r.res.IsError {
		t.Fatalf("the call succeeded: %+v", r.res)
	}
}
