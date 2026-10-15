status: planned

## Summary

Four changes, one phase. (1) The three hook waits in `internal/core/hooks.go` stop sleeping a full `hookPoll` (200ms) past their deadline: each sleep is capped at `min(hookPoll, time.Until(deadline))`. (2) `app.Env` gains `Path string`, the PATH the run resolves binaries against. Every binary lookup in `internal/app` goes through one new method, `Env.lookPath`, and `analyze.New` takes that lookup as a parameter. `cmd/r-loop` fills `Path` from the process environment, and the app fixtures set `f.env.Path` instead of calling `t.Setenv("PATH")`. With no process-wide PATH left, every app test except 9 serial ones gets `t.Parallel()`, and so do the subtests of the two named triage tests. The 9 are the 8 that set TMPDIR, change directory or signal the test process, plus the lock test. (3) `TestALoserNamesAWinnerThatPublishesLate` stays serial and waits until a goroutine is blocked in `syscall.Flock`, which proves the loser is waiting on the gate, instead of sleeping 2.5s. (4) Analyze's cancel path stops the root process group first, then walks the descendants and stops them, re-walking until a walk finds no new pid, and only then kills them all. Each `ps` listing has its own 500ms timeout. Failed or hung listings are retried until a 2s budget runs out, after which the kill still runs. The analyze tests record each detached stub's pid and kill it in `t.Cleanup`.

Choices:
- **Hook sleep cap**: chosen, cap each `time.Sleep` at the remaining deadline. The alternative, waking on the hook log's `changed` channel, does nothing for a deadline with no events (the case the item names) and adds a select at three sites.
- **How fake binaries reach the code**: chosen, a `Path string` on `Env` plus one `Env.lookPath` that follows `exec.LookPath`'s rules: first hit wins, and a hit in a relative or empty entry stops the lookup with `exec.ErrDot`. The alternative, `LookPath func(string) (string, error)` on `Env`, makes the fixtures fake the lookup itself and stop testing the real executable and order rules. It also still needs a resolved path for every exec.
- **How analyze gets the PATH**: chosen, `analyze.New(timeout, lookPath func(string) (string, error))`, which app calls with `env.lookPath` and analyze's own tests with `exec.LookPath`. The alternatives were a shared lookup package, which is a new concept for one function, or a second copy of the lookup in analyze, which would be two copies that must change together.
- **Child-process environment**: chosen, resolve each binary to an absolute path and leave children's `Env` alone. Setting `PATH=Env.Path` in every child's environment changes nothing in production, where `Env.Path` is the process PATH, and no fake resolves anything by name.
- **Cancel-path fix**: chosen, SIGSTOP the root group, then stop and re-walk until nothing new appears, then SIGKILL everything. Each listing gets a 500ms timeout, and failed or hung listings are retried within a 2s budget. A fixed retry count leaves a longer `ps` outage leaking. An unbounded `ps` call can freeze `Cancel` with the root group stopped. A single walk with a retry closes the `ps`-failure hole but still lets a child forked after the snapshot escape.
- **Lock-test signal**: chosen, keep the test serial and poll `runtime.Stack(…, true)` until a goroutine sits in `syscall.Flock(`. In a serial test the loser is the only goroutine that can be there, so this proves the publish comes after the loser waits. The herdr `.calls` file is written before `Reachable` returns, so it cannot prove that order. A short fixed sleep breaks the polling rule.
- **Which app tests go parallel**: chosen, every top-level test except the 9 serial ones, plus the two triage tests' subtests. Parallelising every table's subtests would risk shared loop state for no obligation.

## Why this approach

The items propose capping `hookStart`'s sleep, injecting the binary lookup and PATH into the internal/app wiring, cutting lock_test's sleep to about 100ms, and recording the stub's pid for cleanup. The triage adds "SIGSTOP the root group before walking, kill and re-walk descendants until no new pids, then SIGKILL the group". This plan follows those proposals, with these differences and the measurements behind them:

- **All three sleep sites are capped, not only hookStart.** `awaitSubmit` (`internal/core/hooks.go:216`), `awaitRollout` (`internal/core/hooks.go:295`) and `hookStart` (`internal/core/hooks.go:460`) each `time.Sleep(hookPoll)` with no regard to their deadline (`hookPoll` = 200ms at `internal/core/hooks.go:47`). This answers the phase check's warning.
- **The hook cap alone does not halve the triage tests.** Measured on this tree without `-race`: the two subtests of `TestNoTriageTableIsTypedIntoTheWatchdogPaneWithoutAGate` take 4.19s and 4.21s, and those of `TestUnattendedAndYesStartWithoutAGate` take 4.27s and 4.22s, so each test takes about 8.4s. With the sleeps capped, each subtest takes about 3.3s (6.3–6.4s per test). A run's event timeline shows the rest is real git work in each step, about 250ms per step (worktree add, snapshot, diff checks). Caching git's path in gitrepo saved nothing (measured). What halves both tests is running their two subtests in parallel, measured at 3.55–3.72s each. That needs `t.Parallel`, which `t.Setenv` forbids, so the PATH work is the means for criterion 2 as well as criterion 3.
- **Parallel tests fit `-race -count=3` well inside 10m.** Measured in a scratch copy with the sleeps capped, a shared fake bin set once, and `t.Parallel()` on every test except the 8 that set TMPDIR, change directory or signal the test process: one `-race` pass of `./internal/app/` takes 78s, against 315s for one serial pass without `-race` today. Three passes take about 4 minutes.
- **The PATH cut threads `Env.Path` into every lookup the fakes reach.** The phase check names `exec.LookPath` in `internal/app/wire.go:474`, `internal/app/preflight.go:171,219,234,241,405`, `internal/app/intake.go:58,82` and `internal/analyze/run.go:142`. The two execs by name in `internal/app/models.go:48,59` and analyze's `command` (`internal/analyze/run.go:157`) also resolve through the process PATH. I logged every call to the fake `claude`, `codex` and `semgrep` across the whole app suite: only `codex debug models` (models.go:48), `claude|codex --version` (models.go:59) and `semgrep scan …` (analyze, from `TestAWiredRunReviewsWithTheAnalyzerWhenAnalyzeIsEnabled`, `internal/app/app_test.go:1178`) ever run a fake. Nothing runs `codex review` through `internal/reviewexec`. So `internal/app` and `internal/analyze` are the only packages touched. `gitrepo`, `store`, `herdr`, `notify` and `reviewexec` keep the process PATH, which in production is the same value (see `## Assumptions`). This matters on this machine because real `claude`, `codex` and `semgrep` are installed (`~/.local/bin/claude`, `~/.local/bin/codex`, `/opt/homebrew/bin/semgrep`): a missed lookup would run a real provider or a networked semgrep scan. `cmd/r-loop/main.go` is outside the app package but must fill `Env.Path` (`cmd/r-loop/main.go:48`).
- **The lock test waits on a real signal, not on a shorter sleep.** The loser blocks on `flock(gate, LOCK_EX)` (`internal/store/store.go:337-340`) until the winner's `Publish` closes the gate (`internal/store/store.go:358-364`), so no 2.5s threshold exists. A blocked `flock` shows up in a goroutine dump as a `syscall.Flock(` frame (checked on this darwin machine). The winner already holds its locks, so in a serial test the only goroutine that can be in that frame is the loser inside `Preflight`'s `w.Store.Lock()` (`internal/app/preflight.go:49`). Publishing after seeing that frame exercises the late-publication path every time.
- **The analyze leak is real, so the code fix is unconditional.** One `-count=20` run of the test left no new loop, which matches the triage's non-reproduction at `-count=3`. But this machine already holds 12 orphaned `sh -c while :; do … late.html …` loops, started between 15 Oct 20:52 and 16 Oct 00:32, each with ppid 1 and its own pgid. Each is the stub's `setpgrp` child that escaped. The notes record 7 earlier ones (`issues/issues-stability-run-2026-10-15-notes.md:102-106`). `cmd.Cancel` (`internal/analyze/run.go:164-171`) takes one `descendants()` snapshot (`internal/analyze/run.go:191-213`). That snapshot returns `nil` when `ps` fails (`internal/analyze/run.go:193-195`), and then no detached child is killed. A `ps` that hangs blocks `Cancel` forever, because `exec.Command("ps", …).Output()` has no deadline. A child the root forks after the snapshot, which reaches `setpgrp` before `Kill(-root)`, escapes too. Each hole gets a deterministic test on the base code. Failing and hanging `ps` stubs cover the first two. A `ps` stub that lets a child fork only after it has taken its listing covers the third: the forced snapshot race, both for the root and for a detached daemon. The SIGSTOP of the root group closes the root race. The re-walk closes the same race one level down. The per-listing timeout and the retry budget close the `ps` holes.

## Changes

Build in this order.

1. **Modify `internal/core/hooks.go`**: at `internal/core/hooks.go:216` (`awaitSubmit`), `:295` (`awaitRollout`) and `:460` (`hookStart`), replace `time.Sleep(hookPoll)` with `time.Sleep(min(hookPoll, time.Until(deadline)))`. Each function already has a local `deadline`. A negative duration returns at once, and the loop's next `!time.Now().Before(deadline)` check exits. No other line changes. Serves #16 criteria 1 and 2.

2. **Modify `internal/core/hooks_test.go`**: add `TestAHookWaitGivesUpAtItsDeadlineNotAfterAFullHookPoll` (see `## Tests`). It reuses `hookPromptManager` (`internal/core/hooks_test.go:372`), `fakeHookFeed` (`internal/core/fakes_test.go:606`), `fakeSessionHost`, `fakeRollouts` (`internal/core/fakes_test.go:639`), `shortSubmitWait` (`internal/core/hooks_test.go:356-360`), `askingAgent` (`internal/core/questions_test.go:15`) and `t0`. It never touches `hookPoll`.

