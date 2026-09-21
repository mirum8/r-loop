package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var ErrNothingToTick = errors.New("nothing to tick")

func (Reader) Tick(path string, phase int) error {
	lines, err := readLines(path)
	if err != nil {
		return err
	}
	if isBacklog(lines) {
		return tickBacklog(path, lines, phase)
	}
	start := -1
	for i, l := range lines {
		if m := phaseRe.FindStringSubmatch(strings.TrimRight(l, "\r\n")); m != nil && atoi(m[1]) == phase {
			start = i
			break
		}
	}
	if start < 0 {
		return fmt.Errorf("%s: no phase %d", path, phase)
	}
	ticked := 0
	for i := start + 1; i < len(lines) && !headingRe.MatchString(lines[i]); i++ {
		if strings.HasPrefix(lines[i], "- [ ]") {
			lines[i] = "- [x]" + strings.TrimPrefix(lines[i], "- [ ]")
			ticked++
		}
	}
	if ticked == 0 {
		return fmt.Errorf("%s phase %d: %w", path, phase, ErrNothingToTick)
	}
	return writeLines(path, lines)
}

func (Reader) Stamp(path, entryName, resolvedLine string) error {
	lines, err := readLines(path)
	if err != nil {
		return err
	}
	_, spans := resolveFirst(lines)
	for _, s := range spans {
		if s.entry.Name != entryName {
			continue
		}
		if s.entry.Ticked {
			return fmt.Errorf("%s: entry %q is already ticked", path, entryName)
		}
		if s.entry.HasBox {
			lines[s.start] = entryBoxRe.ReplaceAllString(lines[s.start], "${1}[x]")
		} else {
			marker := entryMarkerRe.FindString(lines[s.start])
			lines[s.start] = marker + "[x] " + strings.TrimPrefix(lines[s.start], marker)
		}
		last := s.end - 1
		stamp := "      Resolved: " + resolvedLine
		if strings.HasSuffix(lines[last], "\n") {
			stamp += "\n"
		} else {
			lines[last] += "\n"
		}
		lines = append(lines[:s.end], append([]string{stamp}, lines[s.end:]...)...)
		return writeLines(path, lines)
	}
	return fmt.Errorf("%s: no Resolve first entry %q", path, entryName)
}

func readLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.SplitAfter(string(raw), "\n"), nil
}

func writeLines(path string, lines []string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".todo-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(strings.Join(lines, "")); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
