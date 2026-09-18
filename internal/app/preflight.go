package app

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"

	"r-loop/internal/config"
	"r-loop/internal/core"
	"r-loop/internal/face/tui"
	"r-loop/internal/plan"
	"r-loop/internal/store"
)

func Preflight(w *Wiring) error {
	opts, env, cfg := w.Opts, w.Env, w.Config
	list, prompts, err := w.checks()
	if err != nil {
		return err
	}
	if opts.DryRun {
		w.banner(env.Stdout, prompts)
		fmt.Fprintln(env.Stdout, "run list:")
		for _, ph := range list {
			fmt.Fprintf(env.Stdout, "phase %d  %s  %s\n", ph.Number, ph.Title, pipeline(w.Loop.Kinds))
		}
		return nil
	}
	if err := w.Host.Reachable(); err != nil {
		return exit(4, "herdr server unreachable: %v", err)
	}
	if id, pid, ok := w.Store.Current(); ok {
		if alive(pid) {
			return exit(4, "run %s is live in pid %d; use r-loop status, resume or abort", id, pid)
		}
		if err := w.Store.ClearCurrent(); err != nil {
			return exit(2, "%v", err)
		}
		fmt.Fprintf(env.Stdout, "cleared run %s: pid %d is gone\n", id, pid)
	}
	if err := w.ready(list); err != nil {
		return err
	}
	resolved, err := yaml.Marshal(cfg)
	if err != nil {
		return exit(2, "%v", err)
	}
	id, err := w.Store.Create(core.RunMeta{Todo: w.Todo, ResolvedConfig: resolved, Started: time.Now()})
	if err != nil {
		return exit(2, "create run: %v", err)
	}
	if err := w.Store.SetCurrent(id, env.PID); err != nil {
		return exit(2, "%v", err)
	}
	if err := recordRunList(w.Store, id, list); err != nil {
		return exit(2, "%v", err)
	}
	w.bind(id)
	w.banner(env.Stdout, prompts)
	return nil
}

func recordRunList(st *store.Store, id string, list []core.Phase) error {
	numbers := make([]string, len(list))
	for i, ph := range list {
		numbers[i] = strconv.Itoa(ph.Number)
	}
	at := time.Now()
	return st.Append(id, core.Record{Kind: core.RecordEvent, At: at, Event: &core.Event{At: at, Kind: "run-list", Fields: map[string]string{"phases": strings.Join(numbers, ",")}}})
}

func (w *Wiring) checks() ([]core.Phase, []string, error) {
	opts, env := w.Opts, w.Env
	if !opts.DryRun {
		if _, err := exec.LookPath(env.Herdr); err != nil {
			return nil, nil, exit(127, "herdr binary %s not found", env.Herdr)
		}
	}
	if err := w.validateProviders(); err != nil {
		return nil, nil, err
	}
	list, err := core.RunList(w.Plan, w.Todo, core.RunOptions{From: opts.From, Phases: opts.Phases})
	if err != nil {
		return nil, nil, exit(2, "%v", err)
	}
	prompts, err := w.promptSources()
	if err != nil {
		return nil, nil, exit(2, "%v", err)
	}
	return list, prompts, nil
}

func (w *Wiring) ready(list []core.Phase) error {
	dirty, err := w.Repo.Clean()
	if err != nil {
		return exit(2, "%v", err)
	}
	if len(dirty) > 0 {
		return exit(4, "primary tree is not clean: %s", strings.Join(dirty, ", "))
	}
	numbers := make([]int, len(list))
	for i, ph := range list {
		numbers[i] = ph.Number
	}
	if blocking := w.Plan.Blocking(numbers); len(blocking) > 0 {
		if w.Face == core.Face(w.Plain) {
			return exit(4, "## Resolve first entry %q blocks this run; resolve it with /r:plan-unblock %s", blocking[0].Name, w.Opts.Todo)
		}
		if err := w.unblock(blocking); err != nil {
			return err
		}
	}
	if err := store.EnsureExcluded(w.Repo.Root()); err != nil {
		return exit(2, "%v", err)
	}
	return nil
}

func (w *Wiring) unblock(entries []core.Entry) error {
	if w.TUI != nil {
		w.TUI.Start(tui.Header{Todo: w.Opts.Todo, Started: time.Now(), Watchdog: !w.Opts.NoWatchdog}, w.Plan.Phases, nil)
		defer w.TUI.Stop()
	}
	now := time.Now
	if w.Env.Now != nil {
		now = w.Env.Now
	}
	if err := resolveFirst(plan.Reader{}, w.Repo, w.Face, w.Todo, entries, now()); err != nil {
		return err
	}
	pl, err := plan.Reader{}.Read(w.Todo)
	if err != nil {
		return exit(2, "%v", err)
	}
	w.Plan, w.Loop.Plan, w.Gate.Boundary.Plan = pl, pl, pl
	return nil
}

