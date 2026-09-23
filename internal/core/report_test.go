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
