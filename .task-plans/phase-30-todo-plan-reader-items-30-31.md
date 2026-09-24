status: planned

## Summary

The phase tightens how the todo reader parses a plan, and adds one resume guard to the loop.

1. **Phase references.** `Depends on:` and `Resolve first` `Blocks:` share a single parser. It reads only phase lists: the word `Phase`/`Phases` in any case, followed by IDs separated by `,`, `&`, `·`, `/` or `and`. Each later ID may repeat the `Phase` word. Anything else ends the list, so `Phase 3 (see ADR-12)` gives `[3]`, `Phase 3, Phase 4b` gives `[3, 4b]` and `Blocks: phase 4` gives `[4]`.
2. **Dependency order.** `Read` rejects a self or forward dependency with the line of the `Depends on:` that names it. The same check is removed from `CheckPlan`, so the rule has one home.
3. **Code fences.** A fence mask marks every line inside a ```` ``` ```` or `~~~` fence. Every structural scan in `internal/plan` treats those lines as body text only: never a heading, a phase heading, a milestone heading, a field line or a checklist item. That covers the todo/backlog classification (`isBacklog`), the `## Resolve first` section scans, the phase scan, the phase block, and `Tick`. A fence that is never closed fails `Read`, before the file is classified.
4. **Resume guard.** Before a phase runs, the loop checks its dependencies. If one is blocked in this run, or is still pending in the run list, the phase is skipped with `phase-skipped {phase, because: <dep>}`. A dependency outside the run list counts as met, as on a fresh run.

Choices taken:

