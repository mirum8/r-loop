status: planned

## Summary

Every step, gate, milestone and reviewer agent name gets a 5-character run token, and all of those names pass through one 32-character cap. The token is a hash of the repository root and the run ID. The scheme is `rloop-[<label>-]<token>-p<N>-<kind>[-a<attempt>]` for step agents. Reviewers get `…-rv-<id>` plus the round/attempt suffix.

A hash can collide, so before starting an agent the driver asks herdr whether the name is already taken (`Host.AgentPane(name) != ""`). If it is, the driver moves to the next salted token, trying at most three. So two runs on one machine never hand herdr the same live name.

The name actually chosen is appended as an `agent-named` event before `Host.Start`, for steps and reviewers alike. Resume interrupts the recorded step and reviewer agents of the last attempt. For a run started before this change (no `agent-named` events), resume rebuilds the old names: `rloop-[<label>-]p<N>-<kind>[-a<N>]` for the step, and the old capped `…-rv-<id>-r<round>[-a<N>]` for each configured reviewer. Workspace labels stay as `4077431` built them.

Choices:
- **Where the token comes from.** Picked: `fnv32a(Repo.Root() + "\n" + runID [+ "\n" + salt])`, taken mod 36⁵ and written as 5 zero-padded base36 characters. Rejected: the PID, because it changes on resume. Rejected: the run ID's time part, because it is only unique per repo and per day. Rejected: `RunDir`, because tests use a temp dir for it, so test names could not be hard-coded.
- **How a token collision is handled.** Picked: check the name with herdr's `AgentPane` (already on the `SessionHost` port, ports.go:46) and move to the next salt when it is taken. Rejected: a longer hash, which only lowers the odds. Rejected: a machine-wide token registry, which is a new store nobody asked for. Rejected: checking with `State`, which would add polls to the scripted hosts that count them (session_test.go:30-37).
- **Where the name is persisted.** Picked: an `agent-named` event appended immediately before `Host.Start`, by `SessionManager.start` for steps and by `ReviewHalf.open` for reviewers. This follows the invariant that a record goes to disk before the action it describes. Rejected: an `agent` field on `step` events. Those are emitted only after `Started`, so a driver killed between `Host.Start` and that event would leave the name unrecorded.
- **Where the label goes.** Picked: before the token (`rloop-test-2kuxv-p3-plan`), so the `rloop-<label>-` prefix stays where `4077431` put it.
- **How names are capped.** Picked: one `agentName(prefix, suffix)` for steps and reviewers. When a name is too long, it keeps the suffix, cuts the prefix's tail and adds `-<shortHash(prefix)>`. Rejected: today's plain cut, because once the token is added, two reviewers of one round lose their ids and clash.

## Changes

1. **`internal/core/session.go`** (modify). Serves: agent names unique per run, collisions detected, one cap, name recorded before start, label kept.
   - Delete `AgentBase` (session.go:175-180). `resume.go` stops using it (change 3).
   - Add `func shortHash(s string) string`. It takes `fnv.New32a()` over `s` (the same hash as `WatchdogName`, watchdog.go:54-56), then `strconv.FormatUint(uint64(sum%60466176), 36)`, left-padded with `"0"` to 5 characters. Import `hash/fnv` and `strconv`.
   - Add `func (m *SessionManager) agentBase(key StepKey, salt int) string`. It sets `seed := m.Repo.Root() + "\n" + key.Run`, and appends `"\n" + strconv.Itoa(salt)` when `salt > 0`. It returns `"rloop-" + [m.Label + "-" when m.Label != ""] + shortHash(seed) + "-p" + key.Phase + "-" + key.Kind`.
   - Add `func (m *SessionManager) freeAgent(key StepKey, tail, suffix string) (string, error)`. For `salt` 0, 1 and 2 it builds `name := agentName(m.agentBase(key, salt)+tail, suffix)`. It returns `name` as soon as `m.Host.AgentPane(name)` returns `""`, and returns the error when `AgentPane` errors. After the third taken name it returns `fmt.Errorf("agent name %s taken, and its alternates", <the salt-0 name>)`.
   - In `Spawn` (session.go:149-152), keep the existing `key.Attempt > 1` conditional, but have it build a local `suffix` (`""`, or `fmt.Sprintf("-a%d", key.Attempt)`), then set `s.Agent, err = m.freeAgent(key, "", suffix)`. No new helper. On a `freeAgent` error, `Spawn` returns `s, fmt.Errorf("spawn: %w", err)`. This happens before `m.record(key, StepSpawned, "")`, so no workspace opens under a taken name. Gate (gate.go:83) and milestone (milestone.go:74) steps spawn through `Spawn` too.
   - In `start` (session.go:197-206), just before `m.Host.Start`, append `Record{Kind: RecordEvent, At: m.now(), Event: &Event{At: <same>, Kind: "agent-named", Phase: key.Phase, Step: key.Kind, Fields: {"attempt": strconv.Itoa(key.Attempt), "agent": s.Agent}}}` to `m.Store` under `key.Run`. An append error is returned (so the spawn fails) and `Host.Start` is not called.
   - `stepLabel` and `labelPrefix` (session.go:182-195) are unchanged.