func resolveFirst(src core.PlanSource, repo interface{ Commit(string) (string, error) }, face core.Face, todo string, entries []core.Entry, now time.Time) error {
	for i, e := range entries {
		q := core.Question{ID: "r" + strconv.Itoa(i+1), Step: core.StepKey{Kind: "resolve first"}, Text: e.Name + "\n" + e.Body, AskedAt: now}
		answer, err := face.Ask(q)
		if err != nil {
			return exit(4, "## Resolve first entry %q was not answered: %v", e.Name, err)
		}
		if err := src.Stamp(todo, e.Name, now.Format("2006-01-02")+" — "+answer); err != nil {
			return exit(2, "%v", err)
		}
		if _, err := repo.Commit("plan: resolve " + e.Name); err != nil {
			return exit(2, "%v", err)
		}
	}
	return nil
}

type role struct {
	field, provider string
	review, ask     bool
}

func (w *Wiring) validateProviders() error {
	cfg := w.Config
	var roles []role
	for _, name := range append(append([]string{}, cfg.Pipeline...), "milestone") {
		row := cfg.Steps[name]
		p := "steps." + name + "."
		roles = append(roles, role{field: p + "provider", provider: row.Provider})
		if row.Fallback.Provider != "" {
			roles = append(roles, role{field: p + "fallback", provider: row.Fallback.Provider})
		}
		for _, rv := range row.Reviewers {
			roles = append(roles, role{field: p + "reviewers", provider: rv.Provider, review: true})
		}
	}
	roles = append(roles, role{field: "land.fix.provider", provider: cfg.Land.Fix.Provider})
	for _, rv := range cfg.Steps["implement"].Reviewers {
		roles = append(roles, role{field: "steps.implement.reviewers", provider: rv.Provider, review: true})
	}
	if !w.Opts.NoWatchdog {
		roles = append(roles, role{field: "watchdog.provider", provider: cfg.Watchdog.Provider, ask: true})
	}
	for _, r := range roles {
		p, err := w.Registry.Resolve(r.provider)
		if err != nil {
			return exit(2, "%s: %v", r.field, err)
		}
		if r.review && p.Review == "" {
			return exit(2, "%s: provider %s has no review command", r.field, r.provider)
		}
		if r.ask && p.Ask != "mcp" {
			return exit(2, "%s: provider %s has no MCP ask channel", r.field, r.provider)
		}
	}
	return nil
}

func (w *Wiring) promptSources() ([]string, error) {
	vars := core.StepVars(core.StepRef{}, w.Plan, w.Todo, "")
	vars["GateCommand"], vars["GateOutput"] = "", ""
	var lines []string
	steps := append(append([]string{}, w.Config.Pipeline...), "milestone")
	for _, name := range steps {
		_, source, err := w.Prompts.Render(w.Config.Steps[name].Prompt, vars)
		if err != nil {
			return nil, fmt.Errorf("steps.%s.prompt: %w", name, err)
		}
		lines = append(lines, fmt.Sprintf("prompt %s: %s", name, source))
	}
	for _, name := range []string{"review", "fix", "gatefix"} {
		_, source, err := w.Prompts.Render(name, vars)
		if err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("prompt %s: %s", name, source))
	}
	return lines, nil
}

func (w *Wiring) banner(out io.Writer, prompts []string) {
	fmt.Fprintf(out, "face: %s\n", w.faceName())
	fmt.Fprint(out, config.Banner(w.Config))
	for _, l := range prompts {
		fmt.Fprintln(out, l)
	}
	state := "on"
	if w.Opts.NoWatchdog {
		state = "off"
	}
	fmt.Fprintf(out, "watchdog: %s\n", state)
}

func pipeline(kinds []core.StepKind) string {
	parts := make([]string, 0, len(kinds)+1)
	for _, k := range kinds {
		part := k.Name
		if k.Row.Rounds > 0 && len(k.Row.Reviewers) > 0 {
			part += fmt.Sprintf(" (review ×%d)", k.Row.Rounds)
		}
		parts = append(parts, part)
	}
	return strings.Join(append(parts, "land"), " → ")
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
