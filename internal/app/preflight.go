package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"gopkg.in/yaml.v3"

	"r-loop/internal/config"
	"r-loop/internal/core"
	"r-loop/internal/face/tui"
	"r-loop/internal/providers"
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
			fmt.Fprintf(env.Stdout, "phase %s  %s  %s\n", ph.ID, core.Printable(ph.Title), pipeline(w.Loop.Kinds, w.Plan.Backlog))
		}
		w.criteriaWarnings(env.Stdout, list)
		w.blockingEntries(env.Stdout, list)
		kept, deferrals := core.DeferBlocked(w.Plan, list)
		_, table := core.RenderTriage(core.TriageView{Plan: w.Plan, List: kept, Checks: w.Findings, Deferrals: deferrals, Kinds: w.Loop.Kinds}, nil)
		fmt.Fprint(env.Stdout, "\n"+table)
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
	if err := w.clean(); err != nil {
		return err
	}
	if err := w.leftovers(list); err != nil {
		return err
	}
	branch, err := w.Repo.HeadBranch()
	if err != nil {
		return exit(2, "head branch: %v", err)
	}
	resolved, err := yaml.Marshal(cfg)
	if err != nil {
		return exit(2, "%v", err)
	}
	id, err := w.Store.Create(core.RunMeta{Todo: w.Todo, Branch: branch, ResolvedConfig: resolved, Started: time.Now()})
	if err != nil {
		return exit(2, "create run: %v", err)
	}
	if err := w.Store.SetCurrent(id, env.PID); err != nil {
		return exit(2, "%v", err)
	}
	w.bind(id)
	w.banner(env.Stdout, prompts)
	w.criteriaWarnings(env.Stdout, list)
	return nil
}

func amber(out io.Writer) lipgloss.Style {
	r := lipgloss.NewRenderer(out)
	if _, noColor := os.LookupEnv("NO_COLOR"); noColor {
		r.SetColorProfile(termenv.Ascii)
	}
	return r.NewStyle().Foreground(lipgloss.Color(tui.Secondary))
}

func (w *Wiring) blockingEntries(out io.Writer, list []core.Phase) {
	ids := phaseIDs(list)
	then := "the watchdog will walk it"
	if w.Opts.Unattended {
		then = "unattended: its phases are skipped"
	}
	for _, e := range w.Plan.Blocking(ids) {
		scope := "every phase"
		if !e.BlocksAll {
			var hit []string
			for _, n := range e.BlocksPhases {
				if slices.Contains(ids, n) {
					hit = append(hit, n)
				}
			}
			scope = "phase " + strings.Join(hit, ", ")
		}
		fmt.Fprintln(out, amber(out).Render(fmt.Sprintf("open ## Resolve first: %q (%s) blocks %s — %s", e.Name, e.Kind, scope, then)))
	}
}

func (w *Wiring) criteriaWarnings(out io.Writer, list []core.Phase) {
	if !w.Plan.Backlog {
		return
	}
	for _, ph := range list {
		if len(ph.Items) == 1 && ph.Items[0].Text == ph.Title {
			fmt.Fprintln(out, amber(out).Render(fmt.Sprintf("warning: phase %s has no acceptance criteria; the plan tests only its title — /r:issues-draft writes them", ph.ID)))
		}
	}
}

