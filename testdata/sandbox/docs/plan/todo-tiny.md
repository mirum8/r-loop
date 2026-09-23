# Calc — Smoke Plan

Sources: this file · Status: draft
One phase, the cheapest full `plan → implement → land` run of the driver.

## Waves
<!-- generated from the Depends on edges — regenerate, never hand-edit -->
- Wave 0: Phase 1

## Milestone 1 — Calculator

### Phase 1 — Subtract
**Implements:** Subtract numbers
**Depends on:** none
**Files:** `subtract.go` (new) · `subtract_test.go` (new)
- [ ] `Subtract(a, b int) int` returns `a - b`, with a table test covering a negative result
**Done when:** `go test ./...` is green.
