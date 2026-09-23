package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"r-loop/internal/core"
)

var _ core.Prompts = (*Renderer)(nil)

var stepTemplates = []string{"plan", "implement", "review", "review-ui", "fix", "milestone", "gatefix", "gate"}

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
		"Unattended":    false,
		"AskURL":        "",
		"PhaseWarnings": "",
		"ItemGate":      false,
		"ReviewedKind":  "implement",
		"Round":         1,
		"Rounds":        2,
		"ReviewCommand": "/review",
		"FindingsPath":  "/runs/r1/phase-7/implement-findings-codex-r1.json",
		"FindingsFiles": []core.FindingsFile{
			{Reviewer: "claude", Path: "/runs/r1/phase-7/implement-findings-claude-r1.json"},
			{Reviewer: "codex", Path: "/runs/r1/phase-7/implement-findings-codex-r1.json"},
		},
		"ArtifactsDir":    "/runs/r1/phase-7/implement-rv-ui-r1",
		"RequiredPath":    "/repo/.claude/skills/test-app/SKILL.md",
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

func TestReviewRunsTheNativeCommandFirstAndKeepsItsOutput(t *testing.T) {
	for _, kind := range []string{"implement", "plan"} {
		t.Run(kind, func(t *testing.T) {
			vars := fullVars()
			vars["ReviewedKind"] = kind
			vars["ReviewCommand"] = "codex exec review --uncommitted -o /runs/r1/phase-7/implement-rv-codex-r1/native-review.txt"
			got := render(t, New(t.TempDir()), "review", vars)
			for _, want := range []string{"## Native review", "    " + vars["ReviewCommand"].(string) + "\n", "/runs/r1/phase-7/implement-rv-ui-r1/native-review.txt", "never review by hand", "write a failed sentinel whose reason names the command"} {
				if !strings.Contains(got, want) {
					t.Errorf("prompt lacks %q:\n%s", want, got)
				}
			}
			if strings.Contains(got, "Run `codex exec review") {
				t.Fatalf("command is prose:\n%s", got)
			}
		})
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
			`{"outcome":"ok","reason":""}`,
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
		if strings.Contains(text, `"at"`) || strings.Contains(text, "RFC3339") {
			t.Errorf("%s: the sentinel still asks for a timestamp", name)
		}
	}
}

func TestWatchdogHasNoSentinelParagraph(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	if strings.Contains(text, `"outcome"`) || strings.Contains(text, "/runs/r1/phase-7/plan-a1.sentinel") {
		t.Errorf("watchdog carries the sentinel paragraph:\n%s", text)
	}
	for _, want := range []string{"signal", "propose_remedy", "restart_step", "answer_question", "ask_watchdog", "AskUserQuestion", "You are a full session", "Never commit, merge or push yourself"} {
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
		"Never edit code or tests yourself, and never delete anything that holds work",
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
		"question <id> from phase-<N>/<kind>: <text> options: <options> recommended: <recommended>",
		"answer with a `path:line` citation",
		"the spec file, the tech-design file, the todo, a committed phase plan or code a landed phase wrote",
		"never the current phase's worktree and never anything under `.r-loop/`",
		"When nothing answers it, ask the maintainer here",
		"the citation `maintainer`",
		"Never guess an answer",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q:\n%s", want, text)
		}
	}
}

func TestWatchdogCarriesThePhaseCheck(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"## Checking a phase",
		"check phase <N> worktree <dir> base <base>",
		"read the phase block and the tree in the worktree",
		"which files must change and how deep the cut is",
		"compare that with its `Files:` and `Risk:` lines",
		"call `signal` with `warn` and step `phase-<N>/check` once per disagreement",
		"Never rewrite the plan, and never halt on a phase check",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q:\n%s", want, text)
		}
	}
}

func TestStepTemplatesSendRealChoicesToTheWatchdog(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range stepTemplates {
		text := render(t, r, name, fullVars())
		if !strings.Contains(text, "call the `ask_watchdog` tool") || !strings.Contains(text, "Never ask the user in this pane") {
			t.Errorf("%s does not send real choices to the watchdog:\n%s", name, text)
		}
		if strings.Contains(text, "ask_user") {
			t.Errorf("%s names ask_user", name)
		}
		if !strings.Contains(text, "It returns at once: end your turn then and do nothing else until the answer arrives as your next message") {
			t.Errorf("%s does not say to end the turn after asking:\n%s", name, text)
		}
	}
}

