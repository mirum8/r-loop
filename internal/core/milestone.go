package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	landed   map[string]bool
}

func (b *MilestoneBoundary) After(ctx context.Context, phase Phase) {
	if b.landed == nil {
		b.landed = map[string]bool{}
	}
	b.landed[phase.ID] = true
	m, ok := b.closed(phase)
	if !ok {
		return
	}
	if err := cleanTree(b.Repo); err != nil {
		recordEvent(b.Sessions.Store, b.Face, b.RunID, Event{Kind: "report-skipped", Phase: phase.ID, Step: b.Kind.Name, Fields: map[string]string{"milestone": strconv.Itoa(m.Number), "reason": err.Error()}})
		return
	}
	branch, err := b.Repo.HeadBranch()
	if err != nil {
		recordEvent(b.Sessions.Store, b.Face, b.RunID, Event{Kind: "report-skipped", Phase: phase.ID, Step: b.Kind.Name, Fields: map[string]string{"milestone": strconv.Itoa(m.Number), "reason": "head: " + err.Error()}})
		return
	}
	base, err := b.Repo.HeadSHA("")
	if err != nil {
		recordEvent(b.Sessions.Store, b.Face, b.RunID, Event{Kind: "report-skipped", Phase: phase.ID, Step: b.Kind.Name, Fields: map[string]string{"milestone": strconv.Itoa(m.Number), "reason": "head: " + err.Error()}})
		return
	}
	if reason := b.report(ctx, phase, m); reason != "" {
		if err := b.restore(branch, base); err != nil {
			reason += "; restore: " + err.Error()
		}
		recordEvent(b.Sessions.Store, b.Face, b.RunID, Event{Kind: "report-skipped", Phase: phase.ID, Step: b.Kind.Name, Fields: map[string]string{"milestone": strconv.Itoa(m.Number), "reason": reason}})
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

func (b *MilestoneBoundary) report(ctx context.Context, phase Phase, m Milestone) (reason string) {
	ref := StepRef{
		Key:       StepKey{Run: b.RunID, Phase: phase.ID, Kind: b.Kind.Name, Attempt: 1},
		Kind:      b.Kind,
		Phase:     phase,
		InPrimary: true,
		RunDir:    b.RunDir,
	}
	ref.Vars = StepVars(ref, b.Plan, b.Plan.Path, b.RunDir)
	report := fmt.Sprintf("docs/%s/reports/milestone-%d-%s.md", b.Topic, m.Number, kebab(m.Name))
	ref.Vars["ReportPath"] = report
	key := ref.Key
	if err := b.Sessions.Store.Append(b.RunID, Record{Kind: RecordStep, At: time.Now(), Step: &key, State: StepQueued}); err != nil {
		return "record: " + err.Error()
	}
	rec := stepRecorder{b.Sessions.Store, b.Face}
	var s *Session
	defer func() {
		if r := recover(); r != nil {
			ev, err := panicked(phase.ID, b.Kind.Name, "milestone report", r)
			quietly(func() { recordEvent(b.Sessions.Store, b.Face, b.RunID, ev) })
			reason = err.Error()
			if s != nil {
				quietly(func() { b.Sessions.Stop(s) })
			}
			var done bool
			if terr := quietly(func() { _, done = b.Sessions.terminal(key) }); terr != nil {
				reason += "; terminal: " + terr.Error()
			}
			if done {
				return
			}
			var rerr error
			if qerr := quietly(func() { rerr = b.Sessions.record(ref.Key, StepFailed, reason) }); qerr != nil {
				rerr = qerr
			}
			if rerr != nil && !errors.Is(rerr, errStepEnded) {
				reason += "; record: " + rerr.Error()
			}
			quietly(func() { rec.finished(ref, Outcome{State: StepFailed, Reason: reason}) })
		}
	}()
	s, err := b.Sessions.Spawn(ctx, ref)
	var out Outcome
	if err != nil {
		out = b.Sessions.Finish(s, Outcome{State: StepFailed, Reason: err.Error(), Session: s})
	} else {
		out = b.Sessions.Finish(s, b.Sessions.Wait(ctx, s, rec))
	}
	rec.finished(ref, out)
	if out.State != StepOK {
		return fmt.Sprintf("%s: %s", out.State, out.Reason)
	}
	now, err := b.Repo.Snapshot("")
	if err != nil {
		return "snapshot: " + err.Error()
	}
	changed, err := b.Repo.TreeDiff(s.StartTree, now)
	if err != nil {
		return "tree diff: " + err.Error()
	}
	var extra []string
	for _, p := range changed {
		if p != report {
			extra = append(extra, p)
		}
	}
	if len(extra) > 0 {
		return "milestone report changed " + strings.Join(extra, ", ") + " besides " + report
	}
	if err := recordFailed(b.Sessions.Store); err != nil {
		return "record: " + err.Error()
	}
	sha, err := b.Repo.Commit(ctx, fmt.Sprintf("docs(report): milestone %d", m.Number), report)
	if err != nil {
		return "commit: " + err.Error()
	}
	touched, err := b.Repo.CommitTouches(sha)
	if err != nil || len(touched) != 1 || touched[0] != report {
		reason := fmt.Sprintf("report commit %s touched %s, not only %s", sha, strings.Join(touched, ", "), report)
		if err != nil {
			reason += "; touches: " + err.Error()
		}
		return reason
	}
	return ""
}

func (b *MilestoneBoundary) restore(branch, base string) error {
	now, err := b.Repo.HeadBranch()
	if err != nil || now != branch {
		return fmt.Errorf("the primary tree left %s for %s; not resetting", branch, now)
	}
	if err := b.Repo.ResetHard(base); err != nil {
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
