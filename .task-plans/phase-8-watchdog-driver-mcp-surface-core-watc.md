status: planned

## Summary

Two fixes on the watchdog's side of the run.

#8: every watchdog tool except the read-only `run_status` and `step_info` gets a second event in `events.jsonl`, `watchdog-result`, appended after its handler returns. It holds `tool`, then `accepted` (`"true"`/`"false"`) or `decision`, then `reason` when there is one. The existing `watchdog-call` event is still appended before the handler. All of this lives in one generic wrapper, `watchdogTool`, which replaces `trackTool` on those 14 tools. It reads the outcome through two small methods on the three output types, so refusals, `noTriage`, `notAvailable`, `resume()` errors and the empty-question refusal are all recorded from one place. If the result itself cannot be recorded, the call's outcome stands and the append error is added to the call's `reason`, so the error is not swallowed.

#13: `markWaiting` no longer calls `host.Focus`, in any mode, and the now-unused `herdr.Client.Focus` is deleted. `watchdog-waiting` is still recorded and emitted exactly as before.

Choices:
- **Where the result is recorded:** one generic wrapper around the handlers, chosen over a `recordResult` call at each of the ~40 `return` statements. The wrapper sees every return path, including ones added later, and it leaves the handlers alone.
- **How the wrapper reads the outcome:** an `outcome()` method plus a `withReason()` method on `acceptedOutput`, `decisionOutput` and `tableOutput`, as a generic constraint. A type switch on `any(out)` was rejected because it still needs a way to write the reason back into `Out`. Passing an extractor function per tool was rejected because it repeats the same three functions at 14 call sites.
- **Tool name in the result:** read from `req.Params.Name`, not passed as an argument, so the name the SDK dispatched on cannot drift from a copied string.
- **A result that cannot be recorded:** the error goes into the call's `reason` and `accepted`/`decision` are kept. Turning it into a tool error was rejected: the action has already happened, and an error invites the watchdog to repeat it. Dropping the error silently was rejected because it swallows it.
- **Focus (#13):** the call is removed in every mode, rather than gated on `Unattended` or a config key. tech-design.md:335-336 says focusing a pane is something r-loop never does, because focusing clears herdr's `done` state, which the driver reads.

## Why this approach

The items proposed the outcome only. The triage proposed, for #8, "a watchdog-result event … via one wrapper around the handlers rather than 14 call sites; skip read-only run_status/step_info; keep the watchdog-call event before the handler; one line in tech-design.md". For #13 it proposed removing the Focus call and the dead `Client.Focus`, and replacing the focus tests with one that asserts Focus is never called. This plan follows both proposals and adds what they leave open:

- **A result that cannot be recorded is not dropped.** `Server.record` already treats a failed append as a refusal (internal/askmcp/watchdog.go:228-230). A result is appended after the action has happened, so it cannot be refused any more. It is reported in `reason` instead.
- **Why one wrapper:** refusals reach the agent from many places. The empty-question check (internal/askmcp/watchdog.go:425-427), absent handlers (:242-244, :259-261, :447-449, :464-466 …), `resume()` errors (:231-233 …) and handler refusals (:451-452) each return directly. Only a wrapper sees all of them. `trackTool` (internal/askmcp/server.go:158-176) already wraps every tool, so the new wrapper nests inside it and keeps the stop and panic handling.
- **Why `run_status` and `step_info` are skipped:** they change nothing, and existing tests pin that they record nothing (internal/askmcp/watchdog_test.go:927, :980).
- **Why #13 is not gated:** gating Focus on `Unattended` would keep a call that tech-design.md:335-336 and spec.html:1971 say r-loop never makes. The only Focus caller is internal/core/watchdog.go:400-406, and `herdr.Client.Focus` (internal/herdr/client.go:480-483) has no other caller, so both go.

Files outside the phase's named files:
- `internal/herdr/client.go`: removes the dead `Client.Focus`, which existed only for the removed call (commit 3757253).
- `docs/task-loop-driver/tech-design.md`: the watchdog-surface contract at :748 lists what every call records. The new event belongs there.

## Changes

1. **Modify `internal/askmcp/watchdog.go`** (#8)
   - Add `"strconv"` to the imports.
   - Add the constraint:
     ```go
     type toolOutput[Out any] interface {
     	outcome() (key, value, reason string)
     	withReason(reason string) Out
     }
     ```
   - Add value-receiver methods next to each output type:
     - `acceptedOutput.outcome()` returns `"accepted", strconv.FormatBool(o.Accepted), o.Reason`.
     - `decisionOutput.outcome()` returns `"decision", o.Decision, o.Reason`.
     - `tableOutput.outcome()` returns `"accepted", strconv.FormatBool(o.Accepted), o.Reason`. `Table` is not recorded.
     - Each type's `withReason(reason string)` sets `o.Reason = reason` and returns `o`.
   - Add the wrapper:
     ```go
     func watchdogTool[In any, Out toolOutput[Out]](s *Server, h mcp.ToolHandlerFor[In, Out]) mcp.ToolHandlerFor[In, Out] {
     	return trackTool(s, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
     		res, out, err := h(ctx, req, in)
     		key, value, reason := out.outcome()
     		if rerr := s.recordResult(req.Params.Name, key, value, reason); rerr != nil {
     			if reason != "" {
     				reason += "; "
     			}
     			out = out.withReason(reason + rerr.Error())
     		}
     		return res, out, err
     	})
     }
     ```
     For `submit_triage` and `submit_gate`, `res` keeps its text (the table, or `Refused: …`). The SDK still marshals the modified `out` into `StructuredContent` (go-sdk v1.8.0 mcp/server.go:436-468), so the error reaches the structured `reason`.
   - In `watchdogServer`, replace `trackTool(s,` with `watchdogTool(s,` for all 14 state-changing tools: `signal`, `choose_gate`, `propose_remedy`, `set_config`, `stop_run`, `pause_run`, `continue_run`, `restart_step`, `answer_question`, `answer_dialog`, `resolve_blocker`, `ask_maintainer`, `submit_triage`, `submit_gate`. `run_status` (:474) and `step_info` (:488) keep `trackTool`. Handler bodies are unchanged; each keeps its `s.record(...)` first, so `watchdog-call` is still appended before the handler.
   - Split the append out of `record` (:548-559) into `func (s *Server) appendEvent(kind, step string, fields map[string]string) error`. It returns nil when `s.Store == nil`, and otherwise builds `core.Event{At: now, Kind: kind, Step: step, Fields: fields}` and returns `s.Store.Append(...)`'s raw error.
     - `record` sets `fields["tool"] = tool` and calls `appendEvent("watchdog-call", step, fields)`, wrapping an error as `record %s: %w`. Its behaviour does not change.
     - Add `func (s *Server) recordResult(tool, key, value, reason string) error`. It builds `fields := map[string]string{"tool": tool, key: value}`, adds `fields["reason"] = reason` only when `reason != ""`, calls `appendEvent("watchdog-result", "", fields)`, and wraps an error as `record %s result: %w`.

2. **Modify `internal/askmcp/watchdog_test.go`**: test-fake support, plus the existing tests whose record counts the new event shifts. (#8)
   - Add the field `failEvent string` to `memStore`. In `Append`, when `rec.Event != nil && rec.Event.Kind == m.failEvent`, return `errors.New("disk full")` without storing. The zero value keeps today's behaviour.
   - `TestEveryCallIsRecordedBeforeItsHandlerRuns` (:421-424): `len(all) != 2 || all[1]…` becomes `len(all) != 4 || all[2]…`.
   - `TestMaintainerSaidReachesTheHandlersAndIsRecorded` (:468): `len(recs) != 2 … recs[1]` becomes `len(recs) != 4 … recs[2]`.
   - `TestSubmitTriageHandsThePayloadToItsHandlerAndReturnsTheTable` (:727): `len(recs) != 2` becomes `len(recs) != 4`.
   - `TestRunControlToolsAreRecordedResumeAndReachTheirHandlers` (:1021): `len(recs) != 3 … recs[2]` becomes `len(recs) != 6 … recs[4]`. The `records=1` in `want` (:1016) is unchanged, because it is counted before the first call's result.

3. **Modify `internal/core/watchdog.go`** (#13): in `markWaiting` (:385-408), delete lines 400-406, the `host, ok := d.Host.(interface{ Focus(string) error })` block and its `warning` emit, and return the `d.emit("watchdog-waiting", …)` result directly: `return d.emit("watchdog-waiting", fields, func() { d.asking, d.blocked = true, byPrompt })`. Nothing else in the wait/resume logic changes.

4. **Modify `internal/herdr/client.go`** (#13): delete `func (c Client) Focus(agent string) error` (:480-483). It has no other caller.

5. **Modify `internal/core/watchdog_test.go`** (#13)
   - Delete `TestAWatchdogAskingTheMaintainerTakesFocusOncePerQuestion` (:1355-1377), `TestAWatchdogBlockedOnAPromptTakesFocus` (:1379-1392) and `TestAFailedFocusIsAWarningNotAnError` (:1394-1408).
   - Remove the `err` field from `focusHost` (:1344-1353), so that `Focus` only appends to `focused` and returns nil. The new test uses it to prove Focus is never called.

6. **Modify `docs/task-loop-driver/tech-design.md`** (#8): at :748, after "Every call is recorded as `watchdog-call` before its handler runs.", add: "Every call but `run_status` and `step_info` also records `watchdog-result` after its handler returns, with `tool`, `accepted` or `decision`, and `reason` when there is one: a refusal, `no triage is open`, `not available` or a failed `Resume` is recorded as the call's result. A result that cannot be recorded leaves the outcome as it is and adds `record <tool> result: <err>` to the call's `reason`."

## Tests

In `internal/askmcp/watchdog_test.go`, through the real MCP client (`serveWatchdog`, `connect`, `call`) with `memStore`. Each row uses its own server and store.

Call arguments, used wherever a tool appears below:

| Tool | Arguments |
|---|---|
| `signal` | `{"kind":"warn","step":"phase-3/implement","reason":"r","evidence":"e"}` |
| `submit_triage` | `{"phases":[{"phase":"3","status":"build"}]}` |
| `submit_gate` | `{"decision":"go","maintainer_said":"go"}` |
| `propose_remedy` | `{"class":"shell","command":"c","why":"w"}` |
| `resolve_blocker` | `{"id":"b1","action":"retry"}` |
| `answer_question` | `{"id":"q1","answer":"a","citation":"maintainer"}` |
| `set_config` | `{"key":"steps.implement.model","value":"x","why":"w"}` |
| `restart_step` | `{"step":"phase-3/implement"}` |

- `TestARefusedSubmitTriageRecordsItsResultWithTheReason`
  - **Given:** a `SubmitTriage` handler returning `false, "item 4 has no verdict", ""`.
  - **When:** `submit_triage` is called with one phase verdict.
  - **Then:** `st.records()` has exactly 2 records. Record 0 is `watchdog-call` with `tool` `submit_triage`. Record 1 has Kind `watchdog-result`, Step `""` and Fields equal to `{"tool":"submit_triage","accepted":"false","reason":"item 4 has no verdict"}`.
  - Covers #8 item 1.

- `TestAnAcceptedWatchdogCallRecordsItsResult`: a table. Each row sets one handler, calls one tool, and asserts that `st.records()[1]` has Kind `watchdog-result` and Fields equal to `expectedFields`. Covers #8 item 2.

  | Row | Handler and call | `expectedFields` |
  |---|---|---|
  | signal | `Signal` returns `true, ""` | `{"tool":"signal","accepted":"true"}` |
  | ask_maintainer | `AskMaintainer` returns nil, question `"which store?"` | `{"tool":"ask_maintainer","accepted":"true"}` |
  | submit_gate | `SubmitGate` returns `true, "", "\| G \|"` | `{"tool":"submit_gate","accepted":"true"}` |
  | propose_remedy | `Propose` returns `"ask", "class shell needs the maintainer"` | `{"tool":"propose_remedy","decision":"ask","reason":"class shell needs the maintainer"}` |
  | resolve_blocker | `ResolveBlocker` returns `"authorised", ""` | `{"tool":"resolve_blocker","decision":"authorised"}` |

- `TestAnErrorBeforeTheHandlerIsRecordedAsTheCallsResult`: a table. Each row asserts that `st.records()[1]` has Kind `watchdog-result` and Fields equal to `expectedFields`. Covers #8 item 3.

  | Row | Setup and call | `expectedFields` |
  |---|---|---|
  | resume error | `Resume` returns `errors.New("record watchdog-resumed: disk full")`; `answer_question` | `{"tool":"answer_question","accepted":"false","reason":"record watchdog-resumed: disk full"}` |
  | no triage open | no handlers; `submit_triage` | `{"tool":"submit_triage","accepted":"false","reason":"no triage is open"}` |
  | accepted tool, no handler | no handlers; `signal` | `{"tool":"signal","accepted":"false","reason":"not available"}` |
  | decision tool, no handler | no handlers; `set_config` | `{"tool":"set_config","decision":"refused","reason":"not available"}` |
  | empty question | `ask_maintainer` with question `"  "` | `{"tool":"ask_maintainer","accepted":"false","reason":"question is empty"}` |

- `TestTheCallIsRecordedBeforeItsHandlerAndItsResultAfter`
  - **Given:** a `Restart` handler that snapshots `st.records()` and returns `false, "no authorised remedy"`.
  - **When:** `restart_step` is called with step `phase-3/implement`.
  - **Then:** the snapshot's event Kinds are exactly `["watchdog-call"]`, and after the call they are exactly `["watchdog-call","watchdog-result"]`.
  - Covers #8 item 4 and the order with the result.

- `TestAResultThatCannotBeRecordedIsAddedToTheReason`
  - **Given:** `memStore{failEvent: "watchdog-result"}` and a `Restart` handler returning `true, ""`.
  - **When:** `restart_step` is called.
  - **Then:** the output is `accepted == true` with `reason == "record restart_step result: disk full"`, and `st.records()` holds only the one `watchdog-call` record.
  - Covers the failed result append: the error is not swallowed and the outcome stands.

In `internal/core/watchdog_test.go`:

- `TestEnteringWatchdogWaitingNeverFocusesTheWatchdogPane`: a table. Each row builds `host := &focusHost{askingHost: askingHost{blockedFor: …}}`, a `fakeStore`, a `fakeFace` and `newWatchdog(host, store, ProviderArgs{Kind: "claude"})` with `dog.Unattended` set, then runs `when(dog)`. It asserts that `when` returns nil, that `host.focused` is `[]string(nil)`, and that `recordedKinds(store)` and `emittedKinds(face)` both equal `expectedKinds`, so there is no `warning` and no other side effect. Covers all three #13 items.

  | Row | `Unattended` | `when` | `expectedKinds` |
  |---|---|---|---|
  | asks the maintainer | false | `dog.AskMaintainer("which store?", nil, "")` | `["watchdog-waiting"]` |
  | asks the maintainer, unattended | true | same as above | `["watchdog-waiting"]` |
  | herdr reports it blocked | false | `blockedFor: 1`, `host.States = {dogName: AgentBlocked}`, `dog.Notify("step started phase-2/implement", false, 0)` | `["watchdog-waiting","watchdog-resumed"]` |
  | herdr reports it blocked, unattended | true | same as above | `["watchdog-waiting","watchdog-resumed"]` |

- `TestWatchdogWaitingKeepsTheQuestionInTheStoreAndOnTheFace`
  - **Given:** `newWatchdog(&focusHost{}, store, ProviderArgs{Kind: "claude"})` with a `fakeFace`.
  - **When:** `dog.AskMaintainer("retry phase-2/implement with the helper renamed?", []string{"yes", "no"}, "yes")` is called.
  - **Then:** the `Fields` of the stored record `store.Records["run-1"][0].Event` and of the emitted `face.Events[0]` each equal `{"question":"retry phase-2/implement with the helper renamed?","options":"yes; no","recommended":"yes"}`.
  - Covers #13 item 3: the waiting payload is recorded and emitted exactly as before.

The gate also runs these existing tests. Each pins a behaviour that this phase's changes touch or must keep:
- `TestRunStatusReturnsTheSnapshotWithoutRecordingOrResuming` and `TestStepInfoReturnsTheStepsAgentWorktreeAndBaseWithoutRecordingOrResuming`: read-only tools record nothing. Unchanged.
- `TestAskingTheMaintainerShowsTheQuestionUntilTheWatchdogsNextCall`, `TestNotifyWaitsOutAWatchdogAskingTheMaintainer` and `TestAWatchdogAskingThenBlockedOnTheMaintainerIsShownWaitingOnce`: the `watchdog-waiting`/`watchdog-resumed` wait semantics for #13 item 3. Unchanged.
- `TestEveryCallIsRecordedBeforeItsHandlerRuns`, `TestMaintainerSaidReachesTheHandlersAndIsRecorded`, `TestSubmitTriageHandsThePayloadToItsHandlerAndReturnsTheTable` and `TestRunControlToolsAreRecordedResumeAndReachTheirHandlers`: the `watchdog-call` event is still written before the handler (#8 item 4). Their record counts are adjusted in Changes step 2.

## Left out

- **A call id pairing `watchdog-call` with its `watchdog-result`:** no item asks for it. The result carries `tool` and is appended right after its own call's handler. Adding an id would mean threading it through `record` in all 14 handlers.
- **`step` on the result event:** the call event right before it already holds the step, and the generic wrapper does not know the input's step without one more interface on two input types.
- **The triage table in `submit_triage`/`submit_gate` results:** the items ask for accepted, decision and reason, and the table can be rebuilt from the recorded triage.
- **Recording a handler panic as a result:** `trackTool` already turns a panic into a tool error (internal/askmcp/server.go:169-173) and no item asks for it. Recording it would mean a second `recover` in the wrapper.
- **Emitting `watchdog-result` to the face:** `watchdog-call` is store-only too, and no item asks to show results.
- **An `Unattended` gate or a config key for focus:** focus is removed in every mode (tech-design.md:335-336).

## Assumptions

- **Watchdog warning on #13:** resolved as warned. Focus is dropped in every mode, not gated on `Unattended`, and `herdr.Client.Focus` is deleted along with the focus tests.
- **Watchdog warning on #8:** resolved as warned. One wrapper records a separate `watchdog-result` event after the handler, skips `run_status`/`step_info`, and adds a line to tech-design.md.
- **Field values:** `accepted` is recorded as the strings `"true"`/`"false"`, because `Event.Fields` is `map[string]string` (internal/core/types.go:155). `reason` is left out when it is empty, matching the outputs' `omitempty`.
- **No handler returns a non-nil error today,** so the wrapper records the `Out` it gets back unconditionally.
- **A result append that fails after the pre-handler `watchdog-call` append also failed** puts both errors in `reason`, joined by `; `.

## Gate

`go test ./internal/askmcp/ ./internal/core/ -run '^(TestARefusedSubmitTriageRecordsItsResultWithTheReason|TestAnAcceptedWatchdogCallRecordsItsResult|TestAnErrorBeforeTheHandlerIsRecordedAsTheCallsResult|TestTheCallIsRecordedBeforeItsHandlerAndItsResultAfter|TestAResultThatCannotBeRecordedIsAddedToTheReason|TestEnteringWatchdogWaitingNeverFocusesTheWatchdogPane|TestWatchdogWaitingKeepsTheQuestionInTheStoreAndOnTheFace|TestRunStatusReturnsTheSnapshotWithoutRecordingOrResuming|TestStepInfoReturnsTheStepsAgentWorktreeAndBaseWithoutRecordingOrResuming|TestAskingTheMaintainerShowsTheQuestionUntilTheWatchdogsNextCall|TestNotifyWaitsOutAWatchdogAskingTheMaintainer|TestAWatchdogAskingThenBlockedOnTheMaintainerIsShownWaitingOnce|TestEveryCallIsRecordedBeforeItsHandlerRuns|TestMaintainerSaidReachesTheHandlersAndIsRecorded|TestSubmitTriageHandsThePayloadToItsHandlerAndReturnsTheTable|TestRunControlToolsAreRecordedResumeAndReachTheirHandlers)$'`
