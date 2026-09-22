package core

import (
	"fmt"
	"slices"
	"strings"
)

type Deferral struct {
	Entry  string
	Phases []string
}

func UnblockText(plan Plan, entries []Entry, list []Phase, donePath string) string {
	ids := phaseIDs(list)
	var b strings.Builder
	fmt.Fprintf(&b, "resolve first: %d open entries in %s block this run's phases %s. Walk them now, as your prompt's \"Resolving blockers\" says.\n", len(entries), plan.Path, joinIDs(ids))
	for i, e := range entries {
		fmt.Fprintf(&b, "\n## R%d — %s\n\nkind: %s · owner: %s · blocks: %s · timebox: %s · output: %s\n\n%s\n", i+1, e.Name, e.Kind, orNone(e.Owner), blockedIn(e, ids), orNone(e.Timebox), orNone(e.Output), e.Body)
		for _, ph := range plan.Phases {
			if slices.Contains(ids, ph.ID) && (e.BlocksAll || slices.Contains(e.BlocksPhases, ph.ID)) {
				fmt.Fprintf(&b, "\nBlocked phase %s:\n\n%s\n", ph.ID, strings.TrimRight(ph.Block, "\n"))
			}
		}
	}
	fmt.Fprintf(&b, "\nWhen the walk is done, write an empty file at %s.\n", donePath)
	return b.String()
}

func DeferBlocked(plan Plan, list []Phase) ([]Phase, []Deferral) {
	ids := phaseIDs(list)
	dropped := map[string]bool{}
	var deferrals []Deferral
	for _, e := range plan.Blocking(ids) {
		var phases []string
		for _, n := range ids {
			if dropped[n] || !(e.BlocksAll || slices.Contains(e.BlocksPhases, n)) {
				continue
			}
			for _, m := range append([]string{n}, Dependents(plan, n)...) {
				if slices.Contains(ids, m) && !dropped[m] {
					dropped[m] = true
					phases = append(phases, m)
				}
			}
		}
		slices.SortFunc(phases, ComparePhaseIDs)
		deferrals = append(deferrals, Deferral{Entry: e.Name, Phases: phases})
	}
	var kept []Phase
	for _, ph := range list {
		if !dropped[ph.ID] {
			kept = append(kept, ph)
		}
	}
	return kept, deferrals
}

func phaseIDs(list []Phase) []string {
	ids := make([]string, len(list))
	for i, ph := range list {
		ids[i] = ph.ID
	}
	return ids
}

func blockedIn(e Entry, ids []string) string {
	if e.BlocksAll {
		return "every phase"
	}
	var hit []string
	for _, n := range e.BlocksPhases {
		if slices.Contains(ids, n) {
			hit = append(hit, "Phase "+n)
		}
	}
	return strings.Join(hit, ", ")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
