package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

type EvidenceContext struct {
	Repo                               Repo
	Worktree, StartSHA, StartTree      string
	PlanPath                           string
	FindingsFiles                      []string
	VerdictPath, RoundTree, ReportPath string
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
		"verdict":   func(EvidenceContext) (bool, string) { return false, "verdict check not built yet" },
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
	if !hasStatusPlanned(lines) {
		return false, "no status: planned header"
	}
	for _, h := range planHeadings {
		if _, ok := section(lines, h); !ok {
			return false, "missing " + h
		}
	}
	tests, _ := section(lines, "## Tests")
	if len(listItems(tests)) == 0 {
		return false, "## Tests is empty"
	}
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

func hasStatusPlanned(lines []string) bool {
	for i := 0; i < len(lines) && i < 5; i++ {
		if lines[i] == "status: planned" {
			return true
		}
	}
	return false
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
		if item, ok := strings.CutPrefix(l, "- "); ok {
			out = append(out, strings.TrimSpace(item))
		}
	}
	return out
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

var (
	ErrNoSentinel        = errors.New("no sentinel")
	ErrSentinelMalformed = errors.New("sentinel malformed")
)

type Sentinel struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
	At      string `json:"at"`
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
	if _, err := time.Parse(time.RFC3339, s.At); err != nil {
		return Sentinel{}, fmt.Errorf("%w: at %q", ErrSentinelMalformed, s.At)
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
