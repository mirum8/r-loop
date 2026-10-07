package analyze

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"r-loop/internal/core"
)

//go:embed init.gradle
var initScript string

const tailBytes = 4096

type Analyzer struct{ timeout time.Duration }

func New(timeout time.Duration) *Analyzer {
	return &Analyzer{timeout: timeout}
}

func (a *Analyzer) Analyze(parent context.Context, dir string) (core.Analysis, error) {
	ctx, cancel := context.WithTimeout(parent, a.timeout)
	defer cancel()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return core.Analysis{}, fmt.Errorf("analyze: %w", err)
	}
	ch, err := Changed(ctx, dir)
	if err != nil {
		return core.Analysis{}, err
	}
	files := ch.Files()
	if len(files) == 0 {
		return core.Analysis{Findings: []core.Finding{}}, nil
	}
	mods, err := Detect(dir)
	if err != nil {
		return core.Analysis{}, fmt.Errorf("analyze: %w", err)
	}
	analysis, err := a.run(ctx, parent, dir, ch, Touched(mods, dir, files), files)
	if tidyErr := tidy(context.WithoutCancel(parent), dir, ch.Untracked); err == nil && tidyErr != nil {
		return core.Analysis{}, tidyErr
	}
	return analysis, err
}

func (a *Analyzer) run(ctx, parent context.Context, dir string, ch Changes, touches []Touch, files []string) (core.Analysis, error) {
	out, err := os.MkdirTemp("", "r-loop-analyze-")
	if err != nil {
		return core.Analysis{}, fmt.Errorf("analyze: %w", err)
	}
	defer os.RemoveAll(out)
	p := &pass{ctx: ctx, parent: parent, timeout: a.timeout, dir: dir, out: out}
	if err := p.all(touches, files); err != nil {
		return core.Analysis{}, err
	}
	analysis := core.Analysis{Findings: Findings(dir, ch, p.hits), Command: strings.Join(p.tools, ", ")}
	if ctx.Err() != nil {
		return core.Analysis{}, failure("analyze", nil, p.cause(ctx.Err()))
	}
	return analysis, nil
}

var buildDirs = map[string]bool{"target": true, "build": true, ".gradle": true, ".kotlin": true}

func tidy(ctx context.Context, dir string, before map[string]bool) error {
	others, err := gitOutput(ctx, dir, "ls-files", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, f := range strings.Split(string(others), "\x00") {
		if f == "" || before[f] || !buildOutput(f) {
			continue
		}
		path := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("analyze: %w", err)
		}
		for d := filepath.Dir(path); d != dir; d = filepath.Dir(d) {
			if os.Remove(d) != nil {
				break
			}
		}
	}
	return nil
}

func buildOutput(rel string) bool {
	parts := strings.Split(rel, "/")
	return slices.ContainsFunc(parts[:len(parts)-1], func(p string) bool { return buildDirs[p] })
}

type pass struct {
	ctx, parent context.Context
	timeout     time.Duration
	dir, out    string
	tools       []string
	hits        []Hit
}

func (p *pass) all(touches []Touch, files []string) error {
	n := 0
	var maven, gradle []Touch
	for _, t := range touches {
		switch t.Module.Kind {
		case Go:
			n++
			if err := p.golang(n, t); err != nil {
				return err
			}
		case Maven:
			maven = append(maven, t)
		case Gradle:
			gradle = append(gradle, t)
		}
	}
	for _, group := range byBuildRoot(maven) {
		if err := p.maven(group); err != nil {
			return err
		}
	}
	for _, group := range byBuildRoot(gradle) {
		if err := p.gradle(group); err != nil {
			return err
		}
	}
	if _, err := exec.LookPath("semgrep"); err == nil {
		return p.semgrep(files)
	}
	return nil
}

func (p *pass) ran(names ...string) {
	for _, name := range names {
		if !slices.Contains(p.tools, name) {
			p.tools = append(p.tools, name)
		}
	}
}

func (p *pass) command(tool, cwd string, env []string, stdout io.Writer, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(p.ctx, name, args...)
	cmd.Dir = cwd
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var detached []int
	cmd.Cancel = func() error {
		detached = descendants(cmd.Process.Pid)
		for _, pid := range detached {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	var out, errOut bytes.Buffer
	if stdout == nil {
		cmd.Stdout, cmd.Stderr = &out, &out
	} else {
		cmd.Stdout, cmd.Stderr = io.MultiWriter(stdout, &out), &errOut
	}
	err := cmd.Run()
	awaitGone(detached)
	output := append(out.Bytes(), errOut.Bytes()...)
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		err = nil
	}
	if err != nil {
		return output, failure(tool, output, p.cause(err))
	}
	return output, nil
}

func descendants(root int) []int {
	out, err := exec.Command("ps", "-A", "-o", "pid=", "-o", "ppid=").Output()
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 == nil && err2 == nil {
			children[ppid] = append(children[ppid], pid)
		}
	}
	found := slices.Clone(children[root])
	for i := 0; i < len(found); i++ {
		found = append(found, children[found[i]]...)
	}
	return found
}

