status: planned

## Summary

`Repo.DiffStat` (`internal/gitrepo/repo.go:347`) counts each untracked file's added lines by loading the whole file with `blob` (`repo.go:380`, `os.ReadFile`) and counting `\n` bytes. That loads multi-gigabyte files whole, and a binary file counts as however many `0x0A` bytes it happens to hold. `git diff --numstat` reports `-` for a binary, which the tracked loop already turns into 0 through `strconv.Atoi`.

The phase replaces `blob` with `untrackedLines(path string) (int, error)`. For a symlink, it counts the link target string, as `blob` does today. For a regular file, it streams the file through a `bufio.Reader` of 32 KiB:
- It peeks the first 8000 bytes. If any of them is NUL, the file is binary and counts as 0. That is git's own rule (`buffer_is_binary`, `FIRST_FEW_BYTES` = 8000 in xdiff-interface.c), so an untracked file is judged the way numstat judges a tracked one.
- Otherwise it reads the file in chunks, counts `\n`, and remembers the last byte. It adds 1 when the file is non-empty and does not end in `\n`, which is the same arithmetic as today.

Memory stays at one 32 KiB buffer per file whatever the file's size.

Choices:
- **How to count untracked files:** stream them in Go (taken), or let git count them. Git could count them through `git add -N` into a temp index followed by `git diff --numstat`, or through one `git diff --no-index --numstat /dev/null <file>` per file. Streaming won: it is a local change to one helper, adds no subprocess per file, and does not move the symlink semantics that `TestDiffStatCountsSymlinkNotTarget` pins.
- **Binary rule:** a NUL within the first 8000 bytes (taken), or a NUL anywhere in the file. The 8000-byte rule won: the item says "as numstat does", and git applies exactly that rule. It also lets the scan stop at the peek for a binary.
- **Large files:** stream them (taken), or skip files above a size cap. Streaming won: text line counts stay exact at any size (item 3), and it adds no cap constant.

## Changes

1. `internal/gitrepo/repo.go` (modify)
   - In `DiffStat` (`repo.go:347`), replace the untracked loop body (`repo.go:367-375`) with:
     ```go
     n, err := untrackedLines(filepath.Join(r.path(dir), p))
     if err != nil {
     	return 0, 0, err
     }
     added += n
     ```
   - Delete `blob` (`repo.go:380-390`). Its only caller is `repo.go:368`.
   - Add `func untrackedLines(path string) (int, error)`:
     - Keep the `os.Lstat` and symlink branch from `blob`. Both paths feed one `var src io.Reader` into a single counting loop:
       - For a symlink: `target, err := os.Readlink(path)`. On error, return `0, err`. Otherwise `src = strings.NewReader(target)`.
       - For any other entry: `f, err := os.Open(path)`. On error, return `0, err`. Otherwise `defer f.Close()` and `src = f`.
     - The counting loop:
       - Create `br := bufio.NewReaderSize(src, 32<<10)`. It runs on `src` from either path.
       - Call `head, err := br.Peek(8000)`. An `io.EOF` or `bufio.ErrBufferFull` is not an error here; any other error is returned as `0, err`. If `bytes.IndexByte(head, 0) >= 0`, return `0, nil`.
       - Then `buf := make([]byte, 32<<10)`, `n, last := 0, byte('\n')`. Loop on `k, err := br.Read(buf)`: `n += bytes.Count(buf[:k], []byte{'\n'})`, and when `k > 0`, set `last = buf[k-1]`. On `err == io.EOF`, break. On any other error, return `0, err`.
       - After the loop, `if last != '\n' { n++ }`. Initialising `last` to `'\n'` makes an empty file count 0.
     - The `os.Open` error is returned unchanged. This keeps today's behaviour for a vanished or unreadable file, and for a directory entry such as a nested repo (`sub/`), where the read fails with EISDIR.
   - Add `"bufio"` to the imports. `bytes`, `io`, `os` and `strings` are already imported (`repo.go:3-17`).
   - Obligations: binary counts as 0 (item 1); streamed, never loaded whole (item 2); text counts unchanged, including an unterminated last line, an empty file and a symlink (item 3); read and open errors still surface.

