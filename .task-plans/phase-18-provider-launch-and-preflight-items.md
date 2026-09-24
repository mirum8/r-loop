status: planned

## Summary

This phase makes two fixes: backlog [#17] (open item #18), where a space in the repo path breaks `--mcp-config`, and [#28] (open item #29), where provider binaries are not checked in preflight.

**[#17].** `providers.Args` fills in the placeholders and only then splits each flag template on whitespace (`internal/providers/registry.go:126-149`). With the repo root `/x/repo with space`, the claude ask flag `--mcp-config {mcpConfig}` therefore becomes `--mcp-config`, `/x/repo`, `with`, `space/.r-loop/runs/<id>/watchdog.mcp.json`. The three writers of the MCP config file are the watchdog (`internal/app/wire.go:559`), a step session (`internal/core/session.go:442`) and intake (`internal/app/intake.go:99`). Each one writes the file only if some argument contains the whole config path. No argument does, so the file is never written and the agent starts pointing at a file that does not exist. The fix changes the order: split the template on whitespace first, then replace the placeholders inside each word. A placeholder value then always stays one argument, and the three writers' existing `strings.Contains` checks start matching again without any change. A prototype run confirmed the bug on base: it ran the shipped claude provider under a spaced repo root, and the watchdog and plan-step arguments came back split, with no `.mcp.json` written.

**[#28].** Provider binaries are checked in `validateProviders` (`internal/app/preflight.go:267`), which runs from `checks()` (`preflight.go:158`) for both a fresh run and a resume (`internal/app/resume.go:378`). The check has two passes:
1. The existing pass validates every role's config, unchanged.
2. Unless `--dry-run` is set, a second pass calls `exec.LookPath` on each role's provider `kind`. `herdr agent start --kind <kind>` (`internal/herdr/client.go:182`) launches exactly that binary. The first missing binary exits 127 with `<field>: provider <name> binary <kind> not found on PATH`.

`checkRole` now also returns the provider it resolved, so pass 2 does not resolve each provider again. A free-form invocation runs intake before `Wire` and `Preflight` (`internal/app/wire.go:220-222`), so intake would start its session before preflight ever ran. `newIntake` (`internal/app/intake.go:71-74`) therefore runs the same binary check for `intake.provider` right after its `checkRole` call, before its herdr check and before any session starts. Both call sites share one helper, `checkBinary`, so the message cannot drift.

Choices:
- **How to keep a spaced value as one argument: split first and then fill in each word, or add quoting syntax to the templates.** Split first. It needs no new template syntax and no config change. Templates without placeholders split exactly as they do today. Values are still never re-expanded, because one `strings.Replacer` pass runs per word.
- **Where the MCP-config fix lives: in `Args`, or in the three `strings.Contains(a, mcpPath)` checks.** In `Args`. The checks are correct once the path arrives as one argument. Changing them would still leave claude receiving a split path.
- **Where the binary check lives: its own pass after all config validation, or inside `checkRole` for each role.** Its own pass. The existing refusals with exit 2 (`TestReviewerWithoutReviewCommandIsRefusedWithExit2`, `TestGateFixReviewersAreValidatedWhenImplementIsNotInThePipeline`, the no-MCP tests) use made-up kinds such as `bare` or rely on `codex` being present. A check inside `checkRole` could turn those refusals into 127, depending on the machine's PATH. A config error is also more specific than a missing binary.
- **Which binary name to check: the provider's `kind`, or a new `bin` provider key.** `kind`. herdr launches the binary named by `--kind`, and the shipped kinds `claude` and `codex` are those binaries. A new key would be a config concept that no item asks for.
- **Intake's binary check: skipped under `--dry-run` or always run.** Always run. Intake starts a real session before the argv, and with it `--dry-run`, is even known, so its binary is needed either way.
- **How existing tests stay green on a machine without `claude` or `codex`: stub binaries on PATH in the shared fixture, or skip the check in tests.** Stubs in `newFixture`. The production path stays the same, and the existing tests become the proof that "a run whose binaries are all on PATH starts exactly as today".

## Changes

Build in this order.

1. **Modify `internal/providers/registry.go`** (serves #18 c1, c2, c3).
   - `expand(tmpl string, values map[string]string) ([]string, bool)`. It keeps the current early returns: `nil, false` for an empty template, and `nil, false` when the template contains a placeholder whose value is empty. It builds `r := strings.NewReplacer(pairs...)` as today. Then it does `words := strings.Fields(tmpl)`, sets `words[i] = r.Replace(w)` for each word, and returns `words, true`.
   - `Args`. The loop body becomes `if words, ok := expand(tmpl, values); ok { args = append(args, words...) }`. `strings.Fields(p.Flags)` is unchanged, because flags cannot contain placeholders (`registry.go:98`).
   - `ToCore` and `shellWord` are unchanged. `shellWord` now quotes a spaced value as one shell word, which is the correct result for the review command.
2. **Modify `internal/app/preflight.go`** (serves #29 c1, c2, c3).
   - `func checkRole(reg *providers.Registry, r role) (providers.Provider, error)`. Every existing error return becomes `return providers.Provider{}, exit(...)` with the same message. On success it returns `p, nil`.
   - Add `func checkBinary(r role, kind string) error`. It returns `exit(127, "%s: provider %s binary %s not found on PATH", r.field, r.provider, kind)` when `exec.LookPath(kind)` fails, and nil otherwise. Placed after `checkRole`.
   - `validateProviders`. It builds `roles` as today. Pass 1: `resolved := make([]providers.Provider, len(roles))`, and for `i, r := range roles`, `p, err := checkRole(w.Registry, r)`, returning `err` if it is set, then `resolved[i] = p`. Then `if w.Opts.DryRun { return nil }`. Pass 2: for `i, r := range roles`, `if err := checkBinary(r, resolved[i].Kind); err != nil { return err }`. It follows the herdr check at `preflight.go:160-164`: the same `exec.LookPath`, the same `!opts.DryRun` gate and the same exit code 127.
   - `checks()` is unchanged. It already calls `validateProviders` after the herdr check and before `Host.Reachable` (`preflight.go:44`), so a missing binary exits before herdr is contacted or a run is created.
3. **Modify `internal/app/intake.go`** (serves #29 c1 on the free-form path, and follows the `checkRole` signature change).
   - Lines 71-74 become `r := role{field: "intake.provider", provider: cfg.Intake.Provider}`, then `p, err := checkRole(reg, r)` with `if err != nil { return nil, err }`, then `if err := checkBinary(r, p.Kind); err != nil { return nil, err }`. The now-redundant `p, _ := reg.Resolve(cfg.Intake.Provider)` is deleted. Both checks come before the herdr `exec.LookPath` at line 75, so a missing intake binary exits 127 before herdr is touched.
4. **Modify `internal/app/app_test.go`** (test support for #18 c2 and #29 c3).
   - `newFixture(t)` becomes: `root, err := filepath.EvalSymlinks(t.TempDir())`, fail on error, then `return newFixtureIn(t, root)`.
   - `newFixtureIn(t *testing.T, root string) *fixture` holds the current body of `newFixture` from `f := &fixture{...}` onward (`app_test.go:63-73`), plus one line before `return f`: `fakeProviders(t)`.
   - `fakeProviders(t *testing.T)`. `bin := t.TempDir()`. For `claude` and `codex`, it writes `#!/bin/sh\nexit 0\n` with mode `0o755` into `bin`, then calls `t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))`. No test in `internal/app` calls `t.Parallel`, so `t.Setenv` is safe.
5. **Add the tests below** to `internal/providers/registry_test.go`, `internal/app/app_test.go`, `internal/app/watchdog_test.go` and `internal/app/intake_test.go`.

## Tests

Write these first.

`internal/providers/registry_test.go`:
- `TestArgsKeepAPlaceholderValueWithSpacesAsOneArgument`. First case: the shipped claude block (`NewRegistry(nil, nil, t.TempDir()).Resolve("claude")`) with `Args(claude, "opus", "", "", "/x/repo with space/.r-loop/runs/r1/watchdog.mcp.json")` equals `["--model", "opus", "--mcp-config", "/x/repo with space/.r-loop/runs/r1/watchdog.mcp.json"]`. Second case: a project block `mine: {kind: claude, askFlag: "--cfg={mcpConfig}", doneSignal: sentinel, ask: mcp}`, built with `projectBlocks` and `Args(p, "", "", "", "/x/a b/c.json")`, equals `["--cfg=/x/a b/c.json"]`. Covers #18 c1. Fails on base.
- `TestArgsSplitTemplatesWithoutPlaceholdersOnWhitespace`. Uses a project block `mine: {kind: codex, flags: "  -c a=1\t--no-alt-screen   -x ", modelFlag: "-c   model={model}", doneSignal: sentinel}`. `Args(p, "gpt-5", "", "", "")` equals `["-c", "a=1", "--no-alt-screen", "-x", "-c", "model=gpt-5"]`. Covers #18 c3, and passes on base as a regression guard. The existing `TestArgsOfShippedBlocks`, `TestArgsOmitAskFlagWithoutItsValue` and `TestArgsDoNotReExpandPlaceholdersInValues` must still pass unchanged.

`internal/app/watchdog_test.go`:
- `TestAWatchdogAndAStepSessionWriteTheirMCPConfigUnderARepoRootWithASpace`. Covers #18 c1 and c2 for the watchdog and a step session, through the real `w.resolve` and the shipped claude provider. Fails on base.
  - Setup:
    - `base := EvalSymlinks(t.TempDir())`, `root := filepath.Join(base, "repo with space")`, `os.MkdirAll(root)`, `link := filepath.Join(base, "link")`, `os.Symlink(root, link)`.
    - `f := newFixtureIn(t, root)`. Then, the way `newResumeFixture` does it: `f.write("docs/topic/todo.md", resumeTodo)`, `f.write(".r-loop/config.yaml", noReviewConfig)`, `f.commit()`, `f.env.PID = os.Getpid()`, `f.env.Pane = "driver-pane"`.
    - `w, err := f.preflight(f.todo, "--plain", "--phases", "1")`, `f.sim(w, newSim())`.
    - A test-local `configAtStart{core.SessionHost; mu sync.Mutex; mcp map[string]string; missing []string}` wraps a host. Its `Start(pane, name, kind, args)` finds the element after `"--mcp-config"` in `args`. It records `mcp[agentRole(name)] = that element` under `mu`, and if `os.Stat(that element)` fails at that moment it appends `agentRole(name)` to `missing`. Then it delegates to the wrapped `Start`.
    - A test-local `spacedSim{*configAtStart; root, link string}` overrides only `Prompt`, delegating with `strings.ReplaceAll(text, root, link)`. `simHost` finds the sentinel with a whitespace-delimited regex (`resume_test.go:46`), and a prototype showed that without this rewrite it writes `internal/app/space/...` and the step hangs.
    - `steps := &configAtStart{SessionHost: newSim(), mcp: map[string]string{}}`, `w.Loop.Sessions.Host = spacedSim{configAtStart: steps, root: root, link: link}`.
    - `dog := &configAtStart{SessionHost: &dogHost{}, mcp: map[string]string{}}`, `w.Dog.Host = dog`.
  - Act: `code := w.Execute(core.RunOptions{Phases: []string{"1"}})`.
  - Assert:
    - `code == 0`.
    - `mcpPath := filepath.Join(w.Store.Dir(w.Loop.RunID), "watchdog.mcp.json")`. `w.Dog.Provider.Args` holds `"--mcp-config"` immediately followed by the element `mcpPath`, and `mcpPath` contains `"repo with space"`. The file at `mcpPath` contains `"/mcp/watchdog/"`.
    - `dog.missing` and `steps.missing` are empty. Each config existed when `Start` ran, which proves the before-start order for both the watchdog and the step session.
    - `dog.mcp` has exactly one entry, and it equals `mcpPath`.
    - `steps.mcp["rloop-p1-plan"]` starts with `root`, ends with `-p1-plan.mcp.json`, and its file contains `"/mcp/"`.
- `TestIntakeWritesItsMCPConfigWhenTheRepoRootAndTempDirHaveASpace`. Covers #18 c2 for intake. The intake config lives under `os.MkdirTemp("", ...)` (`intake.go:87`), so `TMPDIR` also gets a space. On base the split path makes `converse` fail to read the config, and the run times out. Goes in `internal/app/intake_test.go`.
  - Setup: `base := EvalSymlinks(t.TempDir())`, `root := filepath.Join(base, "repo with space")`, `tmp := filepath.Join(base, "tmp with space")`, `MkdirAll` both, `f := newFixtureIn(t, root)`, `f.commit()`, `t.Setenv("TMPDIR", tmp)`. The host is `&intakeHost{done: make(chan struct{}), submit: [][]string{{"docs/topic/todo.md", "--phases", "2"}}}`.
  - Act: `opts, err := f.intake(host, "the topic plan, only phase 2", "--plain")`, then `<-host.done`.
  - Support: `intakeHost` (`intake_test.go:18`) gets one field, `configAtStart bool`. `intakeHost.Start` sets it under `h.mu` to whether `os.Stat` of the element after `"--mcp-config"` in `args` succeeds at Start time. It is false when there is no such element.
  - Assert: `err == nil`, `host.errs` is empty, `host.configAtStart` is true (the config existed when `Start` ran), `opts.Phases` equals `["2"]`, and the element of `host.args` after `"--mcp-config"` starts with `tmp`.
- `TestAFreeFormRunWithAMissingIntakeBinaryExits127BeforeStarting`. Covers #29 c1 on the free-form path, where intake runs before preflight. Fails on base, where the run exits 4 at herdr.
  - Setup: `f := newFixture(t)`, `f.write(".r-loop/config.yaml", "providers:\n  ghost:\n    kind: rloop-no-such-binary\n    doneSignal: sentinel\n    ask: mcp\nintake:\n  provider: ghost\n")`, `f.commit()`.
  - Act: `code := f.main("the topic plan")`.
  - Assert: `code == 127`, `f.err.String()` contains `intake.provider: provider ghost binary rloop-no-such-binary not found on PATH`, and `f.herdrCalled()` is false. Follows `TestAnIntakeProviderWithoutMCPExits2BeforeStarting` (`intake_test.go:213-222`).

`internal/app/app_test.go`:
- `TestAMissingProviderBinaryExits127NamingTheProviderBinaryAndField`. A table over the fields `watchdog.provider`, `intake.provider`, `steps.plan.provider`, `steps.plan.fallback`, `steps.plan.reviewers` and `land.fix.provider`. Covers #29 c1. Fails on base, where preflight passes.
  - Each case writes `providers:\n  ghost:\n    kind: rloop-no-such-binary\n    doneSignal: sentinel\n    ask: mcp\n    review: ghost review\n` plus that field's snippet. The snippets are `watchdog:\n  provider: ghost\n`, `intake:\n  provider: ghost\n`, and the four snippets from `TestAStepSessionProviderWithoutMCPIsRefusedInPreflight` (`watchdog_test.go:359-362`) with `plainbot` replaced by `ghost`.
  - Each case then runs `f.commit()` and `_, err := f.preflight(f.todo, "--plain")`.
  - Assert: `exitCode == 127`, `err.Error()` contains `<field>: provider ghost binary rloop-no-such-binary not found on PATH`, and `f.herdrCalled()` is false.
- `TestAMissingProviderBinaryIsNotCheckedInDryRun`. Writes the ghost block with `watchdog:\n  provider: ghost\n`, then `f.commit()` and `f.fakeHerdr(1)`. `f.main(f.todo, "--dry-run", "--plain")` returns 0. Covers #29 c2, and passes on base as a guard.
- `TestARunWithEveryProviderBinaryOnPathPassesPreflight`. Setup: `newFixture(t)`, `f.commit()`. Assert:
  - `exec.LookPath("claude")` and `exec.LookPath("codex")` resolve to the fixture's stubs, meaning the directory of the result is the first `PATH` entry.
  - `w, err := f.preflight(f.todo, "--plain")` returns `err == nil`.
  - `w.Loop.RunID != ""` and `f.herdrCalled()`.

  Covers #29 c3. Every existing non-dry-run preflight and run test in `internal/app` also covers c3, because it now starts with the binaries on PATH.

## Left out

- A `bin` provider key. herdr launches by `kind`, and no item asks to decouple the two.
- Deduplicating roles that share a provider before `LookPath`. It saves a few `stat` calls per start, and no obligation needs it.
- Changing the `strings.Contains(a, mcpPath)` checks in `session.go`, `wire.go` and `intake.go`. They are correct once the path is one argument.
- Changing `simHost`'s sentinel regex. That is a shared helper used by many tests. The new test's `spacedSim` symlink rewrite is local to it.

## Assumptions

- The binary a provider launches is its `kind`, as passed to `herdr agent start --kind` (`internal/herdr/client.go:182`).
- The binary check also runs on `r-loop resume`, because resume goes through the same `checks()` (`internal/app/resume.go:378`). That matches the herdr check.
- When several binaries are missing, the error names only the first role in `validateProviders` order, the same way the existing config errors work.
- The error text is `<field>: provider <name> binary <kind> not found on PATH`, which follows the herdr message `herdr binary %s not found`.

## Gate

`go test ./internal/providers/ ./internal/app/ -run '^(TestArgsKeepAPlaceholderValueWithSpacesAsOneArgument|TestArgsSplitTemplatesWithoutPlaceholdersOnWhitespace|TestAWatchdogAndAStepSessionWriteTheirMCPConfigUnderARepoRootWithASpace|TestIntakeWritesItsMCPConfigWhenTheRepoRootAndTempDirHaveASpace|TestAFreeFormRunWithAMissingIntakeBinaryExits127BeforeStarting|TestAMissingProviderBinaryExits127NamingTheProviderBinaryAndField|TestAMissingProviderBinaryIsNotCheckedInDryRun|TestARunWithEveryProviderBinaryOnPathPassesPreflight)$'`
