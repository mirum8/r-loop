package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"r-loop/internal/core"
)

type triageRun struct {
	list      []core.Phase
	deferrals []core.Deferral
	ask       bool
	current   *core.Triage
	closed    bool
	end       chan triageEnd
}

type triageEnd struct {
	decision string
	by       string
	said     string
	triage   core.Triage
}

type gateRecord struct {
	Decision       string      `json:"decision"`
	By             string      `json:"by"`
	MaintainerSaid string      `json:"maintainer_said,omitempty"`
	Triage         core.Triage `json:"triage"`
}

func (w *Wiring) triage(ctx context.Context, opts core.RunOptions, kept []core.Phase, deferrals []core.Deferral) (core.RunOptions, bool, error) {
	if w.Triaged || len(kept) == 0 {
		w.recordRunList(kept, w.groups)
		opts.From, opts.Phases = "", phaseIDs(kept)
		return opts, false, nil
	}
	run := &triageRun{list: kept, deferrals: deferrals, ask: !w.Opts.Unattended && !w.Opts.Yes, end: make(chan triageEnd, 1)}
	kind := "plan"
	if w.Plan.Backlog {
		kind = "backlog"
	}
	w.record(core.Event{Kind: "triage-start", Fields: map[string]string{"kind": kind, "phases": joinIDs(phaseIDs(kept))}})
	w.triageMu.Lock()
	w.triaging = run
	w.triageMu.Unlock()
	defer func() {
		w.triageMu.Lock()
		w.triaging = nil
		w.triageMu.Unlock()
	}()
	if err := w.Dog.Notify(core.TriageText(w.Plan, kept, run.ask), false, w.Config.Watchdog.TriageTimeout); err != nil {
		w.record(core.Event{Kind: "warning", Fields: map[string]string{"reason": "triage: " + err.Error()}})
	}
	end, err := w.awaitTriage(ctx, run)
	if err != nil {
		return opts, false, err
	}
	if end.decision == core.GateAbort {
		return opts, false, w.abortTriage("the maintainer aborted at the triage gate")
	}
	return w.startAfterTriage(opts, run, end)
}

func (w *Wiring) awaitTriage(ctx context.Context, run *triageRun) (triageEnd, error) {
	timeout := w.Config.Watchdog.TriageTimeout
	deadline := time.After(timeout)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case end := <-run.end:
			return end, nil
		default:
		}
		if w.Store.Aborted(w.Loop.RunID) {
			return triageEnd{}, w.abortTriage("stopped during triage")
		}
		if w.Dog.Gone() {
			return triageEnd{}, w.halt(exit(5, "the watchdog is gone during triage; r-loop resume triages again"))
		}
		select {
		case end := <-run.end:
			return end, nil
		case <-tick.C:
		case <-deadline:
			w.closeTriage()
			return triageEnd{}, w.halt(exit(4, "the watchdog did not finish triage within %s; r-loop resume triages again", timeout))
		case <-ctx.Done():
			w.closeTriage()
			return triageEnd{}, w.halt(exit(4, "stopped during triage"))
		}
	}
}

func (w *Wiring) closeTriage() {
	w.triageMu.Lock()
	if w.triaging != nil {
		w.triaging.closed = true
	}
	w.triageMu.Unlock()
}

func (w *Wiring) abortTriage(msg string) error {
	w.closeTriage()
	if err := w.Store.MarkAbort(w.Loop.RunID); err != nil {
		fmt.Fprintf(w.Env.Stderr, "r-loop: store: %v\n", err)
	}
	if err := w.Store.Append(w.Loop.RunID, core.Record{Kind: core.RecordRun, At: time.Now(), Run: core.RunHalted, Reason: core.ReasonAborted}); err != nil {
		fmt.Fprintf(w.Env.Stderr, "r-loop: store: %v\n", err)
	}
	w.record(core.Event{Kind: "aborted", Fields: map[string]string{"reason": msg}})
	return exit(1, "%s; r-loop resume triages again", msg)
}

