# Calc — Gate Choice Plan

Sources: this file · Status: draft
One phase whose `Done when:` line mixes a real command with backticked prose, so the watchdog must choose the gate.

## Waves
<!-- generated from the Depends on edges — regenerate, never hand-edit -->
- Wave 0: Phase 1

## Milestone 1 — Calculator

### Phase 1 — Subtract
**Implements:** Subtract numbers
**Depends on:** none
**Files:** `subtract.go` (new) · `subtract_test.go` (new)
- [ ] `Subtract(a, b int) int` returns `a - b`, with a table test covering a negative result
**Done when:** `go test ./...` is green and `calc` serves `GET /sum` against the local stack.
