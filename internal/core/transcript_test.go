package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	claudeReviewer = "rloop-2kuxv-p3-implemen-q1s68-r1"
	codexRollout   = "/home/.codex/sessions/2026/10/13/rollout-2026-10-13T12-00-01-019a.jsonl"
	firstPrompt    = `SessionHost.Prompt rloop-2kuxv-p3-implement "do phase 3" false 0s`
	firstEnter     = "SessionHost.SendKeys rloop-2kuxv-p3-implement enter"
)

func transcriptEvents(store *fakeStore) []Event {
	var out []Event
	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == "agent-transcript" {
			ev := *rec.Event
			ev.At = time.Time{}
			out = append(out, ev)
		}
	}
	return out
}

func transcriptPaths(store *fakeStore) []string {
	var out []string
	for _, ev := range transcriptEvents(store) {
		out = append(out, ev.Fields["transcript_path"])
	}
	return out
}

func reviewerTranscripts(store *fakeStore) map[string]string {
	out := map[string]string{}
	for _, ev := range transcriptEvents(store) {
		out[ev.Fields["reviewer"]] = ev.Fields["transcript_path"]
	}
	return out
}

func implementTranscript(fields map[string]string) []Event {
	return []Event{{Kind: "agent-transcript", Phase: "3", Step: "implement", Fields: fields}}
}

func workerTranscript(agent, attempt, session, path string) []Event {
	return implementTranscript(map[string]string{"agent": agent, "attempt": attempt, "session_id": session, "transcript_path": path})
}

func reviewerTranscript(agent, reviewer, session, path string) []Event {
	return implementTranscript(map[string]string{"agent": agent, "attempt": "1", "reviewer": reviewer, "round": "1", "session_id": session, "transcript_path": path})
}

func claudeSessionStart(agent, session string) HookEvent {
	return HookEvent{At: t0, Agent: agent, Event: "SessionStart", Session: session, Transcript: "/home/.claude/projects/-repo/" + session + ".jsonl"}
}

func claudeRead(agent, session string) HookEvent {
	return HookEvent{At: t1, Agent: agent, Event: "PreToolUse", Tool: "Read", Target: "/repo/a.go", Session: session, Transcript: "/home/.claude/projects/-repo/" + session + ".jsonl"}
}

func finishOK(t *testing.T, r *rig, s *Session) {
	t.Helper()
	r.repo.TreeChanges = []string{"a.go"}
	r.writeSentinel(t, s, "ok", "")
	r.host.script = func(int) AgentState { return AgentIdle }
}

func codexRolloutRig(t *testing.T) (*rig, *fakeRollouts) {
	r := newRig(t)
	r.sm.Now = (&stepClock{t: t0}).Now
	r.sm.Resolve = func(provider, model, effort, askURL, mcpConfigPath, dir string) (ProviderArgs, error) {
		return ProviderArgs{Kind: "codex", Rollout: true}, nil
	}
	rollouts := &fakeRollouts{files: map[string]rolloutFile{filepath.Join(r.runDir, "phase-3", "implement-a1.sentinel"): {codexRollout, "019a"}}}
	r.sm.Rollouts = rollouts
	return r, rollouts
}

func codexReviewersRig(t *testing.T) *reviewRig {
	r := newReviewRig(t, Reviewer{Provider: "codex", Name: "codex-a"}, Reviewer{Provider: "codex", Name: "codex-b"})
	r.sm.Now = (&stepClock{t: t0}).Now
	rolloutResolve(r)
	return r
}

func hookedReviewRig(t *testing.T, provider string, pushes ...[]HookEvent) (*reviewRig, *fakeHookFeed) {
	r := newReviewRig(t, Reviewer{Provider: provider})
	feed := &fakeHookFeed{}
	r.sm.Hooks = feed
	hookedResolve(r)
	r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, pushes: pushes}
	return r, feed
}