2. **`internal/core/review.go`** (modify). Serves: reviewer names unique per run, collisions detected, one cap, reviewers of one round kept apart, reviewer name recorded before start.
   - `agentName(prefix, suffix string)` (review.go:364-369): when `len(prefix)+len(suffix) <= maxAgentName`, it returns `prefix + suffix`. Otherwise `sum := shortHash(prefix)`, `room := maxAgentName - len(suffix) - len(sum) - 1`, and it returns `strings.TrimRight(prefix[:room], "-") + "-" + sum + suffix`. `maxAgentName` stays at 32.
   - `reviewer` (review.go:275) no longer sets `Agent`.
   - In `open` (review.go:176-218), after `h.reviewer(...)` for row `i`, call `name, err := sm.freeAgent(worker.Ref.Key, "-rv-"+rv.ID(), agentSuffix(rd.n, worker.Ref.Key.Attempt))`. On error it returns `nil, sm.fail(worker, "reviewer "+rv.ID()+": "+err.Error())`. Otherwise it sets `sessions[i].Agent = name`.
   - In the start loop (review.go:195-205), just before `sm.Host.Start` for each reviewer, append `h.event(worker, "agent-named", map[string]string{"attempt": strconv.Itoa(worker.Ref.Key.Attempt), "agent": s.Agent, "reviewer": s.Reviewer, "round": strconv.Itoa(rd.n)})`. `h.event` (review.go:353) adds `step`. An append error returns `nil, sm.fail(worker, "record: "+err.Error())`.
3. **`internal/app/resume.go`** (modify). Serves: resume finds step and reviewer agents under the new names and the old ones.
   - Add `func legacyAgent(label, phase, kind, attempt string) string`. It returns `"rloop-p"+phase+"-"+kind`, or `"rloop-"+label+"-p"+phase+"-"+kind` when `label != ""`. When `strconv.Atoi(attempt)` is > 1, it appends `"-a"+attempt`. This is today's scheme (resume.go:158-161).
   - Add `func legacyReviewer(label, phase, kind, id, round, attempt string) string`. It builds `prefix := "rloop-p"+phase+"-"+kind+"-rv-"+id`, with `"rloop-"+label+"-p…"` when a label is set, and `suffix := "-r"+round`, with `"-a"+attempt` appended when the attempt is > 1. When `len(prefix)+len(suffix) > 32`, the prefix becomes `strings.TrimRight(prefix[:32-len(suffix)], "-")`. It returns `prefix + suffix`, which is the pre-change `agentName` exactly.
   - Add `func previousAgents(run core.RunState, phase, kind, attempt, label string, reviewers []string) []string`. It collects the `agent` of every `agent-named` event with `e.Phase == phase && e.Step == kind && e.Fields["attempt"] == attempt`, the step's first and then its reviewers in event order. When none exist (a pre-change run), it returns `legacyAgent(...)`. That is followed by `legacyReviewer(label, phase, kind, id, round, attempt)` for each `id` in `reviewers`, where `round` is the `round` of the last `review-round` event with `e.Step == kind && e.Fields["attempt"] == attempt`. No reviewers are added when there is no such event.
   - `stopStale` (resume.go:152-179): `kind, attempt := lastStep(run, phase)`. The reviewer ids are `core.Reviewer(rv).ID()` for each `rv` in `w.Config.Steps[kind].Reviewers`, the same conversion as wire.go:523. The existing state check / `stale-interrupted` event / `Interrupt` / print body runs for each agent of `previousAgents(run, phase, kind, attempt, w.Config.Label, ids)`, in order, with today's error codes and messages.
   - `previousSession` (resume.go:279-291): for each `step` event with a workspace, `agent` becomes the first entry of `previousAgents(run, phase, e.Step, e.Fields["attempt"], label, nil)`, and `ws = e.Fields["workspace"]`.
