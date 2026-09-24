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
var printsBeforeRe = regexp.MustCompile(`(?i)\b(prints|lists)(\s+the)?\s*$`)
var printsNothingRe = regexp.MustCompile(`(?i)^\s*(prints|lists)\s+nothing\b`)

func gateCommand(doneWhen string) string {
	var spans [][]int
	for _, m := range codeSpanRe.FindAllStringSubmatchIndex(doneWhen, -1) {
		if strings.TrimSpace(doneWhen[m[2]:m[3]]) != "" {
			spans = append(spans, m)
		}
	}
	if len(spans) == 0 {
		return strings.TrimSpace(doneWhen)
	}
	type clause struct {
		cmd      string
		literals []string
		nothing  bool
	}
	var clauses []clause
	prevEnd := 0
	for i, m := range spans {
		gap := doneWhen[prevEnd:m[0]]
		value := strings.TrimSpace(doneWhen[m[2]:m[3]])
		if len(clauses) > 0 && printsBeforeRe.MatchString(gap) {
			clauses[len(clauses)-1].literals = append(clauses[len(clauses)-1].literals, value)
		} else {
			nextStart := len(doneWhen)
			if i+1 < len(spans) {
				nextStart = spans[i+1][0]
			}
			clauses = append(clauses, clause{cmd: value, nothing: printsNothingRe.MatchString(doneWhen[m[1]:nextStart])})
		}
		prevEnd = m[1]
	}
	var rendered []string
	for _, c := range clauses {
		if !c.nothing && len(c.literals) == 0 {
			rendered = append(rendered, c.cmd)
			continue
		}
		var checks []string
		if c.nothing {
			checks = append(checks, `test -z "$out"`)
		}
		if len(c.literals) > 0 {
			checks = append(checks, `test "$st" = 0`)
			for _, literal := range c.literals {
				checks = append(checks, `printf '%s\n' "$out" | grep -qF -e `+shellQuote(literal))
			}
		}
		cmd := c.cmd
		if strings.ContainsAny(cmd, "#\n") {
			cmd = "\n" + cmd + "\n"
		}
		rendered = append(rendered, `{ out=$( ( `+cmd+` ) 2>&1; echo ".$?"); st=${out##*.}; out=${out%.*}; printf '%s' "$out"; `+strings.Join(checks, " && ")+`; }`)
	}
	return strings.Join(rendered, " && ")
}

var (
	ErrGate            = errors.New("gate failed")
	ErrLanding         = errors.New("landing refused")
	ErrDirtyTree       = errors.New("primary tree is not clean")
	ErrUnfinishedMerge = errors.New("unfinished merge")
)

func cleanTree(repo Repo) error {
	dirty, err := repo.Dirty("")
	if err != nil {
		return err
	}
	if len(dirty) > 0 {
		return fmt.Errorf("%w: %s", ErrDirtyTree, strings.Join(dirty, ", "))
	}
	return nil
}

func (g *LandGate) guard() error {
	merging, err := g.Repo.MergeInProgress()
	if err != nil {
		return err
	}
	if merging {
		return fmt.Errorf("%w: the primary tree holds an unfinished merge (MERGE_HEAD); commit it or run git merge --abort, then resume", ErrUnfinishedMerge)
	}
	st, err := g.Store.Load(g.RunID)
	if err != nil {
		return fmt.Errorf("load run: %w", err)
	}
	if st.Branch != "" {
		head, err := g.Repo.HeadBranch()
		if err != nil {
			return err
		}
		if head != st.Branch {
			return fmt.Errorf("%w: the primary tree is on %s, but the run started on %s", ErrLanding, head, st.Branch)
		}
	}
	return cleanTree(g.Repo)
}

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

