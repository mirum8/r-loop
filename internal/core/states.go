package core

import (
	"errors"
	"fmt"
)

type StepState string

const (
	StepQueued       StepState = "queued"
	StepSpawned      StepState = "spawned"
	StepRunning      StepState = "running"
	StepOK           StepState = "ok"
	StepFailed       StepState = "failed"
	StepStalled      StepState = "stalled"
	StepWaitingInput StepState = "waiting-input"
)

type PhaseState string

const (
	PhaseUnticked    PhaseState = "unticked"
	PhasePlanned     PhaseState = "planned"
	PhaseImplemented PhaseState = "implemented"
	PhaseLanded      PhaseState = "landed"
	PhaseBlocked     PhaseState = "blocked"
)

type RunStatus string

const (
	RunCreated  RunStatus = "created"
	RunRunning  RunStatus = "running"
	RunHalted   RunStatus = "halted"
	RunFinished RunStatus = "finished"
)

var ErrIllegalTransition = errors.New("illegal step transition")

var legalTransitions = map[StepState][]StepState{
	StepQueued:       {StepSpawned},
	StepSpawned:      {StepRunning},
	StepRunning:      {StepOK, StepFailed, StepStalled, StepWaitingInput},
	StepWaitingInput: {StepRunning},
	StepStalled:      {StepRunning, StepFailed},
}

func (s StepState) Next(to StepState) (StepState, error) {
	for _, allowed := range legalTransitions[s] {
		if allowed == to {
			return to, nil
		}
	}
	return s, fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, s, to)
}