4. **`docs/task-loop-driver/tech-design.md`** (modify). The "Names and paths" bullet (tech-design.md:255-261) and the "Resume" bullet (tech-design.md:333) are updated to match the code.
   - Author agent `rloop-[<label>-]<token>-p<N>-<kind>[-a<attempt>]`, where `<token>` is 5 base36 characters of `fnv32a(<repo root>\n<runID>[\n<salt>])`, and salt 1 and 2 are used when herdr already has the name.
   - Reviewer `…-rv-<name>` + `-r<round>[-a<attempt>]`, and the cap with a 5-character prefix hash.
   - `Event{Kind: "agent-named", Fields{attempt, agent[, reviewer, round]}}` is appended before each `Host.Start`.
   - Resume interrupts every working or blocked recorded agent of the last attempt, or the pre-token names for a run that recorded none.
5. **Existing test fixtures** (modify, mechanical).
   - Rigs rooted at `/repo` (`newRig`, `newReviewRig`, every `fakeRepo{RootDir: "/repo"}`) use run `run-1`, so their token is `2kuxv`. Replace every generated `rloop-p<N>-…` literal with `rloop-2kuxv-p<N>-…`, and `rloop-test-p` with `rloop-test-2kuxv-p`. Reviewer literals take their capped value: codex r1 is `rloop-2kuxv-p3-implemen-8lgad-r1`, claude r1 is `rloop-2kuxv-p3-implemen-q1s68-r1`, and codex r1 at attempt 2 is `rloop-2kuxv-p3-imple-8lgad-r1-a2`. Any other value is the one the failing test prints, checked against `agentName`.
   - Tests that assert the exact shared call log or `Store.Append` counts gain the new `SessionHost.AgentPane <name>` and `Store.Append … event` entries at the positions changes 1 and 2 define.
   - `newLoopRig` (loop_test.go:149-152) roots its repo at `t.TempDir()`. Add `var tokenRe = regexp.MustCompile(`-[0-9a-z]{5}(-p[1-9])`)` and `func role(name string) string { return tokenRe.ReplaceAllString(name, "$1") }` to `internal/core/fakes_test.go`.
     - `agentSim` (loop_test.go:17-69) and the sims at loop_events_test.go:74 and :94 look up `behaviour` and `idle` by `role(agent)`.
     - In tests built on `newLoopRig` (`loop_test`, `loop_events_test`, `phasecheck_test`, `watchdog_test`, `verdict_test`, `workspaces_test`), every assertion that compares an agent name, or a call-log line that contains one, applies `role` to the actual value first. Expected literals keep the token-free names.
   - `internal/app/resume_test.go` `simHost` gets the same `tokenRe`/`role`.
     - `Start` and `Prompt` apply `role` to the name before they record it or look it up (`started`, `prompts`, `texts`, `opened`, `fail`, `hang`, `edit`).
     - `Prompt` detects a reviewer by `findingsRe.MatchString(text)`, not `strings.Contains(agent, "-rv-")`.
     - Assertions on stdout of runs driven through the sim (resume_test.go:267, :379, and any in `unattended_test.go`/`ask_test.go`/`watchdog_test.go` that the suite shows failing) compare against `role(f.out.String())`.
     - Seeded runs (`seedKilledImplement`) record no `agent-named` event, which is how a pre-change run looks, so their literals stay.
   - `internal/herdr/client_test.go` is unchanged.

## Tests

Write these first. Tokens under root `/repo`:
- `run-1` → `2kuxv`, with salt 1 → `uiz4e` and salt 2 → `kjdff`
- `run-7` → `qih3p`
- root `/other`, `run-1` → `jkmip`

