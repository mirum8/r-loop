package analyze

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
)

const javaIgnore = "target/\nbuild/\n"

func stubbedRepo(t *testing.T, committed map[string]string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(newGitRepo(t, committed))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_ROOT", dir)
	return dir
}

func stubPath(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("STUB_LOG", filepath.Join(t.TempDir(), "log"))
	t.Setenv("TMPDIR", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUB_CACHE", cache)
	return bin
}

func stub(t *testing.T, dir, name, body string) {
	t.Helper()
	script := `#!/bin/sh
printf '%s|%s|%s\n' "$(basename "$0")" "$(pwd -P)" "$*" | sed -e "s|$TMPDIR/r-loop-analyze-[^/ ]*|<out>|g" -e "s|$STUB_CACHE/r-loop/analyze/fetch-[^/ ]*|<fetch>|g" -e "s|$STUB_CACHE|<cache>|g" -e "s|$STUB_ROOT|<root>|g" >> "$STUB_LOG"
` + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

const goStubBody = `for a in "$@"; do
  case "$a" in
    --output.sarif.path=*) printf '%s' "$GOLANGCI_SARIF" > "${a#--output.sarif.path=}" ;;
    *govulncheck*) printf '%s' "$GOVULNCHECK_SARIF" ;;
  esac
done`

func goStub(t *testing.T, bin, golangciSARIF, govulncheckSARIF string) {
	t.Helper()
	t.Setenv("GOLANGCI_SARIF", golangciSARIF)
	t.Setenv("GOVULNCHECK_SARIF", govulncheckSARIF)
	stub(t, bin, "go", goStubBody)
}

const fetchJar = `for a in "$@"; do
  case "$a" in
    -DoutputDirectory=*) : > "${a#-DoutputDirectory=}/findsecbugs-plugin-1.14.0.jar" ;;
  esac
done`

func javaBuild(classes, pmd, sarif string) string {
	return `for d in $STUB_MODULES; do
  mkdir -p "$d/` + classes + `" && : > "$d/` + classes + `/A.class"
  if [ -n "$PMD_XML" ]; then mkdir -p "$(dirname "$d/` + pmd + `")" && printf '%s' "$PMD_XML" > "$d/` + pmd + `"; fi
  if [ -n "$SPOTBUGS_SARIF" ]; then mkdir -p "$(dirname "$d/` + sarif + `")" && printf '%s' "$SPOTBUGS_SARIF" > "$d/` + sarif + `"; fi
done`
}

func mavenBody(fetch, build string) string {
	return `case "$*" in
  *maven-dependency-plugin*)
` + fetch + `
  ;;
  *)
` + build + `
  ;;
esac`
}

var mavenBuild = javaBuild("target/classes", "target/pmd.xml", "target/spotbugsSarif.json")

func javaEnv(t *testing.T, modules, pmdXML, spotbugsSARIF string) {
	t.Helper()
	t.Setenv("STUB_MODULES", modules)
	t.Setenv("PMD_XML", pmdXML)
	t.Setenv("SPOTBUGS_SARIF", spotbugsSARIF)
}

func mvnStub(t *testing.T, bin, modules, pmdXML, spotbugsSARIF string) {
	t.Helper()
	javaEnv(t, modules, pmdXML, spotbugsSARIF)
	stub(t, bin, "mvn", mavenBody(fetchJar, mavenBuild))
}

const copyInitScript = `prev=
for a in "$@"; do
  if [ "$prev" = --init-script ]; then cp "$a" "$STUB_LOG.init"; fi
  prev=$a
done`

var gradleBuild = javaBuild("build/classes/java/main", "build/reports/pmd/main.xml", "build/reports/spotbugs/main.sarif")

func gradleStub(t *testing.T, bin, modules, pmdXML, spotbugsSARIF string) {
	t.Helper()
	javaEnv(t, modules, pmdXML, spotbugsSARIF)
	stub(t, bin, "gradle", copyInitScript+"\n"+gradleBuild)
}

func semgrepStub(t *testing.T, bin, sarif string) {
	t.Helper()
	t.Setenv("SEMGREP_SARIF", sarif)
	stub(t, bin, "semgrep", `prev=
for a in "$@"; do
  if [ "$prev" = --output ]; then printf '%s' "$SEMGREP_SARIF" > "$a"; fi
  prev=$a
done`)
}

func fifoSemgrepStub(t *testing.T, bin string) {
	t.Helper()
	stub(t, bin, "semgrep", `prev=
for a in "$@"; do
  if [ "$prev" = --output ]; then mkfifo "$a" && printf '%s' "$a" > "$STUB_LOG.fifo"; fi
  prev=$a
done
exit 0`)
}

func sarifHit(rule, uri string, line int) string {
	return `{"runs":[{"results":[{"ruleId":"` + rule + `","level":"warning","message":{"text":"` + rule + ` hit"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"` + uri + `"},"region":{"startLine":` + strconv.Itoa(line) + `}}}]}]}]}`
}

func stubLog(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(os.Getenv("STUB_LOG"))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func analyzed(t *testing.T, a *Analyzer, dir string) core.Analysis {
	t.Helper()
	analysis, err := a.Analyze(context.Background(), dir)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	return analysis
}

func analyzeErr(t *testing.T, a *Analyzer, dir string) string {
	t.Helper()
	_, err := a.Analyze(context.Background(), dir)
	if err == nil {
		t.Fatal("Analyze: expected an error")
	}
	return err.Error()
}

func summaries(findings []core.Finding) []string {
	out := []string{}
	for _, f := range findings {
		out = append(out, f.Title+" "+strings.Join(f.Files, ","))
	}
	return out
}

func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("no %s within 10s", path)
}

const golangciArgs = "run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run --issues-exit-code=0 --path-mode=abs --max-same-issues=0 --max-issues-per-linter=0 --output.sarif.path="

const fetchLine = "mvn|<root>|-q -B org.apache.maven.plugins:maven-dependency-plugin:3.11.0:copy -Dartifact=com.h3xstream.findsecbugs:findsecbugs-plugin:1.14.0 -DoutputDirectory=<fetch>"

const mavenGoals = "compile org.apache.maven.plugins:maven-pmd-plugin:3.28.0:pmd com.github.spotbugs:spotbugs-maven-plugin:4.10.4.1:spotbugs -Dspotbugs.sarifOutput=true -Dspotbugs.pluginList=<cache>/r-loop/analyze/findsecbugs-plugin-1.14.0.jar"

func mavenModulesRepo(t *testing.T) string {
	t.Helper()
	dir := stubbedRepo(t, map[string]string{".gitignore": javaIgnore, "pom.xml": "", "api/pom.xml": "", "core/pom.xml": "", "web/pom.xml": ""})
	writeText(t, dir, "api/src/main/java/A.java", "class A {}\n")
	writeText(t, dir, "core/src/main/java/C.java", "class C {}\n")
	return dir
}

func mavenCoreRepo(t *testing.T) string {
	t.Helper()
	dir := stubbedRepo(t, map[string]string{".gitignore": javaIgnore, "pom.xml": "", "core/pom.xml": ""})
	writeText(t, dir, "core/src/main/java/A.java", "class A {}\n")
	return dir
}

func gradleAppRepo(t *testing.T) string {
	t.Helper()
	dir := stubbedRepo(t, map[string]string{".gitignore": javaIgnore, "settings.gradle": "", "app/build.gradle": ""})
	writeText(t, dir, "app/src/main/java/A.java", "class A {}\n")
	return dir
}

func goRepo(t *testing.T) string {
	t.Helper()
	dir := stubbedRepo(t, map[string]string{"go.mod": "module x\n"})
	writeText(t, dir, "main.go", "package main\n")
	return dir
}

func goModChangedRepo(t *testing.T) string {
	t.Helper()
	dir := stubbedRepo(t, map[string]string{"go.mod": "module x\n"})
	writeText(t, dir, "go.mod", "module x\n\ngo 1.25\n")
	writeText(t, dir, "main.go", "package main\n")
	return dir
}

func semgrepRepo(t *testing.T) string {
	t.Helper()
	dir := stubbedRepo(t, map[string]string{"README.md": "a\n"})
	writeText(t, dir, "README.md", "b\n")
	return dir
}

func TestAnalyzeRunsGolangciLintWithGosecInEachTouchedGoModule(t *testing.T) {
	// given
	bin := stubPath(t)
	goStub(t, bin, `{"runs":[]}`, `{"runs":[]}`)
	dir := stubbedRepo(t, map[string]string{"go.mod": "module x\n", "tools/go.mod": "module t\n"})
	writeText(t, dir, "main.go", "package main\n")
	writeText(t, dir, "tools/t.go", "package t\n")

	// when
	analyzed(t, New(time.Minute), dir)

	// then
	actual := stubLog(t)
	expected := []string{
		"go|<root>|" + golangciArgs + "<out>/golangci-1.sarif --enable=gosec",
		"go|<root>/tools|" + golangciArgs + "<out>/golangci-2.sarif --enable=gosec",
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func TestAnalyzeLiftsGolangciLintsIssueCaps(t *testing.T) {
	// given
	bin := stubPath(t)
	goStub(t, bin, `{"runs":[]}`, `{"runs":[]}`)
	dir := goRepo(t)

	// when
	analyzed(t, New(time.Minute), dir)

	// then
	actual := stubLog(t)
	expected := []string{"go|<root>|run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@" + golangciLintVersion + " run --issues-exit-code=0 --path-mode=abs --max-same-issues=0 --max-issues-per-linter=0 --output.sarif.path=<out>/golangci-1.sarif --enable=gosec"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func TestAnalyzeGivesGolangciLintACachePerModuleDirectory(t *testing.T) {
	// given
	bin := stubPath(t)
	goStub(t, bin, `{"runs":[]}`, `{"runs":[]}`)
	stub(t, bin, "go", `printf '%s|%s\n' "$GOLANGCI_LINT_CACHE" "$GOCACHE" >> "$STUB_LOG.cache"
`+goStubBody)
	t.Setenv("GOCACHE", "/shared/gocache")
	first, second := goRepo(t), goRepo(t)

	// when
	analyzed(t, New(time.Minute), first)
	analyzed(t, New(time.Minute), second)
	analyzed(t, New(time.Minute), first)

	// then
	data, err := os.ReadFile(os.Getenv("STUB_LOG") + ".cache")
	if err != nil {
		t.Fatal(err)
	}
	actual := strings.Split(strings.TrimSpace(string(data)), "\n")
	prefix := filepath.Join(os.Getenv("STUB_CACHE"), "r-loop", "golangci") + "/"
	if len(actual) != 3 || !strings.HasPrefix(actual[0], prefix) || !strings.HasPrefix(actual[1], prefix) || !strings.HasSuffix(actual[0], "|/shared/gocache") {
		t.Fatalf("actual %q, expected three caches under %s with GOCACHE kept", actual, prefix)
	}
	if actual[0] == actual[1] || actual[0] != actual[2] {
		t.Fatalf("actual %q, expected one cache per module directory", actual)
	}
}

func TestAnalyzeLeavesGosecToTheRepositorysGolangciConfig(t *testing.T) {
	for _, config := range []string{"tools/.golangci.yml", "tools/.golangci.yaml", "tools/.golangci.toml", "tools/.golangci.json", ".golangci.yml"} {
		t.Run(config, func(t *testing.T) {
			// given
			bin := stubPath(t)
			goStub(t, bin, `{"runs":[]}`, `{"runs":[]}`)
			dir := stubbedRepo(t, map[string]string{"tools/go.mod": "module t\n", config: ""})
			writeText(t, dir, "tools/t.go", "package t\n")

			// when
			analyzed(t, New(time.Minute), dir)

			// then
			actual := stubLog(t)
			expected := []string{"go|<root>/tools|" + golangciArgs + "<out>/golangci-1.sarif"}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("actual %q, expected %q", actual, expected)
			}
		})
	}
}

func TestAnalyzeRunsGovulncheckWhenGoModOrGoSumChanges(t *testing.T) {
	for _, file := range []string{"go.mod", "go.sum"} {
		t.Run(file, func(t *testing.T) {
			// given
			bin := stubPath(t)
			goStub(t, bin, `{"runs":[]}`, `{"runs":[]}`)
			dir := stubbedRepo(t, map[string]string{"go.mod": "module x\n", "go.sum": ""})
			writeText(t, dir, file, "module x\n\nnew\n")

			// when
			analyzed(t, New(time.Minute), dir)

			// then
			actual := stubLog(t)
			expected := []string{
				"go|<root>|" + golangciArgs + "<out>/golangci-1.sarif --enable=gosec",
				"go|<root>|run golang.org/x/vuln/cmd/govulncheck@v1.7.0 -format sarif ./...",
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("actual %q, expected %q", actual, expected)
			}
		})
	}
}

func TestAnalyzeReadsTheGolangciAndGovulncheckReports(t *testing.T) {
	// given
	bin := stubPath(t)
	goStub(t, bin, sarifHit("errcheck", "main.go", 1), sarifHit("GO-2026-0001", "go.mod", 1))
	dir := goModChangedRepo(t)

	// when
	actual := analyzed(t, New(time.Minute), dir)

	// then
	expectedSummaries := []string{"govulncheck/GO-2026-0001: GO-2026-0001 hit go.mod", "golangci-lint/errcheck: errcheck hit main.go"}
	if actualSummaries := summaries(actual.Findings); !reflect.DeepEqual(actualSummaries, expectedSummaries) || actual.Command != "golangci-lint, govulncheck" {
		t.Fatalf("actual %q %q, expected %q %q", actualSummaries, actual.Command, expectedSummaries, "golangci-lint, govulncheck")
	}
}

func TestAnalyzeReadsReportsUnderTheRealPathOfASymlinkedDirectory(t *testing.T) {
	// given
	bin := stubPath(t)
	dir := goRepo(t)
	goStub(t, bin, sarifHit("errcheck", dir+"/main.go", 1), `{"runs":[]}`)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}

	// when
	actual := analyzed(t, New(time.Minute), link)

	// then
	expected := []string{"golangci-lint/errcheck: errcheck hit main.go"}
	if actualSummaries := summaries(actual.Findings); !reflect.DeepEqual(actualSummaries, expected) {
		t.Fatalf("actual %q, expected %q", actualSummaries, expected)
	}
}

func TestAnalyzeRunsMavenOnceAtTheBuildRootForTheTouchedModules(t *testing.T) {
	// given
	bin := stubPath(t)
	mvnStub(t, bin, "api core", pmdReport(""), `{"runs":[]}`)
	dir := mavenModulesRepo(t)

	// when
	analyzed(t, New(time.Minute), dir)

	// then
	actual := stubLog(t)
	expected := []string{fetchLine, "mvn|<root>|-q -B -DskipTests -pl api,core -am " + mavenGoals}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func TestAnalyzeRunsTheMavenWrapperWithTheRootModuleAsDot(t *testing.T) {
	// given
	stubPath(t)
	javaEnv(t, ".", pmdReport(""), `{"runs":[]}`)
	dir := stubbedRepo(t, map[string]string{".gitignore": javaIgnore, "pom.xml": ""})
	stub(t, dir, "mvnw", mavenBody(fetchJar, mavenBuild))
	writeText(t, dir, "src/main/java/A.java", "class A {}\n")

	// when
	analyzed(t, New(time.Minute), dir)

	// then
	actual := stubLog(t)
	expected := []string{"mvnw" + strings.TrimPrefix(fetchLine, "mvn"), "mvnw|<root>|-q -B -DskipTests -pl . -am " + mavenGoals}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func TestAnalyzeFetchesTheFindSecBugsJarOnceIntoTheUserCache(t *testing.T) {
	// given
	bin := stubPath(t)
	mvnStub(t, bin, "api core", pmdReport(""), `{"runs":[]}`)
	dir := mavenModulesRepo(t)

	// when
	analyzed(t, New(time.Minute), dir)
	analyzed(t, New(time.Minute), dir)

	// then
	analysisLine := "mvn|<root>|-q -B -DskipTests -pl api,core -am " + mavenGoals
	actual := stubLog(t)
	expected := []string{fetchLine, analysisLine, analysisLine}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("STUB_CACHE"), "r-loop", "analyze", "findsecbugs-plugin-1.14.0.jar")); err != nil {
		t.Fatalf("cached jar: %v", err)
	}
}

func TestAnalyzeReadsPmdAndSpotbugsReportsFromEachTouchedMavenModule(t *testing.T) {
	// given
	bin := stubPath(t)
	mvnStub(t, bin, "api core", pmdReport(pmdViolation("src/main/java/X.java", 1, 3, "UnusedLocalVariable")), sarifHit("SQL_INJECTION", "X.java", 1))
	dir := stubbedRepo(t, map[string]string{".gitignore": javaIgnore, "pom.xml": "", "api/pom.xml": "", "core/pom.xml": ""})
	writeText(t, dir, "api/src/main/java/X.java", "class X {}\n")
	writeText(t, dir, "core/src/main/java/X.java", "class X {}\n")

	// when
	actual := analyzed(t, New(time.Minute), dir)

	// then
	expectedSummaries := []string{
		"pmd/UnusedLocalVariable: Avoid unused local variables such as 'x'. api/src/main/java/X.java",
		"spotbugs/SQL_INJECTION: SQL_INJECTION hit api/src/main/java/X.java",
		"pmd/UnusedLocalVariable: Avoid unused local variables such as 'x'. core/src/main/java/X.java",
		"spotbugs/SQL_INJECTION: SQL_INJECTION hit core/src/main/java/X.java",
	}
	if actualSummaries := summaries(actual.Findings); !reflect.DeepEqual(actualSummaries, expectedSummaries) || actual.Command != "pmd, spotbugs" {
		t.Fatalf("actual %q %q, expected %q %q", actualSummaries, actual.Command, expectedSummaries, "pmd, spotbugs")
	}
}

func TestAnalyzeResolvesMavenSpotbugsPathsAgainstTheMainSourceRoot(t *testing.T) {
	// given
	bin := stubPath(t)
	sarif := `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"SpotBugs","rules":[{"id":"PREDICTABLE_RANDOM"}]}},"results":[{"ruleId":"PREDICTABLE_RANDOM","ruleIndex":0,"level":"warning","message":{"text":"This random generator (java.util.Random) is predictable"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"demo/App.java"},"region":{"startLine":14}}}]}]}]}`
	mvnStub(t, bin, ".", pmdReport(""), sarif)
	dir := stubbedRepo(t, map[string]string{".gitignore": javaIgnore, "pom.xml": ""})
	writeText(t, dir, "src/main/java/demo/App.java", "package demo;\n")

	// when
	actual := analyzed(t, New(time.Minute), dir)

	// then
	expected := []string{"spotbugs/PREDICTABLE_RANDOM: This random generator (java.util.Random) is predictable src/main/java/demo/App.java"}
	if actualSummaries := summaries(actual.Findings); !reflect.DeepEqual(actualSummaries, expected) {
		t.Fatalf("actual %q, expected %q", actualSummaries, expected)
	}
}

func TestAnalyzeRunsGradleOnceAtTheBuildRootWithTheInitScript(t *testing.T) {
	// given
	bin := stubPath(t)
	gradleStub(t, bin, "app libs/core", pmdReport(""), `{"runs":[]}`)
	dir := stubbedRepo(t, map[string]string{".gitignore": javaIgnore, "settings.gradle": "", "app/build.gradle": "", "libs/core/build.gradle": ""})
	writeText(t, dir, "app/src/main/java/A.java", "class A {}\n")
	writeText(t, dir, "libs/core/src/main/java/C.java", "class C {}\n")

	// when
	analyzed(t, New(time.Minute), dir)

	// then
	actual := stubLog(t)
	expected := []string{"gradle|<root>|--no-daemon -q --init-script <out>/init.gradle :app:pmdMain :app:spotbugsMain :libs:core:pmdMain :libs:core:spotbugsMain"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func loneGradleRepo(t *testing.T) string {
	t.Helper()
	dir := stubbedRepo(t, map[string]string{".gitignore": javaIgnore, "build.gradle": ""})
	writeText(t, dir, "src/main/java/A.java", "class A {}\n")
	return dir
}

func TestAnalyzeNamesTheRootGradleProjectsTasksWithoutAPrefix(t *testing.T) {
	// given
	bin := stubPath(t)
	gradleStub(t, bin, ".", pmdReport(""), `{"runs":[]}`)
	dir := loneGradleRepo(t)

	// when
	analyzed(t, New(time.Minute), dir)

	// then
	actual := stubLog(t)
	expected := []string{"gradle|<root>|--no-daemon -q --init-script <out>/init.gradle :pmdMain :spotbugsMain"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func TestAnalyzeHandsGradleTheEmbeddedInitScriptWithThePinnedVersions(t *testing.T) {
	// given
	bin := stubPath(t)
	gradleStub(t, bin, ".", pmdReport(""), `{"runs":[]}`)
	dir := loneGradleRepo(t)

	// when
	analyzed(t, New(time.Minute), dir)

	// then
	actual, err := os.ReadFile(os.Getenv("STUB_LOG") + ".init")
	expected := `initscript {
    repositories {
        gradlePluginPortal()
    }
    dependencies {
        classpath 'com.github.spotbugs.snom:spotbugs-gradle-plugin:6.5.12'
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
            spotbugsPlugins 'com.h3xstream.findsecbugs:findsecbugs-plugin:1.14.0'
        }
        tasks.matching { it.name == 'spotbugsMain' }.configureEach {
            reports {
                maybeCreate('sarif').required = true
            }
        }
    }
}
`
	if err != nil || string(actual) != expected {
		t.Fatalf("actual %q, err %v, expected %q", actual, err, expected)
	}
}

func TestAnalyzeReadsPmdAndSpotbugsReportsFromEachTouchedGradleModule(t *testing.T) {
	// given
	bin := stubPath(t)
	gradleStub(t, bin, "app libs/core", pmdReport(pmdViolation("src/main/java/X.java", 1, 3, "UnusedLocalVariable")), sarifHit("SQL_INJECTION", "src/main/java/X.java", 1))
	dir := stubbedRepo(t, map[string]string{".gitignore": javaIgnore, "settings.gradle": "", "app/build.gradle": "", "libs/core/build.gradle": ""})
	writeText(t, dir, "app/src/main/java/X.java", "class X {}\n")
	writeText(t, dir, "libs/core/src/main/java/X.java", "class X {}\n")

	// when
	actual := analyzed(t, New(time.Minute), dir)

	// then
	expectedSummaries := []string{
		"pmd/UnusedLocalVariable: Avoid unused local variables such as 'x'. app/src/main/java/X.java",
		"spotbugs/SQL_INJECTION: SQL_INJECTION hit app/src/main/java/X.java",
		"pmd/UnusedLocalVariable: Avoid unused local variables such as 'x'. libs/core/src/main/java/X.java",
		"spotbugs/SQL_INJECTION: SQL_INJECTION hit libs/core/src/main/java/X.java",
	}
	if actualSummaries := summaries(actual.Findings); !reflect.DeepEqual(actualSummaries, expectedSummaries) || actual.Command != "pmd, spotbugs" {
		t.Fatalf("actual %q %q, expected %q %q", actualSummaries, actual.Command, expectedSummaries, "pmd, spotbugs")
	}
}

func TestAnalyzeRequiresNoReportFromAModuleWithoutMainClasses(t *testing.T) {
	cases := []struct {
		name      string
		committed map[string]string
		stubName  string
		body      string
	}{
		{"maven", map[string]string{".gitignore": javaIgnore, "pom.xml": "", "it/pom.xml": ""}, "mvn", mavenBody(fetchJar, mavenBuild)},
		{"gradle", map[string]string{".gitignore": javaIgnore, "settings.gradle": "", "it/build.gradle": ""}, "gradle", gradleBuild},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// given
			bin := stubPath(t)
			javaEnv(t, "", pmdReport(""), `{"runs":[]}`)
			stub(t, bin, c.stubName, c.body)
			dir := stubbedRepo(t, c.committed)
			writeText(t, dir, "it/src/test/java/T.java", "class T {}\n")

			// when
			actual := analyzed(t, New(time.Minute), dir)

			// then
			expected := core.Analysis{Findings: []core.Finding{}, Command: "pmd, spotbugs"}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("actual %+v, expected %+v", actual, expected)
			}
		})
	}
}

func TestAnalyzeIgnoresAStaleReportLeftByAnEarlierRun(t *testing.T) {
	cases := []struct {
		name     string
		repo     func(*testing.T) string
		module   string
		stale    string
		stubName string
		body     string
		expected string
	}{
		{"maven", mavenCoreRepo, "core", "core/target/pmd.xml", "mvn", mavenBody(fetchJar, mavenBuild), "pmd: no report at core/target/pmd.xml"},
		{"gradle", gradleAppRepo, "app", "app/build/reports/pmd/main.xml", "gradle", gradleBuild, "pmd: no report at app/build/reports/pmd/main.xml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// given
			bin := stubPath(t)
			javaEnv(t, c.module, "", `{"runs":[]}`)
			stub(t, bin, c.stubName, c.body)
			dir := c.repo(t)
			writeText(t, dir, c.stale, pmdReport(pmdViolation("src/main/java/A.java", 1, 3, "UnusedLocalVariable")))

			// when
			actual := analyzeErr(t, New(time.Minute), dir)

			// then
			if actual != c.expected {
				t.Fatalf("actual %q, expected %q", actual, c.expected)
			}
		})
	}
}

func TestAnalyzeWritesNoFileIntoTheTreeButBuildOutput(t *testing.T) {
	cases := []struct {
		name     string
		repo     func(*testing.T) string
		module   string
		stubName string
		body     string
		expected string
	}{
		{"maven", mavenCoreRepo, "core", "mvn", mavenBody(fetchJar, mavenBuild), "?? core/src/main/java/A.java"},
		{"gradle", gradleAppRepo, "app", "gradle", gradleBuild, "?? app/src/main/java/A.java"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// given
			bin := stubPath(t)
			javaEnv(t, c.module, pmdReport(""), `{"runs":[]}`)
			stub(t, bin, c.stubName, c.body)
			dir := c.repo(t)

			// when
			analyzed(t, New(time.Minute), dir)

			// then
			if actual := gitStatus(t, dir); actual != c.expected {
				t.Fatalf("actual %q, expected %q", actual, c.expected)
			}
		})
	}
}

func TestAnalyzeLeavesTheUntrackedFilesOfARepoWithoutIgnoresAsItFoundThem(t *testing.T) {
	gradleOutput := gradleBuild + "\nmkdir -p .gradle/8.14 && : > .gradle/8.14/fileHashes.lock"
	cases := []struct {
		name      string
		committed map[string]string
		module    string
		stubName  string
		body      string
		timeout   time.Duration
		failed    bool
		gone      string
	}{
		{"maven", map[string]string{"pom.xml": "", "core/pom.xml": ""}, "core", "mvn", mavenBody(fetchJar, mavenBuild), time.Minute, false, "core/target/classes"},
		{"gradle", map[string]string{"settings.gradle": "", "core/build.gradle": ""}, "core", "gradle", gradleOutput, time.Minute, false, ".gradle"},
		{"maven failure", map[string]string{"pom.xml": "", "core/pom.xml": ""}, "core", "mvn", mavenBody(fetchJar, mavenBuild+"\nexit 1"), time.Minute, true, "core/target/classes"},
		{"gradle timeout", map[string]string{"settings.gradle": "", "core/build.gradle": ""}, "core", "gradle", gradleOutput + "\nsleep 30", 500 * time.Millisecond, true, ".gradle"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// given
			bin := stubPath(t)
			javaEnv(t, c.module, pmdReport(""), `{"runs":[]}`)
			stub(t, bin, c.stubName, c.body)
			dir := stubbedRepo(t, c.committed)
			writeText(t, dir, "core/src/main/java/A.java", "class A {}\n")
			writeText(t, dir, "notes.txt", "mine\n")
			writeText(t, dir, "core/target/notes.txt", "mine\n")
			writeText(t, dir, "core/build/notes.txt", "mine\n")
			expected := gitStatus(t, dir)

			// when
			_, err := New(c.timeout).Analyze(context.Background(), dir)

			// then
			if (err != nil) != c.failed {
				t.Fatalf("Analyze: %v, expected failure %v", err, c.failed)
			}
			if actual := gitStatus(t, dir); actual != expected {
				t.Fatalf("actual %q, expected %q", actual, expected)
			}
			if _, err := os.Lstat(filepath.Join(dir, c.gone)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("%s: %v, expected it removed", c.gone, err)
			}
		})
	}
}

func TestAnalyzeKeepsAnUntrackedSourceFileWrittenDuringTheAnalysis(t *testing.T) {
	// given
	bin := stubPath(t)
	javaEnv(t, "core", pmdReport(""), `{"runs":[]}`)
	stub(t, bin, "mvn", mavenBody(fetchJar, mavenBuild+"\nprintf 'class B {}' > core/src/main/java/B.java"))
	dir := stubbedRepo(t, map[string]string{"pom.xml": "", "core/pom.xml": ""})
	writeText(t, dir, "core/src/main/java/A.java", "class A {}\n")

	// when
	analyzed(t, New(time.Minute), dir)

	// then
	if actual, expected := gitStatus(t, dir), "?? core/src/main/java/A.java\n?? core/src/main/java/B.java"; actual != expected {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func TestAnalyzeRunsGradleWithoutADaemon(t *testing.T) {
	// given
	bin := stubPath(t)
	gradleStub(t, bin, ".", pmdReport(""), `{"runs":[]}`)
	dir := loneGradleRepo(t)

	// when
	analyzed(t, New(time.Minute), dir)

	// then
	actual := stubLog(t)
	expected := []string{"gradle|<root>|--no-daemon -q --init-script <out>/init.gradle :pmdMain :spotbugsMain"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func TestAnalyzeStopsADetachedGradleDaemonBeforeItWritesAfterTheTimeout(t *testing.T) {
	// given
	bin := stubPath(t)
	javaEnv(t, ".", pmdReport(""), `{"runs":[]}`)
	stub(t, bin, "gradle", `perl -e 'setpgrp(0, 0); exec @ARGV' sh -c 'while :; do mkdir -p .gradle/9.8.0 build/reports && : > .gradle/9.8.0/gc.properties && : > build/reports/late.html; sleep 0.05; done' < /dev/null > /dev/null 2>&1 &
sleep 30`)
	dir := stubbedRepo(t, map[string]string{"build.gradle": ""})
	writeText(t, dir, "src/main/java/A.java", "class A {}\n")
	expected := gitStatus(t, dir)

	// when
	_, err := New(500*time.Millisecond).Analyze(context.Background(), dir)
	time.Sleep(time.Second)

	// then
	if err == nil {
		t.Fatal("Analyze: expected a timeout")
	}
	if actual := gitStatus(t, dir); actual != expected {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func TestAnalyzeRunsSemgrepOverTheChangedFiles(t *testing.T) {
	cases := []struct {
		name      string
		committed map[string]string
		config    string
	}{
		{"default", map[string]string{"README.md": "a\n"}, "p/default"},
		{"semgrep.yml", map[string]string{"README.md": "a\n", ".semgrep.yml": "rules: []\n"}, "<root>/.semgrep.yml"},
		{"semgrep dir", map[string]string{"README.md": "a\n", ".semgrep/rules.yml": "rules: []\n"}, "<root>/.semgrep"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// given
			bin := stubPath(t)
			semgrepStub(t, bin, `{"runs":[]}`)
			dir := stubbedRepo(t, c.committed)
			writeText(t, dir, "README.md", "b\n")
			writeText(t, dir, "docs/new.md", "c\n")

			// when
			analyzed(t, New(time.Minute), dir)

			// then
			actual := stubLog(t)
			expected := []string{"semgrep|<root>|scan --metrics=off --sarif --output <out>/semgrep.sarif --config " + c.config + " -- README.md docs/new.md"}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("actual %q, expected %q", actual, expected)
			}
		})
	}
}

func TestAnalyzeReadsTheSemgrepReport(t *testing.T) {
	// given
	bin := stubPath(t)
	semgrepStub(t, bin, sarifHit("python.lang.security.audit.eval", "app.py", 1))
	dir := stubbedRepo(t, map[string]string{})
	writeText(t, dir, "app.py", "eval(x)\n")

	// when
	actual := analyzed(t, New(time.Minute), dir)

	// then
	expected := []string{"semgrep/python.lang.security.audit.eval: python.lang.security.audit.eval hit app.py"}
	if actualSummaries := summaries(actual.Findings); !reflect.DeepEqual(actualSummaries, expected) || actual.Command != "semgrep" {
		t.Fatalf("actual %q %q, expected %q %q", actualSummaries, actual.Command, expected, "semgrep")
	}
}

func TestAnalyzeSkipsSemgrepWhenItIsNotOnPath(t *testing.T) {
	// given
	bin := stubPath(t)
	goStub(t, bin, `{"runs":[]}`, `{"runs":[]}`)
	dir := goRepo(t)

	// when
	actual := analyzed(t, New(time.Minute), dir)

	// then
	actualLog := stubLog(t)
	expectedLog := []string{"go|<root>|" + golangciArgs + "<out>/golangci-1.sarif --enable=gosec"}
	if !reflect.DeepEqual(actualLog, expectedLog) || actual.Command != "golangci-lint" {
		t.Fatalf("actual %q %q, expected %q %q", actualLog, actual.Command, expectedLog, "golangci-lint")
	}
}

func TestAnalyzeRunsNothingWhenNothingChanged(t *testing.T) {
	// given
	bin := stubPath(t)
	goStub(t, bin, `{"runs":[]}`, `{"runs":[]}`)
	semgrepStub(t, bin, `{"runs":[]}`)
	dir := stubbedRepo(t, map[string]string{"go.mod": "module x\n", "main.go": "package main\n"})

	// when
	actual := analyzed(t, New(time.Minute), dir)

	// then
	expected := core.Analysis{Findings: []core.Finding{}, Command: ""}
	if actualLog := stubLog(t); len(actualLog) != 0 || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %q %+v, expected no log and %+v", actualLog, actual, expected)
	}
}

func TestAnalyzeRunsNoToolchainForAChangeWithoutSourceFiles(t *testing.T) {
	// given
	bin := stubPath(t)
	goStub(t, bin, `{"runs":[]}`, `{"runs":[]}`)
	dir := stubbedRepo(t, map[string]string{"go.mod": "module x\n", "README.md": "a\n"})
	writeText(t, dir, "README.md", "b\n")

	// when
	actual := analyzed(t, New(time.Minute), dir)

	// then
	if actualLog := stubLog(t); len(actualLog) != 0 || actual.Command != "" {
		t.Fatalf("actual %q %q, expected no log and no command", actualLog, actual.Command)
	}
}

func TestAnalyzeNamesEveryToolThatRanInCommand(t *testing.T) {
	// given
	bin := stubPath(t)
	goStub(t, bin, `{"runs":[]}`, `{"runs":[]}`)
	mvnStub(t, bin, "svc", pmdReport(""), `{"runs":[]}`)
	semgrepStub(t, bin, `{"runs":[]}`)
	dir := stubbedRepo(t, map[string]string{".gitignore": javaIgnore, "go.mod": "module x\n", "svc/pom.xml": ""})
	writeText(t, dir, "go.mod", "module x\n\ngo 1.25\n")
	writeText(t, dir, "main.go", "package main\n")
	writeText(t, dir, "svc/src/main/java/A.java", "class A {}\n")

	// when
	actual := analyzed(t, New(time.Minute), dir)

	// then
	if expected := "golangci-lint, govulncheck, pmd, spotbugs, semgrep"; actual.Command != expected {
		t.Fatalf("actual %q, expected %q", actual.Command, expected)
	}
}

func TestAnalyzeFailsOnAToolsNonZeroExit(t *testing.T) {
	cases := []struct {
		name     string
		repo     func(*testing.T) string
		stubName string
		body     string
		expected string
	}{
		{"golangci-lint", goRepo, "go", "echo build failed; exit 3", "golangci-lint: exit status 3\nbuild failed\n"},
		{"govulncheck stderr", goModChangedRepo, "go", goStubBody + `
case "$2" in *govulncheck*) echo 'loading packages' >&2; exit 1 ;; esac`, "govulncheck: exit status 1\nloading packages\n"},
		{"govulncheck both streams", goModChangedRepo, "go", goStubBody + `
case "$2" in *govulncheck*) echo partial; echo 'loading packages' >&2; exit 1 ;; esac`, "govulncheck: exit status 1\npartial\nloading packages\n"},
		{"find-sec-bugs", mavenCoreRepo, "mvn", mavenBody("echo '[ERROR] artifact not found'; exit 1", mavenBuild), "find-sec-bugs: exit status 1\n[ERROR] artifact not found\n"},
		{"maven", mavenCoreRepo, "mvn", mavenBody(fetchJar, "echo '[ERROR] COMPILATION ERROR'; exit 1"), "maven: exit status 1\n[ERROR] COMPILATION ERROR\n"},
		{"gradle", gradleAppRepo, "gradle", `echo "Task 'pmdMain' not found"; exit 1`, "gradle: exit status 1\nTask 'pmdMain' not found\n"},
		{"semgrep", semgrepRepo, "semgrep", "echo 'invalid config' >&2; exit 2", "semgrep: exit status 2\ninvalid config\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// given
			bin := stubPath(t)
			t.Setenv("GOLANGCI_SARIF", `{"runs":[]}`)
			javaEnv(t, "core", pmdReport(""), `{"runs":[]}`)
			stub(t, bin, c.stubName, c.body)
			dir := c.repo(t)

			// when
			actual := analyzeErr(t, New(time.Minute), dir)

			// then
			if actual != c.expected {
				t.Fatalf("actual %q, expected %q", actual, c.expected)
			}
		})
	}
}

func TestAnalyzeFailsWhenTheFetchWritesNoJar(t *testing.T) {
	// given
	bin := stubPath(t)
	javaEnv(t, "core", pmdReport(""), `{"runs":[]}`)
	stub(t, bin, "mvn", mavenBody("exit 0", mavenBuild))
	dir := mavenCoreRepo(t)

	// when
	actual := analyzeErr(t, New(time.Minute), dir)

	// then
	if expected := "find-sec-bugs: dependency:copy wrote no findsecbugs-plugin-1.14.0.jar"; actual != expected {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func TestAnalyzeFailsOnAMissingReport(t *testing.T) {
	cases := []struct {
		name      string
		repo      func(*testing.T) string
		module    string
		pmd, sarf string
		stubName  string
		body      string
		expected  string
	}{
		{"golangci-lint", goRepo, "", "", "", "go", "", "golangci-lint: no report at golangci-1.sarif"},
		{"maven pmd", mavenCoreRepo, "core", "", `{"runs":[]}`, "mvn", mavenBody(fetchJar, "echo '[INFO] skipped'\n"+mavenBuild), "pmd: no report at core/target/pmd.xml\n[INFO] skipped\n"},
		{"maven spotbugs", mavenCoreRepo, "core", pmdReport(""), "", "mvn", mavenBody(fetchJar, mavenBuild), "spotbugs: no report at core/target/spotbugsSarif.json"},
		{"gradle pmd", gradleAppRepo, "app", "", `{"runs":[]}`, "gradle", gradleBuild, "pmd: no report at app/build/reports/pmd/main.xml"},
		{"gradle spotbugs", gradleAppRepo, "app", pmdReport(""), "", "gradle", gradleBuild, "spotbugs: no report at app/build/reports/spotbugs/main.sarif"},
		{"semgrep", semgrepRepo, "", "", "", "semgrep", "", "semgrep: no report at semgrep.sarif"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// given
			bin := stubPath(t)
			javaEnv(t, c.module, c.pmd, c.sarf)
			stub(t, bin, c.stubName, c.body)
			dir := c.repo(t)

			// when
			actual := analyzeErr(t, New(time.Minute), dir)

			// then
			if actual != c.expected {
				t.Fatalf("actual %q, expected %q", actual, c.expected)
			}
		})
	}
}

func TestAnalyzeFailsOnAReportThatDoesNotParse(t *testing.T) {
	cases := []struct {
		name     string
		repo     func(*testing.T) string
		stubs    func(t *testing.T, bin string)
		expected string
	}{
		{"golangci-lint", goRepo, func(t *testing.T, bin string) { goStub(t, bin, "{", `{"runs":[]}`) },
			"golangci-lint: sarif: unexpected end of JSON input in golangci-1.sarif"},
		{"govulncheck", goModChangedRepo, func(t *testing.T, bin string) { goStub(t, bin, `{"runs":[]}`, "") },
			"govulncheck: sarif: unexpected end of JSON input in govulncheck-1.sarif"},
		{"maven pmd", mavenCoreRepo, func(t *testing.T, bin string) { mvnStub(t, bin, "core", "<pmd>", `{"runs":[]}`) },
			"pmd: xml: XML syntax error on line 1: unexpected EOF in core/target/pmd.xml"},
		{"gradle spotbugs", gradleAppRepo, func(t *testing.T, bin string) { gradleStub(t, bin, "app", pmdReport(""), "{") },
			"spotbugs: sarif: unexpected end of JSON input in app/build/reports/spotbugs/main.sarif"},
		{"semgrep", semgrepRepo, func(t *testing.T, bin string) { semgrepStub(t, bin, "{") },
			"semgrep: sarif: unexpected end of JSON input in semgrep.sarif"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// given
			bin := stubPath(t)
			c.stubs(t, bin)
			dir := c.repo(t)

			// when
			actual := analyzeErr(t, New(time.Minute), dir)

			// then
			if actual != c.expected {
				t.Fatalf("actual %q, expected %q", actual, c.expected)
			}
		})
	}
}

func TestAnalyzeKeepsTheLast4096BytesOfAFailingToolsOutput(t *testing.T) {
	// given
	bin := stubPath(t)
	stub(t, bin, "go", `printf HEAD; head -c 4096 < /dev/zero | tr '\0' b; exit 1`)
	dir := goRepo(t)

	// when
	actual := analyzeErr(t, New(time.Minute), dir)

	// then
	if expected := "golangci-lint: exit status 1\n" + strings.Repeat("b", 4096); actual != expected {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func TestAnalyzeKeepsTrailingWhitespaceInTheOutputTail(t *testing.T) {
	// given
	bin := stubPath(t)
	stub(t, bin, "go", `printf 'failed  \n\n'; exit 1`)
	dir := goRepo(t)

	// when
	actual := analyzeErr(t, New(time.Minute), dir)

	// then
	if expected := "golangci-lint: exit status 1\nfailed  \n\n"; actual != expected {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func TestAnalyzeFailsWhenTheAnalysisOutlivesTheTimeout(t *testing.T) {
	// given
	bin := stubPath(t)
	stub(t, bin, "go", "echo started; sleep 30")
	dir := goRepo(t)

	// when
	actual := analyzeErr(t, New(300*time.Millisecond), dir)

	// then
	if expected := "golangci-lint: timed out after 300ms: context deadline exceeded\nstarted\n"; actual != expected {
		t.Fatalf("actual %q, expected %q", actual, expected)
	}
}

func TestAnalyzeFailsAsInterruptedWhenTheCallerCancels(t *testing.T) {
	// given
	bin := stubPath(t)
	stub(t, bin, "go", `echo started; : > "$STUB_LOG.started"; sleep 30`)
	dir := goRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		waitForFile(t, os.Getenv("STUB_LOG")+".started")
		cancel()
	}()

	// when
	_, err := New(time.Minute).Analyze(ctx, dir)

	// then
	if expected := "golangci-lint: interrupted: context canceled\nstarted\n"; err == nil || err.Error() != expected {
		t.Fatalf("actual %v, expected %q", err, expected)
	}
}

func TestAnalyzeFailsWhenInterruptedAfterTheLastCommand(t *testing.T) {
	// given
	bin := stubPath(t)
	fifoSemgrepStub(t, bin)
	dir := semgrepRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	marker := os.Getenv("STUB_LOG") + ".fifo"
	go func() {
		waitForFile(t, marker)
		path, err := os.ReadFile(marker)
		if err != nil {
			t.Error(err)
			return
		}
		f, err := os.OpenFile(string(path), os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return
		}
		cancel()
		_, _ = f.WriteString(`{"runs":[]}`)
		_ = f.Close()
	}()

	// when
	_, err := New(time.Minute).Analyze(ctx, dir)

	// then
	if expected := "analyze: interrupted: context canceled"; err == nil || err.Error() != expected {
		t.Fatalf("actual %v, expected %q", err, expected)
	}
}

func TestAnalyzeFailsWhenAStaleReportCannotBeRemoved(t *testing.T) {
	// given
	bin := stubPath(t)
	mvnStub(t, bin, "core", pmdReport(""), `{"runs":[]}`)
	dir := mavenCoreRepo(t)
	writeText(t, dir, "core/target/pmd.xml/x", "")

	// when
	actual := analyzeErr(t, New(time.Minute), dir)

	// then
	expected := "maven: remove " + dir + "/core/target/pmd.xml: directory not empty"
	if actualLog := stubLog(t); actual != expected || !reflect.DeepEqual(actualLog, []string{fetchLine}) {
		t.Fatalf("actual %q %q, expected %q %q", actual, actualLog, expected, []string{fetchLine})
	}
}

func TestAnalyzeFailsWhenTheClassesDirectoryCannotBeRead(t *testing.T) {
	// given
	bin := stubPath(t)
	stub(t, bin, "mvn", mavenBody(fetchJar, "mkdir -p core/target/classes/sub && chmod 000 core/target/classes/sub"))
	dir := mavenCoreRepo(t)
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "core/target/classes/sub"), 0o755) })

	// when
	_, err := New(time.Minute).Analyze(context.Background(), dir)

	// then
	if err == nil || !strings.HasPrefix(err.Error(), "maven: open ") || !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("actual %v, expected a maven permission error", err)
	}
}

func TestAnalyzeFailsWithoutAUserCacheDirectory(t *testing.T) {
	// given
	bin := stubPath(t)
	mvnStub(t, bin, "core", pmdReport(""), `{"runs":[]}`)
	dir := mavenCoreRepo(t)
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	// when
	_, err := New(time.Minute).Analyze(context.Background(), dir)

	// then
	if actualLog := stubLog(t); err == nil || !strings.HasPrefix(err.Error(), "find-sec-bugs: ") || len(actualLog) != 0 {
		t.Fatalf("actual %v %q, expected a find-sec-bugs error and no log", err, actualLog)
	}
}

func TestAnalyzeFailsWithoutATempDirectory(t *testing.T) {
	// given
	bin := stubPath(t)
	goStub(t, bin, `{"runs":[]}`, `{"runs":[]}`)
	dir := goRepo(t)
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))

	// when
	_, err := New(time.Minute).Analyze(context.Background(), dir)

	// then
	if actualLog := stubLog(t); err == nil || !strings.HasPrefix(err.Error(), "analyze: ") || !errors.Is(err, fs.ErrNotExist) || len(actualLog) != 0 {
		t.Fatalf("actual %v %q, expected a missing temp dir error and no log", err, actualLog)
	}
}

func TestAnalyzeFailsForAMissingDirectory(t *testing.T) {
	// given
	dir := filepath.Join(t.TempDir(), "missing")

	// when
	_, err := New(time.Minute).Analyze(context.Background(), dir)

	// then
	if err == nil || !strings.HasPrefix(err.Error(), "analyze: ") || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("actual %v, expected a missing directory error", err)
	}
}
