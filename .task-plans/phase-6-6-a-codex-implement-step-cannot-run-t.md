status: planned

## Summary

Codex's `workspace-write` sandbox blocks local sockets unless `sandbox_workspace_write.network_access` is on, so `internal/askmcp` and `internal/app` tests that `net.Listen("tcp", "127.0.0.1:0")` fail inside a codex step. I measured this again while planning, with codex-cli 0.155.1 and `GOCACHE` under `$TMPDIR`. `codex sandbox -c sandbox_mode=workspace-write -- go test -count=1 ./internal/askmcp/` fails with `intake_test.go:24: listen tcp 127.0.0.1:0: bind: operation not permitted`. The same command with `-c sandbox_workspace_write.network_access=true` added gives `ok r-loop/internal/askmcp`.

The phase adds that one `-c` pair to the shipped codex block's `flags`. It updates every test that pins the shipped codex argv, adds a string pin and a `codex sandbox` listener test, and brings the two docs that quote the shipped codex `flags` line in line with it.

Choices:
- **Fix vs. prompt listing.** The fix is the flag. Naming the packages a codex step cannot run in the implement prompt was rejected: the flag lets the step run the whole suite, which the item puts first, and a listing would still leave the step blind to the listener tests.
- **Which config to set.** Only `sandbox_workspace_write.network_access=true`. Forcing `sandbox_mode=danger-full-access` was rejected because it removes the write sandbox, which no obligation needs. Also forcing `sandbox_mode=workspace-write` was rejected because it would override the maintainer's own sandbox choice for no obligation. Under the other modes the key is a no-op.
- **Where the flag lives.** In `flags`, the fixed start flags (ADR-6 amendment), not in a new provider field. `flags` already reaches both the step session (`Args`, `internal/providers/registry.go:126`) and the nested `codex exec review` through `{args}` (`ToCore`, `internal/providers/registry.go:161`). No new concept is needed.

## Changes

1. **Modify `internal/providers/shipped/codex.yaml:2`.** Set it to
   `flags: "-c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true"`.
   Serves: the step session can open local listeners, so it can run the full `go test ./...` (open item 1). The shipped `flags` carry what codex needs (open item 2). No Go code changes: `Args` splits `flags` with `strings.Fields` (`registry.go:128`), and `shellWord` (`registry.go:170`) leaves `sandbox_workspace_write.network_access=true` unquoted because every rune is in its safe set.
2. **Modify `internal/providers/registry_test.go`.**
   - `TestShippedCodexBlock` (`:47`): set `want.Flags` to `"-c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true"`.
   - `TestArgsOfShippedBlocks` (`:195`): in both codex rows (`:210`, `:212`), insert `"-c", "sandbox_workspace_write.network_access=true"` right after `"-c", "check_for_update_on_startup=false"`.
   - `TestArgsOmitAskFlagWithoutItsValue` (`:233`): make the expected codex argv `[]string{"-c", "check_for_update_on_startup=false", "-c", "sandbox_workspace_write.network_access=true", "-c", "model_reasoning_effort=low"}`.
   - `TestToCoreFillsTheReviewArgsWithFlagsModelAndEffort` (`:280-281`): make the wanted review strings
     `"codex exec review --uncommitted -c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true -c model=gpt-x -c model_reasoning_effort=high -o {output}"` and
     `"codex exec review --uncommitted -c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true -o {output}"`.
   - Add `TestShippedCodexFlagsLetTheSandboxOpenLocalListeners` right after `TestShippedCodexBlock`. It follows that test's shape (`NewRegistry(nil, nil, t.TempDir())`, `Resolve("codex")`, `t.Fatal` on err). It asserts two things:
     - `Args(codex, "", "", "http://127.0.0.1:9/ask", "")` contains the adjacent pair `"-c", "sandbox_workspace_write.network_access=true"`.
     - `ToCore(codex, "", "", "http://x", "").Review` contains `" -c sandbox_workspace_write.network_access=true "`.
   - Add `TestShippedCodexFlagsLetACodexSandboxOpenALocalListener` right after the previous one. It is a helper-process test, the same `exec.Command` shell-out style as `TestToCoreKeepsReviewConfigAsOneShellArgument` (`:300`). Add `"net"` to the imports.
     - **Helper branch.** When `os.Getenv("R_LOOP_LISTEN_HELPER") == "1"`, call `net.Listen("tcp", "127.0.0.1:0")`, `t.Fatal(err)` on error, `Close()` the listener, and return.
     - **Parent branch.** `codexBin, err := exec.LookPath("codex")`, and on error `t.Skip("codex not on PATH")`.
       - Resolve the shipped codex block and build `args := append([]string{"sandbox", "-c", "sandbox_mode=workspace-write"}, strings.Fields(codex.Flags)...)`.
       - Append `"--", os.Args[0], "-test.run=^TestShippedCodexFlagsLetACodexSandboxOpenALocalListener$", "-test.count=1"`.
       - Run `exec.Command(codexBin, args...)` with `cmd.Env = append(os.Environ(), "R_LOOP_LISTEN_HELPER=1")` and `CombinedOutput()`.
       - On error, `t.Fatalf("codex sandbox with the shipped flags could not open a local listener: %v\n%s", err, out)`.
     - `sandbox_mode=workspace-write` is forced here because it is the mode the step fails in. `codex sandbox` runs the command straight under seatbelt, with no approval escalation, so the result is deterministic.
     - Checked while planning: this command, with only `-c check_for_update_on_startup=false`, prints `listen tcp 127.0.0.1:0: bind: operation not permitted` and exits 1. With the new flag it listens and exits 0, and the environment reaches the child.
   Serves: open item 1. A process in a codex workspace-write sandbox, started with the shipped flags, can open the kind of local TCP listener the `internal/askmcp` and `internal/app` tests open. Open item 2: the provider test pins the flags. The existing pins stay true.
