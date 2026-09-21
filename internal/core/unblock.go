package core

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type Deferral struct {
	Entry  string
	Phases []int
}

func UnblockText(plan Plan, entries []Entry, list []Phase, donePath string) string {
	numbers := phaseNumbers(list)
	var b strings.Builder
	fmt.Fprintf(&b, "resolve first: %d open entries in %s block this run's phases %s. Walk them now, as your prompt's \"Resolving blockers\" says.\n", len(entries), plan.Path, joinInts(numbers))
	for i, e := range entries {
		fmt.Fprintf(&b, "\n## R%d — %s\n\nkind: %s · owner: %s · blocks: %s · timebox: %s · output: %s\n\n%s\n", i+1, e.Name, e.Kind, orNone(e.Owner), blockedIn(e, numbers), orNone(e.Timebox), orNone(e.Output), e.Body)
		for _, ph := range plan.Phases {
			if slices.Contains(numbers, ph.Number) && (e.BlocksAll || slices.Contains(e.BlocksPhases, ph.Number)) {
				fmt.Fprintf(&b, "\nBlocked phase %d:\n\n%s\n", ph.Number, strings.TrimRight(ph.Block, "\n"))
			}
		}
	}
	fmt.Fprintf(&b, "\nWhen the walk is done, write an empty file at %s.\n", donePath)
	return b.String()
}

func DeferBlocked(plan Plan, list []Phase) ([]Phase, []Deferral) {
	numbers := phaseNumbers(list)
	dropped := map[int]bool{}
	var deferrals []Deferral
	for _, e := range plan.Blocking(numbers) {
		var phases []int
		for _, n := range numbers {
			if dropped[n] || !(e.BlocksAll || slices.Contains(e.BlocksPhases, n)) {
				continue
			}
			for _, m := range append([]int{n}, Dependents(plan, n)...) {
				if slices.Contains(numbers, m) && !dropped[m] {
					dropped[m] = true
					phases = append(phases, m)
				}
			}
		}
		slices.Sort(phases)
		deferrals = append(deferrals, Deferral{Entry: e.Name, Phases: phases})
	}
	var kept []Phase
	for _, ph := range list {
		if !dropped[ph.Number] {
			kept = append(kept, ph)
		}
	}
	return kept, deferrals
}

func phaseNumbers(list []Phase) []int {
	numbers := make([]int, len(list))
	for i, ph := range list {
		numbers[i] = ph.Number
	}
	return numbers
}

func blockedIn(e Entry, numbers []int) string {
	if e.BlocksAll {
		return "every phase"
	}
	var hit []string
	for _, n := range e.BlocksPhases {
		if slices.Contains(numbers, n) {
			hit = append(hit, "Phase "+strconv.Itoa(n))
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