func awaitGone(pids []int) {
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func (p *pass) cause(err error) error {
	if p.parent.Err() != nil {
		return fmt.Errorf("interrupted: %w", p.parent.Err())
	}
	if errors.Is(p.ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s: %w", p.timeout, context.DeadlineExceeded)
	}
	return err
}

func failure(tool string, output []byte, cause error) error {
	return fmt.Errorf("%s: %w%s", tool, cause, tail(output))
}

func tail(output []byte) string {
	if len(output) == 0 {
		return ""
	}
	return "\n" + string(output[max(0, len(output)-tailBytes):])
}

func (p *pass) show(path string) string {
	if within(p.dir, path) {
		if rel, err := filepath.Rel(p.dir, path); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(path)
}

func (p *pass) read(tool, base, path string, output []byte) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return failure(tool, output, fmt.Errorf("no report at %s", p.show(path)))
	}
	if err != nil {
		return failure(tool, output, err)
	}
	var hits []Hit
	if tool == "pmd" {
		hits, err = ParsePMD(base, data)
	} else {
		hits, err = ParseSARIF(tool, base, data)
	}
	if err != nil {
		return fmt.Errorf("%w in %s%s", err, p.show(path), tail(output))
	}
	p.hits = append(p.hits, hits...)
	return nil
}

func byBuildRoot(touches []Touch) [][]Touch {
	var groups [][]Touch
	index := map[string]int{}
	for _, t := range touches {
		i, ok := index[t.Module.BuildRoot]
		if !ok {
			i = len(groups)
			index[t.Module.BuildRoot] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], t)
	}
	return groups
}

var errFound = errors.New("found")

func hasClasses(dir string) (bool, error) {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && strings.HasSuffix(d.Name(), ".class") {
			return errFound
		}
		return nil
	})
	if errors.Is(err, errFound) {
		return true, nil
	}
	return false, err
}

func removeReports(paths ...string) error {
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func slashRel(root, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return dir
	}
	return filepath.ToSlash(rel)
}

func (p *pass) golang(n int, t Touch) error {
	m := t.Module
	report := filepath.Join(p.out, "golangci-"+strconv.Itoa(n)+".sarif")
	args := []string{"run", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@" + golangciLintVersion, "run",
		"--issues-exit-code=0", "--path-mode=abs", "--max-same-issues=0", "--max-issues-per-linter=0", "--output.sarif.path=" + report}
	if !golangciConfigured(p.dir, m.Dir) {
		args = append(args, "--enable=gosec")
	}
	cache, err := golangciCache(m.Dir)
	if err != nil {
		return failure("golangci-lint", nil, err)
	}
	output, err := p.command("golangci-lint", m.Dir, []string{"GOLANGCI_LINT_CACHE=" + cache}, nil, m.Runner, args...)
	if err != nil {
		return err
	}
	p.ran("golangci-lint")
	if err := p.read("golangci-lint", m.Dir, report, output); err != nil {
		return err
	}
	if !modChanged(p.dir, t) {
		return nil
	}
	vulnReport := filepath.Join(p.out, "govulncheck-"+strconv.Itoa(n)+".sarif")
	f, err := os.Create(vulnReport)
	if err != nil {
		return failure("govulncheck", nil, err)
	}
	output, err = p.command("govulncheck", m.Dir, nil, f, m.Runner, "run", "golang.org/x/vuln/cmd/govulncheck@"+govulncheckVersion, "-format", "sarif", "./...")
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = failure("govulncheck", output, closeErr)
	}
	if err != nil {
		return err
	}
	p.ran("govulncheck")
	return p.read("govulncheck", m.Dir, vulnReport, output)
}

func golangciCache(modDir string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(modDir))
	return filepath.Join(cache, "r-loop", "golangci", hex.EncodeToString(sum[:8])), nil
}

func golangciConfigured(root, modDir string) bool {
	for d := modDir; ; d = filepath.Dir(d) {
		for _, name := range []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json"} {
			if info, err := os.Stat(filepath.Join(d, name)); err == nil && info.Mode().IsRegular() {
				return true
			}
		}
		if d == root || d == filepath.Dir(d) {
			return false
		}
	}
}

func modChanged(root string, t Touch) bool {
	for _, f := range t.Files {
		base := filepath.Base(f)
		if (base == "go.mod" || base == "go.sum") && filepath.Join(root, filepath.Dir(filepath.FromSlash(f))) == t.Module.Dir {
			return true
		}
	}
	return false
}