func TestAClaudeAgentsFirstHookRecordsOneAgentTranscriptEvent(t *testing.T) {
	// given
	r, feed, _ := hookStepRig(t, claudeSessionStart(stepAgent, "s-1"))
	s := r.spawn(t, 1)
	feed.push(HookEvent{At: t1, Agent: stepAgent, Event: "SessionStart", Session: "s-2", Transcript: "/home/.claude/projects/-repo/s-2.jsonl"}, stop(stepAgent))
	finishOK(t, r, s)

	// when
	r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertEqual(t, "agent-transcript events", transcriptEvents(r.store), workerTranscript(stepAgent, "1", "s-1", "/home/.claude/projects/-repo/s-1.jsonl"))
}

func TestAClaudeAgentWithoutASessionStartRecordsTheTranscriptOfItsFirstLaterHook(t *testing.T) {
	// given
	fastHookPoll(t)
	r, feed, _ := hookStepRig(t)
	shortHookStartWait(r.sm)
	s := r.spawn(t, 1)
	feed.push(claudeRead(stepAgent, "s-1"))
	finishOK(t, r, s)

	// when
	r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertEqual(t, "agent-transcript events", transcriptEvents(r.store), workerTranscript(stepAgent, "1", "s-1", "/home/.claude/projects/-repo/s-1.jsonl"))
}

func TestAClaudeWorkerWhosePromptFailsRecordsItsTranscriptWhenTheStepFinishes(t *testing.T) {
	tests := []struct {
		name string
		hook HookEvent
	}{
		{"session start", claudeSessionStart(stepAgent, "s-1")},
		{"another hook, no session start", HookEvent{At: t0, Agent: stepAgent, Event: "PreToolUse", Tool: "Read", Target: "/repo/a.go", Session: "s-1", Transcript: "/home/.claude/projects/-repo/s-1.jsonl"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			fastHookPoll(t)
			r, _, _ := hookStepRig(t, tt.hook)
			shortHookStartWait(r.sm)
			r.prompts.Err = errors.New("prompt template missing")

			// when
			out := singleRunner{sm: r.sm}.Run(context.Background(), r.ref(1), &recObserver{})

			// then
			assertState(t, out, StepFailed, "spawn: prompt template missing")
			assertEqual(t, "agent-transcript events", transcriptEvents(r.store), workerTranscript(stepAgent, "1", "s-1", "/home/.claude/projects/-repo/s-1.jsonl"))
		})
	}
}

func TestAClaudeReviewersTranscriptNamesItsReviewerAndRound(t *testing.T) {
	// given
	r, _ := hookedReviewRig(t, "claude", []HookEvent{{At: t1, Agent: claudeReviewer, Event: "SessionStart", Session: "s-rv", Transcript: "/home/.claude/projects/-repo/s-rv.jsonl"}})
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }

	// when
	r.run()

	// then
	assertEqual(t, "agent-transcript events", transcriptEvents(r.store), reviewerTranscript(claudeReviewer, "claude", "s-rv", "/home/.claude/projects/-repo/s-rv.jsonl"))
}

func TestAReopenedReviewerRecordsItsOwnTranscriptNotItsPredecessors(t *testing.T) {
	// given
	r, _ := hookedReviewRig(t, "codex",
		[]HookEvent{{At: t1, Agent: codexReviewer, Event: "SessionStart", Session: "s-old", Transcript: "/t/s-old.jsonl"}},
		[]HookEvent{{At: t1, Agent: codexReviewer, Event: "SessionStart", Session: "s-new", Transcript: "/t/s-new.jsonl"}})
	failCodexOnce(t, r)

	// when
	r.runRaising(then(Resolution{Action: "retry", By: "watchdog"}, Resolution{Action: "block", By: "watchdog"}))

	// then
	assertCalls(t, transcriptPaths(r.store), []string{"/t/s-old.jsonl", "/t/s-new.jsonl"})
}

