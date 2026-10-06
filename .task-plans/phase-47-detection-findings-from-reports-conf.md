status: planned

## Summary

This phase builds the half of ADR-89 that runs no analyzer:

- the `core.Analyzer` port and its fake;
- a new adapter package `internal/analyze` that detects modules, reads the changed lines of a tree, maps changed files to modules, and turns SARIF and PMD XML reports into `core.Finding`s;
- the `analyze.enabled` and `analyze.timeout` config keys;
- a preflight check that names a detected language's missing toolchain and prints a `static:` line.

Phase 48 builds the runner (`analyze.New`, `run.go`) on top of these pieces. Phase 49 wires the runner into `ReviewHalf`.

Choices:

- **Where report parsing meets the filter:** parsers return a neutral `Hit`, and one `Findings(dir, Changes, hits)` filters, sorts, numbers and caps them. The alternative was parsers that emit `core.Finding` directly. That would duplicate the filter, the numbering and the cap in both parsers and would break the cap across reports, since the 50 apply to the whole analysis and not to one report.
- **How the tool is named:** the caller passes the tool name to `ParseSARIF`. The alternative was reading `runs[].tool.driver.name`. The driver name is unreliable (Semgrep's is `Semgrep OSS`, SpotBugs' is `SpotBugs`), and the govulncheck exception must key on a name the driver controls.
- **What touches a module:** a changed `.go` file, or a Go module's own `go.mod`/`go.sum`, touches the nearest Go module. A `.java` file touches the nearest Maven or Gradle module. Nothing else touches a module. The alternative was the item's literal ".go/.java only". That would never run govulncheck on a dependency-only change, which ADR-89 requires. The watchdog settled this as q1, citing `spec.html:3898`.
- **What `Touched` returns:** each touched module with its changed files (`Touch{Module, Files}`). The alternative was a bare `[]Module`. Phase 48 needs the files to decide govulncheck (`go.mod`/`go.sum` changed) and to give Semgrep its file list.
- **Which hits survive the cap:** hits are sorted by severity first (error, warning, note, other), then by path, line, tool, rule and message. The alternative was path first. The 50 kept findings should be the most severe ones, and a total order keeps the ids stable.
- **Where the preflight check lives:** in `Wiring.checks()` (`internal/app/preflight.go:158`). The alternative was `Preflight` alone. `checks()` is also what `resume` runs (`internal/app/resume.go:415`), so a resumed run gets the same toolchain check and the same `static:` line.
- **Dry run:** the missing-toolchain exit is skipped under `--dry-run`, but the `static:` line is still printed. The alternative was to exit in dry run too. Every other binary check is skipped in dry run: herdr at `preflight.go:161` and provider binaries at `preflight.go:297`, pinned by `TestAMissingProviderBinaryIsNotCheckedInDryRun` (`internal/app/app_test.go:123`).
- **How a toolchain is checked:** `exec.LookPath(m.Runner)` for every module, plus `java` for Maven and Gradle. A wrapper's `Runner` is its absolute path, so `LookPath` checks that it is executable. The alternative was a `mvnw`-specific stat. `LookPath` already covers both bare names and paths.
- **How the boolean key is parsed:** a new `resolver.boolean` getter built on `numeric` (`internal/config/reader.go:249`), accepting exactly `true` or `false`. The alternative was `strconv.ParseBool`, which also takes `1`, `t` and `F`. The config reader is strict everywhere else (`durationFrom`, `reader.go:278`).

## Why this approach

The phase proposes the port signature, `Detect` with its skip list, the changed set from `git diff -U0 HEAD` plus untracked files, the finding format and the cap, two config keys, and the preflight check. The plan follows all of it, with four refinements.

1. **The touch rule includes `go.mod`/`go.sum`.** Item 3 reads ".go or .java". ADR-89 (`docs/task-loop-driver/spec.html:3898`) runs govulncheck "when go.mod or go.sum changed", and the literal item would make that dead for a dependency bump. The watchdog answered q1 with option A: a Go module's own `go.mod`/`go.sum` touches it, and `Touched` returns the files so phase 48 can see them.

2. **The diff is run so that user git config cannot break the parser.** A user's `~/.gitconfig` can set `diff.noprefix`, `diff.mnemonicPrefix`, `color.diff=always`, `diff.relative`, `diff.renames` or an external diff driver. Any of these would break a naive `+++ b/` parser, so the diff command overrides each one explicitly. `--no-renames` also makes a moved file (`git mv`, which with rename detection prints `rename to` and no `+++` line or hunk) a new file whose every line is added, so it touches its module and is filtered like an untracked file. Paths with spaces get a trailing TAB on the `+++` line, and non-ASCII paths are C-quoted; both were verified against git 2.50. They are unquoted with `strconv.Unquote`.

   `internal/gitrepo` has no diff-hunk parser to reuse. Its `ChangedFiles` (`internal/gitrepo/repo.go:346`) returns names only, and adding a method there would touch a file outside `Files:`. `analyze` therefore runs its two git commands itself with `exec.CommandContext`, following `runOnce` (`internal/gitrepo/repo.go:87`): `-C dir` and `GIT_TERMINAL_PROMPT=0`.

3. **The `static:` line has a value when no language is detected.** The item's format `static: <languages>[, semgrep]` has no value for an empty language list. A repository with no markers is the common case in `internal/app` tests, and a Kotlin-only repository is named by ADR-89's Consequences. The plan prints `static: semgrep` or `static: none (semgrep not installed)` there.

4. **`Analysis` lives in `ports.go`.** `ports.go` is in `Files:` and `types.go` is not, so `Analysis` is declared beside its port.

No file outside `Files:` changes.

