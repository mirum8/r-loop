status: planned

## Summary

This phase builds the `core.Analyzer` adapter, `analyze.New(timeout)` with `(*Analyzer).Analyze(ctx, dir)`. It chains phase 47's `Changed` → `Detect`/`Touched` → each touched toolchain → `Findings`, and fills `Analysis.Command` with the tools that ran. Tools run one after another, in order:

1. golangci-lint, plus govulncheck when the module's `go.mod`/`go.sum` changed, in each touched Go module.
2. One Maven invocation per Maven build root, with the find-sec-bugs jar fetched once into `<user cache>/r-loop/analyze`.
3. One Gradle invocation per Gradle build root, with an embedded init script.
4. Semgrep over the changed files when it is on `PATH`.

The whole `Analyze` runs under one `context.WithTimeout(analyze.timeout)`. Each command runs in its own process group so a timeout really kills `go run`/JVM children. Any failure returns `<tool>: <reason>` and the last 4096 bytes of that command's output.

Choices:
- **Constructor `New(timeout time.Duration)`** over `New(cfg config.Analyze)`. Adapters take primitives (`store.New(repoRoot)` at `internal/store/store.go:60`, `prompts.New(projectDir)` at `internal/prompts/render.go:85`), and no adapter imports `internal/config`; only `internal/app` does (`internal/app/wire.go`, `preflight.go`, `intake.go`).
- **One `pass` struct holding the run's state**, rather than threading ctx, dir, out dir, tools and hits through every function. It is unexported and per call, not a new port.
- **Sequential tools** over parallel. Maven and Gradle builds compete for CPU anyway. Sequential gives one timeout, a deterministic `Command` order and a single tool to name in a timeout error.
- **`--path-mode=abs` added to golangci-lint.** Its default `relative-path-mode` is `cfg`, meaning paths are relative to the config file's directory. A parent-directory config would then make module-relative joining wrong.
- **`filepath.EvalSymlinks(dir)` first.** Tools report real paths (golangci `abs`, Java `user.dir`). A symlinked worktree path would otherwise make `filepath.Rel` in `Findings` produce `../…`, and the changed-line filter would silently drop every hit.
- **Stale module reports are deleted before the build**, rather than read as found. Phase 49 re-runs the analysis every round in the same worktree. A leftover `target/pmd.xml` would otherwise hide a report the build no longer writes.
- **A module with no compiled main classes needs no reports and contributes no hits** (watchdog answer q2, by the maintainer: option b). PMD's `pmd` goal and SpotBugs skip, and Gradle's `pmdMain`/`spotbugsMain` are NO-SOURCE, for a test-only module. A missing report from a module that has main classes still fails.
- **The Gradle init script applies under `afterEvaluate` with a `hasPlugin` guard**, rather than plain `plugins.withId('java')`. With `withId`, a build that declares `id 'java'` before `id 'com.github.spotbugs'` would get our SpotBugs plugin first. Its own plugin would then fail with a duplicate `spotbugs` extension, which breaks "a build script that already configures SpotBugs keeps its configuration".
- **Versions go into the init script by placeholder replacement at write time**, rather than hard-coded in `init.gradle`, so `versions.go` stays the only place versions live.
- **The jar is staged in a temp directory inside the cache directory, then renamed**, rather than downloaded straight to its final name. A timeout mid-copy can't leave a truncated jar that later runs treat as cached. Staging in the same directory keeps the rename on one filesystem.

## Why this approach

The phase proposes four new files and the command lines in its items. This plan follows them, with these changes and additions.

