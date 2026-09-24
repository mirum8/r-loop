# Driver — backlog from the reliability review of 23 Sep 2026

This backlog comes from a review of the whole driver for reliability and stability: five reviewers,
one per area (core concurrency, crash safety and resume, external adapters, judgement and landing,
process lifecycle). The review had 34 findings, counting #10 as the two items #10a and #10b, and every
one is real work below. Where a finding's premise was partly wrong, and which items move
architecture or the estimate, is in `issues-reliability-review-2026-09-23-notes.md`. `[#n]` is the
review's own ranking: #1–#9 are P1, #10–#22 are P2 and #23–#33 are P3.

Verified against `r-loop` @ `main` `058c81b`.

- [x] [#1] Signals are only handled during intake: a kill during land leaves main mid-merge, the gate is orphaned, and the TUI's SIGTERM leaves the driver running headless  <!-- fixed: r-loop/phase-1 -->
      - While the driver is in any step or in land, SIGTERM, SIGHUP or SIGINT cancels the run context, records the run as halted with an interrupted reason, and exits non-zero. The process is never killed outright
      - A signal during a land merge or gate kills the gate's process group and aborts the merge (or resets to the pre-merge HEAD), so the primary tree is clean and `r-loop resume` can proceed
      - If the TUI program exits because of a signal or an error from `Run`, the run is halted or aborted as above; phases never keep running without a display
      - A test sends SIGTERM to a run blocked in a long gate command and asserts that the gate child is gone, the store holds a halted record, and the working tree is clean

- [x] [#2] herdr and git calls have no timeout: a wedged herdr server or a git hook, LFS filter or credential prompt hangs the whole run  <!-- fixed: r-loop/phase-1 -->
      - Every herdr CLI call fails with an error once a per-call timeout expires, and the step backstop and abort still work afterwards. Test: a fake herdr binary that sleeps forever
      - Every git invocation runs with `GIT_TERMINAL_PROMPT=0`, and a hanging git command (e.g. a hook that sleeps) ends with an error after a bounded timeout. Its process is killed, not leaked
      - A timed-out herdr or git call becomes a step failure or a halt reason that names the command and the timeout

- [x] [#3] Primary-tree operations assume a clean tree on the start branch: land's `add -A` sweeps in maintainer edits, and ResetHard/RemoveAll can destroy uncommitted work  <!-- fixed: r-loop/phase-3 -->
      - If a file outside the phase's changes is modified or untracked in the primary tree when land starts, land refuses with a named error, does not commit that file, and leaves its contents unchanged
      - If the primary tree's checked-out branch differs from the branch recorded at run start, land refuses before merging and names both branches
      - An untracked maintainer file that existed before a milestone report step still exists, byte for byte, after that report fails
      - Gate-probe discovery and land's tick/commit cleanup never discard an uncommitted maintainer edit; they refuse to start on a dirty tree instead

- [x] [#4] A new run silently reuses leftover `.r-loop/wt/phase-N` worktrees and `r-loop/phase-N` branches without checking their base  <!-- fixed: r-loop/phase-3 -->
      - If `.r-loop/wt/phase-N` or `r-loop/phase-N` already exists when a fresh run starts, the run refuses and names the leftover instead of reusing it
      - An existing `r-loop/phase-N` branch that is not based on the current base-branch HEAD is never reused as the phase's worktree branch
      - Uncommitted files in a leftover worktree never end up in a step commit of a fresh run
      - A phase that ends on the item-skipped path leaves no worktree and no phase branch behind

- [x] [#5] A torn last JSONL line plus the next append makes the run permanently unloadable  <!-- fixed: r-loop/phase-5 -->
      - Given a record file whose last line is a torn JSON prefix with no trailing newline, `Append` followed by `Load` returns every complete record plus the new one, at most with a warning about the torn bytes
      - After such an append, no line in the file merges the torn prefix with the new record
      - An unparseable line in the middle of a file still makes `Load` fail with an error naming the file and the line
      - `resume` and `status` on such a run exit with their normal codes, not 2

- [x] [#6] A dead watchdog pane (`agent_not_found`) is never marked gone, so later phases run and land with no watchdog  <!-- fixed: r-loop/phase-6 -->
      - When the watchdog's herdr agent no longer exists, the next Post, Notify or phase-check prompt marks the watchdog gone, emits one watchdog-unreachable event and calls OnGone exactly once
      - After the watchdog pane is killed, the run halts with "the watchdog is gone" before the next phase starts; no later phase is spawned or landed
      - A phase check that failed because the watchdog vanished is not reported as `phase-check-timeout`
      - A transient prompt error (not not-found and not blocked) still leaves the watchdog live

- [x] [#7] The gate command joins every backtick span in `Done when:` with `&&`, so expected-output literals run as commands and the gate is always red  <!-- fixed: r-loop/phase-7 -->
      - A `Done when:` line whose prose says a command "prints nothing" gives a gate that passes when the command prints nothing and fails when it prints something
      - A `Done when:` line saying a command prints `<literal>` gives a gate that passes only when the command's output contains that literal; the literal is never run as a command
      - A `Done when:` line whose spans are all commands keeps today's command string
      - The land contract in tech-design.md describes the new rule, and todo.md lines 518, 532, 548 and 590 each give a gate that passes on a correct tree (tested against those real lines)

- [x] [#8] The item gate only recognises Go-style test paths, so Java, Kotlin, Python and JS backlog items fail with "adds no test file"  <!-- fixed: r-loop/phase-7 -->
      - An item gate whose changed tests are `src/test/java/a/FooTest.java`, `app/FooTest.kt`, `tests/test_foo.py`, `web/foo.spec.ts` or `pkg/__tests__/x.js` gets past the "no test file" refusal, and those files are copied into the red worktree
      - Nested test directories (`module/src/test/…`, `sub/tests/…`, `sub/test/…`) count as test paths
      - Production files such as `src/main/java/Contest.java` and `latest.go` are still not test paths
      - The foreign-test-edit warning uses the same widened test-path rule

- [x] [#9] A halt that arrives while a step is spawning gives `failed` → `running` → `failed`, and the agent keeps working after the halt  <!-- fixed: r-loop/phase-9 -->
      - A halt for the phase that arrives while the step is spawning leaves exactly one terminal record (`failed`, "watchdog: <reason>") and no spawned or running record after it
      - A halt before or during Spawn leaves no agent running: no workspace opens after the halt, or an agent that already started is interrupted
      - If the runner already finished ok when the halt is processed, the step's final stored state matches the outcome the loop acts on; a step never has both an ok and a failed terminal record
      - A halt in the middle of a run still produces exactly one failed record and interrupts the live agent

- [x] [#10a] A step's commit and land's merge/gate/tick/commit happen before the record that describes them  <!-- fixed: r-loop/phase-10 -->
      - A crash injected between a step's worktree commit and its ok record does not lead resume to re-run already-committed work
      - A crash after the land merge but before the landing record leaves a record, written before the merge, that names the phase and the merge in progress; resume detects it and completes or aborts the merge and says which
      - A crash after the landing commit but before the landing record still shows the phase as landed on resume and in the report, with its merge SHA recovered
      - In the normal path the record order is intent, then action, then outcome, asserted with a recording fake store and repo

- [x] [#10b] A failed state append only shows a warning, and the driver keeps spawning, committing and landing without a record  <!-- fixed: r-loop/phase-10 -->
      - If appending a step-state record (running, stalled) fails, the step ends failed or the run halts with a "record:" reason
      - If appending a run-status record fails, the run halts non-zero and names the store error on stderr and in the face
      - A fake store that fails every append stops the run within one step; no further spawn, merge or commit happens
      - The policy for each record kind (fatal or warn) is decided and covered by a test

- [x] [#11] Abort is ignored during land and the phase check, and the TUI has no force-quit  <!-- fixed: r-loop/phase-1 -->
      - An abort requested while the land gate runs kills the gate's process group within a few seconds, reverts the in-progress merge, records the run as aborted and exits 1
      - An abort during a gate-fix round, gate probe, milestone report or phase-check wait takes effect within the poll interval
      - In the TUI, a second ctrl+c while the stop prompt is up (or after an abort was requested) force-quits: the run is recorded as aborted and the terminal is restored

- [x] [#12] The land-stage steps (gatefix, gate probe, milestone report) bypass the watch and question path  <!-- fixed: r-loop/phase-9 -->
      - When a gate-fix, gate-probe or milestone step calls `ask_watchdog`, the question is either routed to the watchdog with the answer delivered to that step, or withdrawn at once; it is never left unanswered
      - Once a land-stage step ends, no goroutine keeps polling for its question and the ask server holds no open question for it
      - A watchdog halt or warn naming a running land-stage step applies to that step or is rejected back to the caller
      - A held watchdog halt is never delivered to a step from a different phase than the one it named

- [x] [#13] Nothing recovers from panics: one panic in any goroutine kills a multi-hour run and leaves the terminal raw  <!-- fixed: r-loop/phase-14 -->
      - A panic inside a step runner becomes a failed step whose reason contains the panic value, and the process does not crash
      - A panic in a question, watch-tick or watchdog-deliver goroutine is recovered and recorded; if the run cannot continue, a halted record is written
      - After a fatal panic that was recovered, the TUI is stopped and the terminal restored before the process exits

- [x] [#14] `resume` always takes the newest run directory, even one left empty by a failed start, and accepts no run ID  <!-- fixed: r-loop/phase-5 -->
      - `r-loop resume <run-id>` resumes the named run even when a newer run directory exists; an unknown ID exits 2 and names it
      - With no ID, resume never picks a run that recorded no progress (for example, a start that failed before its first step)
      - `meta.json` is written atomically, and a run dir whose `meta.json` can't be read is skipped with a warning rather than failing `resume`/`status` when another run is valid
      - `status` accepts the same optional run ID and chooses the run by the same rule

- [x] [#15] A finished TUI run counts as live until `q`, so resume and new runs are refused  <!-- fixed: r-loop/phase-5 -->
      - As soon as the loop returns, the run's final status is recorded and `current` is cleared, before the TUI waits for `q`
      - While a finished TUI is still on screen, `r-loop resume` and a new run from another terminal do not refuse with "live in pid"
      - The TUI still stays open on the final state until `q`, and the report path is still printed after it closes

- [x] [#16] A passing gate that leaves a background child running is treated as an error (`WaitDelay`)  <!-- fixed: r-loop/phase-1 -->
      - A gate command that exits 0 but leaves a background process holding stdout lets the phase land
      - A gate that exits non-zero with a background child left is reported as a gate failure with its exit code, and gate-fix rounds run
      - A notify hook that exits 0 but leaves a background child does not produce a `notify-failed` event
      - Background children left by the gate's process group never keep the run blocked beyond the wait delay

- [x] [#17] A repo path with a space breaks `--mcp-config`, so the watchdog cannot start  <!-- fixed: r-loop/phase-18 -->
      - With a repo root that contains a space, the claude provider's arguments carry the full MCP config path as one argument after `--mcp-config`
      - With that repo root, the watchdog, a step session and intake each write their MCP config file before the agent starts
      - Templates without placeholders still split on whitespace as today

- [x] [#18] No evidence check stops a step from editing the todo or issues file: a self-ticked item blocks the phase forever, and a backlog edit ticks the wrong item  <!-- fixed: r-loop/phase-19 -->
      - A work step whose tree diff touches the todo or backlog file fails its evidence check with a reason naming that file
      - A phase branch that ticked its own item cannot block landing: it is rejected earlier, or the todo is restored before Tick, and the phase is ticked exactly once
      - Inserting another backlog item during a step never changes which item land ticks
      - Steps whose job is to write their own plan file are still accepted

- [x] [#19] The milestone report session can commit arbitrary code to main; only a non-empty report is checked  <!-- fixed: r-loop/phase-19 -->
      - A milestone report step that changes any path other than its report fails or has those changes discarded, and those paths never appear in the report commit
      - The report commit touches exactly the report file
      - A report step that writes only its report commits as it does today

- [x] [#20] Config accepts zero or negative timeouts, and a key set to null in a higher layer overrides the default with zero  <!-- fixed: r-loop/phase-21 -->
      - Loading config refuses a zero or negative `land.gateTimeout`, `watchdog.checkTimeout`/`stallGrace`/`unblockTimeout`/`remedyWindow` or row timeout, exiting 2 with the file, line and key
      - A key set to null (`gateTimeout:` or `~`) resolves to the next layer's value, and the banner's provenance names that layer
      - The land gate never runs with a zero timeout

- [x] [#21] `index.lock` contention is not retried, and a failed git cleanup can leave the maintainer's tree mid-merge  <!-- fixed: r-loop/phase-3 -->
      - A git command that fails because `.git/index.lock` exists is retried with bounded backoff before its error is returned
      - If `MERGE_HEAD` exists in the primary tree when land starts, land refuses with an error naming the unfinished merge
      - Resume and preflight name a leftover `MERGE_HEAD` explicitly and say how to fix it, instead of a generic dirty-tree message
      - If aborting the merge fails during land cleanup, the phase's error says the primary tree still holds an unfinished merge

- [x] [#22] A question can be re-recorded as open after it was withdrawn  <!-- fixed: r-loop/phase-9 -->
      - If a step ends while its question is being admitted, the question's stored state is withdrawn, not open
      - A question withdrawn before routing is never sent to the watchdog and has no open entry in the router
      - Under normal admission the question is recorded open once, then routed

- [x] [#23] The single-run lock is check-then-act and PID-based: two starts both proceed, and a reused PID blocks everything  <!-- fixed: r-loop/phase-5 -->
      - Of two concurrent starts in the same repo, exactly one proceeds and the other exits 4 naming the live run
      - A `current` file whose PID now belongs to an unrelated process does not block a new start or a resume
      - An exiting driver clears `current` only when it still names this driver's run and never removes another process's pointer
      - After the lock holder crashes, the next start or resume can take the lock without manual cleanup

- [x] [#24] A half-written sentinel fails the step permanently  <!-- fixed: r-loop/phase-19 -->
      - A sentinel that is empty or truncated on one tick and valid on a later tick ends the step by its valid content, not "sentinel unreadable"
      - A sentinel still malformed after a bounded grace period fails the step with a reason that includes the parse error
      - Every step prompt's sentinel section tells the agent to write the file atomically (temp file in the same directory, then rename), asserted by a render test

- [ ] [#25] Every emit reloads the whole run and rewrites the report while holding the loop lock
      - N emitted events cost O(N) store reads in total, not O(N²), shown by a counting fake store
      - The loop lock is not held during report file I/O or `Face.Emit`: with a face whose Emit blocks, emits from other goroutines and step records still proceed
      - `report.md` reflects every appended record once the run ends, and is at most a bounded delay behind while the run is live
      - Handling a watchdog signal does not do a full store load

- [x] [#26] `Watch.forward` with a nil stop channel blocks forever once its 64-slot buffer is full  <!-- fixed: r-loop/phase-6 -->
      - `Accept`/`Handle` return within a bounded time even when the signal buffer is full and the loop isn't draining it; the signal is queued or reported as dropped
      - After the loop's `Run` returns, an MCP signal call returns instead of blocking
      - The watchdog's prompt path never blocks while holding the watchdog's send lock, so a phase-check Notify cannot deadlock behind it
      - A halt signal is never silently lost when the buffer is full

- [x] [#27] `restart_step` can say "accepted" and then not restart  <!-- fixed: r-loop/phase-6 -->
      - `restart_step` returns accepted=true only when the loop actually queued the new attempt
      - When the loop refuses a restart it received (a pending halt, or the restart limit), the caller gets accepted=false with the loop's reason
      - A successful restart still returns accepted=true and starts attempt N+1

- [x] [#28] Provider binaries are not checked in preflight, so a missing `codex` or `claude` surfaces hours into the run  <!-- fixed: r-loop/phase-18 -->
      - Preflight exits 127, naming the provider, the binary and the config field, when any configured role's provider binary is not on PATH
      - The check is skipped in `--dry-run`, as the herdr check is
      - A run whose provider binaries are all on PATH starts exactly as today

- [ ] [#29] `Depends on:`/`Blocks:` parsing is loose, and dependency order is never enforced
      - `Phase 3 (see ADR-12)` gives `DependsOn [3]`, and `Phase 3, Phase 4b` gives `[3, 4b]`
      - A phase that depends on itself or on a later phase fails Read with an error naming the line
      - `Blocks: phase 4` in any case gives specific blocked phases; blocking everything happens only when `Blocks:` names no phase or says all
      - On resume, a phase whose dependency is blocked or not landed is skipped with the dependency named, not run

- [ ] [#30] A `#` heading inside a fenced code block ends the phase block
      - A phase whose block holds a fenced code block with a `# comment` line keeps its items, `Done when:` and body after the fence
      - Tick on such a phase ticks the `- [ ]` items that follow the fence
      - A `### Phase N` line inside a fence is not read as a phase heading
      - Unfenced headings still end the block as today

- [ ] [#31] Step agents can read the watchdog token and URL from the run directory and call watchdog-only tools
      - A step agent cannot reach watchdog tools using anything in the repository tree: the watchdog's token and URL are not stored under the repo, or the endpoint authenticates in a way a step can't
      - A request to the watchdog path with a body over the limit is refused with 413
      - A step-path request calling a watchdog tool is still refused, and the watchdog's own calls still succeed

- [ ] [#32] Notify hooks run synchronously for up to 60 s on the loop goroutine
      - A slow `onWarn` hook (e.g. `sleep 30`) does not delay the loop's handling of step completion, a halt or an abort beyond the normal poll interval
      - A watchdog `signal` MCP call returns promptly while a hook is running
      - Hooks still fire once per transition with the same environment, and a failed or timed-out hook still emits `notify-failed`
      - `onDone`/`onHalt` at run end still complete, bounded by their timeout, before the process exits

- [ ] [#33] `DiffStat` reads every untracked file fully into memory, binaries included
      - An untracked binary file (containing NUL bytes) counts as 0 added lines, as numstat does for tracked binaries
      - A very large untracked file is streamed or capped, never loaded into memory whole
      - Line counts for untracked text files are unchanged, including a last line with no trailing newline