`config.Banner` (`internal/config/banner.go`) is not touched. The keys' provenance lands in `LoopConfig.Provenance`, which is what the item asks for, and the preflight prints the `static:` line.

## Changes

### 1. `internal/core/ports.go` — modify

Serves item 1.

Append the port and its result type after `Notifier` (`ports.go:128`). `context` is already imported (`ports.go:4`).

```go
type Analysis struct {
	Findings []Finding
	Command  string
}

type Analyzer interface {
	Analyze(ctx context.Context, dir string) (Analysis, error)
}
```

`Finding` is the existing type at `internal/core/evidence.go:278` (`ID`, `Title`, `Detail`, `Files`). `Command` is the comma-separated list of tools that ran, such as `golangci-lint, semgrep`. Phase 48 fills it in.

### 2. `internal/core/fakes_test.go` — modify

Serves item 1.

Add a fake that follows the `callLog` pattern (`fakes_test.go:16`). Its results and errors are scripted per call, because phase 49's retry needs an error and then a success.

```go
type fakeAnalyzer struct {
	callLog
	mu      sync.Mutex
	Results []Analysis
	Errs    []error
	n       int
}

func (f *fakeAnalyzer) Analyze(ctx context.Context, dir string) (Analysis, error)
```

`Analyze` behaves as follows:

1. Record `Analyzer.Analyze <dir>`.
2. Under `mu`, take call index `i := f.n` and then do `f.n++`.
3. Return `Results[min(i, len(Results)-1)]`, or the zero `Analysis` when `Results` is empty.
4. Return `Errs[min(i, len(Errs)-1)]` as the error, or nil when `Errs` is empty.

Add `_ Analyzer = (*fakeAnalyzer)(nil)` to the assertion block at `fakes_test.go:539`.

### 3. `internal/analyze/detect.go` — new

Serves items 2, 3 and 6. Package `analyze`, which imports only the standard library.

```go
const (
	Go     = "go"
	Maven  = "maven"
	Gradle = "gradle"
)

type Module struct {
	Kind, Dir, BuildRoot, Runner string
}

func Detect(root string) ([]Module, error)

type Touch struct {
	Module Module
	Files  []string
}

func Touched(mods []Module, dir string, files []string) []Touch
```

**`Detect`.** It runs `filepath.WalkDir(root, …)` and returns the walk's error unchanged. A missing root or an unreadable directory is an error.

During the walk:

- A directory other than `root` whose name is `.git`, `.r-loop`, `vendor`, `node_modules` or `testdata` returns `filepath.SkipDir`.
- Files named `go.mod`, `pom.xml`, `build.gradle`, `build.gradle.kts`, `settings.gradle` and `settings.gradle.kts` are recorded in per-directory sets: `goDirs`, `pomDirs`, `gradleDirs` and `settingsDirs`. Symlinks are not followed; that is WalkDir's default.

After the walk, one module is built per (directory, kind). Paths are absolute, joined from `root`.

| Kind | `BuildRoot` | `Runner` |
|---|---|---|
| `Go` | the module's own `Dir` | `"go"` |
| `Maven` | the outermost directory in `pomDirs` that is the module's `Dir` or an ancestor of it, walking up and stopping at `root` | `filepath.Join(BuildRoot, "mvnw")` when that path is a regular file (`os.Stat`, `Mode().IsRegular()`), else `"mvn"` |
| `Gradle` | the outermost directory in `settingsDirs` that is the module's `Dir` or an ancestor of it, up to `root`; the module's `Dir` when there is none | `gradlew` at the build root by the same rule, else `"gradle"` |

A directory with both `build.gradle` and `build.gradle.kts` gives one Gradle module.

The result is sorted by `Dir`, then by kind in the order `Go`, `Maven`, `Gradle`. A tree with no markers returns an empty, non-nil slice.

**`Touched`.** `files` are slash-separated paths relative to `dir`. For each file:

1. Its language is `Go` when the extension is `.go` or the base name is `go.mod` or `go.sum`. It is Java when the extension is `.java`. Any other file is ignored.
2. `abs := filepath.Join(dir, filepath.FromSlash(f))`.
3. Its module is the module of a matching kind whose `Dir` contains `abs` and is the longest such `Dir`. A Go file matches `Go`; a Java file matches `Maven` or `Gradle`. On a tie (the same `Dir`), the first module in `mods` order wins.
4. "Contains" means `filepath.Rel(m.Dir, abs)` gives no error, is not `..`, and does not start with `../`.
5. A file with no matching module is ignored.

The result holds one `Touch` per touched module, in `mods` order. Each `Files` list keeps the input order, and the input order is sorted (see `Changes.Files`). When nothing is touched the result is an empty, non-nil slice, so no language toolchain runs.

### 4. `internal/analyze/lines.go` — new

Serves items 3 and 4.

```go
type Changes struct {
	Lines     map[string]map[int]bool
	Untracked map[string]bool
}

func Changed(ctx context.Context, dir string) (Changes, error)
func (c Changes) Files() []string
func (c Changes) Has(path string, line int) bool
```

**`Changed`** runs two git commands with `exec.CommandContext(ctx, "git", "-C", dir, …)`. The environment is `os.Environ()` plus `GIT_TERMINAL_PROMPT=0`.

The first command is:

```
git -C dir -c core.quotePath=false diff -U0 --no-color --no-ext-diff --no-renames --src-prefix=a/ --dst-prefix=b/ --relative HEAD
```

`--relative` makes the paths relative to `dir`, which is how `ls-files` prints them, and it overrides `diff.relative`.

The second command is:

```
git -C dir ls-files --others --exclude-standard -z
```