3. **Modify `internal/app/wire.go`**:
   - `Env` (`internal/app/wire.go:52-59`): change the line `Dir, Home, Herdr, Git string` to `Dir, Home, Herdr, Git, Path string`.
   - Add, directly below `Env`:
     ```go
     func (e Env) lookPath(name string) (string, error) {
     	if strings.ContainsRune(name, filepath.Separator) {
     		return exec.LookPath(name)
     	}
     	for _, dir := range filepath.SplitList(e.Path) {
     		if dir == "" {
     			dir = "."
     		}
     		path := filepath.Join(dir, name)
     		probe := path
     		if !strings.ContainsRune(probe, filepath.Separator) {
     			probe = "." + string(filepath.Separator) + probe
     		}
     		if _, err := exec.LookPath(probe); err != nil {
     			continue
     		}
     		if !filepath.IsAbs(path) {
     			return path, &exec.Error{Name: name, Err: exec.ErrDot}
     		}
     		return path, nil
     	}
     	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
     }
     ```
     This is the loop of Go 1.25's `exec.LookPath` (`lp_unix.go`), with `PATH` taken from `e.Path`: the first executable hit wins, and a hit in a relative or empty entry returns that path with `exec.ErrDot`, which every caller treats as an error. Each candidate is checked by `exec.LookPath` itself, so the stdlib's own check (stat, not a directory, and `faccessat` for the effective user) decides what counts as executable. `filepath.Join(".", name)` has no separator, and `exec.LookPath` would search the process PATH for it, so the probe gets a `./` prefix. The returned path is `filepath.Join(dir, name)`, as in Go. A name that contains a separator goes to `exec.LookPath`, which checks that file without consulting PATH. The not-found error is the same `*exec.Error` that `exec.LookPath` and `exec.Command` return today, so wrapped messages such as `codex debug models: exec: "codex": executable file not found in $PATH` from `internal/providers/models.go:22-24` do not change.
   - `Wire` (`internal/app/wire.go:474`): `exec.LookPath(env.Git)` → `env.lookPath(env.Git)`.
   - `internal/app/wire.go:534`: `analyze.New(cfg.Analyze.Timeout)` → `analyze.New(cfg.Analyze.Timeout, env.lookPath)` (`env` is `Wire`'s parameter, `internal/app/wire.go:473`).
   - `versions` (`internal/app/wire.go:1020`): `providerVersion(p)` → `providerVersion(w.Env, p)`.
   - `resolve` (`internal/app/wire.go:1129`): `w.models.resolve(p, model)` → `w.models.resolve(w.Env, p, model)`.
   Serves #16 criterion 3.

4. **Modify `internal/app/models.go`**:
   - `func (c *modelCache) resolve(p providers.Provider, model string)` → `func (c *modelCache) resolve(env Env, p providers.Provider, model string)`. At `internal/app/models.go:27`, pass `env.runCatalog` to `providers.ResolveModel` instead of `runCatalog`.
   - Replace `func runCatalog(name string, args ...string) ([]byte, error)` (`internal/app/models.go:47-49`) with
     ```go
     func (e Env) runCatalog(name string, args ...string) ([]byte, error) {
     	bin, err := e.lookPath(name)
     	if err != nil {
     		return nil, err
     	}
     	return exec.Command(bin, args...).Output()
     }
     ```
   - `func providerVersion(p providers.Provider) string` → `func providerVersion(env Env, p providers.Provider) string`. After the `p.Version == ""` guard, add `bin, err := env.lookPath(p.Kind)` and return `"?"` when `err != nil`. Then `exec.CommandContext(ctx, bin, strings.Fields(p.Version)...)`. The rest is unchanged.
   Serves #16 criterion 3.

5. **Modify `internal/app/preflight.go`**:
   - `checks` (`internal/app/preflight.go:171`): `exec.LookPath(env.Herdr)` → `env.lookPath(env.Herdr)`.
   - `validateProviders` (`internal/app/preflight.go:354`): `checkBinary(r, resolved[i].Kind)` → `checkBinary(w.Env, r, resolved[i].Kind)`. At `:359`: `w.models.resolve(resolved[i], r.model)` → `w.models.resolve(w.Env, resolved[i], r.model)`.
   - `checkBinary` (`internal/app/preflight.go:404-409`): change the signature to `func checkBinary(env Env, r role, kind string) error` and use `env.lookPath(kind)`. The message is unchanged.
   - `static` (`internal/app/preflight.go:212-216, 219`): `toolchain(root, m)` → `toolchain(w.Env, root, m)`, and `exec.LookPath("semgrep")` → `w.Env.lookPath("semgrep")`.
   - `toolchain` (`internal/app/preflight.go:232-246`): change the signature to `func toolchain(env Env, root string, m analyze.Module) error`, and change both `exec.LookPath(m.Runner)` and `exec.LookPath("java")` to `env.lookPath(...)`. The messages are unchanged: a `m.Runner` with a separator still goes through `exec.LookPath` and still reports "which is not executable".
   - Drop the `os/exec` import if nothing else uses it.
   Serves #16 criterion 3.

6. **Modify `internal/app/intake.go`**: at `:58` `exec.LookPath(env.Git)` → `env.lookPath(env.Git)`. At `:76` `checkBinary(r, p.Kind)` → `checkBinary(env, r, p.Kind)`. At `:79` `runCatalog` → `env.runCatalog`. At `:82` `exec.LookPath(env.Herdr)` → `env.lookPath(env.Herdr)`. Drop the `os/exec` import if it becomes unused. Serves #16 criterion 3.

7. **Modify `cmd/r-loop/main.go:48`**: add `Path: os.Getenv("PATH")` to the `app.Env` literal. Serves #16 criterion 3: production keeps resolving against the process PATH.

8. **Modify `internal/analyze/run.go`**:
   - `type Analyzer struct{ timeout time.Duration }` → `type Analyzer struct { timeout time.Duration; lookPath func(string) (string, error) }`. `func New(timeout time.Duration) *Analyzer` → `func New(timeout time.Duration, lookPath func(string) (string, error)) *Analyzer`, which stores both.
   - `pass` (`internal/analyze/run.go:108-115`) gains a `lookPath func(string) (string, error)` field. `run` sets it from `a.lookPath` in the `&pass{…}` literal (`internal/analyze/run.go:68`).
   - `all` (`internal/analyze/run.go:142`): `exec.LookPath("semgrep")` → `p.lookPath("semgrep")`.
   - `command` (`internal/analyze/run.go:156-187`): before `exec.CommandContext`, set `bin := name`. When `!strings.ContainsRune(name, filepath.Separator)`, set `found, err := p.lookPath(name)`. On error, `return nil, failure(tool, nil, p.cause(err))`, the same failure the old path built from the `*exec.Error` that `Run` returned. Otherwise `bin = found`. Then use `exec.CommandContext(p.ctx, bin, args...)`.
   - `cmd.Cancel` (`internal/analyze/run.go:164-171`): `detached = descendants(cmd.Process.Pid)` → `detached = stopTree(cmd.Process.Pid)`. The SIGKILL loop and the final `syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)` stay as they are.
   - Add
     ```go
     const (
     	psTimeout  = 500 * time.Millisecond
     	walkBudget = 2 * time.Second
     )

     func stopTree(root int) []int {
     	_ = syscall.Kill(-root, syscall.SIGSTOP)
     	deadline := time.Now().Add(walkBudget)
     	var stopped []int
     	for time.Now().Before(deadline) {
     		found, err := descendants(root)
     		if err != nil {
     			time.Sleep(20 * time.Millisecond)
     			continue
     		}
     		fresh := slices.DeleteFunc(found, func(pid int) bool { return slices.Contains(stopped, pid) })
     		if len(fresh) == 0 {
     			return stopped
     		}
     		for _, pid := range fresh {
     			_ = syscall.Kill(-pid, syscall.SIGSTOP)
     			_ = syscall.Kill(pid, syscall.SIGSTOP)
     		}
     		stopped = append(stopped, fresh...)
     	}
     	return stopped
     }
     ```
     A stopped process cannot fork, and its children keep it as their parent, so every re-walk still reaches them. SIGKILL ends stopped processes. Once the budget is spent the walk returns what it has, and `Cancel` goes on to its SIGKILLs, so a broken `ps` costs at most about 3s (2s budget plus one listing's 500ms timeout and 500ms `WaitDelay`) and never leaves the root group stopped.
   - `descendants` (`internal/analyze/run.go:191-213`): change it to `func descendants(root int) ([]int, error)`. Run `ps` under `ctx, cancel := context.WithTimeout(context.Background(), psTimeout)` with `exec.CommandContext(ctx, "ps", "-A", "-o", "pid=", "-o", "ppid=")` and `cmd.WaitDelay = psTimeout`. Return `nil, err` when `ps` fails or times out, and `found, nil` otherwise. `stopTree` is its only caller.
   - `awaitGone(detached)` (`internal/analyze/run.go:180`) is unchanged.
   Serves #28 criterion 3 and #16 criterion 3 (analyze finds semgrep through `Env.Path` in app runs).

9. **Modify `internal/analyze/run_test.go`** (the only analyze test file calling `New`):
   - Change every `New(<timeout>)` to `New(<timeout>, exec.LookPath)`. There are 46 sites: 43 `New(time.Minute)`, one `New(3*time.Second)`, one `New(500*time.Millisecond)` and one `New(c.timeout)`. The package's own tests keep resolving through their `t.Setenv("PATH", …)` in `stubPath` (`internal/analyze/run_test.go:30-53`).
   - Add these helpers:
     - `killRecordedOnCleanup(t *testing.T, path string)` registers a `t.Cleanup` that reads `path` and, for every pid line, calls `syscall.Kill(-pid, syscall.SIGKILL)` and `syscall.Kill(pid, syscall.SIGKILL)`, ignoring errors.
     - `recordDetached(t *testing.T) string` creates `filepath.Join(t.TempDir(), "detached")`, calls `t.Setenv("STUB_DETACHED", path)` and `killRecordedOnCleanup(t, path)`, and returns the path. Tests call it after `stubPath`, so its cleanup runs before `stubPath`'s temp dirs are removed.
     - `assertDetachedGone(t *testing.T, path string, expectedRecorded int)` reads the recorded pids. It fails with `actual %d recorded, expected %d` when their count differs from `expectedRecorded`. It then polls every 10ms for up to 5s until `syscall.Kill(pid, 0)` fails for every pid, and fails with `actual alive %v, expected none` if any remain. It follows `waitForFile` (`internal/analyze/run_test.go:204-214`).
     - `startDetached(t *testing.T)` runs `exec.Command("sh", "-c", <script>).Run()`, where `<script>` is the Go raw string `perl -e 'setpgrp(0, 0); exec @ARGV' sh -c 'echo $$ >> "$STUB_DETACHED"; exec sleep 30' < /dev/null > /dev/null 2>&1 &`. It then polls every 10ms for up to 5s until `$STUB_DETACHED` holds a line, and fails otherwise.
     - `assertMarked(t *testing.T, path string)` fails with `actual no %s, expected it to exist` when `os.Stat(path)` fails.
     - `forkingPsStub(t *testing.T, bin string, lines int)` writes, via `stub(t, bin, "ps", …)`, a `ps` that first runs `out=$(PATH=/usr/bin:/bin ps "$@")`. On its first call only (marker `"$STUB_LOG.fork"`, created with `: > "$STUB_LOG.fork"`), it then waits until `$STUB_DETACHED` holds `lines` lines: `while [ "$(cat "$STUB_DETACHED" 2>/dev/null | wc -l | tr -d ' ')" -lt <lines> ] && [ $n -lt 200 ]; do sleep 0.01; n=$((n+1)); done`, with `n=0` first and `<lines>` from `strconv.Itoa(lines)`. Finally it runs `printf '%s\n' "$out"`. The listing is taken before the marker exists, so a child that the marker releases is missing from it.
   - Modify `TestAnalyzeStopsADetachedGradleDaemonBeforeItWritesAfterTheTimeout` (`internal/analyze/run_test.go:819-841`): add `recordDetached(t)` to its given block. Its stub's inner `sh -c '…'` begins with `echo $$ >> "$STUB_DETACHED"; ` before `while :;`. The assertions and the rest are unchanged.
   - Add the five new tests listed in `## Tests`.
   Serves #28 criteria 1–3.

10. **Modify the internal/app tests**:
    - `internal/app/app_test.go:81,86-107`: `fakeProviders(t)` returns the PATH string `bin+sep+tools+sep+os.Getenv("PATH")` instead of calling `t.Setenv`. `newFixtureIn` sets `Path: fakeProviders(t)` in the `Env` literal at `:81` and drops the separate `fakeProviders(t)` call at `:82`. Reading `os.Getenv("PATH")` is fine; only setting it is process-wide.
    - `TestARunWithEveryProviderBinaryOnPathPassesPreflight` (`internal/app/app_test.go:141-155`): `bin := filepath.SplitList(f.env.Path)[0]`, and `f.env.lookPath(name)` in place of `exec.LookPath(name)`.
    - `internal/app/app_test.go:377`, `:393` and `:1230`: `t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))` → `f.env.Path = bin + string(os.PathListSeparator) + f.env.Path`.
    - `TestACodexModelAliasResolvesToTheNewestCatalogVersionOncePerRun` (`internal/app/app_test.go:553`): `exec.LookPath("codex")` → `f.env.lookPath("codex")`.
    - `toolchainPath` (`internal/app/app_test.go:1047-1065`) becomes `func (f *fixture) toolchainPath(tools ...string)`. It keeps the real-git wrapper (`exec.LookPath("git")` on the process PATH is how it finds the real binary) and ends with `f.env.Path = bin + string(os.PathListSeparator) + filepath.SplitList(f.env.Path)[0]`. Its callers at `:1092`, `:1123`, `:1141` and `:1156` become `f.toolchainPath(...)`.
    - `TestAProviderWhoseVersionCommandIsMissingRecordsAQuestionMark` (`internal/app/app_test.go:1257-1263`): `providerVersion(p)` → `providerVersion(Env{Path: t.TempDir()}, p)`.
    - Rewrite `TestALoserNamesAWinnerThatPublishesLate` (`internal/app/lock_test.go:18-40`) as:
      - given: the fixture, `st := store.New(f.root)`, `lock, err := st.Lock()` (`t.Fatal` on error), `t.Cleanup(func() { lock.Release("", 0) })`, and `ready := publishOnceTheLoserWaitsOnTheGate(st, lock, "20260918-120000", 4242)`;
      - when: `_, actualErr := f.preflight(f.todo, "--plain")`;
      - then: `if err := <-ready; err != nil { t.Fatal(err) }`, and fail with `actual code %d err %v, expected 4 and "run 20260918-120000 is live in pid 4242"` unless `exitCode(t, actualErr) == 4` and the message contains `run 20260918-120000 is live in pid 4242`.
      Add to `internal/app/lock_test.go` the helper `func publishOnceTheLoserWaitsOnTheGate(st *store.Store, lock *store.Lock, runID string, pid int) <-chan error`. It returns a 1-buffered channel and starts a goroutine that polls every 5ms, for up to 10s, until `runtime.Stack(buf, true)` (with a 1 MiB `buf`) contains `syscall.Flock(`. Whatever the outcome, it then runs `st.SetCurrent(runID, pid)` and `lock.Publish()`, so a timed-out wait can never leave preflight blocked on the gate. It sends the first error: `errors.New("no goroutine waited on the run gate within 10s")` for the timeout, else the `SetCurrent` error, else nil.
    - Add `t.Parallel()` as the first statement of every top-level `Test…` function in `internal/app/*_test.go` except these 9, which stay serial:
      - `TestALoserNamesAWinnerThatPublishesLate` (`internal/app/lock_test.go`; its goroutine dump must see only its own goroutines in `flock`)
      - `TestIntakeWritesItsMCPConfigWhenTheRepoRootAndTempDirHaveASpace` (`internal/app/intake_test.go`, `t.Setenv("TMPDIR")`)
      - `TestAWatchdogAndAStepSessionWriteTheirMCPConfigUnderARepoRootWithASpace`, `TestATempDirInsideTheRepoRefusesToStartTheWatchdog` and `TestARelativeTempDirOutsideTheRepoGivesTheWatchdogAnAbsoluteConfigPath` (`internal/app/watchdog_test.go`, `t.Setenv("TMPDIR")` / `t.Chdir`)
      - `TestEachSignalHaltsALiveStepAsInterrupted`, `TestSIGTERMDuringALongGateKillsTheGateAbortsTheMergeAndHaltsTheRun`, `TestASignalWhileTheDisplayWaitsForQuitEndsExecute` and `TestSignalAfterTUIStartsPrintsHaltReasonAndResume` (`internal/app/signal_test.go`, `syscall.Kill(os.Getpid(), …)`, which every live `Execute`'s signal context would catch)
      Go runs every serial top-level test to completion before any paused parallel test resumes, so these never overlap the parallel ones. Paused parallel tests sit in `t.Parallel()`, never in `flock`.
    - Add `t.Parallel()` as the first statement inside the `t.Run` body of `TestUnattendedAndYesStartWithoutAGate` (`internal/app/triage_test.go:293-316`) and of `TestNoTriageTableIsTypedIntoTheWatchdogPaneWithoutAGate` (`internal/app/triage_test.go:537-551`). Each body declares its own variables, and Go 1.22+ loop variables are per iteration.
    - Add the three new app tests listed in `## Tests` (in `internal/app/app_test.go`).
    Serves #16 criteria 2–4.

11. **Verify, outside the gate** (these are timing properties of whole suites, not behaviours of one test). Run each of these and record the result in the step's report:
    - `go test -race -count=3 ./internal/app/` passes with Go's default timeout. It is expected to take about 4 minutes.
    - `time go test ./internal/app/ -count=1 -run '^TestNoTriageTableIsTypedIntoTheWatchdogPaneWithoutAGate$'` and the same for `^TestUnattendedAndYesStartWithoutAGate$`. Each must take under 4.2s wall time, half of today's ~8.4s.
    - `pgrep -f 'late.html' | wc -l` before and after `go test -count=20 ./internal/analyze/ -run TestAnalyzeStopsADetachedGradleDaemon` prints the same count. The 12 loops already orphaned on this machine are not touched.

## Tests

Write these first.

- `internal/core/hooks_test.go` · `TestAHookWaitGivesUpAtItsDeadlineNotAfterAFullHookPoll` is a table test, run with `hookPoll` at its 200ms default. Each row builds `sm := hookPromptManager(&fakeSessionHost{}, &fakeHookFeed{}, t0)`, times the wait with `time.Now()`/`time.Since`, and fails when `actualElapsed >= 30*time.Millisecond` (expected: under 30ms). It covers #16 criterion 1 and the phase check's three-site warning. The rows:
  - "a session start without a SessionStart": `sm.HookStartWait = 5 * time.Millisecond`, then `sm.hookStart(&Session{Agent: askingAgent}, true)`.
  - "a prompt never submitted": `sm.awaitSubmit(askingAgent, 5*time.Millisecond)`.
  - "a rollout never shown": `shortSubmitWait(t)`, `sm.Rollouts = &fakeRollouts{files: map[string]rolloutFile{}}`, then `sm.awaitRollout(&Session{Agent: askingAgent, rollout: true})`.
- `internal/app/app_test.go` · `TestTheEnvPathLookupFollowsTheProcessLookupRules` is a table test that calls `Env{Path: …}.lookPath(name)` and compares `actualPath` and `fmt.Sprint(actualErr)` against hard-coded expectations. It covers the lookup's edge cases, whose input is the user's PATH: a non-executable file of the same name, one only its group may run, a relative entry ahead of an absolute one, a missing tool, and a path given as the name. The rows:
  - "an executable in a later directory after a non-executable one": `a/tool` with mode 0644 and `b/tool` with mode 0755, `Path = a+":"+b`. Expected `b/tool` and `<nil>`.
  - "an executable in a later directory after one only its group may run": `a/tool` with mode 0010, owned by the test user, and `b/tool` with mode 0755, `Path = a+":"+b`. Expected `b/tool` and `<nil>`, as `exec.LookPath` picks for the same PATH.
  - "a relative directory ahead of an absolute one stops the lookup": `rel` and `abs` both hold an executable `tool`, and `Path = relPath+":"+abs`, where `relPath` is `rel`'s path relative to `os.Getwd()`. Expected `filepath.Join(relPath, "tool")` and `exec: "tool": cannot run executable found relative to current directory`, which is what `exec.LookPath` returns for the same PATH.
  - "a tool on no directory": `Path` is an empty temp dir. Expected `""` and `exec: "tool": executable file not found in $PATH`.
  - "a name with a separator is tried as given": the name is the absolute path of an executable `tool`, `Path = ""`. Expected that path and `<nil>`.
- `internal/app/app_test.go` · `TestPreflightFindsAProviderBinaryOnTheEnvPathAlone`: the config is `providers:\n  solo:\n    kind: rloop-env-path-only\n    doneSignal: sentinel\n    ask: mcp\nwatchdog:\n  provider: solo\n`. An executable `rloop-env-path-only` (`#!/bin/sh\nexit 0\n`) sits in a temp dir prepended to `f.env.Path`, and nothing on the process PATH has that name. `f.preflight(f.todo, "--plain")` returns a nil error. It covers #16 criterion 3: provider binaries are found on `Env.Path`, not the process PATH.
- `internal/app/app_test.go` · `TestAWiredRunRunsTheSemgrepOnTheEnvPath`: `newResumeFixture(t, reviewConfig)`. A temp dir prepended to `f.env.Path` holds a `semgrep` that runs the fixture semgrep script's `--output` loop after `echo "$1" >> "$0.calls"`. `f.firstRun(newSim(), "--phases", "1")` exits 0 and `<dir>/semgrep.calls` reads exactly `scan\n`. It covers #16 criterion 3: the analyzer resolves through `Env.Path`, so a real semgrep on the process PATH never runs.
- `internal/app/app_test.go` · `TestARunWithEveryProviderBinaryOnPathPassesPreflight` (modified per Changes 10): the fixture's fake `claude` and `codex` are found in the first `Env.Path` directory and preflight passes. It covers #16 criterion 3: the fixture reaches its fakes without `t.Setenv`.
- `internal/app/app_test.go` · `TestACodexModelAliasResolvesToTheNewestCatalogVersionOncePerRun` (modified): the catalog runs the `Env.Path` codex exactly once per run. It covers #16 criterion 3 (`runCatalog`).
- `internal/app/app_test.go` · `TestPreflightPassesAReviewerWhoseProviderHasOnlyAReviewExec` and `TestPreflightPassesASecurityReviewerOnAnyProviderAndInstallsNothing` (modified): the per-test codex prepended to `f.env.Path` is the one preflight runs. They cover #16 criterion 3.
- `internal/app/app_test.go` · `TestPreflightPrintsTheStaticLine`, `TestPreflightExitsTwoNamingAMissingToolchain`, `TestPreflightChecksNoToolchainWhenAnalyzeIsOff` and `TestAMissingToolchainIsNotCheckedInDryRun` (modified to `f.toolchainPath`): the semgrep, runner and java checks see only the restricted `Env.Path`. On this machine `/usr/bin/java` and `/opt/homebrew/bin/semgrep` are on the process PATH, so a lookup that leaked to it would fail the "semgrep not installed" and "needs java" rows. They cover #16 criterion 3 (`static`, `toolchain`).
- `internal/app/app_test.go` · `TestRunStartRecordsEveryNamedProvidersVersion` (modified): versions come from the `Env.Path` fakes (`2.1.220 (Claude Code)`, and `?` for codex's exit 3). It covers #16 criterion 3 (`providerVersion`).
- `internal/app/app_test.go` · `TestAProviderWhoseVersionCommandIsMissingRecordsAQuestionMark` (modified): a kind on no `Env.Path` directory records `?`. It covers `providerVersion`'s lookup-error path.
- `internal/app/lock_test.go` · `TestALoserNamesAWinnerThatPublishesLate` (rewritten): the winner publishes only once the loser is blocked on the gate's `flock`, and the loser exits 4 with `run 20260918-120000 is live in pid 4242`, with no fixed sleep. It covers #16 criterion 4 and keeps the late-publication path the test is named for.
- `internal/app/triage_test.go` · `TestUnattendedAndYesStartWithoutAGate` and `TestNoTriageTableIsTypedIntoTheWatchdogPaneWithoutAGate` (subtests now parallel): the behaviour is unchanged, and the wall time is checked by the step 11 commands. They cover #16 criterion 2.
- `internal/analyze/run_test.go` · `TestAnalyzeStopsADetachedGradleDaemonBeforeItWritesAfterTheTimeout` (modified): the stub's detached loop records its pid, and `recordDetached`'s cleanup kills its group even if Analyze missed it. The assertions are unchanged. It covers #28 criterion 1.
- `internal/analyze/run_test.go` · `TestTheDetachedStubCleanupKillsAChildAnalyzeNeverReached`:
  - given: `path := recordDetached(t)`, the outer guard that kills anything left at the very end;
  - when: `t.Run("a stub Analyze never reached", func(t *testing.T) { killRecordedOnCleanup(t, path); startDetached(t) })`;
  - then: `assertDetachedGone(t, path, 1)`.
  Only the subtest's cleanup can have killed the child before the then block. It covers #28 criterion 2.
- `internal/analyze/run_test.go` · `TestAnalyzeKillsADetachedGradleDaemonWhenTheFirstProcessListingsFail`. Given `stubPath`, `javaEnv(t, ".", pmdReport(""), `{"runs":[]}`)` and `path := recordDetached(t)`. A `ps` stub in `bin` that runs `echo x >> "$STUB_LOG.ps-calls"`, then `if [ "$(wc -l < "$STUB_LOG.ps-calls" | tr -d ' ')" -le 5 ]; then exit 1; fi`, then `PATH=/usr/bin:/bin exec ps "$@"`. A `gradle` stub `perl -e 'setpgrp(0, 0); exec @ARGV' sh -c 'echo $$ >> "$STUB_DETACHED"; while :; do sleep 0.05; done' < /dev/null > /dev/null 2>&1 &` followed by `sleep 30`. A `stubbedRepo` with `build.gradle` and `src/main/java/A.java`. When `New(time.Second, exec.LookPath).Analyze(context.Background(), dir)` runs, then `assertDetachedGone(t, path, 1)`. On the base code `descendants` gets nil from the failed `ps` and the loop survives. Five failures is more than any small fixed retry count. It covers #28 criterion 3 (the `ps` failure hole).
- `internal/analyze/run_test.go` · `TestAnalyzeKillsADetachedGradleDaemonWhenTheFirstProcessListingHangs`: the same setup, except the `ps` stub runs `if [ ! -e "$STUB_LOG.ps-hung" ]; then : > "$STUB_LOG.ps-hung"; exec sleep 30; fi` and then `PATH=/usr/bin:/bin exec ps "$@"`. When it runs with `New(time.Second, exec.LookPath)`, then `assertDetachedGone(t, path, 1)`. The first listing times out after `psTimeout` and the retry finds the loop. On the base code the listing blocks for 30s and then kills nothing detached. It covers #28 criterion 3 (a hung `ps`; `Cancel` stays bounded).
- `internal/analyze/run_test.go` · `TestAnalyzeStopsAGradleBeforeItDetachesAChildAfterTheFirstListing`. Given `stubPath`, `javaEnv`, `path := recordDetached(t)`, `forkingPsStub(t, bin, 1)`, the gradle repo, and a `gradle` stub that runs `while [ ! -e "$STUB_LOG.fork" ]; do sleep 0.01; done`, then `perl -e 'setpgrp(0, 0); exec @ARGV' sh -c 'echo $$ >> "$STUB_DETACHED"; exec sleep 30' < /dev/null > /dev/null 2>&1 &`, then `sleep 30`. When `actualErr := analyzeErr(t, New(time.Second, exec.LookPath), dir)` runs, then: `actualErr` equals `gradle: timed out after 1s: context deadline exceeded`, so Gradle was still running at the timeout; `assertMarked(t, os.Getenv("STUB_LOG")+".fork")` passes, so the cancel path took a listing while the gradle stub waited on it; and `assertDetachedGone(t, path, 0)` passes. `analyzeErr` is the existing helper at `internal/analyze/run_test.go:178`. `assertMarked(t, path)` is a new helper that fails with `actual no %s, expected it to exist` when `os.Stat` fails. With the root group stopped before the first listing, the gradle never detaches its child. On the base code the child forks after the listing, reaches `setpgrp`, records itself and survives the group kill. It covers #28 criterion 3 (a root child that forks after the snapshot and reaches `setpgrp` before the group kill), as a forced race.
- `internal/analyze/run_test.go` · `TestAnalyzeKillsTheChildADetachedDaemonForksAfterTheFirstListing`. Given `stubPath`, `javaEnv`, `path := recordDetached(t)`, `forkingPsStub(t, bin, 2)` and the gradle repo. A `daemon` stub in `bin` that runs `echo $$ >> "$STUB_DETACHED"`, waits with `while [ ! -e "$STUB_LOG.fork" ]; do sleep 0.01; done`, then runs `perl -e 'setpgrp(0, 0); exec @ARGV' sh -c 'echo $$ >> "$STUB_DETACHED"; exec sleep 30' < /dev/null > /dev/null 2>&1 &` and `sleep 30`. A `gradle` stub `perl -e 'setpgrp(0, 0); exec @ARGV' daemon < /dev/null > /dev/null 2>&1 &` followed by `sleep 30`. When it runs with `New(time.Second, exec.LookPath)`, then `assertDetachedGone(t, path, 2)`. The first listing holds the daemon but not its new child. Only a re-walk after stopping the daemon finds that child. The base code, and a root-stop without a re-walk, both leave it alive. It covers #28 criterion 3 (a detached grandchild's child forked after the snapshot), as a forced race.

## Left out

- Resolving git once in `internal/gitrepo` (and in `internal/store` and `internal/analyze/lines.go`): measured, it saves nothing on the triage tests, and no fake git is reached through them.
- Threading `Env.Path` into `gitrepo`, `store`, `herdr.Client`, `notify`, `reviewexec`, or into child processes' `Env`: no fake binary reaches them (call log above), and in production their PATH is the same value.
- A shared lookup package, or a second copy of `lookPath` in analyze: analyze takes the lookup as a `New` parameter, so one implementation serves both.
- An `Env.LookPath func` field: the fixtures would fake lookup rules the code really depends on.
- Waking the hook waits on the hook log's `changed` channel: a deadline with no events, the case asked about, gains nothing.
- `handOff.wait`'s `time.After(hookPoll)` (`internal/core/hooks.go:385-398`): it already returns on the first hook event and has no deadline to overshoot.
- The 200ms sleeps in `TestALoserNamesTheLiveRunNotAStalePointer` and `TestALoserWhoseWinnerGivesUpProceeds`: the item names only the 2.5s one.
- `t.Parallel()` in the analyze tests and in other tables' subtests: no item asks for them. The analyze tests set PATH, HOME and TMPDIR per test.
- The `ps` walk in `internal/gitrepo/repo.go:866`: it only lists JVMs for thread dumps and kills nothing.
- Honouring `GODEBUG=execerrdot=0` in `Env.lookPath`: r-loop never sets it, and Go's own handling sits in `internal/godebug`, which is out of reach.
- A second process-listing mechanism for a `ps` that never answers within the budget: it would need platform code or `golang.org/x/sys`, a new dependency.
- Killing the 12 orphaned loops already on this machine: they are outside the repository. The step 11 check compares counts instead.

## Assumptions

- `Env.Path` is the process `PATH` in production (`cmd/r-loop/main.go`), so leaving every child's environment as `os.Environ()` changes nothing at run time. No test fake resolves a binary by name: the fakes are `/bin/sh` scripts using builtins, and the toolchain git wrapper execs an absolute path. This answers the phase check's "every exec's Env" warning.
- `Env.lookPath` keeps `exec.LookPath`'s ordering, its executable check and its `exec.ErrDot` result for a relative or empty PATH entry.
- The core timing test's bound is 30ms: about 3× the "about 10ms" the item asks for, and well under a 200ms `hookPoll`. Core tests run serially, so only machine load eats into the slack.
- In the lock test, the `syscall.Flock(` frame appears as the loser enters `Flock`. A publish in the instant before the kernel blocks the call takes the same path as one after, because the gate is released either way and `internal/store/store.go:342-348` reads `Current` once the loser holds it.
- `psTimeout` (500ms) and `walkBudget` (2s) bound `Cancel`. A `ps` that never answers within the budget leaves detached children alive, because there is no other portable process listing (see `## Left out`). The root group is still killed.
- The new analyze tests use a 1s Analyze timeout, enough for each stub to have forked and recorded its first child. The `forkingPsStub` wait gives up after 2s, so with the fix its first listing ends at `psTimeout` and the retry runs.
- 8 of the 9 serial app tests are exactly those that call `t.Setenv`, `t.Chdir` or signal the test process. Go panics if `t.Parallel` and `t.Setenv` meet in one test, so the implementer cannot get this list wrong silently. The ninth is the lock test, for its goroutine dump.
- The existing 1s sleep after Analyze in `TestAnalyzeStopsADetachedGradleDaemonBeforeItWritesAfterTheTimeout`, the first-argument-only semgrep spy, the `landRecorder` the app fixtures use, and the style of the existing tests this phase only edits are left as they are. They were reviewed and judged P3 or out of scope.
- Timings come from this machine (18 CPUs, darwin): 315s for one serial pass today, 78s for one parallel `-race` pass, and 3.6–3.7s per triage subtest in parallel.

## Gate

`go test ./internal/core/ -count=1 -run '^TestAHookWaitGivesUpAtItsDeadlineNotAfterAFullHookPoll$' && go test ./internal/app/ -count=1 -run '^(TestTheEnvPathLookupFollowsTheProcessLookupRules|TestPreflightFindsAProviderBinaryOnTheEnvPathAlone|TestAWiredRunRunsTheSemgrepOnTheEnvPath|TestARunWithEveryProviderBinaryOnPathPassesPreflight|TestACodexModelAliasResolvesToTheNewestCatalogVersionOncePerRun|TestPreflightPassesAReviewerWhoseProviderHasOnlyAReviewExec|TestPreflightPassesASecurityReviewerOnAnyProviderAndInstallsNothing|TestPreflightPrintsTheStaticLine|TestPreflightExitsTwoNamingAMissingToolchain|TestPreflightChecksNoToolchainWhenAnalyzeIsOff|TestAMissingToolchainIsNotCheckedInDryRun|TestRunStartRecordsEveryNamedProvidersVersion|TestAProviderWhoseVersionCommandIsMissingRecordsAQuestionMark|TestALoserNamesAWinnerThatPublishesLate|TestUnattendedAndYesStartWithoutAGate|TestNoTriageTableIsTypedIntoTheWatchdogPaneWithoutAGate)$' && go test ./internal/analyze/ -count=1 -run '^(TestAnalyzeStopsADetachedGradleDaemonBeforeItWritesAfterTheTimeout|TestTheDetachedStubCleanupKillsAChildAnalyzeNeverReached|TestAnalyzeKillsADetachedGradleDaemonWhenTheFirstProcessListingsFail|TestAnalyzeKillsADetachedGradleDaemonWhenTheFirstProcessListingHangs|TestAnalyzeStopsAGradleBeforeItDetachesAChildAfterTheFirstListing|TestAnalyzeKillsTheChildADetachedDaemonForksAfterTheFirstListing)$'`
