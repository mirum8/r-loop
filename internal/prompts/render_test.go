package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"r-loop/internal/core"
)

var _ core.Prompts = (*Renderer)(nil)

var stepTemplates = []string{"design", "plan", "implement", "review", "review-plan", "review-ui", "review-security", "fix", "milestone", "gatefix", "gate"}

func TestEveryStepPromptTellsTheAgentToWriteTheSentinelAtomically(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range stepTemplates {
		for _, kind := range []string{"implement", "milestone"} {
			if kind == "milestone" && name != "review" && name != "review-ui" && name != "review-security" && name != "fix" {
				continue
			}
			t.Run(name+"/"+kind, func(t *testing.T) {
				vars := fullVars()
				vars["ReviewedKind"] = kind
				got := render(t, r, name, vars)
				for _, want := range []string{"temporary file in the same directory", "then rename that file onto `/runs/r1/phase-7/plan-a1.sentinel`"} {
					if !strings.Contains(got, want) {
						t.Errorf("%s prompt missing %q", name, want)
					}
				}
			})
		}
	}
}

func TestWatchdogPhaseCheckJudgesABacklogItemAgainstTheCode(t *testing.T) {
	got := render(t, New(t.TempDir()), "watchdog", fullVars())
	for _, want := range []string{"carries a `Backlog item` line in place of those lines", "warn only where the item disagrees with the code", "never because it has no `Files:` or `Risk:` line"} {
		if !strings.Contains(got, want) {
			t.Errorf("watchdog prompt missing %q:\n%s", want, got)
		}
	}
}

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
		"Dialogs":       []string{},
		"Unattended":    false,
		"AskURL":        "",
		"PhaseWarnings": "",
		"ItemGate":      false,
		"ReviewedKind":  "implement",
		"Round":         1,
		"Rounds":        2,
		"ReviewCommand": "/review",
		"ReviewRan":     false,
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
		"GroupItems":      "",
		"Reworks":         "",
		"TriageNotes":     "",
		"DesignPath":      ".task-plans/phase-7.design.md",
		"DesignNote":      "",
		"Design":          "",
		"DesignIntent":    "",
		"Root":            "/repo",
	}
}