- **Where the self/forward rule lives.** Options: in `plan.Reader` and removed from `CheckPlan`; kept in `CheckPlan` as a Stop; or both, through a shared helper. Taken: in `Reader.Read`, and removed from `CheckPlan`. The criterion requires `Read` to fail with the line, and only the reader knows the line. Every todo plan reaches `CheckPlan` through `Read` (backlogs return early at `internal/core/plancheck.go:25`), so a copy in `CheckPlan` would be dead code that could drift. This answers the watchdog's warning.
- **The phase-list grammar.** Options: every number after the first `Phase` (today's rule); every `Phase <id>` taken on its own; or a `Phase` keyword followed by a separated list of IDs. Taken: the separated list. Taking each `Phase <id>` alone would silently drop the `3` in `Blocks: Phase 1, 3`, which today blocks phase 3. Losing that block is fail-open. The separated list keeps comma lists and stops at prose.
- **When `Blocks:` blocks everything.** Options: only when it names no phase; or when it names no phase or its value starts with `all`. Taken: both triggers, because the criterion names both. "Says all" means the word `all` anywhere in the value, so `all, from Phase 2` and `Phase 2 and all later phases` both block everything. Matching only a leading `all` would leave the later phases of the second form unblocked, which is fail-open. Blocking more is the fail-closed side.
- **Which scans honour the mask.** Options: the phase scan, the phase block and `Tick` only; or every scan that finds a structural heading. Taken: every such scan. A fenced `### Phase 1 — example` in a backlog would otherwise make `isBacklog` (`internal/plan/backlog.go:31-38`) classify the backlog as a todo. A fenced `## Resolve first` example in a todo would otherwise open a false blocking section (`internal/plan/resolve.go:83-97`). Both break #30 criterion 3's rule that a fenced heading is not structure. Backlog item parsing inside `readBacklog` stays unchanged, because it already tracks fences itself (`internal/plan/backlog.go:83`).
- **Fenced `- [ ]` lines.** Options: count them as items in both `Read` and `Tick`, or in neither. Taken: neither. A fenced line is code, consistent with fenced headings not counting. `Read` and `Tick` must agree, or a phase whose fenced item `Tick` never ticks stays unticked forever.
- **An unclosed fence.** Options: the fence runs to end of file (CommonMark), or `Read` fails. Taken: `Read` fails, naming the opening line. Running to end of file would silently swallow every later phase, and the run would report finished with those phases never built. `followsPrevious` cannot catch it, because the phases that go missing are trailing ones. An unclosed fence before the first phase heading would also turn a todo into a "backlog". The reader "fails closed and never guesses" (`internal/plan/testdata/todo.md:64`).
- **What "not landed" means on resume.** Taken: option B, as answered by the watchdog (q4, citing `docs/task-loop-driver/tech-design.md:362`). Only dependencies inside the resumed run list are checked. A dependency the maintainer left out with `--from`/`--phases` is their choice.
- **How the loop knows a listed dependency has not landed.** Options: load the store and look for a landing; or use `l.pending`. Taken: `l.pending[d]`. It holds exactly the listed phases that neither landed in the prior run (`internal/core/loop.go:173-177`) nor have run yet in this one (`loop.go:194`). It needs no I/O and no new state.

## Changes

### 1. `internal/plan/reader.go` (modify)

**Phase-list parsing.** Serves #29 criteria 1 and 3.

- Replace `phaseRefRe` (line 24) with two regexps:

  ```go
  phaseListRe = regexp.MustCompile(`(?i)\bphases?[ \t]+\d+[a-z]?\b(?:[ \t]*(?:,|&|·|/|\band\b)[ \t]*(?:phases?[ \t]+)?\d+[a-z]?\b)*`)
  phaseIDRe   = regexp.MustCompile(`\d+[a-zA-Z]?\b`)
  ```

- Rewrite `phaseRefs(s string) []string` (lines 217-223). For each `phaseListRe` match in `s`, append `label(id)` for each `phaseIDRe` match inside it. Keep `label` (line 209) as it is.
- `parseDepends` (lines 194-207):
  - Keep the `""`, `—`, `-` and `none` check.
  - Drop the `strings.Index(rest, "Phase")` step and call `out := phaseRefs(rest)`.
  - When `out` is empty, return the existing error: `depends on names no phase: %q`.

**Dependency order.** Serves #29 criterion 2.

- Add `from string` to `dependsRef` (lines 29-32). Fill it with `id` in `parsePhase` at line 166: `dependsRef{line: lineNo, phase: d, from: id}`.
- Replace the loop at lines 98-102 with a `switch` over each `d`, in this order:
  1. `!seen[d.phase]`: the existing "does not exist" error, unchanged.
  2. `d.phase == d.from`: `fmt.Errorf("%s line %d: phase %s depends on itself", path, d.line, d.from)`.
  3. `core.ComparePhaseIDs(d.phase, d.from) > 0`: `fmt.Errorf("%s line %d: phase %s depends on phase %s, which comes after it; a phase may depend only on earlier phases", path, d.line, d.from, d.phase)`.

  The wording follows the note being removed from `internal/core/plancheck.go:59`.

**Fence mask.** Serves #30 criteria 1, 3 and 4, the unclosed-fence edge case, and codex-r1-1.

- Add to the `var` block: `fenceRe = regexp.MustCompile("^[ \\t]*(`{3,}|~{3,})(.*)$")`.
- Add `func fenced(lines []string) ([]bool, int)`:
  - It returns a mask with `true` for every line from a fence opener through its closer, inclusive.
  - It returns the index of an opener that is never closed, or `-1`.
  - It trims `"\r\n"` from each line before matching.
  - **Outside a fence**, a `fenceRe` match opens one and records the marker (`m[1]`).
  - **Inside a fence**, a line closes it when three things hold: it matches `fenceRe`, `m[1][0] == marker[0]` with `len(m[1]) >= len(marker)`, and `strings.TrimSpace(m[2]) == ""`.
  - Any indentation is accepted, because fences sit under indented list items.
- In `Read`, right after `lines` is split (line 43) and **before** `isBacklog`:
  - Call `mask, open := fenced(lines)`.
  - If `open >= 0`, return `fmt.Errorf("%s line %d: code fence is never closed", path, open+1)`.
  - Then call `isBacklog(lines, mask)` (line 44) and `resolveFirst(lines, mask)` (line 48).
- In the main loop (line 57), `continue` at the top when `mask[i]`. Fenced milestone and phase headings are then never read.
- Make the block-end loop (line 81): `for end < len(lines) && (mask[end] || !headingRe.MatchString(lines[end]))`.
- Change `parsePhase` to `parsePhase(path, id, title string, block []string, fence []bool, headingLine int)` and call it with `mask[i:end]`.
  - At the top of its loop (line 148), `continue` when `fence[j]`. Fenced lines are then never fields or items.
  - Make the `Done when` continuation condition (line 176): `j+1 < len(block) && (fence[j+1] || (!strings.HasPrefix(block[j+1], "**") && !anyHeadingRe.MatchString(block[j+1])))`. A fenced line then passes both terminators, the bold-line test and the heading test (codex-r2-1).

