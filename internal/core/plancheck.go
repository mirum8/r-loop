package core

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type PlanFindings struct {
	Notes, Stops []string
}

const maxOpenItems = 12

var (
	riskWordRe  = regexp.MustCompile(`(?i)\b(auth|money|persistence|concurrency|security|migration|payment)`)
	runnableRe  = regexp.MustCompile("`[^`]+`|\\b(?:curl|mvn|npm|pytest|go test|gradle|docker|psql)\\b")
	dependsOnRe = regexp.MustCompile(`(?m)^\*\*Depends on:\*\*`)
)

func CheckPlan(p Plan) PlanFindings {
	var f PlanFindings
	if p.Backlog {
		return f
	}
	for _, ph := range p.Phases {
		title := fmt.Sprintf("Phase %s — %s", ph.ID, ph.Title)
		if len(ph.Items) == 0 {
			f.Stops = append(f.Stops, title+": no checklist items")
		} else if open := openItems(ph); open > maxOpenItems {
			f.Notes = append(f.Notes, fmt.Sprintf("%s: %d checklist items — too big for one session, split it", title, open))
		}
		if !strings.Contains(ph.Block, "**Done when:**") {
			f.Notes = append(f.Notes, title+": no 'Done when' check")
		} else if !runnableRe.MatchString(strings.Join(strings.Fields(ph.DoneWhen), " ")) {
			f.Notes = append(f.Notes, title+": 'Done when' names no runnable command or observable response")
		}
		if !strings.Contains(ph.Block, "**Implements:**") {
			f.Notes = append(f.Notes, title+": no 'Implements' line — nothing ties it to a story")
		}
		if words := riskWords(ph.Block); len(words) > 0 && ph.Risk == "" {
			f.Notes = append(f.Notes, fmt.Sprintf("%s: touches %s but has no 'Risk:' line", title, strings.Join(words, ", ")))
		}
		if !dependsOnRe.MatchString(ph.Block) {
			f.Notes = append(f.Notes, title+": no 'Depends on' line — every phase declares its edges, '—' when it has none")
		}
	}
	wave, cyclic := Waves(p)
	if len(cyclic) > 0 {
		f.Notes = append(f.Notes, "dependency cycle through phase(s): "+strings.Join(cyclic, ", "))
	}
	f.Notes = append(f.Notes, sharedFiles(p, wave)...)
	return f
}

func openItems(ph Phase) int {
	n := 0
	for _, it := range ph.Items {
		if !it.Done {
			n++
		}
	}
	return n
}

func riskWords(block string) []string {
	var words []string
	for _, m := range riskWordRe.FindAllStringSubmatch(block, -1) {
		if w := strings.ToLower(m[1]); !slices.Contains(words, w) {
			words = append(words, w)
		}
	}
	slices.Sort(words)
	if len(words) > 3 {
		words = words[:3]
	}
	return words
}

func sharedFiles(p Plan, wave map[string]int) []string {
	var notes []string
	planName := filepath.Base(p.Path)
	for i, a := range p.Phases {
		for _, b := range p.Phases[i+1:] {
			if wave[a.ID] != wave[b.ID] {
				continue
			}
			var shared []string
			for _, f := range a.Files {
				if slices.Contains(b.Files, f) && filepath.Base(f) != planName && worthComparing(f) && !slices.Contains(shared, f) {
					shared = append(shared, f)
				}
			}
			if len(shared) > 0 {
				slices.Sort(shared)
				notes = append(notes, fmt.Sprintf("Phase %s and Phase %s are both in wave %d but touch %s — they cannot run concurrently; add a 'Depends on' edge between them", a.ID, b.ID, wave[a.ID], strings.Join(shared, ", ")))
			}
		}
	}
	return notes
}

func worthComparing(path string) bool {
	if strings.HasSuffix(path, ".golden") {
		return false
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, dir := range parts[:len(parts)-1] {
		if dir == "testdata" || (strings.HasPrefix(dir, ".") && dir != "." && dir != "..") {
			return false
		}
	}
	return true
}

func Waves(p Plan) (map[string]int, []string) {
	deps := map[string][]string{}
	for _, ph := range p.Phases {
		deps[ph.ID] = ph.DependsOn
	}
	wave := map[string]int{}
	resolving := map[string]bool{}
	cyclic := map[string]bool{}
	var walk func(n string) int
	walk = func(n string) int {
		if w, ok := wave[n]; ok {
			return w
		}
		if resolving[n] {
			cyclic[n] = true
			return 0
		}
		resolving[n] = true
		w := 0
		for _, d := range deps[n] {
			if _, ok := deps[d]; ok {
				w = max(w, 1+walk(d))
			}
		}
		delete(resolving, n)
		wave[n] = w
		return w
	}
	for _, ph := range p.Phases {
		walk(ph.ID)
	}
	var cycle []string
	for n := range cyclic {
		cycle = append(cycle, n)
	}
	slices.SortFunc(cycle, ComparePhaseIDs)
	return wave, cycle
}