func TestReviewRunsTheNativeCommandFirstAndKeepsItsOutput(t *testing.T) {
	for _, kind := range []string{"implement", "plan"} {
		t.Run(kind, func(t *testing.T) {
			vars := fullVars()
			vars["ReviewedKind"] = kind
			vars["ReviewCommand"] = "codex exec review --uncommitted -o /runs/r1/phase-7/implement-rv-codex-r1/native-review.txt"
			got := render(t, New(t.TempDir()), "review", vars)
			for _, want := range []string{"## Native review", "    " + vars["ReviewCommand"].(string) + "\n", "/runs/r1/phase-7/implement-rv-ui-r1/native-review.txt", "never review by hand", "write a failed sentinel whose reason names the command", "inside the folder `/runs/r1/phase-7/implement-rv-ui-r1/`, not beside the `.sentinel` file", "save the text of its `<result>` verbatim; never read its `<output-file>`", "`(none)` is a valid result: save it as is"} {
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

func TestReviewThatAlreadyRanReadsTheDriversFile(t *testing.T) {
	vars := fullVars()
	vars["ReviewCommand"] = "codex review --uncommitted"
	vars["ReviewRan"] = true

	got := render(t, New(t.TempDir()), "review", vars)

	want := "The driver already ran the native review `codex review --uncommitted`. Its output is in `/runs/r1/phase-7/implement-rv-ui-r1/native-review.txt`: read it and base your findings on it, and do not change that file."
	if !strings.Contains(got, want) {
		t.Errorf("prompt lacks %q:\n%s", want, got)
	}
	for _, unwanted := range []string{"Your first action is to run this command", "Save that output verbatim", "above in your conversation"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("prompt has %q:\n%s", unwanted, got)
		}
	}
}

func TestReviewNotRunYetKeepsTheRunInstruction(t *testing.T) {
	got := render(t, New(t.TempDir()), "review", fullVars())

	if !strings.Contains(got, "Your first action is to run this command") || strings.Contains(got, "already ran in this session") {
		t.Errorf("prompt:\n%s", got)
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

func TestLandStagePromptsDoNotOfferAskWatchdog(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range []string{"gatefix", "gate", "milestone"} {
		got := render(t, r, name, fullVars())
		if strings.Contains(got, "ask_watchdog") || !strings.Contains(got, `{"outcome":"ok","reason":""}`) || !strings.Contains(got, fullVars()["Sentinel"].(string)) {
			t.Errorf("%s prompt has wrong outcome instructions:\n%s", name, got)
		}
	}
	for _, name := range []string{"plan", "implement"} {
		got := render(t, r, name, fullVars())
		if !strings.Contains(got, "call the `ask_watchdog` tool") {
			t.Errorf("%s prompt has no ask instructions", name)
		}
	}
}

func TestGatefixReviewAndFixPromptsDoNotOfferAskWatchdog(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range []string{"review", "review-ui", "review-security", "fix"} {
		vars := fullVars()
		vars["ReviewedKind"] = "gatefix"
		got := render(t, r, name, vars)
		if strings.Contains(got, "ask_watchdog") {
			t.Errorf("%s gatefix prompt offers ask_watchdog", name)
		}
		vars["ReviewedKind"] = "implement"
		got = render(t, r, name, vars)
		if !strings.Contains(got, "ask_watchdog") {
			t.Errorf("%s implement prompt lacks ask_watchdog", name)
		}
	}
}

func TestAllTemplatesRenderWithFullVariableSet(t *testing.T) {
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

func TestWatchdogAnswersDialogsUnderTheConfiguredRules(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", with("Dialogs", []string{"approve writes inside the run folder", "allow go test"}))

	for _, want := range []string{
		"## Answering dialogs",
		"dialog <id> from phase-<N>/<kind>: answer with answer_dialog",
		"`answer_dialog(id, keys, rule?, maintainer_said?)`",
		"- `approve writes inside the run folder`\n- `allow go test`\n",
		"`enter`, `esc`, `up`, `down`, `tab`",
		"`rule: \"decline\"` with `keys: [\"esc\"]`",
		"`maintainer_said`",
		"Never press a key you cannot see on the screen",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "decline every dialog no rule covers") {
		t.Errorf("attended watchdog told to decline off-rule dialogs:\n%s", text)
	}
}

func TestWatchdogClearsBlockers(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"- `resolve_blocker(id, action, rule?, addendum?, keys?, provider?, model?, effort?, maintainer_said?)`",
		"## Clearing blockers",
		"blocker <id> from phase-<N>/<step> (<source>): <reason>",
		"`propose_remedy`, then `retry`",
		"`keys` with `rule`",
		"`switch` to the row's fallback",
		"`ask_maintainer` with the options: retry, switch provider, continue as resume would, block this phase, stop the run",
		"Then call `resolve_blocker` again with their reply, quoted, as `maintainer_said`",
		"- `block` and `stop` are refused while the target has retries left: fix the cause and retry. Nothing in the run is skipped.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "nothing authorised fixes it, call `resolve_blocker` with `continue` when it fits, else `block` or `stop`") {
		t.Errorf("attended watchdog told to block without asking:\n%s", text)
	}
	if strings.Contains(text, "retry, skip,") || strings.Contains(text, "`skip`, a provider") {
		t.Errorf("watchdog offered skip on a blocker:\n%s", text)
	}
}

func TestAnAttendedWatchdogRetriesAMinorFailureWithoutAsking(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"- A failure is minor when fixing its cause lets a fresh attempt pass: fix the cause, as \"Fixing it yourself\" says, then `retry` with an `addendum` saying what changed, and do not ask the maintainer.",
		"Ask the maintainer only for consent the allow-list lacks, or when the step has no retries left.",
		"- Otherwise prefer `retry` with an addendum for anything the agent can do differently, saying in the addendum what to change; a minor failure is retried without asking.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q:\n%s", want, text)
		}
	}
}

func TestWatchdogFixesTheCauseItself(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"- `set_config(key, value, why)`",
		"## Fixing it yourself",
		"run it yourself once it comes back `authorised`, then `retry` with an `addendum`",
		"`set_config(key, value, why)` with one scalar key in `.r-loop/config.yaml`",
		"Then, for a held step, `continue`, which reads the config again; `retry` keeps the row the step started on. A held reviewer takes the new value on `retry`, which reopens it from the current config.",
		"Keys under `watchdog.` are the maintainer's: ask them to edit the file.",
		"edit the plan, `docs/todo.md`, in the primary tree, change nothing else there, then `continue`",
		"`docs(plan): correct phase <N>`",
		"A fault in the step's own code: `retry` with an `addendum`",
		"whose reason starts `r-loop bug: ` and names the file and line",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q:\n%s", want, text)
		}
	}
	for _, gone := range []string{"fixer", "apply_fix", "`fix` hands"} {
		if strings.Contains(text, gone) {
			t.Errorf("watchdog still names %q:\n%s", gone, text)
		}
	}
}

func TestAnUnattendedWatchdogLeavesItsOwnConfigKeysAlone(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", with("Unattended", true))

	if !strings.Contains(text, "Keys under `watchdog.` and `unattended.` are the maintainer's: leave them.") || strings.Contains(text, "ask them to edit the file") {
		t.Errorf("unattended watchdog config keys:\n%s", text)
	}
}

func TestAnUnattendedWatchdogBlocksOrStopsWhenNothingAuthorisedClearsABlocker(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", with("Unattended", true))

	if !strings.Contains(text, "nothing authorised fixes it, call `resolve_blocker` with `continue` when it fits, else `block` or `stop`") {
		t.Errorf("unattended watchdog not told to block or stop:\n%s", text)
	}
	if strings.Contains(text, "`ask_maintainer` with the options: retry") {
		t.Errorf("unattended watchdog told to ask about a blocker:\n%s", text)
	}
}

func TestWatchdogWithNoDialogRulesSaysNone(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	if !strings.Contains(text, "The rules in `watchdog.dialogs`:\n- none\n") {
		t.Errorf("watchdog does not say there are no rules:\n%s", text)
	}
}

func TestAnUnattendedWatchdogDeclinesDialogsNoRuleCovers(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", with("Unattended", true))

	if !strings.Contains(text, "decline every dialog no rule covers") {
		t.Errorf("unattended watchdog lacks the decline rule:\n%s", text)
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

func TestWatchdogCarriesTheGateChoice(t *testing.T) {
	for _, unattended := range []bool{false, true} {
		vars := fullVars()
		vars["Unattended"] = unattended

		text := render(t, New(t.TempDir()), "watchdog", vars)

		for _, want := range []string{
			"`choose_gate(phase, commands)`",
			"## Choosing a phase's gate",
			"choose gate phase <N>",
			"shell commands this tree can run",
			"`command -v <name>`",
			"module names, URLs, HTTP requests",
			"never drop one because it might fail",
			"an empty list only when none of them is a command",
			"exited 127",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("unattended %v: watchdog missing %q:\n%s", unattended, want, text)
			}
		}
	}
}

func TestWatchdogCarriesTheBlockerContinue(t *testing.T) {
	for _, c := range []struct {
		unattended bool
		want       string
	}{
		{false, "Take `continue` on your own when it fits: it is authorised as `retry` is"},
		{true, "Take `continue` on your own when it fits: it is authorised as `retry` is"},
	} {
		vars := fullVars()
		vars["Unattended"] = c.unattended

		text := render(t, New(t.TempDir()), "watchdog", vars)

		for _, want := range []string{
			"`continue` re-enters the held step or land as `r-loop resume` would, with no addendum",
			"a step resumes from its recorded review round",
			"a land reads its phase from the plan again",
			"after a transient failure",
			"once its cause was fixed",
			"never when the same check would fail the same way",
			c.want,
		} {
			if !strings.Contains(text, want) {
				t.Errorf("unattended %v: watchdog missing %q:\n%s", c.unattended, want, text)
			}
		}
	}
}

func TestStepTemplatesSendRealChoicesToTheWatchdog(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range []string{"plan", "implement", "review", "review-plan", "review-ui", "review-security", "fix"} {
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

func TestStepPromptsExplainAReworkOnlyForAReworkPhase(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range []string{"plan", "implement", "review", "review-plan"} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(render(t, r, name, fullVars()), "This phase reworks") {
				t.Error("rework paragraph present without Reworks")
			}
			text := render(t, r, name, with("Reworks", "3, 4"))
			for _, want := range []string{"This phase reworks phase 3, 4", "rewritten or deleted, not kept passing", "not a regression"} {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q:\n%s", want, text)
				}
			}
		})
	}
}

