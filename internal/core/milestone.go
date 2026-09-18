package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type MilestoneBoundary struct {
	Plan     Plan
	Sessions *SessionManager
	Repo     Repo
	Kind     StepKind
	Topic    string
	RunDir   string
	RunID    string
	Face     Face
	landed   map[int]bool
}

func (b *MilestoneBoundary) After(ctx context.Context, phase Phase) {
	if b.landed == nil {
		b.landed = map[int]bool{}
	}
	b.landed[phase.Number] = true
	m, ok := b.closed(phase)
	if !ok {
		return
	}
	if reason := b.report(ctx, phase, m); reason != "" {
		if err := b.restore(); err != nil {
			reason += "; restore: " + err.Error()
		}
		recordEvent(b.Sessions.Store, b.Face, b.RunID, Event{Kind: "report-skipped", Phase: phase.Number, Step: b.Kind.Name, Fields: map[string]string{"milestone": strconv.Itoa(m.Number), "reason": reason}})
	}
}

func (b *MilestoneBoundary) closed(phase Phase) (Milestone, bool) {
	for _, m := range b.Plan.Milestones {
		if m.Number != phase.Milestone {
			continue
		}
		unticked := b.Plan.Unticked()
		for _, n := range m.Phases {
			for _, u := range unticked {
				if u == n && !b.landed[n] {
					return m, false
				}
			}
		}
		return m, true
	}
	return Milestone{}, false
}

func (b *MilestoneBoundary) report(ctx context.Context, phase Phase, m Milestone) string {
	ref := StepRef{
		Key:       StepKey{Run: b.RunID, Phase: phase.Number, Kind: b.Kind.Name, Attempt: 1},
		Kind:      b.Kind,
		Phase:     phase,
		InPrimary: true,
		RunDir:    b.RunDir,
	}
	ref.Vars = StepVars(ref, b.Plan, b.Plan.Path, b.RunDir)
	ref.Vars["ReportPath"] = fmt.Sprintf("docs/%s/reports/milestone-%d-%s.md", b.Topic, m.Number, kebab(m.Name))
	key := ref.Key
	if err := b.Sessions.Store.Append(b.RunID, Record{Kind: RecordStep, At: time.Now(), Step: &key, State: StepQueued}); err != nil {
		return "record: " + err.Error()
	}
	s, err := b.Sessions.Spawn(ctx, ref)
	var out Outcome
	if err != nil {
		out = b.Sessions.Finish(s, Outcome{State: StepFailed, Reason: err.Error(), Session: s})
	} else {
		out = b.Sessions.Finish(s, b.Sessions.Wait(ctx, s, nopObserver{}))
	}
	if out.State != StepOK {
		return fmt.Sprintf("%s: %s", out.State, out.Reason)
	}
	if _, err := b.Repo.Commit(fmt.Sprintf("docs(report): milestone %d", m.Number)); err != nil {
		return "commit: " + err.Error()
	}
	return ""
}

func (b *MilestoneBoundary) restore() error {
	if err := b.Repo.ResetHard("HEAD"); err != nil {
		return err
	}
	left, err := b.Repo.Dirty("")
	if err != nil {
		return err
	}
	for _, p := range left {
		if err := os.RemoveAll(filepath.Join(b.Repo.Root(), p)); err != nil {
			return err
		}
	}
	return nil
}
