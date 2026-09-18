package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

var (
	ErrGate    = errors.New("gate failed")
	ErrLanding = errors.New("landing refused")
)

type LandGate struct {
	Repo            Repo
	Plan            PlanSource
	Store           Store
	Face            Face
	RunID, TodoPath string
	GateTimeout     time.Duration
	Boundary        *MilestoneBoundary
	FixRounds       int
	FixKind         StepKind
	Runner          StepRunner
}

func (g *LandGate) Land(ctx context.Context, phase Phase) (Landing, error) {
	landing, output, err := g.attempt(phase)
	for round := 1; errors.Is(err, ErrGate) && round <= g.FixRounds && g.Runner != nil; round++ {
		g.emit(Event{Kind: "gate-fix", Phase: phase.Number, Step: "land", Fields: map[string]string{"phase": strconv.Itoa(phase.Number), "round": strconv.Itoa(round)}})
		out := g.fix(ctx, phase, phase.DoneWhen, output)
		if out.State != StepOK {
			return Landing{}, fmt.Errorf("%w: gate-fix round %d ended %s: %s", ErrGate, round, out.State, out.Reason)
		}
		landing, output, err = g.attempt(phase)
	}
	if err != nil {
		return Landing{}, err
	}
	if err := g.Store.Append(g.RunID, Record{Kind: RecordLanding, At: time.Now(), Landing: &landing}); err != nil {
		return Landing{}, fmt.Errorf("record landing: %w", err)
	}
	if g.Boundary != nil {
		g.Boundary.After(ctx, phase)
	}
	return landing, nil
}

func (g *LandGate) attempt(phase Phase) (Landing, string, error) {
	n := phase.Number
	if err := g.Repo.MergeNoFF(fmt.Sprintf("r-loop/phase-%d", n)); err != nil {
		return Landing{}, "", err
	}
	landing := Landing{Phase: n}
	var err error
	if landing.Added, landing.Deleted, err = g.Repo.DiffStat("", "HEAD"); err != nil {
		return Landing{}, "", errors.Join(fmt.Errorf("diff size: %w", err), g.Repo.AbortMerge())
	}
	if strings.TrimSpace(phase.DoneWhen) == "" {
		landing.GateSkipped = true
		g.emit(Event{Kind: "gate-skipped", Phase: n, Step: "land", Fields: map[string]string{"phase": strconv.Itoa(n)}})
	} else {
		code, output, err := g.Repo.Run("", phase.DoneWhen, g.GateTimeout)
		if err == nil && code != 0 {
			err = fmt.Errorf("%w: %s exited %d\n%s", ErrGate, phase.DoneWhen, code, output)
		}
		if err != nil {
			return Landing{}, output, errors.Join(err, g.Repo.AbortMerge())
		}
		landing.GateOutput = output
	}
	if err := g.Plan.Tick(g.TodoPath, n); err != nil {
		return Landing{}, "", errors.Join(fmt.Errorf("tick: %w", err), g.Repo.ResetHard("HEAD"))
	}
	sha, err := g.Repo.Commit(fmt.Sprintf("phase %d: %s", n, phase.Title))
	if err != nil {
		return Landing{}, "", errors.Join(fmt.Errorf("commit: %w", err), g.Repo.ResetHard("HEAD"))
	}
	touched, err := g.Repo.CommitTouches(sha)
	todo := g.todoRel()
	if err != nil || !slices.Contains(touched, todo) || len(touched) < 2 {
		reason := fmt.Errorf("%w: code and ticks land as one commit; %s touched %v", ErrLanding, sha, touched)
		return Landing{}, "", errors.Join(reason, err, g.Repo.ResetHard("HEAD~1"))
	}
	landing.MergeSHA = sha
	return landing, "", nil
}

func (g *LandGate) todoRel() string {
	p := g.TodoPath
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(filepath.Clean(p))
	}
	root := g.Repo.Root()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

func (g *LandGate) fix(ctx context.Context, phase Phase, command, output string) Outcome {
	n := phase.Number
	base, err := g.Repo.HeadBranch()
	if err != nil {
		return Outcome{State: StepFailed, Reason: "head branch: " + err.Error()}
	}
	st, err := g.Store.Load(g.RunID)
	if err != nil {
		return Outcome{State: StepFailed, Reason: "load run: " + err.Error()}
	}
	prior, _ := latestAttempt(st, g.RunID, n, g.FixKind.Name)
	ref := StepRef{
		Key:      StepKey{Run: g.RunID, Phase: n, Kind: g.FixKind.Name, Attempt: prior + 1},
		Kind:     g.FixKind,
		Phase:    phase,
		Worktree: fmt.Sprintf(".r-loop/wt/phase-%d", n),
		Branch:   fmt.Sprintf("r-loop/phase-%d", n),
		Base:     base,
		RunDir:   g.Store.Dir(g.RunID),
	}
	ref.Vars = StepVars(ref, Plan{}, g.TodoPath, ref.RunDir)
	ref.Vars["GateCommand"] = command
	ref.Vars["GateOutput"] = output
	key := ref.Key
	if err := g.Store.Append(g.RunID, Record{Kind: RecordStep, At: time.Now(), Step: &key, State: StepQueued}); err != nil {
		return Outcome{State: StepFailed, Reason: "record: " + err.Error()}
	}
	return g.Runner.Run(ctx, ref, nopObserver{})
}

func (g *LandGate) emit(ev Event) {
	recordEvent(g.Store, g.Face, g.RunID, ev)
}

func recordEvent(store Store, face Face, runID string, ev Event) {
	ev.At = time.Now()
	if err := store.Append(runID, Record{Kind: RecordEvent, At: ev.At, Event: &ev}); err != nil && face != nil {
		face.Emit(Event{At: ev.At, Kind: "warning", Fields: map[string]string{"reason": "store: " + err.Error()}})
	}
	if face != nil {
		face.Emit(ev)
	}
}