func TestPlanNeverBuildsARetiredStory(t *testing.T) {
	text := render(t, New(t.TempDir()), "plan", fullVars())

	if !strings.Contains(text, `<h3 class="retired">`) {
		t.Errorf("plan prompt does not name retired stories:\n%s", text)
	}
}

func TestWatchdogJudgesAReworkByItsNewBehaviour(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{"A phase with a `Reworks:` line", "`foreign-test-edit`"} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog prompt missing %q", want)
		}
	}
}

func TestPlanNamesItsStructure(t *testing.T) {
	text := render(t, New(t.TempDir()), "plan", fullVars())

	for _, want := range []string{"status: planned", "## Summary", "## Changes", "## Tests", "## Assumptions", "- [ ] render prompts", "/wt/phase-7", "one test per list item or table row"} {
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

	planReview := render(t, r, "review-plan", with("ReviewedKind", "plan"))
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
	for _, kind := range []string{"implement", "plan"} {
		if strings.Contains(render(t, r, "review", with("ReviewedKind", kind)), "**Excess**") {
			t.Errorf("review for %s carries the plan proportion check", kind)
		}
	}
}

func TestPlanReviewRunsNoNativeReview(t *testing.T) {
	vars := with("ReviewedKind", "plan")
	vars["ReviewCommand"] = "codex exec review --uncommitted"

	got := render(t, New(t.TempDir()), "review-plan", vars)

	for _, unwanted := range []string{"Native review", "native-review.txt", "codex exec review"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("plan review carries %q:\n%s", unwanted, got)
		}
	}
	for _, want := range []string{"Change no file in `/wt/phase-7`", "/runs/r1/phase-7/implement-findings-codex-r1.json", "call the `ask_watchdog` tool"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan review missing %q:\n%s", want, got)
		}
	}
}

