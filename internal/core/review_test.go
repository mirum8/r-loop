package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type reviewRig struct {
	*rig
	worker   *Session
	reviews  []map[string]any
	behave   func(vars map[string]any)
	resolved [][]string
	noReview map[string]bool
	fixes    []map[string]any
	onFix    func(vars map[string]any)
}

func newReviewRig(t *testing.T, reviewers ...Reviewer) *reviewRig {
	r := &reviewRig{rig: newRig(t), noReview: map[string]bool{}}
	r.sm.Prompts = promptsFunc(func(name string, vars map[string]any) (string, string, error) {
		if name != "review" && name != "fix" {
			return "do phase 3", "embedded", nil
		}
		copied := make(map[string]any, len(vars))
		for k, v := range vars {
			copied[k] = v
		}
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
	ref.Vars = map[string]any{"PhaseNumber": 3, "PhaseBlock": "### Phase 3"}
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
	provider := strings.TrimPrefix(vars["ReviewCommand"].(string), "/")
	provider = strings.TrimSuffix(provider, "-review")
	var items []string
	for i := 1; i <= findings; i++ {
		items = append(items, fmt.Sprintf(`{"id":"%s-r%d-%d","title":"t","detail":"d","files":["a.go"]}`, provider, vars["Round"], i))
	}
	body := fmt.Sprintf(`{"reviewer":%q,"findings":[%s]}`, provider, strings.Join(items, ","))
	if err := os.WriteFile(vars["FindingsPath"].(string), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sentinel := `{"outcome":"` + outcome + `","reason":"","at":"2026-09-18T10:05:00Z"}`
	if err := os.WriteFile(vars["Sentinel"].(string), []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReviewSplitsStartsThenPromptsAndReusesPanesWithFreshAgentsInRound2(t *testing.T) {
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
		"SessionHost.Interrupt rloop-p3-implement-rv-claude-r1",
		"SessionHost.Interrupt rloop-p3-implement-rv-codex-r1",
		"SessionHost.Start pane-2 rloop-p3-implement-rv-claude-r2 claude []",
		"SessionHost.Start pane-3 rloop-p3-implement-rv-codex-r2 codex []",
		`SessionHost.Prompt rloop-p3-implement-rv-claude-r2 "review r2" false 0s`,
		`SessionHost.Prompt rloop-p3-implement-rv-codex-r2 "review r2" false 0s`,
		"Store.Append run-1 event",
		"Store.Append run-1 event",
		"Repo.Snapshot " + wt,
		"Repo.TreeDiff tree-start tree-start",
		"Store.Append run-1 event",
	}
	got := r.callsFrom("Repo.Snapshot", "Repo.TreeDiff", "Store.Append", "SessionHost.Split", "SessionHost.Start", "SessionHost.Prompt", "SessionHost.Interrupt")
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
	if len(finds) != 1 || !reflect.DeepEqual(finds[0].Fields, map[string]string{"step": "implement", "round": "1", "reviewer": "codex", "state": "failed", "findings": "0"}) {
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
	want := map[string]string{"step": "implement", "round": "1", "reviewer": "claude", "state": "ok", "findings": "0"}
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
		os.WriteFile(filepath.Join(r.runDir, "phase-3", "implement-a2.sentinel"), []byte(`{"outcome":"ok","reason":"","at":"2026-09-18T10:05:00Z"}`), 0o644)
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
		os.WriteFile(vars["Sentinel"].(string), []byte(`{"outcome":"ok","reason":"","at":"2026-09-18T10:05:00Z"}`), 0o644)
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
	if len(rv) != 1 || rv[0].Step != "implement-rv-codex" || rv[0].Phase != 3 || rv[0].Fields["provider"] != "codex" {
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
	if len(nudges) != 1 || strings.Contains(nudges[0], "ask_user") {
		t.Fatalf("nudges = %q", nudges)
	}
}

func TestReviewerWithAskUserIsNudgedToCallIt(t *testing.T) {
	r := newReviewRig(t, Reviewer{Provider: "claude"})
	r.sm.Ask = &fakeAskChannel{callLog: callLog{Shared: r.shared}, BaseURL: "http://127.0.0.1:7000/mcp/tok"}
	r.sm.Resolve = askingReviewResolve(r)
	r.sm.Host = &stateHost{scriptedHost: r.host, states: map[string]AgentState{"rloop-p3-implement-rv-claude-r1": AgentIdle}}

	r.run()

	nudges := r.callsFrom(`SessionHost.Prompt rloop-p3-implement-rv-claude-r1 "r-loop: no sentinel`)
	if len(nudges) != 1 || !strings.Contains(nudges[0], "ask_user") {
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
