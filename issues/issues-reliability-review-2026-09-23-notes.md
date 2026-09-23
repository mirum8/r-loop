# Driver — notes on the reliability review of 23 Sep 2026

Read against `r-loop` @ `main` `058c81b`. All 34 findings (#1–#33, with #10 split into #10a and
#10b) are in `issues-reliability-review-2026-09-23.md`. This file says where a finding's premise
was partly wrong, and which items move architecture or the estimate.

Five read-only verifiers each checked one area's findings against the code at this revision. The
review's reproductions for #4, #5, #6 and #9 were confirmed by reading the code; they were not
re-run.

## Questions — need an answer before they can become work

None. Every finding is a concrete defect with a reproducible failure path.

## Already built, or built differently than the review assumed

**[#7] The `&&` join is the contract, not a code deviation.**
`tech-design.md:368-370` says the gate command is every backtick span of `Done when:` joined with
`&&`, and `land.go:17-29` does exactly that. Fixing it means changing that contract, or rewording
the `Done when:` lines, not just the code. Only lines 518, 532, 548 and 590 of todo.md are broken.
Lines 561 and 577 are fine: their second span is a grep that succeeds when the expected line is
there.

**[#18] A check exists, but it only warns.**
The `plan-touched` watch check (`checks.go:175-187`) sees a step editing the todo, but only sends
the watchdog a one-time warning. Nothing in evidence rejects the step, so what the review describes
still happens.

**[#21] Resume refuses a mid-merge tree, but doesn't say why.**
`clean()` (`preflight.go:162-172`) fails on the staged merge changes, so main is not silently
corrupted. It exits 4 with "primary tree is not clean" and never names r-loop's unfinished merge.
Within the same run, the next phase's merge fails with "You have not concluded your merge", which
takes every later phase down with it.

**[#26] The direct deadlock described can't happen; a related one can.**
`WatchdogGone` calls `haltGone` only while a step is live (`watch.go:187-196`), and nothing is live
during a phase check. The reachable problem is the watchdog's drain goroutine blocking in `forward`
while it holds the watchdog's send lock (`watchdog.go:158-160`). A later phase-check Notify then
waits on that lock. Also, once the loop exits nothing drains the buffer, so blocked callers stay
blocked.

**[#29] Reading every number after "Phase" as a dependency is the spec's own rule.**
`plan/testdata/todo.md:62` specifies "every integer after `Phase`", so `{3, 12}` is the specified
behaviour, and this is a gap in the spec. Once forward dependencies are rejected, file order is
enough for a fresh run, so the "never checks deps landed" part mainly matters on resume.

**[#32] Hooks don't run inside the MCP handler.**
`fire` always runs on the loop goroutine. The watchdog's `signal` call only puts the signal on a
queue (`watch.go:295-305`). The real cost is the loop stalling up to 60 s per warning and missing
done, halt and abort in that time. The MCP call can only stall indirectly, through a full buffer
(#26).

Smaller corrections that don't change the work:

- **#1:** a keyboard ctrl+c in the TUI arrives as a key press and goes to the stop prompt. Only an
  external `kill -INT` or SIGTERM reaches Bubble Tea's own signal handler.
- **#10a:** a crash after the landing commit but before the landing record leaves the phase ticked.
  Resume then drops it from the run list (`resume.go:65-70`), so that phase never gets a landing
  record.
- **#12:** a question that nobody picks up is polled until the run ends, not literally forever.
- **#23:** a PID reused by the same user also counts as alive, not only the EPERM (other-user) case.
- **#27:** the loop's own restart-limit refusal rarely fires, because Remedies checks the same limit
  first. The refusal that actually causes the problem is a pending halt (`drainHalts`).

## Moves architecture or the estimate

These are all in the backlog and will be built; they are flagged because they go beyond a local fix.

- **[#1] Signals and [#11] abort during land.** One cancellation path is needed through the
  loop, land, the gate's process group (`Repo.Run` takes no ctx today) and the TUI. #2 (timeouts)
  shares the same plumbing, so all three are best done together.
- **[#3] Primary-tree safety.** Every change the driver makes to the primary tree (merge, tick,
  commit, reset, probe, milestone restore) needs a clean-tree and branch guard, and commits must be
  staged by path. This changes how land talks to the repo.
- **[#10a] Record before act.** New intent records before the step commit and before the land merge,
  plus resume logic to reconcile them. Touches `tech-design.md`'s record contract.
- **[#12] Land-stage steps.** Gatefix, gate probe and milestone report have to join the same
  observer path as ordinary steps, or the question and signal routing has to learn about them.
- **[#7] Gate derivation.** A contract change in `tech-design.md` plus rewording of existing
  `Done when:` lines.
- **[#31] Watchdog secrets.** Moving the token and MCP config out of the repo tree changes where the
  watchdog's config file lives.
- **Estimate only:** #2 (timeouts on every herdr and git call), #4 (leftover detection at start),
  #13 (recover on every goroutine), #14 (run selection and a run-ID argument), #23 (an OS file lock
  instead of the PID check).