func TestWatchdogKnowsTheAskingAgentWaitsIdleForItsAnswer(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	if !strings.Contains(text, "The agent has ended its turn and waits idle, its backstop frozen, until you answer with `answer_question`; the driver then types your answer into its pane.") {
		t.Errorf("watchdog does not say how the answer reaches the agent:\n%s", text)
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

func TestPlanTracesEveryElementToAnObligation(t *testing.T) {
	text := render(t, New(t.TempDir()), "plan", fullVars())

	for _, want := range []string{
		"Start with the obligations",
		"A case with no source you can name is not an obligation",
		"fewest new concepts",
		"Every element the design adds names the obligation that needs it",
		"Cutting never touches the floor",
		"## Left out",
		"every change serves an obligation",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plan missing %q", want)
		}
	}
}

func TestPlanReviewJudgesProportionBothWays(t *testing.T) {
	r := New(t.TempDir())

	planReview := render(t, r, "review", with("ReviewedKind", "plan"))
	for _, want := range []string{
		"**Missing**",
		"**Excess**",
		"the simpler replacement that still meets every obligation",
		"A simplification that would drop an obligation is not a finding",
		"Taste is not a finding",
	} {
		if !strings.Contains(planReview, want) {
			t.Errorf("plan review missing %q", want)
		}
	}
	if strings.Contains(render(t, r, "review", fullVars()), "**Excess**") {
		t.Error("implement review carries the plan proportion check")
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
		"Excess that adds a type, interface, config key, dependency or layer no obligation needs is `P2`",
		"An excess finding is `real` only when its replacement still meets every obligation",
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

func TestWatchdogCarriesTheUnattendedProviderRule(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"a provider usage limit, an authentication failure or an outage",
		"propose a `provider` remedy naming the row's fallback",
		"then `restart_step` with it",
		"/config.resolved.yaml`",
		"prefer `retry` with an addendum for anything the agent can do differently",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q:\n%s", want, text)
		}
	}
}

func TestPlanAndImplementTreatResolvedFirstEntriesAsSettled(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range []string{"plan", "implement"} {
		text := render(t, r, name, fullVars())
		if !strings.Contains(text, "A `Resolved first:` list under the phase records decisions the maintainer has already taken: follow each `Resolved:` line and never ask about it again.") {
			t.Errorf("%s lacks the Resolved first rule:\n%s", name, text)
		}
	}
}

func TestPlanAndImplementNameTheItemGateOnlyWhenSet(t *testing.T) {
	r := New(t.TempDir())

	for _, name := range []string{"plan", "implement"} {
		if strings.Contains(render(t, r, name, fullVars()), "## Gate") {
			t.Errorf("%s: gate named without ItemGate", name)
		}
		if !strings.Contains(render(t, r, name, with("ItemGate", true)), "## Gate") {
			t.Errorf("%s: gate not named with ItemGate", name)
		}
	}
}

func TestGateAsksForTheCommandAtItsReportPath(t *testing.T) {
	text := render(t, New(t.TempDir()), "gate", fullVars())

	for _, want := range []string{"/runs/r1/report-m2.md", "whole test suite", "/runs/r1/phase-7/plan-a1.sentinel"} {
		if !strings.Contains(text, want) {
			t.Errorf("gate missing %q", want)
		}
	}
}

func TestReviewAsksForATestPerCriterionOnlyForAnItem(t *testing.T) {
	r := New(t.TempDir())

	if strings.Contains(render(t, r, "review", fullVars()), "name the test that proves it") {
		t.Error("criterion rule without ItemGate")
	}
	plan := render(t, r, "review", with("ItemGate", true))
	vars := fullVars()
	vars["ItemGate"], vars["ReviewedKind"] = true, "plan"
	planReview := render(t, r, "review", vars)
	if !strings.Contains(plan, "- [ ] render prompts") || !strings.Contains(plan, "report every criterion no test proves as a finding") || strings.Contains(plan, "## Evidence") {
		t.Errorf("implement review:\n%s", plan)
	}
	if !strings.Contains(planReview, "in the plan's `## Tests`") || !strings.Contains(planReview, "## Evidence") {
		t.Errorf("plan review:\n%s", planReview)
	}
}

func TestWatchdogWalksTheBlockersLikePlanUnblock(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"## Resolving blockers",
		"one at a time, in the order given",
		"The goal is to fix each blocker, not only to record it",
		"\"How do we fix it?\"",
		"What you can do now, when it is work a session can do",
		"`I do it myself, then tell you the result`",
		"For an entry of kind `person`, offer only this and `Not now`",
		"`Not now — skip phase <N> this run`",
		"leave no file behind",
		"Resolved: <YYYY-MM-DD> — <the decision or the result>; <what settled it>",
		"(estimate; check again <when>)",
		"Never edit that place yourself",
		"Change nothing else in the plan and no other file",
		"Carry the walk forward",
		"write the empty file the request names, then stop",
		"The driver waits for that file, not for your reply",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q", want)
		}
	}
}

