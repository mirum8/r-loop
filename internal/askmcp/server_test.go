package askmcp

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "ask_user", Arguments: args})
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

func answerText(t *testing.T, r result) string {
	t.Helper()
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.res.IsError {
		t.Fatalf("tool error: %+v", r.res.Content)
	}
	m, _ := r.res.StructuredContent.(map[string]any)
	a, _ := m["answer"].(string)
	return a
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

	if got := s.StepURL(core.StepKey{Run: "run-7", Phase: 3, Kind: "implement", Attempt: 2}); got != base+"/3/implement/2" {
		t.Fatalf("step url = %q", got)
	}
	if got := s.StepURL(core.StepKey{Run: "run-7", Phase: 3, Kind: "implement-rv-codex", Attempt: 1}); got != base+"/3/implement-rv-codex/1" {
		t.Fatalf("reviewer url = %q", got)
	}
}

func TestAskUserBlocksUntilAnsweredAndReturnsTheAnswer(t *testing.T) {
	s, _, _ := serve(t)
	key := core.StepKey{Run: "run-7", Phase: 3, Kind: "plan", Attempt: 1}
	cs := connect(t, s.StepURL(key))

	done := ask(cs, map[string]any{"question": "Which store?", "options": []string{"jsonl", "sqlite"}, "recommended": "jsonl"})
	q := next(t, s)

	if q.ID != "q1" || q.Step != key || q.Text != "Which store?" || q.Recommended != "jsonl" || strings.Join(q.Options, ",") != "jsonl,sqlite" || q.AskedAt.IsZero() {
		t.Fatalf("question = %+v", q)
	}
	select {
	case r := <-done:
		t.Fatalf("returned before the answer: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	if err := s.Answer("q1", "jsonl", "person", ""); err != nil {
		t.Fatal(err)
	}
	if got := answerText(t, <-done); got != "jsonl" {
		t.Fatalf("answer = %q", got)
	}
}

func TestAnswerRejectsAnUnknownOrAlreadyAnsweredID(t *testing.T) {
	s, _, _ := serve(t)
	cs := connect(t, s.StepURL(core.StepKey{Run: "run-7", Phase: 1, Kind: "implement", Attempt: 1}))

	if err := s.Answer("q9", "x", "person", ""); err == nil {
		t.Fatal("answered an unknown id")
	}
	done := ask(cs, map[string]any{"question": "Go on?"})
	next(t, s)
	if err := s.Answer("q1", "yes", "watchdog", "spec §3"); err != nil {
		t.Fatal(err)
	}
	<-done
	if err := s.Answer("q1", "no", "person", ""); err == nil {
		t.Fatal("answered the same id twice")
	}
}

func TestAWrongTokenOrUnknownPathIs404(t *testing.T) {
	s, base, _ := serve(t)
	good := s.StepURL(core.StepKey{Run: "run-7", Phase: 3, Kind: "plan", Attempt: 1})
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

func TestTwoConcurrentQuestionsGetDistinctIDsAndTheirOwnAnswers(t *testing.T) {
	s, _, _ := serve(t)
	planKey := core.StepKey{Run: "run-7", Phase: 2, Kind: "plan", Attempt: 1}
	rvKey := core.StepKey{Run: "run-7", Phase: 5, Kind: "implement-rv-claude", Attempt: 1}
	planDone := ask(connect(t, s.StepURL(planKey)), map[string]any{"question": "plan?"})
	rvDone := ask(connect(t, s.StepURL(rvKey)), map[string]any{"question": "review?"})

	byStep := map[core.StepKey]core.Question{}
	for range 2 {
		q := next(t, s)
		byStep[q.Step] = q
	}
	if byStep[planKey].Text != "plan?" || byStep[rvKey].Text != "review?" || byStep[planKey].ID == byStep[rvKey].ID {
		t.Fatalf("questions = %+v", byStep)
	}
	if err := s.Answer(byStep[rvKey].ID, "rv answer", "person", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Answer(byStep[planKey].ID, "plan answer", "person", ""); err != nil {
		t.Fatal(err)
	}
	if got := answerText(t, <-planDone); got != "plan answer" {
		t.Fatalf("plan got %q", got)
	}
	if got := answerText(t, <-rvDone); got != "rv answer" {
		t.Fatalf("reviewer got %q", got)
	}
}

func TestCancellingTheContextReturnsAnErrorAndNoAnswer(t *testing.T) {
	s, _, cancel := serve(t)
	cs := connect(t, s.StepURL(core.StepKey{Run: "run-7", Phase: 3, Kind: "plan", Attempt: 1}))
	done := ask(cs, map[string]any{"question": "Which store?", "recommended": "jsonl"})
	next(t, s)

	cancel()

	select {
	case r := <-done:
		if r.err == nil && !r.res.IsError {
			t.Fatalf("got an answer: %+v", r.res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call never returned")
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
	cs := connect(t, s.StepURL(core.StepKey{Run: "run-7", Phase: 3, Kind: "plan", Attempt: 2}))

	done := ask(cs, map[string]any{"question": "again?"})

	if q := next(t, s); q.ID != "q5" {
		t.Fatalf("id = %q", q.ID)
	}
	if err := s.Answer("q5", "yes", "person", ""); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestARepeatedAskFromTheSameStepReusesTheOpenQuestionAndGetsItsAnswer(t *testing.T) {
	s, _, _ := serve(t)
	key := core.StepKey{Run: "run-7", Phase: 3, Kind: "implement-rv-claude", Attempt: 1}
	args := map[string]any{"question": "Which store?", "options": []string{"jsonl", "sqlite"}}
	callCtx, hangUp := context.WithCancel(context.Background())
	gone := make(chan error, 1)
	go func() {
		_, err := connect(t, s.StepURL(key)).CallTool(callCtx, &mcp.CallToolParams{Name: "ask_user", Arguments: args})
		gone <- err
	}()
	if q := next(t, s); q.ID != "q1" {
		t.Fatalf("id = %q", q.ID)
	}
	hangUp()
	<-gone

	again := ask(connect(t, s.StepURL(key)), args)

	select {
	case q := <-s.Questions():
		s.Answer(q.ID, "", "person", "")
		t.Fatalf("a second question was escalated: %s", q.ID)
	case <-time.After(200 * time.Millisecond):
	}
	if err := s.Answer("q1", "jsonl", "person", ""); err != nil {
		t.Fatal(err)
	}
	if got := answerText(t, <-again); got != "jsonl" {
		t.Fatalf("answer = %q", got)
	}
}

func TestTheSameQuestionFromAnotherStepOrWithOtherOptionsIsNew(t *testing.T) {
	s, _, _ := serve(t)
	key := core.StepKey{Run: "run-7", Phase: 3, Kind: "implement", Attempt: 1}
	other := core.StepKey{Run: "run-7", Phase: 3, Kind: "implement-rv-claude", Attempt: 1}
	calls := []<-chan result{ask(connect(t, s.StepURL(key)), map[string]any{"question": "Which store?", "options": []string{"jsonl", "sqlite"}})}
	next(t, s)

	calls = append(calls,
		ask(connect(t, s.StepURL(other)), map[string]any{"question": "Which store?", "options": []string{"jsonl", "sqlite"}}),
		ask(connect(t, s.StepURL(key)), map[string]any{"question": "Which store?", "options": []string{"jsonl"}}))

	ids := map[string]bool{next(t, s).ID: true, next(t, s).ID: true}
	for _, id := range []string{"q1", "q2", "q3"} {
		s.Answer(id, "jsonl", "person", "")
	}
	for _, c := range calls {
		<-c
	}
	if !ids["q2"] || !ids["q3"] {
		t.Fatalf("ids = %v", ids)
	}
}