Its output is split on NUL and empty entries are dropped.

A command that fails returns `fmt.Errorf("git %s: %v: %s", <"diff" or "ls-files">, err, strings.TrimSpace(stderr))`.

**Parsing the diff.** The output is read line by line with a state flag `inHeader`:

- A line starting `diff --git ` sets `inHeader = true` and clears the current path.
- While `inHeader` is true, a line starting `+++ ` gives the new path:
  1. Take the text after `+++ ` and trim one trailing `\t`.
  2. If it starts with `"`, apply `strconv.Unquote`; an unquote error is returned as an error naming the line.
  3. `/dev/null` (a deleted file) leaves the path empty.
  4. Otherwise strip the `b/` prefix, set the path, and create `Lines[path]` as an empty set.
- A line starting `@@` sets `inHeader = false` and is matched against `^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`. The text after the closing `@@` is ignored. With start `c` and count `d` (default 1), lines `c … c+d-1` are added to `Lines[path]`. `d = 0` adds nothing, and a hunk while the current path is empty (after `+++ /dev/null`) adds nothing.
- Every other line is ignored, so a content line `+++ x` is never read as a header.

A file with only deleted lines therefore appears in `Lines` with an empty set, which touches its module. A deleted file and a binary file do not appear. Because of `--no-renames`, a renamed file appears under its new path with every line added, and its old path is a deleted file.

**`Files`** returns the sorted, de-duplicated union of `Lines` keys and `Untracked` keys.

**`Has`** returns `Untracked[path] || Lines[path][line]`.

### 5. `internal/analyze/sarif.go` — new

Serves item 4.

```go
type Hit struct {
	Tool, Rule, Level, Message, Path string
	Line                             int
}

func ParseSARIF(tool, base string, data []byte) ([]Hit, error)
func Findings(dir string, ch Changes, hits []Hit) []core.Finding
```

This imports `r-loop/internal/core` for `core.Finding`, as every adapter does (for example `internal/gitrepo/repo.go:21`).

**`ParseSARIF`** decodes into minimal structs. It reads:

- `runs[].originalUriBaseIds` (map of id to `{uri}`);
- `runs[].artifacts[]` (`location.{uri, uriBaseId}`);
- `runs[].tool.driver.rules[]` (`id`, `defaultConfiguration.level`, `messageStrings` as a map of id to `{text}`);
- `runs[].tool.driver.globalMessageStrings` (map of id to `{text}`);
- `runs[].results[]`: `ruleId`, `ruleIndex` (a `*int`), `rule.id`, `rule.index` (a `*int`), `level`, `message.text`, `message.markdown`, `message.id`, `message.arguments`, and `locations[0].physicalLocation.{artifactLocation.{uri, uriBaseId, index}, region.startLine}`, where `index` is a `*int`.

A JSON error returns `fmt.Errorf("%s: sarif: %w", tool, err)`.

Each result becomes one `Hit` with `Tool: tool`, filled in as follows:

| Field | First value that is set |
|---|---|
| `Rule` | `ruleId`, then `rule.id`, then the `id` of the driver rule at `ruleIndex` (then `rule.index`) when that index is within `rules`, then `"unknown"` |
| `Level` | the result's `level`, then the `defaultConfiguration.level` of the result's driver rule, then `"warning"` (SARIF's default) |
| `Message` | `message.text`, then `message.markdown`, then the text that `message.id` names with its arguments substituted, then `"(no message)"` |

The result's driver rule is the one at `ruleIndex` (then `rule.index`) when that index is within `rules`, else the first rule whose `id` equals `Rule`, else none.

The text that `message.id` names is `messageStrings[id].text` of the result's driver rule, else `globalMessageStrings[id].text`. Its placeholders are replaced with one `strings.NewReplacer` built from the pairs `{{`→`{`, `}}`→`}`, then `{<i>}`→`arguments[i]` for each argument, so a doubled brace stays literal (SARIF §3.11.5). SpotBugs writes its messages this way (`message.id` `default` and `arguments`, the text in the rule's `messageStrings`).
| `Line` | `region.startLine`, else `0` |

`Path` is resolved like this:

1. With no location, `Path` is `""`. An `artifactLocation` with an empty `uri` and an `index` within `artifacts` takes `uri` and `uriBaseId` from `artifacts[index].location` (SARIF §3.4.5); with an empty `uri` and no usable index, `Path` is `""`.
2. A `uri` starting `file:` is parsed with `url.Parse`, and `Path` is its `Path`.
3. Otherwise the uri is percent-decoded with `url.PathUnescape` (on error, the raw uri is used).
4. If `uriBaseId` names an entry of `originalUriBaseIds` whose `uri` is a `file:` URI, the decoded uri is joined onto that path.
5. Otherwise it is joined onto `base`, unless it is already absolute.

**`Findings`.** Starting from `hits`:

1. **Relative path.** `rel` is `filepath.ToSlash(filepath.Rel(dir, h.Path))` when `h.Path` is not empty and `Rel` succeeds. Otherwise `rel` is `filepath.ToSlash(h.Path)`.
2. **Keep or drop.** A hit is kept when `h.Tool == "govulncheck"` or `ch.Has(rel, h.Line)`.
3. **Sort.** Kept hits are sorted stably by severity rank (`error` 0, `warning` 1, `note` 2, anything else 3), then `rel`, `Line`, `Tool`, `Rule`, `Message`.
4. **First 50.** The first `maxFindings = 50` hits become findings:
   - `ID` is `s<i>` with `i` from 1.
   - `Title` is `<tool>/<rule>: <first line>`. The first line is the message up to its first `\n`, with `\r` and surrounding spaces trimmed.
   - `Detail` is `<level>\n<full message>`, plus `\n<rel>:<line>` when `rel != ""` and `Line > 0`, or `\n<rel>` when `rel != ""` and `Line == 0`.
   - `Files` is `[]string{rel}`, or `[]string{}` when `rel == ""`. It must not be nil: `parseFindings` requires a list (`internal/core/evidence.go:293`).
