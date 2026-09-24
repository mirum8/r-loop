status: planned

## Summary

Today `askmcp.Server.Serve` writes the watchdog's token to `<runDir>/wd-token` (`internal/askmcp/server.go:66`, via `token` at `:177-187`) and `Wiring.startDog` writes the watchdog's MCP config, which holds the full watchdog URL, to `<runDir>/watchdog.mcp.json` (`internal/app/wire.go:554-562`). The run dir is `<repo>/.r-loop/runs/<runID>/`, inside the repository tree every step agent can read (steps even write their sentinels there). This phase takes both out of the repo tree: the watchdog token lives only in memory (nothing reads `wd-token`; only tests do), and the watchdog's MCP config goes to a private temp dir (`os.MkdirTemp`, mode 0700) that `Execute` removes when the run ends — the exact shape the intake already uses (`internal/app/intake.go:91-95`, `tech-design.md:688-692`).

The other two open items already hold on the base code and get pinning tests only: a POST over 4 MiB to the watchdog path is refused with 413 by the go-sdk v1.8.0 handler's default `MaxRequestBodyBytes` (4 MiB, `mcp/streamable.go:222-244`, same value as `maxBody` at `internal/askmcp/server.go:26`) — verified with a throwaway probe (both an uninitialised `tools/call` and an `initialize` of 5 MiB returned 413); step-path refusal of watchdog tools is `callsWatchdogTool` + the step server registering only `ask_watchdog` (`internal/askmcp/server.go:103-111`, `:238-252`), pinned by `TestWatchdogToolsOnAStepPathAre404`.

Choices:
- **Keep the secret off the repo tree (taken) vs. authenticate the watchdog endpoint differently (header token, peer check).** A header token would still have to be written into the same MCP config file, and every step runs as the same OS user, so no in-band scheme a step "can't" reproduce exists; moving the files out is what the item's first branch asks and matches the intake.
- **Watchdog token in memory only (taken) vs. move `wd-token` to the temp dir.** No production code reads the file (`grep wd-token` hits tests only); the URL already carries the token to the only consumer.
- **Private `os.MkdirTemp` dir removed at run end (taken) vs. a fixed path like `$TMPDIR/r-loop-<runID>/`.** MkdirTemp gives an unguessable 0700 dir with no collision handling, and removing it follows the intake precedent; a fixed path would leak one dir per run and be discoverable by name.
- **A temp base inside the repo (`TMPDIR=<repo>/.tmp`, or a relative `TMPDIR` resolved against the working dir): refuse with exit 2 (taken) vs. fall back to another base such as `/tmp`.** A fallback would silently pick a location the maintainer didn't choose (and there is no base that is guaranteed to be outside every repo). Refusing blocks with a message naming the fix, which is how every other bad-config case in `startDog` behaves (`exit(2, ...)`, `internal/app/wire.go:557`).
- **413 on the watchdog path: rely on the SDK default and pin it with a test (taken) vs. read the body through `http.MaxBytesReader` ourselves as on the step path.** The SDK already enforces 4 MiB with 413 before any handler runs; re-creating it adds a second copy of the same limit.

## Changes

1. **Modify `internal/askmcp/server.go`** — in `Serve` (`:61-69`), replace `wdToken, err := s.token("wd-token")` with `wdToken, err := newToken()` (reuses `newToken`, `:169-175`). The step token (`s.token("token")`) and the `token` helper stay as they are. No other change in this file. Serves item 1.