`internal/core/session_test.go`
- `TestTwoRunsNameTheSameStepApart` (new): spawns `r.ref(1)` and the same ref with `Key.Run = "run-7"`. The agents are `rloop-2kuxv-p3-implement` and `rloop-qih3p-p3-implement`. Covers: two unlabelled runs never clash, whatever their phase numbers.
- `TestTheSameRunIDInAnotherRepositoryIsNamedApart` (new): the rig's `fakeRepo.RootDir = "/other"`, and the agent is `rloop-jkmip-p3-implement`. Covers: runs in different repos never clash.
- `TestASpawnWhoseNameIsTakenMovesToTheNextToken` (new): `r.host.Panes = {"rloop-2kuxv-p3-implement": "pane-9"}`, and the agent is `rloop-uiz4e-p3-implement`. Covers: a hash collision is detected and avoided.
- `TestASpawnWithEveryAlternateNameTakenFails` (new): `Panes` holds `rloop-2kuxv-p3-implement`, `rloop-uiz4e-p3-implement` and `rloop-kjdff-p3-implement`. `Spawn` returns `spawn: agent name rloop-2kuxv-p3-implement taken, and its alternates`. `SessionHost.Open` and `SessionHost.Start` are never called, and no step record was appended. Covers: the error path of collision handling.
- `TestASpawnFailsWhenHerdrCannotBeAskedForTheName` (new): `r.host.Err = errors.New("herdr down")` before `Spawn`. `Spawn` returns an error wrapping `herdr down` with the `spawn: ` prefix, and `SessionHost.Start` is never called. Covers: the herdr error path of the check.
- `TestSpawnRecordsTheAgentNameBeforeStartingIt` (new): in the shared call log, `Store.Append run-1 event` comes before `SessionHost.Start pane-1 rloop-2kuxv-p3-implement`. The appended event is `agent-named` with `Phase "3"`, `Step "implement"`, `Fields{"attempt": "1", "agent": "rloop-2kuxv-p3-implement"}`. Covers: the name is persisted before the start.
- `TestASpawnWhoseNameCannotBeRecordedNeverStartsTheAgent` (new): `r.sm.Store = failingEventStore{fakeStore: r.store, kind: "agent-named"}` (review_test.go:657-667). `Spawn` returns an error containing `disk full`, and `SessionHost.Start` is never called. Covers: the error path of the record.
- `TestAttemptTwoGetsTheSuffixedNameAndSentinel` (updated): `rloop-2kuxv-p3-implement-a2`, workspace `◆ p3 implement·a2`.
- `TestALabelledRunNamesTheStepAgentAndWorkspaceWithTheLabel` (updated): `rloop-test-2kuxv-p3-implement`, workspace `◆ test p3 implement`. Covers: a labelled run keeps its label.
- `TestALabelledRetryKeepsTheAttemptSuffix` (updated): `rloop-test-2kuxv-p3-implement-a2` (32 characters), workspace `◆ test p3 implement·a2`.
- `TestALongStepNameIsCutToTheLimitKeepingTheRunTokenAndAttempt` (new): label `test`, `Key.Phase = "12"`, attempt 2 → `rloop-test-2kuxv-p12-im-4cdh5-a2`, 32 characters. Covers: step names are capped.
- `TestTwoRunsWithALongLabelStillNameTheStepApart` (new): label `a-very-long-label-name`, runs `run-1`/`run-7` → `rloop-a-very-long-label-na-hykpv` / `rloop-a-very-long-label-na-bzyxw`. Covers: names stay unique when the cut hides the token.
- `TestAGateStepIsNamedWithTheRunToken` (new): `Key.Phase = "1"`, `Key.Kind = "gate"`, `Kind.Name = "gate"` → `rloop-2kuxv-p1-gate`. Covers: gate agents carry the run token.

`internal/core/review_test.go`
- `TestALabelledRunNamesTheReviewerWithTheLabel` (updated): `SessionHost.Start pane-2 rloop-test-2kuxv-p3-imp-v9mk8-r1 codex`.
- `TestReviewersOfOneRoundGetDistinctNamesWithinTheLimit` (new): `newReviewRig(t, Reviewer{Provider: "claude"}, Reviewer{Provider: "codex"})`. The `SessionHost.Start` calls name `rloop-2kuxv-p3-implemen-q1s68-r1` and `rloop-2kuxv-p3-implemen-8lgad-r1`. Covers: reviewers are capped and do not clash within a round.
- `TestAReviewerOfARetriedStepKeepsRoundAndAttempt` (new): `r.worker.Ref.Key.Attempt = 2` before `r.run()`, codex → `rloop-2kuxv-p3-imple-8lgad-r1-a2`.
- `TestTwoRunsNameTheSameReviewerApart` (new): `r.worker.Ref.Key.Run = "run-7"`, codex → `rloop-qih3p-p3-implemen-an2ns-r1`.
- `TestAReviewerWhoseNameIsTakenMovesToTheNextToken` (new): `r.host.Panes["rloop-2kuxv-p3-implemen-8lgad-r1"] = "pane-9"`, codex → starts `rloop-uiz4e-p3-implemen-y9gc2-r1`.
- `TestAReviewerIsRecordedBeforeItStarts` (new): codex reviewer. An `agent-named` event with `Fields{"attempt": "1", "agent": "rloop-2kuxv-p3-implemen-8lgad-r1", "reviewer": "codex", "round": "1", "step": "implement"}` is appended, and its `Store.Append` precedes that reviewer's `SessionHost.Start` in the shared call log.

