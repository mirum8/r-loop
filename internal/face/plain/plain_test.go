package plain

import (
	"bytes"
	"testing"
	"time"

	"r-loop/internal/core"
)

var at = time.Date(2026, 9, 18, 14, 3, 9, 0, time.Local)

func TestStepEventLine(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	f.Emit(core.Event{At: at, Kind: "step", Phase: "4", Step: "implement", Fields: map[string]string{
		"state": "failed", "provider": "codex", "reason": "evidence missing: diff",
	}})

	want := "14:03:09  phase 4  implement  failed  codex  evidence missing: diff\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestStepEventLineDuringAReviewNamesTheRoundAndHalf(t *testing.T) {
	for half, want := range map[string]string{
		"find": "14:03:09  phase 4  implement  running  codex  review r2/3 find\n",
		"fix":  "14:03:09  phase 4  implement  running  codex  review r2/3 fix\n",
	} {
		var out bytes.Buffer
		f := &Face{Out: &out}

		f.Emit(core.Event{At: at, Kind: "step", Phase: "4", Step: "implement", Fields: map[string]string{
			"state": "running", "provider": "codex", "round": "2", "rounds": "3", "half": half,
		}})

		if out.String() != want {
			t.Errorf("got %q, want %q", out.String(), want)
		}
	}
}

func TestWarningLine(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	f.Emit(core.Event{At: at, Kind: "warning", Phase: "4", Step: "implement", Fields: map[string]string{"reason": "diff is large"}})

	if want := "!  phase 4 implement: diff is large\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestAWatchdogAskingTheMaintainerIsFlagged(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	f.Emit(core.Event{At: at, Kind: "watchdog-waiting"})

	if want := "!  watchdog waiting for you\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestAWatchdogAskingTheMaintainerShowsTheQuestion(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	f.Emit(core.Event{At: at, Kind: "watchdog-waiting", Fields: map[string]string{"question": "Retry phase 2 with the helper renamed?", "options": "yes; no"}})

	if want := "!  watchdog waiting for you: Retry phase 2 with the helper renamed?\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestNudgeLineNamesTheStep(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	f.Emit(core.Event{At: at, Kind: "nudge", Phase: "4", Step: "implement"})

	if want := "14:03:09  phase 4  implement  nudge\n"; out.String() != want {
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

func TestOtherEventsAreOneLine(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	f.Emit(core.Event{At: at, Kind: "phase-blocked", Phase: "4", Step: "implement", Fields: map[string]string{"phase": "4", "reason": "backstop"}})

	if want := "14:03:09  phase 4  phase-blocked  phase=4 reason=backstop\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestThePhaseCheckIsOneLineWhenItStartsAndOneWithItsResult(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   string
		fields map[string]string
		want   string
	}{
		{"start", "phase-check-start", nil, "14:03:09  phase 4  phase check  watchdog checking the plan\n"},
		{"clear", "phase-check", map[string]string{"result": "no disagreement"}, "14:03:09  phase 4  phase check  found no disagreement\n"},
		{"warned", "phase-check", map[string]string{"result": "Risk: none is too low"}, "14:03:09  phase 4  phase check  warned\n"},
		{"timeout", "phase-check-timeout", map[string]string{"reason": "herdr: timeout"}, "14:03:09  phase 4  phase check  timed out: herdr: timeout\n"},
		{"skip reason", "phase-check-skipped", map[string]string{"reason": "watchdog unreachable"}, "14:03:09  phase 4  phase check  skipped: watchdog unreachable\n"},
		{"skip", "phase-check-skipped", nil, "14:03:09  phase 4  phase check  skipped\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			f := &Face{Out: &out}
			f.Emit(core.Event{At: at, Kind: tc.kind, Phase: "4", Fields: tc.fields})
			if got := out.String(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStepEventWithoutDetailHasNoTrailingSpace(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	f.Emit(core.Event{At: at, Kind: "step", Phase: "4", Step: "plan", Fields: map[string]string{"state": "running", "provider": "claude"}})

	if want := "14:03:09  phase 4  plan  running  claude\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}
