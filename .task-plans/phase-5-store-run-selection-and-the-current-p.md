status: planned

## Summary

This phase fixes backlog items #5, #14, #15 and #23 of `issues/issues-reliability-review-2026-09-23.md` (the phase numbers them 5, 15, 16 and 24). One change covers all four: the run store, how a run is selected, and the single-run lock.

- **#5 Torn tail.** `Store.Append` checks the file's last byte before it writes. If the file does not end in `\n`, the tail after the last newline is either a complete record, which gets its missing `\n`, or a torn prefix, which is truncated away. The new record then always starts on its own line. `Load` keeps today's rules: it skips an unterminated torn last line with a warning, and it fails on any other bad line, naming the file and the line.
- **#23 Lock.** Liveness is no longer "the PID in `current` is alive". It becomes "some process holds an exclusive `flock(2)` on `.r-loop/runs/lock`". A new run and a resume take the lock before they write anything. The kernel drops the lock when the holder dies, so a crash needs no cleanup and a reused PID means nothing. `current` stays the pointer that names the live run (`<runID> <pid>`). A second `flock`ed file, `.r-loop/runs/gate`, makes the pair (lock, `current`) consistent for every observer: a driver holds `gate` exclusively from before it takes `lock` until it has published its own `current` (and again while it clears `current` and drops `lock` on exit), and every observer reads `lock` and `current` only while holding `gate` shared. So an observer sees either "lock free" or "lock held and `current` names the holder", never a stale pointer next to someone else's lock. An exiting driver removes the pointer only when it names its own run and PID, then releases the lock. All four places that call `alive(pid)` today (`preflight.go:48`, `resume.go:50`, `resume.go:368`, `status.go:58`) switch to the lock: start and resume take it, abort and status ask `Store.Live`.
- **#15 Finished TUI.** `Execute` releases the run (clears the pointer, then the lock) as soon as the loop has returned, the watchdog is stopped and the ask server has drained every in-flight handler, so no handler of this driver can append to the run after another driver takes it. That is before `Face.Close`, which is where the TUI waits for `q`. The final status is already recorded by then, because the loop, unblock and triage append it before they return. Because the run may now be taken over while the TUI is still up, the TUI's stop prompt no longer aborts a run that has ended.
- **#14 Run selection.** `resume` and `status` take an optional run ID. Without one, they pick the live run (the one `current` names while the lock is held). Otherwise they pick the newest run directory whose run recorded progress (status not `created`, or any step record). Directories whose `meta.json` can't be read are skipped with a warning. `meta.json` is written atomically and last in `Create`, so a directory whose `Create` was interrupted has no `meta.json` and is skipped.

