package tray

import (
	"time"

	"r-loop/internal/core"
)

type State string

const (
	Running  State = "running"
	Waiting  State = "waiting"
	Paused   State = "paused"
	Halted   State = "halted"
	Finished State = "finished"
)

type Snapshot struct {
	Repo  string    `json:"repo"`
	Run   string    `json:"run"`
	PID   int       `json:"pid"`
	State State     `json:"state"`
	Phase string    `json:"phase,omitempty"`
	Step  string    `json:"step,omitempty"`
	At    time.Time `json:"at"`

	paused, waiting bool
}

func (s Snapshot) ended() bool {
	return s.State == Halted || s.State == Finished
}

func next(s Snapshot, ev core.Event) (Snapshot, bool) {
	if s.ended() {
		return s, false
	}
	n := s
	switch ev.Kind {
	case "finished":
		n.State = Finished
	case "halt", "aborted":
		n.State = Halted
	case "watchdog-waiting":
		n.waiting = true
	case "watchdog-resumed":
		n.waiting = false
	case "paused":
		n.paused = true
	case "continued":
		n.paused = false
	case "phase-start":
		n.Phase, n.Step = ev.Phase, ""
	case "step", "land-stage":
		if ev.Phase != "" {
			n.Phase = ev.Phase
		}
		n.Step = ev.Step
	}
	if !n.ended() {
		switch {
		case n.waiting:
			n.State = Waiting
		case n.paused:
			n.State = Paused
		default:
			n.State = Running
		}
	}
	return n, n.State != s.State || n.Phase != s.Phase || n.Step != s.Step
}
