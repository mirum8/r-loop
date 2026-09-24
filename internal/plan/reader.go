package plan

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"r-loop/internal/core"
)

var (
	headingRe    = regexp.MustCompile(`^#{1,3}[ \t]`)
	anyHeadingRe = regexp.MustCompile(`^#{1,6}[ \t]`)
	phaseStartRe = regexp.MustCompile(`^###[ \t]+Phase\b`)
	phaseRe      = regexp.MustCompile(`^###[ \t]+Phase[ \t]+(\d+[a-zA-Z]?)[ \t]+(?:—|-)[ \t]+(.*)$`)
	milestoneRe  = regexp.MustCompile(`^##[ \t]+Milestone[ \t]+(\d+)[ \t]+(?:—|-)[ \t]+(.*)$`)
	builtRe      = regexp.MustCompile(`<!--\s*built:.*?-->`)
	itemRe       = regexp.MustCompile(`^- \[([ xX])\][ \t]?(.*)$`)
	backtickRe   = regexp.MustCompile("`([^`]+)`")
	phaseListRe  = regexp.MustCompile(`(?i)\bphases?[ \t]+\d+[a-z]?\b(?:[ \t]*(?:,(?:[ \t]*\band\b)?|&|·|/|\band\b)[ \t]*(?:phases?[ \t]+)?\d+[a-z]?\b)*`)
	phaseIDRe    = regexp.MustCompile(`\d+[a-zA-Z]?\b`)
	fenceRe      = regexp.MustCompile("^[ \\t]*(`{3,}|~{3,})(.*)$")
)

type Reader struct{}

type dependsRef struct {
	line  int
	phase string
	from  string
}

func (Reader) Read(path string) (core.Plan, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return core.Plan{}, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return core.Plan{}, err
	}
	lines := strings.SplitAfter(string(raw), "\n")
	mask, open := fenced(lines)
	if open >= 0 {
		return core.Plan{}, fmt.Errorf("%s line %d: code fence is never closed", path, open+1)
	}
	if isBacklog(lines, mask) {
		return readBacklog(path, lines)
	}
	p := core.Plan{Path: path, Topic: filepath.Base(filepath.Dir(abs))}
	if found, spans := resolveFirst(lines, mask); found {
		p.ResolveFirst = []core.Entry{}
		for _, s := range spans {
			p.ResolveFirst = append(p.ResolveFirst, s.entry)
		}
	}
	milestone := 0
	var deps []dependsRef
	seen := map[string]bool{}
	for i := 0; i < len(lines); i++ {
		if mask[i] {
			continue
		}
		line := strings.TrimRight(lines[i], "\r\n")
		if m := milestoneRe.FindStringSubmatch(line); m != nil {
			milestone, _ = strconv.Atoi(m[1])
			p.Milestones = append(p.Milestones, core.Milestone{Number: milestone, Name: strings.TrimSpace(m[2])})
			continue
		}
		if !phaseStartRe.MatchString(line) {
			continue
		}
		m := phaseRe.FindStringSubmatch(line)
		if m == nil {
			return core.Plan{}, fmt.Errorf("%s line %d: phase heading without its dash: %q", path, i+1, line)
		}
		n := label(m[1])
		if seen[n] {
			return core.Plan{}, fmt.Errorf("%s line %d: duplicate phase %s", path, i+1, n)
		}
		if err := followsPrevious(p.Phases, n); err != nil {
			return core.Plan{}, fmt.Errorf("%s line %d: %v", path, i+1, err)
		}
		seen[n] = true

		end := i + 1
		for end < len(lines) && (mask[end] || !headingRe.MatchString(lines[end])) {
			end++
		}
		ph, refs, err := parsePhase(path, n, m[2], lines[i:end], mask[i:end], i+1)
		if err != nil {
			return core.Plan{}, err
		}
		ph.Milestone = milestone
		p.Phases = append(p.Phases, ph)
		deps = append(deps, refs...)
		if milestone != 0 {
			ms := &p.Milestones[len(p.Milestones)-1]
			ms.Phases = append(ms.Phases, n)
		}
		i = end - 1
	}

	for _, d := range deps {
		switch {
		case !seen[d.phase]:
			return core.Plan{}, fmt.Errorf("%s line %d: depends on phase %s, which does not exist", path, d.line, d.phase)
		case d.phase == d.from:
			return core.Plan{}, fmt.Errorf("%s line %d: phase %s depends on itself", path, d.line, d.from)
		case core.ComparePhaseIDs(d.phase, d.from) > 0:
			return core.Plan{}, fmt.Errorf("%s line %d: phase %s depends on phase %s, which comes after it; a phase may depend only on earlier phases", path, d.line, d.from, d.phase)
		}
	}
	for i, ph := range p.Phases {
		if resolved := resolvedFor(p.ResolveFirst, ph.ID); resolved != "" {
			p.Phases[i].Block = strings.TrimRight(ph.Block, "\n") + "\n\nResolved first:\n\n" + resolved
		}
	}
	return p, nil
}