func TestOnlyTheWatchdogAsksTheMaintainerInItsOwnSession(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"## Talking to the maintainer",
		"Only you ask the maintainer, and only here, in your own session",
		"ask_maintainer(question, options?, recommended?)",
		"First call `ask_maintainer` with the question",
		"AskUserQuestion",
		"a person who has not read the logs",
		"Offer what you can do yourself as an option",
		"propose_remedy(class, command, why, maintainer_said?)",
		"restart_step(step, addendum?, provider?, maintainer_said?)",
		"call `propose_remedy` again with `maintainer_said` set to their reply",
		"write the empty file the request names",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q", want)
		}
	}
	for _, gone := range []string{"ask_user", "consent_question", "maintainer:<id>", "unattended"} {
		if strings.Contains(text, gone) {
			t.Errorf("watchdog still names %q", gone)
		}
	}
}

func TestAnUnattendedWatchdogNeverAsksTheMaintainer(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", with("Unattended", true))

	for _, want := range []string{
		"This run is unattended: never ask the maintainer",
		"take the agent's recommended option and cite the `path:line` that best supports it",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "the citation `maintainer`") {
		t.Errorf("an unattended watchdog is told to cite the maintainer:\n%s", text)
	}
}

func TestUIReviewPromptPointsAtTheTestSkillAndTheCaptureDir(t *testing.T) {
	text := render(t, New(t.TempDir()), "review-ui", fullVars())

	for _, want := range []string{
		"`/repo/.claude/skills/test-app/SKILL.md`",
		"`/test-app`",
		"`frontend-design`",
		"`/runs/r1/phase-7/implement-rv-ui-r1`",
		"`/runs/r1/phase-7/implement-findings-codex-r1.json`",
		"never an empty list",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Earlier rounds") {
		t.Errorf("round 1 prompt names earlier rounds")
	}
	later := render(t, New(t.TempDir()), "review-ui", with("PriorFindings", "- /runs/r1/phase-7/implement-findings-ui-r1.json"))
	if !strings.Contains(later, "implement-findings-ui-r1.json") {
		t.Errorf("round 2 prompt lacks earlier findings:\n%s", later)
	}
}

func TestIntakeTemplateCarriesTheTextTheUsageAndTheSubmitTool(t *testing.T) {
	vars := map[string]any{"Text": "the loop plan, phases 3 and 4", "Dir": "/repo/sub", "Root": "/repo", "Usage": "  -phases n,n", "Unattended": false}

	text := render(t, New(t.TempDir()), "intake", vars)

	for _, want := range []string{"the loop plan, phases 3 and 4", "/repo/sub", "-phases n,n", "submit_args", "get a yes before you submit", "AskUserQuestion"} {
		if !strings.Contains(text, want) {
			t.Errorf("intake missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "outcome") {
		t.Errorf("intake carries the sentinel paragraph:\n%s", text)
	}
}

func TestUnattendedIntakeNeverAsks(t *testing.T) {
	vars := map[string]any{"Text": "phase 3", "Dir": "/repo", "Root": "/repo", "Usage": "", "Unattended": true}

	text := render(t, New(t.TempDir()), "intake", vars)

	if strings.Contains(text, "AskUserQuestion") || !strings.Contains(text, "Do not ask them anything") {
		t.Errorf("unattended intake:\n%s", text)
	}
}