2. `internal/gitrepo/repo_test.go` (modify): add the tests below after `TestDiffStatCountsSymlinkNotTarget` (`repo_test.go:958`). They use the existing `newRepo`, `write` and `git` helpers (`repo_test.go:21-57`). Add `"runtime"` and `"bytes"` to the test imports.

## Tests

All tests are in `internal/gitrepo/repo_test.go` and are written first. Each one starts from `newRepo(t)`, whose only tracked file is `a.txt`, so the tracked numstat part is 0/0.

- `TestDiffStatCountsUntrackedBinaryAsZero`
  - Setup: write untracked `bin.dat` = `"a\x00b\nc\nd\n"` and untracked `t.txt` = `"x\ny"`.
  - Asserts: `DiffStat("", "HEAD")` returns `2, 0, nil`. The binary counts 0 and the text beside it still counts.
  - Covers item 1, and fails on the base code (5).
- `TestDiffStatTreatsNULPastFirst8000BytesAsText`
  - Setup: write untracked `late.dat` = `strings.Repeat("a", 8000) + "\n\x00\n"`.
  - Asserts: `DiffStat` returns `2, 0, nil`. This pins git's 8000-byte binary rule, so a NUL beyond the peek is still text, as numstat would count it.
  - Covers item 1 (parity with numstat).
- `TestDiffStatStreamsLargeUntrackedFile`
  - Setup: write untracked `big.txt` = `bytes.Repeat([]byte("x\n"), 16<<20)` (32 MiB) with `os.WriteFile`, then `data = nil`.
  - Asserts:
    - Call `runtime.ReadMemStats(&before)`, then `DiffStat("", "HEAD")`, then `runtime.ReadMemStats(&after)`.
    - The call returns `16<<20, 0, nil`.
    - `after.TotalAlloc-before.TotalAlloc < 4<<20`.
  - Covers item 2. It fails on the base code, where `os.ReadFile` allocates at least 32 MiB.
- `TestDiffStatCountsUntrackedTextLines`
  - Setup: write untracked files `empty.txt` = `""` (0), `blank.txt` = `"\n\n"` (2), `open.txt` = `"x\ny"` (2) and `long.txt` = `strings.Repeat("a", 40000)` (1: a single unterminated line longer than both the peek and the 32 KiB chunk).
  - Asserts: `DiffStat` returns `5, 0, nil`.
  - Covers item 3, including a last line with no trailing newline across a chunk boundary.
- `TestDiffStatReturnsErrorForUnreadableUntrackedFile`
  - Setup: call `t.Skip` when `os.Geteuid() == 0`. Write untracked `secret.txt` = `"s\n"`, then `os.Chmod(path, 0)` with a `t.Cleanup` that restores `0o644`.
  - Asserts: `DiffStat` returns a non-nil error.
  - Covers the open error path, still surfaced after the rewrite.
- `TestDiffStatCountsSymlinkNotTarget` (existing, `repo_test.go:958`): a regression check, run by the gate, that symlinks still count their target string (item 3).

## Left out

- **A size cap or skipping large files:** streaming keeps memory bounded without losing exact counts, so no cap is needed.
- **Honouring `.gitattributes` (`binary`, `-diff`) for untracked files:** no item asks for it, and it needs `git check-attr` per file. Only the NUL rule is required.
- **Special handling for nested-repo directory entries (`sub/`) from `ls-files --others`:** the base code already errors on them. This phase preserves that behaviour, and no item covers it.
- **A new file for the helper, or an interface:** it has a single call site and stays next to `DiffStat`.

## Assumptions

- The binary test is git's own rule: a NUL within the first 8000 bytes, the same rule numstat applies to tracked files.
- The read chunk and the `bufio` buffer are both 32 KiB. The allocation bound in the streaming test is 4 MiB, against a 32 MiB file.
- An open or read error on any untracked file still fails the whole `DiffStat`, as today.

## Gate

`go test ./internal/gitrepo/ -run '^(TestDiffStatCountsUntrackedBinaryAsZero|TestDiffStatTreatsNULPastFirst8000BytesAsText|TestDiffStatStreamsLargeUntrackedFile|TestDiffStatCountsUntrackedTextLines|TestDiffStatReturnsErrorForUnreadableUntrackedFile|TestDiffStatCountsSymlinkNotTarget)$'`
