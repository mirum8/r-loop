# Calc spec

## ADR-1 — Add takes base-10 integers only

`Add` sums comma-separated base-10 integers and returns an `int`. Other number formats — hexadecimal, octal, Roman numerals, floating point — are out of scope, and `Add` keeps its `(int, error)` signature: callers rely on an exact integer result.