5. **Overflow.** When more than 50 hits are kept, one more finding follows:
   - `ID` is `s51`.
   - `Title` is `static: <n> more findings`.
   - `Detail` has one line per `<tool>/<rule>: <count>`, sorted by count descending, then by name ascending, joined with `\n`.
   - `Files` is the sorted, distinct non-empty `rel` paths of the overflow.
6. **No hits.** The result is `[]core.Finding{}`, never nil.

### 6. `internal/analyze/pmdxml.go` — new

Serves item 4.

```go
func ParsePMD(base string, data []byte) ([]Hit, error)
```

The report decodes into:

```go
struct {
	XMLName xml.Name `xml:"pmd"`
	Files []struct {
		Name       string `xml:"name,attr"`
		Violations []struct {
			BeginLine int    `xml:"beginline,attr"`
			Rule      string `xml:"rule,attr"`
			Priority  int    `xml:"priority,attr"`
			Text      string `xml:",chardata"`
		} `xml:"violation"`
	} `xml:"file"`
}
```

Local names match any namespace, which covers PMD's `http://pmd.sourceforge.net/report/2.0.0`. A decode error, including a root other than `<pmd>`, returns `fmt.Errorf("pmd: xml: %w", err)`. `<error>` elements, which are PMD processing errors, are not decoded.

Each violation becomes a `Hit`:

- `Tool` is `"pmd"`.
- `Rule` is the `rule` attribute.
- `Level` comes from the priority: 1 and 2 give `error`, 3 gives `warning`, 4 and 5 give `note`, and anything else gives `warning`.
- `Message` is `strings.TrimSpace(Text)`.
- `Path` is `Name` when it is absolute, else `filepath.Join(base, Name)`.
- `Line` is `BeginLine`.

### 7. `internal/config/reader.go` — modify

Serves item 5.

- Add the type and the field. The field goes in `LoopConfig` after `Land` (`reader.go:85`).
  ```go
  type Analyze struct {
  	Enabled bool
  	Timeout time.Duration
  }
  ```
  ```go
  Analyze Analyze
  ```
- Add `"analyze": schema{"enabled": nil, "timeout": nil}` to `topSchema` (`reader.go:122`). An unknown key under `analyze` is then rejected by `walk` (`reader.go:200`) with `file:line: unknown key "analyze.<k>"`. `Wire` turns any config error into exit 2 (`internal/app/wire.go:478`).
- Add the getter:
  ```go
  func (r *resolver) boolean(path string) (bool, error)
  ```
  It calls `r.numeric(path)` (`reader.go:249`), so a null value falls through to the next layer and provenance is recorded. It returns `true` for `"true"` and `false` for `"false"`. Any other value returns `errAt(l.file, n, "%s: %q is not true or false", path, v)`.
- In `sections` (`reader.go:625`), after `land.gateIdle`, set `cfg.Analyze.Enabled` from `r.boolean("analyze.enabled")` and `cfg.Analyze.Timeout` from `r.duration("analyze.timeout")`. `duration` rejects zero and negative values through `durationFrom` (`reader.go:278`).

### 8. `internal/config/defaults.yaml` — modify

Serves item 5 and the `Done when:` grep.

Insert this block between `land:` (`defaults.yaml:66`) and `watchdog:`, as in spec.html's config listing:

```yaml
analyze:
  enabled: true
  timeout: 15m
```

### 9. `internal/app/preflight.go` — modify

Serves item 6.

Import `r-loop/internal/analyze`. At the end of `checks()` (`preflight.go:158`), after `promptSources`, do:

```go
if w.Config.Analyze.Enabled {
	line, err := w.static()
	if err != nil {
		return nil, nil, err
	}
	prompts = append(prompts, line)
}
```

`banner` (`preflight.go:383`) prints the `prompts` lines in order. The line therefore appears in a live run, in a dry run and on resume (`internal/app/resume.go:415`).

```go
func (w *Wiring) static() (string, error)
```

1. **Detect.** `mods, err := analyze.Detect(w.Repo.Root())`. An error returns `exit(2, "analyze: %v", err)`.
2. **Kinds.** Collect the distinct kinds of `mods` in the order `go`, `maven`, `gradle`.
3. **Toolchains.** Unless `w.Opts.DryRun`, check each module in `mods` order. `rel` is `filepath.Rel(root, m.Dir)` in slash form, `.` for the root.
   1. `exec.LookPath(m.Runner)`. On error:
      - for a bare runner (no path separator): `exit(2, "analyze: %s module %s needs %s, not found on PATH; install it or set analyze.enabled: false", m.Kind, rel, m.Runner)`;
      - for a wrapper path: `exit(2, "analyze: %s module %s needs %s, which is not executable; chmod +x it or set analyze.enabled: false", m.Kind, rel, <wrapper path relative to root>)`.
   2. For `maven` and `gradle`, also `exec.LookPath("java")`. On error: `exit(2, "analyze: %s module %s needs java, not found on PATH; install it or set analyze.enabled: false", m.Kind, rel)`.
   3. The first failure returns.
4. **Semgrep.** `_, serr := exec.LookPath("semgrep")`.
5. **The line.** `langs` is the kinds joined with `, `.

   | Kinds | Semgrep found | Line |
   |---|---|---|
   | some | yes | `static: <langs>, semgrep` |
   | none | yes | `static: semgrep` |
   | some | no | `static: <langs> (semgrep not installed)` |
   | none | no | `static: none (semgrep not installed)` |

