package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"r-loop/internal/core"
	"r-loop/internal/gitrepo"
	"r-loop/internal/store"
)

func Resume(args []string, env Env) int {
	w, opts, err := PrepareResume(args, env)
	if err != nil {
		return fail(env, err)
	}
	return w.Execute(opts)
}

func PrepareResume(args []string, env Env) (*Wiring, core.RunOptions, error) {
	fs := flag.NewFlagSet("r-loop resume", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	replan := fs.Bool("replan", false, "")
	plain := fs.Bool("plain", false, "")
	unattended := fs.Bool("unattended", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return nil, core.RunOptions{}, exit(2, "usage: r-loop resume [--replan] [--unattended]")
	}
	repo, err := gitrepo.Open(env.Dir)
	if err != nil {
		return nil, core.RunOptions{}, exit(2, "%v", err)
	}
	st := store.New(repo.Root())
	id, err := runToShow(st)
	if err != nil {
		return nil, core.RunOptions{}, exit(2, "%v", err)
	}
	if id == "" {
		return nil, core.RunOptions{}, exit(2, "nothing to resume: no run")
	}
	if cur, pid, ok := st.Current(); ok && cur == id && alive(pid) {
		return nil, core.RunOptions{}, exit(2, "run %s is live in pid %d", id, pid)
	}
	run, err := st.Load(id)
	if err != nil {
		return nil, core.RunOptions{}, exit(2, "load run %s: %v", id, err)
	}
	if run.Status == core.RunFinished {
		return nil, core.RunOptions{}, exit(2, "nothing to resume: run %s finished", id)
	}
	w, err := Wire(Options{Todo: run.Todo, Plain: *plain, Unattended: *unattended}, env)
	if err != nil {
		return nil, core.RunOptions{}, err
	}
	if recorded := recordedRunList(run); len(recorded) > 0 {
		unticked := w.Plan.Unticked()
		w.Opts.Phases = slices.DeleteFunc(recorded, func(n int) bool { return !slices.Contains(unticked, n) })
		if len(w.Opts.Phases) == 0 {
			return nil, core.RunOptions{}, exit(2, "nothing to resume: run %s landed every phase", id)
		}
	}
	list, prompts, err := w.checks()
	if err != nil {
		return nil, core.RunOptions{}, err
	}
	if err := w.Host.Reachable(); err != nil {
		return nil, core.RunOptions{}, exit(4, "herdr server unreachable: %v", err)
	}
	if err := w.ready(list); err != nil {
		return nil, core.RunOptions{}, err
	}
	halted := haltedPhases(run, list)
	for _, n := range halted {
		if err := w.claim(run, n); err != nil {
			return nil, core.RunOptions{}, err
		}
	}
	if err := w.Store.ClearAbort(id); err != nil {
		return nil, core.RunOptions{}, exit(2, "%v", err)
	}
	if err := w.Store.SetCurrent(id, env.PID); err != nil {
		return nil, core.RunOptions{}, exit(2, "%v", err)
	}
	w.bind(id)
	for _, n := range halted {
		if agent, ws := previousSession(run, n); agent != "" {
			fmt.Fprintf(env.Stdout, "previous session %s left in workspace %s\n", agent, ws)
		}
	}
	w.banner(env.Stdout, prompts)
	return w, core.RunOptions{Phases: w.Opts.Phases, Resume: true, Replan: *replan}, nil
}

func recordedRunList(run core.RunState) []int {
	var phases []int
	for _, e := range run.Events {
		if e.Kind != "run-list" {
			continue
		}
		for _, s := range strings.Split(e.Fields["phases"], ",") {
			if n, err := strconv.Atoi(s); err == nil {
				phases = append(phases, n)
			}
		}
	}
	return phases
}

func haltedPhases(run core.RunState, list []core.Phase) []int {
	var out []int
	for _, ph := range list {
		n := ph.Number
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

func (w *Wiring) claim(run core.RunState, phase int) error {
	rel := fmt.Sprintf(".r-loop/wt/phase-%d", phase)
	dir := filepath.Join(w.Repo.Root(), rel)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	dirty, err := w.Repo.Dirty(dir)
	if err != nil {
		return exit(2, "%s: %v", rel, err)
	}
	if len(dirty) == 0 {
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

func recordedTree(run core.RunState, phase int) string {
	kind, attempt := "", ""
	for _, e := range run.Events {
		if e.Kind == "step" && e.Phase == phase {
			kind, attempt = e.Step, e.Fields["attempt"]
		}
	}
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

func previousSession(run core.RunState, phase int) (string, string) {
	agent, ws := "", ""
	for _, e := range run.Events {
		if e.Kind != "step" || e.Phase != phase || e.Fields["workspace"] == "" {
			continue
		}
		agent, ws = fmt.Sprintf("rloop-p%d-%s", phase, e.Step), e.Fields["workspace"]
		if a, _ := strconv.Atoi(e.Fields["attempt"]); a > 1 {
			agent += "-a" + strconv.Itoa(a)
		}
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
	id, pid, ok := st.Current()
	if !ok || !alive(pid) {
		return fail(env, exit(2, "no live run to abort"))
	}
	if err := st.MarkAbort(id); err != nil {
		return fail(env, exit(2, "%v", err))
	}
	fmt.Fprintf(env.Stdout, "abort requested for run %s; the live step's session and worktree are left standing\n", id)
	return 0
}
