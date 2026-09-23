package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

type EvidenceContext struct {
	Repo                               Repo
	Worktree, StartSHA, StartTree      string
	PlanPath                           string
	FindingsFiles                      []string
	VerdictPath, RoundTree, ReportPath string
	NeedGate                           bool
	FS                                 fs.FS
}

type EvidenceCheck func(ctx EvidenceContext) (ok bool, missing string)

var (
	checksMu sync.RWMutex
	checks   = map[string]EvidenceCheck{
		"plan-file": planFileCheck,
		"diff":      diffCheck,
		"report":    reportCheck,
		"findings":  findingsCheck,
		"verdict":   verdictCheck,
	}
)

func RegisterCheck(name string, fn EvidenceCheck) {
	checksMu.Lock()
	defer checksMu.Unlock()
	checks[name] = fn
}

func LookupCheck(name string) (EvidenceCheck, bool) {
	checksMu.RLock()
	defer checksMu.RUnlock()
	fn, ok := checks[name]
	return fn, ok
}

var planHeadings = []string{"## Summary", "## Changes", "## Tests", "## Assumptions"}

func planFileCheck(ctx EvidenceContext) (bool, string) {
	data, err := fs.ReadFile(ctx.FS, ctx.PlanPath)
	if err != nil {
		return false, "no plan at " + ctx.PlanPath
	}
	lines := splitLines(string(data))
	if ctx.NeedGate && itemSkipStatus[planStatus(lines)] {
		evidence, _ := section(lines, "## Evidence")
		if !citationRe.MatchString(strings.Join(evidence, "\n")) {
			return false, "## Evidence cites no path:line"
		}
		return planChangedOnlyItself(ctx)
	}
	if planStatus(lines) != "planned" {
		return false, "no status: planned header"
	}
	for _, h := range planHeadings {
		if _, ok := section(lines, h); !ok {
			return false, "missing " + h
		}
	}
	tests, _ := section(lines, "## Tests")
	if len(listItems(tests))+len(tableRows(tests)) == 0 {
		return false, "## Tests is empty"
	}
	if ctx.NeedGate {
		if _, reason := PlanGate(lines); reason != "" {
			return false, reason
		}
	}
	return planChangedOnlyItself(ctx)
}

func planChangedOnlyItself(ctx EvidenceContext) (bool, string) {
	changed, missing := stepChanges(ctx)
	if missing != "" {
		return false, missing
	}
	for _, p := range changed {
		if p != ctx.PlanPath {
			return false, "plan step changed " + p
		}
	}
	if len(changed) == 0 {
		return false, "plan step did not change " + ctx.PlanPath
	}
	return true, ""
}

var (
	itemSkipStatus = map[string]bool{"already-done": true, "not-work": true}
	citationRe     = regexp.MustCompile(`[\w./-]+\.\w+:\d+`)
)

func PlanSkip(path string, fsys fs.FS) (string, string) {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return "", ""
	}
	lines := splitLines(string(data))
	status := planStatus(lines)
	if !itemSkipStatus[status] {
		return "", ""
	}
	evidence, _ := section(lines, "## Evidence")
	var parts []string
	for _, l := range evidence {
		if l != "" {
			parts = append(parts, strings.TrimPrefix(l, "- "))
		}
	}
	return status, strings.Join(parts, "; ")
}

func PlanGate(lines []string) (string, string) {
	body, ok := section(lines, "## Gate")
	if !ok {
		return "", "missing ## Gate"
	}
	spans := codeSpanRe.FindAllStringSubmatch(strings.Join(body, "\n"), -1)
	if len(spans) != 1 || strings.TrimSpace(spans[0][1]) == "" {
		return "", "## Gate must hold exactly one command in backticks"
	}
	return strings.TrimSpace(spans[0][1]), ""
}

