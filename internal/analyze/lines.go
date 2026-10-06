package analyze

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Changes struct {
	Lines     map[string]map[int]bool
	Untracked map[string]bool
}

var hunkRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

func Changed(ctx context.Context, dir string) (Changes, error) {
	diff, err := gitOutput(ctx, dir, "diff", "-c", "core.quotePath=false", "diff", "-U0", "--inter-hunk-context=0", "--no-color", "--no-ext-diff", "--no-renames",
		"--src-prefix=a/", "--dst-prefix=b/", "--relative", "HEAD")
	if err != nil {
		return Changes{}, err
	}
	lines, err := parseDiff(diff)
	if err != nil {
		return Changes{}, err
	}
	others, err := gitOutput(ctx, dir, "ls-files", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return Changes{}, err
	}
	untracked := map[string]bool{}
	for _, f := range strings.Split(string(others), "\x00") {
		if f != "" {
			untracked[f] = true
		}
	}
	return Changes{Lines: lines, Untracked: untracked}, nil
}

func gitOutput(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func parseDiff(diff []byte) (map[string]map[int]bool, error) {
	lines := map[string]map[int]bool{}
	path, inHeader := "", false
	sc := bufio.NewScanner(bytes.NewReader(diff))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHeader, path = true, ""
		case inHeader && strings.HasPrefix(line, "+++ "):
			name := strings.TrimSuffix(strings.TrimPrefix(line, "+++ "), "\t")
			if strings.HasPrefix(name, `"`) {
				unquoted, err := strconv.Unquote(name)
				if err != nil {
					return nil, fmt.Errorf("git diff: cannot read header %q: %v", line, err)
				}
				name = unquoted
			}
			if name == "/dev/null" {
				path = ""
				continue
			}
			path = strings.TrimPrefix(name, "b/")
			lines[path] = map[int]bool{}
		case strings.HasPrefix(line, "@@"):
			inHeader = false
			m := hunkRe.FindStringSubmatch(line)
			if m == nil || path == "" {
				continue
			}
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			for i := start; i < start+count; i++ {
				lines[path][i] = true
			}
		}
	}
	return lines, sc.Err()
}

func (c Changes) Files() []string {
	files := make([]string, 0, len(c.Lines)+len(c.Untracked))
	for f := range c.Lines {
		files = append(files, f)
	}
	for f := range c.Untracked {
		if _, ok := c.Lines[f]; !ok {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files
}

func (c Changes) Has(path string, line int) bool {
	return c.Untracked[path] || c.Lines[path][line]
}
