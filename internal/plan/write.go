package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"r-loop/internal/core"
)

var ErrNothingToTick = errors.New("nothing to tick")

func (Reader) Tick(path string, ph core.Phase) error {
	lines, err := readLines(path)
	if err != nil {
		return err
	}
	if isBacklog(lines) {
		return tickBacklog(path, lines, ph)
	}
	phase := ph.ID
	start := -1
	for i, l := range lines {
		if m := phaseRe.FindStringSubmatch(strings.TrimRight(l, "\r\n")); m != nil && label(m[1]) == phase {
			start = i
			break
		}
	}
	if start < 0 {
		return fmt.Errorf("%s: no phase %s", path, phase)
	}
	ticked := 0
	for i := start + 1; i < len(lines) && !headingRe.MatchString(lines[i]); i++ {
		if strings.HasPrefix(lines[i], "- [ ]") {
			lines[i] = "- [x]" + strings.TrimPrefix(lines[i], "- [ ]")
			ticked++
		}
	}
	if ticked == 0 {
		return fmt.Errorf("%s phase %s: %w", path, phase, ErrNothingToTick)
	}
	return writeLines(path, lines)
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
