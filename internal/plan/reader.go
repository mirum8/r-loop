package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"r-loop/internal/core"
)

var (
	headingRe    = regexp.MustCompile(`^#{1,3}[ \t]`)
	anyHeadingRe = regexp.MustCompile(`^#{1,6}[ \t]`)
	phaseStartRe = regexp.MustCompile(`^###[ \t]+Phase\b`)
	phaseRe      = regexp.MustCompile(`^###[ \t]+Phase[ \t]+(\d+)[ \t]+(?:—|-)[ \t]+(.*)$`)
	milestoneRe  = regexp.MustCompile(`^##[ \t]+Milestone[ \t]+(\d+)[ \t]+(?:—|-)[ \t]+(.*)$`)
	builtRe      = regexp.MustCompile(`<!--\s*built:.*?-->`)
	itemRe       = regexp.MustCompile(`^- \[([ xX])\][ \t]?(.*)$`)
	backtickRe   = regexp.MustCompile("`([^`]+)`")
	numberRe     = regexp.MustCompile(`\d+`)
)

type Reader struct{}

type dependsRef struct {
	line  int
	phase int
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
	p := core.Plan{Path: path, Topic: filepath.Base(filepath.Dir(abs))}

	lines := strings.SplitAfter(string(raw), "\n")
	milestone := 0
	var deps []dependsRef
	seen := map[int]bool{}
	for i := 0; i < len(lines); i++ {
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
		n, _ := strconv.Atoi(m[1])
		if seen[n] {
			return core.Plan{}, fmt.Errorf("%s line %d: duplicate phase %d", path, i+1, n)
		}
		if n != len(p.Phases)+1 {
			return core.Plan{}, fmt.Errorf("%s line %d: phase %d skips phase %d", path, i+1, n, len(p.Phases)+1)
		}
		seen[n] = true

		end := i + 1
		for end < len(lines) && !headingRe.MatchString(lines[end]) {
			end++
		}
		ph, refs, err := parsePhase(path, n, m[2], lines[i:end], i+1)
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
		if !seen[d.phase] {
			return core.Plan{}, fmt.Errorf("%s line %d: depends on phase %d, which does not exist", path, d.line, d.phase)
		}
	}
	return p, nil
}

func parsePhase(path string, number int, title string, block []string, headingLine int) (core.Phase, []dependsRef, error) {
	title = builtRe.ReplaceAllString(title, "")
	title = strings.ReplaceAll(title, "✅", "")
	ph := core.Phase{Number: number, Title: strings.TrimSpace(title), Block: strings.Join(block, "")}

	var refs []dependsRef
	for j := 1; j < len(block); j++ {
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
				refs = append(refs, dependsRef{line: lineNo, phase: d})
			}
		case strings.HasPrefix(line, "**Files:**"):
			for _, m := range backtickRe.FindAllStringSubmatch(line, -1) {
				ph.Files = append(ph.Files, m[1])
			}
		case strings.HasPrefix(line, "**Risk:**"):
			ph.Risk = field(line, "**Risk:**")
		case strings.HasPrefix(line, "**Done when:**"):
			parts := []string{field(line, "**Done when:**")}
			for j+1 < len(block) && !strings.HasPrefix(block[j+1], "**") && !anyHeadingRe.MatchString(block[j+1]) {
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

func parseDepends(rest string) ([]int, error) {
	if rest == "" || rest == "—" || rest == "-" || strings.EqualFold(rest, "none") {
		return nil, nil
	}
	idx := strings.Index(rest, "Phase")
	if idx < 0 {
		return nil, fmt.Errorf("depends on names no phase: %q", rest)
	}
	var out []int
	for _, s := range numberRe.FindAllString(rest[idx:], -1) {
		n, _ := strconv.Atoi(s)
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("depends on names no phase: %q", rest)
	}
	return out, nil
}

func (Reader) Tick(path string, phase int) error {
	return errors.New("tick: not implemented")
}

func (Reader) Stamp(path, entryName, resolvedLine string) error {
	return errors.New("stamp: not implemented")
}