func (p *pass) maven(group []Touch) error {
	root, runner := group[0].Module.BuildRoot, group[0].Module.Runner
	jar, err := p.findSecBugs(root, runner)
	if err != nil {
		return err
	}
	modules := make([]string, len(group))
	for i, t := range group {
		modules[i] = slashRel(root, t.Module.Dir)
		target := filepath.Join(t.Module.Dir, "target")
		if err := removeReports(filepath.Join(target, "pmd.xml"), filepath.Join(target, "spotbugsSarif.json")); err != nil {
			return failure("maven", nil, err)
		}
	}
	output, err := p.command("maven", root, nil, nil, runner, "-q", "-B", "-DskipTests", "-pl", strings.Join(modules, ","), "-am", "compile",
		"org.apache.maven.plugins:maven-pmd-plugin:"+mavenPMDPluginVersion+":pmd",
		"com.github.spotbugs:spotbugs-maven-plugin:"+spotbugsMavenPluginVersion+":spotbugs",
		"-Dspotbugs.sarifOutput=true", "-Dspotbugs.pluginList="+jar)
	if err != nil {
		return err
	}
	p.ran("pmd", "spotbugs")
	for _, t := range group {
		target := filepath.Join(t.Module.Dir, "target")
		if err := p.reports("maven", t.Module.Dir, filepath.Join(t.Module.Dir, "src", "main", "java"), filepath.Join(target, "classes"), filepath.Join(target, "pmd.xml"), filepath.Join(target, "spotbugsSarif.json"), output); err != nil {
			return err
		}
	}
	return nil
}

func (p *pass) reports(tool, dir, sources, classes, pmd, sarif string, output []byte) error {
	ok, err := hasClasses(classes)
	if err != nil {
		return failure(tool, output, err)
	}
	if !ok {
		return nil
	}
	if err := p.read("pmd", dir, pmd, output); err != nil {
		return err
	}
	return p.read("spotbugs", sources, sarif, output)
}

func (p *pass) findSecBugs(root, runner string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", failure("find-sec-bugs", nil, err)
	}
	dir := filepath.Join(cache, "r-loop", "analyze")
	name := "findsecbugs-plugin-" + findSecBugsVersion + ".jar"
	jar := filepath.Join(dir, name)
	if info, err := os.Stat(jar); err == nil && info.Mode().IsRegular() {
		return jar, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", failure("find-sec-bugs", nil, err)
	}
	stage, err := os.MkdirTemp(dir, "fetch-")
	if err != nil {
		return "", failure("find-sec-bugs", nil, err)
	}
	defer os.RemoveAll(stage)
	output, err := p.command("find-sec-bugs", root, nil, nil, runner, "-q", "-B",
		"org.apache.maven.plugins:maven-dependency-plugin:"+mavenDependencyPluginVersion+":copy",
		"-Dartifact=com.h3xstream.findsecbugs:findsecbugs-plugin:"+findSecBugsVersion,
		"-DoutputDirectory="+stage)
	if err != nil {
		return "", err
	}
	if err := os.Rename(filepath.Join(stage, name), jar); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", failure("find-sec-bugs", output, fmt.Errorf("dependency:copy wrote no %s", name))
		}
		return "", failure("find-sec-bugs", output, err)
	}
	return jar, nil
}

func (p *pass) gradle(group []Touch) error {
	root, runner := group[0].Module.BuildRoot, group[0].Module.Runner
	script := filepath.Join(p.out, "init.gradle")
	content := strings.NewReplacer("{{spotbugsGradlePlugin}}", spotbugsGradlePluginVersion, "{{findSecBugs}}", findSecBugsVersion).Replace(initScript)
	if err := os.WriteFile(script, []byte(content), 0o644); err != nil {
		return failure("gradle", nil, err)
	}
	args := []string{"--no-daemon", "-q", "--init-script", script}
	for _, t := range group {
		prefix := ":"
		if rel := slashRel(root, t.Module.Dir); rel != "." {
			prefix = ":" + strings.ReplaceAll(rel, "/", ":") + ":"
		}
		args = append(args, prefix+"pmdMain", prefix+"spotbugsMain")
		reports := filepath.Join(t.Module.Dir, "build", "reports")
		if err := removeReports(filepath.Join(reports, "pmd", "main.xml"), filepath.Join(reports, "spotbugs", "main.sarif")); err != nil {
			return failure("gradle", nil, err)
		}
	}
	output, err := p.command("gradle", root, nil, nil, runner, args...)
	if err != nil {
		return err
	}
	p.ran("pmd", "spotbugs")
	for _, t := range group {
		build := filepath.Join(t.Module.Dir, "build")
		reports := filepath.Join(build, "reports")
		if err := p.reports("gradle", t.Module.Dir, t.Module.Dir, filepath.Join(build, "classes", "java", "main"), filepath.Join(reports, "pmd", "main.xml"), filepath.Join(reports, "spotbugs", "main.sarif"), output); err != nil {
			return err
		}
	}
	return nil
}

func (p *pass) semgrep(files []string) error {
	report := filepath.Join(p.out, "semgrep.sarif")
	config := "p/default"
	if info, err := os.Stat(filepath.Join(p.dir, ".semgrep.yml")); err == nil && info.Mode().IsRegular() {
		config = filepath.Join(p.dir, ".semgrep.yml")
	} else if info, err := os.Stat(filepath.Join(p.dir, ".semgrep")); err == nil && info.IsDir() {
		config = filepath.Join(p.dir, ".semgrep")
	}
	args := append([]string{"scan", "--metrics=off", "--sarif", "--output", report, "--config", config, "--"}, files...)
	output, err := p.command("semgrep", p.dir, nil, nil, "semgrep", args...)
	if err != nil {
		return err
	}
	p.ran("semgrep")
	return p.read("semgrep", p.dir, report, output)
}