When `analyze.enabled` is false nothing is detected, checked or printed.

### 10. `README.md` — modify

Serves item 5.

- Add an `### analyze` section between `### land` (`README.md:244`) and `### watchdog`. It opens with one paragraph:

  > The `static` reviewer runs the project's own analyzers in every review round of `implement` and `gatefix`. The analyzers need no install: the project's toolchain fetches golangci-lint, govulncheck, PMD, SpotBugs and find-sec-bugs at versions pinned in r-loop and caches them. Semgrep runs too when it is on `PATH`. Preflight prints `static: <languages>` and exits 2 when a detected language's toolchain is missing: `go`, or the Maven or Gradle runner and `java`.

  Then a Key | Default | Meaning table:
  - `enabled` | `true` | `false` turns the `static` reviewer off.
  - `timeout` | `15m` | Time limit for one round's analysis, all languages and a Java compile included.
- Change the exit-code row `2` (`README.md:161`) to: `Bad usage, bad config or plan, a git state problem, or a detected language's toolchain missing for static analysis.`

## Tests

Write these first. Every test has `// given`, `// when` and `// then` blocks.

### `internal/analyze/detect_test.go`

Package `analyze`. Helpers:

- `touch(t, root, rels...)` creates empty files, making the parent directories.
- `executable(t, root, rel)` writes `#!/bin/sh\n` with mode `0o755`.

Every expected `Module` is a hard-coded literal built with `filepath.Join(root, "…")`.

- `TestDetectFindsAGoModuleInEachGoModDirectory`. Given `go.mod` and `tools/go.mod`. `Detect` returns `[{go, root, root, "go"}, {go, root/tools, root/tools, "go"}]`. Covers item 2 (Go).
- `TestDetectGivesEachMavenModuleTheOutermostPomAndItsWrapper`. Given `pom.xml`, executable `mvnw`, `api/pom.xml` and `api/core/pom.xml`. Detect returns three `maven` modules (root, `api`, `api/core`), each with `BuildRoot` root and `Runner` `root/mvnw`. Covers item 2 (multi-module Maven with `mvnw`).
- `TestDetectRunsMavenFromPathWithoutAWrapper`. Given `pom.xml` and `api/pom.xml`, with no `mvnw`. Both modules have `Runner` `"mvn"` and `BuildRoot` root. Covers item 2 (Maven without `mvnw`).
- `TestDetectGivesEachGradleModuleTheOutermostSettingsDirectoryAndItsWrapper`. Given `settings.gradle.kts`, executable `gradlew`, `app/build.gradle.kts` and `lib/build.gradle`. Detect returns `gradle` modules `app` and `lib` only, each with `BuildRoot` root and `Runner` `root/gradlew`. Covers item 2 (Gradle).
- `TestDetectFallsBackToTheModuleDirectoryAndGradleOnPath`. Given `svc/build.gradle` with no settings file and no wrapper. Detect returns `{gradle, root/svc, root/svc, "gradle"}`. Covers the Gradle build-root fallback.
- `TestDetectCountsAModuleWithBothGradleScriptsOnce`. Given `build.gradle` and `build.gradle.kts` in the same directory. Detect returns one `gradle` module. Covers the dedupe.
- `TestDetectSkipsDirectoriesThatHoldNoProjectModules`. A table with one row each for `.git`, `.r-loop`, `vendor`, `node_modules` and `testdata`. Each row has root `go.mod` plus `<dir>/x/go.mod` and `<dir>/pom.xml`. Detect returns only the root Go module. Covers item 2 (skipped directories).
- `TestDetectReturnsNoModulesForATreeWithoutMarkers`. Given only `README.md`. Detect returns an empty slice and no error. Covers the no-language path that preflight prints.
- `TestDetectFailsForAMissingRoot`. `Detect(filepath.Join(t.TempDir(), "gone"))` returns an error wrapping `fs.ErrNotExist`. Covers the walk-error path.
- `TestTouchedMapsEachFileToItsNearestModule`. Given Go modules at root and `root/tools`, and files `main.go` and `tools/gen.go`. The result is `[{root, ["main.go"]}, {tools, ["tools/gen.go"]}]`. Covers item 3 (nearest module).
- `TestTouchedCountsAGoModulesOwnGoModAndGoSum`. Given Go modules at root and `root/tools`, and files `tools/go.mod` and `tools/go.sum`. The result is `[{tools, ["tools/go.mod", "tools/go.sum"]}]`. Covers the q1 rule.
- `TestTouchedMapsJavaToTheBuildModuleAndGoToTheGoModule`. Given a Go module at root and a Maven module at `root/api`, and files `api/src/A.java` and `api/gen/x.go`. The result is `[{root go, ["api/gen/x.go"]}, {api maven, ["api/src/A.java"]}]`. Covers matching by language.
- `TestTouchedRunsNoToolchainForAChangeWithoutSourceFiles`. Given a Go module and a Maven module, and files `README.md`, `api/pom.xml` and `docs/x.md`. The result is empty. Covers item 3 ("no .go or .java file runs no language toolchain").
- `TestTouchedIgnoresAJavaFileOutsideEveryJavaModule`. Given only a Go module at root, and the file `scripts/Tool.java`. The result is empty. Covers the no-matching-module case.

### `internal/analyze/lines_test.go`

Package `analyze`. These tests use a real git repository.

Helpers:

- `newGitRepo(t, files map[string]string) string` creates a temp directory. It sets `t.Setenv("GIT_CONFIG_GLOBAL", <temp file>)` and `GIT_CONFIG_NOSYSTEM=1`, as `internal/gitrepo/repo_test.go:46` does. It runs `git init -q -b main`, writes the files, and commits them with `-c user.name=t -c user.email=t@t`.
- `writeText(t, dir, rel, content)` writes a file.

The tests:

- `TestChangedListsTheAddedAndModifiedLines`. Commit `a.go` = `1\n2\n3\n4\n5\n`, then rewrite it to `1\nB\n3\n5\n6\n7\n`. `Changed` gives `Lines["a.go"] == {2, 5, 6}`. Covers item 4 (the changed-line set from a real `git diff -U0`).
- `TestChangedListsAFileWithOnlyDeletedLinesWithNoLines`. Commit `d.go` = `x\ny\n`, then rewrite it to `x\n`. `Lines["d.go"]` exists and is empty, and `Files()` contains `d.go`. Covers deletion-only files touching their module.
- `TestChangedTakesANewUntrackedFileWhole`. Add an uncommitted `new/b.go`. `Untracked == {"new/b.go"}` and `Has("new/b.go", 99)` is true. Covers "or its file is untracked".
- `TestChangedTakesAMovedFileWholeUnderItsNewPath`. Commit `old/a.go` = `1\n2\n`, then `git mv old/a.go new/a.go` with no content change. `Files()` is `["new/a.go"]` and `Lines["new/a.go"] == {1, 2}`. Covers a pure rename touching its module.
- `TestChangedLeavesOutADeletedFile`. Commit `old.go`, then remove it. `Files()` is empty. Covers `+++ /dev/null`.
- `TestChangedReadsPathsWithASpaceAndANonASCIIName`. Commit `sp ace.go` and `é.go`, then change line 1 of each. The `Lines` keys are exactly `sp ace.go` and `é.go`, each `{1}`. Covers TAB-terminated and C-quoted headers.
- `TestChangedDoesNotReadAnAddedPlusPlusLineAsAHeader`. Commit `a.go` = `1\n`, then append the line `++ x`. `Files()` is `["a.go"]` and `Lines["a.go"] == {2}`. Covers the `inHeader` state.
- `TestChangedIgnoresTheUsersDiffConfig`. The global config file holds `diff.noprefix=true`, `diff.mnemonicPrefix=true`, `color.diff=always` and `diff.relative=false`, written with `git config --file`. A one-line change in `a.go` still gives `Lines["a.go"] == {1}`. Covers the user's gitconfig as an input.
- `TestChangedFilesIsTheSortedUnionOfTrackedAndUntracked`. Modified `b.go`, untracked `a.go`. `Files()` is `["a.go", "b.go"]`. Covers the file list that `Touched` takes.
- `TestChangedFailsOutsideARepository`. `Changed(ctx, t.TempDir())` returns an error that starts with `git diff:`. Covers the git error path.

### `internal/analyze/sarif_test.go`

Package `analyze`. SARIF documents are inline string literals. The helper `hit(tool, rule, level, path string, line int) Hit` sets `Message` to `"m"` unless the test sets it.

- `TestParseSARIFReadsEachResult`. A golangci-style run with two results with relative uris, parsed with `ParseSARIF("golangci-lint", "/repo/mod", data)`, gives the two exact `Hit`s with `Path` `/repo/mod/<uri>`. Covers item 4 (parsing).
- `TestParseSARIFResolvesTheResultsPath`. A table:

  | Row | Input | Expected `Path` |
  |---|---|---|
  | file URI | `file:///abs/x.go` | `/abs/x.go` |
  | uriBaseId | `com/a/B.java` with `uriBaseId` `SRCROOT` = `file:///repo/src/main/java/` | `/repo/src/main/java/com/a/B.java` |
  | percent-encoded | `sp%20ace.go` with base `/repo` | `/repo/sp ace.go` |

  Covers path resolution for SpotBugs, golangci and Semgrep reports.
- `TestParseSARIFTakesTheLevelFromTheResultTheRuleOrTheDefault`. A table: the result's `error` gives `error`; no result level with the rule default `note` gives `note`; neither gives `warning`. Covers the level in `Detail`.
- `TestParseSARIFResolvesAnArtifactLocationByIndex`. A run with `artifacts: [{location: {uri: "src/A.java"}}]` and a result at line 7 whose `artifactLocation` is `{index: 0}`, parsed with base `/repo`, gives `Path` `/repo/src/A.java` and `Line` 7; fed through `Findings` with `Changes{Lines: {"src/A.java": {7}}}` and dir `/repo`, it gives one finding with `Files` `["src/A.java"]`. Covers a location that names its file only by index.
- `TestParseSARIFResolvesTheRuleFromRuleIndex`. A result with `ruleIndex: 1`, no `ruleId` and no level, against driver rules `[{id: A}, {id: B, defaultConfiguration.level: error}]`, gives `Rule` `B` and `Level` `error`. Covers a result that names its rule only by index.
- `TestParseSARIFBuildsTheMessageFromItsIdAndArguments`. A table: `message: {id: default, arguments: ["x", "Foo.bar()"]}` with the rule's `messageStrings.default.text` `Dead store to {0} in {1}` gives `Dead store to x in Foo.bar()`; the same id found only in `globalMessageStrings` with text `{{0}} is {0}` and arguments `["x"]` gives `{0} is x`. Covers SpotBugs-style messages and the brace escape.
- `TestParseSARIFKeepsAResultWithoutALocation`. The hit has `Path` `""` and `Line` 0. Covers govulncheck module-level results.
- `TestParseSARIFRejectsInvalidJSON`. The error starts with `semgrep: sarif:`. Covers an unparsable report.
- `TestFindingsKeepOnlyHitsOnChangedLinesOrInUntrackedFiles`. A table over `Changes{Lines: {"a.go": {3}}, Untracked: {"n.go"}}` with `dir` `/repo`. Each row says whether one hit becomes a finding:

  | Hit | Kept |
  |---|---|
  | `/repo/a.go:3` | yes |
  | `/repo/a.go:4` | no |
  | `/repo/n.go:40` | yes |
  | `/repo/b.go:1` | no |
  | pathless | no |

  Covers item 4 (the filter).