func TestAReviewerReopenedAfterItsPromptFailedRecordsItsPredecessorsTranscriptOnce(t *testing.T) {
	// given
	r, _ := hookedReviewRig(t, "claude",
		[]HookEvent{{At: t1, Agent: claudeReviewer, Event: "SessionStart", Session: "s-old", Transcript: "/t/s-old.jsonl"}},
		[]HookEvent{{At: t1, Agent: claudeReviewer, Event: "SessionStart", Session: "s-new", Transcript: "/t/s-new.jsonl"}})
	inner := r.sm.Prompts
	reviews := 0
	r.sm.Prompts = promptsFunc(func(name string, vars map[string]any) (string, string, error) {
		if name == "review" {
			reviews++
			if reviews == 1 {
				return "", "", errors.New("review template missing")
			}
		}
		return inner.Render(name, vars)
	})
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }

	// when
	reviewed, _ := r.runRaising(then(Resolution{Action: "retry", By: "watchdog"}))
	out := r.sm.Finish(r.worker, reviewed)

	// then
	assertState(t, out, StepOK, "")
	assertCalls(t, transcriptPaths(r.store), []string{"/t/s-old.jsonl", "/t/s-new.jsonl"})
}

func TestAFailedTranscriptRecordWarnsAndDoesNotFailTheStep(t *testing.T) {
	// given
	r, feed, face := hookStepRig(t, claudeSessionStart(stepAgent, "s-1"))
	guard := &RecordGuard{Store: failingStore{Store: r.store, fail: func(rec Record) bool { return isEvent(rec, "agent-transcript") }}}
	r.sm.Store = guard
	s := r.spawn(t, 1)
	feed.push(stop(stepAgent))
	finishOK(t, r, s)

	// when
	out := r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertState(t, out, StepOK, "")
	assertEqual(t, "guard failure", guard.Failed(), nil)
	assertCalls(t, warnings(face), []string{"transcript: disk full"})
}

func TestACodexWorkersRolloutIsRecordedAsItsTranscript(t *testing.T) {
	// given
	r, rollouts := codexRolloutRig(t)
	s := r.spawn(t, 1)
	finishOK(t, r, s)

	// when
	r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertEqual(t, "agent-transcript events", transcriptEvents(r.store), workerTranscript(stepAgent, "1", "019a", codexRollout))
	assertEqual(t, "first lookup", rollouts.calls[0], "/repo/.r-loop/wt/phase-3 "+filepath.Join(r.runDir, "phase-3", "implement-a1.sentinel")+" 2026-10-13T12:00:00Z")
}

func TestACodexRolloutFoundOnALaterTickIsRecorded(t *testing.T) {
	// given
	r, rollouts := codexRolloutRig(t)
	rollouts.misses = 1
	s := r.spawn(t, 1)
	r.repo.TreeChanges = []string{"a.go"}
	r.host.script = func(n int) AgentState {
		if n == 2 {
			r.writeSentinel(t, s, "ok", "")
		}
		return AgentIdle
	}

	// when
	r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertCalls(t, transcriptPaths(r.store), []string{codexRollout})
}

func TestACodexRolloutIsNotLookedUpAgainOnceRecorded(t *testing.T) {
	// given
	r, rollouts := codexRolloutRig(t)
	s := r.spawn(t, 1)
	r.repo.TreeChanges = []string{"a.go"}
	r.host.script = func(n int) AgentState {
		if n == 3 {
			r.writeSentinel(t, s, "ok", "")
		}
		return AgentIdle
	}

	// when
	r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertEqual(t, "lookups", len(rollouts.calls), 1)
	assertEqual(t, "agent-transcript events", len(r.events("agent-transcript")), 1)
}

