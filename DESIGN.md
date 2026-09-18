---
version: alpha
name: Instrument
description: A quiet measuring device. Near-monochrome until something needs a person.
omitted:
  - section: typography
    reason: "the terminal owns the font; only bold, dim, inverse and underline are ours"
  - section: rounded
    reason: "cells have no radius"
terminal:
  colorDepth: truecolor
colors:
  surface: "#0F1115"
  surface-raised: "#171A20"
  on-surface: "#D6DAE0"
  on-surface-dim: "#8A929E"
  primary: "#6E9FC4"
  on-primary: "#0F1115"
  secondary: "#E0A458"
  on-secondary: "#0F1115"
  tertiary: "#8FA87F"
  error: "#E0736A"
  on-error: "#0F1115"
  outline: "#2E343D"
spacing:
  gutter: 2
  pad: 1
  margin: 2
  rail: 24
components:
  row:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.on-surface}"
  row-idle:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.on-surface-dim}"
  row-selected:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.on-primary}"
  row-running:
    backgroundColor: "{colors.surface}"
    textColor: "{colors.primary}"
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
    backgroundColor: "{colors.surface-raised}"
    textColor: "{colors.on-surface-dim}"
  statusbar:
    backgroundColor: "{colors.surface-raised}"
    textColor: "{colors.on-surface-dim}"
  banner-question:
    backgroundColor: "{colors.secondary}"
    textColor: "{colors.on-secondary}"
  banner-warn:
    backgroundColor: "{colors.surface-raised}"
    textColor: "{colors.secondary}"
  banner-halt:
    backgroundColor: "{colors.error}"
    textColor: "{colors.on-error}"
---

# Instrument

## Overview

`r-loop` is a supervisor that runs an implementation plan to completion over hours, and the person
watching it is one maintainer with it open on a second screen. For almost all of that time there is
nothing for them to do. So the screen is built for the glance, not for the read: it should be
possible to look at it from across a desk and know, without focusing, whether anything wants a
human.

The register is a **measuring instrument** — calm, precise, not playful, and deliberately close to
monochrome. Colour is not decoration here; it is the signal. The palette spends almost everything on
a single restrained blue for what is live and a single amber for what is waiting on a person, and
leaves every other row in greys. A screen where four things are coloured is a screen where nothing
is.

What it should not feel like: a build dashboard. Those mark every row with status colour, and the
result is that an urgent row and a finished one compete for the same attention.

**The chosen arrangement is the rail-and-detail skeleton**: a fixed-width phase rail down the left,
the live step and the watchdog feed stacked in the pane to its right, and a full-width question
banner above the key bar. It was picked against a stacked table and a three-pane split; the rail
keeps the whole plan visible while the detail pane carries the one phase that is live, which is the
arrangement this identity's glance-first premise depends on.

## Colors

Two greys carry the whole interface. `surface` is a near-black with a trace of blue so that a
terminal's own pure black still reads as behind it; `on-surface` is a light neutral that sits at
roughly 13:1 against it, which is far above the floor and deliberately so — the body text should
never be the thing you strain at.

`on-surface-dim` is the workhorse. Phases that have landed, steps not yet started, timestamps and
labels all use it, which is what leaves the two accent colours alone to mean something.

- **`primary`** — a muted steel blue. The live step, and the selected row. One thing on screen is
  live, so one thing on screen is blue.
- **`secondary`** — amber. **Something is waiting for you**: an open question, a warning, a remedy
  asking for consent. This is the only colour that means *act*, and nothing else is allowed to use
  it.
- **`tertiary`** — a desaturated sage. A phase that landed. It reads as settled rather than
  celebratory, because a landed phase is not news.
- **`error`** — a warm red, used for a failed step and a watchdog halt.

`outline` is intentionally low-contrast against `surface`. Borders here are structure, not content;
a border that competes with text is a border drawn too brightly, and the linter's contrast warning
on this pair is expected rather than a defect.

## Typography

Declared omitted above — the terminal owns the font. What remains ours is weight and attribute, and
the rules are narrow:

- **Bold** marks exactly one thing: the phase currently being built. Not headers, not labels.
- **Dim** is `on-surface-dim`'s job and is not additionally applied.
- **Inverse** is reserved for the selected row, and pairs with `primary`.
- **Underline** is unused. It survives poorly across terminals and there is nothing here that needs
  a third emphasis.

## Layout

Spacing is in cells and is generous by terminal standards, because the screen is mostly empty and
the emptiness is the point.

- `gutter: 2` between columns, so the phase list and the detail pane do not touch.
- `pad: 1` inside every bordered box.
- `margin: 2` at the frame edge.
- `rail: 24` — the phase rail is a fixed 24 columns, which fits `Phase 12 · implement` with room
  and does not reflow as the run progresses.

The vertical rhythm is one line per phase, one blank line between the rail and the detail. No
multi-line rows: a run of twenty phases should fit without scrolling at 40 lines, and a row that
wraps destroys the scannability the whole design is for.

## Elevation & Depth

There are no shadows in a terminal, and this design does not simulate them with gradients or block
characters. Depth is carried by exactly two devices:

1. **`surface-raised`** for the header, the status bar and any panel that sits above the run — a
   single step lighter, barely perceptible, enough to separate chrome from content.
2. **The border colour.** A focused pane draws its border in `on-surface-dim`; an unfocused one in
   `outline`. That one-step difference is the entire focus model.

Nothing else is layered. A modal is a bordered box on `surface-raised` with the rest of the screen
left alone, not dimmed — dimming the background in a terminal means repainting every cell, and it
looks like a rendering fault.

## Shapes

**Single-line box drawing**: `│ ─ ┌ ┐ └ ┘ ├ ┤ ┬ ┴ ┼`. Light, uniform weight, and legible in every
monospace font that ships with a modern terminal.

No heavy or double-line variants — they read as emphasis, and emphasis in this design is carried by
colour alone. Where box-drawing glyphs are unavailable the fallback is ASCII `| - +`, which is
noticeably worse and is why the document declares truecolor terminals as the target.

## Components

The state vocabulary is small on purpose — five row states, three banners:

| component | when |
|---|---|
| `row-idle` | a phase not yet started, or already landed and scrolled past |
| `row-running` | the phase currently being built — `primary`, and the only bold row |
| `row-selected` | the row under the cursor — inverse against `primary` |
| `row-landed` | merged and ticked — `tertiary` |
| `row-failed` | a failed step or a watchdog halt — `error` |
| `banner-question` | an agent is waiting on an answer — full-width `secondary` |
| `banner-warn` | the watchdog raised a warning, run continues — `secondary` on raised surface |
| `banner-halt` | the run stopped — full-width `error` |

`banner-question` is the loudest thing this design can produce, and that is correct: it is the one
state where the run is stopped and a person is the only thing that can move it.

## Do's and Don'ts

- **Do** leave a row grey unless its state is one a person acts on.
- **Do** use amber for exactly one meaning: something is waiting for you.
- **Do** keep every row one line, at any terminal width down to 80 columns.
- **Don't** colour a landed phase in green-as-celebration. It is settled, not good news.
- **Don't** add a fourth accent. Three is already at the edge of what a glance resolves.
- **Don't** simulate shadow, gradient or dimmed overlays with block characters.
- **Don't** use underline, or bold for anything except the live phase.
