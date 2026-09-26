package app

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/gitrepo"
	"r-loop/internal/plan"
	"r-loop/internal/store"
)

func Resume(args []string, env Env) int {
	w, opts, err := PrepareResume(args, env)
	if err != nil {
		return fail(env, err)
	}
	return w.Execute(opts)
}

func PrepareResume(args []string, env Env) (w *Wiring, opts core.RunOptions, err error) {
	fs := flag.NewFlagSet("r-loop resume", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	replan := fs.Bool("replan", false, "")
	plain := fs.Bool("plain", false, "")
	unattended := fs.Bool("unattended", false, "")
	yes := fs.Bool("yes", false, "")
	positional, err := parsePositional(fs, args)
	if err != nil || len(positional) > 1 {
		return nil, core.RunOptions{}, exit(2, "usage: r-loop resume [--replan] [--unattended] [--yes] [--plain] [<run-id>]")
	}
	repo, err := gitrepo.Open(env.Dir)
	if err != nil {
		return nil, core.RunOptions{}, exit(2, "%v", err)
	}
	st := store.New(repo.Root())
	named := ""
	if len(positional) == 1 {
		named = positional[0]
	}
	id, err := selectRun(st, named, env.Stderr)
	if err != nil {
		return nil, core.RunOptions{}, err
	}
	if id == "" {
		return nil, core.RunOptions{}, exit(2, "nothing to resume: no run")
	}
	lock, lerr := st.Lock()
	var live *store.LockedError
	if errors.As(lerr, &live) {
		return nil, core.RunOptions{}, exit(2, "%v", live)
	}
	if lerr != nil {
		return nil, core.RunOptions{}, exit(2, "%v", lerr)
	}
	defer func() {
		if err != nil {
			lock.Release("", 0)
		}
	}()
	run, err := st.Load(id)
	if err != nil {
		return nil, core.RunOptions{}, exit(2, "load run %s: %v", id, err)
	}
	if run.Status == core.RunFinished {
		return nil, core.RunOptions{}, exit(2, "nothing to resume: run %s finished", id)
	}
	todoAbs := run.Todo
	if !filepath.IsAbs(todoAbs) {
		todoAbs = filepath.Join(env.Dir, todoAbs)
	}
	if err := reconcileLand(repo, st, run, todoAbs, env.Stdout); err != nil {
		return nil, core.RunOptions{}, err
	}
	if run, err = st.Load(id); err != nil {
		return nil, core.RunOptions{}, exit(2, "load run %s: %v", id, err)
	}
	w, err = Wire(Options{Todo: run.Todo, Plain: *plain, Unattended: *unattended, Yes: *yes}, env)
	if err != nil {
		return nil, core.RunOptions{}, err
	}
	w.lock = lock
	opts, err = w.resume(run, *replan)
	if err != nil {
		return nil, core.RunOptions{}, err
	}
	return w, opts, nil
}

func reconcileLand(repo *gitrepo.Repo, st *store.Store, run core.RunState, todoAbs string, out io.Writer) error {
	var mi *core.Event
	miIndex := -1
	for i := range run.Events {
		if run.Events[i].Kind == core.EventMergeIntent {
			mi = &run.Events[i]
			miIndex = i
		}
	}
	if mi == nil || slices.ContainsFunc(run.Landed, func(l core.Landing) bool { return l.Phase == mi.Phase }) {
		return nil
	}
	var ci *core.Event
	for i := miIndex + 1; i < len(run.Events); i++ {
		e := &run.Events[i]
		if e.Kind == core.EventCommitIntent && e.Step == "land" && e.Phase == mi.Phase {
			ci = e
		}
	}
	root := repo.Root()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	if resolved, err := filepath.EvalSymlinks(todoAbs); err == nil {
		todoAbs = resolved
	}
	todoRel, err := filepath.Rel(root, todoAbs)
	if err != nil {
		return exit(2, "%v", err)
	}
	todoRel = filepath.ToSlash(todoRel)
	merging, err := repo.MergeInProgress()
	if err != nil {
		return exit(2, "%v", err)
	}
	landing := func(sha string) error {
		added, _ := strconv.Atoi(ci.Fields["added"])
		deleted, _ := strconv.Atoi(ci.Fields["deleted"])
		l := core.Landing{Phase: mi.Phase, MergeSHA: sha, GateSkipped: ci.Fields["gateSkipped"] == "true", Added: added, Deleted: deleted}
		if err := st.Append(run.ID, core.Record{Kind: core.RecordLanding, At: time.Now(), Landing: &l}); err != nil {
			return exit(2, "%v", err)
		}
		return nil
	}
	if merging {
		mh, err := repo.RevParse("MERGE_HEAD")
		if err != nil {
			return exit(2, "%v", err)
		}
		tip, err := repo.RevParse(mi.Fields["branch"])
		if err != nil {
			return missingPhaseBranch(mi.Phase, mi.Fields["branch"], err)
		}
		head, err := repo.HeadSHA("")
		if err != nil {
			return exit(2, "%v", err)
		}
		if mh != tip || head != mi.Fields["base"] {
			return nil
		}
		if ci != nil {
			wt, err := repo.Snapshot("")
			if err != nil {
				return unfinishedMergeError(repo, mi.Phase, err)
			}
			idx, err := repo.IndexTree(todoRel)
			if err != nil {
				return unfinishedMergeError(repo, mi.Phase, err)
			}
			if wt == ci.Fields["tree"] && idx == ci.Fields["tree"] {
				sha, err := repo.Commit(context.Background(), mi.Fields["message"], todoRel)
				if err != nil {
					return exit(4, "complete phase %s's unfinished merge: %v", mi.Phase, err)
				}
				if err := landing(sha); err != nil {
					return err
				}
				fmt.Fprintf(out, "completed phase %s's merge the crash left unfinished: landed as %s\n", mi.Phase, sha[:7])
				return nil
			}
		}
		idx, err := repo.IndexTree()
		if err != nil {
			return unfinishedMergeError(repo, mi.Phase, err)
		}
		wt, err := repo.Snapshot("")
		if err != nil {
			return unfinishedMergeError(repo, mi.Phase, err)
		}
		baseline := ""
		if ci != nil {
			baseline = ci.Fields["tree"]
		} else {
			baseline, err = repo.MergeTree(mi.Fields["base"], tip)
			if err != nil {
				return unfinishedMergeError(repo, mi.Phase, err)
			}
			ok, err := todoMatchesOwnTick(repo, run, mi.Phase, baseline, idx, todoRel, todoAbs)
			if err != nil {
				return exit(4, "phase %s's unfinished merge (MERGE_HEAD): check %s before aborting: %v", mi.Phase, todoRel, err)
			}
			if !ok {
				return exit(4, "phase %s's unfinished merge (MERGE_HEAD) has unproven changes to %s; commit them or run git merge --abort by hand, then resume", mi.Phase, todoRel)
			}
		}
		paths, err := repo.TreeDiff(idx, wt)
		if err != nil {
			return exit(2, "%v", err)
		}
		paths = slices.DeleteFunc(paths, func(p string) bool { return p == todoRel })
		if len(paths) > 0 {
			return exit(4, "phase %s's unfinished merge (MERGE_HEAD) has index and working-tree differences at %s; commit the change or run git merge --abort by hand, then resume", mi.Phase, strings.Join(paths, ", "))
		}
		staged, err := repo.TreeDiff(baseline, idx)
		if err != nil {
			return exit(2, "%v", err)
		}
		changed, err := repo.TreeDiff(baseline, wt)
		if err != nil {
			return exit(2, "%v", err)
		}
		paths = slices.Compact(slices.Sorted(slices.Values(append(staged, changed...))))
		if ci == nil {
			paths = slices.DeleteFunc(paths, func(p string) bool { return p == todoRel })
		}
		if len(paths) > 0 {
			return exit(4, "phase %s's unfinished merge (MERGE_HEAD) has changes outside its recorded state at %s; commit them or run git merge --abort by hand, then resume", mi.Phase, strings.Join(paths, ", "))
		}
		var originalTodo []byte
		if ci == nil {
			originalTodo, err = os.ReadFile(todoAbs)
			if err != nil {
				return exit(4, "phase %s's unfinished merge (MERGE_HEAD): read %s before aborting: %v", mi.Phase, todoRel, err)
			}
			baseTodo, err := repo.ReadTreeFile(baseline, todoRel)
			if err != nil {
				return exit(4, "phase %s's unfinished merge (MERGE_HEAD): read %s from the merge tree: %v", mi.Phase, todoRel, err)
			}
			stagedTodo, err := repo.ReadTreeFile(idx, todoRel)
			if err != nil {
				return exit(4, "phase %s's unfinished merge (MERGE_HEAD): read staged %s: %v", mi.Phase, todoRel, err)
			}
			if !bytes.Equal(originalTodo, baseTodo) && bytes.Equal(stagedTodo, baseTodo) {
				if err := os.WriteFile(todoAbs, baseTodo, 0o644); err != nil {
					return exit(4, "phase %s's unfinished merge (MERGE_HEAD): restore r-loop's tick in %s before aborting: %v", mi.Phase, todoRel, err)
				}
			}
		}
		if err := repo.AbortMerge(); err != nil {
			if originalTodo != nil {
				if restoreErr := os.WriteFile(todoAbs, originalTodo, 0o644); restoreErr != nil {
					err = errors.Join(err, restoreErr)
				}
			}
			return abortMergeFailure(repo, mi.Phase, todoRel, err)
		}
		dirty, err := repo.Dirty("")
		if err != nil {
			return exit(2, "%v", err)
		}
		if slices.Contains(dirty, todoRel) {
			return exit(4, "phase %s's aborted merge left %s changed (it may hold the phase's tick); check it, restore it with git checkout -- %s, then resume", mi.Phase, todoRel, todoRel)
		}
		fmt.Fprintf(out, "aborted phase %s's merge the crash left unfinished; the phase lands again\n", mi.Phase)
		return nil
	}
	if ci == nil {
		return nil
	}
	tip, err := repo.RevParse(mi.Fields["branch"])
	if err != nil {
		return missingPhaseBranch(mi.Phase, mi.Fields["branch"], err)
	}
	sha, err := repo.LandedCommit(mi.Fields["base"], tip, ci.Fields["tree"])
	if err != nil {
		return exit(2, "%v", err)
	}
	if sha == "" {
		return nil
	}
	if err := landing(sha); err != nil {
		return err
	}
	fmt.Fprintf(out, "phase %s landed as %s before the crash; its landing is now recorded\n", mi.Phase, sha[:7])
	return nil
}

func missingPhaseBranch(phase, branch string, cause error) error {
	return exit(4, "phase %s's recorded branch %s cannot be resolved: %v; restore it at its original tip or inspect the landing by hand, then resume", phase, branch, cause)
}

func abortMergeFailure(repo *gitrepo.Repo, phase, todoRel string, cause error) error {
	paths, err := repo.Dirty("")
	if err != nil {
		cause = errors.Join(cause, err)
	}
	if len(paths) == 0 {
		paths = []string{todoRel}
	}
	return exit(4, "phase %s's unfinished merge (MERGE_HEAD) could not be aborted at %s: %v; commit, stage or restore the blocking path, then finish the merge or retry git merge --abort by hand", phase, strings.Join(paths, ", "), cause)
}

func todoMatchesOwnTick(repo *gitrepo.Repo, run core.RunState, phase, baseline, index, todoRel, todoAbs string) (bool, error) {
	before, err := repo.ReadTreeFile(baseline, todoRel)
	if err != nil {
		return false, err
	}
	staged, err := repo.ReadTreeFile(index, todoRel)
	if err != nil {
		return false, err
	}
	working, err := os.ReadFile(todoAbs)
	if err != nil {
		return false, err
	}
	if bytes.Equal(staged, before) && bytes.Equal(working, before) {
		return true, nil
	}
	dir, err := os.MkdirTemp("", "r-loop-resume-tick-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, filepath.Base(todoRel))
	if err := os.WriteFile(path, before, 0o600); err != nil {
		return false, err
	}
	reader := plan.Reader{}
	p, err := reader.Read(path)
	if err != nil {
		return false, err
	}
	if groups := recordedGroups(run); len(groups) > 0 {
		p = core.GroupBacklog(p, groups)
	}
	for _, ph := range p.Phases {
		if ph.ID != phase {
			continue
		}
		if err := reader.Tick(path, ph); err != nil {
			return false, err
		}
		ticked, err := os.ReadFile(path)
		if err != nil {
			return false, err
		}
		valid := func(data []byte) bool { return bytes.Equal(data, before) || bytes.Equal(data, ticked) }
		return valid(staged) && valid(working), nil
	}
	return false, fmt.Errorf("phase %s is absent from %s", phase, todoRel)
}

func unfinishedMergeError(repo *gitrepo.Repo, phase string, cause error) error {
	paths, err := repo.Dirty("")
	if err != nil {
		return exit(4, "phase %s's unfinished merge (MERGE_HEAD): %v; run git merge --abort by hand, then resume", phase, errors.Join(cause, err))
	}
	return exit(4, "phase %s's unfinished merge (MERGE_HEAD) at %s: %v; run git merge --abort by hand, then resume", phase, strings.Join(paths, ", "), cause)
}

func (w *Wiring) resume(run core.RunState, replan bool) (core.RunOptions, error) {
	id, env := run.ID, w.Env
	if groups := recordedGroups(run); len(groups) > 0 {
		w.groups = groups
		w.setPlan(core.GroupBacklog(w.Plan, groups))
	}
	recorded := recordedRunList(run)
	w.Triaged = len(recorded) > 0
	if len(recorded) > 0 {
		unticked := w.Plan.Unticked()
		w.Opts.Phases = slices.DeleteFunc(recorded, func(n string) bool { return !slices.Contains(unticked, n) })
		if len(w.Opts.Phases) == 0 {
			if err := os.WriteFile(filepath.Join(w.Store.Dir(id), "report.md"), []byte(core.Report(run, w.Plan)), 0o644); err != nil {
				return core.RunOptions{}, exit(2, "%v", err)
			}
			return core.RunOptions{}, exit(2, "nothing to resume: run %s landed every phase", id)
		}
	}
	list, prompts, err := w.checks()
	if err != nil {
		return core.RunOptions{}, err
	}
	if err := w.Host.Reachable(); err != nil {
		return core.RunOptions{}, exit(4, "herdr server unreachable: %v", err)
	}
	if err := w.clean(); err != nil {
		return core.RunOptions{}, err
	}
	if run.Branch != "" {
		head, err := w.Repo.HeadBranch()
		if err != nil {
			return core.RunOptions{}, exit(2, "head branch: %v", err)
		}
		if head != run.Branch {
			return core.RunOptions{}, exit(4, "primary tree is on %s, but run %s started on %s; check out %s, then resume", head, id, run.Branch, run.Branch)
		}
	}
	halted := haltedPhases(run, list)
	for _, n := range halted {
		if err := w.stopStale(run, n); err != nil {
			return core.RunOptions{}, err
		}
		if err := w.claim(run, n); err != nil {
			return core.RunOptions{}, err
		}
		if err := w.closeInterrupted(run, n); err != nil {
			return core.RunOptions{}, err
		}
	}
	if err := w.withdrawOpenQuestions(run); err != nil {
		return core.RunOptions{}, err
	}
	if err := w.Store.ClearAbort(id); err != nil {
		return core.RunOptions{}, exit(2, "%v", err)
	}
	if err := w.Store.SetCurrent(id, env.PID); err != nil {
		return core.RunOptions{}, exit(2, "%v", err)
	}
	w.lock.Publish()
	w.bind(id)
	for _, n := range halted {
		if agent, ws := previousSession(run, n, w.Config.Label); agent != "" {
			fmt.Fprintf(env.Stdout, "previous session %s left in workspace %s\n", agent, ws)
		}
	}
	w.banner(env.Stdout, prompts)
	return core.RunOptions{Phases: w.Opts.Phases, Resume: true, Replan: replan}, nil
}

func recordedRunList(run core.RunState) []string {
	var phases []string
	for _, e := range run.Events {
		if e.Kind != "run-list" {
			continue
		}
		phases = nil
		for _, s := range strings.Split(e.Fields["phases"], ",") {
			if core.ValidPhaseID(s) {
				phases = append(phases, s)
			}
		}
	}
	return phases
}

func haltedPhases(run core.RunState, list []core.Phase) []string {
	var out []string
	for _, ph := range list {
		n := ph.ID
		if slices.ContainsFunc(run.Landed, func(l core.Landing) bool { return l.Phase == n }) {
			continue
		}
		for key := range run.Steps {
			if key.Phase == n {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

func (w *Wiring) stopStale(run core.RunState, phase string) error {
	kind, attempt := lastStep(run, phase)
	if kind == "" {
		return nil
	}
	var ids []string
	for _, rv := range w.Config.Steps[kind].Reviewers {
		ids = append(ids, core.Reviewer(rv).ID())
	}
	for _, agent := range previousAgents(run, phase, kind, attempt, w.Config.Label, ids) {
		state, err := w.Host.State(agent)
		if err != nil {
			return exit(4, "previous session %s: %v", agent, err)
		}
		if state != core.AgentWorking && state != core.AgentBlocked {
			continue
		}
		at := time.Now()
		ev := core.Event{At: at, Kind: "stale-interrupted", Phase: phase, Step: kind, Fields: map[string]string{"agent": agent, "state": string(state)}}
		if err := w.Store.Append(run.ID, core.Record{Kind: core.RecordEvent, At: at, Event: &ev}); err != nil {
			return exit(2, "%v", err)
		}
		if err := w.Host.Interrupt(agent); err != nil {
			return exit(4, "interrupt previous session %s: %v", agent, err)
		}
		fmt.Fprintf(w.Env.Stdout, "interrupted previous session %s: still %s\n", agent, state)
	}
	return nil
}

func legacyAgent(label, phase, kind, attempt string) string {
	name := "rloop-"
	if label != "" {
		name += label + "-"
	}
	name += "p" + phase + "-" + kind
	if a, _ := strconv.Atoi(attempt); a > 1 {
		name += "-a" + attempt
	}
	return name
}

func legacyReviewer(label, phase, kind, id, round, attempt string) string {
	prefix := legacyAgent(label, phase, kind, "1") + "-rv-" + id
	suffix := "-r" + round
	if a, _ := strconv.Atoi(attempt); a > 1 {
		suffix += "-a" + attempt
	}
	if len(prefix)+len(suffix) > 32 {
		prefix = strings.TrimRight(prefix[:32-len(suffix)], "-")
	}
	return prefix + suffix
}

func previousAgents(run core.RunState, phase, kind, attempt, label string, reviewers []string) []string {
	var agents []string
	for _, e := range run.Events {
		if e.Kind == "agent-named" && e.Phase == phase && e.Step == kind && e.Fields["attempt"] == attempt {
			agents = append(agents, e.Fields["agent"])
		}
	}
	if len(agents) > 0 {
		return agents
	}
	agents = append(agents, legacyAgent(label, phase, kind, attempt))
	round := ""
	for _, e := range run.Events {
		if e.Kind == "review-round" && e.Phase == phase && e.Step == kind && e.Fields["attempt"] == attempt {
			round = e.Fields["round"]
		}
	}
	if round != "" {
		for _, id := range reviewers {
			agents = append(agents, legacyReviewer(label, phase, kind, id, round, attempt))
		}
	}
	return agents
}

func (w *Wiring) closeInterrupted(run core.RunState, phase string) error {
	var open []core.StepKey
	for key, state := range run.Steps {
		if key.Phase == phase && state != core.StepOK && state != core.StepFailed {
			open = append(open, key)
		}
	}
	slices.SortFunc(open, func(a, b core.StepKey) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
	for _, key := range open {
		var ci *core.Event
		for i := range run.Events {
			e := &run.Events[i]
			if e.Kind == core.EventCommitIntent && e.Phase == key.Phase && e.Step == key.Kind && e.Fields["attempt"] == strconv.Itoa(key.Attempt) {
				ci = e
			}
		}
		if ci != nil {
			if err := w.recoverCommit(run.ID, key, *ci); err != nil {
				return err
			}
			continue
		}
		if err := w.Store.Append(run.ID, core.Record{Kind: core.RecordStep, At: time.Now(), Step: &key, State: core.StepFailed, Reason: "interrupted: driver died"}); err != nil {
			return exit(2, "%v", err)
		}
	}
	return nil
}

func (w *Wiring) recoverCommit(runID string, key core.StepKey, ci core.Event) error {
	dir := ci.Fields["dir"]
	now, err := w.Repo.Snapshot(dir)
	if err != nil {
		return exit(2, "%v", err)
	}
	if now != ci.Fields["tree"] {
		paths, _ := w.Repo.TreeDiff(ci.Fields["tree"], now)
		return exit(2, "phase %s %s: the worktree changed after the crash (%s); commit or discard those changes, then resume", key.Phase, key.Kind, strings.Join(paths, ", "))
	}
	head, err := w.Repo.HeadSHA(dir)
	if err != nil {
		return exit(2, "%v", err)
	}
	if head == ci.Fields["head"] {
		sha, err := w.Repo.CommitAll(dir, ci.Fields["message"])
		if err != nil {
			return exit(2, "%v", err)
		}
		fmt.Fprintf(w.Env.Stdout, "phase %s %s: committed the work the crash left uncommitted (%s)\n", key.Phase, key.Kind, sha[:7])
	} else {
		paths, err := w.Repo.TreeDiff(head, ci.Fields["tree"])
		if err != nil {
			return exit(2, "%v", err)
		}
		if len(paths) > 0 {
			return exit(2, "phase %s %s: HEAD %s does not hold the work the crash left (%s); commit or discard it, then resume", key.Phase, key.Kind, head[:7], strings.Join(paths, ", "))
		}
		fmt.Fprintf(w.Env.Stdout, "phase %s %s: its work was committed before the crash (%s)\n", key.Phase, key.Kind, head[:7])
	}
	if err := w.Store.Append(runID, core.Record{Kind: core.RecordStep, At: time.Now(), Step: &key, State: core.StepOK}); err != nil {
		return exit(2, "%v", err)
	}
	return nil
}

func (w *Wiring) withdrawOpenQuestions(run core.RunState) error {
	for _, q := range run.Questions {
		if q.AnsweredBy != "" {
			continue
		}
		q.Answer, q.AnsweredBy, q.AnsweredAt = "step "+string(core.StepFailed), "withdrawn", time.Now()
		if q.Kind == core.QuestionBlocker {
			q.Answer = "run stopped"
		}
		if err := w.Store.Append(run.ID, core.Record{Kind: core.RecordQuestion, At: q.AnsweredAt, Question: &q}); err != nil {
			return exit(2, "%v", err)
		}
	}
	return nil
}

func (w *Wiring) claim(run core.RunState, phase string) error {
	rel := fmt.Sprintf(".r-loop/wt/phase-%s", phase)
	dir := filepath.Join(w.Repo.Root(), rel)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	dirty, err := w.Repo.Dirty(dir)
	if err != nil {
		return exit(2, "%s: %v", rel, err)
	}
	if len(dirty) == 0 || leftoversOfRunningStep(run, phase) {
		return nil
	}
	changed := dirty
	if recorded := recordedTree(run, phase); recorded != "" {
		now, err := w.Repo.Snapshot(dir)
		if err != nil {
			return exit(2, "%s: %v", rel, err)
		}
		if now == recorded {
			return nil
		}
		if changed, err = w.Repo.TreeDiff(recorded, now); err != nil {
			return exit(2, "%s: %v", rel, err)
		}
	}
	return exit(2, "unclaimed changes in %s: %s; commit or discard them, then resume", rel, strings.Join(changed, ", "))
}

func leftoversOfRunningStep(run core.RunState, phase string) bool {
	kind, attempt := lastStep(run, phase)
	if kind == "" {
		return false
	}
	for key, state := range run.Steps {
		if key.Phase == phase && key.Kind == kind && strconv.Itoa(key.Attempt) == attempt && (state == core.StepOK || state == core.StepFailed) {
			return false
		}
	}
	return slices.ContainsFunc(run.Events, func(e core.Event) bool {
		return e.Kind == "baseline" && e.Phase == phase && e.Step == kind
	})
}

func lastStep(run core.RunState, phase string) (string, string) {
	kind, attempt := "", ""
	for _, e := range run.Events {
		if e.Kind == "step" && e.Phase == phase {
			kind, attempt = e.Step, e.Fields["attempt"]
		}
	}
	return kind, attempt
}

func recordedTree(run core.RunState, phase string) string {
	kind, attempt := lastStep(run, phase)
	tree := ""
	for _, e := range run.Events {
		switch {
		case e.Phase != phase:
		case e.Kind == "snapshot" && e.Fields["step"] == kind+"-a"+attempt:
			tree = e.Fields["tree"]
		case e.Kind == "review-round" && e.Step == kind && e.Fields["attempt"] == attempt:
			tree = e.Fields["tree"]
		}
	}
	return tree
}

func previousSession(run core.RunState, phase, label string) (string, string) {
	agent, ws := "", ""
	for _, e := range run.Events {
		if e.Kind != "step" || e.Phase != phase || e.Fields["workspace"] == "" {
			continue
		}
		agents := previousAgents(run, phase, e.Step, e.Fields["attempt"], label, nil)
		agent, ws = agents[0], e.Fields["workspace"]
	}
	return agent, ws
}

func Abort(args []string, env Env) int {
	if len(args) > 0 {
		fmt.Fprintln(env.Stderr, "usage: r-loop abort")
		return 2
	}
	repo, err := gitrepo.Open(env.Dir)
	if err != nil {
		return fail(env, exit(2, "%v", err))
	}
	st := store.New(repo.Root())
	id, _, ok := st.Live()
	if !ok {
		return fail(env, exit(2, "no live run to abort"))
	}
	if err := st.MarkAbort(id); err != nil {
		return fail(env, exit(2, "%v", err))
	}
	fmt.Fprintf(env.Stdout, "abort requested for run %s; the live step's session and worktree are left standing\n", id)
	return 0
}
