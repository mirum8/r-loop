package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"r-loop/internal/core"
)

var _ core.Prompts = (*Renderer)(nil)

var stepTemplates = []string{"plan", "implement", "review", "fix", "milestone", "gatefix"}

func fullVars() map[string]any {
	return map[string]any{
		"PhaseNumber":   7,
		"PhaseTitle":    "PromptRenderer",
		"PhaseBlock":    "### Phase 7 — PromptRenderer",
		"Criteria":      "- [ ] render prompts",
		"TodoPath":      "docs/todo.md",
		"SpecDir":       "docs",
		"PlanPath":      ".task-plans/phase-7.md",
		"Branch":        "r-loop/phase-7",
		"Base":          "main",
		"Worktree":      "/wt/phase-7",
		"Sentinel":      "/runs/r1/phase-7/plan-a1.sentinel",
		"RunDir":        "/runs/r1",
		"Allow":         []string{},
		"AskURL":        "",
		"PhaseWarnings": "",
		"ReviewedKind":  "implement",
		"Round":         1,
		"Rounds":        2,
		"ReviewCommand": "/review",
		"FindingsPath":  "/runs/r1/phase-7/implement-findings-codex-r1.json",
		"FindingsFiles": []core.FindingsFile{
			{Reviewer: "claude", Path: "/runs/r1/phase-7/implement-findings-claude-r1.json"},
			{Reviewer: "codex", Path: "/runs/r1/phase-7/implement-findings-codex-r1.json"},
		},
		"PriorFindings":   "",
		"PriorVerdicts":   "",
		"RoundTree":       "",
		"VerdictPath":     "/runs/r1/phase-7/implement-r1.verdict.json",
		"ReportPath":      "/runs/r1/report-m2.md",
		"MilestoneName":   "Milestone 2",
		"MilestonePhases": "4, 5, 6, 7",
		"GateCommand":     "go test ./internal/prompts/...",
		"GateOutput":      "FAIL",
		"Addendum":        "",
	}
}

func with(key string, value any) map[string]any {
	vars := fullVars()
	vars[key] = value
	return vars
}

func render(t *testing.T, r *Renderer, name string, vars map[string]any) string {
	t.Helper()
	text, _, err := r.Render(name, vars)
	if err != nil {
		t.Fatalf("Render(%q): %v", name, err)
	}
	return text
}

func TestOverrideWinsAndNamesItsSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".r-loop", "prompts", "plan.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("custom plan for phase {{.PhaseNumber}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	text, source, err := New(dir).Render("plan", fullVars())

	if err != nil {
		t.Fatal(err)
	}
	if text != "custom plan for phase 7" {
		t.Errorf("text = %q", text)
	}
	if source != path {
		t.Errorf("source = %q, want %q", source, path)
	}
}

func TestEmbeddedFallbackWithoutOverride(t *testing.T) {
	text, source, err := New(t.TempDir()).Render("plan", fullVars())

	if err != nil {
		t.Fatal(err)
	}
	if source != "embedded" {
		t.Errorf("source = %q, want embedded", source)
	}
	if !strings.Contains(text, ".task-plans/phase-7.md") {
		t.Errorf("embedded plan does not name the plan path:\n%s", text)
	}
}

func TestUnknownNameIsAnError(t *testing.T) {
	_, _, err := New(t.TempDir()).Render("nonsense", fullVars())

	if err == nil {
		t.Fatal("want error for unknown template")
	}
}

func TestMissingVariableFails(t *testing.T) {
	vars := fullVars()
	delete(vars, "Sentinel")

	_, _, err := New(t.TempDir()).Render("implement", vars)

	if err == nil || !strings.Contains(err.Error(), "Sentinel") {
		t.Fatalf("err = %v, want missing Sentinel", err)
	}
}

func TestMissingVariableFailsInOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".r-loop", "prompts", "fix.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{{.NoSuchVar}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := New(dir).Render("fix", fullVars())

	if err == nil {
		t.Fatal("want error for absent variable")
	}
}

