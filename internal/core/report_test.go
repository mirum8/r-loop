package core

import (
	"strings"
	"testing"
)

func TestReportListsEachReviewerAndTheCommandItRan(t *testing.T) {
	st := RunState{ID: "run-1", Events: []Event{
		{Kind: "review-find", Phase: "1", Fields: map[string]string{"step": "plan", "round": "1", "reviewer": "codex", "command": "codex exec review --uncommitted -o /x", "state": "ok", "findings": "2"}},
		{Kind: "review-find", Phase: "1", Fields: map[string]string{"step": "plan", "round": "1", "reviewer": "claude", "command": "/code-review", "state": "failed", "findings": "0"}},
	}}
	want := "## Reviews\n\n- phase 1 plan r1 codex: ran `codex exec review --uncommitted -o /x` — ok, 2 findings\n- phase 1 plan r1 claude: ran `/code-review` — failed, 0 findings\n"
	if got := Report(st, threePhasePlan()); !strings.Contains(got, want) {
		t.Fatalf("report missing reviews:\n%s", got)
	}
}

func TestReportDoesNotClaimAnUnknownHistoricalReviewCommand(t *testing.T) {
	st := RunState{ID: "run-1", Events: []Event{{Kind: "review-find", Phase: "1", Fields: map[string]string{"step": "plan", "round": "1", "reviewer": "codex", "state": "ok", "findings": "0"}}}}
	got := Report(st, threePhasePlan())
	if strings.Contains(got, "ran ``") || !strings.Contains(got, "phase 1 plan r1 codex: ok, 0 findings") {
		t.Fatalf("report = %q", got)
	}
}

func TestReportMarksDroppedSignalsWithoutMarkingDeliveredSignals(t *testing.T) {
	key := StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}
	st := RunState{ID: "run-1", Signals: []Signal{
		{Seq: 64, Kind: SignalWarn, Source: SourceWatchdog, Step: key, Reason: "first"},
		{Seq: 65, Kind: SignalWarn, Source: SourceWatchdog, Step: key, Reason: "overflow"},
	}, Events: []Event{{Kind: "signal-dropped", Phase: "2", Step: "implement", Fields: map[string]string{"seq": "65", "kind": "warn", "source": "watchdog", "reason": "overflow"}}}}
	got := Report(st, threePhasePlan())
	if !strings.Contains(got, "warn from watchdog, phase 2 implement: overflow (dropped: signal queue full)") {
		t.Errorf("dropped signal not marked:\n%s", got)
	}
	if strings.Contains(got, "first (dropped") {
		t.Errorf("delivered signal marked dropped:\n%s", got)
	}
}

func TestReportNamesADroppedRejectionNotice(t *testing.T) {
	key := StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}
	st := RunState{ID: "run-1", Signals: []Signal{{Seq: 1, Kind: SignalWarn, Source: SourceWatchdog, Step: key, Reason: "late", Rejected: true, RejectReason: "step is not running"}}, Events: []Event{{Kind: "signal-dropped", Fields: map[string]string{"seq": "1"}}}}
	got := Report(st, threePhasePlan())
	if !strings.Contains(got, "(rejected: step is not running) (rejection notice dropped: signal queue full)") {
		t.Fatalf("report does not identify the dropped notice:\n%s", got)
	}
}

func TestReportCountsAndListsAResolveFirstAnswer(t *testing.T) {
	st := RunState{
		ID: "run-1", Status: RunFinished,
		Events: []Event{
			{Kind: "human", Step: "resolve first", Fields: map[string]string{"what": "answer", "id": "r1", "by": "maintainer", "entry": "Pick the database", "answer": "Postgres"}},
		},
		Questions: []Question{
			{ID: "q1", Step: StepKey{Phase: "1", Kind: "implement"}, Text: "keep api?", Answer: "yes", AnsweredBy: "maintainer"},
		},
	}

	rep := Report(st, threePhasePlan())

	for _, want := range []string{
		"human touches: 1\n",
		"## Questions\n\n" +
			"- r1 resolve first: Pick the database → Postgres (maintainer)\n" +
			"- q1 phase 1 implement: keep api? → yes (maintainer, waited 0s)\n",
	} {
		if !strings.Contains(rep, want) {
			t.Errorf("report lacks %q:\n%s", want, rep)
		}
	}
}