func (g *LandGate) attempt(ctx context.Context, phase Phase) (_ Landing, _, _ string, err error) {
	if err := recordFailed(g.Store); err != nil {
		return Landing{}, "", "", fmt.Errorf("record: %w", err)
	}
	if err := g.guard(); err != nil {
		return Landing{}, "", "", err
	}
	n := phase.ID
	itemGate := g.Suite != nil && phase.DoneWhen == ""
	var suite string
	if itemGate {
		var err error
		if suite, err = g.Suite.Command(ctx, phase); err != nil {
			return Landing{}, "", "", err
		}
		if err := g.guard(); err != nil {
			return Landing{}, "", "", err
		}
	}
	todoAbs := g.TodoPath
	if !filepath.IsAbs(todoAbs) {
		todoAbs = filepath.Join(g.Repo.Root(), todoAbs)
	}
	originalTodo, err := os.ReadFile(todoAbs)
	if err != nil {
		return Landing{}, "", "", fmt.Errorf("read todo: %w", err)
	}
	if err := recordFailed(g.Store); err != nil {
		return Landing{}, "", "", fmt.Errorf("record: %w", err)
	}
	base, err := g.Repo.HeadSHA("")
	if err != nil {
		return Landing{}, "", "", err
	}
	var rejected bool
	var mergedTodo []byte
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		ev, perr := panicked(n, "land", "land", r)
		quietly(func() { g.emit(ev) })
		err = perr
		var head string
		var herr error
		if qerr := quietly(func() { head, herr = g.Repo.HeadSHA("") }); qerr != nil {
			herr = qerr
		}
		if herr != nil {
			err = errors.Join(perr, herr)
			return
		}
		if head != base {
			if rejected {
				var rerr error
				if qerr := quietly(func() { rerr = g.Repo.ResetKeep("HEAD~1") }); qerr != nil {
					rerr = qerr
				}
				err = errors.Join(perr, rerr)
			}
			return
		}
		var restore error
		if qerr := quietly(func() {
			merging, merr := g.Repo.MergeInProgress()
			restore = merr
			if merging {
				if mergedTodo != nil {
					restore = errors.Join(restore, os.WriteFile(todoAbs, mergedTodo, 0o644))
				}
				restore = errors.Join(restore, g.Repo.AbortMerge())
			}
			restore = errors.Join(restore, os.WriteFile(todoAbs, originalTodo, 0o644))
		}); qerr != nil {
			restore = errors.Join(restore, qerr)
		}
		err = errors.Join(perr, restore)
	}()
	message := fmt.Sprintf("phase %s: %s", n, phase.Title)
	mergeAt := time.Now()
	mergeIntent := Event{At: mergeAt, Kind: EventMergeIntent, Phase: n, Step: "land", Fields: map[string]string{
		"phase": n, "branch": "r-loop/phase-" + n, "base": base, "message": message,
	}}
	if err := g.Store.Append(g.RunID, Record{Kind: RecordEvent, At: mergeAt, Event: &mergeIntent}); err != nil {
		return Landing{}, "", "", fmt.Errorf("record: %w", err)
	}
	if err := g.Repo.MergeNoFF(ctx, fmt.Sprintf("r-loop/phase-%s", n), g.todoRel()); err != nil {
		return Landing{}, "", "", err
	}
	mergedIndex, err := g.Repo.IndexTree()
	if err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("index tree: %w", err), g.Repo.AbortMerge())
	}
	merged, err := g.Repo.Snapshot("")
	if err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("snapshot: %w", err), g.Repo.AbortMerge())
	}
	baseline, err := g.Repo.TreeDiff(mergedIndex, merged)
	if err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("compare trees: %w", err), g.Repo.AbortMerge())
	}
	indexGitlinks, err := g.Repo.GitlinkPaths(mergedIndex)
	if err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("index gitlinks: %w", err), g.Repo.AbortMerge())
	}
	worktreeGitlinks, err := g.Repo.GitlinkPaths(merged)
	if err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("worktree gitlinks: %w", err), g.Repo.AbortMerge())
	}
	allowed := make(map[string]bool, len(baseline))
	for _, path := range baseline {
		if !slices.Contains(indexGitlinks, path) || !slices.Contains(worktreeGitlinks, path) {
			return Landing{}, "", "", errors.Join(fmt.Errorf("%w: staged apart from the working tree: %s", ErrDirtyTree, path), g.Repo.AbortMerge())
		}
		allowed[path] = true
	}
	landing := Landing{Phase: n}
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
		if output, err := g.redAtBase(ctx, phase, item); err != nil {
			return Landing{}, command, output, errors.Join(err, g.Repo.AbortMerge())
		}
	}
	if command == "" {
		landing.GateSkipped = true
		g.emit(Event{Kind: "gate-skipped", Phase: n, Step: "land", Fields: map[string]string{"phase": n}})
	} else {
		code, output, err := g.Repo.Run(ctx, "", command, g.GateTimeout)
		if err == nil && code != 0 {
			err = fmt.Errorf("%w: %s exited %d\n%s", ErrGate, command, code, output)
		}
		if err != nil {
			return Landing{}, command, output, errors.Join(err, g.Repo.AbortMerge())
		}
		landing.GateOutput = output
	}
	if err := ctx.Err(); err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("interrupted: %w", err), g.Repo.AbortMerge())
	}
	now, err := g.Repo.Snapshot("")
	if err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("snapshot: %w", err), g.Repo.AbortMerge())
	}
	if now != merged {
		paths, _ := g.Repo.TreeDiff(merged, now)
		return Landing{}, "", "", errors.Join(fmt.Errorf("%w: changed while the gate ran: %s", ErrDirtyTree, strings.Join(paths, ", ")), g.Repo.AbortMerge())
	}
	before, err := os.ReadFile(todoAbs)
	if err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("tick: %w", err), g.Repo.AbortMerge())
	}
	mergedTodo = before
	if err := g.Plan.Tick(g.TodoPath, phase); err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("tick: %w", err), os.WriteFile(todoAbs, before, 0o644), g.Repo.AbortMerge())
	}
	tree, err := g.Repo.IndexTree(g.todoRel())
	if err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("record: %w", err), os.WriteFile(todoAbs, before, 0o644), g.Repo.AbortMerge())
	}
	current, err := g.Repo.Snapshot("")
	if err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("record: %w", err), os.WriteFile(todoAbs, before, 0o644), g.Repo.AbortMerge())
	}
	if tree != current {
		paths, diffErr := g.Repo.TreeDiff(tree, current)
		indexChanges, indexErr := g.Repo.TreeDiff(mergedIndex, tree)
		worktreeChanges, worktreeErr := g.Repo.TreeDiff(merged, current)
		if diffErr != nil || indexErr != nil || worktreeErr != nil {
			return Landing{}, "", "", errors.Join(fmt.Errorf("compare trees: %w", errors.Join(diffErr, indexErr, worktreeErr)), os.WriteFile(todoAbs, before, 0o644), g.Repo.AbortMerge())
		}
		unexpected := make([]string, 0, len(paths))
		for _, path := range paths {
			if !allowed[path] {
				unexpected = append(unexpected, path)
			}
		}
		for _, path := range append(indexChanges, worktreeChanges...) {
			if path != g.todoRel() && !slices.Contains(unexpected, path) {
				unexpected = append(unexpected, path)
			}
		}
		if len(unexpected) > 0 {
			return Landing{}, "", "", errors.Join(fmt.Errorf("%w: staged apart from the working tree: %s", ErrDirtyTree, strings.Join(unexpected, ", ")), os.WriteFile(todoAbs, before, 0o644), g.Repo.AbortMerge())
		}
	}
	commitAt := time.Now()
	commitIntent := Event{At: commitAt, Kind: EventCommitIntent, Phase: n, Step: "land", Fields: map[string]string{
		"phase": n, "tree": tree, "gateSkipped": strconv.FormatBool(landing.GateSkipped), "added": strconv.Itoa(landing.Added), "deleted": strconv.Itoa(landing.Deleted),
	}}
	if err := g.Store.Append(g.RunID, Record{Kind: RecordEvent, At: commitAt, Event: &commitIntent}); err != nil {
		return Landing{}, "", "", errors.Join(fmt.Errorf("record: %w", err), os.WriteFile(todoAbs, before, 0o644), g.Repo.AbortMerge())
	}
	sha, err := g.Repo.Commit(ctx, message, g.todoRel())
	if err != nil {
		commitErr := fmt.Errorf("commit: %w", err)
		if abortErr := g.Repo.AbortMerge(); abortErr == nil {
			return Landing{}, "", "", errors.Join(commitErr, os.WriteFile(todoAbs, originalTodo, 0o644))
		} else {
			restoreErr := os.WriteFile(todoAbs, before, 0o644)
			if retryErr := g.Repo.AbortMerge(); retryErr == nil {
				return Landing{}, "", "", errors.Join(commitErr, restoreErr, os.WriteFile(todoAbs, originalTodo, 0o644))
			} else {
				return Landing{}, "", "", errors.Join(commitErr, abortErr, restoreErr, retryErr)
			}
		}
	}
	touched, err := g.Repo.CommitTouches(sha)
	todo := g.todoRel()
	if err != nil || !slices.Contains(touched, todo) || len(touched) < 2 {
		rejected = true
		reason := fmt.Errorf("%w: code and ticks land as one commit; %s touched %v", ErrLanding, sha, touched)
		return Landing{}, "", "", errors.Join(reason, err, g.Repo.ResetKeep("HEAD~1"))
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

func (g *LandGate) redAtBase(ctx context.Context, phase Phase, item string) (string, error) {
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
	defer g.Repo.Run(context.Background(), "", remove+"; git worktree prune", time.Minute)
	if code, out, err := g.Repo.Run(ctx, "", remove+"; "+strings.Join(setup, " && "), time.Minute); err != nil || code != 0 {
		return out, fmt.Errorf("base worktree for the red check: exit %d: %v\n%s", code, err, out)
	}
	code, out, err := g.Repo.Run(ctx, red, item, g.GateTimeout)
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
	return repoRel(g.Repo.Root(), g.TodoPath)
}

func repoRel(root, p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(filepath.Clean(p))
	}
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

func (g *LandGate) fix(ctx context.Context, phase Phase, command, output string) (out Outcome) {
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
	defer func() {
		if r := recover(); r != nil {
			ev, err := panicked(n, g.FixKind.Name, "gate-fix", r)
			quietly(func() { g.emit(ev) })
			var st RunState
			var loadErr error
			if qerr := quietly(func() { st, loadErr = g.Store.Load(g.RunID) }); qerr != nil {
				loadErr = qerr
			}
			if loadErr != nil {
				out = Outcome{State: StepFailed, Reason: err.Error() + "; load run: " + loadErr.Error()}
				return
			}
			if state := st.Steps[key]; state == StepOK || state == StepFailed {
				out = Outcome{State: state, Reason: err.Error()}
				quietly(func() { rec.finished(ref, out) })
				return
			}
			out = Outcome{State: StepFailed, Reason: err.Error()}
			var rerr error
			if qerr := quietly(func() {
				rerr = g.Store.Append(g.RunID, Record{Kind: RecordStep, At: time.Now(), Step: &key, State: StepFailed, Reason: out.Reason})
			}); qerr != nil {
				rerr = qerr
			}
			if rerr != nil {
				out.Reason += "; record: " + rerr.Error()
			}
			quietly(func() { rec.finished(ref, out) })
		}
	}()
	out = g.Runner.Run(ctx, ref, rec)
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
