package core

import (
	"strings"
	"testing"
)

func TestReportCountsAndListsAResolveFirstAnswer(t *testing.T) {
	st := RunState{
		ID: "run-1", Status: RunFinished,
		Events: []Event{
			{Kind: "human", Step: "resolve first", Fields: map[string]string{"what": "answer", "id": "r1", "by": "maintainer", "entry": "Pick the database", "answer": "Postgres"}},
		},
		Questions: []Question{
			{ID: "q1", Step: StepKey{Phase: 1, Kind: "implement"}, Text: "keep api?", Answer: "yes", AnsweredBy: "maintainer"},
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