Choices:
- **Lock primitive.** Options: `flock(2)`, POSIX `fcntl` record locks, or `O_EXCL` create of a pid file. Taken: **`flock`** on a separate file. `fcntl` locks belong to the process, so a second fd in the same process never conflicts: tests could not run two drivers in one process, and closing any fd on the file would drop the driver's own lock. `O_EXCL` leaves a stale file after a crash, which is the bug being fixed. `flock` belongs to the open file description, is released by the kernel on death, and conflicts between two opens in one process, so in-process tests can model two drivers.
- **Which file is locked.** Options: `current` itself, or a separate `.r-loop/runs/lock` that is never removed. Taken: **the separate `lock`**. `SetCurrent` replaces `current` by rename, which would move the pointer to a new inode and leave the lock on the old one.
- **How liveness is probed (status, abort, selection).** Options: probe `lock` with `LOCK_SH|LOCK_NB`, or with `LOCK_EX|LOCK_NB`. Taken: **`LOCK_SH|LOCK_NB`, under `gate` shared**. It fails only while someone holds the exclusive lock, and two probes never conflict. A starter takes `gate` exclusively before it tries `lock`, so a probe can never land between a starter's `gate` and `lock` and make `Lock` fail spuriously; `Lock` needs no retry loop.
- **Shape of the lock API.** Options: hidden state inside `Store` (`Lock()`/`Unlock()` on the store), or a handle `*store.Lock` returned by `Store.Lock()`. Taken: **the handle**. `PrepareResume` takes the lock with its own `store.New` before `Wire` builds `w.Store`. A handle can be handed to the `Wiring` (`w.lock`), and one store instance's state can't.
- **When a new run takes the lock.** Options: before `Store.Create`, or after it. Taken: **before**. A start that loses leaves no empty run directory behind, which is #14's "run left empty by a failed start".
- **How observers trust `current` (the loser's name, abort, status, selection).** Options: poll `current` while the lock is held; write the owner's identity into the lock file after `flock`; or a second `gate` lock held by the driver from before `lock` until `current` is published. Taken: **the `gate` lock**. The first two leave a window between taking `lock` and publishing the owner, in which a stale `current` (run A, left by a crash) sits next to a live lock held by run B: a loser would name A and `abort` would mark A. With `gate`, an observer blocks for exactly that window and then reads a consistent pair, with no fixed timeout; a loser whose winner gave up during its preflight takes the lock itself, so one start still proceeds.
- **Draining the ask server before release.** Options: rely on `cancel(nil)` and the 1 s `Shutdown` in `serveHTTP` (`internal/askmcp/server.go:115-125`), which runs in a goroutine nobody waits for; or add `Server.Wait()` that waits for that shutdown and for every in-flight handler. Taken: **`Wait()`**. Without it a watchdog or step tool call can still `Append` after `release` hands the run to another driver, breaking the one-driver-per-run invariant. `Wait` stays bounded: `ask_watchdog` returns when the server's ctx ends (`server.go:216-224`), and the watchdog tools do short, non-blocking work (e.g. `AskMaintainer` only marks the dog waiting, `internal/core/watchdog.go:291-300`) or herdr/git calls under their per-call timeouts.
- **Torn tail repair.** Options: truncate the torn prefix; terminate it with `\n` and teach `Load` to skip it; or move it aside to a `.torn` file. Taken: **truncate, but keep a tail that parses as a record**. A terminated torn line can't be told apart from real mid-file corruption, which must still fail `Load`. A `.torn` file is a new concept that no criterion asks for. A tail that parses was already counted by `Load`, so dropping it would change the run's state.
- **Serialising appends.** Options: a package-level mutex, or a mutex field on `Store`. Taken: **package-level `appendMu`**. The app builds several `Store` values on one root (`PrepareResume`, `Wire`, tests), and a tail check racing a half-written line from another goroutine would truncate a good record.
- **Where run selection lives.** Options: extend `runToShow` in `internal/app/status.go`, or add `Store.Latest`. Taken: **extend it, as `selectRun`, in app**. `runOrder`/`newerRun` are already there, and the progress rule is a decision about CLI behaviour, not about storage.
- **What makes a run skippable.** Options: skip any run whose `Load` fails, or skip only an unreadable `meta.json`. Taken: **only `meta.json`** (`store.ErrMeta`). Skipping a run with a corrupt record line would silently resume an older run. #5 requires that line to surface as an error naming file and line.
- **"Recorded progress".** Options: at least one step record; status not `created`; or both. Taken: **status ≠ `created` or at least one step record**. A start that fails before the loop (watchdog didn't start, usage error) never leaves `created`. A run the loop entered is `running` or later and is worth resuming even when it crashed before its first step.
- **Flag and positional order.** Options: the ID only after the flags, or interleaved like the main command. Taken: **interleaved**. The loop in `parseFlags` (`internal/app/wire.go:155-170`) already does this. It is extracted as `parsePositional` and becomes its third user.

## Changes

Build in this order.

### 1. `internal/store/store.go` (modify)

- **Imports.** Add `sync`.
- **`var appendMu sync.Mutex`** at package level (#5).
- **`Append`** (`store.go:98-120`, #5-1, #5-2, #5-3):
  - Lock `appendMu` for the whole call.
  - Open with `os.O_APPEND|os.O_RDWR`.
  - `Stat` the file. If size > 0, `ReadAt` the last byte. If it is not `'\n'`:
    - read the whole file with `os.ReadFile`, let `i := bytes.LastIndexByte(b, '\n')` and `tail := b[i+1:]`;
    - if `json.Unmarshal(legacyPhase(tail), &core.Record{}) == nil`, prefix the write with `"\n"`, so the write is `"\n" + line + "\n"`;
    - otherwise `f.Truncate(int64(i + 1))`, which truncates to 0 when there is no newline.
  - Then the existing single `Write` + `Sync` + `Close`.
  - Only an unterminated tail is ever touched. Complete lines, corrupt or not, are never rewritten.
- **`var ErrMeta = errors.New("meta.json")`** (#14-3).
  - `Load` (`store.go:124-131`) wraps both the `ReadFile` error and the `Unmarshal` error as `fmt.Errorf("%w: %w", ErrMeta, err)`.
  - The parse message keeps its current shape: `meta.json: …`.
- **`func writeAtomic(dir, name string, data []byte) error`** (#14-3, pinned by `TestWriteAtomicReplacesTheFileByRename`).
  - Temp file `.<name>-*` in `dir`, write, `Sync`, `Close`, `Rename` onto `dir/name`, and remove the temp on any error.
  - This is the body of `SetCurrent` (`store.go:270-287`) moved into a helper. `SetCurrent` becomes `MkdirAll(s.runs)` + `writeAtomic(s.runs, "current", fmt.Appendf(nil, "%s %d\n", runID, pid))`.
- **`Create`** (`store.go:63-96`, #14-2, #14-3): write `config.resolved.yaml`, then the four record files, then `meta.json` last, through `writeAtomic(dir, "meta.json", mb)`. A directory without `meta.json` is an interrupted create.
- **`func (s *Store) flockFile(name string, how int) (*os.File, error)`**: `MkdirAll(s.runs)`, `os.OpenFile(filepath.Join(s.runs, name), os.O_RDWR|os.O_CREATE, 0o644)`, then `syscall.Flock(int(f.Fd()), how)`; on a flock error close the file and return the raw error (callers test `syscall.EWOULDBLOCK`). Used by `Lock`, `Live` and `Release` below, for both `gate` and `lock` (#23).
- **`type LockedError struct{ RunID string; PID int }`** with `Error()`: `run %s is live in pid %d` when `RunID != ""`, else `another r-loop run is live in this repository` (#23-1).
- **`type Lock struct{ s *Store; gate, f *os.File }`** (#23).
- **`func (s *Store) Lock() (*Lock, error)`** (#23-1, #23-4):
  - `gate, err := s.flockFile("gate", syscall.LOCK_EX)`: blocking, so a starter waits while another driver is between its own `Lock` and `Publish` (or inside `Release`). Error → `fmt.Errorf("run lock: %w", err)`.
  - `f, err := s.flockFile("lock", syscall.LOCK_EX|syscall.LOCK_NB)`.
    - `EWOULDBLOCK`: `id, pid, ok := s.Current()`; close `gate`; return `&LockedError{}` filled with `id, pid` when `ok`. Under `gate`, a held `lock` always has its holder's `current` published.
    - Any other error: close `gate`, return `fmt.Errorf("run lock: %w", err)`.
  - Success returns `&Lock{s, gate, f}` with `gate` still held.
- **`func (l *Lock) Publish()`** (#23-1, #23-2): closes `l.gate` (dropping it) and sets it to nil. Called right after the driver's `SetCurrent`. Nil-safe.
- **`func (l *Lock) Release(runID string, pid int) error`** (#23-3, #15-1):
  - Returns nil when `l == nil` or `l.f == nil`.
  - If `l.gate == nil`, take `gate` again with `flockFile("gate", syscall.LOCK_EX)`.
  - `l.s.ClearCurrent(runID, pid)`, then close `l.f` (dropping `lock`), then close `gate`; set both to nil. Return the first error.
  - Nil-safe and idempotent because some existing tests build a `Wiring` without preflight and some call cleanup twice. An unpublished lock (a failed preflight or resume) passes `runID == ""`, which `ClearCurrent` never matches.
- **`func (s *Store) Live() (runID string, pid int, ok bool)`** (#23-2, and the watchdog warning's four call sites):
  - `gate, err := s.flockFile("gate", syscall.LOCK_SH)`; on error return not ok. `defer gate.Close()`.
  - Open `lock` read-only and `Flock(LOCK_SH|LOCK_NB)`: success → `LOCK_UN`, close, return not ok. Any error other than `EWOULDBLOCK` → not ok.
  - `EWOULDBLOCK` → return `s.Current()`.
- `Live` must never be called by a goroutine of a process that holds its own unpublished `Lock`: the shared `gate` would wait on its own exclusive one. The plan's only such caller, `leftovers`, calls `newestProgressed` instead of `selectRun` (step 6).
- **`ClearCurrent(runID string, pid int) error`** replaces `ClearCurrent()` (`store.go:290-296`, #23-3):
  - `id, p, ok := s.Current()`. If `!ok || id != runID || p != pid`, return nil and leave the file.
  - Otherwise `os.Remove`, where `ErrNotExist` counts as nil.

### 2. `internal/core/ports.go` (modify), `internal/core/fakes_test.go`, `internal/core/land_test.go` (modify)

- `ports.go:86`: `ClearCurrent(runID string, pid int) error`, so that `*store.Store` still satisfies `core.Store` (`store_test.go:477`).
- `fakes_test.go:362` and `land_test.go:303`: take the same arguments. The fake keeps recording `"Store.ClearCurrent"` and clearing its fields. Core never calls it.
- `internal/askmcp/watchdog_test.go:40` (modify): `func (m *memStore) ClearCurrent(string, int) error { return nil }`, so its `memStore` still satisfies `core.Store` and `internal/askmcp` compiles.

### 2b. `internal/askmcp/server.go` (modify)

- `Server` gets `wg sync.WaitGroup` and `done chan struct{}` (#15-1, #23-3).
- In `Serve`, the `http.HandlerFunc` wrapper (`server.go:88`) starts with `s.wg.Add(1); defer s.wg.Done()`.
- `serveHTTP(ctx, srv, ln)` becomes `serveHTTP(ctx, srv, ln, done)`: `Serve` makes `done := make(chan struct{})`, stores it in `s.done` under `s.mu`, and the shutdown goroutine (`server.go:117-124`) `close(done)`s after `Shutdown`/`Close` returns.
- **`func (s *Server) Wait()`**: read `s.done` under `s.mu`; nil (Serve never reached the listener) → return. Otherwise `<-done`, then `s.wg.Wait()`. `srv.Close` does not wait for handlers, so the `WaitGroup` is what guarantees none is still running.

### 3. `internal/face/tui/model.go` (modify)

- `confirmStop` (`model.go:524-544`): after `m.stopping = false`, if `key.String() == "y" && m.Status != ""`, set `m.Notice = "the run has already ended"` and return without calling `m.abort`.
- Covers #15-2's derived edge: a stop prompt that was open when the run ended must not write an abort marker into a run another terminal may already have resumed.

### 4. `internal/app/wire.go` (modify)

- **`func parsePositional(fs *flag.FlagSet, args []string) ([]string, error)`**: the loop at `wire.go:158-168` moved out verbatim. `parseFlags` calls it (#14-1, #14-4).
- **`Wiring` gets `lock *store.Lock`** (#23, #15).
- **`func (w *Wiring) release()`** (#15-1, #23-3):
  - `w.lock.Release(w.Loop.RunID, w.Env.PID)`; on error print `r-loop: release run lock: %v` to `w.Env.Stderr`. `Release` removes the pointer before it drops the lock, under `gate`, so no observer sees the lock free with this run's pointer, or the lock held with it gone.
  - Then `w.lock = nil`.
- **`Execute`** (`wire.go:290-301`, #15-1, #15-3): the tail becomes `TUI.Stop` (when cancelled) → `w.Dog.Stop()` → `cancel(nil)` → `w.Ask.Wait()` → `w.release()` → `w.Face.Close()`. The old `ClearCurrent` after `Face.Close` is deleted. Release comes after `Dog.Stop` and `Ask.Wait` because both the watchdog and an in-flight tool handler can still append to the run. It comes before `Face.Close` because that is where the TUI waits for `q`, and `Face.Close` still prints the halt lines and `report: …` after the TUI closes.

### 5. `internal/app/preflight.go` (modify)

- **`Preflight`** gets a named result `(err error)` (#23-1, #23-2, #23-4, #14-2).
- The block at `preflight.go:48-56` is replaced with the following, placed right after `Reachable`:
  ```go
  lock, lerr := w.Store.Lock()
  var live *store.LockedError
  if errors.As(lerr, &live) {
      return exit(4, "%v; use r-loop status, resume or abort", live)
  }
  if lerr != nil {
      return exit(2, "%v", lerr)
  }
  w.lock = lock
  defer func() {
      if err != nil {
          lock.Release("", 0)
          w.lock = nil
      }
  }()
  if id, pid, ok := w.Store.Current(); ok {
      if err := w.Store.ClearCurrent(id, pid); err != nil {
          return exit(2, "%v", err)
      }
      fmt.Fprintf(env.Stdout, "cleared run %s: pid %d is gone\n", id, pid)
  }
  ```
  The rest (`clean`, `leftovers`, `HeadBranch`, `Create`, `SetCurrent`, `bind`) is unchanged, except that `w.lock.Publish()` follows the `SetCurrent` at `preflight.go:75`. `Create` now runs only after the lock is held. A failed preflight's `Release("", 0)` leaves the new run's pointer if `SetCurrent` succeeded and a later step failed; that pointer is harmless because the lock is free, and the next start clears it.
- Delete `alive` (`preflight.go:386-392`) and drop the `syscall` import if it becomes unused.

### 6. `internal/app/status.go` (modify)

- **`var runIDRe = regexp.MustCompile(`^\d{8}-\d{6}(-\d+)?$`)`** (#14-1).
- **`func selectRun(st *store.Store, id string, stderr io.Writer) (string, error)`** replaces `runToShow` (`status.go:67-85`, #14-1, #14-2, #14-3, #14-4):
  - **Named ID.** If `id != ""`: `!runIDRe.MatchString(id)`, or `os.Stat(st.Dir(id))` failing or not being a directory, returns `exit(2, "no run %s in .r-loop/runs", id)`. Otherwise return `id`. A named run is taken even without progress.
  - **Live run.** If `cur, _, ok := st.Live(); ok`, return `cur`.
  - Otherwise return `newestProgressed(st, stderr)`.
- **`func newestProgressed(st *store.Store, stderr io.Writer) (string, error)`**: read the runs dir; `ErrNotExist` returns `""`. Take the directory entries, sort them newest-first with `runOrder` (keep `runOrder` and `newerRun`; `newerRun` is used by the sort comparator), and for each:
    - `run, err := st.Load(name)`;
    - `errors.Is(err, store.ErrMeta)`: `fmt.Fprintf(stderr, "r-loop: skipped run %s: %v\n", name, err)` and continue;
    - any other `err != nil`: return `name`, so the caller's own `Load` reports the file and line (#5-3);
    - `progressed(run)`: return `name`.
  - Nothing matched: return `""`.
- **`func progressed(run core.RunState) bool { return run.Status != core.RunCreated || len(run.Steps) > 0 }`**.
- **`leftovers`** (in `internal/app/preflight.go`, `preflight.go:232`, the third caller of `runToShow`): `id, err := newestProgressed(w.Store, w.Env.Stderr)`, not `selectRun`: preflight holds its own unpublished lock there, so `Live` would wait on its own `gate`, and no other run can be live. The rest of the block is unchanged, including `exit(2, "%v", err)`.
- **`Status`** (#14-4, #23-2):
  - Parse with `parsePositional`. A parse error or more than one positional prints `usage: r-loop status [--plain] [<run-id>]` and exits 2.
  - `id, err := selectRun(st, positional-or-"", env.Stderr)`. On error, `return fail(env, err)`.
  - `deadPID` (`status.go:57-60`) becomes `liveID, _, live := st.Live(); if cur, pid, ok := st.Current(); ok && cur == id && !(live && liveID == id) { deadPID = pid }`.

### 7. `internal/app/resume.go` (modify)

- **`PrepareResume`** (#14-1, #14-2, #14-3, #23-2, #23-4, #5-4) gets named results `(w *Wiring, opts core.RunOptions, err error)`. The flow:
  1. Parse with `parsePositional`. A parse error or more than one positional returns `exit(2, "usage: r-loop resume [--replan] [--unattended] [--yes] [--plain] [<run-id>]")`. The old usage stays a prefix, so `TestResumeUsageNamesEveryFlag` stays green.
  2. `gitrepo.Open`, then `st := store.New(root)`.
  3. `id, err := selectRun(st, positional-or-"", env.Stderr)`; return `err` as is, since it is already an `*ExitError`. `id == ""` → `exit(2, "nothing to resume: no run")`.
  4. `lock, lerr := st.Lock()`.
     - `errors.As(lerr, &live)` with `live *store.LockedError`: `exit(2, "%v", live)`, which reads `run <id> is live in pid <pid>`.
     - Any other error: `exit(2, "%v", lerr)`.
     - Then `defer func() { if err != nil { lock.Release("", 0) } }()`.
     - The lock is taken before any write: `stopStale`, `closeInterrupted` and `withdrawOpenQuestions` all append.
  5. `st.Load(id)`, the finished check, and `Wire`, as today (`resume.go:53-63`), each assigning to the named `err`.
  6. `w.lock = lock`, then `opts, err = w.resume(run, *replan)`.
- **`(w *Wiring) resume`**: `w.lock.Publish()` right after `w.Store.SetCurrent(id, env.PID)` (`resume.go:123`).
- **`Abort`** (`resume.go:368-371`, the watchdog warning): `id, _, ok := st.Live(); if !ok` → `exit(2, "no live run to abort")`. `Live` reads `current` under `gate`, so it names the lock holder, never a stale run.

### 8. `README.md`, `docs/task-loop-driver/tech-design.md` (modify)

- `README.md:101-102`: `r-loop status [--plain] [<run-id>]` and `r-loop resume [--replan] [--unattended] [--yes] [--plain] [<run-id>]`.
- `tech-design.md:134`: `ClearCurrent() error` becomes `ClearCurrent(runID, pid) error` (removes `current` only when it names that run and pid), matching `ports.go:86`.
- `tech-design.md:149`: after the `current` sentence, add: "`.r-loop/runs/lock` is `flock`ed exclusively by the live driver from preflight or resume until its loop returns; a run is live only while that lock is held, and `current` names it. `.r-loop/runs/gate` is held exclusively by a driver from before it takes `lock` until its `current` is published, and while it clears `current` and drops `lock`; observers read `lock` and `current` under `gate` shared".
- `tech-design.md:402-403`: "with a dead pid" becomes "while no driver holds the run lock".

### 9. Existing tests updated for the new lock rule

In each of these, "live" now means a test-held, published lock: `lock, _ := store.New(f.root).Lock()`, then the test's `SetCurrent`, then `lock.Publish()`, and `defer lock.Release("", 0)`. A bare PID no longer counts.

- `TestLiveRunIsRefusedWithExit4` (`app_test.go:150`): take the lock before `SetCurrent`.
- `TestResumeRefusedWhileTheRunIsLive` (`resume_test.go:551`): take the lock before `SetCurrent`.
- `TestStatusReadsTheCurrentRunOverTheNewest` (`status_test.go:102`): take the lock before `SetCurrent(older, 1)`.
- `TestCurrentRoundTrip` (`store_test.go:307`): `ClearCurrent("20260918-140305", 4242)` twice.
- `TestResumeUnattendedAppliesTheModeToTheResumedRun` (`unattended_test.go:77`): `w.Store.ClearCurrent()` becomes `w.release()`.
- Any other existing test in `./internal/app/` that fails only because a `Wiring` from an earlier `f.preflight`/`PrepareResume` in the same test still holds the lock gets `w.release()` right after that `Wiring` is done with. No assertion changes.

## Tests

Write these first.

### `internal/store/store_test.go`

- **`TestAppendAfterATornLastLineKeepsEveryCompleteRecord`** (#5-1, #5-2):
  - Setup: `events.jsonl` holds one complete `run running` line, then `{"Kind":"run","Ru` with no newline. `Append` a `run finished` record.
  - Expect: `Load` has no error, status `finished` and no warnings. Every line of the file unmarshals as JSON, the file has exactly two lines, and no line contains `{"Kind":"run","Ru{`.
- **`TestAppendAfterATornFirstLineTruncatesIt`** (#5-1, #5-2): the file holds only a torn prefix. After `Append`, the file is exactly the new record's line, and `Load` returns it.
- **`TestAppendAfterAnUnterminatedCompleteRecordKeepsIt`** (#5-1): the file ends in a complete record with no `\n`. After `Append`, `Load` has both records in order, and the file has two lines.
- **`TestAppendLeavesACorruptMiddleLineForLoadToReport`** (#5-3): the file holds `garbage\n{"Kind":"run","Run":"running"}\n`. After `Append`, `Load` fails, and the error contains `events.jsonl line 1`.
- **`TestCreateWritesMetaAtomicallyAndLeavesNoTempFile`** (#14-3): after `Create`, the run dir holds exactly `config.resolved.yaml`, `meta.json` and the four `.jsonl` files, and `meta.json` parses.
- **`TestWriteAtomicReplacesTheFileByRename`** (#14-3):
  - Setup: `os.WriteFile(dir/"meta.json", "old")`, then `Stat` it as `before`.
  - Act: `writeAtomic(dir, "meta.json", []byte("new"))`.
  - Expect: no error; the file reads `new`; `os.SameFile(before, after)` is false, so the target was replaced by a rename of a fully written file and never rewritten in place (a plain `os.WriteFile` keeps the inode and fails this); `dir` holds only `meta.json`, with no `.meta.json-*` temp left.
  - Second case: when `dir` does not exist, `writeAtomic` returns an error and creates nothing.
- **`TestLoadWithoutReadableMetaIsErrMeta`** (#14-3): for a run dir with no `meta.json`, and for one whose `meta.json` is `{`, `Load`'s error satisfies `errors.Is(err, ErrMeta)`.
- **`TestLockIsExclusiveAcrossStores`** (#23-1): `a := New(root).Lock()`, `SetCurrent("20260918-140305", 4242)`, `a.Publish()`. A second `New(root).Lock()` returns a `*LockedError` with `RunID "20260918-140305"`, `PID 4242`. After `a.Release("20260918-140305", 4242)`, the second succeeds and `current` is gone.
- **`TestLiveIsTrueOnlyWhileTheLockIsHeld`** (#23-2): with no lock file, `Live` is not ok. While another `Store` holds a published lock for `20260918-140305`, `Live` returns that run. After `Release("", 0)`, it is not ok, even though `SetCurrent(id, os.Getpid())` still names a live PID.
- **`TestLiveWaitsForTheHolderToPublishAndNeverReturnsAStalePointer`** (#23-1, #23-2, the stale-A/live-B case): `SetCurrent("20260918-100000", 999999)` (stale A). Store B `Lock()`s. A goroutine sleeps 300 ms, then `ClearCurrent("20260918-100000", 999999)`, `SetCurrent("20260918-110000", 4242)`, `Publish()`. `New(root).Live()` returns `"20260918-110000", 4242, true`, never A, and took at least 250 ms.
- **`TestReleaseOfAnUnpublishedLockFreesItAndLeavesCurrent`** (#23-3, #23-4): `SetCurrent("20260918-100000", 999999)`; `Lock()` then `Release("", 0)` without `Publish`; a second `Lock()` succeeds at once and `current` still reads `20260918-100000 999999`.
- **`TestALockHeldByAKilledProcessIsFreeAgain`** (#23-4):
  - Run the test binary itself (`exec.Command(os.Args[0], "-test.run=^TestLockHolderProcess$")`, env `R_LOOP_LOCK_HOLDER=<root>`). `TestLockHolderProcess` returns at once unless that env var is set. When it is set, it runs `New(root).Lock()`, `SetCurrent("20260918-140305", os.Getpid())` and `Publish()`, prints `locked\n`, and blocks forever.
  - After reading `locked`, the parent asserts `Live()` is ok and `Lock()` gives a `*LockedError`.
  - Then `Process.Kill()` and `Wait()`. Now `Live()` is not ok and `Lock()` succeeds, while `current` still names the dead holder.
- **`TestAGateHeldByAKilledProcessIsFreeAgain`** (#23-4): as above, but the child skips `Publish` (env `R_LOOP_LOCK_HOLDER_UNPUBLISHED=1`), so it dies holding `gate` too. After `Kill` + `Wait`, `Live()` returns promptly (within 1 s) not ok, and `Lock()` succeeds.
- **`TestClearCurrentLeavesAPointerToAnotherRun`** (#23-3): after `SetCurrent("b", 2)`, `ClearCurrent("a", 1)` and `ClearCurrent("b", 3)` both leave `current` as `b 2`, and `ClearCurrent("b", 2)` removes it.
- **`TestCurrentRoundTrip`** (updated, #23-3).

### `internal/askmcp/server_test.go`

- **`TestWaitReturnsOnlyAfterAnInFlightHandlerHasReturned`** (#15-1, #23-3, the single-driver invariant): a `Server` whose watchdog handler (`Handle` with a `Signal` func) blocks on `<-release` and then sets an `atomic.Bool` `finished`. Serve with a cancellable ctx, call the tool in a goroutine, wait until the handler is entered, then `cancel()` and call `Wait()` in another goroutine. `Wait` has not returned after 200 ms; after `close(release)`, it returns within 2 s, and `finished` was already true when `Wait` returned.
- **`TestWaitWithoutServeReturns`** (#15-1): `(&Server{}).Wait()` returns at once.

### `internal/face/tui/model_test.go`

- **`TestConfirmingAStopAfterTheRunEndedDoesNotAbort`** (#15-2 edge):
  - Setup: `newModel(recorded())` with a counting `m.abort`. Press ctrl+c, so the stop prompt opens. Then `Apply` a `halt` event, then press `y`.
  - Expect: `m.abort` is never called, and the view contains `the run has already ended`.

### `internal/app/lock_test.go` (new)

- **`TestALoserNamesAWinnerThatPublishesLate`** (#23-1, the delayed-winner case):
  - Setup: `lock, _ := store.New(f.root).Lock()`. A goroutine sleeps 2500 ms, then calls `SetCurrent("20260918-120000", 4242)` and `lock.Publish()`.
  - Act: `f.preflight(f.todo, "--plain")`.
  - Expect: an `*ExitError` with code 4 whose message contains `run 20260918-120000 is live in pid 4242`. Cleanup: `lock.Release("", 0)`.
- **`TestALoserNamesTheLiveRunNotAStalePointer`** (#23-1, stale A / live B): `SetCurrent("20260918-100000", 999999)` (stale A). A holder `Lock()`s; a goroutine sleeps 200 ms, then `ClearCurrent("20260918-100000", 999999)`, `SetCurrent("20260918-120000", 4242)`, `Publish()`. `f.preflight(f.todo, "--plain")` exits 4, its message contains `run 20260918-120000 is live in pid 4242` and does not contain `20260918-100000`.
- **`TestALoserWhoseWinnerGivesUpProceeds`** (#23-1): a holder `Lock()`s and a goroutine `Release("", 0)`s it after 200 ms without ever writing `current`. `f.preflight(f.todo, "--plain")` succeeds and `current` names its new run. Cleanup: `release()` on it.
- **`TestTwoConcurrentStartsOneProceedsAndTheOtherExits4NamingIt`** (#23-1, #14-2):
  - Setup: `newResumeFixture`, then `EnsureExcluded`. Two goroutines each run `Wire` + `Preflight` with `--plain`, each with its own `Env` copy and its own `Stdout`/`Stderr` buffers.
  - Expect: exactly one error is nil. The other is an `*ExitError` with code 4 whose message contains `run <winner.Loop.RunID> is live in pid`. `.r-loop/runs` holds exactly one run directory.
  - Cleanup: `winner.release()`.
- **`TestAReusedPidInCurrentDoesNotBlockANewStart`** (#23-2): after `SetCurrent("20260918-101500", os.Getpid())` with no lock held, `f.preflight(f.todo, "--plain")` succeeds, prints `cleared run 20260918-101500: pid <getpid> is gone`, and `current` names the new run.
- **`TestAReusedPidInCurrentDoesNotBlockResume`** (#23-2):
  - Setup: the first run fails implement (exit 1). Then `SetCurrent(id, os.Getpid())` with no lock held.
  - Expect: `f.resume(newSim())` returns code 0, and the run's status is `finished`.
- **`TestAbortIgnoresACurrentWhosePidIsReused`** (#23-2, watchdog warning): after `Create` + `SetCurrent(id, os.Getpid())` with no lock, `Main abort` exits 2 and `st.Aborted(id)` is false.
- **`TestAbortDuringAnotherRunsStartAbortsTheLiveRunNotAStaleOne`** (#23-2, stale A / live B): runs A and B are `Create`d; `SetCurrent(A, 999999)` (stale). A holder `Lock()`s; a goroutine sleeps 200 ms, then `ClearCurrent(A, 999999)`, `SetCurrent(B, 4242)`, `Publish()`. `Main abort` exits 0; `st.Aborted(A)` is false and `st.Aborted(B)` is true. Cleanup: `Release(B, 4242)`.
- **`TestStatusCallsTheDriverGoneWhenItsPidIsReused`** (#23-2, watchdog warning): for a seeded `running` run whose `current` holds `os.Getpid()` and no lock, the first status line is `run <id> running (driver pid <getpid> not alive — r-loop resume)`.
- **`TestAStartAfterTheLockHolderCrashedTakesTheLock`** (#23-4):
  - Setup: another `store.New(f.root).Lock()` runs, then `SetCurrent("20260918-090000", os.Getpid())`, then `Release("", 0)`, which leaves `current`. That is the state a crashed holder leaves.
  - Expect: `f.preflight` succeeds, printing the `cleared run 20260918-090000` note.
- **`TestResumeAfterTheLockHolderCrashedTakesTheLock`** (#23-4):
  - Setup: the first run fails implement. A second store `Lock()`s, `SetCurrent(id, os.Getpid())`, and `Release("", 0)`s, leaving `current`.
  - Expect: `f.resume(newSim())` returns code 0.
- **`TestAnExitingDriverLeavesAnotherRunsPointer`** (#23-3):
  - Setup: a run whose plan step hangs is `Execute`d in a goroutine. Once plan is prompted, `SetCurrent("20990101-000000", 7)` runs, then `MarkAbort(runID)`.
  - Expect: after `Execute` returns, `current` is still `20990101-000000 7` and `Live()` is not ok.
- **`TestAFinishedTUIRunReleasesTheRunBeforeWaitingForQ`** (#15-1, #15-3):
  - Setup: plan fails, and `installSignalTestDisplay` is used. `Execute` runs in a goroutine.
  - Wait up to 10 s until the run's status is `halted` and `Current()` is not ok. On the base code this times out.
  - Then: `Live()` is not ok and `Execute` has not returned.
  - Write `q`. `Execute` returns 1, and the display output contains `r-loop resume` and `report:`.
- **`TestResumeFromAnotherTerminalWhileAFinishedTUIWaitsForQ`** (#15-2):
  - Setup: as above, until the run is released.
  - Expect: `PrepareResume([]string{"--plain"}, env2)` has no error, where `env2` is `f.env` with its own buffers, and it selects the same run.
  - Then call `release()` on the resumed `Wiring` and write `q`.
- **`TestANewRunFromAnotherTerminalWhileAFinishedTUIWaitsForQ`** (#15-2):
  - Setup: as above, until the run is released.
  - Expect: `Wire` + `Preflight` with `--plain` and `env2` has no error.
  - Then call `release()` on it and write `q`.

### `internal/app/status_test.go`

- **`TestStatusShowsANamedRunOverANewerOne`** (#14-4): of two seeded runs, `status --plain <older>` prints `run <older> halted` first.
- **`TestStatusUnknownRunIDExits2NamingIt`** (#14-4): `status 20990101-000000` exits 2, and stderr contains `no run 20990101-000000`. `status ../x` also exits 2.
- **`TestStatusSkipsANewerRunThatRecordedNoProgress`** (#14-2, #14-4): with an older `halted` run and a newer run from `Create` only, `status` shows the older run.
- **`TestStatusSkipsARunWithUnreadableMetaWithAWarning`** (#14-3): with an older `halted` run and a newer one whose `meta.json` is removed, `status` exits 0, shows the older run, and stderr contains `skipped run <newer>`.
- **`TestStatusDoesNotSkipARunWithACorruptRecordLine`** (#5-3): with an older `halted` run and a newer one whose `events.jsonl` is `garbage\n` plus a valid `run running` line, `status` exits 2, and stderr contains `events.jsonl line 1`.
- **`TestStatusReadsTheCurrentRunOverTheNewest`** (updated, #23-2).

### `internal/app/resume_test.go`

- **`TestResumeANamedRunOverANewerOne`** (#14-1):
  - Setup: the first run fails implement (run A). Then a second run B is seeded, `Create` + `run halted`.
  - Expect: `f.resume(newSim(), A)` returns code 0, A ends `finished`, and B is still `halted`.
- **`TestResumeUnknownRunIDExits2NamingIt`** (#14-1): `f.resume(newSim(), "20990101-000000")` returns an `*ExitError` with code 2 whose message contains `no run 20990101-000000`.
- **`TestResumeSkipsANewerRunThatRecordedNoProgress`** (#14-2):
  - Setup: the first run fails implement (A), then `Create` only (B, newer).
  - Expect: `f.resume(newSim())` returns code 0, and A ends `finished`.
- **`TestResumeSkipsARunWithUnreadableMetaWithAWarning`** (#14-3):
  - Setup: A as above. B is newer and has `meta.json` set to `{`.
  - Expect: resume returns code 0, A ends `finished`, and `f.err` contains `skipped run <B>`.
- **`TestResumeAndStatusOfARunWithATornLastLineExitNormally`** (#5-4):
  - Setup: the first run fails implement. Then `{"Kind":"event","Ev` is appended to its `events.jsonl` with no newline.
  - Expect, in this order: first `Main status --plain` exits 0 while the torn tail is still on disk (checked by reading the file's last byte, which is not `\n`); then `f.resume(newSim())` returns code 0 with no error, and the run's status is `finished`.
- **`TestResumeRefusedWhileTheRunIsLive`** (updated, #23).

### `internal/app/app_test.go`

- **`TestLiveRunIsRefusedWithExit4`** (updated, #23-1).

## Left out

- **`fcntl`/`O_EXCL` locks.** Rejected under Choices. `flock` meets every criterion.
- **Retry loops and time bounds on `Lock`/`Live`.** `gate` serialises starters and observers, so neither a probe nor a slow winner can make `Lock` fail spuriously or name the wrong run; the waits are bounded by the holder's own git/herdr per-call timeouts.
- **Adding `Lock`/`Live` to the `core.Store` port.** Core never locks or probes, and only `internal/app` uses them.
- **Recording a final status in `Execute` for starts that fail before the loop** (watchdog didn't start, ask server failed). No criterion needs it: such a run stays `created` and is excluded by the progress rule (#14-2). Every path that reaches the loop already records its final status before `w.run` returns (`internal/core/loop.go:182,187,753,1034`, `internal/app/unblock.go:35,154`, `internal/app/triage.go:115,141`).
- **A `.torn` side file, or a warning after repair.** "At most with a warning" makes the warning optional. A truncated prefix is a record whose `Append` never returned.
- **Rewording `driver pid N not alive`.** Its meaning (the recorded driver is gone) still holds under the lock rule. `tech-design.md` and the existing status test pin that text.
- **Repairing files that an older build already corrupted** (a torn prefix merged with a record). No criterion asks for it, and `Load` still reports them by file and line.
- **A `--run` flag.** The criteria ask for a positional run ID.

## Assumptions

- **No residual naming race.** Every reader of `lock` + `current` holds `gate`, and a driver holds `gate` from before `lock` until `Publish`, so a stale pointer is never read next to another driver's lock. A winner that fails before `Publish` releases both, and the waiting loser takes the lock and proceeds: of two starts, exactly one proceeds.
- **`status` and `abort` may wait.** While a driver is between `Lock` and `Publish` (its preflight or resume preparation), `status`, `abort` and a second start block on `gate` until it publishes or gives up. That is seconds, bounded by the driver's per-call git/herdr timeouts. Exactly one start proceeds in every case.
- **Watchdog warning 1 (the lock vs. a finished TUI).** Resolved. The lock is not held for the process's life: `Wiring.release` drops it together with the pointer, after `Dog.Stop` and before `Face.Close` waits for `q`.
- **Watchdog warning 2 (four liveness sites).** Resolved. Preflight and resume take the lock. Abort (`resume.go:368`) and status (`status.go:58`) call `Store.Live()`. `alive()` is deleted, so no caller can keep trusting a PID.
- **Children never hold the lock.** `os.OpenFile` sets `O_CLOEXEC`, so gate commands, git and agent processes do not inherit the lock fd. A crashed driver's leftover children cannot keep the lock.
- **Filesystem.** `flock` is honoured on the local filesystem that holds the repository (macOS APFS, Linux ext4/xfs). Network filesystems are out of scope.
- **Messages.** A live run refuses `resume` with exit 2 and a new start with exit 4, as today. Only the rule for "live" changes.
- **Children never hold `gate` either.** Both files are opened with `os.OpenFile`, which sets `O_CLOEXEC`.
- **Explicit ID.** A named run ID must match `^\d{8}-\d{6}(-\d+)?$` and exist as a directory. A named run with unreadable `meta.json` fails with the `Load` error (exit 2) rather than being skipped, because the maintainer asked for it by name.
- **Selection for `status` and `resume`.** A live run always wins. `resume` then refuses it as live.

## Gate

`go test ./internal/store/ ./internal/askmcp/ ./internal/face/tui/ ./internal/app/ -run '^(TestAppendAfterATornLastLineKeepsEveryCompleteRecord|TestAppendAfterATornFirstLineTruncatesIt|TestAppendAfterAnUnterminatedCompleteRecordKeepsIt|TestAppendLeavesACorruptMiddleLineForLoadToReport|TestCreateWritesMetaAtomicallyAndLeavesNoTempFile|TestWriteAtomicReplacesTheFileByRename|TestLoadWithoutReadableMetaIsErrMeta|TestLockIsExclusiveAcrossStores|TestLiveIsTrueOnlyWhileTheLockIsHeld|TestLiveWaitsForTheHolderToPublishAndNeverReturnsAStalePointer|TestReleaseOfAnUnpublishedLockFreesItAndLeavesCurrent|TestALockHeldByAKilledProcessIsFreeAgain|TestAGateHeldByAKilledProcessIsFreeAgain|TestWaitReturnsOnlyAfterAnInFlightHandlerHasReturned|TestWaitWithoutServeReturns|TestClearCurrentLeavesAPointerToAnotherRun|TestCurrentRoundTrip|TestConfirmingAStopAfterTheRunEndedDoesNotAbort|TestALoserNamesAWinnerThatPublishesLate|TestALoserNamesTheLiveRunNotAStalePointer|TestALoserWhoseWinnerGivesUpProceeds|TestTwoConcurrentStartsOneProceedsAndTheOtherExits4NamingIt|TestAReusedPidInCurrentDoesNotBlockANewStart|TestAReusedPidInCurrentDoesNotBlockResume|TestAbortIgnoresACurrentWhosePidIsReused|TestAbortDuringAnotherRunsStartAbortsTheLiveRunNotAStaleOne|TestStatusCallsTheDriverGoneWhenItsPidIsReused|TestAStartAfterTheLockHolderCrashedTakesTheLock|TestResumeAfterTheLockHolderCrashedTakesTheLock|TestAnExitingDriverLeavesAnotherRunsPointer|TestAFinishedTUIRunReleasesTheRunBeforeWaitingForQ|TestResumeFromAnotherTerminalWhileAFinishedTUIWaitsForQ|TestANewRunFromAnotherTerminalWhileAFinishedTUIWaitsForQ|TestStatusShowsANamedRunOverANewerOne|TestStatusUnknownRunIDExits2NamingIt|TestStatusSkipsANewerRunThatRecordedNoProgress|TestStatusSkipsARunWithUnreadableMetaWithAWarning|TestStatusDoesNotSkipARunWithACorruptRecordLine|TestStatusReadsTheCurrentRunOverTheNewest|TestResumeANamedRunOverANewerOne|TestResumeUnknownRunIDExits2NamingIt|TestResumeSkipsANewerRunThatRecordedNoProgress|TestResumeSkipsARunWithUnreadableMetaWithAWarning|TestResumeAndStatusOfARunWithATornLastLineExitNormally|TestResumeRefusedWhileTheRunIsLive|TestLiveRunIsRefusedWithExit4)$'`