func TestReviewCarriesNoPlanText(t *testing.T) {
	got := render(t, New(t.TempDir()), "review", with("ItemGate", true))
	vars := with("ReviewedKind", "plan")
	vars["ItemGate"] = true
	planKind := render(t, New(t.TempDir()), "review", vars)

	for _, text := range []string{got, planKind} {
		for _, unwanted := range []string{".task-plans/phase-7.md", "## Tests", "## Evidence", "**Missing**"} {
			if strings.Contains(text, unwanted) {
				t.Errorf("review carries plan text %q:\n%s", unwanted, text)
			}
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

	planReview := render(t, r, "review-plan", with("ReviewedKind", "plan"))
	if !strings.Contains(planReview, "the plan at `.task-plans/phase-7.md`, read against the phase block below and the code in `/wt/phase-7`") {
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
		"a `path:line` or `path:line-line` you have read",
		"Your current directory is the worktree root: write the path relative to it, such as `calc_test.go:75`",
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
	planReview := render(t, r, "review-plan", vars)
	if strings.Contains(render(t, r, "review-plan", with("ReviewedKind", "plan")), "name the test that proves it") {
		t.Error("plan criterion rule without ItemGate")
	}
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
		"restart_step(step, addendum?, provider?, model?, effort?, maintainer_said?)",
		"stop_run(when, reason, maintainer_said)",
		"pause_run(reason, maintainer_said)",
		"continue_run(maintainer_said)",
		"## Stopping or pausing the run",
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

func TestPlanImplementAndReviewNameTheGroupOnlyWhenSet(t *testing.T) {
	r := New(t.TempDir())
	const want = "This phase fixes backlog items 3, 5, 7 with one change. Every member's outcome is an obligation, `## Gate` runs the tests of every member, and `status: already-done` holds only when every member is done."

	for _, name := range []string{"plan", "implement", "review", "review-plan"} {
		if strings.Contains(render(t, r, name, fullVars()), "backlog items") {
			t.Errorf("%s: group paragraph without GroupItems", name)
		}
		if text := render(t, r, name, with("GroupItems", "3, 5, 7")); !strings.Contains(text, want) {
			t.Errorf("%s: group paragraph missing:\n%s", name, text)
		}
	}
}

func TestPlanChoosesTheBestSolutionAndSaysWhy(t *testing.T) {
	r := New(t.TempDir())
	for _, vars := range []map[string]any{fullVars(), with("ItemGate", true)} {
		text := render(t, r, "plan", vars)
		for _, want := range []string{
			"that is a proposal, not an obligation",
			"- `## Why this approach` —",
			"a file outside the phase's `Files:` is justified under `## Why this approach`",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("plan (ItemGate %v) lacks %q", vars["ItemGate"], want)
			}
		}
		if strings.Contains(text, "## The watchdog's triage found") {
			t.Errorf("triage block without TriageNotes:\n%s", text)
		}
	}

	notes := "#3: no lock — approach: take the lock in Save"
	if text := render(t, r, "plan", with("TriageNotes", notes)); !strings.Contains(text, "## The watchdog's triage found\n\n"+notes+"\n") {
		t.Errorf("plan lacks the triage notes:\n%s", text)
	}
	if text := render(t, r, "plan", with("ItemGate", true)); !strings.Contains(text, "a defect the code does not have") {
		t.Errorf("plan not-work misses a false defect:\n%s", text)
	}
}

func TestStepsAfterThePlanFollowItsApproach(t *testing.T) {
	r := New(t.TempDir())
	for _, c := range []struct{ name, want string }{
		{"review-plan", "**Approach** — for a `status: planned` plan: `## Why this approach` is missing"},
		{"review-plan", "**Missing** — an open item's outcome,"},
		{"implement", "Where its `## Why this approach` replaces a means the phase block proposes, follow the plan."},
		{"review", "A change that reaches an item's outcome another way is not a finding for that reason alone"},
	} {
		if text := render(t, r, c.name, fullVars()); !strings.Contains(text, c.want) {
			t.Errorf("%s lacks %q:\n%s", c.name, c.want, text)
		}
	}
}

func TestReviewersProveOutcomesNotProposedMeans(t *testing.T) {
	r := New(t.TempDir())
	const want = "A criterion that prescribes a means — a type, a signature, a mechanism, a file — is proved by a test of the outcome it serves, not of the means. The spec's invariants and ADRs outrank an item's proposed means: never report a change or a plan for avoiding a means they rule out."
	for _, name := range []string{"review", "review-plan"} {
		if text := render(t, r, name, with("ItemGate", true)); !strings.Contains(text, want) {
			t.Errorf("%s lacks the outcome rule:\n%s", name, text)
		}
		if strings.Contains(render(t, r, name, fullVars()), want) {
			t.Errorf("%s carries the criteria rule without ItemGate", name)
		}
	}
}

func TestWatchdogTriageJudgesWhetherAnItemIsReal(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"Is the claimed defect real?",
		"`not-a-bug` — the code already behaves as the item wants",
		"`not-relevant` —",
		"A `stale`, `duplicate`, `not-a-bug` or `not-relevant` skip cites a `path:line` that exists",
		"`approach` — for a fix, the change you judge best",
		"justifies under `## Why this approach` is expected",
		"Judge the outcome the item wants, never the fix it proposes",
		"is a `fix` with your `approach`, not a skip",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q", want)
		}
	}
}

func TestWatchdogTriageFlagsThePhasesThatNeedADesign(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"### Design",
		"When the request says the pipeline has a design step",
		"`design` — `true` when the phase changes what a user sees or interacts with",
		"Judge the rendered result, not the file kind",
		"A phase meant to look and behave as it does today is `false`",
		"`design_note` — for `true`, the screen or view that changes",
		"submit_gate(decision, drop?, split?, merge?, design_on?, design_off?, maintainer_said)",
		"`design_on` or `design_off` (phase or group ids)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q", want)
		}
	}
}