func diffCheck(ctx EvidenceContext) (bool, string) {
	changed, missing := stepChanges(ctx)
	if missing != "" {
		return false, missing
	}
	if len(changed) == 0 {
		return false, "no change since the step started"
	}
	return true, ""
}

func stepChanges(ctx EvidenceContext) ([]string, string) {
	now, err := ctx.Repo.Snapshot(ctx.Worktree)
	if err != nil {
		return nil, "snapshot failed: " + err.Error()
	}
	changed, err := ctx.Repo.TreeDiff(ctx.StartTree, now)
	if err != nil {
		return nil, "tree diff failed: " + err.Error()
	}
	return changed, ""
}

func reportCheck(ctx EvidenceContext) (bool, string) {
	data, err := fs.ReadFile(ctx.FS, ctx.ReportPath)
	if err != nil {
		return false, "no report at " + ctx.ReportPath
	}
	if strings.TrimSpace(string(data)) == "" {
		return false, "report " + ctx.ReportPath + " is empty"
	}
	return true, ""
}

func PlanAssumptions(path string, fsys fs.FS) []string {
	data, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil
	}
	body, _ := section(splitLines(string(data)), "## Assumptions")
	var out []string
	for _, item := range listItems(body) {
		if !strings.EqualFold(item, "none") {
			out = append(out, item)
		}
	}
	return out
}

func splitLines(s string) []string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	return lines
}

func planStatus(lines []string) string {
	for i := 0; i < len(lines) && i < 5; i++ {
		if status, ok := strings.CutPrefix(lines[i], "status: "); ok {
			return strings.TrimSpace(status)
		}
	}
	return ""
}

func section(lines []string, heading string) ([]string, bool) {
	for i, l := range lines {
		if l != heading {
			continue
		}
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(lines[end], "## ") {
			end++
		}
		return lines[i+1 : end], true
	}
	return nil, false
}

func listItems(lines []string) []string {
	var out []string
	for _, l := range lines {
		if item, ok := listItem(l); ok {
			out = append(out, strings.TrimSpace(item))
		}
	}
	return out
}

func tableRows(lines []string) []string {
	var out []string
	inTable := false
	for i, l := range lines {
		switch {
		case !strings.HasPrefix(l, "|"):
			inTable = false
		case !inTable:
			inTable = i+1 < len(lines) && tableSeparator(lines[i+1])
		case !tableSeparator(l):
			out = append(out, l)
		}
	}
	return out
}

func tableSeparator(l string) bool {
	return strings.HasPrefix(l, "|") && strings.Contains(l, "-") && strings.Trim(l, "|-: ") == ""
}

func listItem(l string) (string, bool) {
	for _, bullet := range []string{"- ", "* ", "+ "} {
		if item, ok := strings.CutPrefix(l, bullet); ok {
			return item, true
		}
	}
	digits := len(l) - len(strings.TrimLeft(l, "0123456789"))
	if digits == 0 || digits+1 >= len(l) {
		return "", false
	}
	if (l[digits] == '.' || l[digits] == ')') && l[digits+1] == ' ' {
		return l[digits+2:], true
	}
	return "", false
}

type Findings struct {
	Reviewer string    `json:"reviewer"`
	Findings []Finding `json:"findings"`
}

type Finding struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Detail string   `json:"detail"`
	Files  []string `json:"files"`
}

func ReadFindings(path string) (Findings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Findings{}, err
	}
	return parseFindings(data)
}

