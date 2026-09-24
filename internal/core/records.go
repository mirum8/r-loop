package core

import "sync"

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
	mu  sync.Mutex
	err error
}

func (g *RecordGuard) Append(runID string, rec Record) error {
	err := g.Store.Append(runID, rec)
	if err != nil && FatalRecord(rec) {
		g.mu.Lock()
		if g.err == nil {
			g.err = err
		}
		g.mu.Unlock()
	}
	return err
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