`Block` stays `strings.Join(block, "")`, so the fence and everything after it stay in the body.

### 2. `internal/plan/backlog.go` (modify)

Serves #30 criterion 3 (codex-r1-1).

- Change `isBacklog(lines []string) bool` (line 31) to `isBacklog(lines []string, mask []bool) bool`. In its loop, skip index `i` when `mask[i]`, so a fenced `### Phase` line never makes a backlog a todo.

### 3. `internal/plan/resolve.go` (modify)

Serves #29 criterion 3 and #30 criterion 3 (codex-r1-1, codex-r1-2).

- Change `resolveFirst(lines []string)` (line 83) to `resolveFirst(lines []string, mask []bool)`:
  - The heading search (line 86) skips `i` when `mask[i]`.
  - The section-end loop (line 95) becomes `for stop < len(lines) && (mask[stop] || !anyHeadingRe.MatchString(lines[stop]))`.
  - The entry-start test (line 101) becomes `if mask[i] || !entryStartRe.MatchString(lines[i]) { continue }`. The `next` loop (line 105) becomes `for next < stop && (mask[next] || !entryStartRe.MatchString(lines[next]))`. A fenced bullet then neither starts nor splits an entry; it stays in the body of the entry that holds it (codex-r2-2).
- `outsideResolveFirst` (line 63) computes `mask, _ := fenced(lines)` after splitting, and applies the same two rules to its heading search (line 66) and end loop (line 70). `OnlyResolveFirstChanged` then cuts the same section `Read` parses.
- Add `blocksAllRe = regexp.MustCompile(`(?i)\ball\b`)` to the `var` block at lines 11-18.
- Replace lines 166-169 with:

  ```go
  e.BlocksPhases = phaseRefs(e.Blocks)
  e.BlocksAll = len(e.BlocksPhases) == 0 || blocksAllRe.MatchString(e.Blocks)
  ```

  `e.Blocks` is already trimmed at line 153.

### 4. `internal/plan/write.go` (modify)

`Tick` uses the reader's fence mask. Serves #30 criteria 2 and 3.

- Before the backlog check, call `mask, _ := fenced(lines)`, and make line 20 `isBacklog(lines, mask)`. `Tick` runs on a plan `Read` has already accepted. If the fence is unclosed anyway, the mask runs to end of file, which is safe.
- Start search (line 25): skip `i` when `mask[i]`.
- Tick loop (line 35):
  - Condition: `i < len(lines) && (mask[i] || !headingRe.MatchString(lines[i]))`.
  - Tick a line only when `!mask[i] && strings.HasPrefix(lines[i], "- [ ]")`.

### 5. `internal/core/plancheck.go` (modify)

Serves the watchdog's warning: one rule, not two.

- Delete the `known` map (lines 28-31) and the `for _, d := range ph.DependsOn` switch (lines 53-61).
- Leave the cycle note (lines 63-66) and `Waves` unchanged. They use the same edges, and with self and forward edges rejected by `Read`, a plan from `Read` can have no cycle.

### 6. `internal/core/loop.go` (modify)

Serves #29 criterion 4.

- In `Run`, insert this after `if !l.pending[ph.ID] { continue }` (lines 188-190) and before `dogGone`:

  ```go
  if opts.Resume {
      if d := l.unmetDependency(ph); d != "" {
          delete(l.pending, ph.ID)
          l.blocked = append(l.blocked, ph.ID)
          l.emit(Event{Kind: "phase-skipped", Phase: ph.ID, Fields: map[string]string{"phase": ph.ID, "because": d}})
          if first == 0 {
              first, firstPhase, firstReason = 1, ph.ID, fmt.Sprintf("depends on phase %s, which has not landed", d)
          }
          continue
      }
  }
  ```

  It follows the cascade in `block` (`loop.go:1418-1425`). The exit code is set because a skip with no blocked root would otherwise end a halted run with exit 0 (`loop.go:248`).
