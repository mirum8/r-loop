package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var codeSpanRe = regexp.MustCompile("`([^`]+)`")

func gateCommand(doneWhen string) string {
	var spans []string
	for _, m := range codeSpanRe.FindAllStringSubmatch(doneWhen, -1) {
		if c := strings.TrimSpace(m[1]); c != "" {
			spans = append(spans, c)
		}
	}
	if len(spans) == 0 {
		return strings.TrimSpace(doneWhen)
	}
	return strings.Join(spans, " && ")
}

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
	Suite           Suite
}

func (g *LandGate) Land(ctx context.Context, phase Phase) (Landing, error) {
	landing, command, output, err := g.attempt(ctx, phase)
	for round := 1; errors.Is(err, ErrGate) && round <= g.FixRounds && g.Runner != nil; round++ {
		g.emit(Event{Kind: "gate-fix", Phase: phase.ID, Step: "land", Fields: map[string]string{"phase": phase.ID, "round": strconv.Itoa(round)}})
		out := g.fix(ctx, phase, command, output)
		if out.State != StepOK {
			return Landing{}, fmt.Errorf("%w: gate-fix round %d ended %s: %s", ErrGate, round, out.State, out.Reason)
		}
		landing, command, output, err = g.attempt(ctx, phase)
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

func (g *LandGate) attempt(ctx context.Context, phase Phase) (Landing, string, string, error) {
	n := phase.ID
	itemGate := g.Suite != nil && phase.DoneWhen == ""
	var suite string
	if itemGate {
		var err error
		if suite, err = g.Suite.Command(ctx, phase); err != nil {
			return Landing{}, "", "", err
		}
	}
	if err := g.Repo.MergeNoFF(fmt.Sprintf("r-loop/phase-%s", n)); err != nil {
		return Landing{}, "", "", err
	}
	landing := Landing{Phase: n}
	var err error
	if landing.Added, landing.Deleted, err = g.Repo.DiffStat("", "HEAD"); err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("diff size: %w", err), g.Repo.AbortMerge())
	}
	command := gateCommand(phase.DoneWhen)
	if itemGate {
		item, err := g.itemCommand(phase)
		if err != nil {
			return Landing{}, "", "", errors.Join(err, g.Repo.AbortMerge())
		}
		command = item + " && " + suite
		if output, err := g.redAtBase(phase, item); err != nil {
			return Landing{}, command, output, errors.Join(err, g.Repo.AbortMerge())
		}
	}
	if command == "" {
		landing.GateSkipped = true
		g.emit(Event{Kind: "gate-skipped", Phase: n, Step: "land", Fields: map[string]string{"phase": n}})
	} else {
		code, output, err := g.Repo.Run("", command, g.GateTimeout)
		if err == nil && code != 0 {
			err = fmt.Errorf("%w: %s exited %d\n%s", ErrGate, command, code, output)
		}
		if err != nil {
			return Landing{}, command, output, errors.Join(err, g.Repo.AbortMerge())
		}
		landing.GateOutput = output
	}
	if err := g.Plan.Tick(g.TodoPath, phase); err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("tick: %w", err), g.Repo.ResetHard("HEAD"))
	}
	sha, err := g.Repo.Commit(fmt.Sprintf("phase %s: %s", n, phase.Title))
	if err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("commit: %w", err), g.Repo.ResetHard("HEAD"))
	}
	touched, err := g.Repo.CommitTouches(sha)
	todo := g.todoRel()
	if err != nil || !slices.Contains(touched, todo) || len(touched) < 2 {
		reason := fmt.Errorf("%w: code and ticks land as one commit; %s touched %v", ErrLanding, sha, touched)
		return Landing{}, "", "", errors.Join(reason, err, g.Repo.ResetHard("HEAD~1"))
	}
	landing.MergeSHA = sha
	return landing, "", "", nil
}