func TestReportListsEachDialogWithItsKeysAndWhoAuthorisedThem(t *testing.T) {
	key := StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}
	st := RunState{ID: "run-1", Questions: []Question{
		{ID: "d1", Kind: QuestionDialog, Step: key, Text: "Allow write?\n1. Yes", Answer: "1 enter", AnsweredBy: "watchdog", Citation: "approve writes under the run folder"},
		{ID: "d2", Kind: QuestionDialog, Step: StepKey{Phase: "2", Kind: "implement-rv-codex"}, Text: "Run tests?", Answer: "2", AnsweredBy: "maintainer"},
		{ID: "d3", Kind: QuestionDialog, Step: key, Text: "Allow network?", Answer: "dialog closed in the pane", AnsweredBy: "withdrawn"},
		{ID: "d4", Kind: QuestionDialog, Step: key, Text: "Allow network?"},
	}}

	rep := Report(st, threePhasePlan())

	want := "## Questions\n\n" +
		"- d1 phase-2/implement: dialog → 1 enter (watchdog, approve writes under the run folder)\n" +
		"- d2 phase-2/implement-rv-codex: dialog → 2 (maintainer)\n" +
		"- d3 phase-2/implement: dialog → dialog closed in the pane (withdrawn)\n" +
		"- d4 phase-2/implement: dialog (open)\n"
	if !strings.Contains(rep, want) {
		t.Errorf("report lacks %q:\n%s", want, rep)
	}
	if strings.Contains(rep, "Allow write?") {
		t.Errorf("report prints the screen:\n%s", rep)
	}
}

func TestReportListsEachBlockerWithItsActionAndWhoChoseIt(t *testing.T) {
	key := StepKey{Run: "run-1", Phase: "2", Kind: "implement-rv-codex", Attempt: 1}
	text := "blocker b1 from phase-2/implement-rv-codex (reviewer): review in pane: review never started\nactions: retry, keys, skip, block, stop; resolve it with resolve_blocker\n\nscreen text"
	st := RunState{ID: "run-1", Questions: []Question{
		{ID: "b1", Kind: QuestionBlocker, Step: key, Text: text, Answer: "retry", AnsweredBy: "watchdog", Citation: "allow-list"},
		{ID: "b2", Kind: QuestionBlocker, Step: StepKey{Run: "run-1", Phase: "3", Kind: "land"}, Text: "blocker b2 from phase-3/land (land): merge conflict\nactions: retry, block, stop; resolve it with resolve_blocker", Answer: "block", AnsweredBy: "timeout", Citation: "timeout"},
		{ID: "b3", Kind: QuestionBlocker, Step: StepKey{Run: "run-1", Phase: "3", Kind: "land"}, Text: "blocker b3 from phase-3/land (land): dirty tree\nactions: retry, block, stop; resolve it with resolve_blocker"},
	}}

	rep := Report(st, threePhasePlan())

	want := "- b1 phase-2/implement-rv-codex (reviewer): review in pane: review never started → retry (watchdog)\n" +
		"- b2 phase-3/land (land): merge conflict → block (timeout)\n" +
		"- b3 phase-3/land (land): dirty tree (open)\n"
	if !strings.Contains(rep, want) {
		t.Errorf("report lacks %q:\n%s", want, rep)
	}
	if strings.Contains(rep, "screen text") || strings.Contains(rep, "resolve_blocker") {
		t.Errorf("report prints more than the first line:\n%s", rep)
	}
}

func TestAHaltLineOfABlockedStepListsTheTranscriptsOfItsWorkerAndReviewers(t *testing.T) {
	// given
	st := RunState{ID: "run-1", Status: RunHalted, Events: []Event{
		{Kind: "agent-transcript", Phase: "2", Step: "implement", Fields: map[string]string{"attempt": "1", "agent": "w", "transcript_path": "/home/.claude/projects/-wt/s-1.jsonl"}},
		{Kind: "agent-transcript", Phase: "2", Step: "implement", Fields: map[string]string{"attempt": "1", "reviewer": "codex", "round": "1", "transcript_path": "/home/.codex/sessions/2026/10/13/rollout-a.jsonl"}},
		{Kind: "agent-transcript", Phase: "2", Step: "implement", Fields: map[string]string{"attempt": "1", "reviewer": "claude", "round": "1", "transcript_path": "/home/.claude/projects/-wt/s-2.jsonl"}},
		{Kind: "agent-transcript", Phase: "2", Step: "plan", Fields: map[string]string{"attempt": "1", "transcript_path": "/home/.claude/projects/-wt/s-0.jsonl"}},
		{Kind: "agent-transcript", Phase: "3", Step: "implement", Fields: map[string]string{"attempt": "1", "transcript_path": "/home/.claude/projects/-wt/s-9.jsonl"}},
		{Kind: "phase-blocked", Phase: "2", Step: "implement", Fields: map[string]string{"phase": "2", "reason": "tests red", "workspace": "ws-2", "worktree": "/wt/phase-2"}},
	}}

	// when
	actual := Report(st, threePhasePlan())

	// then
	expected := "- phase 2 implement: tests red — workspace ws-2, worktree /wt/phase-2 — transcripts: worker a1 /home/.claude/projects/-wt/s-1.jsonl, reviewer codex a1 r1 /home/.codex/sessions/2026/10/13/rollout-a.jsonl, reviewer claude a1 r1 /home/.claude/projects/-wt/s-2.jsonl\n"
	if !strings.Contains(actual, expected) {
		t.Fatalf("report missing %q:\n%s", expected, actual)
	}
}