func followsPrevious(phases []core.Phase, n string) error {
	num, suffix := core.SplitPhaseID(n)
	if len(phases) == 0 {
		if n != "1" {
			return fmt.Errorf("phase %s skips phase 1", n)
		}
		return nil
	}
	prev := phases[len(phases)-1].ID
	prevNum, _ := core.SplitPhaseID(prev)
	switch {
	case suffix == "" && num != prevNum+1:
		return fmt.Errorf("phase %s skips phase %d", n, prevNum+1)
	case suffix != "" && num != prevNum:
		return fmt.Errorf("phase %s skips phase %d", n, num)
	case suffix != "" && core.ComparePhaseIDs(n, prev) <= 0:
		return fmt.Errorf("phase %s is out of order after phase %s", n, prev)
	}
	return nil
}

func resolvedFor(entries []core.Entry, phase string) string {
	var b strings.Builder
	for _, e := range entries {
		if e.Ticked && (e.BlocksAll || slices.Contains(e.BlocksPhases, phase)) {
			b.WriteString(e.Body + "\n")
		}
	}
	return b.String()
}

func parsePhase(path, id, title string, block []string, fence []bool, headingLine int) (core.Phase, []dependsRef, error) {
	title = builtRe.ReplaceAllString(title, "")
	title = strings.ReplaceAll(title, "✅", "")
	ph := core.Phase{ID: id, Title: strings.TrimSpace(title), Block: strings.Join(block, "")}

	var refs []dependsRef
	for j := 1; j < len(block); j++ {
		if fence[j] {
			continue
		}
		line := strings.TrimRight(block[j], "\r\n")
		lineNo := headingLine + j
		switch {
		case strings.HasPrefix(line, "**Implements:**"):
			for _, s := range strings.Split(field(line, "**Implements:**"), " · ") {
				if s = strings.TrimSpace(s); s != "" {
					ph.Implements = append(ph.Implements, s)
				}
			}
		case strings.HasPrefix(line, "**Depends on:**"):
			rest := field(line, "**Depends on:**")
			deps, err := parseDepends(rest)
			if err != nil {
				return core.Phase{}, nil, fmt.Errorf("%s line %d: %v", path, lineNo, err)
			}
			ph.DependsOn = deps
			for _, d := range deps {
				refs = append(refs, dependsRef{line: lineNo, phase: d, from: id})
			}
		case strings.HasPrefix(line, "**Files:**"):
			for _, m := range backtickRe.FindAllStringSubmatch(line, -1) {
				ph.Files = append(ph.Files, m[1])
			}
		case strings.HasPrefix(line, "**Risk:**"):
			ph.Risk = field(line, "**Risk:**")
		case strings.HasPrefix(line, "**Done when:**"):
			parts := []string{field(line, "**Done when:**")}
			for j+1 < len(block) && (fence[j+1] || (!strings.HasPrefix(block[j+1], "**") && !anyHeadingRe.MatchString(block[j+1]))) {
				j++
				parts = append(parts, strings.TrimRight(block[j], "\r\n"))
			}
			ph.DoneWhen = strings.TrimSpace(strings.Join(parts, "\n"))
		default:
			if m := itemRe.FindStringSubmatch(line); m != nil {
				ph.Items = append(ph.Items, core.Item{Text: m[2], Done: m[1] != " "})
			}
		}
	}
	return ph, refs, nil
}

func field(line, label string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, label))
}

func parseDepends(rest string) ([]string, error) {
	if rest == "" || rest == "—" || rest == "-" || strings.EqualFold(rest, "none") {
		return nil, nil
	}
	out := phaseRefs(rest)
	if len(out) == 0 {
		return nil, fmt.Errorf("depends on names no phase: %q", rest)
	}
	return out, nil
}

func label(s string) string {
	s = strings.ToLower(s)
	if trimmed := strings.TrimLeft(s, "0"); trimmed != "" && trimmed[0] >= '0' && trimmed[0] <= '9' {
		return trimmed
	}
	return s
}

func phaseRefs(s string) []string {
	var out []string
	for _, list := range phaseListRe.FindAllString(s, -1) {
		for _, id := range phaseIDRe.FindAllString(list, -1) {
			out = append(out, label(id))
		}
	}
	return out
}

func fenced(lines []string) ([]bool, int) {
	mask := make([]bool, len(lines))
	open := -1
	marker := ""
	for i, raw := range lines {
		line := strings.TrimRight(raw, "\r\n")
		m := fenceRe.FindStringSubmatch(line)
		if open < 0 {
			if m != nil {
				open, marker = i, m[1]
				mask[i] = true
			}
			continue
		}
		mask[i] = true
		if m != nil && m[1][0] == marker[0] && len(m[1]) >= len(marker) && strings.TrimSpace(m[2]) == "" {
			open = -1
			marker = ""
		}
	}
	return mask, open
}
