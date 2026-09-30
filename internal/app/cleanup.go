package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/gitrepo"
	"r-loop/internal/herdr"
	"r-loop/internal/store"
)

type cleaner struct {
	env    Env
	repo   *gitrepo.Repo
	st     *store.Store
	runs   []core.RunState
	did    bool
	failed bool
}

func Cleanup(env Env) int {
	repo, err := gitrepo.Open(env.Dir)
	if err != nil {
		return fail(env, exit(2, "%v", err))
	}
	if err := store.EnsureExcluded(repo.Root()); err != nil {
		return fail(env, exit(2, "%v", err))
	}
	st := store.New(repo.Root())
	lock, err := st.Lock()
	var live *store.LockedError
	if errors.As(err, &live) {
		return fail(env, exit(4, "%v; abort it before cleaning up", live))
	}
	if err != nil {
		return fail(env, exit(2, "%v", err))
	}
	defer lock.Release("", 0)
	c := &cleaner{env: env, repo: repo, st: st}
	if err := c.loadRuns(); err != nil {
		return fail(env, exit(2, "%v", err))
	}
	c.closeWorkspaces()
	if err := c.removeWorktrees(); err != nil {
		return fail(env, exit(2, "%v", err))
	}
	if c.failed {
		return 1
	}
	if !c.did {
		fmt.Fprintln(env.Stdout, "nothing to clean up")
	}
	return 0
}

func (c *cleaner) report(format string, args ...any) {
	fmt.Fprintf(c.env.Stdout, format+"\n", args...)
	c.did = true
}

func (c *cleaner) fail(format string, args ...any) {
	fmt.Fprintf(c.env.Stderr, "r-loop: "+format+"\n", args...)
	c.failed = true
}

func (c *cleaner) record(runID string, ev core.Event) error {
	now := time.Now
	if c.env.Now != nil {
		now = c.env.Now
	}
	ev.At = now()
	return c.st.Append(runID, core.Record{Kind: core.RecordEvent, At: ev.At, Event: &ev})
}

func (c *cleaner) loadRuns() error {
	entries, err := os.ReadDir(filepath.Dir(c.st.Dir("x")))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && runIDRe.MatchString(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	slices.SortFunc(ids, func(a, b string) int { return strings.Compare(runOrder(a), runOrder(b)) })
	for _, id := range ids {
		run, err := c.st.Load(id)
		if err != nil {
			c.fail("skipped run %s: %v", id, err)
			continue
		}
		c.runs = append(c.runs, run)
	}
	return nil
}

func (c *cleaner) closeWorkspaces() {
	host := herdr.Client{Bin: c.env.Herdr}
	closed := map[string]bool{}
	for _, run := range c.runs {
		var phases []string
		for _, ev := range run.Events {
			if ev.Kind == "step" && ev.Fields["workspace"] != "" && !slices.Contains(phases, ev.Phase) {
				phases = append(phases, ev.Phase)
			}
		}
		for _, phase := range phases {
			for _, ws := range core.OpenWorkspaces(run, phase, core.EveryWorkspace) {
				if closed[ws.ID] {
					continue
				}
				closed[ws.ID] = true
				fields := map[string]string{"workspace": ws.ID}
				if err := c.record(run.ID, core.Event{Kind: "workspace-closing", Phase: phase, Step: ws.Kind, Fields: fields}); err != nil {
					c.fail("record run %s: %v", run.ID, err)
					continue
				}
				if err := host.Close(ws.ID); err != nil {
					c.fail("close workspace %s (run %s, phase %s): %v", ws.ID, run.ID, phase, err)
					continue
				}
				if err := c.record(run.ID, core.Event{Kind: "workspace-closed", Phase: phase, Step: ws.Kind, Fields: fields}); err != nil {
					c.fail("record run %s: %v", run.ID, err)
					continue
				}
				c.report("closed workspace %s (run %s, phase %s)", ws.ID, run.ID, phase)
			}
		}
	}
}

type phaseLeftovers struct {
	phase, worktree, branch string
}

func (c *cleaner) removeWorktrees() error {
	entries, err := os.ReadDir(filepath.Join(c.repo.Root(), ".r-loop", "wt"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	branches, err := c.repo.Branches("r-loop/phase-*")
	if err != nil {
		return err
	}
	head, _ := c.repo.HeadBranch()
	var phases []*phaseLeftovers
	of := func(phase string) *phaseLeftovers {
		for _, p := range phases {
			if p.phase == phase {
				return p
			}
		}
		p := &phaseLeftovers{phase: phase}
		phases = append(phases, p)
		return p
	}
	var loose []string
	for _, e := range entries {
		rel := ".r-loop/wt/" + e.Name()
		if phase, ok := strings.CutPrefix(e.Name(), "phase-"); ok && !strings.HasSuffix(phase, "-red") {
			of(phase).worktree = rel
		} else {
			loose = append(loose, rel)
		}
	}
	for _, b := range branches {
		if b == head {
			fmt.Fprintf(c.env.Stderr, "r-loop: kept branch %s: it is checked out in %s\n", b, c.repo.Root())
			continue
		}
		of(strings.TrimPrefix(b, "r-loop/phase-")).branch = b
	}
	for _, p := range phases {
		if id := c.owner(p.phase); id != "" {
			fields := map[string]string{}
			if p.worktree != "" {
				fields["worktree"] = p.worktree
			}
			if p.branch != "" {
				fields["branch"] = p.branch
			}
			if err := c.record(id, core.Event{Kind: "worktree-removed", Phase: p.phase, Fields: fields}); err != nil {
				c.fail("record run %s: %v", id, err)
				continue
			}
		}
		if p.worktree != "" {
			c.removeWorktree(p.worktree)
		}
		if p.branch != "" {
			if err := c.repo.DeleteBranch(p.branch, true); err != nil {
				c.fail("delete branch %s: %v", p.branch, err)
			} else {
				c.report("deleted branch %s", p.branch)
			}
		}
	}
	for _, rel := range loose {
		c.removeWorktree(rel)
	}
	return c.repo.PruneWorktrees()
}

func (c *cleaner) removeWorktree(rel string) {
	if err := c.repo.RemoveWorktree(rel); err != nil {
		if rmErr := os.RemoveAll(filepath.Join(c.repo.Root(), rel)); rmErr != nil {
			c.fail("remove worktree %s: %v", rel, err)
			return
		}
	}
	c.report("removed worktree %s", rel)
}

func (c *cleaner) owner(phase string) string {
	for _, run := range slices.Backward(c.runs) {
		for _, ev := range run.Events {
			if ev.Kind == "phase-start" && ev.Phase == phase {
				return run.ID
			}
		}
	}
	return ""
}
