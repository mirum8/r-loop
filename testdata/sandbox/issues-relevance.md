# Calc — backlog drafted from a user's email of 1 Oct 2026

Three asks, drafted before anyone checked them against the code.

- [ ] [#1] `Add` rejects negative numbers
      - `Add("-3,5")` returns `2`

- [ ] [#2] Accept hexadecimal input
      - `Add("0x1F,1")` returns `32`

- [ ] [#3] Large sums overflow silently
      - Change `Add` to return a `float64`
      - `Add("9223372036854775807,1")` no longer returns a negative number