func (w *Wiring) startAfterTriage(opts core.RunOptions, run *triageRun, end triageEnd) (core.RunOptions, bool, error) {
	kept, skipped, groups := core.TriageResult(w.Plan, run.list, end.triage)
	for _, ev := range skipped {
		w.record(ev)
	}
	gate, err := json.MarshalIndent(gateRecord{Decision: end.decision, By: end.by, MaintainerSaid: end.said, Triage: end.triage}, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(w.Store.Dir(w.Loop.RunID), "gate.json"), append(gate, '\n'), 0o644)
	}
	if err != nil {
		return opts, false, w.halt(exit(2, "%v", err))
	}
	if w.Plan.Backlog {
		w.setPlan(core.GroupBacklog(w.Plan, groups))
	}
	w.groups = groups
	w.recordRunList(kept, groups)
	if len(kept) == 0 {
		w.record(core.Event{Kind: "finished", Fields: map[string]string{"reason": "nothing left to run after triage"}})
		if err := w.Store.Append(w.Loop.RunID, core.Record{Kind: core.RecordRun, At: time.Now(), Run: core.RunFinished}); err != nil {
			return opts, false, exit(2, "%v", err)
		}
		return opts, true, nil
	}
	opts.From, opts.Phases = "", phaseIDs(kept)
	return opts, false, nil
}

func (w *Wiring) submitTriage(t core.Triage) (bool, string, string) {
	w.triageMu.Lock()
	defer w.triageMu.Unlock()
	run := w.triaging
	if run == nil || run.closed {
		return false, "no triage is open", ""
	}
	checked, err := core.ValidateTriage(w.Plan, run.list, t, func(c string) string { return core.CheckCitation(w.Repo.Root(), c) })
	if err != nil {
		return false, err.Error(), ""
	}
	table, err := w.showTriage(run, checked)
	if err != nil {
		return false, err.Error(), ""
	}
	if !run.ask {
		by := "yes"
		if w.Opts.Unattended {
			by = "unattended"
		}
		run.closed = true
		run.end <- triageEnd{decision: core.GateGo, by: by, triage: checked}
	}
	return true, "", table
}

func (w *Wiring) submitGate(g core.GateDecision) (bool, string, string) {
	w.triageMu.Lock()
	defer w.triageMu.Unlock()
	run := w.triaging
	switch {
	case run == nil || run.closed:
		return false, "no triage is open", ""
	case !run.ask:
		return false, "this run starts without asking the maintainer; there is no gate", ""
	case run.current == nil:
		return false, "no triage accepted yet: call submit_triage first", ""
	}
	next, err := core.ApplyGate(w.Plan, run.list, *run.current, g)
	if err != nil {
		return false, err.Error(), ""
	}
	if g.Decision == core.GateAbort {
		run.closed = true
		run.end <- triageEnd{decision: core.GateAbort, by: "maintainer", said: g.MaintainerSaid, triage: *run.current}
		return true, "", ""
	}
	table, err := w.showTriage(run, next)
	if err != nil {
		return false, err.Error(), ""
	}
	if g.Decision == core.GateGo {
		run.closed = true
		run.end <- triageEnd{decision: core.GateGo, by: "maintainer", said: g.MaintainerSaid, triage: next}
	}
	return true, "", table
}

func (w *Wiring) showTriage(run *triageRun, t core.Triage) (string, error) {
	dir := w.Store.Dir(w.Loop.RunID)
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "triage.json"), append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	summary, table := core.RenderTriage(core.TriageView{Plan: w.Plan, List: run.list, Checks: w.Findings, Deferrals: run.deferrals, Kinds: w.Loop.Kinds}, &t)
	path := filepath.Join(dir, "triage.md")
	if err := os.WriteFile(path, []byte(summary+"\n\n"+table), 0o644); err != nil {
		return "", err
	}
	run.current = &t
	w.record(core.Event{Kind: "triage", Fields: map[string]string{"summary": summary, "table": table, "path": path}})
	return table, nil
}

func (w *Wiring) recordRunList(list []core.Phase, groups []core.Group) {
	fields := map[string]string{"phases": strings.Join(phaseIDs(list), ",")}
	if len(groups) > 0 {
		if data, err := json.Marshal(groups); err == nil {
			fields["groups"] = string(data)
		}
	}
	w.record(core.Event{Kind: "run-list", Fields: fields})
}

func recordedGroups(run core.RunState) []core.Group {
	var groups []core.Group
	for _, e := range run.Events {
		if e.Kind == "run-list" {
			groups = core.RunListGroups(e.Fields)
		}
	}
	return groups
}