- **`New`/`Analyze` are built here.** This is the watchdog's warning. Phase 47 shipped only the building blocks: `Detect` and `Touched` (`internal/analyze/detect.go:31`, `:103`), `Changed` (`internal/analyze/lines.go:23`), `ParseSARIF` (`internal/analyze/sarif.go:80`), `ParsePMD` (`internal/analyze/pmdxml.go:10`) and `Findings` (`internal/analyze/sarif.go:211`). The port is `core.Analyzer` (`internal/core/ports.go:137`). Phase 49 (`docs/task-loop-driver/todo.md:810`) wires `analyze.New(cfg)` into `ReviewHalf`. `run.go` is new in this phase, so the analyzer lives there and `run_test.go` drives everything through `Analyze`. The signature takes `cfg.Analyze.Timeout` (`internal/config/reader.go:77-80`); phase 49 calls `analyze.New(cfg.Analyze.Timeout)`.
- **golangci-lint gets `--path-mode=abs`.** golangci-lint v2.14.0's `pkg/fsutils/basepath.go:29-38` resolves paths against the config directory by default, falling back to the working directory. `pkg/commands/flagsets.go:65` defines `--path-mode` (value `abs`, `basepath.go:23`).
- **The `gosec` config check also accepts `.golangci.json`.** golangci-lint reads that name too. Without it, a repository configured in JSON would get `gosec` forced on top of its own choice. The parent walk stops at `dir`, the repository: ADR-89 says "when the repository has no .golangci.yml" (`docs/task-loop-driver/spec.html:3896-3897`).
- **`maven-dependency-plugin` is pinned as well.** It is fetched like the analyzers, and ADR-89 says the toolchain fetches "at versions pinned in the binary". `dependency:copy` is invoked by full coordinates (`org.apache.maven.plugins:maven-dependency-plugin:3.11.0:copy`) at the build root, with the build's own wrapper. The 3.11.0 descriptor declares `copy` with `requiresProject=false` and the `artifact` and `outputDirectory` user properties, both checked in the published jar.
- **One Maven invocation per build root.** It runs `compile`, the PMD goal and the SpotBugs goal in that order, matching `docs/task-loop-driver/tech-design.md` §Milestone 18 "Maven". The item's "then" is satisfied by goal order within that one invocation. spotbugs-maven-plugin 4.10.4.1's `spotbugs` goal reads `spotbugs.sarifOutput` and `spotbugs.pluginList` and writes `spotbugsSarif.json` to `${project.build.directory}`. maven-pmd-plugin 3.28.0's `pmd` goal defaults to `format=xml` in `${project.build.directory}`. Neither goal fails the build on a finding: they are `pmd:pmd` and `spotbugs:spotbugs`, not `check`.
- **The Gradle init script uses `afterEvaluate` + `pluginManager.hasPlugin('com.github.spotbugs')`.** The tech design says "under `plugins.withId('java')`". This changes how the plugins are applied, not what gets applied, and it is what makes the "existing configuration kept" item hold for builds that already use SpotBugs. spotbugs-gradle-plugin 6.5.12 (`com.github.spotbugs.snom.SpotBugsPlugin`, the plugin portal's latest) calls no `afterEvaluate` itself, so applying it inside one is safe. Its SARIF report is `build/reports/spotbugs/main.sarif` for `spotbugsMain`.
- **The init script adds `mavenCentral()` to a Java project that has no repositories at all.** The Gradle docs keep the init script's `initscript { repositories }` (for the plugin classpath) separate from a project's dependency repositories, which resolve the `pmd`, `spotbugs` and `spotbugsPlugins` configurations. ADR-89 requires the analysis to work unconfigured (`docs/task-loop-driver/spec.html:3892-3894`). Settings-level repositories, captured in `settingsEvaluated`, count as declared, so builds that centralise repositories are left alone.
- **Process-group kill.** Each command runs in its own process group, killed on cancel, with `WaitDelay`: `Setpgid`, `cmd.Cancel` killing `-pid`, `WaitDelay = time.Second`. This follows `internal/gitrepo/repo.go:87-118` and `internal/notify/notify.go:45-47`. The timeout and interrupt wording follows `internal/gitrepo/repo.go:110-115` ("timed out after %s: %w", "interrupted: %w"). Without the process-group kill, killing `go run` leaves the compiled golangci-lint child holding the output pipe, and Maven or Gradle JVMs keep running past `analyze.timeout`.
- **Semgrep's file list starts with `--`.** A changed file whose name starts with `-` cannot be read as a flag.
- **Pinned versions** are the newest releases as of 2026-10-06, looked up from the module proxy, Maven Central and the Gradle plugin portal:

| Tool | Version | Source |
|---|---|---|
| golangci-lint | `v2.14.0` | `go list -m -versions github.com/golangci/golangci-lint/v2` |
| govulncheck | `v1.8.0` | `go list -m -versions golang.org/x/vuln` |
| maven-pmd-plugin | `3.28.0` | Maven Central |
| spotbugs-maven-plugin | `4.10.4.1` | Maven Central |
| spotbugs-gradle-plugin | `6.5.12` | plugin portal |
| findsecbugs-plugin | `1.14.0` | Maven Central |
| maven-dependency-plugin | `3.11.0` | Maven Central |

No file outside the phase's `Files:` line changes.

## Changes

### 1. `internal/analyze/versions.go` (create)

Unexported constants, the only place versions live:

```go
const (
	golangciLintVersion          = "v2.14.0"
	govulncheckVersion           = "v1.8.0"
	mavenPMDPluginVersion        = "3.28.0"
	spotbugsMavenPluginVersion   = "4.10.4.1"
	spotbugsGradlePluginVersion  = "6.5.12"
	findSecBugsVersion           = "1.14.0"
	mavenDependencyPluginVersion = "3.11.0"
)
```

Serves: the versions item.

### 2. `internal/analyze/init.gradle` (create)

Embedded with `//go:embed init.gradle` into `var initScript string` in `run.go`, following `internal/config/reader.go:21`. Exact content:

```groovy
initscript {
    repositories {
        gradlePluginPortal()
    }
    dependencies {
        classpath 'com.github.spotbugs.snom:spotbugs-gradle-plugin:{{spotbugsGradlePlugin}}'
    }
}

def settingsDeclareRepositories = false
settingsEvaluated { settings ->
    settingsDeclareRepositories = !settings.dependencyResolutionManagement.repositories.isEmpty()
}

allprojects {
    afterEvaluate {
        if (!plugins.hasPlugin('java')) {
            return
        }
        if (!settingsDeclareRepositories && repositories.isEmpty()) {
            repositories {
                mavenCentral()
            }
        }
        apply plugin: 'pmd'
        if (!pluginManager.hasPlugin('com.github.spotbugs')) {
            apply plugin: com.github.spotbugs.snom.SpotBugsPlugin
        }
        pmd {
            ignoreFailures = true
        }
        spotbugs {
            ignoreFailures = true
        }
        dependencies {
            spotbugsPlugins 'com.h3xstream.findsecbugs:findsecbugs-plugin:{{findSecBugs}}'
        }
        tasks.matching { it.name == 'spotbugsMain' }.configureEach {
            reports {
                maybeCreate('sarif').required = true
            }
        }
    }
}
```

`apply plugin: 'pmd'` is idempotent, so an existing `pmd { … }` block is kept. A build that already applies SpotBugs keeps its own plugin and configuration; only `ignoreFailures`, the SARIF report and find-sec-bugs are added. `maybeCreate` keeps a `sarif` report the build already declares.

The PMD and SpotBugs engines and find-sec-bugs resolve through the project's dependency repositories, not the init script's classpath repositories. A Java project that declares no repositories at all, neither in its build script nor in `settings.gradle`'s `dependencyResolutionManagement`, could not resolve them and would fail before writing a report. For exactly that project the script adds `mavenCentral()`. A project or settings file that declares any repository keeps its own, so corporate mirrors and `FAIL_ON_PROJECT_REPOS` builds are untouched.

Serves: the Gradle item, the existing-configuration item, and ADR-89's "works unconfigured" for a Java build with no repositories (review codex-r1-1).

**Real-Gradle check (planner-run, and re-run by the implementer once before finishing; not a committed test).** The planner ran this exact script, with `6.5.12` and `1.14.0` substituted, under Gradle 9.8.0 and JDK 25 against two scratch fixtures. Each fixture held a `src/main/java/a/Bad.java` that builds an SQL string from a parameter and passes it to `Statement.executeQuery`.
- **No repositories anywhere.** The fixture is `settings.gradle` with only `rootProject.name`, and `build.gradle` = `plugins { id 'java' }`. `gradle -q --no-daemon --init-script init.gradle :pmdMain :spotbugsMain` exited 0. It wrote `build/reports/pmd/main.xml` and `build/reports/spotbugs/main.sarif`, and the SARIF holds find-sec-bugs' `SQL_INJECTION_JDBC`. This proves the `mavenCentral()` fallback resolves PMD, SpotBugs and find-sec-bugs.
- **Repositories in settings, SpotBugs already applied.** `settings.gradle` declares `dependencyResolutionManagement { repositoriesMode = RepositoriesMode.FAIL_ON_PROJECT_REPOS; repositories { mavenCentral() } }` and `include 'app'`. `app/build.gradle` = `plugins { id 'java'; id 'com.github.spotbugs' version '6.5.12' }` plus `spotbugs { effort = com.github.spotbugs.snom.Effort.MAX }`. `:app:pmdMain :app:spotbugsMain` exited 0 and wrote both reports, with `SQL_INJECTION_JDBC` in the SARIF. `FAIL_ON_PROJECT_REPOS` would have failed the build had the script added a project repository, and the build's own SpotBugs plugin and `effort` were kept without a duplicate-extension error.

The implementer re-runs both fixtures with the final `init.gradle` and the same commands. Both must exit 0 and write both reports. A failure means the script deviates from this plan's text, and the step fails with Gradle's output rather than improvising a different script (review codex-r2-3).

### 3. `internal/analyze/run.go` (create)

Imports: `bytes`, `context`, `_ "embed"`, `errors`, `fmt`, `io`, `io/fs`, `os`, `os/exec`, `path/filepath`, `slices`, `strconv`, `strings`, `syscall`, `time`, `r-loop/internal/core`.

**Public API**

```go
type Analyzer struct{ timeout time.Duration }

func New(timeout time.Duration) *Analyzer

func (a *Analyzer) Analyze(parent context.Context, dir string) (core.Analysis, error)
```

**`Analyze` steps**

1. `ctx, cancel := context.WithTimeout(parent, a.timeout)`; `defer cancel()`.
2. `dir, err = filepath.EvalSymlinks(dir)`. On error, return `fmt.Errorf("analyze: %w", err)`.
3. `ch, err := Changed(ctx, dir)` (`lines.go:23`). On error, return it unchanged.
4. `files := ch.Files()`. If `len(files) == 0`, return `core.Analysis{Findings: []core.Finding{}}` with `Command` `""`, running nothing. Semgrep given no targets would scan the whole tree.
5. `mods, err := Detect(dir)`. On error, return `fmt.Errorf("analyze: %w", err)`. Then `touches := Touched(mods, dir, files)`.
6. `out, err := os.MkdirTemp("", "r-loop-analyze-")`. On error, return `fmt.Errorf("analyze: %w", err)`. Then `defer os.RemoveAll(out)`. The temp dir is outside the worktree, so nothing lands in the tree or the commit.
7. Build `p := &pass{ctx, parent, timeout: a.timeout, dir, out}`.
8. Run the Go modules: for each touch with `Module.Kind == Go`, in `Touched` order, `p.golang(n, t)`, where `n` is the 1-based count of Go touches.
9. Run Maven: for each group in `byBuildRoot(touches of Kind Maven)`, call `p.maven(group)`.
10. Run Gradle: for each group in `byBuildRoot(touches of Kind Gradle)`, call `p.gradle(group)`.
11. If `exec.LookPath("semgrep")` succeeds, call `p.semgrep(files)`.
12. Return the first error from steps 8–11 as is.
13. Build `analysis := core.Analysis{Findings: Findings(dir, ch, p.hits), Command: strings.Join(p.tools, ", ")}`, reusing `sarif.go:211`.
14. If `ctx.Err() != nil`, return `failure("analyze", nil, p.cause(ctx.Err()))`. The deadline or the caller's cancel can pass during work that runs no command: `Detect`'s walk, `hasClasses`, report reads, `Findings` itself. Without this check, a run with no later command would return success after the deadline. The check is the last statement before success, so nothing runs after it (reviews codex-r1-3, codex-r2-2).
15. Otherwise return `analysis`.

**`pass` and its helpers**

```go
type pass struct {
	ctx, parent context.Context
	timeout     time.Duration
	dir, out    string
	tools       []string
	hits        []Hit
}
```

- **`func (p *pass) ran(names ...string)`** appends each name not already in `p.tools`. Tool names are `golangci-lint`, `govulncheck`, `pmd`, `spotbugs`, `semgrep`, in first-run order.
- **`func (p *pass) command(tool, cwd string, stdout io.Writer, name string, args ...string) ([]byte, error)`** runs one command.
  - Setup:
    - `exec.CommandContext(p.ctx, name, args...)`, with `cmd.Dir = cwd`.
    - `cmd.Env` is left nil, so the child gets `PWD=cwd`.
    - `SysProcAttr{Setpgid: true}`, `cmd.Cancel` killing `-pid` with SIGKILL, and `WaitDelay = time.Second`, as in `gitrepo/repo.go:92-94`.
    - With a nil `stdout`, stdout and stderr both go to one `bytes.Buffer`, `out`.
    - With a non-nil `stdout` (govulncheck's report), stdout goes to `io.MultiWriter(stdout, &out)` and stderr to a second `bytes.Buffer`, `errOut`. Two buffers keep the two copy goroutines race-free. The returned output is `out`'s bytes followed by `errOut`'s, so a failing govulncheck's tail carries both streams (review codex-r1-2).
  - An `exec.ErrWaitDelay` with a successful `ProcessState` counts as success (`gitrepo/repo.go:107`).
  - It returns the output, and on error `failure(tool, output, p.cause(err))`.
- **`func (p *pass) cause(err error) error`** returns:
  - `fmt.Errorf("interrupted: %w", p.parent.Err())` when `p.parent.Err() != nil`;
  - otherwise `fmt.Errorf("timed out after %s: %w", p.timeout, context.DeadlineExceeded)` when `p.ctx.Err()` is `DeadlineExceeded`;
  - otherwise `err` itself, such as `exit status 3` or `exec: "mvn": executable file not found in $PATH`.
- **`func failure(tool string, output []byte, cause error) error`** returns `fmt.Errorf("%s: %w%s", tool, cause, tail(output))`.
- **`func tail(output []byte) string`** returns `""` for empty output. Otherwise it returns `"\n"` followed by the last 4096 bytes of `output`, exactly, untrimmed (review codex-r1-4).
- **`func (p *pass) show(path string) string`** returns the slash path relative to `p.dir` when `within(p.dir, path)` (`detect.go:138`), else `filepath.Base(path)`. Reports in the temp dir are shown by file name.
- **`func (p *pass) read(tool, base, path string, output []byte) error`**:
  - `os.ReadFile(path)`. On `fs.ErrNotExist`, return `failure(tool, output, fmt.Errorf("no report at %s", p.show(path)))`. On any other read error, return `failure(tool, output, err)`.
  - Parse with `ParsePMD(base, data)` when `tool == "pmd"`, else `ParseSARIF(tool, base, data)`.
  - A parse error returns `fmt.Errorf("%w in %s%s", err, p.show(path), tail(output))`. The parse errors already start with the tool name: `sarif.go:85`, `pmdxml.go:24`.
  - Appends the hits to `p.hits`.
- **`func byBuildRoot(touches []Touch) [][]Touch`** groups touches by `Module.BuildRoot`, in order of first appearance.
- **`func hasClasses(dir string) (bool, error)`** uses `filepath.WalkDir`. It returns true at the first regular file ending in `.class`, false for a missing `dir`, and any other walk error as is.
- **`func removeReports(paths ...string) error`** calls `os.Remove` on each path, ignoring `fs.ErrNotExist` and returning any other error.

**Go: `func (p *pass) golang(n int, t Touch) error`**

- Let `m := t.Module` and `report := filepath.Join(p.out, "golangci-"+strconv.Itoa(n)+".sarif")`.
- Args are `run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@<golangciLintVersion> run --issues-exit-code=0 --path-mode=abs --output.sarif.path=<report>`, plus a trailing `--enable=gosec` when `!golangciConfigured(p.dir, m.Dir)`.
- Run `p.command("golangci-lint", m.Dir, nil, m.Runner, args...)`, then `p.ran("golangci-lint")`, then `p.read("golangci-lint", m.Dir, report, output)`.
- If `modChanged(p.dir, t)`:
  - `f, err := os.Create(<out>/govulncheck-<n>.sarif)`; on error, return `failure("govulncheck", nil, err)`.
  - Run `p.command("govulncheck", m.Dir, f, m.Runner, "run", "golang.org/x/vuln/cmd/govulncheck@"+govulncheckVersion, "-format", "sarif", "./...")`, then close `f`.
  - Then `p.ran("govulncheck")` and `p.read("govulncheck", m.Dir, <that file>, output)`.
- **`func golangciConfigured(root, modDir string) bool`** walks from `modDir` up to and including `root`. It returns true when any of `.golangci.yml`, `.golangci.yaml`, `.golangci.toml`, `.golangci.json` is a regular file in one of those directories.
- **`func modChanged(root string, t Touch) bool`** is true when a file in `t.Files` has base name `go.mod` or `go.sum` and `filepath.Join(root, filepath.Dir(f)) == t.Module.Dir`. A `testdata/x/go.mod` mapped to the parent module does not count.

**Maven: `func (p *pass) maven(group []Touch) error`**

- Let `root, runner := group[0].Module.BuildRoot, group[0].Module.Runner`.
- `jar, err := p.findSecBugs(root, runner)`.
- `pl` is each module's `filepath.Rel(root, m.Dir)` as a slash path (`.` for the build root), joined with `,` in group order.
- For each module, run `removeReports(<Dir>/target/pmd.xml, <Dir>/target/spotbugsSarif.json)`. On error, return `failure("maven", nil, err)`.
- `output, err := p.command("maven", root, nil, runner, "-q", "-B", "-DskipTests", "-pl", pl, "-am", "compile", "org.apache.maven.plugins:maven-pmd-plugin:"+mavenPMDPluginVersion+":pmd", "com.github.spotbugs:spotbugs-maven-plugin:"+spotbugsMavenPluginVersion+":spotbugs", "-Dspotbugs.sarifOutput=true", "-Dspotbugs.pluginList="+jar)`, then `p.ran("pmd", "spotbugs")`.
- For each module:
  - `ok, err := hasClasses(<Dir>/target/classes)`. On error, return `failure("maven", output, err)`. If `!ok`, skip the module.
  - Otherwise `p.read("pmd", m.Dir, <Dir>/target/pmd.xml, output)`, then `p.read("spotbugs", m.Dir, <Dir>/target/spotbugsSarif.json, output)`.

**`func (p *pass) findSecBugs(root, runner string) (string, error)`**

- `cache, err := os.UserCacheDir()`. On error, return `failure("find-sec-bugs", nil, err)`.
- `dir := cache/r-loop/analyze`, `name := "findsecbugs-plugin-" + findSecBugsVersion + ".jar"`, `jar := dir/name`. If `jar` is a regular file, return it: fetched once.
- `os.MkdirAll(dir, 0o755)`, then `stage, err := os.MkdirTemp(dir, "fetch-")`, with `defer os.RemoveAll(stage)`. Either failure returns `failure("find-sec-bugs", nil, err)`.
- `output, err := p.command("find-sec-bugs", root, nil, runner, "-q", "-B", "org.apache.maven.plugins:maven-dependency-plugin:"+mavenDependencyPluginVersion+":copy", "-Dartifact=com.h3xstream.findsecbugs:findsecbugs-plugin:"+findSecBugsVersion, "-DoutputDirectory="+stage)`.
- `os.Rename(stage/name, jar)`:
  - `fs.ErrNotExist` returns `failure("find-sec-bugs", output, fmt.Errorf("dependency:copy wrote no %s", name))`.
  - Any other error returns `failure("find-sec-bugs", output, err)`.

**Gradle: `func (p *pass) gradle(group []Touch) error`**

- Let `root, runner` be as for Maven.
- `script := filepath.Join(p.out, "init.gradle")`. Write `strings.NewReplacer("{{spotbugsGradlePlugin}}", spotbugsGradlePluginVersion, "{{findSecBugs}}", findSecBugsVersion).Replace(initScript)` with mode `0o644`. On a write error, return `failure("gradle", nil, err)`.
- Tasks: for each module, `prefix` is `":"` when the slash path relative to `root` is `.`, else `":" + strings.ReplaceAll(rel, "/", ":") + ":"`. Append `prefix+"pmdMain"` and `prefix+"spotbugsMain"`.
- For each module, run `removeReports(<Dir>/build/reports/pmd/main.xml, <Dir>/build/reports/spotbugs/main.sarif)`. On error, return `failure("gradle", nil, err)`.
- `output, err := p.command("gradle", root, nil, runner, append([]string{"-q", "--init-script", script}, tasks...)...)`, then `p.ran("pmd", "spotbugs")`.
- For each module:
  - `hasClasses(<Dir>/build/classes/java/main)`, handled as for Maven but naming `gradle`.
  - Then `p.read("pmd", m.Dir, <Dir>/build/reports/pmd/main.xml, output)` and `p.read("spotbugs", m.Dir, <Dir>/build/reports/spotbugs/main.sarif, output)`.

**Semgrep: `func (p *pass) semgrep(files []string) error`**

- `report := <out>/semgrep.sarif`.
- `config` is `<dir>/.semgrep.yml` when that is a regular file, else `<dir>/.semgrep` when that is a directory, else `p/default`.
- `output, err := p.command("semgrep", p.dir, nil, "semgrep", append([]string{"scan", "--metrics=off", "--sarif", "--output", report, "--config", config, "--"}, files...)...)`.
- Then `p.ran("semgrep")` and `p.read("semgrep", p.dir, report, output)`.

Serves: the outcome of every item; the watchdog warning (`New`/`Analyze`); ADR-89 (no build file written, nothing installed, analyzers fetched at pinned versions, a non-zero exit is a failed analysis and never a finding).

### 4. `internal/analyze/run_test.go` (create)

All tests live here, in `package analyze`, with `// given`, `// when`, `// then` blocks.

**Existing helpers reused**
- `newGitRepo` and `writeText` (`lines_test.go:21`, `:32`)
- `pmdReport` and `pmdViolation` (`pmdxml_test.go:12`, `:19`)
- `executable` (`detect_test.go:25`)

**New helpers.** Names must not clash with the existing `touch`, `run`, `changed`, `hit`, `detect`.

- **`stubbedRepo(t, committed map[string]string) string`**: `newGitRepo`, then `filepath.EvalSymlinks`; sets `STUB_ROOT` to the real path and returns it.
- **`stubPath(t) string`** sets up the stub environment and returns the stub bin directory:
  - Resolves the real `git` with `exec.LookPath` first and symlinks it into a new bin dir.
  - Sets `PATH=<bin>:/usr/bin:/bin`.
  - Sets `STUB_LOG=<tempdir>/log`, `TMPDIR=t.TempDir()`, `HOME=t.TempDir()` and `XDG_CACHE_HOME=$HOME/.cache`.
  - Sets `STUB_CACHE` to the value of `os.UserCacheDir()` after those are set (standard library, not production code).
- **`stub(t, dir, name, body string)`** writes an executable `#!/bin/sh` script:
  - First it logs one line, `printf '%s|%s|%s\n' "$(basename "$0")" "$(pwd -P)" "$*"`, piped through `sed`, which replaces:
    - `$TMPDIR/r-loop-analyze-[^/ ]*` with `<out>`;
    - `$STUB_CACHE/r-loop/analyze/fetch-[^/ ]*` with `<fetch>`;
    - `$STUB_CACHE` with `<cache>`;
    - `$STUB_ROOT` with `<root>`.
  - The line is appended to `$STUB_LOG`; then `body` runs.
- **`goStub(t, bin, golangciSARIF, govulncheckSARIF string)`**: sets `GOLANGCI_SARIF` and `GOVULNCHECK_SARIF` in the environment. The body writes `$GOLANGCI_SARIF` to the `--output.sarif.path=` argument's file, and prints `$GOVULNCHECK_SARIF` when the args contain `govulncheck`.
- **`mvnStub(t, bin, modules, pmdXML, spotbugsSARIF string)`**: sets `STUB_MODULES`, `PMD_XML` and `SPOTBUGS_SARIF`.
  - When the args contain `maven-dependency-plugin`, the body creates `findsecbugs-plugin-1.14.0.jar` in the `-DoutputDirectory=` directory.
  - Otherwise, for each `d` in `$STUB_MODULES` it writes `$d/target/classes/A.class`, `$d/target/pmd.xml` and `$d/target/spotbugsSarif.json`.
- **`gradleStub(t, bin, modules, pmdXML, spotbugsSARIF string)`**: copies the file after `--init-script` to `$STUB_LOG.init`. For each module it writes `$d/build/classes/java/main/A.class`, `$d/build/reports/pmd/main.xml` and `$d/build/reports/spotbugs/main.sarif`.
- **`semgrepStub(t, bin, sarif string)`**: writes `$SEMGREP_SARIF` to the argument after `--output`.
- **`fifoSemgrepStub(t, bin)`**: makes the argument after `--output` a FIFO (`mkfifo`), writes its path to `$STUB_LOG.fifo`, and exits 0.
- **`sarifHit(rule, uri string, line int) string`** returns `{"runs":[{"results":[{"ruleId":"<rule>","level":"warning","message":{"text":"<rule> hit"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"<uri>"},"region":{"startLine":<line>}}}]}]}]}`.
- **`emptySARIF`** is `{"runs":[]}`, inlined as a literal.
- **`stubLog(t) []string`** returns the log lines, or an empty slice when there is no log.
- **`analyzed(t, a, dir) core.Analysis`** fails the test on an error.
- **`summaries([]core.Finding) []string`** returns `Title + " " + strings.Join(Files, ",")` for each finding.
- **`gitStatus(t, dir) string`** is `git status --porcelain --untracked-files=all`, trimmed.

The log prefix is `go`, `mvn`, `mvnw`, `gradle` or `semgrep`. Expected log lines below are written as `<name>|<cwd>|<args>`.

Serves: the test item.

## Tests

Write these first, in `internal/analyze/run_test.go`. Unless a test says otherwise, it uses `New(time.Minute)`. Each Java fixture's `.gitignore` commits `target/` and `build/`.

**Go**

1. `TestAnalyzeRunsGolangciLintWithGosecInEachTouchedGoModule`
   - Given: `go.mod` and `tools/go.mod` committed; untracked `main.go` and `tools/t.go`; `goStub` with `{"runs":[]}`.
   - Expected log: `go|<root>|run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run --issues-exit-code=0 --path-mode=abs --output.sarif.path=<out>/golangci-1.sarif --enable=gosec` and `go|<root>/tools|… --output.sarif.path=<out>/golangci-2.sarif --enable=gosec`.
   - Also shows that no govulncheck runs when only `.go` files changed.
   - Covers: the Go command line, each touched module, `<n>` numbering, the `gosec` rule.
2. `TestAnalyzeLeavesGosecToTheRepositorysGolangciConfig` (table)
   - Rows: `tools/.golangci.yml`, `tools/.golangci.yaml`, `tools/.golangci.toml`, `tools/.golangci.json`, and `.golangci.yml` at the root (a parent).
   - Given: `tools/go.mod` and the row's config committed; untracked `tools/t.go`.
   - Expected: the log is exactly the one golangci line in `<root>/tools`, without `--enable=gosec`.
   - Covers: the `gosec` rule's config lookup up the parents.
3. `TestAnalyzeRunsGovulncheckWhenGoModOrGoSumChanges` (table)
   - Rows: `go.mod`, `go.sum`.
   - Given: `go.mod` = `module x\n` and empty `go.sum` committed; the row's file is rewritten with a new line.
   - Expected log: the golangci line, then `go|<root>|run golang.org/x/vuln/cmd/govulncheck@v1.8.0 -format sarif ./...`.
   - Covers: the govulncheck trigger and its command line.
4. `TestAnalyzeReadsTheGolangciAndGovulncheckReports`
   - Given: `go.mod` committed and modified; untracked `main.go`; golangci SARIF `sarifHit("errcheck","main.go",1)`; govulncheck SARIF `sarifHit("GO-2026-0001","go.mod",1)`.
   - Expected summaries: `govulncheck/GO-2026-0001: GO-2026-0001 hit go.mod`, `golangci-lint/errcheck: errcheck hit main.go`.
   - Expected `Command`: `golangci-lint, govulncheck`.
   - Covers: Go reports read per module; govulncheck read from stdout.
5. `TestAnalyzeReadsReportsUnderTheRealPathOfASymlinkedDirectory`
   - Given: a symlink to the repo; the golangci SARIF uri is the absolute real path `<root>/main.go`; untracked `main.go`.
   - When: `Analyze(link)`.
   - Expected summaries: `golangci-lint/errcheck: errcheck hit main.go`.
   - Covers: the `EvalSymlinks` edge (`--path-mode=abs` paths are filtered correctly).

**Maven**

6. `TestAnalyzeRunsMavenOnceAtTheBuildRootForTheTouchedModules`
   - Given: `pom.xml`, `api/pom.xml`, `core/pom.xml` and `web/pom.xml` committed; untracked `api/src/main/java/A.java` and `core/src/main/java/C.java`; `mvnStub` with modules `api core`, `pmdReport("")` and `{"runs":[]}`.
   - Expected log:
     - `mvn|<root>|-q -B org.apache.maven.plugins:maven-dependency-plugin:3.11.0:copy -Dartifact=com.h3xstream.findsecbugs:findsecbugs-plugin:1.14.0 -DoutputDirectory=<fetch>`
     - `mvn|<root>|-q -B -DskipTests -pl api,core -am compile org.apache.maven.plugins:maven-pmd-plugin:3.28.0:pmd com.github.spotbugs:spotbugs-maven-plugin:4.10.4.1:spotbugs -Dspotbugs.sarifOutput=true -Dspotbugs.pluginList=<cache>/r-loop/analyze/findsecbugs-plugin-1.14.0.jar`
   - Covers: one Maven run at the build root, `-pl` for the touched modules only, the command line.
7. `TestAnalyzeRunsTheMavenWrapperWithTheRootModuleAsDot`
   - Given: a single `pom.xml`; an executable `mvnw` stub at the root, written with `stub(t, root, "mvnw", …)` using the `mvnStub` body; untracked `src/main/java/A.java`.
   - Expected: the analysis line is `mvnw|<root>|-q -B -DskipTests -pl . -am compile …`.
   - Covers: the runner from `Detect` and the `.` module.
8. `TestAnalyzeFetchesTheFindSecBugsJarOnceIntoTheUserCache`
   - When: `Analyze` runs twice on test 6's repo.
   - Expected log: `[fetch line, analysis line, analysis line]`, and `<STUB_CACHE>/r-loop/analyze/findsecbugs-plugin-1.14.0.jar` exists.
   - Covers: the jar fetched once, cached in the user cache.
9. `TestAnalyzeReadsPmdAndSpotbugsReportsFromEachTouchedMavenModule`
   - Given: modules `api core`; untracked `api/src/main/java/X.java` and `core/src/main/java/X.java`; PMD `pmdReport(pmdViolation("src/main/java/X.java",1,3,"UnusedLocalVariable"))`; SpotBugs `sarifHit("SQL_INJECTION","src/main/java/X.java",1)`.
   - Expected summaries:
     - `pmd/UnusedLocalVariable: Avoid unused local variables such as 'x'. api/src/main/java/X.java`
     - `spotbugs/SQL_INJECTION: SQL_INJECTION hit api/src/main/java/X.java`
     - the same two for `core/…`
   - Expected `Command`: `pmd, spotbugs`.
   - Covers: Maven reports read from each module.

**Gradle**

10. `TestAnalyzeRunsGradleOnceAtTheBuildRootWithTheInitScript`
    - Given: `settings.gradle`, `app/build.gradle` and `libs/core/build.gradle` committed; untracked `app/src/main/java/A.java` and `libs/core/src/main/java/C.java`.
    - Expected log: `gradle|<root>|-q --init-script <out>/init.gradle :app:pmdMain :app:spotbugsMain :libs:core:pmdMain :libs:core:spotbugsMain`.
    - Covers: the Gradle command line, once at the build root, nested task paths.
11. `TestAnalyzeNamesTheRootGradleProjectsTasksWithoutAPrefix`
    - Given: a lone `build.gradle`; untracked `src/main/java/A.java`.
    - Expected log: `gradle|<root>|-q --init-script <out>/init.gradle :pmdMain :spotbugsMain`.
    - Covers: the root project's tasks.
12. `TestAnalyzeHandsGradleTheEmbeddedInitScriptWithThePinnedVersions`
    - Given: test 11's repo; `gradleStub` copies the `--init-script` file to `$STUB_LOG.init`.
    - Expected: the copied file equals, byte for byte, a hard-coded literal in the test: `init.gradle`'s text from Changes §2 with `{{spotbugsGradlePlugin}}` → `6.5.12` and `{{findSecBugs}}` → `1.14.0`.
    - Covers: the Go code's behaviour for the Gradle item: it writes the embedded script and substitutes `versions.go`'s pins. Gradle's handling of the script, including the repository fallback (codex-r1-1) and existing SpotBugs configuration, is proved by the real-Gradle check under Changes §2, since `go test` runs no JDK (codex-r2-3).
13. `TestAnalyzeReadsPmdAndSpotbugsReportsFromEachTouchedGradleModule`
    - As test 9, with modules `app libs/core`.
    - Expected summaries: the `pmd/…` and `spotbugs/…` lines for `app/src/main/java/X.java` and `libs/core/src/main/java/X.java`.
    - Covers: Gradle reports read from each module.

**Java edge cases**

14. `TestAnalyzeRequiresNoReportFromAModuleWithoutMainClasses` (table)
    - Rows: Maven (`it/pom.xml` under a root pom), Gradle (`it/build.gradle` under `settings.gradle`).
    - Given: untracked `it/src/test/java/T.java`; stub modules `""`, so no classes and no reports are written.
    - Expected: no error, `Findings` `[]core.Finding{}`, `Command` `pmd, spotbugs`.
    - Covers: the q2 answer.
15. `TestAnalyzeIgnoresAStaleReportLeftByAnEarlierRun` (table)
    - Rows: Maven, with `core/target/pmd.xml` pre-written holding one violation; Gradle, with `app/build/reports/pmd/main.xml` pre-written.
    - Given: the stub writes the classes and the SpotBugs report but no PMD report.
    - Expected errors: `pmd: no report at core/target/pmd.xml` and `pmd: no report at app/build/reports/pmd/main.xml`.
    - Covers: stale reports deleted before the build.
16. `TestAnalyzeWritesNoFileIntoTheTreeButBuildOutput` (table)
    - Rows: Maven module `core`, Gradle module `app`.
    - Expected: after `Analyze`, `gitStatus` equals `?? core/src/main/java/A.java` or `?? app/src/main/java/A.java`, the only untracked file; the `.gitignore` covers `target/` and `build/`.
    - Covers: no build file, init script or report written into the tree.

**Semgrep and Command**

17. `TestAnalyzeRunsSemgrepOverTheChangedFiles` (table)
    - Rows: no config gives `--config p/default`; committed `.semgrep.yml` gives `--config <root>/.semgrep.yml`; committed `.semgrep/rules.yml` gives `--config <root>/.semgrep`.
    - Given: `README.md` committed and modified; untracked `docs/new.md`.
    - Expected log: `semgrep|<root>|scan --metrics=off --sarif --output <out>/semgrep.sarif --config <row> -- README.md docs/new.md`.
    - Covers: the Semgrep command line, config choice, every changed file.
18. `TestAnalyzeReadsTheSemgrepReport`
    - Given: untracked `app.py`; SARIF `sarifHit("python.lang.security.audit.eval","app.py",1)`.
    - Expected summaries: `semgrep/python.lang.security.audit.eval: python.lang.security.audit.eval hit app.py`.
    - Expected `Command`: `semgrep`.
    - Covers: the Semgrep report read.
19. `TestAnalyzeSkipsSemgrepWhenItIsNotOnPath`
    - Given: a Go repo with only `goStub`; untracked `main.go`.
    - Expected: the log is the one golangci line; `Command` is `golangci-lint`.
    - Covers: Semgrep only when present.
20. `TestAnalyzeRunsNothingWhenNothingChanged`
    - Given: `go.mod` and `main.go` committed, with go and semgrep stubs.
    - Expected: the log is empty and the analysis is `core.Analysis{Findings: []core.Finding{}, Command: ""}`.
    - Covers: no whole-tree Semgrep scan and no toolchain on an empty change.
21. `TestAnalyzeRunsNoToolchainForAChangeWithoutSourceFiles`
    - Given: a Go repo; `README.md` modified; no semgrep stub.
    - Expected: the log is empty; `Command` is `""`.
    - Covers: only touched toolchains run.
22. `TestAnalyzeNamesEveryToolThatRanInCommand`
    - Given: root `go.mod` modified, untracked `main.go`, `svc/pom.xml` committed, untracked `svc/src/main/java/A.java`; go, mvn and semgrep stubs.
    - Expected `Command`: `golangci-lint, govulncheck, pmd, spotbugs, semgrep`.
    - Covers: the `Command` contract.

**Failures**

23. `TestAnalyzeFailsOnAToolsNonZeroExit` (table; each row's stubs exit as stated; expected error messages):
    - golangci exits 3 after `echo build failed`: `golangci-lint: exit status 3\nbuild failed\n`
    - govulncheck prints `loading packages` to stderr and exits 1, while golangci succeeds: `govulncheck: exit status 1\nloading packages\n`
    - govulncheck prints `partial` to stdout and `loading packages` to stderr, then exits 1: `govulncheck: exit status 1\npartial\nloading packages\n` (codex-r1-2)
    - the `dependency:copy` call exits 1 after `echo '[ERROR] artifact not found'`: `find-sec-bugs: exit status 1\n[ERROR] artifact not found\n`
    - the Maven analysis exits 1 after `echo '[ERROR] COMPILATION ERROR'`: `maven: exit status 1\n[ERROR] COMPILATION ERROR\n`
    - gradle exits 1 after `echo "Task 'pmdMain' not found"`: `gradle: exit status 1\nTask 'pmdMain' not found\n`
    - semgrep exits 2 after `echo 'invalid config' >&2`: `semgrep: exit status 2\ninvalid config\n`
    - Covers: non-zero exit → failure naming the tool, with its output.
24. `TestAnalyzeFailsWhenTheFetchWritesNoJar`
    - Given: the `dependency:copy` stub exits 0 and writes nothing.
    - Expected error: `find-sec-bugs: dependency:copy wrote no findsecbugs-plugin-1.14.0.jar`.
    - Covers: the missing fetched jar.
25. `TestAnalyzeFailsOnAMissingReport` (table; expected errors):
    - golangci writes nothing: `golangci-lint: no report at golangci-1.sarif`
    - the Maven stub writes classes and SpotBugs and prints `[INFO] skipped`: `pmd: no report at core/target/pmd.xml\n[INFO] skipped\n`
    - the Maven stub writes classes and PMD only: `spotbugs: no report at core/target/spotbugsSarif.json`
    - Gradle without the PMD report: `pmd: no report at app/build/reports/pmd/main.xml`
    - Gradle without the SpotBugs report: `spotbugs: no report at app/build/reports/spotbugs/main.sarif`
    - semgrep writes nothing: `semgrep: no report at semgrep.sarif`
    - Covers: a missing report in a module with main classes fails, with the producing command's output.
26. `TestAnalyzeFailsOnAReportThatDoesNotParse` (table; the JSON and XML texts are the standard library's):
    - golangci SARIF `{`: `golangci-lint: sarif: unexpected end of JSON input in golangci-1.sarif`
    - govulncheck prints nothing: `govulncheck: sarif: unexpected end of JSON input in govulncheck-1.sarif`
    - Maven `pmd.xml` = `<pmd>`: `pmd: xml: XML syntax error on line 1: unexpected EOF in core/target/pmd.xml`
    - Gradle `main.sarif` = `{`: `spotbugs: sarif: unexpected end of JSON input in app/build/reports/spotbugs/main.sarif`
    - semgrep SARIF `{`: `semgrep: sarif: unexpected end of JSON input in semgrep.sarif`
    - Covers: an unparsable report fails, naming the tool.
27. `TestAnalyzeKeepsTheLast4096BytesOfAFailingToolsOutput`
    - Given: the golangci stub prints `HEAD` and then 4096 `b`s, and exits 1.
    - Expected error: `"golangci-lint: exit status 1\n" + strings.Repeat("b", 4096)`.
    - Covers: the 4096-byte tail.
27a. `TestAnalyzeKeepsTrailingWhitespaceInTheOutputTail`
    - Given: the golangci stub runs `printf 'failed  \n\n'` and exits 1.
    - Expected error: `"golangci-lint: exit status 1\nfailed  \n\n"`.
    - Covers: codex-r1-4, the tail kept byte for byte.
28. `TestAnalyzeFailsWhenTheAnalysisOutlivesTheTimeout`
    - Given: `New(300 * time.Millisecond)`; the golangci stub prints `started` and then runs `sleep 30`.
    - Expected error: `golangci-lint: timed out after 300ms: context deadline exceeded\nstarted\n`.
    - Covers: the timeout, including the killed process group.
29. `TestAnalyzeFailsAsInterruptedWhenTheCallerCancels`
    - Given: the stub prints `started`, touches `$STUB_LOG.started` and runs `sleep 30`. A goroutine polls for that file with a 10s deadline, then calls `cancel()`.
    - Expected error: `golangci-lint: interrupted: context canceled\nstarted\n`.
    - Covers: the caller-cancel error path.
29a. `TestAnalyzeFailsWhenInterruptedAfterTheLastCommand`
    - Given: `README.md` modified; `fifoSemgrepStub`; `ctx, cancel := context.WithCancel(context.Background())`.
    - A goroutine polls for `$STUB_LOG.fifo` with a 10s deadline and opens that FIFO for writing. The open blocks until `Analyze` opens it to read the report, which happens only after semgrep has exited. The goroutine then calls `cancel()`, writes `{"runs":[]}` and closes.
    - Expected error: `analyze: interrupted: context canceled`.
    - Covers: codex-r1-3. With no command after it, the end-of-analysis check still fails a run whose context ended. The timed-out wording comes from the same `cause`, which test 28 pins.
30. `TestAnalyzeFailsWhenAStaleReportCannotBeRemoved`
    - Given: `core/target/pmd.xml` is a non-empty directory.
    - Expected error: `"maven: remove " + root + "/core/target/pmd.xml: directory not empty"`; the log holds only the fetch line.
    - Covers: the `removeReports` error.
31. `TestAnalyzeFailsWhenTheClassesDirectoryCannotBeRead`
    - Given: the Maven stub succeeds; the test sets `core/target/classes/sub` to mode `0o000`, restoring `0o755` in `t.Cleanup`.
    - Expected: the error starts with `maven: open ` and `errors.Is(err, fs.ErrPermission)`.
    - Covers: the `hasClasses` error.
32. `TestAnalyzeFailsWithoutAUserCacheDirectory`
    - Given: a Maven repo; `HOME` and `XDG_CACHE_HOME` set to `""`.
    - Expected: the error starts with `find-sec-bugs: `, and the log is empty: no fetch, no build.
    - Covers: the `os.UserCacheDir` error.
33. `TestAnalyzeFailsWithoutATempDirectory`
    - Given: `TMPDIR` set to a missing path; untracked `main.go`.
    - Expected: the error starts with `analyze: ` and `errors.Is(err, fs.ErrNotExist)`; the log is empty.
    - Covers: the `MkdirTemp` error.
34. `TestAnalyzeFailsForAMissingDirectory`
    - When: `Analyze` on `t.TempDir()+"/missing"`.
    - Expected: the error starts with `analyze: ` and `errors.Is(err, fs.ErrNotExist)`.
    - Covers: the `EvalSymlinks` error.

## Left out

- **Parallel toolchains**: no obligation asks for speed, and parallel runs complicate the single timeout attribution and the `Command` order.
- **A dedicated tail ring buffer**: the output is buffered whole and sliced. Maven/Gradle output with `-q` is small, and no obligation bounds memory.
- **Trimming the tail to a UTF-8 rune boundary**: the item says "last 4096 bytes", and phase 49's JSON encoding already replaces invalid UTF-8.
- **A `config` import or a `New(cfg config.Analyze)` signature**: only the timeout is needed, and adapters take primitives.
- **Reading reports from upstream `-am` modules**: only touched modules' changes are reviewed, and `Findings` would drop their lines anyway.
- **Handling custom Maven `<directory>` or Gradle `buildDir` layouts**: no repository in scope uses them. With such a layout the reports land elsewhere and the analysis fails loudly, naming the missing path.
- **Splitting Semgrep's file list for argv limits**: no obligation names a change large enough to exceed `ARG_MAX`.
- **Exporting the version constants**: nothing outside the package reads them.

## Assumptions

- **`New`'s signature is `New(timeout time.Duration) *Analyzer`.** Phase 49 wires it as `analyze.New(cfg.Analyze.Timeout)` (its `analyze.New(cfg)` is shorthand).
- **`Changed`'s git errors are returned unchanged.** Under the analysis timeout, a killed `git diff` surfaces as `git diff: …` from `lines.go:52`; a hung `git diff` is not a source this phase can name.
- **`Command` is `""` when no tool ran.** The tech design says it "lists the tools that ran"; an empty list joins to `""`.
- **Projects ignore `target/` and `build/` in git.** The analysis writes build output there exactly as the project's own build does. A project that tracks build output would see it as untracked files; that is the project's existing state, not this phase's.
- **Gradle project paths follow the directory layout** (`app/sub` → `:app:sub`), which is Gradle's default `include` mapping. A remapped `projectDir` makes Gradle fail with "task not found", reported as a `gradle` failure.
- **The pinned golangci-lint v2.14.0 and govulncheck v1.8.0 require Go ≥ 1.26.** `go run …@<v>` fetches that toolchain under the default `GOTOOLCHAIN=auto`; a user with `GOTOOLCHAIN=local` and an older Go gets a `golangci-lint: exit status 1` failure with Go's message. The item mandates the newest release.
- **The unit tests use stubs only and do not run real Maven or Gradle.** The init script's behaviour under Gradle is proved by the real-Gradle check under Changes §2, run by the planner and re-run by the implementer, not by `go test`, which must not need a JDK. Real-toolchain behaviour of `init.gradle` and the Maven goals is taken from the plugin descriptors and sources checked in Why this approach; `go test -race ./internal/analyze/...` is the acceptance command.
- **The watchdog's warning is resolved** by building `New` and `Analyze` in `run.go`, covered by every test above.
