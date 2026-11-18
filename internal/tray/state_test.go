package tray

import (
	"testing"

	"r-loop/internal/core"
)

func TestEventsMoveTheRunState(t *testing.T) {
	cases := []struct {
		name  string
		from  State
		kind  string
		want  State
		moved bool
	}{
		{"watchdog asks", Running, "watchdog-waiting", Waiting, true},
		{"watchdog resumes", Waiting, "watchdog-resumed", Running, true},
		{"step keeps waiting", Waiting, "phase-state", Waiting, false},
		{"pause", Running, "paused", Paused, true},
		{"continue", Paused, "continued", Running, true},
		{"resume does not unpause", Paused, "watchdog-resumed", Paused, false},
		{"halt", Waiting, "halt", Halted, true},
		{"abort", Running, "aborted", Halted, true},
		{"finish", Running, "finished", Finished, true},
		{"error alone is not a halt", Running, "error", Running, false},
		{"ended stays ended", Finished, "watchdog-waiting", Finished, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, moved := next(at(c.from), core.Event{Kind: c.kind})

			if got.State != c.want || moved != c.moved {
				t.Fatalf("state %s moved %v, want %s %v", got.State, moved, c.want, c.moved)
			}
		})
	}
}

func at(state State) Snapshot {
	return Snapshot{State: state, waiting: state == Waiting, paused: state == Paused}
}

func TestStepEventsTrackThePhaseAndStep(t *testing.T) {
	s, _ := next(Snapshot{State: Running}, core.Event{Kind: "phase-start", Phase: "4"})
	s, moved := next(s, core.Event{Kind: "step", Phase: "4", Step: "implement"})

	if s.Phase != "4" || s.Step != "implement" || !moved {
		t.Fatalf("got %+v moved %v", s, moved)
	}
	s, _ = next(s, core.Event{Kind: "phase-start", Phase: "5"})
	if s.Phase != "5" || s.Step != "" {
		t.Fatalf("got %+v", s)
	}
}

func TestAPauseAndAWatchdogWaitAreTrackedApart(t *testing.T) {
	s := Snapshot{State: Running}
	s, _ = next(s, core.Event{Kind: "paused"})
	s, _ = next(s, core.Event{Kind: "watchdog-waiting"})
	if s.State != Waiting {
		t.Fatalf("paused then asked: %s", s.State)
	}
	s, _ = next(s, core.Event{Kind: "watchdog-resumed"})
	if s.State != Paused {
		t.Fatalf("answered while paused: %s", s.State)
	}

	s = Snapshot{State: Running}
	s, _ = next(s, core.Event{Kind: "watchdog-waiting"})
	s, _ = next(s, core.Event{Kind: "paused"})
	if s.State != Waiting {
		t.Fatalf("asked then paused: %s", s.State)
	}
	s, _ = next(s, core.Event{Kind: "continued"})
	if s.State != Waiting {
		t.Fatalf("continued while asking: %s", s.State)
	}
}

func TestALandStageShowsTheLandStep(t *testing.T) {
	s, moved := next(Snapshot{State: Running, Phase: "3", Step: "implement"}, core.Event{Kind: "land-stage", Phase: "3", Step: "land"})

	if s.Step != "land" || !moved {
		t.Fatalf("got %+v moved %v", s, moved)
	}
}
