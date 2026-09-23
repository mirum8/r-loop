# Calc — backlog from the maintainer's notes of 23 Sep 2026

Three asks; one is still open.

- [ ] [#1] `Add("1, 2")` fails on the space after the comma
      - Spaces around a number are ignored, so `Add(" 1 , 2 ")` returns `3`
      - A part that is only spaces is still an error

- [x] [#2] Empty input returns zero  <!-- fixed: add-empty -->
      - `Add("")` returns `0`

- [ ] ~~[#3] Add a Roman-numeral mode~~
      - Dropped: not in scope for the calculator
