package core

import (
	"maps"
	"slices"
	"sync"
)

const EventMergeIntent = "merge-intent"
const EventCommitIntent = "commit-intent"

var resumeEvents = map[string]bool{
	EventMergeIntent:  true,
	EventCommitIntent: true,
	"run-list":        true,
	"step":            true,
	"baseline":        true,
	"snapshot":        true,
	"review-round":    true,
	"agent-named":     true,
	"restart":         true,
	"item-skipped":    true,
	gateDiscovered:    true,
}

func FatalRecord(rec Record) bool {
	switch rec.Kind {
	case RecordStep, RecordRun, RecordLanding:
		return true
	case RecordEvent:
		return rec.Event != nil && resumeEvents[rec.Event.Kind]
	default:
		return false
	}
}

type RecordGuard struct {
	Store
	mu      sync.Mutex
	err     error
	runID   string
	view    *RunState
	changed chan struct{}
}

func (g *RecordGuard) Append(runID string, rec Record) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	err := g.Store.Append(runID, rec)
	if err != nil && FatalRecord(rec) {
		if g.err == nil {
			g.err = err
		}
	}
	if err == nil && g.view != nil && runID == g.runID {
		_ = g.view.Apply(rec)
		select {
		case g.changed <- struct{}{}:
		default:
		}
	}
	return err
}

func (g *RecordGuard) Follow(runID string) (<-chan struct{}, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.view != nil && runID == g.runID {
		return g.changed, nil
	}
	st, err := g.Store.Load(runID)
	if err != nil {
		g.runID = runID
		g.view = nil
		g.changed = nil
		return nil, err
	}
	g.runID = runID
	g.view = &st
	g.changed = make(chan struct{}, 1)
	return g.changed, nil
}

func (g *RecordGuard) Snapshot() (RunState, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.view == nil {
		return RunState{}, false
	}
	st := *g.view
	st.Steps = maps.Clone(st.Steps)
	st.Spans = maps.Clone(st.Spans)
	st.Landed = slices.Clone(st.Landed)
	st.Questions = slices.Clone(st.Questions)
	st.Signals = slices.Clone(st.Signals)
	st.Remedies = slices.Clone(st.Remedies)
	st.Events = slices.Clone(st.Events)
	st.Warnings = slices.Clone(st.Warnings)
	return st, true
}

func (g *RecordGuard) Landed(phase string) bool {
	g.mu.Lock()
	if g.view == nil {
		runID := g.runID
		g.mu.Unlock()
		if runID == "" {
			return false
		}
		st, err := g.Store.Load(runID)
		if err != nil {
			return false
		}
		for _, landing := range st.Landed {
			if landing.Phase == phase {
				return true
			}
		}
		return false
	}
	defer g.mu.Unlock()
	for _, landing := range g.view.Landed {
		if landing.Phase == phase {
			return true
		}
	}
	return false
}

func (g *RecordGuard) Latest(phase, kind string) (int, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.view == nil {
		return 0, false
	}
	var latest int
	for key := range g.view.Steps {
		if key.Phase == phase && key.Kind == kind && key.Attempt > latest {
			latest = key.Attempt
		}
	}
	return latest, true
}

func (g *RecordGuard) Failed() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

func recordFailed(s Store) error {
	if guard, ok := s.(*RecordGuard); ok {
		return guard.Failed()
	}
	return nil
}
