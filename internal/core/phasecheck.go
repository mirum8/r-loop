package core

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	phaseCheckStart   = "phase-check-start"
	phaseCheckRan     = "phase-check"
	phaseCheckTimeout = "phase-check-timeout"
	phaseCheckSkipped = "phase-check-skipped"
	phaseCheckHalt    = "phase check may only warn"
)

type CheckOutcome struct {
	Kind, Reason string
}

type PhaseCheck struct {
	Dog     *Watchdog
	Repo    Repo
	Timeout time.Duration
	Backlog bool
}

func (c *PhaseCheck) Run(ctx context.Context, ph Phase, base string) CheckOutcome {
	if !c.Dog.live() {
		return CheckOutcome{Kind: phaseCheckSkipped, Reason: "watchdog unreachable"}
	}
	wt := fmt.Sprintf(".r-loop/wt/phase-%s", ph.ID)
	if err := c.Repo.AddWorktree(wt, fmt.Sprintf("r-loop/phase-%s", ph.ID), base); err != nil {
		return CheckOutcome{Kind: phaseCheckSkipped, Reason: "worktree: " + err.Error()}
	}
	if err := c.Dog.Notify(checkText(ph, filepath.Join(c.Repo.Root(), wt), base, c.Backlog), true, c.Timeout); err != nil {
		return CheckOutcome{Kind: phaseCheckTimeout, Reason: err.Error()}
	}
	return CheckOutcome{Kind: phaseCheckRan}
}

func checkText(ph Phase, worktree, base string, backlog bool) string {
	if backlog {
		return fmt.Sprintf("check phase %s worktree %s base %s\nBacklog item: an issues file names no files and no risk\n\n%s", ph.ID, worktree, base, ph.Block)
	}
	files, risk := strings.Join(ph.Files, ", "), ph.Risk
	if files == "" {
		files = "none"
	}
	if risk == "" {
		risk = "none"
	}
	return fmt.Sprintf("check phase %s worktree %s base %s\nFiles: %s\nRisk: %s\n\n%s", ph.ID, worktree, base, files, risk, ph.Block)
}