func TestCodexReviewersInOneWorktreeEachRecordTheRolloutNamingTheirOwnSentinel(t *testing.T) {
	// given
	r := codexReviewersRig(t)
	r.sm.Rollouts = &fakeRollouts{files: map[string]rolloutFile{
		filepath.Join(r.runDir, "phase-3", "implement-rv-codex-a-r1-a1.sentinel"): {"/x/rollout-a.jsonl", "a"},
		filepath.Join(r.runDir, "phase-3", "implement-rv-codex-b-r1-a1.sentinel"): {"/x/rollout-b.jsonl", "b"},
	}}
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }

	// when
	r.run()

	// then
	assertEqual(t, "reviewer transcripts", reviewerTranscripts(r.store), map[string]string{"codex-a": "/x/rollout-a.jsonl", "codex-b": "/x/rollout-b.jsonl"})
}

func TestNoRolloutLookupForASessionWhoseProviderWritesNone(t *testing.T) {
	// given
	r := newRig(t)
	rollouts := &fakeRollouts{}
	r.sm.Rollouts = rollouts
	s := r.spawn(t, 1)
	finishOK(t, r, s)

	// when
	r.sm.Wait(waitCtx(t), s, &recObserver{})

	// then
	assertCalls(t, rollouts.calls, nil)
	assertEqual(t, "agent-transcript events", r.events("agent-transcript"), nil)
}

func TestAClaudeReviewerWhosePromptFailsRecordsItsTranscriptWhenTheStepFinishes(t *testing.T) {
	// given
	fastHookPoll(t)
	r, _ := hookedReviewRig(t, "claude", []HookEvent{claudeRead(claudeReviewer, "s-rv")})
	shortHookStartWait(r.sm)
	r.sm.Prompts = promptsFunc(func(string, map[string]any) (string, string, error) {
		return "", "", errors.New("review template missing")
	})

	// when
	out := r.sm.Finish(r.worker, r.run())

	// then
	assertState(t, out, StepFailed, "reviewer claude: review template missing")
	assertEqual(t, "agent-transcript events", transcriptEvents(r.store), reviewerTranscript(claudeReviewer, "claude", "s-rv", "/home/.claude/projects/-repo/s-rv.jsonl"))
}

func TestAStepCancelledBeforeItsFirstTickRecordsItsCodexRolloutWhenItFinishes(t *testing.T) {
	// given
	r, rollouts := codexRolloutRig(t)
	r.sm.Poll = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// when
	out := singleRunner{sm: r.sm}.Run(ctx, r.ref(1), &recObserver{})

	// then
	assertState(t, out, StepFailed, "interrupted: context canceled")
	assertCalls(t, rollouts.calls, []string{"/repo/.r-loop/wt/phase-3 " + filepath.Join(r.runDir, "phase-3", "implement-a1.sentinel") + " 2026-10-13T12:00:00Z"})
	assertEqual(t, "agent-transcript events", transcriptEvents(r.store), workerTranscript(stepAgent, "1", "019a", codexRollout))
}

