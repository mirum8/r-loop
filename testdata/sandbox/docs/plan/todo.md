# Calc — Implementation Plan

Sources: this file · Status: draft
A three-phase plan for the r-loop driver tests. Phase 1 and Phase 2 are independent; Phase 3
waits on Phase 1 and on the open entry under `## Resolve first`.

## Resolve first

- [ ] **Custom delimiter syntax** — should a custom delimiter be declared as `//;\n1;2` or through a separate argument?
      Owner: maintainer · Blocks: Phase 3 · Timebox: 10m
      Output: the syntax Phase 3 implements

## Waves
<!-- generated from the Depends on edges — regenerate, never hand-edit -->
- Wave 0: Phase 1, Phase 2
- Wave 1: Phase 3

## Milestone 1 — Calculator

### Phase 1 — Newlines separate numbers too
**Implements:** Add numbers from free-form input
**Depends on:** none
**Files:** `calc.go` (modify) · `calc_test.go` (modify)
- [ ] `Add` accepts `\n` as a separator beside `,`, so `Add("1\n2,3")` returns `6`
- [ ] `Add("1,\n")` returns an error rather than a sum
**Done when:** `go test ./...` is green.

### Phase 2 — Multiply
**Implements:** Multiply numbers from comma-separated input
**Depends on:** none
**Files:** `multiply.go` (new) · `multiply_test.go` (new)
- [ ] `Multiply(input string) (int, error)` returns the product of comma-separated integers; an empty input returns `1`
- [ ] a non-integer part returns an error, as `Add` does
**Done when:** `go test ./...` is green.

### Phase 3 — Custom delimiter
**Implements:** Add numbers split by a delimiter the caller chooses
**Depends on:** Phase 1
**Files:** `calc.go` (modify) · `calc_test.go` (modify)
- [ ] `Add` accepts the custom-delimiter syntax settled under `## Resolve first`
- [ ] the default separators still work when no delimiter is declared
**Done when:** `go test ./...` is green.
