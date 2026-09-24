package app

import (
	"slices"
	"testing"

	"r-loop/internal/core"
)

func selectionPlan() core.Plan {
	phase := func(id string, done bool) core.Phase {
		return core.Phase{ID: id, Title: "t" + id, Items: []core.Item{{Text: "x", Done: done}}}
	}
	return core.Plan{Phases: []core.Phase{phase("1", true), phase("2", false), phase("3", false), phase("4", false), phase("5", false)}}
}

func ids(phases []core.Phase) []string {
	var out []string
	for _, ph := range phases {
		out = append(out, ph.ID)
	}
	return out
}

func TestSelectedPhasesFollowTheLaunchSelection(t *testing.T) {
	got := selectedPhases(selectionPlan(), "todo.md", Options{Phases: []string{"2", "4"}}, core.RunState{})

	if !slices.Equal(ids(got), []string{"2", "4"}) {
		t.Errorf("got %v", ids(got))
	}
}

func TestSelectedPhasesFromLeaveOutTickedAndEarlierPhases(t *testing.T) {
	got := selectedPhases(selectionPlan(), "todo.md", Options{From: "3"}, core.RunState{})

	if !slices.Equal(ids(got), []string{"3", "4", "5"}) {
		t.Errorf("got %v", ids(got))
	}
}

func TestSelectedPhasesWithoutASelectionAreTheUntickedOnes(t *testing.T) {
	got := selectedPhases(selectionPlan(), "todo.md", Options{}, core.RunState{})

	if !slices.Equal(ids(got), []string{"2", "3", "4", "5"}) {
		t.Errorf("got %v", ids(got))
	}
}

func TestSelectedItemsOfABacklog(t *testing.T) {
	pl := selectionPlan()
	pl.Backlog = true

	got := selectedPhases(pl, "issues.md", Options{Phases: []string{"3"}}, core.RunState{})

	if !slices.Equal(ids(got), []string{"3"}) {
		t.Errorf("got %v", ids(got))
	}
}

func TestSelectedPhasesOnResumeAreTheRecordedRunListWithLandedAndTriageSkipped(t *testing.T) {
	run := core.RunState{Events: []core.Event{
		{Kind: "triage-start", Fields: map[string]string{"phases": "1, 2, 3, 4"}},
		{Kind: core.TriageSkipped, Phase: "4"},
		{Kind: "run-list", Fields: map[string]string{"phases": "1,3"}},
	}}

	got := selectedPhases(selectionPlan(), "todo.md", Options{}, run)

	if !slices.Equal(ids(got), []string{"1", "3", "4"}) {
		t.Errorf("got %v", ids(got))
	}
}

func TestSelectedPhasesOnResumeBeforeTheRunListUseTheTriagedList(t *testing.T) {
	run := core.RunState{Events: []core.Event{{Kind: "triage-start", Fields: map[string]string{"phases": "2, 5"}}}}

	got := selectedPhases(selectionPlan(), "todo.md", Options{}, run)

	if !slices.Equal(ids(got), []string{"2", "5"}) {
		t.Errorf("got %v", ids(got))
	}
}