func TestWatchdogTriagesTheRunListAndAsksAtTheGate(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"## Triage",
		"`triage plan <plan> phases <ids>.`",
		"`triage backlog <plan> items <ids>.`",
		"`already-done` — the tree already does what the block asks. The `note` cites a `path:line`",
		"Never put `cosmetic` and `deep` in one group",
		"Put every fix item in exactly one group",
		"submit_triage(phases?, items?, groups?)",
		"submit_gate(decision, drop?, split?, merge?, design_on?, design_off?, maintainer_said)",
		"When `submit_triage` is accepted, end your turn. Never print or summarise the table yourself: the driver types it into this pane for the maintainer.",
		"When the table arrives, ask the maintainer",
		"go, drop, split, merge and abort",
		"the driver types the new table into this pane: end your turn and ask again when it arrives",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q", want)
		}
	}

	unattended := render(t, New(t.TempDir()), "watchdog", with("Unattended", true))
	if strings.Contains(unattended, "When the request says to ask") || !strings.Contains(unattended, "The run starts when `submit_triage` is accepted") {
		t.Errorf("unattended triage:\n%s", unattended)
	}
}

func TestPromptsThatWriteTestsCarryTheTestRules(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range []string{"plan", "implement", "fix", "review", "review-plan"} {
		t.Run(name, func(t *testing.T) {
			actual := render(t, r, name, fullVars())

			for _, want := range []string{"## Writing tests", "`// given`", "hard-coded expected values", "true I/O boundaries", "poll for the condition with a deadline"} {
				if !strings.Contains(actual, want) {
					t.Errorf("%s prompt missing %q", name, want)
				}
			}
		})
	}
}

