package core

import (
	"errors"
	"strings"
	"testing"
)

func TestLegalStepTransitions(t *testing.T) {
	legal := [][2]StepState{
		{StepQueued, StepSpawned},
		{StepSpawned, StepRunning},
		{StepRunning, StepOK},
		{StepRunning, StepFailed},
		{StepRunning, StepStalled},
		{StepRunning, StepWaitingInput},
		{StepWaitingInput, StepRunning},
		{StepStalled, StepRunning},
		{StepStalled, StepFailed},
	}
	for _, tr := range legal {
		got, err := tr[0].Next(tr[1])
		if err != nil || got != tr[1] {
			t.Errorf("%s -> %s: got %s, %v", tr[0], tr[1], got, err)
		}
	}
}

func TestIllegalStepTransitions(t *testing.T) {
	illegal := [][2]StepState{
		{StepOK, StepRunning},
		{StepFailed, StepRunning},
		{StepFailed, StepSpawned},
		{StepQueued, StepOK},
	}
	for _, tr := range illegal {
		got, err := tr[0].Next(tr[1])
		if !errors.Is(err, ErrIllegalTransition) {
			t.Errorf("%s -> %s: want ErrIllegalTransition, got %v", tr[0], tr[1], err)
			continue
		}
		if got != tr[0] {
			t.Errorf("%s -> %s: state changed to %s", tr[0], tr[1], got)
		}
		if !strings.Contains(err.Error(), string(tr[0])) || !strings.Contains(err.Error(), string(tr[1])) {
			t.Errorf("error %q does not name both states", err)
		}
	}
}

func TestEveryPairOutsideTheTableIsIllegal(t *testing.T) {
	all := []StepState{StepQueued, StepSpawned, StepRunning, StepOK, StepFailed, StepStalled, StepWaitingInput}
	legal := map[[2]StepState]bool{
		{StepQueued, StepSpawned}:       true,
		{StepSpawned, StepRunning}:      true,
		{StepRunning, StepOK}:           true,
		{StepRunning, StepFailed}:       true,
		{StepRunning, StepStalled}:      true,
		{StepRunning, StepWaitingInput}: true,
		{StepWaitingInput, StepRunning}: true,
		{StepStalled, StepRunning}:      true,
		{StepStalled, StepFailed}:       true,
	}
	for _, from := range all {
		for _, to := range all {
			_, err := from.Next(to)
			if legal[[2]StepState{from, to}] == (err != nil) {
				t.Errorf("%s -> %s: legal=%t err=%v", from, to, legal[[2]StepState{from, to}], err)
			}
		}
	}
}