2. **Modify `internal/app/wire.go`**:
   - Add an unexported field `dogDir string` to `Wiring` (`:66-100`), next to `lock *store.Lock` (`:96`).
   - In `startDog` (`:552-570`), before `url := w.Ask.WatchdogURL()`, create the dir exactly as intake does (`internal/app/intake.go:91-95`):
     ```go
     dir, err := os.MkdirTemp("", "r-loop-watchdog-")
     if err != nil {
         return exit(2, "%v", err)
     }
     if inside, err := under(w.Dog.Root, dir); err != nil || inside {
         os.RemoveAll(dir)
         if err != nil {
             return exit(2, "%v", err)
         }
         return exit(2, "watchdog config dir %s is inside the repository %s: set TMPDIR outside it", dir, w.Dog.Root)
     }
     w.dogDir = dir
     ```
     `w.Dog.Root` is the repo root (`internal/app/wire.go:399`, `:521`). Add, in `wire.go` below `startDog`:
     ```go
     func under(root, dir string) (bool, error) {
         rootInfo, err := os.Stat(root)
         if err != nil {
             return false, err
         }
         abs, err := filepath.Abs(dir)
         if err != nil {
             return false, err
         }
         for p := abs; ; p = filepath.Dir(p) {
             info, err := os.Stat(p)
             if err != nil {
                 return false, err
             }
             if os.SameFile(rootInfo, info) {
                 return true, nil
             }
             if filepath.Dir(p) == p {
                 return false, nil
             }
         }
     }
     ```
     `filepath.Abs` resolves a relative `TMPDIR` against the process working dir, the same dir `os.MkdirTemp` used. Comparing the dir and each ancestor with the root by filesystem identity (`os.Stat` follows symlinks, `os.SameFile` compares device and inode) catches a path that reaches the repo through a symlink (`/var` vs `/private/var` on macOS, a link to the repo) or through a different letter case on a case-insensitive filesystem (APFS default), where a spelling-based `filepath.Rel` would report "outside". Then change `mcpPath := filepath.Join(w.Dog.RunDir, "watchdog.mcp.json")` to `mcpPath := filepath.Join(dir, "watchdog.mcp.json")`. Keep the rest of `startDog` unchanged (`providers.WriteMCPConfig` still writes it with mode 0600, only when the args reference `mcpPath`).
   - In `Execute`'s deferred cleanup (`:300-313`), directly after the `w.Dog.Stop()` block, add:
     ```go
     if err := os.RemoveAll(w.dogDir); err != nil {
         fmt.Fprintf(w.Env.Stderr, "r-loop: remove watchdog config: %v\n", err)
     }
     ```
     (`os.RemoveAll("")` returns nil, so a run that never reached `startDog` — dry-run, ask-server failure — needs no guard.) It runs after the watchdog pane is closed, so the watchdog has already read its config at start. The same stderr pattern as `r-loop: close watchdog: %v` (`:305`). Serves item 1.

3. **Modify `internal/askmcp/watchdog_test.go`** — replace `TestWatchdogURLHasItsOwnPrivateToken` (`:79-101`) with `TestTheWatchdogTokenLivesOnlyInItsURLNeverInTheRunDir`, and add `TestAnOversizedBodyOnTheWatchdogPathIsRefusedWith413` after `TestAnOversizedBodyOnAStepPathIsRefusedWithoutBeingReadWhole` (`:410-424`), following that test's shape. See `## Tests`. Serves items 1, 2.

