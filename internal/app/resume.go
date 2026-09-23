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
	"time"

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
		return nil, core.RunOptions{}, exit(2, "usage: r-loop resume [--replan] [--unattended] [--plain]")
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
	opts, err := w.resume(run, *replan)
	if err != nil {
		return nil, core.RunOptions{}, err
	}
	return w, opts, nil
}

func (w *Wiring) resume(run core.RunState, replan bool) (core.RunOptions, error) {
	id, env := run.ID, w.Env
	if recorded := recordedRunList(run); len(recorded) > 0 {
		unticked := w.Plan.Unticked()
		w.Opts.Phases = slices.DeleteFunc(recorded, func(n string) bool { return !slices.Contains(unticked, n) })
		if len(w.Opts.Phases) == 0 {
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
	w.bind(id)
	for _, n := range halted {
		if agent, ws := previousSession(run, n); agent != "" {
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
	agent := fmt.Sprintf("rloop-p%s-%s", phase, kind)
	if a, _ := strconv.Atoi(attempt); a > 1 {
		agent += "-a" + attempt
	}
	state, err := w.Host.State(agent)
	if err != nil {
		return exit(4, "previous session %s: %v", agent, err)
	}
	if state != core.AgentWorking && state != core.AgentBlocked {
		return nil
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
	return nil
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
		if err := w.Store.Append(run.ID, core.Record{Kind: core.RecordStep, At: time.Now(), Step: &key, State: core.StepFailed, Reason: "interrupted: driver died"}); err != nil {
			return exit(2, "%v", err)
		}
	}
	return nil
}

func (w *Wiring) withdrawOpenQuestions(run core.RunState) error {
	for _, q := range run.Questions {
		if q.AnsweredBy != "" {
			continue
		}
		q.Answer, q.AnsweredBy, q.AnsweredAt = "step "+string(core.StepFailed), "withdrawn", time.Now()
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

func previousSession(run core.RunState, phase string) (string, string) {
	agent, ws := "", ""
	for _, e := range run.Events {
		if e.Kind != "step" || e.Phase != phase || e.Fields["workspace"] == "" {
			continue
		}
		agent, ws = fmt.Sprintf("rloop-p%s-%s", phase, e.Step), e.Fields["workspace"]
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
