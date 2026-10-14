package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const (
	EventFailureReport        = "failure-report"
	EventFailureReportMissing = "failure-report-missing"
)

func FailureReportText(path, reason string) string {
	return fmt.Sprintf(`failure report %s
The run halted: %s
Diagnose the halt before your session is closed. Read events.jsonl, signals.jsonl, remedies.jsonl and report.md in the run dir, the transcripts report.md lists under Halt, and the worktrees and git log as needed.
Write only %s, in markdown:
- Verdict: driver bug, plan, agent or environment
- Timeline of what led to the halt
- Root cause, with file:line evidence
- Impact: what was committed, and what r-loop resume will do
- Fix directions
Do not edit code, do not commit, do not call signal.`, path, reason, path)
}

func (d *Watchdog) FailureReport(ctx context.Context, timeout time.Duration) {
	st, err := d.Store.Load(d.RunID)
	if err != nil || st.Status != RunHalted || aborted(st) || ctx.Err() != nil || !d.live() {
		return
	}
	path := filepath.Join(d.RunDir, "failure-report.md")
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		recordEvent(d.Store, d.Face, d.RunID, Event{Kind: EventFailureReportMissing, Fields: map[string]string{"reason": "remove stale " + path + ": " + err.Error()}})
		return
	}
	err = d.NotifyContext(ctx, FailureReportText(path, st.Reason), true, timeout)
	if info, serr := os.Stat(path); serr == nil && info.Size() > 0 {
		recordEvent(d.Store, d.Face, d.RunID, Event{Kind: EventFailureReport, Fields: map[string]string{"path": path}})
		return
	}
	reason := "watchdog wrote no " + path
	if err != nil {
		reason += ": " + err.Error()
	}
	recordEvent(d.Store, d.Face, d.RunID, Event{Kind: EventFailureReportMissing, Fields: map[string]string{"reason": reason}})
}

func aborted(st RunState) bool {
	out := false
	for _, ev := range st.Events {
		switch {
		case ev.Kind == "human" && ev.Fields["what"] == "resume":
			out = false
		case ev.Kind == "aborted":
			out = true
		}
	}
	return out
}
