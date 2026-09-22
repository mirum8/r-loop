package plan

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"r-loop/internal/core"
)

var (
	listRe       = regexp.MustCompile(`^(?:[-*+]|\d+[.)])[ \t]+(.*)$`)
	boxRe        = regexp.MustCompile(`^\[([ xX])\][ \t]?`)
	itemHeadRe   = regexp.MustCompile(`^(#{2,6})[ \t]+(.*)$`)
	fixedRe      = regexp.MustCompile(`<!--\s*fixed:.*?-->`)
	doneHeadRe   = regexp.MustCompile(`(?i)^(done|completed|fixed|shipped|archive)\b`)
	indentListRe = regexp.MustCompile(`^[ \t]+(?:[-*+]|\d+[.)])[ \t]+(?:\[[ xX]\][ \t]?)?(.*)$`)
)

type backlogItem struct {
	line   int
	title  string
	body   []string
	block  string
	hasBox bool
	done   bool
}

func isBacklog(lines []string) bool {
	for _, l := range lines {
		if phaseStartRe.MatchString(l) {
			return false
		}
	}
	return true
}

func readBacklog(path string, lines []string) (core.Plan, error) {
	if base := filepath.Base(path); strings.HasSuffix(base, "-notes.md") {
		return core.Plan{}, fmt.Errorf("%s is the notes file of an issues backlog, not the backlog; pass %s", path, strings.TrimSuffix(base, "-notes.md")+".md")
	}
	items := parseBacklog(lines)
	if len(items) == 0 {
		return core.Plan{}, fmt.Errorf("%s: no phases and no backlog items", path)
	}
	topic := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	p := core.Plan{Path: path, Topic: topic, Backlog: true}
	for i, it := range items {
		ph := core.Phase{ID: strconv.Itoa(i + 1), Title: it.title, Block: it.block}
		for _, b := range it.body {
			if m := indentListRe.FindStringSubmatch(b); m != nil {
				ph.Items = append(ph.Items, core.Item{Text: strings.TrimSpace(m[1]), Done: it.done})
			}
		}
		if len(ph.Items) == 0 {
			ph.Items = []core.Item{{Text: it.title, Done: it.done}}
		}
		p.Phases = append(p.Phases, ph)
	}
	return p, nil
}

func parseBacklog(lines []string) []backlogItem {
	var items []backlogItem
	var cur *backlogItem
	fenced, doneLevel := false, 0
	closeItem := func() {
		if cur == nil {
			return
		}
		for len(cur.body) > 0 && strings.TrimSpace(cur.body[len(cur.body)-1]) == "" {
			cur.body = cur.body[:len(cur.body)-1]
		}
		cur.block = strings.Join(append([]string{strings.TrimRight(lines[cur.line], "\r\n")}, cur.body...), "\n") + "\n"
		items = append(items, *cur)
		cur = nil
	}
	headingItem := false
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], "\r\n")
		fence := strings.HasPrefix(strings.TrimLeft(line, " \t"), "```")
		if fence {
			fenced = !fenced
		}
		if fenced || fence {
			if cur != nil {
				cur.body = append(cur.body, line)
			}
			continue
		}
		if m := itemHeadRe.FindStringSubmatch(line); m != nil {
			closeItem()
			level := len(m[1])
			if doneLevel != 0 && level <= doneLevel {
				doneLevel = 0
			}
			text := strings.TrimSpace(m[2])
			if doneHeadRe.MatchString(text) {
				doneLevel = level
				continue
			}
			if level <= 3 && proseFollows(lines, i+1) {
				cur = newItem(i, text, false, doneLevel != 0)
				headingItem = true
			}
			continue
		}
		if strings.HasPrefix(line, "#") && anyHeadingRe.MatchString(line) {
			closeItem()
			doneLevel = 0
			continue
		}
		if m := listRe.FindStringSubmatch(line); m != nil {
			closeItem()
			text, hasBox, ticked := m[1], false, false
			if b := boxRe.FindStringSubmatch(text); b != nil {
				hasBox, ticked = true, b[1] != " "
				text = text[len(b[0]):]
			}
			cur = newItem(i, text, hasBox, ticked || doneLevel != 0)
			headingItem = false
			continue
		}
		if cur == nil {
			continue
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' || headingItem {
			cur.body = append(cur.body, line)
			continue
		}
		closeItem()
	}
	closeItem()
	return items
}

func newItem(line int, text string, hasBox, done bool) *backlogItem {
	if fixedRe.MatchString(text) {
		done = true
		text = fixedRe.ReplaceAllString(text, "")
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "~~") && strings.HasSuffix(text, "~~") && len(text) > 4 {
		done = true
		text = strings.TrimSpace(text[2 : len(text)-2])
	}
	return &backlogItem{line: line, title: text, hasBox: hasBox, done: done}
}

func proseFollows(lines []string, from int) bool {
	for _, l := range lines[from:] {
		l = strings.TrimRight(l, "\r\n")
		if strings.TrimSpace(l) == "" {
			continue
		}
		return !anyHeadingRe.MatchString(l) && !listRe.MatchString(l)
	}
	return false
}

func tickBacklog(path string, lines []string, phase string) error {
	items := parseBacklog(lines)
	n, err := strconv.Atoi(phase)
	if err != nil || n < 1 || n > len(items) || strconv.Itoa(n) != phase {
		return fmt.Errorf("%s: no item %s", path, phase)
	}
	it := items[n-1]
	if it.done {
		return fmt.Errorf("%s item %s: %w", path, phase, ErrNothingToTick)
	}
	raw := lines[it.line]
	body := strings.TrimRight(raw, "\r\n")
	eol := raw[len(body):]
	if it.hasBox {
		m := listRe.FindStringSubmatchIndex(body)
		at := m[2]
		body = body[:at] + "[x]" + body[at+3:]
	}
	lines[it.line] = fmt.Sprintf("%s  <!-- fixed: r-loop/phase-%s -->%s", body, phase, eol)
	return writeLines(path, lines)
}