- `TestFindingsKeepEveryGovulncheckHit`. Two govulncheck hits, one on unchanged `/repo/go.mod:5` and one pathless, with empty `Changes`, give two findings. Covers the govulncheck exception.
- `TestFindingsNumberTitleAndDescribeEachHit`. One hit `golangci-lint/errcheck`, level `warning`, message `Error return value is not checked\nsecond line`, at `/repo/internal/a.go:12`, on a changed line. The result is exactly `[]core.Finding{{ID: "s1", Title: "golangci-lint/errcheck: Error return value is not checked", Detail: "warning\nError return value is not checked\nsecond line\ninternal/a.go:12", Files: []string{"internal/a.go"}}}`. Covers the id, title, detail and files format.
- `TestFindingsDescribeAPathlessHitWithoutALocation`. A pathless govulncheck hit gives `Detail` `"error\nGO-2026-0001 in x/net"` and `Files` `[]string{}`. Covers the non-nil `Files` that `parseFindings` requires.
- `TestFindingsListTheMostSevereFirst`. A `warning` hit at `a.go:1` and then an `error` hit at `b.go:9` give `s1` for `b.go` and `s2` for `a.go`. Covers the sort that decides which 50 survive.
- `TestFindingsCapAtFiftyAndCountTheRestByToolAndRule`. The hits are built with the helper `manyHits(tool, rule, level, path string, n int) []Hit` (lines 1…n, all in `Untracked`): 50 `golangci-lint/errcheck` errors in `e.go`, 2 `gosec/G104` warnings in `w.go`, and 1 `pmd/UnusedLocalVariable` note in `n.java`. The result has 51 findings. `s50` is an errcheck error. `s51` is `{ID: "s51", Title: "static: 3 more findings", Detail: "gosec/G104: 2\npmd/UnusedLocalVariable: 1", Files: []string{"n.java", "w.go"}}`. Covers the cap.
- `TestFindingsAtExactlyFiftyAddNoSummary`. 50 hits give 50 findings, the last `s50`. Covers the cap boundary.
- `TestFindingsOfNoHitsIsAnEmptyList`. The result is `[]core.Finding{}`, non-nil, with length 0. Covers the findings file's required list.

### `internal/analyze/pmdxml_test.go`

Package `analyze`. Reports are inline XML literals with the PMD 2.0.0 namespace.

- `TestParsePMDReadsEachViolation`. Two `<file>` entries, one with an absolute name and one relative, parsed with base `/repo/api`. The result is the exact `Hit`s, including `Tool` `pmd`, `Rule`, the message trimmed of its surrounding newlines, `Path` and `Line`. Covers item 4 (PMD parsing).
- `TestParsePMDMapsPriorityToLevel`. A table: 1 gives `error`, 2 `error`, 3 `warning`, 4 `note`, 5 `note`. Covers the level in `Detail`.
- `TestParsePMDIgnoresProcessingErrors`. A report with only `<error filename="x" msg="boom"/>` gives no hits and no error.
- `TestParsePMDRejectsMalformedXML`. A truncated document gives an error starting `pmd: xml:`.
- `TestParsePMDRejectsAnotherRootElement`. A `<checkstyle>` root gives an error starting `pmd: xml:`.
- `TestPMDViolationsPassTheChangedLineFilter`. Parsed violations at `A.java:7` (changed) and `A.java:8` (not changed), fed through `Findings`, give one finding: `s1`, `pmd/UnusedLocalVariable: Avoid unused local variables such as 'x'.`. Covers the filter, ids and title for PMD.

### `internal/config/reader_test.go`

These use the existing `newDirs`, `writeProject` and `load`/`loadErr` helpers (`reader_test.go:16-60`).

- `TestAnalyzeIsOnWithAFifteenMinuteTimeoutByDefault`. The result is `cfg.Analyze == Analyze{Enabled: true, Timeout: 15 * time.Minute}`, and both `Provenance["analyze.enabled"]` and `Provenance["analyze.timeout"]` are `"default"`. Covers item 5 (defaults and provenance).
- `TestAnalyzeKeysFromTheProjectFileCarryTheirProvenance`. Given `analyze:\n  enabled: false\n  timeout: 5m\n`. The result is `Analyze{false, 5m}`, with provenance `.r-loop/config.yaml:analyze.enabled` and `.r-loop/config.yaml:analyze.timeout`. Covers item 5 (provenance).
- `TestNullAnalyzeEnabledFallsThroughToTheMachineFile`. Given home `enabled: false` and project `enabled:`. The result is `false` with provenance `~/.config/r-loop/config.yaml:analyze.enabled`. Covers the new `boolean` getter's null rule.
- `TestUnknownAnalyzeKeyRejected`. Given `analyze:\n  languages: go\n`. The error is `.r-loop/config.yaml:2: unknown key "analyze.languages"`. Covers item 5 (unknown keys).
- `TestAnalyzeEnabledThatIsNotABooleanIsRejected`. Given `enabled: yes`. The error is `.r-loop/config.yaml:2: analyze.enabled: "yes" is not true or false`. Covers the bad-value path.
- `TestAnalyzeTimeoutThatIsNotPositiveIsRejected`. Given `timeout: 0s`. The error is `.r-loop/config.yaml:2: analyze.timeout: "0s" is not a positive duration`. Covers the timeout validation.

