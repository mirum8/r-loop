status: planned

## Summary

A native reviewer (a reviewer whose template is `review`) now has to leave proof that its provider's own review command ran: the command's raw output at `<ArtifactsDir>/native-review.txt`, a path the driver names. The maintainer chose this in q1, option A. The shipped codex block's `review` becomes the shell command `codex exec review --uncommitted -o {output}`, so codex's real reviewer writes that file itself. Claude keeps `/code-review` and saves its report to the same file. The driver fills `{output}` with that path when it builds the reviewer's prompt. It creates the artifacts directory and deletes any stale output before the reviewer starts. It then fails the reviewer through the existing `findings` evidence check when the file is missing or blank. So a command that could not run is a failed reviewer and a failed step, never a clean round. That includes a nested codex that has no network inside the reviewer's sandbox.

`review.md` tells the reviewer to run the command as its first action. It must never fall back to a review by hand. When the command cannot run, it writes a failed sentinel naming the command and the error. Plan rounds run the command too (`--uncommitted` covers the new, untracked plan file), plus the existing proportion rubric. Every `review-find` event carries a `command` field. The plain face prints it on a dedicated line, and `report.md` gets a `## Reviews` section with one line per reviewer per round.

Choices:
- **Where `{output}` is expanded:** in `core.ReviewHalf.reviewer`, not in `providers.Args`. Only core knows the per-round `ArtifactsDir`. `Args` builds start flags, and the review command is prompt text.
- **Where the output is checked:** the existing `findings` evidence check, with two new `EvidenceContext` fields, rather than a new check in `ReviewHalf.join`. The check already runs on every reviewer sentinel, judges evidence rather than the agent's word, and produces the `failed` reviewer outcome that `join` turns into a failed step. `join` needs no new branch.
- **Where the output lives:** `<ArtifactsDir>/native-review.txt`, where `ArtifactsDir` is the per-reviewer, per-round, per-attempt directory that already exists as a variable, rather than a new template variable such as `NativeOutput`. This adds no key to `StepVars`, and no fixture that pins the full variable set has to change. The findings check's FS is rooted at the step directory, and `ArtifactsDir` is a subdirectory of it, so the relative path is `<base>/native-review.txt`.
- **How the nested codex gets the reviewer's model, effort and fixed flags:** a `{args}` placeholder in `review`, which `providers.ToCore` fills with `Args(p, model, effort, "", "")`, rather than core appending `ProviderArgs.Args`. `Args` already builds exactly those flags and leaves out the ask flag when its value is empty. Appending in core would also append to claude's slash command, and would pass the reviewer's MCP flag to a headless process.
- **Keeping one attempt's output from passing for another's:** `ArtifactsDir` gains the attempt suffix (`<kind>-rv-<name>-r<round>-a<attempt>`, the same shape as the reviewer's sentinel name), rather than deleting the file before each start. A reviewer left over from an earlier attempt then writes only to its own directory, even after the new attempt has started.
- **Command a non-native reviewer reports:** `prompt <template>` (for example `prompt review-ui`), rather than an empty field. The item asks that every reviewer's line name what it really ran, and a `review-ui` reviewer runs its prompt, not `ReviewCommand`.

## Changes

1. **Modify `internal/providers/shipped/codex.yaml`.** Change `review: "/review"` to `review: "codex exec review --uncommitted {args} -o {output}"`. Obligation: codex runs its own review command, not `/review` read as prose, under the reviewer's configured model and effort.

1a. **Modify `internal/providers/registry.go`.** In `ToCore` (`registry.go:161-162`), set `Review: strings.ReplaceAll(p.Review, "{args}", strings.Join(Args(p, model, effort, "", ""), " "))`.
   - This reuses `Args` (`registry.go:126-135`): the fixed `flags`, then `modelFlag` and `effortFlag`, each left out when its value is empty. The ask flag is always left out, because its value is passed empty.
   - A `review` without `{args}` is unchanged.
   - Obligation: the nested native review runs with the reviewer row's model and effort and the provider's fixed flags. `internal/providers/shipped/claude.yaml` stays as it is (`review: "/code-review"`): see Assumptions.

2. **Modify `internal/core/evidence.go`.**
   - Add two fields to `EvidenceContext` (`evidence.go:17-25`): `NativeOutput string` (a path inside `FS`, empty when the reviewer is not native) and `ReviewCommand string`.
   - `findingsCheck` (`evidence.go:322-329`): after the loop over `ctx.FindingsFiles`, add this check when `ctx.NativeOutput != ""`: read the file with `fs.ReadFile(ctx.FS, ctx.NativeOutput)`. When that returns an error, or `strings.TrimSpace(string(data)) == ""`, return `false, "native review `" + ctx.ReviewCommand + "` produced no output"`.
   - Obligations: a command that cannot run is a reviewer failure, never a clean review; the claude `/code-review` path is checked the same way.

3. **Modify `internal/core/session.go`.** In `evidence` (`session.go:449-452`, the `s.Reviewer != ""` branch), when `s.Ref.Kind.Prompt == "review"`:
   - set `ctx.NativeOutput = filepath.ToSlash(filepath.Join(filepath.Base(str("ArtifactsDir")), "native-review.txt"))`;
   - set `ctx.ReviewCommand = str("ReviewCommand")`.

   The FS is already `os.DirFS(filepath.Dir(FindingsPath))`, the step directory, which contains `ArtifactsDir`. `review-ui` reviewers and step sessions get no `NativeOutput`. Obligation: the same check for both native reviewers.

4. **Modify `internal/core/review.go`.**
   - **`reviewer`** (`review.go:284-286`): compute `artifacts := filepath.Join(dir, fmt.Sprintf("%s-a%d", base, key.Attempt))` once, the same suffix as the sentinel at `review.go:272`. Set `vars["ArtifactsDir"] = artifacts` and `vars["ReviewCommand"] = strings.ReplaceAll(args.Review, "{output}", shellQuote(filepath.Join(artifacts, "native-review.txt")))`. A command without `{output}`, such as `/code-review`, is passed through unchanged.
     - Add `func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }` to `review.go`. The path sits under the repository root, which may contain spaces or quotes (for example `/Users/me/Work Projects/r-loop`). POSIX single quotes keep it one shell argument whatever it contains.
     - Obligation: codex runs its real command, pointed at the file the driver checks, from any checkout path.
   - **`open`** (`review.go:195-198`): in the loop that already removes the stale `FindingsPath`, before `Host.Start`, add `os.MkdirAll(s.Ref.Vars["ArtifactsDir"].(string), 0o755)`, because codex's `-o` needs the directory to exist. An error ends the step with `sm.fail(worker, "reviewer "+s.Reviewer+": "+err.Error())`, the same as the findings removal at `review.go:196-198`. Obligation: the native command can write its output.
   - **`join`** (`review.go:316`): add `"command": reviewCommand(s)` to the `review-find` fields. Add the unexported helper `func reviewCommand(s *Session) string`. It returns `s.Ref.Vars["ReviewCommand"].(string)` when `s.Ref.Kind.Prompt == "review"`, else `"prompt " + s.Ref.Kind.Prompt`. Obligation: the plain face and `report.md` name the command each reviewer really ran.

5. **Modify `internal/prompts/templates/review.md`.**
   - Replace line 3 with: ``You are a reviewer. Review, report-only, what the `{{.ReviewedKind}}` step produced:``. Keep lines 4-8, the plan and worktree targets, as they are.
   - Insert this section directly after `{{.PhaseBlock}}` and before the plan-only proportion block:

     ```
     ## Native review

     Your first action is to run this command, exactly as written:

         {{.ReviewCommand}}

     A command that starts with `/` is a slash command: invoke it as the slash command or skill of that name. Anything else is a shell command: run it in `{{.Worktree}}` with your shell tool. Its raw output must end up in `{{.ArtifactsDir}}/native-review.txt`. A command that writes that file itself leaves it as written; otherwise save the command's full report there verbatim. When the command cannot run — it is not found, is not available in this session, is refused a permission or network access, or exits with an error — never review by hand instead: write a failed sentinel whose reason names the command and the error. The driver fails a reviewer whose `native-review.txt` is missing or empty. Base your findings on that output.
     ```
   - Change line 14 from ``Besides `{{.ReviewCommand}}`, judge`` to ``Besides the native review, judge``.
   - Obligations: codex runs its command instead of reading it as prose; plan rounds run it too; a command that cannot run surfaces as a failure.

6. **Modify `internal/face/plain/plain.go`.** Add `case "review-find":` before `default` (`plain.go:43`). It prints `fmt.Sprintf("%s  phase %s  %s  reviewer %s r%s  %s  %s findings  ran `%s`\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, f["reviewer"], f["round"], f["state"], f["findings"], f["command"])`, where `f := ev.Fields`. Obligation: the plain face names the command.

7. **Modify `internal/core/report.go`.**
   - Add `writeSection(&b, "Reviews", reviewLines(state))` directly before the `Findings` section (`report.go:35`).
   - Add `reviewLines(st RunState) []string`, following `findingLines` (`report.go:269-283`). For each `review-find` event it emits `fmt.Sprintf("%s r%s %s: ran `%s` — %s, %s findings", where(ev.Phase, f["step"]), f["round"], f["reviewer"], f["command"], f["state"], f["findings"])`.
   - Obligation: `report.md` names the command.

8. **Modify `docs/task-loop-driver/tech-design.md`.**
   - Line 195: `review` (may be empty; `{args}` is replaced with `Args(p, model, effort, "", "")`, and `{output}` with the shell-quoted `<ArtifactsDir>/native-review.txt`).
   - Line 201: `review: codex exec review --uncommitted {args} -o {output}`.
   - Lines 590-591: `ArtifactsDir` = `<RunDir>/phase-<N>/<kind>-rv-<name>-r<round>-a<attempt>`.
   - Round step (3), lines 408-410: add "a native reviewer's `<ArtifactsDir>/native-review.txt` must exist and be non-empty, else `evidence missing: native review `<cmd>` produced no output`".
   - Line 581: `review-find` gains `command`.
   - This keeps the contract document true for the next phase.

8a. **Modify `docs/task-loop-driver/spec.html`** to record q1's amendment of ADR-55. q1 replaces codex's in-session `/review` with a headless `codex exec review` that the reviewer session runs.
   - Line 2616, ADR-55's status: append `` · amended 2026-09-23 — codex's native reviewer is <code>codex exec review --uncommitted -o {output}</code>, run by the reviewer session from its shell, because an interactive <code>/review</code> typed inside a prompt never runs (it opens a preset picker, and inside prose codex reviewed by hand). The reviewer stays a session the watchdog can read; the driver fails a native reviewer that leaves no <code>&lt;ArtifactsDir&gt;/native-review.txt</code>``.
   - Lines 3314-3315, the Codex CLI stack row: replace `its <code>/review</code> command is the native reviewer, run inside the session` with `its <code>codex exec review</code> is the native reviewer, run from the reviewer session's shell (ADR-55, amended)`.
   - Lines 3316-3317: replace `Codex's <code>/review</code>` with `Codex's <code>codex exec review</code>`.
   - Line 3422, the config example: `review: "codex exec review --uncommitted -o {output}"   # the native reviewer, run by the reviewer session (ADR-55)`.
   - Obligation: the spec stays the source of truth for a decision the maintainer took in q1.

9. **Update the fixtures the new evidence rule would otherwise break.**
   - `internal/core/review_test.go:95-113`: split `writeReview` in two.
     - `writeFindings(t, vars, outcome, findings)` holds today's body: the findings file, then the sentinel. It takes the reviewer name from `findingsName.FindStringSubmatch(filepath.Base(vars["FindingsPath"].(string)))[1]` instead of parsing `ReviewCommand`.
     - `writeReview(t, vars, outcome, findings)` first writes `"native review output\n"` to `filepath.Join(vars["ArtifactsDir"].(string), "native-review.txt")` when `vars["prompt"] == "review"`, using `os.MkdirAll` first. It then calls `writeFindings`, so the proof exists before the sentinel.
     - Every existing caller keeps `writeReview`. The failure tests below call `writeFindings`.
   - `reviewRig` gets `reviewCmd map[string]string`. When `reviewCmd[provider]` is set, `Resolve` (`review_test.go:51-55`) returns it as `Review`.
   - `internal/app/resume_test.go:100-103`: in the `-rv-` case, also `writeTo(m[1], "native review output")`, where `m` is `nativeRe.FindStringSubmatch(text)` and is non-nil.
     - Declare `nativeRe = regexp.MustCompile("`([^`]+/native-review\\.txt)`")` beside `findingsRe` (`resume_test.go:47`).
     - It captures the backticked path from the prompt's "must end up in" sentence, which both claude and codex prompts carry, never the shell-quoted `-o '…'` argument.
   - `internal/core/review_test.go:854`: the expected `ArtifactsDir` becomes `filepath.Join(dir, "implement-rv-ui-r1-a1")`. `internal/prompts/render_test.go:44` and `:613` keep their literal values, since they only render a given variable.

## Tests

Write these first. Every one fails on the base code.

- `internal/core/review_test.go`
  - **`TestNativeReviewCommandPointsAtTheOutputFileInItsArtifactsDir`**: rig with `reviewCmd["codex"] = "codex exec review --uncommitted -o {output}"`. The round-1 `ReviewCommand` equals `"codex exec review --uncommitted -o '" + <runDir>/phase-3/implement-rv-codex-r1-a1/native-review.txt + "'"`. That directory exists when the reviewer's prompt is rendered. A second reviewer, `claude` (`/claude-review`), gets its command unchanged. Covers: codex runs its real command; the directory exists for `-o`.
  - **`TestNativeReviewerWithoutOutputFailsTheStepNamingTheCommand`**, a table over `missing` (`writeFindings` only) and `blank` (output file holding `"  \n"`). Both give the outcome `StepFailed` with reason ``reviewer codex: evidence missing: native review `/codex-review` produced no output``. The `review-find` event has `state=failed`, and there is no `review-clean` event and no fix prompt. Covers: a command that cannot run is never a clean review.
  - **`TestClaudeReviewerWithoutOutputFailsTheSameWay`**: the same as `missing` for provider `claude`. The reason names `/claude-review`. Covers: the claude `/code-review` path is checked the same way.
  - **`TestReviewerThatCannotRunItsCommandFailsTheStep`**: the reviewer calls `writeFindings` with the outcome `failed`, then overwrites the sentinel with `{"outcome":"failed","reason":"/review is not available in this session"}`, and writes no output. The outcome is `StepFailed` with reason `reviewer codex: /review is not available in this session`, and there is no `review-clean` event. Covers: the reviewer's own report of a command that could not run becomes a failure.
  - **`TestShellQuoteKeepsAPathOneShellArgument`**, a table over `/runs/r1/x.txt`, `/Users/me/Work Projects/r-loop/x.txt` and `/tmp/it's here/x.txt`. For each, `exec.Command("sh", "-c", "printf %s "+shellQuote(p)).Output()` returns exactly `p`. Covers: a checkout path with spaces or quotes cannot split the output argument.
  - **`TestARetriedAttemptCannotPassOnAnEarlierAttemptsNativeOutput`**, set up like `TestRetriedAttemptSuffixesReviewerNamesAndDropsStaleFindings` (`review_test.go:499`) with `r.worker.Ref.Key.Attempt = 2`.
    - Before `run`, write `native-review.txt` into `implement-rv-codex-r1-a1/`: this is where an attempt-1 reviewer, even one that finishes late, writes.
    - The attempt-2 reviewer calls `writeFindings` only.
    - The step fails with ``reviewer codex: evidence missing: native review `/codex-review` produced no output``, and the reviewer's `ArtifactsDir` is `…/implement-rv-codex-r1-a2`.
    - Covers: a resumed or retried attempt cannot pass on another attempt's output.
  - **`TestReviewFindNamesTheCommandEachReviewerRan`**: a `claude` reviewer plus a `ui` reviewer (`Prompt: "review-ui"`, `Requires` present, as in `TestNamedUIReviewerRunsBesideTheSameProviderWithItsOwnPromptAndFiles`), both clean. The `review-find` `command` fields are `/claude-review` and `prompt review-ui`, and the round ends `review-clean` without the ui reviewer writing any native output. Covers: naming the command for every reviewer; a non-native reviewer needs no output file.
  - **`TestZeroFindingsEndsTheHalfClean`** (existing, `review_test.go:443`): its `want` map gains `"command": "/claude-review"`.
- `internal/core/evidence_test.go`
  - **`TestFindingsCheckRequiresTheNativeOutputWhenNamed`**, a table over `findingsCtx` plus `NativeOutput: "implement-rv-codex-r1/native-review.txt"` and `ReviewCommand: "codex exec review"`:
    - absent: `false`, ``native review `codex exec review` produced no output``;
    - `"\n\t "`: the same;
    - `"P1: nil map"`: `true`;
    - empty `NativeOutput` with no file: `true`.
  - Covers: the evidence rule and its error text.
- `internal/providers/registry_test.go`
  - **`TestShippedCodexBlock`** (existing, `registry_test.go:46`): `Review` becomes `"codex exec review --uncommitted {args} -o {output}"`. Covers: the codex block declares the real command.
  - **`TestToCoreFillsTheReviewArgsWithFlagsModelAndEffort`**: shipped `codex`.
    - `ToCore(codex, "gpt-x", "high", "http://x", "")` gives `.Review` = `"codex exec review --uncommitted -c check_for_update_on_startup=false -c model=gpt-x -c model_reasoning_effort=high -o {output}"`, with no `mcp_servers` flag.
    - `ToCore(codex, "", "", "http://x", "")` gives `"codex exec review --uncommitted -c check_for_update_on_startup=false -o {output}"`.
    - The shipped `claude` block's `.Review` stays `"/code-review"`.
    - Covers: the nested review runs under the reviewer's model, effort and fixed flags.
- `internal/app/resume_test.go`
  - **`TestResumeDuringAReviewContinuesAtTheRecordedRound`** (existing, `resume_test.go:391`): unchanged, apart from the `nativeRe` fixture. It must still pass with native output required. Covers: the reviewed resume path still works end to end.
- `internal/prompts/render_test.go`
  - **`TestReviewRunsTheNativeCommandFirstAndKeepsItsOutput`**: render `review` for `ReviewedKind` `implement` and for `plan`, with `ReviewCommand` `"codex exec review --uncommitted -o /runs/r1/phase-7/implement-rv-codex-r1/native-review.txt"`. Both texts contain:
    - `## Native review`;
    - that exact command on its own indented line;
    - ``/runs/r1/phase-7/implement-rv-ui-r1/native-review.txt`` (the `ArtifactsDir` from `fullVars`);
    - `never review by hand`;
    - `write a failed sentinel whose reason names the command`.

    Neither contains ``Run `codex exec review``. Covers: the command is an instruction to run, not prose; plan rounds run it too.
- `internal/face/plain/plain_test.go`
  - **`TestReviewFindLineNamesTheCommandTheReviewerRan`**: an event `review-find` at phase 4, step implement, with `reviewer=codex round=2 state=failed findings=0 command=codex exec review --uncommitted -o /x/native-review.txt`. The output is exactly ``14:03:09  phase 4  implement  reviewer codex r2  failed  0 findings  ran `codex exec review --uncommitted -o /x/native-review.txt` ``, with no trailing space and a trailing newline. Covers: the plain face names the command.
- `internal/core/report_test.go`
  - **`TestReportListsEachReviewerAndTheCommandItRan`**: two `review-find` events, phase 1 plan r1 codex (`codex exec review …`, ok, 2) and claude (`/code-review`, failed, 0). The report contains ``## Reviews\n\n- phase 1 plan r1 codex: ran `codex exec review --uncommitted -o /x` — ok, 2 findings\n- phase 1 plan r1 claude: ran `/code-review` — failed, 0 findings\n``. Covers: `report.md` names the command.

## Left out

- **A new `NativeOutput` template variable:** `ArtifactsDir` already names the directory, and a new key would change `StepVars` and two full-set fixtures for nothing.
- **A separate `native-output` evidence check or a new branch in `join`:** the `findings` check already runs on every reviewer sentinel and fails it through the existing path.
- **Registry validation that `review` contains `{output}`:** `/code-review` is valid without it, and a project command without `{output}` still has to leave the file, which the evidence check enforces.
- **Driving codex's interactive `/review` picker with keystrokes (option B), and a self-reported `ran` flag (option C):** both rejected by the maintainer in q1.
- **Skipping the native command in plan rounds (option D):** rejected in q1.
- **Deleting `native-review.txt` before each reviewer starts:** the attempt-suffixed `ArtifactsDir` is new for every attempt and every round, so no earlier file can already be there.
- **A TUI rendering of `review-find`:** the item names the plain face and `report.md`. A failed reviewer already reaches the TUI as the step's failed reason.

## Assumptions

- The maintainer's q1 answer settles the notes' open question: plan rounds run the native command too, plus the rubric.
- The failure reason reads ``reviewer <name>: evidence missing: native review `<cmd>` produced no output``. The `evidence missing: ` prefix is `Judge`'s standing prefix (`evidence.go:531`), kept for consistency with every other evidence failure. It still contains the maintainer's wording.
- `claude.yaml` is unchanged. q1 keeps `/code-review`, and its report is saved to the file by the agent under the prompt's instruction. So the watchdog's "Files" warning is met by this plan's file list: `claude.yaml` needs no edit. The report writer is `internal/core/report.go` (`Report`, called from `writeReport` at `loop.go:1160`), not `loop.go` itself.
- The watchdog's "Risk" warning is resolved by changes 2-4. The native-output file is the detection mechanism, and it applies to every reviewed step, plan and implement alike, because both go through `ReviewHalf`.
- A nested `codex exec review` that cannot reach the network leaves no output file, or the reviewer writes a failed sentinel. Either way the reviewer fails. No sandbox or network flag is added here (issue #6 owns codex's sandbox flags).
- q1 amends ADR-55 for codex only: the reviewer is still a herdr session (the watchdog still reads it), and that session runs `codex exec review` from its shell. Change 8a records this in the spec.
- The `command` field records the expanded command, including the output path, because that is exactly what ran.

## Gate

`go test ./internal/core/ ./internal/providers/ ./internal/prompts/ ./internal/face/plain/ ./internal/app/ -run '^(TestToCoreFillsTheReviewArgsWithFlagsModelAndEffort|TestResumeDuringAReviewContinuesAtTheRecordedRound|TestNativeReviewCommandPointsAtTheOutputFileInItsArtifactsDir|TestNativeReviewerWithoutOutputFailsTheStepNamingTheCommand|TestClaudeReviewerWithoutOutputFailsTheSameWay|TestReviewerThatCannotRunItsCommandFailsTheStep|TestShellQuoteKeepsAPathOneShellArgument|TestARetriedAttemptCannotPassOnAnEarlierAttemptsNativeOutput|TestReviewFindNamesTheCommandEachReviewerRan|TestZeroFindingsEndsTheHalfClean|TestFindingsCheckRequiresTheNativeOutputWhenNamed|TestShippedCodexBlock|TestReviewRunsTheNativeCommandFirstAndKeepsItsOutput|TestReviewFindLineNamesTheCommandTheReviewerRan|TestReportListsEachReviewerAndTheCommandItRan)$'`
