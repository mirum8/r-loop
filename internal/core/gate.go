package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var ErrNoGate = errors.New("no gate")

const gateDiscovered = "gate-discovered"

type Suite interface {
	Command(ctx context.Context, phase Phase) (string, error)
}

type GateProbe struct {
	Sessions      *SessionManager
	Repo          Repo
	Kind          StepKind
	Plan          Plan
	RunID, RunDir string
	Face          Face
	Timeout       time.Duration
	command       string
}

func (p *GateProbe) Command(ctx context.Context, phase Phase) (string, error) {
	if p.command != "" {
		return p.command, nil
	}
	st, err := p.Sessions.Store.Load(p.RunID)
	if err != nil {
		return "", fmt.Errorf("%w: load run: %v", ErrNoGate, err)
	}
	for i := len(st.Events) - 1; i >= 0; i-- {
		if ev := st.Events[i]; ev.Kind == gateDiscovered && ev.Fields["command"] != "" {
			p.command = ev.Fields["command"]
			return p.command, nil
		}
	}
	command, err := p.discover(ctx, phase, st)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoGate, err)
	}
	recordEvent(p.Sessions.Store, p.Face, p.RunID, Event{Kind: gateDiscovered, Phase: phase.ID, Step: p.Kind.Name, Fields: map[string]string{"command": command}})
	p.command = command
	return command, nil
}

func (p *GateProbe) discover(ctx context.Context, phase Phase, st RunState) (string, error) {
	root := p.Repo.Root()
	reportAbs := filepath.Join(p.RunDir, "gate.md")
	report, err := filepath.Rel(root, reportAbs)
	if err != nil {
		return "", err
	}
	base, err := p.Repo.HeadBranch()
	if err != nil {
		return "", err
	}
	prior, _ := latestAttempt(st, p.RunID, phase.ID, p.Kind.Name)
	ref := StepRef{
		Key:       StepKey{Run: p.RunID, Phase: phase.ID, Kind: p.Kind.Name, Attempt: prior + 1},
		Kind:      p.Kind,
		Phase:     phase,
		InPrimary: true,
		Base:      base,
		RunDir:    p.RunDir,
	}
	ref.Vars = StepVars(ref, p.Plan, p.Plan.Path, p.RunDir)
	ref.Vars["Worktree"] = root
	ref.Vars["ReportPath"] = report
	key := ref.Key
	if err := p.Sessions.Store.Append(p.RunID, Record{Kind: RecordStep, At: time.Now(), Step: &key, State: StepQueued}); err != nil {
		return "", fmt.Errorf("record: %w", err)
	}
	rec := stepRecorder{p.Sessions.Store, p.Face}
	s, err := p.Sessions.Spawn(ctx, ref)
	var out Outcome
	if err != nil {
		out = p.Sessions.Finish(s, Outcome{State: StepFailed, Reason: err.Error(), Session: s})
	} else {
		out = p.Sessions.Finish(s, p.Sessions.Wait(ctx, s, rec))
	}
	rec.finished(ref, out)
	if err := p.Repo.ResetHard("HEAD"); err != nil {
		return "", fmt.Errorf("restore: %w", err)
	}
	if out.State != StepOK {
		return "", fmt.Errorf("gate step %s: %s", out.State, out.Reason)
	}
	data, err := os.ReadFile(reportAbs)
	if err != nil {
		return "", err
	}
	m := codeSpanRe.FindStringSubmatch(string(data))
	if m == nil || strings.TrimSpace(m[1]) == "" {
		return "", fmt.Errorf("%s names no command in backticks", report)
	}
	command := strings.TrimSpace(m[1])
	code, output, err := p.Repo.Run("", command, p.Timeout)
	if err == nil && code != 0 {
		err = fmt.Errorf("exited %d\n%s", code, output)
	}
	if err != nil {
		return "", fmt.Errorf("discovered gate %s fails on %s: %v", command, base, err)
	}
	return command, nil
}