func TestPromptsThatWriteNoTestsCarryNoTestRules(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range []string{"design", "review-ui", "gate", "gatefix", "milestone", "watchdog", "intake"} {
		t.Run(name, func(t *testing.T) {
			vars := fullVars()
			vars["Text"], vars["Dir"], vars["Root"], vars["Usage"] = "phase 3", "/repo", "/repo", ""
			actual := render(t, r, name, vars)

			if strings.Contains(actual, "## Writing tests") {
				t.Errorf("%s prompt carries the test rules", name)
			}
		})
	}
}

func TestStepPromptsTellTheAgentToRunBuildsAndTestsInTheForeground(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range []string{"plan", "implement", "fix", "gatefix"} {
		t.Run(name, func(t *testing.T) {
			// given
			vars := fullVars()

			// when
			actual := render(t, r, name, vars)

			// then
			for _, expected := range []string{"Run builds and tests in the foreground", "never end your turn while background work you started is still running"} {
				if !strings.Contains(actual, expected) {
					t.Errorf("%s prompt missing %q", name, expected)
				}
			}
		})
	}
}

func TestTheForegroundGuardStillLetsAReviewerWaitForItsForkedCommand(t *testing.T) {
	// given
	r := New(t.TempDir())

	// when
	actual := render(t, r, "review", fullVars())

	// then
	for _, expected := range []string{"The one wait allowed is for a task notification this prompt tells you to wait for.", "A slash command that forks into the background reports through a task notification: wait for it"} {
		if !strings.Contains(actual, expected) {
			t.Errorf("review prompt missing %q", expected)
		}
	}
}
func TestOverrideCanUseTheTestsPartial(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".r-loop", "prompts", "implement.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`custom implement{{template "tests" .}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	actual := render(t, New(dir), "implement", fullVars())

	if !strings.Contains(actual, "## Writing tests") {
		t.Errorf("override did not render the test rules:\n%s", actual)
	}
}