3. **Modify `internal/app/app_test.go:330`** (`TestWireBuildsTheGateFixKindAndTheMilestoneBoundary`). Set the wanted joined args to `"-c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true -c model=gpt-x -c model_reasoning_effort=high"`. Serves: keeps the wiring pin true, since it resolves the shipped block.
4. **Modify `docs/task-loop-driver/tech-design.md:201`.** In the shipped `codex` summary, change `flags: -c check_for_update_on_startup=false` to `flags: -c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true`. Serves: the contract doc quotes the shipped block and must not contradict it.
5. **Modify `docs/task-loop-driver/spec.html:3417`.** In the config example, set the `flags` value to `"-c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true"` and keep the trailing comment `# passed first on every start (ADR-6)`. Serves: same as 4.

## Tests

Write these first; all are in `internal/providers` unless named otherwise.
- `TestShippedCodexFlagsLetTheSandboxOpenLocalListeners` (new). Pins the network-access pair in the step argv and in the nested review command. Covers open items 1 and 2, and the review path the watchdog named.
- `TestShippedCodexFlagsLetACodexSandboxOpenALocalListener` (new). Runs a helper process under `codex sandbox` in `workspace-write` mode with the shipped flags and asserts it can bind `127.0.0.1:0`. Fails on the base flags. Covers open item 1 as behaviour, not strings.
- `TestShippedCodexBlock` (updated). Pins the full shipped `flags` string. Covers open item 2.
- `TestArgsOfShippedBlocks` (updated). Pins the exact codex argv order, with the fixed flags first, with and without model and effort.
- `TestArgsOmitAskFlagWithoutItsValue` (updated). Pins the codex argv without an ask URL.
- `TestToCoreFillsTheReviewArgsWithFlagsModelAndEffort` (updated). Pins the review command carrying the new flag, unquoted, with no `mcp_servers`.
- `internal/app` `TestWireBuildsTheGateFixKindAndTheMilestoneBoundary` (updated). Pins the wired codex argv.

## Left out

- A prompt listing of packages a codex step cannot run: the flag lets it run them all.
- A test that drives a live codex *step* (herdr pane, model turn): the watchdog noted it cannot reproduce the failure reliably because of approval escalation. `codex sandbox` exercises the same seatbelt policy deterministically.
- Running the whole `go test ./...` inside the sandbox test: one listener in a helper process is the failing operation. A nested suite run would recurse into this test and need a writable Go cache.
- Setting `sandbox_mode`: no obligation needs it (see Summary).
- A new provider field or config key: `flags` already carries fixed flags to both sessions.

## Assumptions

- **The watchdog's warning is resolved by the Changes above.** The cut is one flag in `codex.yaml` plus a pin in `registry_test.go`, not a prompt listing. I reproduced the measurement while planning (Summary). The flag also reaches `codex exec review` through `{args}`, which gives the nested reviewer the network that phase 1's deferred finding claude-r1-1 said it lacked. `TestShippedCodexFlagsLetTheSandboxOpenLocalListeners` pins that path. The watchdog's intermittency (`approvals_reviewer = "auto_review"` escalating out of the sandbox) affects live steps only. `TestShippedCodexFlagsLetACodexSandboxOpenALocalListener` uses `codex sandbox`, which does not escalate.
- The sandbox test skips when `codex` is not on `PATH`. A machine without codex runs no codex steps, so it has nothing to prove there. The land gate runs on the maintainer's machine, where codex-cli 0.155.1 is installed, so the gate fails on the base flags there.
- **The flag is harmless outside the case it fixes.** It is only honoured in `workspace-write` mode. Under `read-only` or `danger-full-access` codex ignores it, so it never widens a stricter mode's write access.
- The phase block names no `Files:` line. The files above are all that reference the shipped codex flags (from `grep -rn check_for_update_on_startup`). `docs/task-loop-driver/todo.md:560-561` is a ticked historical item whose `grep` still matches the new line, so it stays as it is. `.task-plans/phase-1-*.md` and `issues-driver-run-2026-09-23-notes.md` are records of past work and stay as they are.

## Gate

`go test ./internal/providers/ -run 'TestShippedCodexBlock|TestShippedCodexFlagsLetTheSandboxOpenLocalListeners|TestShippedCodexFlagsLetACodexSandboxOpenALocalListener|TestArgsOfShippedBlocks|TestArgsOmitAskFlagWithoutItsValue|TestToCoreFillsTheReviewArgsWithFlagsModelAndEffort' && go test ./internal/app/ -run 'TestWireBuildsTheGateFixKindAndTheMilestoneBoundary'`
