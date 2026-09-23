# Driver — backlog from the /test-app run on the pre-run triage change, 23 Sep 2026

The /test-app run on the pre-run triage change (ADR-77/78) found nine problems. Four came from the
triage change itself and are already fixed in the working tree. Four are older bugs the run exposed,
and one is an environment problem. All five of those are work, listed below. Where the report
misread the cause, and which items move architecture or the estimate, is in
`issues-triage-test-run-2026-09-23-notes.md`. `[#n]` numbers the leftover findings in the order the
report gave them.

Verified against `r-loop` @ `main` `492697c`, with the uncommitted triage change in the working
tree.

- [ ] [#1] Item numbers are ordinal positions, not the `[#N]` tag: `[#6]` became item/phase 4
      - For an issues file, the triage table, the dry-run item table and the skipped lines show each item's own leading `[#…]` tag (e.g. `[#6]`), not only its place in the file. An item with no tag shows its place, as today
      - A group phase's title and its criterion prefixes name the members by their tags, e.g. `calc.Add input parsing ([#6], [#7])`
      - Branches, worktrees, the `<!-- fixed: r-loop/phase-N -->` marker, `--phases`, triage verdict ids and resume stay on the place-in-file ID. A backlog with `[#5a]`, `[#1/2]` and repeated `[#1]` tags still parses, ticks and resumes

- [ ] [#2] A watchdog `warn` signal arriving after the step already failed produces a phase-blocked reason `watchdog: watchdog signal rejected: phase-1/plan is not running`
      - A watchdog `warn` rejected only because its step has already ended is recorded as rejected, but it halts nothing. The held step's remedy window, restart and fallback go on as they would without it
      - When a failed step blocks its phase, the blocked reason, the run's halt reason, the report and the `onHalt` hook carry the step's own failure (e.g. the reviewer's error). They never read `watchdog signal rejected: …` unless an accepted watchdog halt stopped the step
      - A rejected `warn` sent between steps does not halt the next step that starts
      - A malformed or unknown step name in a watchdog signal still halts the run as a protocol error, and the tests that pin that behaviour still pass

- [ ] [#3] After resume, the TUI header shows the absolute plan path (truncated at 80 cols) instead of the relative path the first run showed
      - After `r-loop resume` of a run started as `r-loop docs/x/todo.md`, the TUI header shows `docs/x/todo.md`
      - The first run and the resumed run show the same plan path in the header, whichever directory inside the repo resume is started from
      - A plan outside the repository still shows a usable path; nothing crashes

- [ ] [#4] `r-loop --help` prints only `usage: r-loop <todo.md> [flags]: flag: help requested` with exit 2 and no flag list
      - `r-loop --help` and `r-loop -h` print the command forms and the run flags with their descriptions to stdout and exit 0, and start no intake session
      - `r-loop status -h`, `r-loop resume -h` and `r-loop abort -h` print their own usage and exit 0
      - An unknown flag still prints usage to stderr and exits 2
      - The README's command-line section documents `--help`

- [ ] [#5] codex reviewers fail inside a nested macOS sandbox with `failed to initialize in-process app-server client: Operation not permitted`
      - The failing setup is reproduced once, with the exact environment recorded: which process runs `codex exec review`, and under which sandbox
      - When the review command cannot start, the reviewer failure on the face and in `report.md` includes codex's own error line and the command, not only a paraphrase
      - Once the cause is known, a codex reviewer's `codex exec review` completes in a normally started run, and a test or no-agent check catches the refusal before a live run does