func TestAHaltLineOfAnAbortedStepListsItsTranscripts(t *testing.T) {
	// given
	st := RunState{ID: "run-1", Status: RunHalted, Events: []Event{
		{Kind: "agent-transcript", Phase: "2", Step: "implement", Fields: map[string]string{"attempt": "1", "transcript_path": "/t/w.jsonl"}},
		{Kind: "aborted", Phase: "2", Step: "implement", Fields: map[string]string{"workspace": "ws-2", "worktree": "/wt/phase-2"}},
	}}

	// when
	actual := Report(st, threePhasePlan())

	// then
	expected := "- aborted during phase 2 implement — workspace ws-2, worktree /wt/phase-2 — transcripts: worker a1 /t/w.jsonl\n"
	if !strings.Contains(actual, expected) {
		t.Fatalf("report missing %q:\n%s", expected, actual)
	}
}

func TestAHaltLineOfAStepWithoutTranscriptsRendersAsBefore(t *testing.T) {
	// given
	st := RunState{ID: "run-1", Status: RunHalted, Events: []Event{
		{Kind: "agent-transcript", Phase: "1", Step: "plan", Fields: map[string]string{"attempt": "1", "transcript_path": "/t/p.jsonl"}},
		{Kind: "phase-blocked", Phase: "2", Step: "implement", Fields: map[string]string{"phase": "2", "reason": "tests red", "workspace": "ws-2", "worktree": "/wt/phase-2"}},
	}}

	// when
	actual := Report(st, threePhasePlan())

	// then
	expected := "- phase 2 implement: tests red — workspace ws-2, worktree /wt/phase-2\n"
	if !strings.Contains(actual, expected) {
		t.Fatalf("report missing %q:\n%s", expected, actual)
	}
}

func TestAHaltLineOfALandingStepListsTheTranscriptsOfItsGateAndMilestoneSessions(t *testing.T) {
	// given
	st := RunState{ID: "run-1", Status: RunHalted, Events: []Event{
		{Kind: "agent-transcript", Phase: "2", Step: "implement", Fields: map[string]string{"attempt": "1", "transcript_path": "/t/w.jsonl"}},
		{Kind: "agent-transcript", Phase: "2", Step: "gatefix", Fields: map[string]string{"attempt": "1", "transcript_path": "/t/gf.jsonl"}},
		{Kind: "agent-transcript", Phase: "2", Step: "milestone", Fields: map[string]string{"attempt": "2", "transcript_path": "/t/m.jsonl"}},
		{Kind: "agent-transcript", Phase: "3", Step: "gatefix", Fields: map[string]string{"attempt": "1", "transcript_path": "/t/gf3.jsonl"}},
		{Kind: "phase-blocked", Phase: "2", Step: "land", Fields: map[string]string{"phase": "2", "reason": "land: gate red", "workspace": "ws-2", "worktree": "/wt/phase-2"}},
	}}

	// when
	actual := Report(st, threePhasePlan())

	// then
	expected := "- phase 2 land: land: gate red — workspace ws-2, worktree /wt/phase-2 — transcripts: gatefix worker a1 /t/gf.jsonl, milestone worker a2 /t/m.jsonl\n"
	if !strings.Contains(actual, expected) {
		t.Fatalf("report missing %q:\n%s", expected, actual)
	}
}

func TestReportListsTheWatchdogsConfigChangesAndPlanCorrectionsAsAutomaticDecisions(t *testing.T) {
	st := RunState{ID: "run-1", Events: []Event{
		{Kind: EventConfigSet, Fields: map[string]string{"key": "steps.implement.model", "value": "gpt-5.5", "why": "gpt-5 is retired"}},
		{Kind: EventConfigSet, Fields: map[string]string{"key": "steps.implement.modle", "value": "x", "why": "typo"}},
		{Kind: EventConfigSetFailed, Fields: map[string]string{"key": "steps.implement.modle", "reason": "unknown key"}},
		{Kind: EventPlanCorrected, Phase: "2", Fields: map[string]string{"phase": "2", "path": "docs/x/todo.md"}},
	}}

	got := Report(st, threePhasePlan())

	want := "## Automatic decisions\n\n" +
		"- set steps.implement.model = gpt-5.5 — gpt-5 is retired\n" +
		"- set steps.implement.modle = x — typo (failed: unknown key)\n" +
		"- phase 2: plan corrected in docs/x/todo.md\n"
	if !strings.Contains(got, want) {
		t.Fatalf("report:\n%s", got)
	}
}