### `internal/app/app_test.go`

Helper `toolchainPath(t, tools ...string)`, called after `newFixture`:

1. Resolve the real `git` with `exec.LookPath`.
2. Create a temp `bin` holding a `git` script, `#!/bin/sh\nexec <real git> "$@"\n`, and one `#!/bin/sh\nexit 0\n` script, mode `0o755`, per name in `tools`.
3. `t.Setenv("PATH", bin + sep + <fakeProviders' bin, the first PATH entry, as at app_test.go:136>)`.

This leaves `semgrep`, `go`, `java`, `mvn` and `gradle` absent unless the test names them. Fixtures write their markers with `f.write` and then `f.commit()`.

- `TestPreflightPrintsTheStaticLine`. A table; each row runs `f.preflight(f.todo, "--plain")` and asserts that `f.out` contains the line.

  | Markers | Tools | Expected line |
  |---|---|---|
  | `go.mod` | `go`, `semgrep` | `static: go, semgrep\n` |
  | `go.mod` | `go` | `static: go (semgrep not installed)\n` |
  | `go.mod`, `svc/pom.xml`, `app/build.gradle` | `go`, `mvn`, `gradle`, `java` | `static: go, maven, gradle (semgrep not installed)\n` |
  | none | `semgrep` | `static: semgrep\n` |
  | none | none | `static: none (semgrep not installed)\n` |

  Covers item 6 (the printed line).
- `TestPreflightExitsTwoNamingAMissingToolchain`. A table; each row asserts `exitCode == 2`, that the error contains the message, and that `f.herdrCalled()` is false.

  | Markers | Tools | Message |
  |---|---|---|
  | `go.mod` | none | `analyze: go module . needs go, not found on PATH` |
  | `svc/pom.xml` | `java` | `analyze: maven module svc needs mvn, not found on PATH` |
  | `svc/pom.xml` | `mvn` | `analyze: maven module svc needs java, not found on PATH` |
  | `build.gradle` | `java` | `analyze: gradle module . needs gradle, not found on PATH` |
  | `build.gradle` | `gradle` | `analyze: gradle module . needs java, not found on PATH` |
  | `build.gradle`, `settings.gradle`, `gradlew` committed with mode `0644` | `java` | `analyze: gradle module . needs gradlew, which is not executable` |

  Covers item 6 (the exit and the toolchain it names).
- `TestPreflightChecksNoToolchainWhenAnalyzeIsOff`. Given `.r-loop/config.yaml` with `analyze:\n  enabled: false\n`, `go.mod`, and no tools. `f.preflight` returns nil and `f.out` has no `static:`. Covers "when analyze.enabled".
- `TestAMissingToolchainIsNotCheckedInDryRun`. Given `go.mod`, no tools, and `f.fakeHerdr(1)`. `f.main(f.todo, "--dry-run", "--plain")` returns 0, and `f.out` contains `static: go (semgrep not installed)\n`. Covers the dry-run choice.

## Left out

- **A banner line for `analyze.enabled`/`analyze.timeout`.** The item asks for provenance, which `LoopConfig.Provenance` holds. `config.Banner` is not in `Files:`, and the `static:` line already says the reviewer is on.
- **`Analyzer` implementation (`analyze.New`), running tools, `analyze.timeout` enforcement and failure messages.** These are phase 48.
- **Wiring into `ReviewHalf`, the findings file and the `review-find` event.** These are phase 49.
- **A test of `fakeAnalyzer` itself.** It is test scaffolding. Phase 49's review tests exercise it, and the `var _ Analyzer` assertion keeps its signature honest.
- **Treating PMD `<error>` elements as a failure.** ADR-89 defines failure as a non-zero exit, a missing or unparsable report, or the timeout. That is phase 48's run, not a parse result.
- **Deduplicating identical hits across tools.** No obligation asks for it, and the step session judges each finding.
- **Deleted files in the changed set.** No line of a deleted file can carry a finding, and the line filter would drop them anyway.
- **Reusing `internal/gitrepo` for the diff.** It has no hunk parser, and adding one there would touch a file outside `Files:` for a single call site.
- **`strconv.ParseBool` leniency (`1`, `t`, `F`).** The config reader is strict everywhere else.

## Assumptions

- **Finding ids.** They are exactly `s1…sN`, as the item says. `checkFindingsFile` (`internal/core/evidence.go:350`) expects agent-written ids prefixed `<reviewer>-r<round>-`. Phase 49 owns how `static`'s file meets that check, and `Findings` needs no change for it.
- **Who names the tool.** `ParseSARIF`'s caller passes the tool name used in titles and in the govulncheck exception (`golangci-lint`, `govulncheck`, `spotbugs`, `semgrep`). Phase 48 passes these names.
- **SARIF paths.** These resolve against `originalUriBaseIds`, then against `base`, which is the directory the tool ran in. Phase 48 passes the module or build-root directory as `base`.
- **The `static:` line's language names.** These are the module kinds (`go`, `maven`, `gradle`), because they name the toolchain that will run. An empty list prints `none`, or only `semgrep` when Semgrep is present.
- **Detection runs on the primary tree.** It walks from `w.Repo.Root()` on every preflight and resume. Detection errors exit 2, as other preflight I/O errors do (`preflight.go:80`, `:186`).
- **The missing-toolchain exit is 2, as the spec says.** Missing provider binaries exit 127 (`preflight.go:327`), and the README's exit-code row for 2 is extended to cover this case.
