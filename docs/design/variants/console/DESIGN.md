---
version: alpha
name: Console
description: A dense operations console. Every row states its own condition, everything legible at once.
omitted:
  - section: typography
    reason: "the terminal owns the font; only bold, dim, inverse and underline are ours"
  - section: rounded
    reason: "cells have no radius"
terminal:
  colorDepth: truecolor
colors:
  surface: "#0B0F14"
  surface-raised: "#141C24"
  on-surface: "#C9D6E2"
  on-surface-dim: "#7D8FA1"
  primary: "#31C6D9"
  on-primary: "#06161B"
  secondary: "#F2B441"
  on-secondary: "#1A1204"
  tertiary: "#A98BF5"
  on-tertiary: "#120A24"
  success: "#5BD98A"
  warning: "#F2B441"
  error: "#F4736C"
  on-error: "#1B0806"
  outline: "#24323F"
spacing:
  gutter: 1
  pad: 0
  margin: 1
  rail: 18
components:
  row:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.on-surface}"
  row-queued:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.tertiary}"
  row-running:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.primary}"
  row-waiting:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.warning}"
  row-landed:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.success}"
  row-failed:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.error}"
  row-selected:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.on-primary}"
  panel:
    backgroundColor: "{colors.surface-raised}"
    textColor: "{colors.on-surface}"
  border:
    textColor: "{colors.outline}"
  header:
    backgroundColor: "{colors.surface-raised}"
    textColor: "{colors.primary}"
  statusbar:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.on-primary}"
  chip-ok:
    backgroundColor: "{colors.success}"
    textColor: "{colors.surface}"
  chip-warn:
    backgroundColor: "{colors.secondary}"
    textColor: "{colors.on-secondary}"
  chip-halt:
    backgroundColor: "{colors.error}"
    textColor: "{colors.on-error}"
  banner-question:
    backgroundColor: "{colors.secondary}"
    textColor: "{colors.on-secondary}"
  banner-halt:
    backgroundColor: "{colors.error}"
    textColor: "{colors.on-error}"
---

# Console

## Overview

`r-loop` runs an implementation plan across many phases and several agent sessions, and a lot is
happening at once — a step live, questions answered and logged, signals raised, remedies recorded,
phases landing. This identity assumes the person watching wants to see **all of it**, and treats
the screen as an operations console rather than a status light.

The register is **dense and instrumented** — think `htop`, a CI matrix, a trading terminal.
Every row states its own condition in colour, so the run's whole shape reads in one pass without
selecting anything or opening a detail pane. Density is a feature: more rows on screen means more
of the run visible, so padding is near zero and the type scale is whatever the terminal gives.

The honest cost, stated here because a candidate that hides its trade is not a real choice: when
everything carries status colour, nothing stands out by colour alone. This design answers that with
background inversion rather than hue — an urgent row is *filled*, not merely tinted — but if you
want the screen to be quiet until it isn't, this is the wrong one of the three.

## Colors

A cool near-black ground with a full status spectrum on top. Six states each get their own hue, and
the hues are chosen to be distinguishable rather than harmonious:

- **`primary`** — cyan. Live, running, and the current selection.
- **`tertiary`** — violet. Queued, not yet started. Distinct from grey so that "waiting its turn"
  and "finished" never look alike.
- **`warning`** / **`secondary`** — amber. Waiting on input, and watchdog warnings.
- **`success`** — green. Landed.
- **`error`** — coral red. Failed, or halted.
- **`on-surface-dim`** — only for timestamps, labels and column headers, never for a state.

Every foreground here is authored against `surface` above 4.5:1, and every `on-*` pair is a dark
text on a saturated fill so that inversion stays readable. That is what lets urgency be carried by
*fill* instead of by a seventh hue: `chip-halt` and `banner-halt` invert, and an inverted row beats
any tinted one for attention regardless of how many colours are already on screen.

## Typography

Declared omitted — the terminal owns the font. The attribute rules are looser here than in a
restrained design, because density needs more separation:

- **Bold** on column headers and on the live step's row.
- **Dim** on any row older than the current phase, so history recedes without losing its colour.
- **Inverse** carries urgency: the status bar, chips, and both banners.
- **Underline** on the focused pane's title, since with this many colours a border-colour change is
  not enough to read as focus.

## Layout

Tight. The point of this design is rows per screen.

- `gutter: 1` — one cell between columns, which is enough to separate them at this density.
- `pad: 0` — no padding inside boxes; the border is the padding.
- `margin: 1` at the frame edge.
- `rail: 18` — a narrower phase rail than a spacious design would use, because the columns to its
  right carry step, provider, elapsed and state, and all four should fit at 80 columns.

Rows are one line and columns are fixed-width with truncation, never wrapping. A run of forty
phases should be readable at 40 lines with the rail scrolled, and every column should hold its
position as the run progresses so the eye can return to the same place.

## Elevation & Depth

Hierarchy is carried by **saturation**, not by layering. The status bar is a full-width inverted
`primary` band — the most saturated thing on screen — and everything else recedes from it. Panels
use `surface-raised`, which is a step lighter and cooler.

There is no shadow simulation and no dimming overlay. A modal is a bordered panel drawn over the
content with its own border in `primary`; the content behind it stays exactly as it was, because
repainting the background dim in a terminal reads as a glitch.

## Shapes

**Heavy box drawing for the focused pane**, single-line elsewhere: `┃ ━ ┏ ┓ ┗ ┛` against
`│ ─ ┌ ┐ └ ┘`. With this many colours in play, border weight is a more reliable focus cue than
border colour, which is why the weight difference exists here and not in a quieter design.

Chips are drawn with the half-block `▌` as a leading marker plus an inverted label — they need to
survive at one cell of padding, and a bracketed `[ok]` costs two cells that this layout does not
have.

## Components

| component | when |
|---|---|
| `row-queued` | phase not started — violet |
| `row-running` | live — cyan, bold |
| `row-waiting` | an open question or a consent prompt — amber |
| `row-landed` | merged and ticked — green |
| `row-failed` | failed step or watchdog halt — coral |
| `row-selected` | under the cursor — inverted cyan |
| `chip-ok` · `chip-warn` · `chip-halt` | inline state markers in the step column |
| `statusbar` | full-width inverted cyan: run, phase counter, elapsed |
| `banner-question` · `banner-halt` | full-width inverted, pushing the rail down one line |

The chips exist so the step column can carry state without spending a colour on the row itself —
which is how a row can be both `row-running` and carrying a `chip-warn` from the watchdog.

## Do's and Don'ts

- **Do** give every state its own hue, and keep the mapping fixed across every pane.
- **Do** carry urgency with inversion rather than by adding hues.
- **Do** truncate columns; never wrap a row.
- **Do** dim history rather than decolouring it — the colour is still the state.
- **Don't** use green for anything but landed, or cyan for anything but live.
- **Don't** pad. Every cell spent on air is a row not on screen.
- **Don't** add a seventh state hue — use a chip instead.
- **Don't** rely on border colour alone for focus; weight carries it here.
