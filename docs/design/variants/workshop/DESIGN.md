---
version: alpha
name: Workshop
description: The pack's own warm house palette, carried into the terminal. Spacious, literate, unhurried.
omitted:
  - section: typography
    reason: "the terminal owns the font; only bold, dim, inverse and underline are ours"
  - section: rounded
    reason: "cells have no radius"
terminal:
  colorDepth: truecolor
colors:
  surface: "#1C1917"
  surface-raised: "#282320"
  on-surface: "#EDE7DE"
  on-surface-dim: "#A89A8B"
  primary: "#D97757"
  on-primary: "#1C1917"
  secondary: "#D9A441"
  on-secondary: "#1C1917"
  tertiary: "#9BB075"
  error: "#DF6A52"
  on-error: "#1C1917"
  outline: "#3B342F"
spacing:
  gutter: 3
  pad: 1
  margin: 4
  rail: 26
components:
  row:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.on-surface}"
  row-idle:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.on-surface-dim}"
  row-running:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.primary}"
  row-selected:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.on-primary}"
  row-landed:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.tertiary}"
  row-failed:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.error}"
  panel:
    backgroundColor: "{colors.surface-raised}"
    textColor: "{colors.on-surface}"
  border:
    textColor: "{colors.outline}"
  header:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.primary}"
  statusbar:
    backgroundColor: "{colors.surface-raised}"
    textColor: "{colors.on-surface-dim}"
  eyebrow:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.on-surface-dim}"
  banner-question:
    backgroundColor: "{colors.surface-raised}"
    textColor: "{colors.secondary}"
  banner-warn:
    backgroundColor: "{colors.surface-raised}"
    textColor: "{colors.secondary}"
  banner-halt:
    backgroundColor: "{colors.error}"
    textColor: "{colors.on-error}"
---

# Workshop

## Overview

The documents this tool is built from — the specs, the plans, the reports it will write — already
have a house style: warm paper, a clay accent, an olive for things that worked, a serif register
that reads as considered rather than mechanical. This identity carries that family resemblance into
the terminal, so that `r-loop`, the spec it implements and the milestone reports it produces look
like they came from one place.

The register is a **workshop** — warm, unhurried, literate. It assumes the person watching is the
same person who wrote the plan, and that the screen is a place they will sit with rather than
monitor. So it is spacious where a console is dense, and it labels things in words where a console
uses a chip.

What it is not: neutral. A warm charcoal ground is a stated preference and will look wrong against a
cool-themed terminal beside it. That is the trade, and it buys the only thing the other two
candidates cannot — continuity with everything else the pack produces.

## Colors

The palette is the pack's document palette, adapted from paper to screen. `--ivory`, `--clay`,
`--olive` and `--slate` are recognisable here, inverted for a dark terminal and lifted where a
paper value would have been too dark to read on one.

- **`surface`** — a warm charcoal, the inversion of the documents' ivory. It has a red-brown cast
  rather than a blue one, which is what keeps the clay from looking orange against it.
- **`on-surface`** — warm off-white, the ivory itself at terminal weight.
- **`primary`** — `#D97757`, the pack's clay, unchanged. It marks the live phase and the selection,
  exactly as it marks part numbers and pivotal events in the specs.
- **`tertiary`** — the olive, lifted from the documents' `#788C5D` to `#9BB075` because the paper
  value sits too close to the ground to read as text on a dark terminal. Landed phases.
- **`secondary`** — a warm amber for anything waiting on a person.
- **`error`** — a red pulled toward the clay rather than away from it, so a failure reads as part of
  the same family rather than as a system alert pasted in.

`on-surface-dim` does most of the work, as in any design where the accents are reserved. The
distinguishing choice is that it is *warm* grey, not neutral — a cool grey against this ground reads
as a different design leaking in.

## Typography

Declared omitted — the terminal owns the font, and the serif that carries this register on paper is
simply unavailable. What replaces it is restraint and space rather than attribute:

- **Bold** on the phase heading and the live step only.
- **Dim** is `on-surface-dim`, used as a colour rather than as an ANSI attribute, so it survives
  terminals that render dim inconsistently.
- **Inverse** is used once, for the selected row.
- **Underline** is unused.

Where a document would set a section in small caps, this uses an `eyebrow` — a dim, spaced label
line above a group. That is the closest a terminal comes to the specs' `.pnum` treatment, and it is
why the spacing scale is generous enough to afford a line for it.

## Layout

Spacious. Air is how this design carries the register that a serif carries on paper.

- `gutter: 3` — three cells between columns, wide enough that the eye groups by column without a
  rule between them.
- `pad: 1` inside bordered boxes.
- `margin: 4` at the frame edge, which is the single most distinctive measurement here: content
  never touches the terminal's edge.
- `rail: 26` — the phase rail is wide enough for a phase title, not just its number, because a
  design that reads as literate should show the words.

One blank line between phase groups, and an eyebrow line above each group. A run of twelve phases
fills a 40-line screen comfortably; beyond that the rail scrolls. This design is explicitly worse
than `Console` at forty phases, and better at twelve.

## Elevation & Depth

Depth is warmth, not layering. `surface-raised` is a step lighter *and* a step warmer, so a panel
reads as lifted toward the light rather than merely lighter.

The other device is the left rule: a bordered group carries a single `│` in `primary` down its left
edge, which is the terminal's version of the specs' `.lede` and `.adr` left borders. That one
character does the work a card shadow does on paper, and it is why there are so few boxes here —
most groups need a rule, not a frame.

No shadows, no gradients, no dimmed overlays.

## Shapes

**Rounded box drawing**: `╭ ╮ ╰ ╯ │ ─`. The softened corner is the only place this design spends a
glyph on register rather than on information, and it is worth it: against the warm ground, square
corners read as institutional in a way the rounded set does not.

Heavy and double-line variants are unused. Where the rounded glyphs are unavailable the fallback is
the single-line set — not ASCII — and a terminal without either is out of scope, which the
truecolor requirement already implies.

## Components

| component | when |
|---|---|
| `eyebrow` | a dim label above a group — the terminal's small caps |
| `row-idle` | not started, or landed and scrolled past |
| `row-running` | the live phase — clay, bold |
| `row-selected` | under the cursor — inverse on clay |
| `row-landed` | merged and ticked — olive |
| `row-failed` | a failed step or a halt — warm red |
| `panel` | a detail pane, on the raised surface with a clay left rule |
| `banner-question` | an agent is waiting — amber on the raised surface, not inverted |
| `banner-halt` | the run stopped — the one inverted element in the design |

`banner-question` is deliberately *not* inverted, unlike the other two candidates. In a design this
quiet, amber text on a raised panel is already the loudest thing on screen, and inverting it would
break the register for a state that is common rather than exceptional. `banner-halt` inverts,
because a halt should break the register.

## Do's and Don'ts

- **Do** keep the clay for one meaning — live, or selected — exactly as the documents keep it for
  part numbers.
- **Do** spend cells on air; the margin is the design.
- **Do** write words where a console would use a chip.
- **Do** use the left rule instead of a full box wherever a group needs separating.
- **Don't** introduce a cool grey, a cool accent, or a blue. One cold colour ruins the ground.
- **Don't** invert anything except the selected row and a halt.
- **Don't** use this at forty phases on a 40-line screen; it is the wrong candidate for that run.
- **Don't** square the corners. The rounded set is the register.