func (w *Wiring) commitHint(dirty []string) string {
	root, todo := w.Repo.Root(), w.Todo
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	if t, err := filepath.EvalSymlinks(todo); err == nil {
		todo = t
	}
	rel, err := filepath.Rel(root, todo)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	notes := strings.TrimSuffix(rel, ".md") + "-notes.md"
	for _, p := range dirty {
		if p != rel && p != notes {
			return ""
		}
	}
	return "commit " + strings.Join(dirty, " and ") + " first"
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
	w.Findings = core.CheckPlan(w.Plan)
	if len(w.Findings.Stops) > 0 {
		return nil, nil, exit(2, "%s", strings.Join(w.Findings.Stops, "; "))
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

func (w *Wiring) clean() error {
	merging, err := w.Repo.MergeInProgress()
	if err != nil {
		return exit(2, "%v", err)
	}
	if merging {
		return exit(4, "primary tree holds an unfinished merge (MERGE_HEAD): commit it or run git merge --abort, then retry")
	}
	if err := store.EnsureExcluded(w.Repo.Root()); err != nil {
		return exit(2, "%v", err)
	}
	dirty, err := w.Repo.Clean()
	if err != nil {
		return exit(2, "%v", err)
	}
	if len(dirty) > 0 {
		if hint := w.commitHint(dirty); hint != "" {
			return exit(4, "primary tree is not clean: %s; %s", strings.Join(dirty, ", "), hint)
		}
		return exit(4, "primary tree is not clean: %s", strings.Join(dirty, ", "))
	}
	return nil
}

func (w *Wiring) leftovers(list []core.Phase) error {
	var found []string
	var remedies []string
	for _, ph := range list {
		wt := ".r-loop/wt/phase-" + ph.ID
		if _, err := os.Lstat(filepath.Join(w.Repo.Root(), wt)); err == nil {
			found = append(found, wt)
			remedies = append(remedies, fmt.Sprintf("inspect %s, then use git worktree remove --force %s if registered, or rm -r %s if it is a plain directory", wt, wt, wt))
		} else if !errors.Is(err, os.ErrNotExist) {
			return exit(2, "%v", err)
		}
		branch := "r-loop/phase-" + ph.ID
		exists, err := w.Repo.BranchExists(branch)
		if err != nil {
			return exit(2, "%v", err)
		}
		if exists {
			registered, err := w.Repo.WorktreePath(branch)
			if err != nil {
				return exit(2, "%v", err)
			}
			if registered != "" {
				if _, err := os.Lstat(registered); errors.Is(err, os.ErrNotExist) {
					found = append(found, registered)
					remedies = append(remedies, "git worktree prune")
				} else if err != nil {
					return exit(2, "%v", err)
				} else if filepath.Clean(registered) != filepath.Join(w.Repo.Root(), wt) {
					remedies = append(remedies, "release the branch from worktree "+registered)
				}
			}
			found = append(found, branch)
			remedies = append(remedies, "git branch -D "+branch)
		}
	}
	if len(found) > 0 {
		resume := ""
		id, err := runToShow(w.Store)
		if err != nil {
			return exit(2, "%v", err)
		}
		if id != "" {
			run, err := w.Store.Load(id)
			if err != nil {
				return exit(2, "load run %s: %v", id, err)
			}
			if run.Status != core.RunFinished {
				resume = "resume run " + id + " with r-loop resume, or "
			}
		}
		return exit(4, "leftover from an earlier run: %s; %sremove the leftovers: %s", strings.Join(found, ", "), resume, strings.Join(remedies, "; "))
	}
	return nil
}

type role struct {
	field, provider string
	review          bool
}

func (w *Wiring) validateProviders() error {
	cfg := w.Config
	var roles []role
	for _, name := range w.sessionSteps() {
		row := cfg.Steps[name]
		p := "steps." + name + "."
		roles = append(roles, role{field: p + "provider", provider: row.Provider})
		if row.Fallback.Provider != "" {
			roles = append(roles, role{field: p + "fallback", provider: row.Fallback.Provider})
		}
		for _, rv := range row.Reviewers {
			roles = append(roles, role{field: p + "reviewers", provider: rv.Provider, review: native(rv)})
		}
	}
	roles = append(roles, role{field: "land.fix.provider", provider: cfg.Land.Fix.Provider})
	for _, rv := range cfg.Steps["implement"].Reviewers {
		roles = append(roles, role{field: "steps.implement.reviewers", provider: rv.Provider, review: native(rv)})
	}
	roles = append(roles, role{field: "watchdog.provider", provider: cfg.Watchdog.Provider})
	roles = append(roles, role{field: "intake.provider", provider: cfg.Intake.Provider})
	for _, r := range roles {
		if err := checkRole(w.Registry, r); err != nil {
			return err
		}
	}
	return nil
}

func checkRole(reg *providers.Registry, r role) error {
	p, err := reg.Resolve(r.provider)
	if err != nil {
		return exit(2, "%s: %v", r.field, err)
	}
	if r.review && p.Review == "" {
		return exit(2, "%s: provider %s has no review command", r.field, r.provider)
	}
	if p.Ask != "mcp" {
		return exit(2, "%s: provider %s has no MCP ask channel", r.field, r.provider)
	}
	return nil
}

func native(rv config.Reviewer) bool {
	return rv.Prompt == "" || rv.Prompt == "review"
}

func (w *Wiring) promptSources() ([]string, error) {
	vars := core.StepVars(core.StepRef{}, w.Plan, w.Todo, "")
	vars["GateCommand"], vars["GateOutput"] = "", ""
	var lines []string
	for _, name := range w.sessionSteps() {
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
	seen := map[string]bool{"review": true}
	for _, name := range w.sessionSteps() {
		for _, rv := range w.Config.Steps[name].Reviewers {
			if rv.Prompt != "" && !seen[rv.Prompt] {
				seen[rv.Prompt] = true
				_, source, err := w.Prompts.Render(rv.Prompt, vars)
				if err != nil {
					return nil, fmt.Errorf("steps.%s.reviewers: %w", name, err)
				}
				lines = append(lines, fmt.Sprintf("prompt %s: %s", rv.Prompt, source))
			}
			if rv.Requires != "" {
				state := "found"
				if _, err := os.Stat(filepath.Join(w.Repo.Root(), rv.Requires)); err != nil {
					state = "missing, reviewer skipped"
				}
				id := rv.Name
				if id == "" {
					id = rv.Provider
				}
				lines = append(lines, fmt.Sprintf("reviewer %s requires %s: %s", id, rv.Requires, state))
			}
		}
	}
	return lines, nil
}

func (w *Wiring) banner(out io.Writer, prompts []string) {
	fmt.Fprintf(out, "face: %s\n", w.faceName())
	fmt.Fprint(out, config.Banner(w.Config, w.extraSteps()...))
	for _, l := range prompts {
		fmt.Fprintln(out, l)
	}
	if !w.Opts.Unattended {
		fmt.Fprintln(out, "mode: attended")
		return
	}
	fmt.Fprintf(out, "mode: unattended  allow + %s\n", strings.Join(addedClasses(w.Config), ", "))
}

func (w *Wiring) extraSteps() []string {
	if w.Plan.Backlog {
		return []string{"gate"}
	}
	return nil
}

func (w *Wiring) sessionSteps() []string {
	return append(append(append([]string{}, w.Config.Pipeline...), "milestone"), w.extraSteps()...)
}

func pipeline(kinds []core.StepKind, backlog bool) string {
	parts := make([]string, 0, len(kinds)+1)
	for _, k := range kinds {
		part := k.Name
		if k.Row.Rounds > 0 && len(k.Row.Reviewers) > 0 {
			part += fmt.Sprintf(" (review ×%d)", k.Row.Rounds)
		}
		parts = append(parts, part)
	}
	land := "land"
	if backlog {
		land = "land (gate: item tests red at base, then item tests && discovered suite)"
	}
	return strings.Join(append(parts, land), " → ")
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