func (g *LandGate) itemCommand(phase Phase) (string, error) {
	rel := phasePlanPath(phase.ID, phase.Title)
	data, err := os.ReadFile(filepath.Join(g.Repo.Root(), rel))
	if err != nil {
		return "", fmt.Errorf("%w: no plan names the item's tests: %v", ErrNoGate, err)
	}
	item, reason := PlanGate(splitLines(string(data)))
	if reason != "" {
		return "", fmt.Errorf("%w: %s: %s", ErrNoGate, rel, reason)
	}
	return item, nil
}

func (g *LandGate) redAtBase(phase Phase, item string) (string, error) {
	n := phase.ID
	changed, err := g.Repo.ChangedFiles("", "HEAD")
	if err != nil {
		return "", fmt.Errorf("changed files: %w", err)
	}
	root := g.Repo.Root()
	var tests []string
	for _, p := range changed {
		if _, err := os.Stat(filepath.Join(root, p)); err == nil && isTestPath(p) {
			tests = append(tests, p)
		}
	}
	if len(tests) == 0 {
		return "phase adds or changes no test file", fmt.Errorf("%w: phase %s adds or changes no test file", ErrGate, n)
	}
	red := fmt.Sprintf(".r-loop/wt/phase-%s-red", n)
	remove := "git worktree remove --force " + shellQuote(red) + " 2>/dev/null; rm -rf " + shellQuote(red)
	setup := []string{"git worktree add --detach " + shellQuote(red) + " HEAD >/dev/null"}
	for _, p := range tests {
		setup = append(setup, fmt.Sprintf("mkdir -p %s && cp %s %s", shellQuote(filepath.Dir(filepath.Join(red, p))), shellQuote(p), shellQuote(filepath.Join(red, p))))
	}
	defer g.Repo.Run("", remove+"; git worktree prune", time.Minute)
	if code, out, err := g.Repo.Run("", remove+"; "+strings.Join(setup, " && "), time.Minute); err != nil || code != 0 {
		return out, fmt.Errorf("base worktree for the red check: exit %d: %v\n%s", code, err, out)
	}
	code, out, err := g.Repo.Run(red, item, g.GateTimeout)
	if err != nil {
		return out, fmt.Errorf("red check: %w", err)
	}
	if code == 0 {
		msg := fmt.Sprintf("the item gate %s passes on the base code with only the phase's tests added (%s): the tests do not exercise the change\n%s", item, strings.Join(tests, ", "), out)
		return msg, fmt.Errorf("%w: %s", ErrGate, msg)
	}
	return "", nil
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
	n := phase.ID
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
		Worktree: fmt.Sprintf(".r-loop/wt/phase-%s", n),
		Branch:   fmt.Sprintf("r-loop/phase-%s", n),
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
	rec := stepRecorder{g.Store, g.Face}
	out := g.Runner.Run(ctx, ref, rec)
	rec.finished(ref, out)
	return out
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

type stepRecorder struct {
	store Store
	face  Face
}

func (r stepRecorder) Started(s *Session) { r.record(s, StepRunning, 0) }
func (r stepRecorder) Stalled(s *Session) { r.record(s, StepStalled, 0) }
func (r stepRecorder) Resumed(s *Session) { r.record(s, StepRunning, 0) }

func (r stepRecorder) Reviewing(s *Session, round int) { r.record(s, StepRunning, round) }

func (r stepRecorder) finished(ref StepRef, out Outcome) {
	ws, _ := sessionPlace(out.Session)
	key := ref.Key
	recordEvent(r.store, r.face, key.Run, Event{Kind: "step", Phase: key.Phase, Step: key.Kind, Fields: stepFields(ref, out.State, out.Reason, ws, 0)})
}

func (r stepRecorder) record(s *Session, state StepState, round int) {
	key := s.Ref.Key
	recordEvent(r.store, r.face, key.Run, Event{Kind: "step", Phase: key.Phase, Step: key.Kind, Fields: stepFields(s.Ref, state, "", s.Workspace, round)})
}
