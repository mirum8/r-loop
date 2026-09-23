package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/plan"
)

func (w *Wiring) unblock(ctx context.Context, opts core.RunOptions) ([]core.Phase, []core.Deferral, bool, error) {
	list, err := core.RunList(w.Plan, w.Todo, opts)
	if err != nil {
		return nil, nil, false, w.halt(exit(2, "%v", err))
	}
	blocking := w.Plan.Blocking(phaseIDs(list))
	if len(blocking) > 0 && !w.Opts.Unattended {
		if err := w.walk(ctx, blocking, list); err != nil {
			return nil, nil, false, w.halt(err)
		}
	}
	kept, deferrals := core.DeferBlocked(w.Plan, list)
	for _, d := range deferrals {
		w.record(core.Event{Kind: "entry-deferred", Fields: map[string]string{"entry": d.Entry, "phases": joinIDs(d.Phases)}})
	}
	if len(list) > 0 && len(kept) == 0 {
		w.record(core.Event{Kind: "finished", Fields: map[string]string{"reason": "nothing left to run: every phase is blocked by an open ## Resolve first entry"}})
		if err := w.Store.Append(w.Loop.RunID, core.Record{Kind: core.RecordRun, At: time.Now(), Run: core.RunFinished}); err != nil {
			return nil, nil, false, exit(2, "%v", err)
		}
		return nil, nil, true, nil
	}
	return kept, deferrals, false, nil
}

func (w *Wiring) walk(ctx context.Context, blocking []core.Entry, list []core.Phase) error {
	before, err := os.ReadFile(w.Todo)
	if err != nil {
		return exit(2, "%v", err)
	}
	w.record(core.Event{Kind: "unblock", Fields: map[string]string{"entries": strconv.Itoa(len(blocking))}})
	marker := filepath.Join(w.Store.Dir(w.Loop.RunID), "unblock.done")
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		return exit(2, "%v", err)
	}
	if err := w.Dog.Notify(core.UnblockText(w.Plan, blocking, list, marker), false, w.Config.Watchdog.UnblockTimeout); err != nil {
		w.record(core.Event{Kind: "warning", Fields: map[string]string{"reason": "resolve-first walk: " + err.Error()}})
	}
	if err := w.awaitWalk(ctx, marker); err != nil {
		return err
	}
	if w.Store.Aborted(w.Loop.RunID) {
		return exit(4, "stopped during the ## Resolve first walk")
	}
	after, err := os.ReadFile(w.Todo)
	if err != nil {
		return exit(2, "%v", err)
	}
	dirty, err := w.Repo.Clean()
	if err != nil {
		return exit(2, "%v", err)
	}
	todo := w.todoRel()
	if others := slices.DeleteFunc(slices.Clone(dirty), func(p string) bool { return p == todo }); len(others) > 0 {
		return exit(4, "the watchdog left changes outside the plan: %s", strings.Join(others, ", "))
	}
	if bytes.Equal(before, after) {
		return nil
	}
	if !plan.OnlyResolveFirstChanged(before, after) {
		if err := os.WriteFile(w.Todo, before, 0o644); err != nil {
			return exit(2, "%v", err)
		}
		return exit(4, "the watchdog edited %s outside ## Resolve first; the edit was undone", todo)
	}
	pl, err := plan.Reader{}.Read(w.Todo)
	if err != nil {
		return exit(2, "%v", err)
	}
	var resolved []core.Entry
	for _, e := range pl.ResolveFirst {
		if e.Ticked && slices.ContainsFunc(blocking, func(b core.Entry) bool { return b.Name == e.Name }) {
			resolved = append(resolved, e)
		}
	}
	if _, err := w.Repo.Commit(fmt.Sprintf("docs: resolve %d plan blockers", len(resolved))); err != nil {
		return exit(2, "%v", err)
	}
	for _, e := range resolved {
		w.record(core.Event{Kind: "entry-resolved", Fields: map[string]string{"entry": e.Name, "resolved": e.Resolved}})
	}
	w.setPlan(pl)
	return nil
}

func (w *Wiring) awaitWalk(ctx context.Context, marker string) error {
	deadline := time.After(w.Config.Watchdog.UnblockTimeout)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			return nil
		}
		if w.Store.Aborted(w.Loop.RunID) {
			return exit(4, "stopped during the ## Resolve first walk")
		}
		select {
		case <-tick.C:
		case <-deadline:
			w.record(core.Event{Kind: "warning", Fields: map[string]string{"reason": fmt.Sprintf("the ## Resolve first walk did not finish within %s", w.Config.Watchdog.UnblockTimeout)}})
			return nil
		case <-ctx.Done():
			return exit(4, "stopped during the ## Resolve first walk")
		}
	}
}

func (w *Wiring) setPlan(pl core.Plan) {
	w.Plan, w.Loop.Plan, w.Gate.Boundary.Plan, w.Watch.Plan, w.Probe.Plan = pl, pl, pl, pl, pl
}

func (w *Wiring) todoRel() string {
	root, todo := w.Repo.Root(), w.Todo
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	if t, err := filepath.EvalSymlinks(todo); err == nil {
		todo = t
	}
	rel, err := filepath.Rel(root, todo)
	if err != nil {
		return todo
	}
	return filepath.ToSlash(rel)
}

func (w *Wiring) record(ev core.Event) {
	ev.At = time.Now()
	if err := w.Store.Append(w.Loop.RunID, core.Record{Kind: core.RecordEvent, At: ev.At, Event: &ev}); err != nil {
		fmt.Fprintf(w.Env.Stderr, "r-loop: store: %v\n", err)
	}
	w.Face.Emit(ev)
}

func (w *Wiring) halt(err error) error {
	reason := err.Error()
	if rec := w.Store.Append(w.Loop.RunID, core.Record{Kind: core.RecordRun, At: time.Now(), Run: core.RunHalted, Reason: reason}); rec != nil {
		fmt.Fprintf(w.Env.Stderr, "r-loop: store: %v\n", rec)
	}
	return err
}

func phaseIDs(list []core.Phase) []string {
	ids := make([]string, len(list))
	for i, ph := range list {
		ids[i] = ph.ID
	}
	return ids
}

func joinIDs(ids []string) string {
	return strings.Join(ids, ", ")
}
