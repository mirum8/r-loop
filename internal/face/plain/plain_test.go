package plain

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"r-loop/internal/core"
)

var at = time.Date(2026, 9, 18, 14, 3, 9, 0, time.Local)

func TestStepEventLine(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	f.Emit(core.Event{At: at, Kind: "step", Phase: 4, Step: "implement", Fields: map[string]string{
		"state": "failed", "provider": "codex", "reason": "evidence missing: diff",
	}})

	want := "14:03:09  phase 4  implement  failed  codex  evidence missing: diff\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestWarningLine(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	f.Emit(core.Event{At: at, Kind: "warning", Phase: 4, Step: "implement", Fields: map[string]string{"reason": "diff is large"}})

	if want := "!  phase 4 implement: diff is large\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestCloseNamesTheReport(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out, Report: "/repo/.r-loop/runs/r1/report.md"}

	f.Close()

	if want := "report: /repo/.r-loop/runs/r1/report.md\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestAskWithoutATerminalLeavesTheQuestionOpen(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out, In: strings.NewReader("1\n")}

	answer, err := f.Ask(core.Question{ID: "q3", Step: core.StepKey{Phase: 4, Kind: "implement"}, Text: "Which port?", Options: []string{"8080", "9090"}})

	if !errors.Is(err, core.ErrNoInput) || answer != "" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	want := "?  q3  phase 4 implement: Which port?\n   1. 8080\n   2. 9090\nquestion q3 stays open — answer from the TUI or resume later\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestAskOnATerminalReadsOneLine(t *testing.T) {
	q := core.Question{ID: "q3", Step: core.StepKey{Phase: 4, Kind: "implement"}, Text: "Which port?", Options: []string{"8080", "9090"}}
	for _, tc := range []struct{ in, want string }{
		{"2\n", "9090"},
		{"use 7000 instead\n", "use 7000 instead"},
		{"\n  \n1\n", "8080"},
		{"3\n", "3"},
	} {
		var out bytes.Buffer
		f := &Face{Out: &out, In: strings.NewReader(tc.in), TTY: true}

		answer, err := f.Ask(q)

		if err != nil || answer != tc.want {
			t.Errorf("in %q: answer=%q err=%v, want %q", tc.in, answer, err, tc.want)
		}
	}
}

func TestAskOnATerminalRepromptsOnAnEmptyLine(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out, In: strings.NewReader("\n2\n"), TTY: true}

	f.Ask(core.Question{ID: "q3", Step: core.StepKey{Phase: 4, Kind: "implement"}, Text: "Which port?", Options: []string{"8080", "9090"}})

	if got := strings.Count(out.String(), "answer q3> "); got != 2 {
		t.Fatalf("prompted %d times:\n%s", got, out.String())
	}
}

func TestAskOnATerminalAtEndOfInputHasNoInput(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out, In: strings.NewReader(""), TTY: true}

	_, err := f.Ask(core.Question{ID: "q3", Text: "Which port?"})

	if !errors.Is(err, core.ErrNoInput) {
		t.Fatalf("err %v", err)
	}
}

func TestEmitIsNotBlockedWhileAskWaitsForInput(t *testing.T) {
	var out syncBuffer
	r, w := io.Pipe()
	f := &Face{Out: &out, In: r, TTY: true}
	done := make(chan string)
	go func() { a, _ := f.Ask(core.Question{ID: "q3", Text: "Which port?"}); done <- a }()
	for !strings.Contains(out.String(), "answer q3> ") {
		time.Sleep(time.Millisecond)
	}

	f.Emit(core.Event{At: at, Kind: "warning", Fields: map[string]string{"reason": "still here"}})
	w.Write([]byte("8080\n"))

	if a := <-done; a != "8080" || !strings.Contains(out.String(), "!  still here\n") {
		t.Fatalf("answer %q, out:\n%s", a, out.String())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestOtherEventsAreOneLine(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	f.Emit(core.Event{At: at, Kind: "phase-blocked", Phase: 4, Step: "implement", Fields: map[string]string{"phase": "4", "reason": "backstop"}})

	if want := "14:03:09  phase 4  phase-blocked  phase=4 reason=backstop\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestStepEventWithoutDetailHasNoTrailingSpace(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	f.Emit(core.Event{At: at, Kind: "step", Phase: 4, Step: "plan", Fields: map[string]string{"state": "running", "provider": "claude"}})

	if want := "14:03:09  phase 4  plan  running  claude\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestWithdrawEndsAWaitingAskAndFreesTheTerminalForTheNextQuestion(t *testing.T) {
	var out syncBuffer
	r, w := io.Pipe()
	f := &Face{Out: &out, In: r, TTY: true}
	first := make(chan error)
	go func() { _, err := f.Ask(core.Question{ID: "q1", Text: "Which port?"}); first <- err }()
	for !strings.Contains(out.String(), "answer q1> ") {
		time.Sleep(time.Millisecond)
	}

	f.Withdraw("q1")

	if err := <-first; !errors.Is(err, core.ErrNoInput) {
		t.Fatalf("withdrawn ask err %v", err)
	}
	second := make(chan string)
	go func() { a, _ := f.Ask(core.Question{ID: "q2", Text: "Which host?"}); second <- a }()
	w.Write([]byte("localhost\n"))
	if a := <-second; a != "localhost" {
		t.Fatalf("q2 answer %q", a)
	}
}

func TestAskForAnAlreadyWithdrawnQuestionReturnsAtOnce(t *testing.T) {
	var out bytes.Buffer
	r, _ := io.Pipe()
	f := &Face{Out: &out, In: r, TTY: true}
	f.Withdraw("q1")

	_, err := f.Ask(core.Question{ID: "q1", Text: "Which port?"})

	if !errors.Is(err, core.ErrNoInput) {
		t.Fatalf("err %v", err)
	}
}