- Add, next to `dependents` (line 1435):

  ```go
  func (l *RunLoop) unmetDependency(ph Phase) string {
      for _, d := range ph.DependsOn {
          if slices.Contains(l.blocked, d) || l.pending[d] {
              return d
          }
      }
      return ""
  }
  ```

  A dependency outside the run list is never pending or blocked, so it counts as met.

## Tests

Write these first. Plan tests go in `internal/plan`, using `writePlan` (`internal/plan/reader_test.go:25`) and `readEntries`/`onlyEntry` (`internal/plan/resolve_test.go:25-41`). Loop tests go in `internal/core/loop_test.go`, using `newLoopRig`, `threePhasePlan` (line 142), `r.events` and `r.calls`.

### `internal/plan/reader_test.go`

**`TestDependsOnReadsOnlyNamedPhases`** covers #29 criterion 1. It reads a plan of phases 1, 2, 3, 4, 4a, 4b and 5 with these `Depends on:` lines, and asserts each `DependsOn`:

| Phase | `Depends on:` | Want |
|---|---|---|
| 1 | `—` | none |
| 2 | `Phase 1 (see ADR-12)` | `[1]` |
| 3 | `phase 1 and Phase 2` | `[1 2]` |
| 4 | `Phase 3 (see ADR-12)` | `[3]` |
| 4a | `Phases 1, 2` | `[1 2]` |
| 4b | `Phase 3` | `[3]` |
| 5 | `Phase 3, Phase 4b` | `[3 4b]` |

**`TestFailClosed`** gains three cases. Each asserts that the error contains the listed substrings.

| Case | Content | Want | Covers |
|---|---|---|---|
| `depends on itself` | `### Phase 1 — A\n### Phase 2 — B\n**Depends on:** Phase 2\n` | `line 3`, `phase 2 depends on itself` | #29 criterion 2 |
| `depends on a later phase` | `### Phase 1 — A\n**Depends on:** Phase 2\n### Phase 2 — B\n` | `line 2`, `phase 1 depends on phase 2, which comes after it` | #29 criterion 2 |
| `unclosed code fence` | ``### Phase 1 — A\n```\n### Phase 2 — B\n`` | `line 2`, `code fence is never closed` | unclosed-fence edge case |

**`TestFencedCommentKeepsThePhaseBlock`** covers #30 criterion 1. Phase 1 holds, in order:

- `- [ ] before`
- an indented ```` ```sh ```` fence holding `# comment` and `## also a comment`
- `- [ ] after`
- ``**Done when:** `go test ./a/...` is green.``

Phase 2 follows. It asserts:

- two phases;
- phase 1 `Items` are `before` and `after`;
- `DoneWhen` is ``"`go test ./a/...` is green."``;
- `Block` contains `- [ ] after` and the `Done when` line.

**`TestDoneWhenRunsThroughAFencedBoldLine`** covers #30 criterion 1 for the `Done when` continuation (codex-r2-1). Phase 1 ends with these lines, and phase 2 follows:

- ``**Done when:** `go test ./a/...` is green,``
- ```` ``` ````
- `**example**`
- ```` ``` ````
- `and the log is clean.`

It asserts that phase 1 `DoneWhen` equals those five lines joined with `\n`, from the text after `**Done when:**` through `and the log is clean.`.

**`TestFencedPhaseHeadingIsNotAPhase`** covers #30 criterion 3, and the fenced-item rule. Phase 1 holds a `~~~` fence containing:

- `### Phase 2 — Fake`
- `## Milestone 9 — Fake`
- `- [ ] sample`

The real `### Phase 2 — Real` comes after the fence. It asserts:

- two phases, with phase 2 titled `Real`;
- no milestone;
- phase 1 `Items` hold only its unfenced item.

**`TestUnfencedHeadingStillEndsTheBlock`** covers #30 criterion 4. Phase 1 has a closed fence, then `## Notes` with `- [ ] not an item` under it, then `### Phase 2 — B`. It asserts:

- phase 1 `Items` exclude `not an item`;
- phase 1 `Block` ends before `## Notes`.

**`TestFencedPhaseHeadingDoesNotMakeABacklogATodo`** covers #30 criterion 3 for classification (codex-r1-1). It writes this backlog through `writePlan`:

```
- [ ] [#1] Rename the app
      ```md
