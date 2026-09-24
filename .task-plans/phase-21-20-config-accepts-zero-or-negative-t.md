status: planned

## Summary

Right now, loading config accepts a zero or negative duration. `resolver.duration` at `internal/config/reader.go:253` parses whatever it gets. A key set to null (`gateTimeout:` or `~`) stops the layer walk at the null node: `resolver.scalar` at `reader.go:239` returns `""` with the null layer's provenance, and `duration` turns that `""` into `0`. So a higher-layer null replaces the default 30m gate timeout with 0. `gitrepo.Repo.Run` (`internal/gitrepo/repo.go:676`) then kills the gate command at once.

The phase makes three changes, all in `internal/config`:

1. **Null falls through.** Numeric keys skip null nodes when they walk the layers. That covers durations, counts and factors. A null value resolves to the next layer that sets the key, and that layer is recorded as its provenance.
2. **Written durations must be positive.** Every duration that is written (not null) must be > 0. Otherwise loading fails with `ErrConfig` as `<file>:<line>: <key>: "<value>" is not a positive duration`. `app.Wire` already maps every `config.Load` error to exit 2 (`internal/app/wire.go:407-409`).
3. **Row timeouts must end up set.** After resolution, a row's `timeout` must be set. Its `reviewTimeout` must be set only when the row has a review half (`rounds > 0` and at least one reviewer, the same condition as `internal/core/runners.go:23` and `internal/config/banner.go:21`). Otherwise loading fails, pointing at the row.

The banner gains one line, `land gateTimeout <d>  ← <provenance>`, so the provenance of a null that fell through shows up where the maintainer reads it.

Choices:

- **Which keys a null falls through on:** only numeric keys (`duration`, `count`, `factor`) (taken), or every key. Numeric-only won. For strings, lists and `fallback`, a null already means something: `model: ~` is the provider default, `allow:` is an empty list (`defaults.yaml:60`), and `fallback: ~` means no fallback (`reader.go:521`). Making those fall through would change working behaviour. For numbers, a null only ever produced a silent 0 (`rounds:` switching review off, `gateTimeout:` meaning an instant kill) or an error (`factor`).
- **Where the positivity check lives:** in `resolver.duration`, for every written duration (taken), or as a named list of keys checked in `sections`. Checking in `duration` won:
  - Every duration key the schema has is a timer the run arms: the six watchdog/land keys, the row `timeout` and `reviewTimeout`. A zero or negative value is wrong for all of them.
  - One check covers `watchdog.triageTimeout` (the watchdog's first warning) and any duration key added later.
  - The error has the node, so it names the file and line.
- **How an unset row timeout is caught:** a post-resolution check in `resolver.row` that points at the row's node, like the `check` error at `reader.go:485-488` (taken), or leaving 0 as "no backstop" (`internal/core/session.go:394`). The check won. The item forbids a zero row timeout, the spec names no "no backstop" mode, and every row in `defaults.yaml` carries a timeout.
- **`reviewTimeout` on rows without a review half (the watchdog's second warning):** require it only when the review half runs (taken), or require it on every row. Requiring it only when the review half runs won. The `milestone` and `gate` rows (`defaults.yaml:36-49`) set neither `reviewTimeout` nor reviewers and legitimately resolve 0s. A blanket check would stop the default config from loading. An explicit `reviewTimeout: 0s` on those rows is still refused by the written-value check.
- **How the banner shows the gate timeout's provenance:** a new `land gateTimeout` line after `gatefix` (taken), or folding it into the `gatefix` line. The separate line won. The `gatefix` line's `← <provenance>` already describes provider, model and effort (`banner.go:31-33`), and mixing a second key into it would make `sources()` ambiguous.

## Changes

1. **`internal/config/reader.go`, modify.**
   - Add `func (r *resolver) numeric(path string) (string, *yaml.Node, *layer, error)`.
     - It walks `r.layers` in order, skipping a layer whose node for `path` is missing or `isNull` (`reader.go:153`).
     - For the first non-null node, it sets `r.prov[path] = source(l, path)`. It returns `errAt(l.file, n, "%s must be a single value", path)` when that node is not a `ScalarNode`, else `n.Value, n, l, nil`.
     - When no layer has a non-null node, it sets `r.prov[path] = defaultSource` and returns `"", nil, nil, nil`.
     - It mirrors `scalar` at `reader.go:232-246`. `scalar` itself stays unchanged for strings, `label`, `check` and `land.fix.*`.
     - Obligation: null resolves to the next layer, and provenance names that layer.
   - `duration` (`reader.go:253`), `count` (`reader.go:265`) and `factor` (`reader.go:277`) call `r.numeric(path)` instead of `r.scalar(path)`.
     - Obligation: null falls through for every timeout key, including row `timeout` and `reviewTimeout` and `watchdog.triageTimeout`.
     - `rounds:`, `fixRounds:` and `maxRestarts:` stop silently becoming 0.
     - The `factor` error branch at `reader.go:284-286` for `n == nil` stays as it is.
   - In `duration`, replace the early return `if err != nil || v == "" { return 0, err }` (`reader.go:255-257`) with `if err != nil || n == nil { return 0, err }`. Only a key that no layer sets resolves to 0. A written empty scalar (`gateTimeout: ""`) now reaches `time.ParseDuration`, which fails, so loading refuses it with the existing `errAt(l.file, n, "%s: %q is not a duration", path, v)` (`reader.go:260`), naming the file, line and key. Obligation: the land gate never runs with a zero timeout, and an empty value is not treated as unset.
   - In `duration`, after the `time.ParseDuration` success, add: `if d <= 0 { return 0, errAt(l.file, n, "%s: %q is not a positive duration", path, v) }`.
     - Obligation: refuse zero or negative `land.gateTimeout`, `watchdog.checkTimeout`, `stallGrace`, `unblockTimeout`, `remedyWindow` and `triageTimeout`, and a row `timeout` or `reviewTimeout` that is written explicitly. The error names the file, line and key.
   - In `row` (`reader.go:468`), right after the reviewers loop ends (after `reader.go:519`, before the `fallback` lookup at `reader.go:520`), add two guards. Both use `n, l := r.lookup("steps." + name)` for the row node, the same way as `reader.go:486`:
     - `if row.Timeout == 0` → `errAt(l.file, n, "%stimeout: not set, want a positive duration", p)`
     - `if row.ReviewTimeout == 0 && row.Rounds > 0 && len(row.Reviewers) > 0` → `errAt(l.file, n, "%sreviewTimeout: not set, want a positive duration for the review half", p)`
     - Obligation: no row runs with a zero timer, and gate and milestone rows without a review half still load.
     - Both values are already ≥ 0 at this point, because `duration` refuses ≤ 0 and returns 0 only when the key is unset everywhere.
2. **`internal/config/banner.go`, modify.**
   - In `Banner`, right after the `gatefix` line (`banner.go:32-33`), add `fmt.Fprintf(&b, "land gateTimeout %s  ← %s\n", Duration(cfg.Land.GateTimeout), p["land.gateTimeout"])`.
   - It reuses `Duration` (`banner.go:83`).
   - Obligation: the banner's provenance names the layer a null fell through to.
3. **`internal/config/reader_test.go`, modify.**
   - `TestBannerForTwoOverrideConfig` (`reader_test.go:397`): insert `"land gateTimeout 30m  ← default",` after the `gatefix ...` entry of `want`.
   - Add the config tests below.
4. **`internal/app/app_test.go`, modify.** Add the two app tests below, next to `TestWireBuildsTheGateFixKindAndTheMilestoneBoundary` (`app_test.go:407`). They use `newFixture`, `f.write`, `f.commit`, `f.preflight`, `exitCode` (`app_test.go:203`) and `f.out`.
5. **`docs/task-loop-driver/tech-design.md`, modify.** Keep the shared contract in line with the code (codex r2-1). There are two edits:
   - **Config resolution (`tech-design.md:166-200`).** Replace the closing sentence `A flow-style YAML node is rejected naming the line; an unknown key is rejected naming the key and file; a negative `rounds` is rejected; each is exit `2`.` with this text:
     > A flow-style YAML node is rejected naming the line; an unknown key is rejected naming the key and file; a negative `rounds` is rejected. A written duration that is empty, zero or negative is rejected naming the file, line and key. A row whose `timeout` no layer sets is rejected, and so is a row with a review half (`rounds > 0` and at least one reviewer) whose `reviewTimeout` no layer sets. Each is exit `2`. A numeric key (a duration, `rounds`, `fixRounds`, `maxRestarts` or a factor) set to null (`key:` or `~`) resolves to the next layer that sets it, with that layer's provenance. A null string, list or `fallback` keeps its meaning: provider default, empty list, no fallback.
   - **Banner (`tech-design.md:415-424`).** After the clause `` `gatefix <provider> <model|provider default> <effort|provider default>`; `` insert `` `land gateTimeout <duration> ← <provenance>`; ``.
   - Obligation: the null fall-through, the positivity rule and the banner line are part of the documented contract, so implementers of later phases read the same rules the code enforces.

No change to `internal/core` or `internal/app` production code. `wire.go:465-479` copies `cfg.Land.GateTimeout` into `GateProbe.Timeout` and `LandGate.GateTimeout`, and that value is now always positive.

## Tests

Write these first. The config tests use `newDirs`, `writeProject`, `writeHome`, `load` and `loadErr` from `reader_test.go:13-61`.

- `TestZeroOrNegativeTimeoutRejectedWithFileLineAndKey` (`internal/config/reader_test.go`). This is a table test, one `t.Run` per row. Each row writes one file and calls `loadErr` with `<label>:<line>: <key>: "<value>" is not a positive duration`.
  - project `land:\n  gateTimeout: 0s\n` → `.r-loop/config.yaml:2: land.gateTimeout: "0s"`
  - home `land:\n  gateTimeout: -5m\n` → `~/.config/r-loop/config.yaml:2: land.gateTimeout: "-5m"`
  - project `watchdog:\n  checkTimeout: 0\n` → `:2: watchdog.checkTimeout: "0"`
  - project `watchdog:\n  stallGrace: -1s\n` → `:2: watchdog.stallGrace: "-1s"`
  - project `watchdog:\n  unblockTimeout: 0s\n` → `:2: watchdog.unblockTimeout: "0s"`
  - project `watchdog:\n  remedyWindow: 0s\n` → `:2: watchdog.remedyWindow: "0s"`
  - project `watchdog:\n  triageTimeout: 0s\n` → `:2: watchdog.triageTimeout: "0s"`
  - project `steps:\n  plan:\n    timeout: 0s\n` → `:3: steps.plan.timeout: "0s"`
  - project `steps:\n  plan:\n    reviewTimeout: -20m\n` → `:3: steps.plan.reviewTimeout: "-20m"`
  - project `steps:\n  milestone:\n    reviewTimeout: 0s\n` → `:3: steps.milestone.reviewTimeout: "0s"`. An explicit zero is refused even on a row with no review half.
  - Covers: open item 1, the triageTimeout warning, and the explicit half of the reviewTimeout warning.
- `TestEmptyQuotedTimeoutRejectedWithFileLineAndKey`. This is a table test, one `t.Run` per row, and each row calls `loadErr`:
  - project `land:\n  gateTimeout: ""\n` → `.r-loop/config.yaml:2: land.gateTimeout: "" is not a duration`
  - project `watchdog:\n  stallGrace: ""\n` → `.r-loop/config.yaml:2: watchdog.stallGrace: "" is not a duration`
  - project `steps:\n  plan:\n    timeout: ""\n` → `.r-loop/config.yaml:3: steps.plan.timeout: "" is not a duration`
  - Covers: open items 1 and 3. A written empty duration never resolves to 0 (codex r1-1).
- `TestNullTimeoutFallsThroughToTheNextLayerAndTheBannerNamesIt`.
  - Home is `land:\n  gateTimeout: 12m\n` and project is `land:\n  gateTimeout:\n`.
  - `cfg.Land.GateTimeout == 12*time.Minute`.
  - `cfg.Provenance["land.gateTimeout"] == "~/.config/r-loop/config.yaml:land.gateTimeout"`.
  - `Banner(cfg)` contains `"land gateTimeout 12m  ← ~/.config/r-loop/config.yaml:land.gateTimeout\n"`.
  - Covers: open items 2 and 3.
- `TestTildeTimeoutsFallThroughToTheDefaults`.
  - Project is `land:\n  gateTimeout: ~\nwatchdog:\n  triageTimeout: ~\n  stallGrace: ~\nsteps:\n  plan:\n    timeout: ~\n    reviewTimeout: ~\n`.
  - Resolves `Land.GateTimeout` 30m, `Watchdog.TriageTimeout` 2h, `Watchdog.StallGrace` 2m, `Steps["plan"].Timeout` 1h and `Steps["plan"].ReviewTimeout` 20m.
  - `Provenance` for each of those five paths is `"default"`.
  - `Banner(cfg)` contains `"land gateTimeout 30m  ← default\n"`.
  - Covers: open items 2 and 3 for `~` and for row keys.
- `TestNullCountAndFactorFallThrough`.
  - Project is `land:\n  fixRounds:\nsteps:\n  plan:\n    rounds: ~\nwatchdog:\n  overtimeFactor: ~\n`.
  - `Land.FixRounds == 1`, `Steps["plan"].Rounds == 2` and `Watchdog.OvertimeFactor == 2`.
  - `Provenance["steps.plan.rounds"] == "default"`.
  - Covers: the numeric-only fall-through choice.
- `TestAddedRowWithoutTimeoutRejected`.
  - Project is `pipeline:\n  - plan\n  - implement\n  - docs\nsteps:\n  docs:\n    prompt: docs\n    check: diff\n    provider: claude\n`.
  - `loadErr` wants `.r-loop/config.yaml:7: steps.docs.timeout: not set, want a positive duration`. Line 7 is the row mapping's first key; checked with yaml.v3.
  - Covers: open item 1, a row timeout that resolves to zero.
- `TestAddedRowWithReviewHalfWithoutReviewTimeoutRejected`.
  - Project is `steps:\n  docs:\n    prompt: docs\n    check: diff\n    provider: claude\n    timeout: 30m\n    reviewers:\n      - codex\n    rounds: 1\n`.
  - `loadErr` wants `.r-loop/config.yaml:3: steps.docs.reviewTimeout: not set, want a positive duration for the review half`.
  - Covers: the review-half half of the reviewTimeout warning.
- `TestRowsWithoutAReviewHalfLoadWithoutReviewTimeout`.
  - Project is `steps:\n  docs:\n    prompt: docs\n    check: diff\n    provider: claude\n    timeout: 30m\n    reviewers:\n      - codex\n`, with rounds unset, so 0.
  - Loads with `Steps["docs"].ReviewTimeout == 0`, `Steps["milestone"].ReviewTimeout == 0` and `Steps["gate"].ReviewTimeout == 0`.
  - Covers: the watchdog's warning that the default gate and milestone rows must keep loading.
- `TestZeroGateTimeoutExitsTwoNamingTheFileLineAndKey` (`internal/app/app_test.go`).
  - `f.write(".r-loop/config.yaml", "land:\n  gateTimeout: 0s\n")`, `f.commit()`, then `f.preflight(f.todo, "--plain")`.
  - `exitCode(t, err) == 2`, and `err.Error()` contains `.r-loop/config.yaml:2: land.gateTimeout: "0s" is not a positive duration`.
  - Covers: open item 1, exit 2 end to end.
- `TestNullGateTimeoutWiresTheDefaultIntoTheGate` (`internal/app/app_test.go`).
  - `f.write(".r-loop/config.yaml", "land:\n  gateTimeout:\n")`, `f.commit()`, then `w, err := f.preflight(f.todo, "--plain")`.
  - No error; `w.Gate.GateTimeout == 30*time.Minute` and `w.Probe.Timeout == 30*time.Minute`.
  - `f.out.String()` contains `"land gateTimeout 30m  ← default\n"`.
  - Covers: open items 2 and 3, the land gate never running with a zero timeout.

Update the existing `TestBannerForTwoOverrideConfig` as described under Changes. `TestDefaults` (`reader_test.go:63`) keeps passing unchanged, which shows that the default config still loads.

## Left out

- **A `GateTimeout <= 0` guard in `core.LandGate`/`GateProbe`.** The only constructor is `app.Wire` (`wire.go:465-479`), and it copies a value that `config.Load` now guarantees is positive. A second check would guard a state the config invariant already rules out.
- **Null fall-through for string, list and `fallback` keys.** For those keys a null already has meaning (provider default, empty list, no fallback), so the phase needs no change there.
- **Banner lines for the watchdog durations and row timeout provenance.** The items name the gate timeout in the banner. The other keys' provenance is still recorded in `cfg.Provenance`, and the tests check it there.
- **A new error type or a list of positive-only keys.** The check in `duration` covers every duration key.

## Assumptions

- **Null falls through only for numeric keys** (`duration`, `count`, `factor`). A null string, list or `fallback` keeps its current meaning. The item's examples (`gateTimeout:`, `~`) are all timeouts, and this is the smallest change that stops the "null overrides with zero" defect without changing `model: ~`, `allow:` or `fallback: ~`.
- **Watchdog warning 1 (triageTimeout) is resolved.** `watchdog.triageTimeout` is in the must-be-positive set through the shared `duration` check, and the first test table has a row for it.
- **Watchdog warning 2 (reviewTimeout) is resolved.** A written `reviewTimeout` must always be positive. An unset one is refused only when the row has a review half, defined as `rounds > 0` **and** at least one reviewer. That is the condition under which `core` runs reviewers (`runners.go:23`) and the banner shows them (`banner.go:21`).
  - This is instead of the watchdog's "reviewers **or** rounds > 0". A row with reviewers but `rounds: 0`, or rounds without reviewers, never arms the review timer.
  - The gate-fix kind borrows the implement row's `ReviewTimeout` with `Rounds: 1` (`wire.go:461`). The implement row always resolves a positive `reviewTimeout`, either from `defaults.yaml:35` or a written positive value.
- **An unset row `timeout` is refused rather than kept as "no backstop".** Every default row sets one, and the item forbids a zero row timeout.
- **Error wording.** A written value gets `<key>: "<value>" is not a positive duration`, matching the existing `is not a duration` style at `reader.go:260`. An unset row key gets `<key>: not set, want a positive duration`.
- **The banner line reads `land gateTimeout <d>  ← <provenance>`** and sits right after the `gatefix` line.

- **The `tech-design.md` edit (Changes 5) has no test.** It is prose. The config tests above pin the behaviour it describes.

## Gate

`go test ./internal/config/ ./internal/app/ -run '^(TestZeroOrNegativeTimeoutRejectedWithFileLineAndKey|TestEmptyQuotedTimeoutRejectedWithFileLineAndKey|TestNullTimeoutFallsThroughToTheNextLayerAndTheBannerNamesIt|TestTildeTimeoutsFallThroughToTheDefaults|TestNullCountAndFactorFallThrough|TestAddedRowWithoutTimeoutRejected|TestAddedRowWithReviewHalfWithoutReviewTimeoutRejected|TestRowsWithoutAReviewHalfLoadWithoutReviewTimeout|TestZeroGateTimeoutExitsTwoNamingTheFileLineAndKey|TestNullGateTimeoutWiresTheDefaultIntoTheGate)$'`
