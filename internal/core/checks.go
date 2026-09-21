package core

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxListedPaths = 10

type onceCheck struct {
	name  string
	test  func(CheckContext) string
	mu    sync.Mutex
	fired map[StepKey]bool
}

func (c *onceCheck) Name() string { return c.name }

func (c *onceCheck) Run(ctx CheckContext) []Signal {
	key := ctx.Step.Key
	c.mu.Lock()
	done := c.fired[key]
	c.mu.Unlock()
	if done {
		return nil
	}
	reason := c.test(ctx)
	if reason == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fired[key] {
		return nil
	}
	c.fired[key] = true
	return []Signal{{Kind: SignalWarn, Source: SourceDriver, Step: key, Reason: reason}}
}

func ShippedChecks(overtimeFactor, diffFactor float64) []Check {
	checks := []struct {
		name string
		test func(CheckContext) string
	}{
		{"files-outside-plan", filesOutsidePlan},
		{"step-overtime", func(ctx CheckContext) string { return stepOvertime(ctx, overtimeFactor) }},
		{"diff-oversize", func(ctx CheckContext) string { return diffOversize(ctx, diffFactor) }},
		{"plan-touched", planTouched},
		{"foreign-test-edit", foreignTestEdit},
	}
	out := make([]Check, 0, len(checks))
	for _, c := range checks {
		out = append(out, &onceCheck{name: c.name, test: c.test, fired: map[StepKey]bool{}})
	}
	return out
}

func worktreeDir(ctx CheckContext) string {
	if ctx.Session != nil && ctx.Session.Dir != "" {
		return ctx.Session.Dir
	}
	return ctx.Step.Worktree
}

func changedFiles(ctx CheckContext) []string {
	if ctx.Repo == nil || ctx.Step.Base == "" {
		return nil
	}
	files, err := ctx.Repo.ChangedFiles(worktreeDir(ctx), ctx.Step.Base)
	if err != nil {
		return nil
	}
	return files
}

func underFiles(p string, files []string) bool {
	for _, f := range files {
		dir := strings.TrimSuffix(f, "/")
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return true
		}
	}
	return false
}

func filesOutsidePlan(ctx CheckContext) string {
	if len(ctx.Step.Phase.Files) == 0 {
		return ""
	}
	var outside []string
	for _, p := range changedFiles(ctx) {
		if !underFiles(p, ctx.Step.Phase.Files) && !strings.HasPrefix(p, ".task-plans/") {
			outside = append(outside, p)
		}
	}
	if len(outside) == 0 {
		return ""
	}
	listed := outside[:min(len(outside), maxListedPaths)]
	reason := "files outside the phase's Files: " + strings.Join(listed, ", ")
	if more := len(outside) - len(listed); more > 0 {
		reason += fmt.Sprintf(" and %d more", more)
	}
	return reason
}

func landedRun(ctx CheckContext) (RunState, map[int]bool, bool) {
	if ctx.Store == nil {
		return RunState{}, nil, false
	}
	st, err := ctx.Store.Load(ctx.Step.Key.Run)
	if err != nil {
		return RunState{}, nil, false
	}
	landed := map[int]bool{}
	for _, l := range st.Landed {
		landed[l.Phase] = true
	}
	return st, landed, len(landed) >= 2
}

func stepOvertime(ctx CheckContext, factor float64) string {
	st, landed, ok := landedRun(ctx)
	if !ok {
		return ""
	}
	var longest time.Duration
	for key, sp := range st.Spans {
		if key.Kind == ctx.Step.Key.Kind && landed[key.Phase] && !sp.Started.IsZero() && !sp.Ended.IsZero() {
			longest = max(longest, sp.Ended.Sub(sp.Started))
		}
	}
	elapsed := ctx.Now.Sub(ctx.Started).Round(time.Second)
	if longest <= 0 || float64(elapsed) <= factor*float64(longest) {
		return ""
	}
	return fmt.Sprintf("%s has run %s, over %g× the longest landed %s step (%s)", stepName(ctx.Step.Key), elapsed, factor, ctx.Step.Key.Kind, longest.Round(time.Second))
}

func diffOversize(ctx CheckContext, factor float64) string {
	st, _, ok := landedRun(ctx)
	if !ok || ctx.Repo == nil || ctx.Step.Base == "" {
		return ""
	}
	largest := 0
	for _, l := range st.Landed {
		largest = max(largest, l.Added+l.Deleted)
	}
	added, deleted, err := ctx.Repo.DiffStat(worktreeDir(ctx), ctx.Step.Base)
	if err != nil || largest == 0 || float64(added+deleted) <= factor*float64(largest) {
		return ""
	}
	return fmt.Sprintf("diff of %d lines (+%d -%d) is over %g× the largest landed phase (%d lines)", added+deleted, added, deleted, factor, largest)
}

func todoRel(ctx CheckContext) string {
	todo, _ := ctx.Step.Vars["TodoPath"].(string)
	if todo == "" {
		todo = ctx.Plan.Path
	}
	if todo == "" || !filepath.IsAbs(todo) || ctx.Repo == nil {
		return filepath.ToSlash(todo)
	}
	rel, err := filepath.Rel(ctx.Repo.Root(), todo)
	if err != nil {
		return filepath.ToSlash(todo)
	}
	return filepath.ToSlash(rel)
}

func planTouched(ctx CheckContext) string {
	changed := changedFiles(ctx)
	if len(changed) == 0 {
		return ""
	}
	todo := todoRel(ctx)
	own := phasePlanPath(ctx.Step.Phase.Number, ctx.Step.Phase.Title)
	for _, p := range changed {
		if (todo != "" && p == todo) || (strings.HasPrefix(p, ".task-plans/") && p != own) {
			return "plan file changed during " + stepName(ctx.Step.Key) + ": " + p
		}
	}
	return ""
}

func isTestPath(p string) bool {
	base := path.Base(p)
	for _, pattern := range []string{"*_test.go", "*.test.*", "*_test.*"} {
		if ok, _ := path.Match(pattern, base); ok {
			return true
		}
	}
	return strings.HasPrefix(p, "test/")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func foreignTestEdit(ctx CheckContext) string {
	if len(ctx.Step.Phase.Files) == 0 {
		return ""
	}
	var candidates []string
	for _, p := range changedFiles(ctx) {
		if isTestPath(p) && !underFiles(p, ctx.Step.Phase.Files) {
			candidates = append(candidates, p)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	quoted := make([]string, len(candidates))
	for i, p := range candidates {
		quoted[i] = shellQuote(p)
	}
	command := fmt.Sprintf("git ls-tree -r --name-only %s -- %s", shellQuote(ctx.Step.Base), strings.Join(quoted, " "))
	code, out, err := ctx.Repo.Run(worktreeDir(ctx), command, 30*time.Second)
	if err != nil || code != 0 {
		return ""
	}
	existed := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		existed[strings.TrimSpace(line)] = true
	}
	for _, p := range candidates {
		if existed[p] {
			return "test file that existed at base edited outside the phase's Files: " + p
		}
	}
	return ""
}

func stepName(key StepKey) string {
	return fmt.Sprintf("phase-%d/%s", key.Phase, key.Kind)
}
