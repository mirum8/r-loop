package plain

import (
	"bytes"
	"testing"
	"time"

	"r-loop/internal/core"
)

var at = time.Date(2026, 9, 18, 14, 3, 9, 0, time.Local)

func TestReviewFindLineNamesTheCommandTheReviewerRan(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}
	f.Emit(core.Event{At: at, Kind: "review-find", Phase: "4", Step: "implement", Fields: map[string]string{"reviewer": "codex", "round": "2", "state": "failed", "findings": "0", "command": "codex exec review --uncommitted -o /x/native-review.txt"}})
	want := "14:03:09  phase 4  implement  reviewer codex r2  failed  0 findings  ran `codex exec review --uncommitted -o /x/native-review.txt`\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestReviewFindLineDoesNotClaimAnUnknownHistoricalCommand(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}
	f.Emit(core.Event{At: at, Kind: "review-find", Phase: "4", Step: "implement", Fields: map[string]string{"reviewer": "codex", "round": "2", "state": "ok", "findings": "0"}})
	want := "14:03:09  phase 4  implement  reviewer codex r2  ok  0 findings\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

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

func TestTriagePrintsTheSummaryThenTheWholeTable(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}
	table := "| Phase | Title |\n|---|---|\n| 3 | three |\n"

	f.Emit(core.Event{At: at, Kind: "triage-start", Fields: map[string]string{"kind": "plan", "phases": "3, 9"}})
	f.Emit(core.Event{At: at, Kind: "triage", Fields: map[string]string{"summary": "1 phase to run", "table": table}})
	f.Emit(core.Event{At: at, Kind: core.TriageSkipped, Phase: "9", Fields: map[string]string{"reason": "already done: a.go:3"}})

	want := "14:03:09  triage: watchdog verifying 2 phases\n14:03:09  triage: 1 phase to run\n" + table + "14:03:09  phase 9  skipped by triage: already done: a.go:3\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestTheRunListNamesAGroupsItems(t *testing.T) {
	for _, tc := range []struct {
		name, groups, want string
	}{
		{"groups", `[{"group_id":"g1","items":["7","3","5"],"subsystem":"x"},{"group_id":"g2","items":["9"],"subsystem":"y"}]`, "run list: 3 (items 3, 5, 7), 9\n"},
		{"ids", "", "run list: 3, 9\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			f := &Face{Out: &out}

			f.Emit(core.Event{At: at, Kind: "run-list", Fields: map[string]string{"phases": "3,9", "groups": tc.groups}})

			if out.String() != tc.want {
				t.Fatalf("got %q, want %q", out.String(), tc.want)
			}
		})
	}
}

func TestTriageStartCountsOneItemInTheSingular(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	f.Emit(core.Event{At: at, Kind: "triage-start", Fields: map[string]string{"kind": "plan", "phases": "3"}})
	f.Emit(core.Event{At: at, Kind: "triage-start", Fields: map[string]string{"kind": "backlog", "phases": "4"}})

	if want := "14:03:09  triage: watchdog verifying 1 phase\n14:03:09  triage: watchdog verifying 1 item\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}

func TestReviewRoundLines(t *testing.T) {
	var out bytes.Buffer
	f := &Face{Out: &out}

	for _, ev := range []core.Event{
		{Kind: "review-round", Fields: map[string]string{"round": "1", "tree": "abc", "attempt": "1"}},
		{Kind: "agent-named", Fields: map[string]string{"round": "1", "reviewer": "codex", "agent": "p4-impl-rv-codex-r1", "attempt": "1"}},
		{Kind: "finding", Fields: map[string]string{"round": "1", "reviewer": "codex", "id": "codex-r1-1", "title": "nil map", "verdict": "real", "severity": "P1", "fixed": "true", "evidence": "a.go:3"}},
		{Kind: "review-clean", Fields: map[string]string{"round": "2"}},
	} {
		ev.At, ev.Phase, ev.Step = at, "4", "implement"
		f.Emit(ev)
	}

	want := "14:03:09  phase 4  implement  review r1\n" +
		"14:03:09  phase 4  implement  reviewer codex r1  agent p4-impl-rv-codex-r1\n" +
		"14:03:09  phase 4  implement  r1 codex codex-r1-1  real P1 fixed=true: nil map\n" +
		"14:03:09  phase 4  implement  review r2 clean\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
}