func TestReviewersReportTestsThatBreakTheRules(t *testing.T) {
	r := New(t.TempDir())
	for _, name := range []string{"review", "review-plan"} {
		t.Run(name, func(t *testing.T) {
			actual := render(t, r, name, fullVars())

			if !strings.Contains(actual, "breaks a rule under `## Writing tests` below as a finding, naming the rule") {
				t.Errorf("%s prompt does not ask to report rule-breaking tests:\n%s", name, actual)
			}
		})
	}
}

func TestFixRanksAWeakRuleBreakingTestAsP2(t *testing.T) {
	actual := render(t, New(t.TempDir()), "fix", fullVars())

	if !strings.Contains(actual, "is `P2` when it would still pass with the behaviour it names broken, and `P3` otherwise") {
		t.Errorf("fix prompt does not rank rule-breaking tests:\n%s", actual)
	}
}

func TestWatchdogCatchesUpWithRunStatusAndStepInfo(t *testing.T) {
	text := render(t, New(t.TempDir()), "watchdog", fullVars())

	for _, want := range []string{
		"`run_status()` — read the run as the driver holds it now",
		"`step_info(step)` — read a started step",
		"your next call of any other tool except `run_status` and `step_info` marks the wait over",
		"When you have lost track of a step or an open id",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("watchdog missing %q", want)
		}
	}
}

func TestSecurityReviewReadsTheUntrackedFilesAndNeedsAnAttackPath(t *testing.T) {
	got := render(t, New(t.TempDir()), "review-security", fullVars())

	for _, want := range []string{
		"`git status --porcelain` and `git diff HEAD`",
		"Read every untracked file in full",
		"a concrete path from an attacker-controlled input to the sink",
		"Data the operator supplies",
		"is trusted",
		"P1 exploitable as shipped",
		"Change no file in",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"ReviewCommand", "native-review.txt", "never review by hand"} {
		if strings.Contains(got, gone) {
			t.Errorf("prompt still has %q", gone)
		}
	}
}

func TestFixExplainsThatStaticFindingsComeFromAnalyzers(t *testing.T) {
	// given
	vars := with("FindingsFiles", []core.FindingsFile{{Reviewer: "static", Path: "/runs/r1/phase-7/implement-findings-static-r1.json"}})

	// when
	actual := render(t, New(t.TempDir()), "fix", vars)

	// then
	if !strings.Contains(actual, "`static`'s findings come from static analyzers the driver ran on the change") {
		t.Errorf("fix prompt does not name the analyzers:\n%s", actual)
	}
	if !strings.Contains(actual, "A rule hit is not a defect by itself") {
		t.Errorf("fix prompt does not discount a rule hit:\n%s", actual)
	}
	if !strings.Contains(actual, "A false positive is `not-real`, with the `path:line` that shows it as its evidence") {
		t.Errorf("fix prompt does not ask for evidence on a false positive:\n%s", actual)
	}
}

func TestFixWithoutAStaticFileLeavesTheAnalyzerNoteOut(t *testing.T) {
	// when
	actual := render(t, New(t.TempDir()), "fix", fullVars())

	// then
	if strings.Contains(actual, "A rule hit is not a defect by itself") {
		t.Errorf("fix prompt names the analyzers:\n%s", actual)
	}
}