`internal/app/resume_test.go`

The `…-rv-…` names in the tests below are seeded data, not names the code builds.
- `TestResumeInterruptsTheRecordedStepAgent` (new): calls `seedKilledImplement()`, then appends `ev(t0, "agent-named", 1, "implement", {"attempt": "1", "agent": "rloop-k3x9q-p1-implement"})` via `store.New(f.root).Append`. The herdr script answers `working` only for `agent get rloop-k3x9q-p1-implement`. Expected results:
  - herdr calls include `agent send-keys rloop-k3x9q-p1-implement esc`
  - the `stale-interrupted` event has `agent == "rloop-k3x9q-p1-implement"`
  - stdout has `interrupted previous session rloop-k3x9q-p1-implement: still working` and `previous session rloop-k3x9q-p1-implement left in workspace w2`

  Covers: a new run's step agent is found.
- `TestResumeFindsTheRecordedAgentOfTheLastAttempt` (new): the seed, plus `agent-named` events for attempt `1` (`rloop-k3x9q-p1-implement`) and attempt `2` (`rloop-k3x9q-p1-implement-a2`), plus a `step` event `{"state": "running", "attempt": "2", "workspace": "w3"}`. herdr answers `working` for the `-a2` name only, and `stale-interrupted` names `rloop-k3x9q-p1-implement-a2`.
- `TestResumeInterruptsARecordedReviewerStillWorking` (new): `newResumeFixture(t, reviewConfig)` and the seed, plus a `review-round` event `{"round": "2", "tree": <seed baseline>, "attempt": "1"}` and `agent-named` events for the step (`rloop-k3x9q-p1-implement`) and reviewer `claude` round 2 (`rloop-k3x9q-p1-implemen-abcde-r2`). herdr answers `working` only for the reviewer name, and it is sent `esc`. One `stale-interrupted` event names it, and the step agent (answered `{}`) is left alone. Covers: resume finds a new run's reviewer agents.
- `TestResumeOfARunStartedBeforeTheChangeInterruptsItsLegacyReviewer` (new): `reviewConfig`, the seed and the same `review-round` event, with no `agent-named` events. herdr answers `working` only for `agent get rloop-p1-implement-rv-claude-r2`, and `stale-interrupted` names it. herdr was also asked for `rloop-p1-implement` and `rloop-p1-implement-rv-ui-r2`. Covers: resume finds a pre-change run's reviewer agents.
- `TestResumeCrashedRightAfterStartFindsTheRecordedAgent` (new): the seed with its `step` event, plus an `agent-named` event (`rloop-k3x9q-p1-implement`, attempt `1`) appended after the step event. herdr answers `working` for that name, and it is interrupted. This models a driver that died after `Host.Start` and before `Started`. Covers: the crash window.
- `TestAResumedRunFindsTheStepAgentItNamedItself` (new): runs a first run through `newSim()` with `first.fail["rloop-p1-implement"] = true` and `--phases 1`, then `f.resume(newSim())`. Expected results:
  - `f.load(id)` holds an `agent-named` event for phase 1 `implement` whose `agent` matches `^rloop-[0-9a-z]{5}-p1-implement$`
  - the `stale-interrupted` event's `agent` equals it
  - raw stdout has `previous session <that agent> left in workspace w2`

  Covers: resume of a new-scheme run, end to end.
- `TestResumeInterruptsTheKilledDriversStillWorkingStepAgentBeforeItClaims` (existing, unchanged): the pre-change unlabelled step agent is still found.
- `TestResumeOfALabelledRunInterruptsTheLabelledStepAgent` (existing, unchanged): the pre-change labelled step agent is still found.
- `TestResumeAfterFailedImplementRerunsOnlyImplementAsAttempt2OverItsWork` (updated to `role`): the banner still names the previous session.

## Left out

- A run token in workspace labels: the items require unique agent names only, and the label from `4077431` already marks test runs in the sidebar.
- Changing `WatchdogName` or the intake name: `rloop-wd-<runID>` (watchdog.go:46-60) and `rloop-intake-<pid>` (intake.go:105) are unchanged by this item.
- Telling the watchdog prompt its own token: no item asks for it.
- A length limit on `label`: `4077431` already cut the label inside capped names (review.go:275, :364-369), so a cut label is how it built them. The workspace label always carries the full label.
- A config key for the token, the cap or the number of alternates: no obligation needs one.
- An `agent` field on `step` events: the `agent-named` event covers every lookup, including the crash window.