func TestAddendumSectionOnlyWhenSet(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range stepTemplates {
		without := render(t, r, name, fullVars())
		if strings.Contains(without, "Note from the previous attempt:") {
			t.Errorf("%s: addendum section present without Addendum", name)
		}

		withNote := render(t, r, name, with("Addendum", "the build broke on go vet"))
		idx := strings.Index(withNote, "Note from the previous attempt:")
		if idx < 0 {
			t.Errorf("%s: addendum section missing", name)
			continue
		}
		if !strings.Contains(withNote[idx:], "the build broke on go vet") {
			t.Errorf("%s: addendum text not in the final section", name)
		}
		if strings.Contains(withNote[idx:], "{\"outcome\":\"ok\"") {
			t.Errorf("%s: addendum is not the final section", name)
		}
	}
}

func TestAllSevenTemplatesRenderWithFullVariableSet(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range append(stepTemplates, "watchdog") {
		text, source, err := r.Render(name, fullVars())
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if source != "embedded" || strings.TrimSpace(text) == "" {
			t.Errorf("%s: source %q, empty %v", name, source, strings.TrimSpace(text) == "")
		}
		if strings.Contains(text, "<no value>") {
			t.Errorf("%s: rendered <no value>", name)
		}
	}
}

func TestStepTemplatesCarryTheSentinelParagraph(t *testing.T) {
	r := New(t.TempDir())
	sentinel := "/runs/r1/phase-7/plan-a1.sentinel"
	for _, name := range stepTemplates {
		text := render(t, r, name, fullVars())
		for _, want := range []string{
			`{"outcome":"ok","reason":"","at":"<RFC3339>"}`,
			`{"outcome":"failed","reason":"<why>"`,
			sentinel,
			"last action",
			"never report completion only in the terminal",
			"Never commit",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("%s: missing %q", name, want)
			}
		}
	}
}

func TestWatchdogHasNoSentinelParagraph(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	if strings.Contains(text, `"outcome"`) || strings.Contains(text, "/runs/r1/phase-7/plan-a1.sentinel") {
		t.Errorf("watchdog carries the sentinel paragraph:\n%s", text)
	}
	for _, want := range []string{"signal", "propose_remedy", "restart_step", "answer_question", "never approve"} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q", want)
		}
	}
}

func TestWatchdogCarriesTheStepWatchingRuleAndTheAllowList(t *testing.T) {
	vars := fullVars()
	vars["Allow"] = []string{"deps", "ports"}

	text := render(t, New(t.TempDir()), "watchdog", vars)

	for _, want := range []string{
		"step started",
		"herdr agent read <name> --source recent-unwrapped --lines 200",
		"git -C <worktree> diff <base>",
		"`warn` for anything short of that",
		"step ended",
		"Allow-listed, authorised without asking: `deps`, `ports`.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q:\n%s", want, text)
		}
	}
}

func TestWatchdogCarriesTheRemedyRule(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"## Remedies",
		"Diagnose",
		"propose the exact command with `propose_remedy`",
		"run it only when the decision is `authorised`",
		"then call `restart_step`",
		"Never edit code, tests or the plan",
		"never merge, push, or delete anything that holds work",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q:\n%s", want, text)
		}
	}
}

func TestWatchdogCarriesTheAnsweringRule(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"## Answering questions",
		"question <id> from phase-<N>/<kind>: <text> options: <options>",
		"answer only with a `path:line` citation",
		"the spec file, the tech-design file, the todo, a committed phase plan or code a landed phase wrote",
		"never the current phase's worktree and never anything under `.r-loop/`",
		"call `answer_question` with an empty citation to escalate rather than guess",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q:\n%s", want, text)
		}
	}
}

func TestAskUserOnlyWhenAskURLSet(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range stepTemplates {
		if strings.Contains(render(t, r, name, fullVars()), "ask_user") {
			t.Errorf("%s: mentions ask_user without AskURL", name)
		}
		if !strings.Contains(render(t, r, name, with("AskURL", "http://127.0.0.1:9/mcp/x")), "ask_user") {
			t.Errorf("%s: no ask_user with AskURL", name)
		}
	}
}

func TestPlanListsPhaseWarningsOnlyWhenSet(t *testing.T) {
	r := New(t.TempDir())

	if strings.Contains(render(t, r, "plan", fullVars()), "The watchdog's phase check warned:") {
		t.Error("warnings section present without PhaseWarnings")
	}
	text := render(t, r, "plan", with("PhaseWarnings", "- Files: names a package that does not exist"))
	if !strings.Contains(text, "The watchdog's phase check warned:") || !strings.Contains(text, "names a package that does not exist") {
		t.Errorf("warnings not listed:\n%s", text)
	}
}