func TestDesignPromptLoadsTheRubricAndWritesOnlyTheDesign(t *testing.T) {
	text := render(t, New(t.TempDir()), "design", with("DesignNote", "the store screen gains an empty state"))

	for _, want := range []string{
		"# Design phase 7 — PromptRenderer",
		"Write the design to `.task-plans/phase-7.design.md` and touch no other file",
		"The watchdog flagged this phase for design: the store screen gains an empty state",
		"Load the `frontend-design` skill with the Skill tool first",
		"write a `failed` sentinel whose reason is `frontend-design is not available`",
		"`DESIGN.md`",
		"Never start a second visual language",
		"For a terminal UI",
		"`## States` and `## Reuse map` are never left out",
		"`## States` — what each changed screen shows for default, empty, loading, error, success and no permission",
		"`## Reuse map` — each existing component",
		"\"Do not invent\" list",
		"`## Decisions`",
		"DESIGN-INTENT: <at most two sentences",
		"status: no-visual-change",
		"## Evidence",
		"never how the code is structured",
		"## Naming the commit",
		"ask_watchdog",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("design prompt missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "## Writing tests") {
		t.Errorf("the design prompt carries the test rules")
	}
	if strings.Contains(render(t, New(t.TempDir()), "design", fullVars()), "flagged this phase") {
		t.Errorf("design prompt names a flag without a note")
	}
}

func TestPlanAndImplementBuildTheDesignOnlyWhenThereIsOne(t *testing.T) {
	r := New(t.TempDir())
	vars := with("Design", ".task-plans/phase-7.design.md")

	plan := render(t, r, "plan", vars)
	implement := render(t, r, "implement", vars)

	for _, want := range []string{"## The phase's design", "decided what the user sees in `.task-plans/phase-7.design.md`", "Do not decide them again", "Take its reuse map into `## Changes`"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan missing %q", want)
		}
	}
	if !strings.Contains(implement, "The design at `.task-plans/phase-7.design.md` is the UI spec the plan builds") {
		t.Errorf("implement lacks the design:\n%s", implement)
	}
	for _, name := range []string{"plan", "implement"} {
		if strings.Contains(render(t, r, name, fullVars()), "design at") || strings.Contains(render(t, r, name, fullVars()), "## The phase's design") {
			t.Errorf("%s names a design without one", name)
		}
	}
}

func TestTheUIReviewJudgesAgainstTheDesignIntentOnlyWhenThereIsOne(t *testing.T) {
	r := New(t.TempDir())
	vars := with("DesignIntent", "the list reads calm and dense.")
	vars["Design"] = ".task-plans/phase-7.design.md"

	text := render(t, r, "review-ui", vars)

	if !strings.Contains(text, "8. The phase's design (`.task-plans/phase-7.design.md`) set this intent: \"the list reads calm and dense.\". Judge the visual check against it") {
		t.Errorf("review-ui lacks the intent:\n%s", text)
	}
	if strings.Contains(render(t, r, "review-ui", fullVars()), "set this intent") {
		t.Errorf("review-ui names an intent without one")
	}
}

func TestIntakeNamesTheDesignStep(t *testing.T) {
	text := render(t, New(t.TempDir()), "intake", map[string]any{"Dir": "/repo", "Text": "x", "Usage": "u", "Root": "/repo", "Unattended": false})

	if !strings.Contains(text, "pipeline step (`design`, `plan`, `implement`, `milestone`, `gate`)") {
		t.Errorf("intake:\n%s", text)
	}
}

func TestDesignPromptIsOverridable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".r-loop", "prompts", "design.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("design {{.DesignPath}}"), 0o644); err != nil {
		t.Fatal(err)
	}

	text, source, err := New(dir).Render("design", fullVars())

	if err != nil || text != "design .task-plans/phase-7.design.md" || source != path {
		t.Errorf("text %q source %q err %v", text, source, err)
	}
}
