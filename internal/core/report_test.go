package core

import (
	"strings"
	"testing"
)

func TestReportListsEachReviewerAndTheCommandItRan(t *testing.T) {
	st := RunState{ID: "run-1", Events: []Event{
		{Kind: "review-find", Phase: "1", Fields: map[string]string{"step": "plan", "round": "1", "reviewer": "codex", "command": "codex exec review --uncommitted -o /x", "state": "ok", "findings": "2"}},
		{Kind: "review-find", Phase: "1", Fields: map[string]string{"step": "plan", "round": "1", "reviewer": "claude", "command": "/code-review", "state": "failed", "findings": "0"}},
	}}
	want := "## Reviews\n\n- phase 1 plan r1 codex: ran `codex exec review --uncommitted -o /x` — ok, 2 findings\n- phase 1 plan r1 claude: ran `/code-review` — failed, 0 findings\n"
	if got := Report(st, threePhasePlan()); !strings.Contains(got, want) {
		t.Fatalf("report missing reviews:\n%s", got)
	}
}

func TestReportDoesNotClaimAnUnknownHistoricalReviewCommand(t *testing.T) {
	st := RunState{ID: "run-1", Events: []Event{{Kind: "review-find", Phase: "1", Fields: map[string]string{"step": "plan", "round": "1", "reviewer": "codex", "state": "ok", "findings": "0"}}}}
	got := Report(st, threePhasePlan())
	if strings.Contains(got, "ran ``") || !strings.Contains(got, "phase 1 plan r1 codex: ok, 0 findings") {
		t.Fatalf("report = %q", got)
	}
}

func TestReportMarksDroppedSignalsWithoutMarkingDeliveredSignals(t *testing.T) {
	key := StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}
	st := RunState{ID: "run-1", Signals: []Signal{
		{Seq: 64, Kind: SignalWarn, Source: SourceWatchdog, Step: key, Reason: "first"},
		{Seq: 65, Kind: SignalWarn, Source: SourceWatchdog, Step: key, Reason: "overflow"},
	}, Events: []Event{{Kind: "signal-dropped", Phase: "2", Step: "implement", Fields: map[string]string{"seq": "65", "kind": "warn", "source": "watchdog", "reason": "overflow"}}}}
	got := Report(st, threePhasePlan())
	if !strings.Contains(got, "warn from watchdog, phase 2 implement: overflow (dropped: signal queue full)") {
		t.Errorf("dropped signal not marked:\n%s", got)
	}
	if strings.Contains(got, "first (dropped") {
		t.Errorf("delivered signal marked dropped:\n%s", got)
	}
}

func TestReportNamesADroppedRejectionNotice(t *testing.T) {
	key := StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}
	st := RunState{ID: "run-1", Signals: []Signal{{Seq: 1, Kind: SignalWarn, Source: SourceWatchdog, Step: key, Reason: "late", Rejected: true, RejectReason: "step is not running"}}, Events: []Event{{Kind: "signal-dropped", Fields: map[string]string{"seq": "1"}}}}
	got := Report(st, threePhasePlan())
	if !strings.Contains(got, "(rejected: step is not running) (rejection notice dropped: signal queue full)") {
		t.Fatalf("report does not identify the dropped notice:\n%s", got)
	}
}

func TestReportCountsAndListsAResolveFirstAnswer(t *testing.T) {
	st := RunState{
		ID: "run-1", Status: RunFinished,
		Events: []Event{
			{Kind: "human", Step: "resolve first", Fields: map[string]string{"what": "answer", "id": "r1", "by": "maintainer", "entry": "Pick the database", "answer": "Postgres"}},
		},
		Questions: []Question{
			{ID: "q1", Step: StepKey{Phase: "1", Kind: "implement"}, Text: "keep api?", Answer: "yes", AnsweredBy: "maintainer"},
		},
	}

	rep := Report(st, threePhasePlan())

	for _, want := range []string{
		"human touches: 1\n",
		"## Questions\n\n" +
			"- r1 resolve first: Pick the database → Postgres (maintainer)\n" +
			"- q1 phase 1 implement: keep api? → yes (maintainer, waited 0s)\n",
	} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
}