func TestAStepCancelledBeforeItsFirstTickRecordsAClaudeHookQueuedAfterItsStart(t *testing.T) {
	// given
	fastHookPoll(t)
	r, feed, _ := hookStepRig(t)
	shortHookStartWait(r.sm)
	r.sm.Poll = time.Hour
	s := r.spawn(t, 1)
	feed.push(claudeRead(stepAgent, "s-1"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// when
	out := r.sm.Finish(s, r.sm.Wait(ctx, s, &recObserver{}))

	// then
	assertState(t, out, StepFailed, "interrupted: context canceled")
	assertEqual(t, "agent-transcript events", transcriptEvents(r.store), workerTranscript(stepAgent, "1", "s-1", "/home/.claude/projects/-repo/s-1.jsonl"))
}

func TestAnEarlierReviewersRolloutIsRecordedWhenASiblingsPromptFailureEndsTheRound(t *testing.T) {
	// given
	r := codexReviewersRig(t)
	r.sm.Rollouts = &fakeRollouts{files: map[string]rolloutFile{
		filepath.Join(r.runDir, "phase-3", "implement-rv-codex-a-r1-a1.sentinel"): {"/x/rollout-a.jsonl", "a"},
	}}
	inner := r.sm.Prompts
	r.sm.Prompts = promptsFunc(func(name string, vars map[string]any) (string, string, error) {
		if path, _ := vars["FindingsPath"].(string); strings.Contains(path, "codex-b") {
			return "", "", errors.New("review template missing")
		}
		return inner.Render(name, vars)
	})

	// when
	out := r.sm.Finish(r.worker, r.run())

	// then
	assertState(t, out, StepFailed, "reviewer codex-b: review template missing")
	assertEqual(t, "reviewer transcripts", reviewerTranscripts(r.store), map[string]string{"codex-a": "/x/rollout-a.jsonl"})
}

func TestAResumedWorkersFirstHookIsRecordedWhileOnlyItsReviewersAreWaited(t *testing.T) {
	// given
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	feed := &fakeHookFeed{}
	r.sm.Hooks = feed
	ref := r.worker.Ref
	ref.Key.Attempt = 2
	ref.ReviewFrom = 1
	r.behave = func(vars map[string]any) {
		feed.push(HookEvent{At: t1, Agent: "rloop-2kuxv-p3-implement-a2", Event: "PreToolUse", Tool: "Read", Target: "/repo/a.go", Session: "s-w", Transcript: "/home/.claude/projects/-repo/s-w.jsonl"})
		writeReview(t, vars, "ok", 0)
	}

	// when
	out := singleRunner{sm: r.sm, review: ReviewHalf{Sessions: r.sm, Store: r.store}.Run}.Run(context.Background(), ref, &recObserver{})

	// then
	assertState(t, out, StepOK, "")
	assertEqual(t, "agent-transcript events", transcriptEvents(r.store), workerTranscript("rloop-2kuxv-p3-implement-a2", "2", "s-w", "/home/.claude/projects/-repo/s-w.jsonl"))
}

func TestAWorkerWhoseStartFailsAfterItsFirstHookRecordsItsTranscript(t *testing.T) {
	// given
	r, feed, _ := hookStepRig(t)
	r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, pushes: [][]HookEvent{{claudeSessionStart(stepAgent, "s-1")}}, errs: []error{errors.New("herdr: agent rloop-2kuxv-p3-implement never became ready (state blocked)")}}

	// when
	out := singleRunner{sm: r.sm}.Run(context.Background(), r.ref(1), &recObserver{})

	// then
	assertState(t, out, StepFailed, "spawn: herdr: agent rloop-2kuxv-p3-implement never became ready (state blocked)")
	assertEqual(t, "agent-transcript events", transcriptEvents(r.store), workerTranscript(stepAgent, "1", "s-1", "/home/.claude/projects/-repo/s-1.jsonl"))
}

func TestAReviewerWhoseStartFailsAfterItsFirstHookRecordsItsTranscript(t *testing.T) {
	// given
	r, feed := hookedReviewRig(t, "claude")
	r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, pushes: [][]HookEvent{{{At: t1, Agent: claudeReviewer, Event: "SessionStart", Session: "s-rv", Transcript: "/home/.claude/projects/-repo/s-rv.jsonl"}}}, errs: []error{errors.New("start refused")}}

	// when
	out := r.sm.Finish(r.worker, r.run())

	// then
	assertState(t, out, StepFailed, "reviewer claude: start refused")
	assertEqual(t, "agent-transcript events", transcriptEvents(r.store), reviewerTranscript(claudeReviewer, "claude", "s-rv", "/home/.claude/projects/-repo/s-rv.jsonl"))
}

type rolloutHost struct {
	*scriptedHost
	rollouts *fakeRollouts
	marker   string
	onPrompt int
	onEnter  bool
	prompts  int
}

func (h *rolloutHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	err := h.scriptedHost.Prompt(agent, text, wait, timeout)
	h.prompts++
	if h.prompts == h.onPrompt {
		h.rollouts.show(h.marker)
	}
	return err
}