func parseFindings(data []byte) (Findings, error) {
	var raw struct {
		Reviewer string `json:"reviewer"`
		Findings []struct {
			ID     string   `json:"id"`
			Title  string   `json:"title"`
			Detail *string  `json:"detail"`
			Files  []string `json:"files"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Findings{}, errors.New("unreadable")
	}
	if raw.Findings == nil {
		return Findings{}, errors.New("no findings list")
	}
	f := Findings{Reviewer: raw.Reviewer, Findings: []Finding{}}
	for i, r := range raw.Findings {
		switch {
		case r.ID == "":
			return Findings{}, fmt.Errorf("finding %d has no id", i+1)
		case r.Title == "":
			return Findings{}, fmt.Errorf("finding %s has no title", r.ID)
		case r.Detail == nil:
			return Findings{}, fmt.Errorf("finding %s has no detail", r.ID)
		case r.Files == nil:
			return Findings{}, fmt.Errorf("finding %s has no files list", r.ID)
		}
		f.Findings = append(f.Findings, Finding{ID: r.ID, Title: r.Title, Detail: *r.Detail, Files: r.Files})
	}
	return f, nil
}

var findingsName = regexp.MustCompile(`^.+?-findings-(.+)-r([0-9]+)\.json$`)

func findingsCheck(ctx EvidenceContext) (bool, string) {
	for _, p := range ctx.FindingsFiles {
		if missing := checkFindingsFile(ctx.FS, p); missing != "" {
			return false, missing
		}
	}
	return true, ""
}

func checkFindingsFile(fsys fs.FS, p string) string {
	m := findingsName.FindStringSubmatch(path.Base(p))
	if m == nil {
		return p + ": name is not <kind>-findings-<reviewer>-r<round>.json"
	}
	reviewer, round := m[1], m[2]
	data, err := fs.ReadFile(fsys, p)
	if err != nil {
		return "no findings at " + p
	}
	f, err := parseFindings(data)
	if err != nil {
		return p + ": " + err.Error()
	}
	if f.Reviewer != reviewer {
		return fmt.Sprintf("%s: reviewer %s does not match %s", p, f.Reviewer, reviewer)
	}
	prefix := reviewer + "-r" + round + "-"
	seen := map[string]bool{}
	for _, fd := range f.Findings {
		switch {
		case !strings.HasPrefix(fd.ID, prefix):
			return fmt.Sprintf("%s: id %s does not start with %s", p, fd.ID, prefix)
		case seen[fd.ID]:
			return fmt.Sprintf("%s: duplicate id %s", p, fd.ID)
		}
		seen[fd.ID] = true
	}
	return ""
}

type Verdict struct {
	Findings []VerdictEntry `json:"findings"`
}

type VerdictEntry struct {
	ID       string   `json:"id"`
	Reviewer string   `json:"reviewer"`
	Title    string   `json:"title"`
	Verdict  string   `json:"verdict"`
	Severity string   `json:"severity"`
	Fixed    bool     `json:"fixed"`
	Files    []string `json:"files"`
	Evidence string   `json:"evidence"`
}

func (e VerdictEntry) blocking() bool {
	return e.Verdict == "real" && (e.Severity == "P1" || e.Severity == "P2")
}

var (
	verdicts   = []string{"real", "not-real", "out-of-scope"}
	severities = []string{"P1", "P2", "P3", "P4"}
	pathLine   = regexp.MustCompile(`^[^\s:]+:\d+$`)
)

func ReadVerdict(path string) (Verdict, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Verdict{}, err
	}
	var v Verdict
	if err := json.Unmarshal(data, &v); err != nil {
		return Verdict{}, errors.New("unreadable")
	}
	for i, e := range v.Findings {
		switch {
		case e.ID == "":
			return Verdict{}, fmt.Errorf("entry %d has no id", i+1)
		case e.Verdict == "":
			return Verdict{}, fmt.Errorf("entry %s has no verdict", e.ID)
		case !slices.Contains(verdicts, e.Verdict):
			return Verdict{}, fmt.Errorf("entry %s has verdict %q", e.ID, e.Verdict)
		case e.Severity == "":
			return Verdict{}, fmt.Errorf("entry %s has no severity", e.ID)
		case !slices.Contains(severities, e.Severity):
			return Verdict{}, fmt.Errorf("entry %s has severity %q", e.ID, e.Severity)
		}
	}
	return v, nil
}

func verdictCheck(ctx EvidenceContext) (bool, string) {
	var ids []string
	for _, p := range ctx.FindingsFiles {
		f, err := ReadFindings(p)
		if err != nil {
			return false, filepath.Base(p) + ": " + err.Error()
		}
		for _, fd := range f.Findings {
			ids = append(ids, fd.ID)
		}
	}
	v, err := ReadVerdict(ctx.VerdictPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, "no verdict at " + filepath.Base(ctx.VerdictPath)
	}
	if err != nil {
		return false, filepath.Base(ctx.VerdictPath) + ": " + err.Error()
	}
	seen := map[string]bool{}
	for _, e := range v.Findings {
		switch {
		case !slices.Contains(ids, e.ID):
			return false, "verdict for unknown finding " + e.ID
		case seen[e.ID]:
			return false, "finding " + e.ID + " has two verdicts"
		}
		seen[e.ID] = true
	}
	for _, id := range ids {
		if !seen[id] {
			return false, "no verdict for finding " + id
		}
	}
	var changed []string
	for _, e := range v.Findings {
		if missing := checkEntry(ctx, e, &changed); missing != "" {
			return false, missing
		}
	}
	return true, ""
}

func checkEntry(ctx EvidenceContext, e VerdictEntry, changed *[]string) string {
	if e.Verdict == "not-real" {
		if !pathLine.MatchString(e.Evidence) {
			return "not-real finding " + e.ID + " has no path:line evidence"
		}
		p := e.Evidence[:strings.LastIndex(e.Evidence, ":")]
		if _, err := fs.Stat(ctx.FS, p); err != nil {
			return "evidence " + e.Evidence + " of " + e.ID + " is not in the worktree"
		}
	}
	if !e.Fixed {
		if e.blocking() {
			return fmt.Sprintf("real %s finding %s is not fixed", e.Severity, e.ID)
		}
		return ""
	}
	if !e.blocking() {
		return "fixed finding " + e.ID + " is not real at P1 or P2"
	}
	if len(e.Files) == 0 {
		return "fixed finding " + e.ID + " names no files"
	}
	if *changed == nil {
		now, err := ctx.Repo.Snapshot(ctx.Worktree)
		if err != nil {
			return "snapshot failed: " + err.Error()
		}
		diff, err := ctx.Repo.TreeDiff(ctx.RoundTree, now)
		if err != nil {
			return "tree diff failed: " + err.Error()
		}
		*changed = append([]string{}, diff...)
	}
	for _, f := range e.Files {
		if !slices.Contains(*changed, f) {
			return "fixed finding " + e.ID + ": " + f + " did not change in this round"
		}
	}
	return ""
}

var (
	ErrNoSentinel        = errors.New("no sentinel")
	ErrSentinelMalformed = errors.New("sentinel malformed")
)

type Sentinel struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

func ReadSentinel(path string) (Sentinel, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Sentinel{}, ErrNoSentinel
	}
	if err != nil {
		return Sentinel{}, fmt.Errorf("%w: %v", ErrSentinelMalformed, err)
	}
	var s Sentinel
	if err := json.Unmarshal(data, &s); err != nil {
		return Sentinel{}, fmt.Errorf("%w: %v", ErrSentinelMalformed, err)
	}
	if s.Outcome != "ok" && s.Outcome != "failed" {
		return Sentinel{}, fmt.Errorf("%w: outcome %q", ErrSentinelMalformed, s.Outcome)
	}
	return s, nil
}

func Judge(s Sentinel, sErr error, evidenceOK bool, missing string) (StepState, string) {
	switch {
	case sErr != nil || (s.Outcome != "ok" && s.Outcome != "failed"):
		return StepFailed, "sentinel unreadable"
	case s.Outcome == "failed":
		return StepFailed, s.Reason
	case !evidenceOK:
		return StepFailed, "evidence missing: " + missing
	}
	return StepOK, ""
}
