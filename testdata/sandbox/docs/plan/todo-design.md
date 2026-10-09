# Calc — Design Plan

Sources: this file · Status: draft
Two independent phases for the design step (ADR-92): Phase 1 changes what a user sees on the
terminal, Phase 2 is library logic with no visible output.

## Waves
<!-- generated from the Depends on edges — regenerate, never hand-edit -->
- Wave 0: Phase 1, Phase 2

## Milestone 1 — Calculator

### Phase 1 — Command-line calculator
**Implements:** Add numbers from the command line
**Depends on:** none
**Files:** `cmd/calc/main.go` (new) · `cmd/calc/main_test.go` (new)
- [ ] `calc "1,2,3"` prints the result line `sum: 6` to stdout and exits 0
- [ ] `calc "1,x"` prints a one-line error naming the bad input to stderr and exits 1
**Done when:** `go test ./...` is green.

### Phase 2 — Max
**Implements:** Pick the larger of two numbers
**Depends on:** none
**Files:** `max.go` (new) · `max_test.go` (new)
- [ ] `Max(a, b int) int` returns the larger of `a` and `b`, with a table test covering equal and negative inputs
**Done when:** `go test ./...` is green.