4. **Modify `internal/app/watchdog_test.go`**:
   - Add to `dogHost` (`:22-32`) the fields `config string` (file content), `configPath string`, `configPerm os.FileMode`, and in `dogHost.Start` (`:162-165`), under `h.mu`, for each arg that ends in `watchdog.mcp.json`, set `configPath = arg[strings.Index(arg, "/"):]` (covers both `--mcp-config <path>` and `--cfg=<path>`), then `os.ReadFile` it into `config` and `os.Stat` its `Mode().Perm()` into `configPerm` (ignore errors: an unreadable file leaves `config` empty, which the assertions catch). Add `func (h *dogHost) startConfig() (path, data string, perm os.FileMode)` returning the three under `h.mu`.
   - `TestAWatchdogAndAStepSessionWriteTheirMCPConfigUnderARepoRootWithASpace` (`:79-143`): keep a handle `dogH := &dogHost{}` and wrap it as today (`dog := &configAtStart{SessionHost: dogH, ...}`). Replace `mcpPath := filepath.Join(w.Store.Dir(...), "watchdog.mcp.json")` and the "lacks spaced root" check (`:109-112`) with `mcpPath, data, _ := dogH.startConfig()` and `if mcpPath == "" || strings.HasPrefix(mcpPath, root) { t.Errorf("watchdog config %q is under the repo", mcpPath) }`. Replace the post-run `os.ReadFile(mcpPath)` check (`:126-128`) with `if !strings.Contains(data, "/mcp/watchdog/") { ... }`. The `--mcp-config` arg check, `dog.missing`/`dog.mcp` checks and the step-config checks stay.
   - `TestExecuteStartsTheWatchdogAndAHaltThroughItsMCPSurfaceExits5` (`:233-293`): replace `mcpPath := filepath.Join(runDir, "watchdog.mcp.json")` (`:276`) with `mcpPath, data, perm := dog.startConfig()`; keep the `calls` assertion (`:278`) using that `mcpPath`; replace the post-run `os.ReadFile`/`os.Stat` checks (`:281-286`) with: `data` contains `w.Ask.WatchdogURL()`, `perm == 0o600`, `strings.HasPrefix(mcpPath, f.root)` is false, and `os.Stat(mcpPath)` returns an `errors.Is(err, os.ErrNotExist)` error (removed at run end).
   - `TestAWatchdogAskFlagWithTheConfigPathEmbeddedStillGetsItsConfig` (`:575-596`): replace `mcpPath := filepath.Join(w.Store.Dir(...), "watchdog.mcp.json")` with `mcpPath, data, _ := dog.startConfig()`; keep the `calls[1]` assertion; replace the post-run `os.ReadFile` with `strings.Contains(data, "/mcp/watchdog/")`.
   - Add `TestNoFileUnderTheRepoHoldsTheWatchdogTokenWhileTheRunIsLive` and `TestATempDirInsideTheRepoRefusesToStartTheWatchdog` (see `## Tests`).
   Serves item 1.

5. **Modify `internal/askmcp/watchdog_test.go` · `TestWatchdogToolsOnAStepPathAre404`** (`:355-390`) — extend the tool list at `:372` from `{"signal", "propose_remedy", "restart_step", "answer_question", "ask_maintainer"}` to every tool the watchdog server registers (`internal/askmcp/watchdog.go:107,131,148,165,182,201,218`): `{"signal", "propose_remedy", "restart_step", "answer_question", "ask_maintainer", "submit_triage", "submit_gate"}`. Nothing else in the test changes. Serves item 3.

No other file changes. `internal/askmcp/server_test.go` (`TestServeWritesAPrivateTokenAndReturnsTheBaseURL`) and `internal/app/ask_test.go:323` keep checking the step `token` file, which stays.

## Tests

Write these first; the three marked *new-failing* fail on the base code.

