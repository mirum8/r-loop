package core

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type reviewRig struct {
	*rig
	worker    *Session
	reviews   []map[string]any
	behave    func(vars map[string]any)
	resolved  [][]string
	noReview  map[string]bool
	reviewCmd map[string]string
	fixes     []map[string]any
	onFix     func(vars map[string]any)
}

func newReviewRig(t *testing.T, reviewers ...Reviewer) *reviewRig {
	r := &reviewRig{rig: newRig(t), noReview: map[string]bool{}, reviewCmd: map[string]string{}}
	r.sm.Prompts = promptsFunc(func(name string, vars map[string]any) (string, string, error) {
		if name != "review" && name != "review-ui" && name != "fix" {
			return "do phase 3", "embedded", nil
		}
		copied := make(map[string]any, len(vars)+1)
		for k, v := range vars {
			copied[k] = v
		}
		copied["prompt"] = name
		if name == "fix" {
			r.fixes = append(r.fixes, copied)
			if r.onFix != nil {
				r.onFix(copied)
			}
			return fmt.Sprintf("fix r%d", vars["Round"]), "embedded", nil
		}
		r.reviews = append(r.reviews, copied)
		if r.behave != nil {
			r.behave(copied)
		}
		return fmt.Sprintf("review r%d", vars["Round"]), "embedded", nil
	})
	r.sm.Resolve = func(provider, model, effort, askURL, mcpConfigPath string) (ProviderArgs, error) {
		r.resolved = append(r.resolved, []string{provider, model, effort})
		review := "/" + provider + "-review"
		if r.noReview[provider] {
			review = ""
		}
		if cmd := r.reviewCmd[provider]; cmd != "" {
			review = cmd
		}
		var args []string
		if model != "" {
			args = append(args, "--model", model)
		}
		if effort != "" {
			args = append(args, "--effort", effort)
		}
		return ProviderArgs{Kind: provider, Args: args, Review: review}, nil
	}
	ref := r.ref(1)
	ref.Kind.Row.Reviewers = reviewers
	ref.Kind.Row.Rounds = 2
	ref.Kind.Row.ReviewTimeout = time.Hour
	ref.Vars = map[string]any{"PhaseNumber": "3", "PhaseBlock": "### Phase 3"}
	s, err := r.sm.Spawn(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	r.worker = s
	r.resolved = nil
	r.host.script = func(int) AgentState { return AgentWorking }
	return r
}

func (r *reviewRig) run() Outcome {
	h := ReviewHalf{Sessions: r.sm, Store: r.store}
	return h.Run(context.Background(), r.worker.Ref, r.worker, &recObserver{})
}

func (r *reviewRig) callsFrom(prefixes ...string) []string {
	var out []string
	for _, c := range r.shared.Calls() {
		for _, p := range prefixes {
			if strings.HasPrefix(c, p) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func writeReview(t *testing.T, vars map[string]any, outcome string, findings int) {
	t.Helper()
	if vars["prompt"] == "review" {
		if err := os.MkdirAll(vars["ArtifactsDir"].(string), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(vars["ArtifactsDir"].(string), "native-review.txt"), []byte("native review output\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFindings(t, vars, outcome, findings)
}

func writeFindings(t *testing.T, vars map[string]any, outcome string, findings int) {
	t.Helper()
	provider := findingsName.FindStringSubmatch(filepath.Base(vars["FindingsPath"].(string)))[1]
	var items []string
	for i := 1; i <= findings; i++ {
		items = append(items, fmt.Sprintf(`{"id":"%s-r%d-%d","title":"t","detail":"d","files":["a.go"]}`, provider, vars["Round"], i))
	}
	body := fmt.Sprintf(`{"reviewer":%q,"findings":[%s]}`, provider, strings.Join(items, ","))
	if err := os.WriteFile(vars["FindingsPath"].(string), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sentinel := `{"outcome":"` + outcome + `","reason":""}`
	if err := os.WriteFile(vars["Sentinel"].(string), []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNativeReviewCommandPointsAtTheOutputFileInItsArtifactsDir(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"}, Reviewer{Provider: "claude"})
	r.reviewCmd["codex"] = "codex exec review --uncommitted -o {output}"
	r.behave = func(vars map[string]any) {
		if _, err := os.Stat(vars["ArtifactsDir"].(string)); err != nil {
			t.Fatal(err)
		}
		writeReview(t, vars, "ok", 0)
	}
	if out := r.run(); out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	want := "codex exec review --uncommitted -o '" + filepath.Join(r.runDir, "phase-3", "implement-rv-codex-r1-a1", "native-review.txt") + "'"
	if got := r.reviews[0]["ReviewCommand"]; got != want {
		t.Fatalf("command = %q, want %q", got, want)
	}
	if got := r.reviews[1]["ReviewCommand"]; got != "/claude-review" {
		t.Fatalf("claude command = %q", got)
	}
}

func TestNativeReviewerWithoutOutputFailsTheStepNamingTheCommand(t *testing.T) {
	for _, tc := range []string{"missing", "blank"} {
		t.Run(tc, func(t *testing.T) {
			r := newReviewRig(t, Reviewer{Provider: "codex"})
			r.behave = func(vars map[string]any) {
				if tc == "blank" {
					if err := os.WriteFile(filepath.Join(vars["ArtifactsDir"].(string), "native-review.txt"), []byte("  \n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				writeFindings(t, vars, "ok", 0)
			}
			out := r.run()
			if out.State != StepFailed || out.Reason != "reviewer codex: evidence missing: native review `/codex-review` produced no output" {
				t.Fatalf("outcome = %+v", out)
			}
			if f := r.events("review-find"); len(f) != 1 || f[0].Fields["state"] != "failed" {
				t.Fatalf("finds = %+v", f)
			}
			if len(r.events("review-clean")) != 0 || len(r.fixes) != 0 {
				t.Fatal("review was treated as clean")
			}
		})
	}
}

func TestClaudeReviewerWithoutOutputFailsTheSameWay(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"})
	r.behave = func(vars map[string]any) { writeFindings(t, vars, "ok", 0) }
	out := r.run()
	if out.State != StepFailed || out.Reason != "reviewer claude: evidence missing: native review `/claude-review` produced no output" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestReviewerThatCannotRunItsCommandFailsTheStep(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) {
		writeFindings(t, vars, "failed", 0)
		if err := os.WriteFile(vars["Sentinel"].(string), []byte(`{"outcome":"failed","reason":"/review is not available in this session"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := r.run()
	if out.State != StepFailed || out.Reason != "reviewer codex: /review is not available in this session" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(r.events("review-clean")) != 0 {
		t.Fatal("clean review")
	}
}

func TestShellQuoteKeepsAPathOneShellArgument(t *testing.T) {
	for _, p := range []string{"/runs/r1/x.txt", "/Users/me/Work Projects/r-loop/x.txt", "/tmp/it's here/x.txt"} {
		got, err := exec.Command("sh", "-c", "printf %s "+shellQuote(p)).Output()
		if err != nil || string(got) != p {
			t.Fatalf("path %q: got %q, err %v", p, got, err)
		}
	}
}

func TestARetriedAttemptCannotPassOnAnEarlierAttemptsNativeOutput(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.worker.Ref.Key.Attempt = 2
	old := filepath.Join(r.runDir, "phase-3", "implement-rv-codex-r1-a1")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "native-review.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.behave = func(vars map[string]any) { writeFindings(t, vars, "ok", 0) }
	out := r.run()
	if out.State != StepFailed || out.Reason != "reviewer codex: evidence missing: native review `/codex-review` produced no output" {
		t.Fatalf("outcome = %+v", out)
	}
	if got := r.reviews[0]["ArtifactsDir"]; got != filepath.Join(r.runDir, "phase-3", "implement-rv-codex-r1-a2") {
		t.Fatalf("artifacts = %q", got)
	}
}

func TestReviewFindNamesTheCommandEachReviewerRan(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"}, uiReviewer)
	r.worker.Dir = t.TempDir()
	writeSkill(t, r.worker.Dir)
	r.behave = func(vars map[string]any) {
		if vars["prompt"] == "review" {
			writeReview(t, vars, "ok", 0)
		} else {
			writeNamedReview(t, vars, 0)
		}
	}
	if out := r.run(); out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	f := r.events("review-find")
	if len(f) != 2 || f[0].Fields["command"] != "/claude-review" || f[1].Fields["command"] != "prompt review-ui" || len(r.events("review-clean")) != 1 {
		t.Fatalf("finds = %+v", f)
	}
}

func TestReviewSplitsStartsThenPromptsAndReplacesPanesWithFreshAgentsInRound2(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"}, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) {
		r.repo.TreeChanges = nil
		findings := 0
		if vars["Round"] == 1 {
			findings = 1
		}
		writeReview(t, vars, "ok", findings)
	}
	r.onFix = func(vars map[string]any) {
		r.repo.TreeChanges = []string{"a.go"}
		writeVerdict(t, vars, entry("claude-r1-1", "real", "P1", true, ""), entry("codex-r1-1", "real", "P2", true, ""))
	}

	out := r.run()

	if out.State != StepOK || out.Reason != "" {
		t.Fatalf("outcome = %+v", out)
	}
	wt := "/repo/.r-loop/wt/phase-3"
	want := []string{
		"Repo.Snapshot " + wt,
		"Store.Append run-1 event",
		"SessionHost.Split pane-1 right " + wt,
		"SessionHost.Split pane-2 down " + wt,
		"SessionHost.Start pane-2 rloop-p3-implement-rv-claude-r1 claude []",
		"SessionHost.Start pane-3 rloop-p3-implement-rv-codex-r1 codex []",
		`SessionHost.Prompt rloop-p3-implement-rv-claude-r1 "review r1" false 0s`,
		`SessionHost.Prompt rloop-p3-implement-rv-codex-r1 "review r1" false 0s`,
		"Store.Append run-1 event",
		"Store.Append run-1 event",
		"Repo.Snapshot " + wt,
		"Repo.TreeDiff tree-start tree-start",
		`SessionHost.Prompt rloop-p3-implement "fix r1" false 0s`,
		"Repo.Snapshot " + wt,
		"Repo.TreeDiff tree-start tree-start",
		"Repo.Snapshot " + wt,
		"Repo.TreeDiff tree-start tree-start",
		"Store.Append run-1 event",
		"Store.Append run-1 event",
		"Repo.Snapshot " + wt,
		"Store.Append run-1 event",
		"SessionHost.ClosePane pane-2",
		"SessionHost.ClosePane pane-3",
		"SessionHost.Split pane-1 right " + wt,
		"SessionHost.Split pane-4 down " + wt,
		"SessionHost.Start pane-4 rloop-p3-implement-rv-claude-r2 claude []",
		"SessionHost.Start pane-5 rloop-p3-implement-rv-codex-r2 codex []",
		`SessionHost.Prompt rloop-p3-implement-rv-claude-r2 "review r2" false 0s`,
		`SessionHost.Prompt rloop-p3-implement-rv-codex-r2 "review r2" false 0s`,
		"Store.Append run-1 event",
		"Store.Append run-1 event",
		"Repo.Snapshot " + wt,
		"Repo.TreeDiff tree-start tree-start",
		"Store.Append run-1 event",
	}
	got := r.callsFrom("Repo.Snapshot", "Repo.TreeDiff", "Store.Append", "SessionHost.Split", "SessionHost.Start", "SessionHost.Prompt", "SessionHost.ClosePane")
	got = got[len(got)-len(want):]
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls =\n%s", strings.Join(got, "\n"))
	}
	rounds := r.events("review-round")
	if len(rounds) != 2 || !reflect.DeepEqual(rounds[1].Fields, map[string]string{"step": "implement", "round": "2", "tree": "tree-start", "attempt": "1"}) {
		t.Fatalf("review-round events = %+v", rounds)
	}
	clean := r.events("review-clean")
	if len(clean) != 1 || !reflect.DeepEqual(clean[0].Fields, map[string]string{"step": "implement", "round": "2"}) {
		t.Fatalf("review-clean events = %+v", clean)
	}
	if got := r.worker.asker("implement-rv-codex"); got != "rloop-p3-implement-rv-codex-r2" {
		t.Errorf("a codex reviewer question goes to %q", got)
	}
}

func TestReviewPromptVariablesPerRound(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) {
		r.repo.TreeChanges = nil
		findings := 0
		if vars["Round"] == 1 {
			findings = 2
		}
		writeReview(t, vars, "ok", findings)
	}
	r.onFix = func(vars map[string]any) {
		r.repo.TreeChanges = []string{"a.go"}
		writeVerdict(t, vars, entry("codex-r1-1", "real", "P1", true, ""), entry("codex-r1-2", "out-of-scope", "P3", false, ""))
	}

	r.run()

	if len(r.reviews) != 2 {
		t.Fatalf("reviews = %d", len(r.reviews))
	}
	dir := filepath.Join(r.runDir, "phase-3")
	first, second := r.reviews[0], r.reviews[1]
	wantFirst := map[string]any{
		"ReviewedKind": "implement", "Round": 1, "Rounds": 2, "ReviewCommand": "/codex-review",
		"Sentinel":     filepath.Join(dir, "implement-rv-codex-r1-a1.sentinel"),
		"FindingsPath": filepath.Join(dir, "implement-findings-codex-r1.json"),
		"RoundTree":    "", "PriorFindings": "", "PriorVerdicts": "",
		"PhaseBlock": "### Phase 3",
	}
	for k, v := range wantFirst {
		if first[k] != v {
			t.Errorf("round 1 %s = %v, want %v", k, first[k], v)
		}
	}
	wantSecond := map[string]any{
		"Round":         2,
		"Sentinel":      filepath.Join(dir, "implement-rv-codex-r2-a1.sentinel"),
		"FindingsPath":  filepath.Join(dir, "implement-findings-codex-r2.json"),
		"RoundTree":     "tree-start",
		"PriorFindings": "- " + filepath.Join(dir, "implement-findings-codex-r1.json"),
		"PriorVerdicts": "- " + filepath.Join(dir, "implement-verdict-r1.json"),
	}
	for k, v := range wantSecond {
		if second[k] != v {
			t.Errorf("round 2 %s = %v, want %v", k, second[k], v)
		}
	}
}

func TestReviewerAgentNameIsTruncatedToFitKeepingTheRound(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude-enterprise"})
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }

	r.run()

	starts := r.callsFrom("SessionHost.Start pane-2")
	if len(starts) != 1 {
		t.Fatalf("starts = %q", starts)
	}
	name := strings.Fields(starts[0])[2]
	if len(name) > 32 || strings.Contains(name, "--") || !strings.HasSuffix(name, "-r1") || !strings.HasPrefix(name, "rloop-p3-implement-rv-claude") {
		t.Fatalf("name = %q", name)
	}
}

func TestReviewerWithNoReviewCommandFailsBeforeAnySplit(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"}, Reviewer{Provider: "aider"})
	r.noReview["aider"] = true

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer aider declares no native reviewer" {
		t.Fatalf("outcome = %+v", out)
	}
	if n := len(r.callsFrom("SessionHost.Split", "SessionHost.Start pane-2")) + len(r.events("review-round")); n != 0 {
		t.Fatalf("calls = %q", r.shared.Calls())
	}
}

func TestBlockReviewerPassesModelAndEffortScalarReviewerNone(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex", Model: "gpt-5.6-sol", Effort: "high"}, Reviewer{Provider: "claude"})
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }

	r.run()

	if !reflect.DeepEqual(r.resolved, [][]string{{"codex", "gpt-5.6-sol", "high"}, {"claude", "", ""}}) {
		t.Fatalf("resolved = %q", r.resolved)
	}
	starts := r.callsFrom("SessionHost.Start pane-")
	want := []string{
		"SessionHost.Start pane-2 rloop-p3-implement-rv-codex-r1 codex [--model gpt-5.6-sol --effort high]",
		"SessionHost.Start pane-3 rloop-p3-implement-rv-claude-r1 claude []",
	}
	if !reflect.DeepEqual(starts[1:], want) {
		t.Fatalf("starts = %q", starts)
	}
}

type splitFailHost struct {
	*scriptedHost
	failOn int
	splits int
}

func (h *splitFailHost) Split(pane, direction, cwd string) (string, error) {
	p, _ := h.scriptedHost.Split(pane, direction, cwd)
	h.splits++
	if h.splits == h.failOn {
		return "", fmt.Errorf("pane_not_found")
	}
	return p, nil
}

func TestSplitFailureStopsTheRestAndNamesTheReviewer(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"}, Reviewer{Provider: "codex"})
	r.sm.Host = &splitFailHost{scriptedHost: r.host, failOn: 2}

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer codex: pane_not_found" {
		t.Fatalf("outcome = %+v", out)
	}
	if n := len(r.callsFrom("SessionHost.Start pane-2", "SessionHost.Interrupt", "SessionHost.Close")); n != 0 {
		t.Fatalf("calls = %q", r.shared.Calls())
	}
}

func TestStartFailureNamesTheReviewer(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"})
	r.host.StartErr = fmt.Errorf("agent_start_failed")

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer claude: agent_start_failed" {
		t.Fatalf("outcome = %+v", out)
	}
	if n := len(r.callsFrom("SessionHost.Prompt rloop-p3-implement-rv")); n != 0 {
		t.Fatalf("calls = %q", r.shared.Calls())
	}
}

func TestStepAgentClocksFrozenWhileReviewersWork(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	var polls, frozen atomic.Int32
	r.host.script = func(int) AgentState {
		polls.Add(1)
		if r.worker.Reviewing.Load() {
			frozen.Add(1)
		}
		if polls.Load() == 3 {
			writeReview(t, r.reviews[0], "ok", 0)
		}
		return AgentWorking
	}

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	if polls.Load() != 3 || frozen.Load() != 3 {
		t.Fatalf("polls = %d, frozen = %d", polls.Load(), frozen.Load())
	}
	if r.worker.Reviewing.Load() {
		t.Fatal("Reviewing still true after the find half")
	}
}

func TestInvalidFindingsFileFailsTheStepNamingIt(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) {
		writeReview(t, vars, "ok", 0)
		os.WriteFile(vars["FindingsPath"].(string), []byte(`{"reviewer":`), 0o644)
	}

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer codex: evidence missing: implement-findings-codex-r1.json: unreadable" {
		t.Fatalf("outcome = %+v", out)
	}
	finds := r.events("review-find")
	if len(finds) != 1 || !reflect.DeepEqual(finds[0].Fields, map[string]string{"step": "implement", "round": "1", "reviewer": "codex", "state": "failed", "findings": "0", "command": "/codex-review"}) {
		t.Fatalf("review-find = %+v", finds)
	}
}

func TestReviewerThatEditsTheWorktreeFailsTheStep(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) {
		writeReview(t, vars, "ok", 1)
		r.repo.TreeChanges = []string{"a.go", "b.go"}
	}

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer modified the tree: a.go, b.go" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestOneReviewerStallsWhileTheOtherFinishes(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"}, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) {
		if vars["ReviewCommand"] == "/codex-review" {
			writeReview(t, vars, "ok", 1)
		}
	}
	r.sm.Host = &stateHost{scriptedHost: r.host, states: map[string]AgentState{"rloop-p3-implement-rv-claude-r1": AgentIdle}}

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer claude: stalled: no response to nudge" || !out.Stalled {
		t.Fatalf("outcome = %+v", out)
	}
	if n := len(r.callsFrom(`SessionHost.Prompt rloop-p3-implement-rv-claude-r1 "r-loop: no sentinel`)); n != 1 {
		t.Fatalf("nudges = %d", n)
	}
	finds := r.events("review-find")
	if len(finds) != 2 || finds[0].Fields["state"] != "failed" || finds[1].Fields["state"] != "ok" || finds[1].Fields["findings"] != "1" {
		t.Fatalf("review-find = %+v", finds)
	}
	for _, s := range r.steps() {
		if s == StepStalled {
			t.Fatalf("reviewer stall recorded as a step state: %v", r.steps())
		}
	}
}

type stateHost struct {
	*scriptedHost
	states map[string]AgentState
}

func (h *stateHost) State(agent string) (AgentState, error) {
	h.record("SessionHost.State %s", agent)
	if s, ok := h.states[agent]; ok {
		return s, nil
	}
	return AgentWorking, nil
}

func TestReviewerBackstopIsTheReviewTimeout(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.worker.Ref.Kind.Row.ReviewTimeout = 5 * time.Minute

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer codex: backstop 5m0s" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestZeroFindingsEndsTheHalfClean(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"}, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	if len(r.events("review-round")) != 1 || len(r.events("review-clean")) != 1 {
		t.Fatalf("events = %+v", r.store.Records["run-1"])
	}
	finds := r.events("review-find")
	want := map[string]string{"step": "implement", "round": "1", "reviewer": "claude", "state": "ok", "findings": "0", "command": "/claude-review"}
	if len(finds) != 2 || !reflect.DeepEqual(finds[0].Fields, want) {
		t.Fatalf("review-find = %+v", finds)
	}
	if r.count("Repo.CommitAll") != 0 {
		t.Fatal("review half committed")
	}
}

func TestEachReviewRoundIsReportedToTheObserver(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"})
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }
	obs := &recObserver{}

	ReviewHalf{Sessions: r.sm, Store: r.store}.Run(context.Background(), r.worker.Ref, r.worker, obs)

	if !reflect.DeepEqual(obs.rounds, []int{1}) {
		t.Fatalf("rounds %v", obs.rounds)
	}
}

func TestDefaultRunnersRunTheReviewHalfBeforeTheCommit(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) { writeReview(t, vars, "failed", 0) }
	ref := r.worker.Ref
	ref.Key.Attempt = 2
	r.repo.TreeChanges = []string{"a.go"}
	r.host.script = func(int) AgentState {
		os.WriteFile(filepath.Join(r.runDir, "phase-3", "implement-a2.sentinel"), []byte(`{"outcome":"ok","reason":""}`), 0o644)
		return AgentWorking
	}
	runner := DefaultRunners(r.sm, []StepKind{ref.Kind})["diff"]

	out := runner.Run(context.Background(), ref, &recObserver{})

	if out.State != StepFailed || !strings.HasPrefix(out.Reason, "reviewer codex: ") {
		t.Fatalf("outcome = %+v", out)
	}
	if len(r.reviews) != 1 || r.count("Repo.CommitAll") != 0 {
		t.Fatalf("reviews = %d, calls = %q", len(r.reviews), r.shared.Calls())
	}
}

func TestRetriedAttemptSuffixesReviewerNamesAndDropsStaleFindings(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.worker.Ref.Key.Attempt = 2
	stale := filepath.Join(r.runDir, "phase-3", "implement-findings-codex-r1.json")
	os.WriteFile(stale, []byte(`{"reviewer":"codex","findings":[]}`), 0o644)
	r.behave = func(vars map[string]any) {
		os.WriteFile(vars["Sentinel"].(string), []byte(`{"outcome":"ok","reason":""}`), 0o644)
	}

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer codex: evidence missing: no findings at implement-findings-codex-r1.json" {
		t.Fatalf("outcome = %+v", out)
	}
	if n := len(r.callsFrom("SessionHost.Start pane-2 rloop-p3-implement-rv-code-r1-a2 ")); n != 1 {
		t.Fatalf("calls = %q", r.shared.Calls())
	}
}

type failingEventStore struct {
	*fakeStore
	kind string
}

func (s failingEventStore) Append(runID string, rec Record) error {
	if rec.Kind == RecordEvent && rec.Event.Kind == s.kind {
		return fmt.Errorf("disk full")
	}
	return s.fakeStore.Append(runID, rec)
}

func TestReviewFindPersistenceFailureFailsTheStep(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }
	h := ReviewHalf{Sessions: r.sm, Store: failingEventStore{fakeStore: r.store, kind: "review-find"}}

	out := h.Run(context.Background(), r.worker.Ref, r.worker, &recObserver{})

	if out.State != StepFailed || out.Reason != "record: disk full" {
		t.Fatalf("outcome = %+v", out)
	}
}

func askingReviewResolve(r *reviewRig) func(provider, model, effort, askURL, mcpConfigPath string) (ProviderArgs, error) {
	return func(provider, model, effort, askURL, mcpConfigPath string) (ProviderArgs, error) {
		r.resolved = append(r.resolved, []string{provider, askURL, mcpConfigPath})
		var args []string
		if mcpConfigPath != "" {
			args = []string{"--mcp-config", mcpConfigPath}
		}
		return ProviderArgs{Kind: provider, Args: args, Review: "/" + provider + "-review", Ask: true}, nil
	}
}

func TestReviewerIsStartedWithItsOwnAskURLAndMCPConfig(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"})
	r.sm.Ask = &fakeAskChannel{callLog: callLog{Shared: r.shared}, BaseURL: "http://127.0.0.1:7000/mcp/tok"}
	r.sm.Resolve = askingReviewResolve(r)
	path := filepath.Join(r.runDir, "phase-3", "implement-rv-claude-a1.mcp.json")
	host := &configCheckingHost{scriptedHost: r.host, path: path}
	r.sm.Host = host
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	url := "http://127.0.0.1:7000/mcp/tok/run-1/3/implement-rv-claude/1"
	if got := r.count("AskChannel.StepURL run-1/3/implement-rv-claude/1"); got != 1 {
		t.Fatalf("calls =\n%s", strings.Join(r.shared.Calls(), "\n"))
	}
	if r.reviews[0]["AskURL"] != url {
		t.Fatalf("AskURL = %v", r.reviews[0]["AskURL"])
	}
	if !host.present {
		t.Fatal("the mcp config was not there when the reviewer started")
	}
	if got := r.count("SessionHost.Start pane-2 rloop-p3-implement-rv-claude-r1 claude [--mcp-config " + path + "]"); got != 1 {
		t.Fatalf("calls =\n%s", strings.Join(r.shared.Calls(), "\n"))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"mcpServers":{"r-loop":{"type":"http","url":"` + url + `"}}}`; string(data) != want {
		t.Fatalf("mcp config = %s", data)
	}
}

func TestAnAskNoneReviewerRecordsAskNoneOnceNamingTheReviewer(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.sm.Ask = &fakeAskChannel{callLog: callLog{Shared: r.shared}, BaseURL: "http://127.0.0.1:7000/mcp/tok"}
	r.behave = func(vars map[string]any) {
		r.repo.TreeChanges = nil
		findings := 0
		if vars["Round"] == 1 {
			findings = 1
		}
		writeReview(t, vars, "ok", findings)
	}
	r.onFix = func(vars map[string]any) {
		r.repo.TreeChanges = []string{"a.go"}
		writeVerdict(t, vars, entry("codex-r1-1", "real", "P1", true, ""))
	}

	out := r.run()

	if out.State != StepOK || len(r.reviews) != 2 {
		t.Fatalf("outcome = %+v, reviews = %d", out, len(r.reviews))
	}
	var rv []Event
	for _, e := range r.events("ask-none") {
		if e.Step != "implement" {
			rv = append(rv, e)
		}
	}
	if len(rv) != 1 || rv[0].Step != "implement-rv-codex" || rv[0].Phase != "3" || rv[0].Fields["provider"] != "codex" {
		t.Fatalf("ask-none events = %+v", r.events("ask-none"))
	}
	if _, ok := r.reviews[0]["AskURL"]; ok || r.count("AskChannel.StepURL") != 0 {
		t.Fatalf("AskURL = %v", r.reviews[0]["AskURL"])
	}
}

func TestReviewerWithoutAskUserIsNotNudgedToCallIt(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"})
	r.sm.Host = &stateHost{scriptedHost: r.host, states: map[string]AgentState{"rloop-p3-implement-rv-claude-r1": AgentIdle}}

	r.run()

	nudges := r.callsFrom(`SessionHost.Prompt rloop-p3-implement-rv-claude-r1 "r-loop: no sentinel`)
	if len(nudges) != 1 || strings.Contains(nudges[0], "ask_watchdog") {
		t.Fatalf("nudges = %q", nudges)
	}
}

func TestReviewerWithAskWatchdogIsNudgedToCallIt(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"})
	r.sm.Ask = &fakeAskChannel{callLog: callLog{Shared: r.shared}, BaseURL: "http://127.0.0.1:7000/mcp/tok"}
	r.sm.Resolve = askingReviewResolve(r)
	r.sm.Host = &stateHost{scriptedHost: r.host, states: map[string]AgentState{"rloop-p3-implement-rv-claude-r1": AgentIdle}}

	r.run()

	nudges := r.callsFrom(`SessionHost.Prompt rloop-p3-implement-rv-claude-r1 "r-loop: no sentinel`)
	if len(nudges) != 1 || !strings.Contains(nudges[0], "ask_watchdog") {
		t.Fatalf("nudges = %q", nudges)
	}
}

func TestReviewerClockHoldsWhileTheStepHasAnOpenQuestion(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.worker.Ref.Kind.Row.ReviewTimeout = 5 * time.Minute
	r.worker.OpenQuestion.Store(true)
	r.host.script = func(n int) AgentState {
		if n == 20 {
			writeReview(t, r.reviews[0], "ok", 0)
		}
		return AgentIdle
	}

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	if n := len(r.callsFrom(`SessionHost.Prompt rloop-p3-implement-rv-codex-r1 "r-loop: no sentinel`)); n != 0 {
		t.Fatalf("nudges = %d", n)
	}
}

type fixObserver struct {
	recObserver
	log *[]string
}

func (o *fixObserver) Reviewing(s *Session, round int) {
	*o.log = append(*o.log, fmt.Sprintf("reviewing r%d", round))
}

func (o *fixObserver) Fixing(s *Session, round int) {
	*o.log = append(*o.log, fmt.Sprintf("fixing r%d %s", round, s.Agent))
}

func TestARoundWithFindingsReportsTheFixHalfToTheObserverBeforeTheFixPrompt(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"})
	var log []string
	r.behave = func(vars map[string]any) {
		r.repo.TreeChanges = nil
		findings := 0
		if vars["Round"] == 1 {
			findings = 1
		}
		writeReview(t, vars, "ok", findings)
	}
	r.onFix = func(vars map[string]any) {
		log = append(log, fmt.Sprintf("fix prompt r%d", vars["Round"]))
		r.repo.TreeChanges = []string{"a.go"}
		writeVerdict(t, vars, entry("claude-r1-1", "real", "P1", true, ""))
	}

	out := ReviewHalf{Sessions: r.sm, Store: r.store}.Run(context.Background(), r.worker.Ref, r.worker, &fixObserver{log: &log})

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	want := []string{"reviewing r1", "fixing r1 " + r.worker.Agent, "fix prompt r1", "reviewing r2"}
	if !reflect.DeepEqual(log, want) {
		t.Fatalf("log %v, want %v", log, want)
	}
}

type occupiedPaneHost struct {
	*scriptedHost
	busy map[string]bool
}

func (h *occupiedPaneHost) Start(pane, name, kind string, args []string) (Agent, error) {
	if h.busy[pane] {
		return Agent{}, fmt.Errorf("agent_pane_busy: agent target pane %s is not an available shell", pane)
	}
	h.busy[pane] = true
	return h.scriptedHost.Start(pane, name, kind, args)
}

func (h *occupiedPaneHost) ClosePane(pane string) error {
	delete(h.busy, pane)
	return h.scriptedHost.ClosePane(pane)
}

func TestRound2StartsWhenTheRound1ReviewerIgnoresTheInterrupt(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"})
	r.sm.Host = &occupiedPaneHost{scriptedHost: r.host, busy: map[string]bool{}}
	r.behave = func(vars map[string]any) {
		r.repo.TreeChanges = nil
		findings := 0
		if vars["Round"] == 1 {
			findings = 1
		}
		writeReview(t, vars, "ok", findings)
	}
	r.onFix = func(vars map[string]any) {
		r.repo.TreeChanges = []string{"a.go"}
		writeVerdict(t, vars, entry("claude-r1-1", "real", "P2", true, ""))
	}

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	if got := r.callsFrom("SessionHost.Start pane-2", "SessionHost.Start pane-3", "SessionHost.ClosePane"); !reflect.DeepEqual(got, []string{
		"SessionHost.Start pane-2 rloop-p3-implement-rv-claude-r1 claude []",
		"SessionHost.ClosePane pane-2",
		"SessionHost.Start pane-3 rloop-p3-implement-rv-claude-r2 claude []",
	}) {
		t.Fatalf("calls = %q", got)
	}
}

var uiReviewer = Reviewer{Provider: "claude", Model: "opus", Effort: "high", Name: "ui", Prompt: "review-ui", Requires: ".claude/skills/test-app/SKILL.md"}

func writeSkill(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, ".claude", "skills", "test-app", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# test-app\n<!-- test-app-surface: web -->\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeNamedReview(t *testing.T, vars map[string]any, findings int) {
	t.Helper()
	writeReview(t, vars, "ok", findings)
}

func TestReviewerWhoseRequiredFileIsMissingIsSkippedAndRecorded(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"}, uiReviewer)
	r.worker.Dir = t.TempDir()
	r.repo.RootDir = t.TempDir()
	r.behave = func(vars map[string]any) { writeNamedReview(t, vars, 0) }

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	if len(r.reviews) != 1 || r.reviews[0]["prompt"] != "review" {
		t.Fatalf("reviews = %+v", r.reviews)
	}
	if n := len(r.callsFrom("SessionHost.Split")); n != 1 {
		t.Fatalf("splits = %d", n)
	}
	skipped := r.events("reviewer-skipped")
	want := map[string]string{"step": "implement", "reviewer": "ui", "reason": "reviewer ui: no .claude/skills/test-app/SKILL.md"}
	if len(skipped) != 1 || !reflect.DeepEqual(skipped[0].Fields, want) {
		t.Fatalf("reviewer-skipped = %+v", skipped)
	}
}

func TestOnlyReviewerSkippedEndsTheHalfWithoutARound(t *testing.T) {
	r := newReviewRig(t, uiReviewer)
	r.worker.Dir = t.TempDir()
	r.repo.RootDir = t.TempDir()

	out := r.run()

	if out.State != StepOK || len(r.events("review-round")) != 0 || r.count("SessionHost.Split") != 0 {
		t.Fatalf("outcome = %+v, calls = %q", out, r.shared.Calls())
	}
}

func TestNamedUIReviewerRunsBesideTheSameProviderWithItsOwnPromptAndFiles(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"}, uiReviewer)
	r.worker.Dir = t.TempDir()
	skill := writeSkill(t, r.worker.Dir)
	r.behave = func(vars map[string]any) { writeNamedReview(t, vars, 0) }

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	starts := r.callsFrom("SessionHost.Start pane-")[1:]
	want := []string{
		"SessionHost.Start pane-2 rloop-p3-implement-rv-claude-r1 claude []",
		"SessionHost.Start pane-3 rloop-p3-implement-rv-ui-r1 claude [--model opus --effort high]",
	}
	if !reflect.DeepEqual(starts, want) {
		t.Fatalf("starts = %q", starts)
	}
	dir := filepath.Join(r.runDir, "phase-3")
	ui := r.reviews[1]
	for k, v := range map[string]any{
		"prompt":       "review-ui",
		"FindingsPath": filepath.Join(dir, "implement-findings-ui-r1.json"),
		"Sentinel":     filepath.Join(dir, "implement-rv-ui-r1-a1.sentinel"),
		"ArtifactsDir": filepath.Join(dir, "implement-rv-ui-r1-a1"),
		"RequiredPath": skill,
	} {
		if ui[k] != v {
			t.Errorf("%s = %v, want %v", k, ui[k], v)
		}
	}
	finds := r.events("review-find")
	if len(finds) != 2 || finds[1].Fields["reviewer"] != "ui" {
		t.Fatalf("review-find = %+v", finds)
	}
}

func TestUIReviewerFindsTheSkillInThePrimaryTreeAndNeedsNoNativeReviewCommand(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"}, Reviewer{Provider: "aider", Name: "ui", Prompt: "review-ui", Requires: uiReviewer.Requires})
	r.noReview["aider"] = true
	r.worker.Dir = t.TempDir()
	r.repo.RootDir = t.TempDir()
	skill := writeSkill(t, r.repo.RootDir)
	r.behave = func(vars map[string]any) {
		if vars["prompt"] == "review" {
			writeReview(t, vars, "ok", 0)
			return
		}
		writeNamedReview(t, vars, 0)
	}

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	if len(r.reviews) != 2 || r.reviews[1]["RequiredPath"] != skill {
		t.Fatalf("reviews = %+v", r.reviews)
	}
}

func TestUIFindingGoesToTheFixHalfAndReviewsAgainNextRound(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"}, uiReviewer)
	r.worker.Dir = t.TempDir()
	writeSkill(t, r.worker.Dir)
	r.behave = func(vars map[string]any) {
		r.repo.TreeChanges = nil
		findings := 0
		if vars["Round"] == 1 && vars["prompt"] == "review-ui" {
			findings = 1
		}
		writeNamedReview(t, vars, findings)
	}
	r.onFix = func(vars map[string]any) {
		r.repo.TreeChanges = []string{"a.go"}
		writeVerdict(t, vars, entry("ui-r1-1", "real", "P1", true, ""))
	}

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	dir := filepath.Join(r.runDir, "phase-3")
	files := r.fixes[0]["FindingsFiles"].([]FindingsFile)
	if len(files) != 2 || files[1] != (FindingsFile{Reviewer: "ui", Path: filepath.Join(dir, "implement-findings-ui-r1.json")}) {
		t.Fatalf("fix files = %+v", files)
	}
	if len(r.reviews) != 4 {
		t.Fatalf("reviews = %d", len(r.reviews))
	}
	prior := "- " + filepath.Join(dir, "implement-findings-claude-r1.json") + "\n- " + filepath.Join(dir, "implement-findings-ui-r1.json")
	if r.reviews[3]["prompt"] != "review-ui" || r.reviews[3]["PriorFindings"] != prior {
		t.Fatalf("round 2 ui vars = %+v", r.reviews[3])
	}
	fixed := r.events("finding")
	if len(fixed) != 1 || fixed[0].Fields["reviewer"] != "ui" || fixed[0].Fields["fixed"] != "true" {
		t.Fatalf("finding = %+v", fixed)
	}
}

func TestResumedReviewNamesEarlierFindingsByReviewerName(t *testing.T) {
	r := newReviewRig(t, uiReviewer)
	r.worker.Dir = t.TempDir()
	writeSkill(t, r.worker.Dir)
	r.worker.Ref.ReviewFrom = 2
	r.worker.Ref.PrevRoundTree = "tree-r1"
	r.behave = func(vars map[string]any) { writeNamedReview(t, vars, 0) }

	r.run()

	dir := filepath.Join(r.runDir, "phase-3")
	if len(r.reviews) != 1 || r.reviews[0]["PriorFindings"] != "- "+filepath.Join(dir, "implement-findings-ui-r1.json") {
		t.Fatalf("reviews = %+v", r.reviews)
	}
}

func TestALabelledRunNamesTheReviewerWithTheLabel(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.sm.Label = "test"
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }

	r.run()

	if n := len(r.callsFrom("SessionHost.Start pane-2 rloop-test-p3-implement-rv-co-r1 codex")); n != 1 {
		t.Fatalf("starts = %q", r.callsFrom("SessionHost.Start"))
	}
}