func TestPlanNamesItsStructure(t *testing.T) {
	text := render(t, New(t.TempDir()), "plan", fullVars())

	for _, want := range []string{"status: planned", "## Summary", "## Changes", "## Tests", "## Assumptions", "- [ ] render prompts", "/wt/phase-7"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan missing %q", want)
		}
	}
}

func TestImplementReadsThePlanAndProtectsTodo(t *testing.T) {
	text := render(t, New(t.TempDir()), "implement", fullVars())

	for _, want := range []string{".task-plans/phase-7.md", "## Tests", "docs/todo.md", "failed"} {
		if !strings.Contains(text, want) {
			t.Errorf("implement missing %q", want)
		}
	}
}

func TestReviewTargetsPlanOrWorktree(t *testing.T) {
	r := New(t.TempDir())

	planReview := render(t, r, "review", with("ReviewedKind", "plan"))
	if !strings.Contains(planReview, ".task-plans/phase-7.md") {
		t.Errorf("plan review does not name the plan:\n%s", planReview)
	}
	codeReview := render(t, r, "review", fullVars())
	if !strings.Contains(codeReview, "uncommitted changes in /wt/phase-7") {
		t.Errorf("implement review does not name the worktree changes:\n%s", codeReview)
	}
	for _, want := range []string{"/review", "round 1 of 2", `"reviewer":"<name>"`, "<name>-r<round>-<n>", "implement-findings-codex-r1.json", "unique", "exactly once"} {
		if !strings.Contains(codeReview, want) {
			t.Errorf("review missing %q", want)
		}
	}
	if strings.Contains(codeReview, "dismissed with evidence") {
		t.Error("prior-rounds section present in round 1")
	}
}

func TestReviewNamesPriorRounds(t *testing.T) {
	vars := fullVars()
	vars["Round"] = 2
	vars["PriorFindings"] = "/runs/r1/phase-7/implement-rv-codex-r1.findings.json"
	vars["PriorVerdicts"] = "/runs/r1/phase-7/implement-r1.verdict.json"
	vars["RoundTree"] = "abc123"

	text := render(t, New(t.TempDir()), "review", vars)

	for _, want := range []string{"abc123", "implement-r1.verdict.json", "dismissed with evidence"} {
		if !strings.Contains(text, want) {
			t.Errorf("review round 2 missing %q", want)
		}
	}
}

func TestFixAsksForVerdicts(t *testing.T) {
	text := render(t, New(t.TempDir()), "fix", fullVars())

	for _, want := range []string{
		"real", "not-real", "out-of-scope", "P1", "P4", "path:line", "implement-r1.verdict.json", `"evidence"`,
		"- claude: `/runs/r1/phase-7/implement-findings-claude-r1.json`",
		"- codex: `/runs/r1/phase-7/implement-findings-codex-r1.json`",
		`{"findings":[{"id":"<id>","reviewer":"<name>","title":"…","verdict":"real|not-real|out-of-scope","severity":"P1|P2|P3|P4","fixed":true|false,"files":["…"],"evidence":"<path:line>"}]}`,
		"Only a finding that is `real` at `P1` or `P2` may be fixed",
		"a `path:line` you have read",
		"Do not commit",
		"exactly once",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("fix missing %q", want)
		}
	}
}

func TestGatefixNamesCommandAndOutput(t *testing.T) {
	text := render(t, New(t.TempDir()), "gatefix", with("GateOutput", "--- FAIL: TestX"))

	for _, want := range []string{"go test ./internal/prompts/...", "--- FAIL: TestX", "docs/todo.md"} {
		if !strings.Contains(text, want) {
			t.Errorf("gatefix missing %q", want)
		}
	}
}

func TestMilestoneNamesReport(t *testing.T) {
	text := render(t, New(t.TempDir()), "milestone", fullVars())

	for _, want := range []string{"Milestone 2", "4, 5, 6, 7", "/runs/r1/report-m2.md", ".task-plans/"} {
		if !strings.Contains(text, want) {
			t.Errorf("milestone missing %q", want)
		}
	}
}