- `internal/askmcp/watchdog_test.go` · `TestTheWatchdogTokenLivesOnlyInItsURLNeverInTheRunDir` (*new-failing*, replaces `TestWatchdogURLHasItsOwnPrivateToken`) — `serveWatchdog(t, &memStore{})`; match `s.WatchdogURL()` against `^http://127\.0\.0\.1:\d+/mcp/watchdog/([0-9a-f]{32})$` and take group 1 as `wd`; read `<RunDir>/token` and assert it differs from `wd`; assert `os.Stat(<RunDir>/wd-token)` is `os.ErrNotExist`; `filepath.WalkDir(s.RunDir)` over every regular file and fail if any file's content contains `wd`. Pins item 1 at the server: the token is fresh, distinct from the step token, and not on disk. On base it fails at the `wd-token` stat.
- `internal/askmcp/watchdog_test.go` · `TestAnOversizedBodyOnTheWatchdogPathIsRefusedWith413` — install a `Signal` handler that sets `called = true`; `http.Post(s.WatchdogURL(), "application/json", body)` where body is `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"signal","arguments":{"kind":"halt","step":"phase-3/implement","reason":"` + `strings.Repeat("x", 5<<20)` + `"}}}`; assert status `413` and `!called`. Pins item 2 (over the 4 MiB limit, refused before any handler runs).
- `internal/app/watchdog_test.go` · `TestNoFileUnderTheRepoHoldsTheWatchdogTokenWhileTheRunIsLive` (*new-failing*) — `f := newResumeFixture(t, noReviewConfig)`; preflight `--plain --phases 1`; `f.sim(w, newSim())`; `dog := &dogHost{}` with `dog.onPrompt` set to a func that, on its first call only (guard with a `sync.Once`), takes the token as `filepath.Base(w.Ask.WatchdogURL())` and walks `f.root` with `filepath.WalkDir`, appending to a `leaks []string` every regular file whose content contains the token or `"/mcp/watchdog/"`; `w.Dog.Host = dog`; `w.Execute(core.RunOptions{Phases: []string{"1"}})` returns 0. Assert the once ran (the token was non-empty) and `leaks` is empty; assert `dog.startConfig()` path is non-empty, not under `f.root`, and gone after `Execute` (`os.ErrNotExist`). Pins item 1 end to end at the moment the watchdog has started (the first watchdog prompt follows `Serve` and `startDog`), and the temp dir cleanup. On base it lists `<runDir>/wd-token` and `<runDir>/watchdog.mcp.json`.
- Updated `TestAWatchdogAndAStepSessionWriteTheirMCPConfigUnderARepoRootWithASpace`, `TestExecuteStartsTheWatchdogAndAHaltThroughItsMCPSurfaceExits5`, `TestAWatchdogAskFlagWithTheConfigPathEmbeddedStillGetsItsConfig` — pin that the watchdog still gets a readable 0600 config holding its URL at start, through both `--mcp-config <path>` and an embedded `--cfg=<path>`, outside the repo, and that a halt through the watchdog's MCP surface still exits 5 (item 3, watchdog's own calls still succeed end to end).
- `internal/app/watchdog_test.go` · `TestATempDirInsideTheRepoRefusesToStartTheWatchdog` (*new-failing*) — two subtests, each with `f := newResumeFixture(t, noReviewConfig)`, preflight `--plain --phases 1`, `f.sim(w, newSim())`, `dog := &dogHost{}`, `w.Dog.Host = dog`, and `os.Mkdir(filepath.Join(f.root, ".tmp"), 0o700)` set after preflight: `absolute` sets `t.Setenv("TMPDIR", filepath.Join(f.root, ".tmp"))`; `relative` calls `t.Chdir(f.root)` and `t.Setenv("TMPDIR", ".tmp")`; `case-variant` sets `t.Setenv("TMPDIR", filepath.Join(strings.ToUpper(f.root), ".tmp"))` after first calling `t.Skip("case-sensitive filesystem")` when `os.Stat(strings.ToUpper(f.root))` fails. Each runs `w.Execute(core.RunOptions{Phases: []string{"1"}})` and asserts: exit code `2`; `f.err` contains `inside the repository`; no call in `dog.Calls()` starts with `Start `; `<f.root>/.tmp` has no entries (the created dir was removed); `filepath.WalkDir(f.root)` finds no `watchdog.mcp.json`. Pins item 1 for a `TMPDIR` under the repo, whether it is spelled absolute, relative, or with different case. On base it exits 0 (the config goes to the run dir), so it fails.
- Extended `TestWatchdogToolsOnAStepPathAre404` (`internal/askmcp/watchdog_test.go:355`) — step-path `tools/call` of all seven watchdog tools, `submit_triage` and `submit_gate` included, is 404/not found and the handler never runs (item 3, refusal).
- Existing watchdog-path success tests, one or more per tool, all in the gate unchanged (item 3, watchdog calls succeed): `TestSignalOnTheWatchdogPathReachesItsHandlerAsAWatchdogSignalForTheLatestAttempt` (`:103`, `signal`), `TestEachToolDelegatesToItsHandler` (`:224`, `propose_remedy`, `restart_step`, `answer_question`), `TestAskMaintainerIsRecordedThenDelegatedAndReturnsAtOnce` (`:441`, `ask_maintainer`), `TestSubmitTriageHandsThePayloadToItsHandlerAndReturnsTheTable` (`:545`, `submit_triage`), `TestSubmitGateHandsTheDecisionToItsHandler` (`:584`, `submit_gate`). Existing `TestAWrongWatchdogTokenIs404` (`:392`) — the step token or a wrong token on the watchdog path is 404 (item 1: the step's own token grants nothing). All three run unchanged in the gate.

## Left out

- Our own `http.MaxBytesReader` on the watchdog path — the go-sdk handler already refuses bodies over 4 MiB with 413; the new test pins it.
- Passing `&mcp.StreamableHTTPOptions{MaxRequestBodyBytes: maxBody}` to the watchdog handler — the default is already the same 4 MiB; no obligation needs the explicit value.
- Header-based or peer-credential authentication of the watchdog endpoint — any secret would still sit in a file or argv readable by the same OS user; the item is met by keeping it off the repo tree.
- Moving or deleting the step `token` file — the step token grants only step paths (`TestAWrongWatchdogTokenIs404`), and it is outside this item.
- Hiding the codex watchdog's URL from its argv (`askFlag: -c mcp_servers.r-loop.url={url}`) — argv is not in the repository tree; the item names the repo tree.
- A fallback temp base when `TMPDIR` is inside the repo — refusing with exit 2 meets item 1 and names the fix; a silent fallback would pick a location nobody chose.
- Editing `docs/task-loop-driver/todo.md:430` (the finished Phase 26 item naming `<RunDir>/wd-token`) — it is the ticked record of an earlier phase, not a contract any code reads. This phase's issue item supersedes it, and no obligation of this phase needs the edit.
- A `tech-design.md` edit — it never says where the watchdog token or config is stored (`:576`), so nothing there is now wrong.

## Assumptions

- "Not stored under the repo" is met by `os.MkdirTemp("", ...)`, i.e. `$TMPDIR` (per-user, 0700 on macOS) — even when a test's repo root is itself under `$TMPDIR`, the new dir is a sibling, never inside the root.
- The watchdog provider reads its MCP config only at process start (claude `--mcp-config`), so removing the dir after `Dog.Stop()` at run end is safe; a resumed run starts a new server with a new token and writes a new config.
- A `TMPDIR` inside the repo is a maintainer misconfiguration: the run refuses to start the watchdog (exit 2), in line with "bad config blocks, never fall back".
- A failure to remove the temp dir is reported on stderr and does not change the exit code, like the `close watchdog` failure beside it.

## Gate

`go test ./internal/askmcp/ ./internal/app/ -run '^(TestTheWatchdogTokenLivesOnlyInItsURLNeverInTheRunDir|TestAnOversizedBodyOnTheWatchdogPathIsRefusedWith413|TestNoFileUnderTheRepoHoldsTheWatchdogTokenWhileTheRunIsLive|TestAWatchdogAndAStepSessionWriteTheirMCPConfigUnderARepoRootWithASpace|TestExecuteStartsTheWatchdogAndAHaltThroughItsMCPSurfaceExits5|TestAWatchdogAskFlagWithTheConfigPathEmbeddedStillGetsItsConfig|TestATempDirInsideTheRepoRefusesToStartTheWatchdog|TestWatchdogToolsOnAStepPathAre404|TestSignalOnTheWatchdogPathReachesItsHandlerAsAWatchdogSignalForTheLatestAttempt|TestEachToolDelegatesToItsHandler|TestAskMaintainerIsRecordedThenDelegatedAndReturnsAtOnce|TestSubmitTriageHandsThePayloadToItsHandlerAndReturnsTheTable|TestSubmitGateHandsTheDecisionToItsHandler|TestAWrongWatchdogTokenIs404)(/.*)?$'`