### Phase 1 — example
      ```
- [ ] [#2] Export books
```

It asserts:

- `Read` succeeds with `Backlog == true`;
- `len(p.Phases) == 2`.

On base code the file is read as a todo.

### `internal/plan/write_test.go`

**`TestTickTicksItemsAfterAFence`** covers #30 criterion 2. It uses the fixture of `TestFencedCommentKeepsThePhaseBlock` and calls `Tick(path, core.Phase{ID: "1"})`. It asserts:

- the file has `- [x] before` and `- [x] after`;
- the fence lines are byte-identical;
- phase 2's `- [ ] b` is untouched.

**`TestTickIgnoresAFencedPhaseHeading`** covers #30 criteria 2 and 3. It uses the fixture of `TestFencedPhaseHeadingIsNotAPhase`. It asserts:

- `Tick` of phase 2 ticks only the real phase 2 item, leaving phase 1's items and the fenced `- [ ] sample` open;
- `Tick` of phase 1 then ticks phase 1's own item and still leaves `- [ ] sample` open.

### `internal/plan/resolve_test.go`

**`TestFencedResolveFirstHeadingOpensNoSection`** covers #30 criterion 3 for the section scans (codex-r1-1). A plan has no real `## Resolve first` section. Phase 1 holds a fence containing:

- `## Resolve first`
- `- [ ] **Example** — which? Owner: x. Blocks: Phase 1.`

It asserts:

- `p.ResolveFirst == nil`;
- `OnlyResolveFirstChanged(before, after)` is `false` when only a line inside that fenced block differs.

**`TestFencedBulletInAResolveFirstEntryIsNotAnEntry`** covers the entry scans (codex-r2-2). A real `## Resolve first` section holds one entry. It is `- [ ] **Real** — which?`, followed by `Owner: platform. Blocks: Phase 1. Timebox: an hour.` Under it sits a fence at column 0: a ```` ``` ```` line, then `- [ ] **Example** — x?`, then a ```` ``` ```` line. The fenced bullet is at column 0 because `entryStartRe` (`internal/plan/resolve.go:13`) matches only at column 0 or 1, and on base code that line starts a second, outstanding entry. `phasesTail` follows. It asserts:

- through `onlyEntry`, that there is exactly one entry, named `Real`;
- its `BlocksPhases == [1]`;
- its `Body` contains the fenced example line.

Each test below uses `phasesTail`.

| Test | `Blocks:` value | Asserts | Covers |
|---|---|---|---|
| `TestBlocksLowercasePhaseNamesThatPhase` | `phase 2` | `BlocksPhases == [2]`, `!BlocksAll`, `p.Blocking([]string{"1"})` is empty | #29 criterion 3 |
| `TestBlocksStopsAtProseAfterThePhase` | `Phase 1 (see ADR-3)` | `BlocksPhases == [1]`, `!BlocksAll` | #29 criteria 1 and 3 |
| `TestBlocksSayingAllBlocksAll` | `all, from Phase 2`, and in a second entry `Phase 2 and all later phases` | `BlocksAll` for both entries | #29 criterion 3, "says all" (codex-r1-2) |

The "names no phase" side of #29 criterion 3 stays covered by the existing `TestBlocksWithProseOnlyBlocksAll` and `TestMissingBlocksBlocksAll`.

### `internal/core/plancheck_test.go`

**`TestCheckPlan`** changes two cases:

- `self dependency`: `notes` becomes only `dependency cycle through phase(s): 1`.
- `forward dependency and cycle`: `notes` becomes only `dependency cycle through phase(s): 1`.

This pins the removal of the duplicate rule, and pins the cycle note as still consistent.

### `internal/core/loop_test.go`

**`TestResumeRunsADependentWhoseDependencyIsOutsideTheRunList`** covers #29 criterion 4 and the watchdog's answer. It runs `r.run(RunOptions{Phases: []string{"3"}, Resume: true})`. Phase 3 depends on phase 1, which is unticked and not listed. It asserts:

- exit 0;
- `r.calls("Land ") == ["3"]`;
- no `phase-skipped` event.

