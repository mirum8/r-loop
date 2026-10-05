package core

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxSubject = 100

var (
	subjectShape = regexp.MustCompile(`^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([a-z0-9][a-z0-9._/-]*\))?!?: (.+)$`)
	labelOnly    = regexp.MustCompile(`(?i)^(phase\s*\d+\b.*|plan|implement|gatefix|fix|review|wip|changes|update)$`)
)

func ValidSubject(s string) error {
	switch {
	case s == "":
		return errors.New("no commit subject")
	case strings.ContainsFunc(s, unicode.IsControl):
		return errors.New("commit subject must be one line")
	case utf8.RuneCountInString(s) > maxSubject:
		return fmt.Errorf("commit subject is longer than %d characters", maxSubject)
	case s != strings.TrimSpace(s):
		return errors.New("commit subject has surrounding spaces")
	}
	m := subjectShape.FindStringSubmatch(s)
	if m == nil {
		return fmt.Errorf("commit subject %q is not <type>(<scope>): <description> with type feat, fix, docs, style, refactor, perf, test, build, ci, chore or revert", s)
	}
	if desc := m[3]; desc != strings.TrimSpace(desc) || labelOnly.MatchString(desc) {
		return fmt.Errorf("commit subject %q must describe the change, not label the step", s)
	}
	return nil
}

func Subject(prefix, text string) string {
	s := prefix + text
	if utf8.RuneCountInString(s) <= maxSubject {
		return s
	}
	cut := string([]rune(s)[:maxSubject])
	if i := strings.LastIndexByte(cut, ' '); i > len(prefix) {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut)
}

const lastResortSubject = "chore: apply the phase's changes"

func RepairSubject(raw, fallback string) (subject, repair string) {
	verr := ValidSubject(raw)
	if verr == nil {
		return raw, ""
	}
	subject = repairLine(subjectLine(raw), fallback)
	if ValidSubject(subject) != nil {
		subject = fallback
	}
	if ValidSubject(subject) != nil {
		subject = lastResortSubject
	}
	return subject, fmt.Sprintf("commit subject repaired (%v): committed %q", verr, subject)
}

func subjectLine(raw string) string {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, line))
		if line != "" {
			return line
		}
	}
	return ""
}

func repairLine(line, fallback string) string {
	if line == "" || labelOnly.MatchString(line) {
		return fallback
	}
	m := subjectShape.FindStringSubmatch(line)
	if m == nil {
		return Subject("chore: ", line)
	}
	desc := strings.TrimSpace(m[3])
	if desc == "" || labelOnly.MatchString(desc) {
		return fallback
	}
	return Subject(line[:len(line)-len(m[3])], desc)
}