func (h *rolloutHost) SendKeys(agent string, keys ...string) error {
	err := h.scriptedHost.SendKeys(agent, keys...)
	if h.onEnter {
		h.rollouts.show(h.marker)
	}
	return err
}

func freshCodexRig(t *testing.T) (*rig, *rolloutHost) {
	shortSubmitWait(t)
	fastHookPoll(t)
	r := newRig(t)
	r.sm.Now = (&stepClock{t: t0}).Now
	r.sm.Resolve = func(provider, model, effort, askURL, mcpConfigPath, dir string) (ProviderArgs, error) {
		return ProviderArgs{Kind: "codex", Rollout: true}, nil
	}
	r.host.script = func(int) AgentState { return AgentIdle }
	rollouts := &fakeRollouts{files: map[string]rolloutFile{}}
	r.sm.Rollouts = rollouts
	host := &rolloutHost{scriptedHost: r.host, rollouts: rollouts, marker: filepath.Join(r.runDir, "phase-3", "implement-a1.sentinel")}
	r.sm.Host = host
	return r, host
}

func callsOf(r *rig, prefix string) []string {
	var out []string
	for _, c := range r.host.Calls() {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func TestAFreshCodexPromptNoRolloutEverShowsIsRetypedThenFailsTheSpawn(t *testing.T) {
	// given
	r, _ := freshCodexRig(t)

	// when
	_, err := r.sm.Spawn(context.Background(), r.ref(1))

	// then
	if err == nil || !strings.Contains(err.Error(), "prompt to "+stepAgent+" not received") {
		t.Fatalf("spawn error = %v, want a prompt not received failure", err)
	}
	assertEqual(t, "prompts typed", callsOf(r, "SessionHost.Prompt "), []string{firstPrompt, firstPrompt, firstPrompt})
	assertEqual(t, "enters sent", callsOf(r, "SessionHost.SendKeys "), []string{firstEnter, firstEnter, firstEnter})
	assertEqual(t, "agent-transcript events", transcriptEvents(r.store), []Event(nil))
}

func TestAFreshCodexPromptNoRolloutShowsWhileHerdrReportsItBusyIsTypedOnceAndTheSpawnSucceeds(t *testing.T) {
	for _, state := range []AgentState{AgentWorking, AgentBlocked, AgentUnknown, AgentGone} {
		t.Run(string(state), func(t *testing.T) {
			// given
			r, _ := freshCodexRig(t)
			r.host.script = func(int) AgentState { return state }

			// when
			_, err := r.sm.Spawn(context.Background(), r.ref(1))

			// then
			assertEqual(t, "spawn error", err, error(nil))
			assertEqual(t, "prompts typed", callsOf(r, "SessionHost.Prompt "), []string{firstPrompt})
			assertEqual(t, "enters sent", callsOf(r, "SessionHost.SendKeys "), []string(nil))
		})
	}
}

func TestAFreshCodexPromptWhoseHerdrStateCannotBeReadCountsAsReceived(t *testing.T) {
	// given
	r, _ := freshCodexRig(t)
	r.host.StateErr = errors.New("herdr: timed out")
	face := &fakeFace{}
	r.sm.Face = face

	// when
	_, err := r.sm.Spawn(context.Background(), r.ref(1))

	// then
	assertEqual(t, "spawn error", err, error(nil))
	assertEqual(t, "prompts typed", callsOf(r, "SessionHost.Prompt "), []string{firstPrompt})
	assertEqual(t, "enters sent", callsOf(r, "SessionHost.SendKeys "), []string(nil))
	assertEqual(t, "warnings", warnings(face), []string{"prompt to rloop-2kuxv-p3-implement: no rollout shows it (lookup by " + filepath.Join(r.runDir, "phase-3", "implement-a1.sentinel") + " found nothing); herdr state failed (herdr: timed out), so it counts as received"})
}

func TestAFreshCodexPromptHerdrReportsWorkingAfterTheEnterIsNotRetyped(t *testing.T) {
	// given
	r, _ := freshCodexRig(t)
	r.host.script = func(n int) AgentState {
		if n == 1 {
			return AgentIdle
		}
		return AgentWorking
	}

	// when
	_, err := r.sm.Spawn(context.Background(), r.ref(1))

	// then
	assertEqual(t, "spawn error", err, error(nil))
	assertEqual(t, "prompts typed", callsOf(r, "SessionHost.Prompt "), []string{firstPrompt})
	assertEqual(t, "enters sent", callsOf(r, "SessionHost.SendKeys "), []string{firstEnter})
}

func TestAFreshCodexPromptHerdrReportsDoneIsRetypedUntilTheTriesRunOut(t *testing.T) {
	// given
	r, _ := freshCodexRig(t)
	r.host.script = func(int) AgentState { return AgentDone }

	// when
	_, err := r.sm.Spawn(context.Background(), r.ref(1))

	// then
	if err == nil || !strings.Contains(err.Error(), "prompt to rloop-2kuxv-p3-implement not received") {
		t.Fatalf("spawn error = %v, want a prompt not received failure", err)
	}
	assertEqual(t, "prompts typed", callsOf(r, "SessionHost.Prompt "), []string{firstPrompt, firstPrompt, firstPrompt})
	assertEqual(t, "enters sent", callsOf(r, "SessionHost.SendKeys "), []string{firstEnter, firstEnter, firstEnter})
}

func TestAFreshCodexPromptCountedReceivedByHerdrStateWarnsOnceNamingTheAgentAndTheRolloutLookup(t *testing.T) {
	// given
	r, _ := freshCodexRig(t)
	r.host.script = func(int) AgentState { return AgentWorking }
	face := &fakeFace{}
	r.sm.Face = face

	// when
	r.spawn(t, 1)

	// then
	assertEqual(t, "warnings", warnings(face), []string{"prompt to rloop-2kuxv-p3-implement: no rollout shows it (lookup by " + filepath.Join(r.runDir, "phase-3", "implement-a1.sentinel") + " found nothing); herdr reports it working, so it counts as received"})
}

func TestHerdrStateIsNotReadWhileTheRolloutConfirmsTheFirstPrompt(t *testing.T) {
	// given
	r, host := freshCodexRig(t)
	r.host.script = func(int) AgentState { return AgentWorking }
	host.rollouts.files[host.marker] = rolloutFile{codexRollout, "019a"}
	host.rollouts.misses = 1

	// when
	r.spawn(t, 1)

	// then
	assertEqual(t, "state reads", callsOf(r, "SessionHost.State "), []string(nil))
	assertEqual(t, "prompts typed", callsOf(r, "SessionHost.Prompt "), []string{firstPrompt})
	assertEqual(t, "transcripts", transcriptPaths(r.store), []string{codexRollout})
}

func TestALaterPromptToACodexAgentCountedReceivedByHerdrStateIsTypedWithoutARolloutWait(t *testing.T) {
	// given
	r, _ := freshCodexRig(t)
	r.host.script = func(int) AgentState { return AgentWorking }
	face := &fakeFace{}
	r.sm.Face = face
	r.spawn(t, 1)

	// when
	err := r.sm.prompt(stepAgent, "r-loop: go on", nil)

	// then
	assertEqual(t, "prompt error", err, error(nil))
	assertEqual(t, "prompts typed", callsOf(r, "SessionHost.Prompt "), []string{firstPrompt, `SessionHost.Prompt rloop-2kuxv-p3-implement "r-loop: go on" false 0s`})
	assertEqual(t, "state reads", callsOf(r, "SessionHost.State "), []string{"SessionHost.State rloop-2kuxv-p3-implement"})
	assertEqual(t, "warnings", len(warnings(face)), 1)
}

func TestACodexReviewerWhoseRolloutNeverShowsWhileHerdrReportsItWorkingRunsItsReview(t *testing.T) {
	// given
	shortSubmitWait(t)
	fastHookPoll(t)
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	rolloutResolve(r)
	r.sm.Rollouts = &fakeRollouts{files: map[string]rolloutFile{}}
	r.host.script = func(int) AgentState { return AgentWorking }
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }

	// when
	out := r.run()

	// then
	assertState(t, out, StepOK, "")
	assertEqual(t, "reviewer prompts", r.callsFrom("SessionHost.Prompt "+codexReviewer), []string{`SessionHost.Prompt rloop-2kuxv-p3-implemen-8lgad-r1 "review r1" false 0s`})
}

func TestAFreshCodexPromptLostOnceIsRetypedUntilItsRolloutShowsIt(t *testing.T) {
	// given
	r, host := freshCodexRig(t)
	host.onPrompt = 2

	// when
	r.spawn(t, 1)

	// then
	assertEqual(t, "prompts typed", len(callsOf(r, "SessionHost.Prompt ")), 2)
	assertEqual(t, "transcripts", transcriptPaths(r.store), []string{codexRollout})
}

func TestAFreshCodexPromptLeftInTheComposerIsSubmittedByEnterNotRetyped(t *testing.T) {
	// given
	r, host := freshCodexRig(t)
	host.onEnter = true

	// when
	r.spawn(t, 1)

	// then
	assertEqual(t, "prompts typed", len(callsOf(r, "SessionHost.Prompt ")), 1)
	assertEqual(t, "enters sent", len(callsOf(r, "SessionHost.SendKeys ")), 1)
}

func TestAFreshCodexPromptItsRolloutShowsIsTypedOnce(t *testing.T) {
	// given
	r, host := freshCodexRig(t)
	host.onPrompt = 1

	// when
	r.spawn(t, 1)

	// then
	assertEqual(t, "prompts typed", len(callsOf(r, "SessionHost.Prompt ")), 1)
	assertEqual(t, "enters sent", len(callsOf(r, "SessionHost.SendKeys ")), 0)
	assertEqual(t, "transcripts", transcriptPaths(r.store), []string{codexRollout})
}

func TestACodexReviewerWhoseFirstPromptNeverArrivesFailsTheStepWithADeliveryReason(t *testing.T) {
	// given
	shortSubmitWait(t)
	fastHookPoll(t)
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	rolloutResolve(r)
	r.sm.Rollouts = &fakeRollouts{files: map[string]rolloutFile{}}
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }

	// when
	out := r.run()

	// then
	if out.State != StepFailed || !strings.HasPrefix(out.Reason, "reviewer codex: prompt to ") || !strings.Contains(out.Reason, " not received") {
		t.Fatalf("outcome = %s %q, want a reviewer prompt not received failure", out.State, out.Reason)
	}
}

func TestACodexWorkerResumedIntoReviewHasItsFixPromptConfirmedByTheFixSentinel(t *testing.T) {
	// given
	shortSubmitWait(t)
	fastHookPoll(t)
	r := newReviewRig(t, Reviewer{Provider: "claude"})
	rollouts := &fakeRollouts{files: map[string]rolloutFile{}}
	r.sm.Rollouts = rollouts
	r.worker.rollout = true
	r.worker.Ref.ReviewFrom = 1
	r.behave = func(vars map[string]any) {
		r.repo.TreeChanges = nil
		findings := 0
		if vars["Round"] == 1 {
			findings = 1
		}
		writeReview(t, vars, "ok", findings)
	}
	r.onFix = func(vars map[string]any) {
		rollouts.show(vars["Sentinel"].(string))
		r.repo.TreeChanges = []string{"a.go"}
		writeVerdict(t, vars, entry("claude-r1-1", "real", "P1", true, ""))
	}

	// when
	out := r.run()

	// then
	assertState(t, out, StepOK, "")
	assertEqual(t, "fix prompts typed", len(r.callsFrom(`SessionHost.Prompt `+r.worker.Agent+` "fix r1"`)), 1)
}