## Assumptions

- "Unique to its run" means unique for each (repository root, run ID). Run IDs are unique only per repository (store.go:62-73). Uniqueness among live agents is enforced by the `AgentPane` check. Two drivers checking the same name in the same instant stay caught by herdr's own refusal of a duplicate name at `Start`.
- Three tokens (salt 0-2) are enough: a second collision on the same name is not a realistic input. The error names the salt-0 name so the maintainer can find what holds it.
- Watchdog warning 1: resolved. Steps (with gate and milestone) and reviewers share `agentName` and the 32-character cap. The cut keeps the round/attempt suffix, and the full prefix, token included, goes into the appended hash. The token stays visible whenever the label is at most 8 characters, which holds for the sandbox's `label: test`.
- Watchdog warning 2: resolved by the `agent-named` event. A run with none is a pre-change run, and resume rebuilds its names the old way with today's `label` config, as resume.go:158 already does.
- The `AgentPane` check followed by `Start` is not an atomic reservation, and no herdr call reserves a name (ports.go:46 offers only lookups). A clash would need two runs whose names hash to the same 5-character token (1 in 36⁵ per pair) and that both spawn that phase and kind between one check and the other's `Start`. herdr then refuses the second `Start`, and that step fails through the existing `spawn:` path. The phase adds no reservation store for this case. Within one review batch, reviewer prefixes differ by `-rv-<id>`, so their names match only through a hash collision too.
- A pre-change run's legacy names are rebuilt from today's `label` and reviewer config, the same source resume.go:158 uses now and the config `Wire` gives the resumed run. The run's saved resolved config (preflight.go:57-61) is not parsed on resume. That would be a new read path, and it only matters if the maintainer edits `label` or reviewers between the crash and the resume, which this item does not cover.
- Resume rebuilds a pre-change run's reviewers from today's config for the step. A reviewer that was skipped or renamed since is looked up, gets `gone`, and is left alone.
- Test fixture edits outside the files the watchdog listed (`fakes_test.go`, the other core `_test.go` files, the app sim) are required, because every hard-coded agent name changes. `tech-design.md` is updated to match the code.

## Gate

`go test ./internal/core/ ./internal/app/ -run '^(TestTwoRunsNameTheSameStepApart|TestTheSameRunIDInAnotherRepositoryIsNamedApart|TestASpawnWhoseNameIsTakenMovesToTheNextToken|TestASpawnWithEveryAlternateNameTakenFails|TestASpawnFailsWhenHerdrCannotBeAskedForTheName|TestSpawnRecordsTheAgentNameBeforeStartingIt|TestASpawnWhoseNameCannotBeRecordedNeverStartsTheAgent|TestAttemptTwoGetsTheSuffixedNameAndSentinel|TestALabelledRunNamesTheStepAgentAndWorkspaceWithTheLabel|TestALabelledRetryKeepsTheAttemptSuffix|TestALongStepNameIsCutToTheLimitKeepingTheRunTokenAndAttempt|TestTwoRunsWithALongLabelStillNameTheStepApart|TestAGateStepIsNamedWithTheRunToken|TestALabelledRunNamesTheReviewerWithTheLabel|TestReviewersOfOneRoundGetDistinctNamesWithinTheLimit|TestAReviewerOfARetriedStepKeepsRoundAndAttempt|TestTwoRunsNameTheSameReviewerApart|TestAReviewerWhoseNameIsTakenMovesToTheNextToken|TestAReviewerIsRecordedBeforeItStarts|TestResumeInterruptsTheRecordedStepAgent|TestResumeFindsTheRecordedAgentOfTheLastAttempt|TestResumeInterruptsARecordedReviewerStillWorking|TestResumeOfARunStartedBeforeTheChangeInterruptsItsLegacyReviewer|TestResumeCrashedRightAfterStartFindsTheRecordedAgent|TestAResumedRunFindsTheStepAgentItNamedItself|TestResumeInterruptsTheKilledDriversStillWorkingStepAgentBeforeItClaims|TestResumeOfALabelledRunInterruptsTheLabelledStepAgent|TestResumeAfterFailedImplementRerunsOnlyImplementAsAttempt2OverItsWork)$'`
