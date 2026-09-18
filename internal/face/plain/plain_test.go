package plain

import (
	"bytes"
	"errors"
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

func TestAskPrintsTheQuestionAndHasNoInput(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	answer, err := f.Ask(core.Question{ID: "q3", Step: core.StepKey{Phase: 4, Kind: "implement"}, Text: "Which port?", Options: []string{"8080", "9090"}})

	if !errors.Is(err, core.ErrNoInput) || answer != "" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	want := "?  q3  phase 4 implement: Which port?\n   1. 8080\n   2. 9090\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
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