**`TestResumeSkipsADependentWhoseListedDependencyHasNotLanded`** covers #29 criterion 4, for a listed dependency still pending. It sets `r.loop.Plan.Phases[0].DependsOn = []string{"2"}`: phase 1 depends on phase 2, an edge `Read` now rejects but the loop still guards. It runs `RunOptions{Resume: true}` and asserts:

- exit 1;
- `phase-skipped` events are `{phase 1, because 2}`, then `{phase 3, because 1}`;
- `r.calls("Land ") == ["2"]`;
- no `SessionHost.Open` call contains `rloop-p1` or `rloop-p3`.

**`TestResumeSkipsADependentOfAPhaseBlockedAgain`** covers #29 criterion 4, for a blocked dependency. It sets up:

- the prior records of `TestResumeSkipsLandedPhasesAndOkStepsAndRerunsTheStoppedStep` (line 1241): phase 1 plan `ok`, phase 1 implement `failed`. There is no landing for phase 2.
- `r.host.behaviour["rloop-p1-implement-a2"] = "fail"`.

It runs `RunOptions{Resume: true}` and asserts:

- exit 1;
- a `phase-blocked` event for phase 1;
- exactly one `phase-skipped`, `{phase 3, because 1}`;
- `r.calls("Land ") == ["2"]`.

## Left out

- **Changes to backlog item parsing** (`parseBacklog`). It already tracks ```` ``` ```` fences itself (`internal/plan/backlog.go:83`). Its tilde and longer-fence gaps predate this phase, and no criterion covers them: both backlog items are about the todo plan's phase block (codex-r2-3, out of scope).
- **Stopping `Done when` continuation at a fence opener.** No criterion needs it. The criterion is about a `Done when` line after a fence, which the mask handles.
- **A resume check on fresh runs.** The watchdog's answer keeps fresh runs as they are, and `TestFromNarrowsTheRunList` (`loop_test.go:1330`) pins that fresh behaviour.
- **An `OnWarn` hook for a dependency skip.** Cascaded skips in `block` fire none (`loop.go:1418-1425`), and the run's `OnHalt` still fires.
- **Updating `spec.html:3310`** (the plan check listing forward/self dependencies as notes) and the fixture text at `internal/plan/testdata/todo.md:62`/`:74` ("every integer after `Phase`"). The phase changes code, not the design docs. The fixture text is test input, and the reader's field assertions do not depend on it.
- **A shared core helper for the self/forward rule.** With the `CheckPlan` copy removed, there is one call site.

## Assumptions

- The backlog items override the spec's plan-check line (`spec.html:3310`): a self or forward dependency now fails `Read` (exit 2 through the plan read) instead of being shown as a note. The watchdog's warning asked for exactly this single rule.
- A listed dependency that finished `ok` without landing counts as met, because it is no longer pending. The only such path is an item skip, which needs an empty `Done when` and item gates (`loop.go:452`). That is backlog-only, and backlog phases carry no `DependsOn`. This matches triage `already-done` counting as met.
- A dependency skip on resume that has no blocked root exits 1 (failed), the code of a plain block (`loop.go:1432`).
- The `Phase` keyword matches case-insensitively in both `Depends on:` and `Blocks:`. `phase 1 and Phase 2` is now accepted where it used to be an error, and `Phases 3, 4` names both phases.

## Gate

`go test ./internal/plan/ -run '^(TestDependsOnReadsOnlyNamedPhases|TestFailClosed|TestFencedCommentKeepsThePhaseBlock|TestFencedPhaseHeadingIsNotAPhase|TestUnfencedHeadingStillEndsTheBlock|TestTickTicksItemsAfterAFence|TestTickIgnoresAFencedPhaseHeading|TestBlocksLowercasePhaseNamesThatPhase|TestBlocksStopsAtProseAfterThePhase|TestBlocksSayingAllBlocksAll|TestFencedPhaseHeadingDoesNotMakeABacklogATodo|TestFencedResolveFirstHeadingOpensNoSection|TestDoneWhenRunsThroughAFencedBoldLine|TestFencedBulletInAResolveFirstEntryIsNotAnEntry)$' && go test ./internal/core/ -run '^(TestCheckPlan|TestResumeRunsADependentWhoseDependencyIsOutsideTheRunList|TestResumeSkipsADependentWhoseListedDependencyHasNotLanded|TestResumeSkipsADependentOfAPhaseBlockedAgain)$'`
