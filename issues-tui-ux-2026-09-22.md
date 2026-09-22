# TUI UX — backlog from the TUI review of 22 Sep 2026

There were 15 suggestions for the TUI. The first four have already been built: the question pointer,
the watchdog marker, the steps line and the event feed. The other 11 are below. `[#n]` is the
suggestion's number from that review. Every item has to keep to `DESIGN.md`: amber means only
"waiting for you", only the live phase is bold, and every row stays one line down to 80 columns.

Verified against `r-loop` @ `main` `79d8330`.

- [ ] [#5] Header progress `phase N/M`
      - The header's right side shows `phase <live>/<total>` while a phase is live, e.g. `phase 4/12`
      - Landed phases count toward N; blocked phases don't
      - Progress is dropped before the watchdog marker when the header doesn't fit

- [ ] [#6] Human durations (`43m`, `1h14m`)
      - Header elapsed, step elapsed, backstop and question age render as `12s`, `43m` or `1h14m`, never `43m0s`
      - A single formatter is used everywhere a duration is drawn

- [ ] [#7] Aging warnings stop being amber
      - A warning is drawn amber only while its phase is still live, or for the last 15 minutes for a run-level warning
      - After that it keeps its text and `!` marker but turns dim
      - Errors stay red regardless of age

- [ ] [#8] Step suffix on the live rail row, replacing the cryptic `p`/`i`
      - The live phase's rail row ends with its current step kind, e.g. `▸  4 session mgr  impl`, within 24 columns
      - The `p`/`i` glyphs become one "in progress" glyph; landed, blocked and unticked phases keep their glyphs
      - Long titles truncate before the suffix does

- [ ] [#9] Height handling: keep the live phase visible and collapse landed runs
      - The frame never has more lines than the terminal is tall; the header and footer always show
      - Consecutive landed phases collapse to one row such as `✓  1–9 landed` when the rail doesn't fit
      - The live phase's row is always visible; the event feed shrinks before the step panel does

- [ ] [#10] Rebuild the live step from history on resume
      - After `r-loop resume` the panel shows the last step that was running in the replayed history, not "no step running"
      - An open question from the prior run is not shown as waiting unless it is re-asked
      - The old run's ending (halted/finished) is still not replayed

- [ ] [#11] Key hint line
      - A dim line shows `^c stop` while the run is live and `q quit` once it has ended
      - The line uses the status-bar style and fits within one row at 80 columns

- [ ] [#12] OSC 2 terminal title
      - While running, the TUI sets the terminal title to `r-loop <N>/<M> <step>`; after a halt it is `r-loop HALTED`, after finishing `r-loop done`
      - The title is restored or cleared when the TUI exits
      - Nothing is written under `--plain`

- [ ] [#13] Landed-phase duration and attempts
      - A landed rail row shows its wall-clock duration, e.g. `✓  3 state store  18m`, within 24 columns
      - A phase that needed more than one attempt of any step shows it, e.g. `18m ×2`
      - The duration is taken from event timestamps, so it survives resume

- [ ] [#14] Phase row selection (j/k)
      - `j`/`k` move a `row-selected` cursor over the rail (inverse on `primary`), and `esc` returns to following the live phase
      - The panel shows the selected phase's steps: kind, final state, attempts, duration, merge SHA
      - Selection never changes what the run does

- [ ] [#15] Reviewer panes' count and findings during a round
      - During a review round the panel shows how many reviewers there are, how many have reported, and the findings count so far
      - `review-round` and `review-find` events reach the Face as well as the store (today `ReviewHalf.event` appends only to the store)
      - The plain face prints the same review progress
