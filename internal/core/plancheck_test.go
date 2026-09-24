package core

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

const cleanBlock = "### Phase %s\n**Implements:** Story\n**Depends on:** —\n**Done when:** `go test ./...`\n- [ ] item\n"

func cleanPhase(id string) Phase {
	return Phase{
		ID:       id,
		Title:    "Title " + id,
		Items:    []Item{{Text: "item"}},
		DoneWhen: "`go test ./...`",
		Block:    strings.ReplaceAll(cleanBlock, "%s", id),
	}
}

func planOf(phases ...Phase) Plan {
	return Plan{Path: "docs/topic/todo.md", Phases: phases}
}

func TestCheckPlan(t *testing.T) {
	for _, tc := range []struct {
		name         string
		plan         func() Plan
		notes, stops []string
	}{
		{
			name: "clean",
			plan: func() Plan { return planOf(cleanPhase("1"), cleanPhase("2")) },
		},
		{
			name: "no checklist",
			plan: func() Plan {
				ph := cleanPhase("1")
				ph.Items = nil
				return planOf(ph)
			},
			stops: []string{"Phase 1 — Title 1: no checklist items"},
		},
		{
			name: "fully ticked is not a stop",
			plan: func() Plan {
				ph := cleanPhase("1")
				ph.Items = []Item{{Text: "item", Done: true}}
				return planOf(ph)
			},
		},
		{
			name: "too many open items",
			plan: func() Plan {
				ph := cleanPhase("1")
				ph.Items = make([]Item, 13)
				ph.Items = append(ph.Items, Item{Text: "done", Done: true})
				return planOf(ph)
			},
			notes: []string{"Phase 1 — Title 1: 13 checklist items — too big for one session, split it"},
		},
		{
			name: "twelve open items",
			plan: func() Plan {
				ph := cleanPhase("1")
				ph.Items = make([]Item, 12)
				return planOf(ph)
			},
		},
		{
			name: "no done when",
			plan: func() Plan {
				ph := cleanPhase("1")
				ph.Block = strings.Replace(ph.Block, "**Done when:** `go test ./...`\n", "", 1)
				ph.DoneWhen = ""
				return planOf(ph)
			},
			notes: []string{"Phase 1 — Title 1: no 'Done when' check"},
		},
		{
			name: "done when with no runnable command",
			plan: func() Plan {
				ph := cleanPhase("1")
				ph.DoneWhen = "it works"
				return planOf(ph)
			},
			notes: []string{"Phase 1 — Title 1: 'Done when' names no runnable command or observable response"},
		},
		{
			name: "done when with a bare command word",
			plan: func() Plan {
				ph := cleanPhase("1")
				ph.DoneWhen = "go\ntest passes for the package"
				return planOf(ph)
			},
		},
		{
			name: "no implements",
			plan: func() Plan {
				ph := cleanPhase("1")
				ph.Block = strings.Replace(ph.Block, "**Implements:** Story\n", "", 1)
				return planOf(ph)
			},
			notes: []string{"Phase 1 — Title 1: no 'Implements' line — nothing ties it to a story"},
		},
		{
			name: "risk word without risk",
			plan: func() Plan {
				ph := cleanPhase("1")
				ph.Block += "- [ ] check Authentication and the payments migration, and auth again\n"
				return planOf(ph)
			},
			notes: []string{"Phase 1 — Title 1: touches auth, migration, payment but has no 'Risk:' line"},
		},
		{
			name: "risk word with risk",
			plan: func() Plan {
				ph := cleanPhase("1")
				ph.Block += "- [ ] check auth\n"
				ph.Risk = "security"
				return planOf(ph)
			},
		},
		{
			name: "no depends on",
			plan: func() Plan {
				ph := cleanPhase("1")
				ph.Block = strings.Replace(ph.Block, "**Depends on:** —\n", "", 1)
				return planOf(ph)
			},
			notes: []string{"Phase 1 — Title 1: no 'Depends on' line — every phase declares its edges, '—' when it has none"},
		},
		{
			name: "self dependency",
			plan: func() Plan {
				ph := cleanPhase("1")
				ph.DependsOn = []string{"1"}
				return planOf(ph)
			},
			notes: []string{"dependency cycle through phase(s): 1"},
		},
		{
			name: "forward dependency and cycle",
			plan: func() Plan {
				one, two := cleanPhase("1"), cleanPhase("2")
				one.DependsOn = []string{"2"}
				two.DependsOn = []string{"1"}
				return planOf(one, two)
			},
			notes: []string{"dependency cycle through phase(s): 1"},
		},
		{
			name: "same wave shares a file",
			plan: func() Plan {
				one, two := cleanPhase("1"), cleanPhase("2")
				one.Files = []string{"internal/a.go", "internal/b.go"}
				two.Files = []string{"internal/b.go", "internal/a.go"}
				return planOf(one, two)
			},
			notes: []string{"Phase 1 and Phase 2 are both in wave 0 but touch internal/a.go, internal/b.go — they cannot run concurrently; add a 'Depends on' edge between them"},
		},
		{
			name: "different waves share a file",
			plan: func() Plan {
				one, two := cleanPhase("1"), cleanPhase("2")
				one.Files = []string{"internal/a.go"}
				two.Files = []string{"internal/a.go"}
				two.DependsOn = []string{"1"}
				return planOf(one, two)
			},
		},
		{
			name: "same wave shares only excluded files",
			plan: func() Plan {
				one, two := cleanPhase("1"), cleanPhase("2")
				shared := []string{"docs/topic/todo.md", "internal/x/frame.golden", "internal/x/testdata/in.md", ".agent/skills/test-app/SKILL.md"}
				one.Files, two.Files = shared, shared
				return planOf(one, two)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckPlan(tc.plan())

			if !slices.Equal(got.Notes, tc.notes) {
				t.Errorf("notes = %q, want %q", got.Notes, tc.notes)
			}
			if !slices.Equal(got.Stops, tc.stops) {
				t.Errorf("stops = %q, want %q", got.Stops, tc.stops)
			}
		})
	}
}

func TestCheckPlanIsSilentOnABacklog(t *testing.T) {
	ph := cleanPhase("1")
	ph.Items, ph.Block = nil, "- [ ] bare\n"

	got := CheckPlan(Plan{Backlog: true, Phases: []Phase{ph}})

	if len(got.Notes) != 0 || len(got.Stops) != 0 {
		t.Errorf("findings on a backlog: %+v", got)
	}
}

func TestWavesAreTheLongestPathOverDependsOn(t *testing.T) {
	ids := []string{"1", "2", "3", "3a", "4"}
	deps := map[string][]string{"2": {"1"}, "3": {"1"}, "3a": {"2", "3"}, "4": {"1", "3a"}}
	var phases []Phase
	for _, id := range ids {
		phases = append(phases, Phase{ID: id, DependsOn: deps[id]})
	}

	wave, cycle := Waves(Plan{Phases: phases})

	if want := map[string]int{"1": 0, "2": 1, "3": 1, "3a": 2, "4": 3}; !maps.Equal(wave, want) {
		t.Errorf("waves = %v, want %v", wave, want)
	}
	if len(cycle) != 0 {
		t.Errorf("cycle = %v", cycle)
	}
}

func TestWavesNameThePhasesInACycle(t *testing.T) {
	phases := []Phase{
		{ID: "1"},
		{ID: "2", DependsOn: []string{"4"}},
		{ID: "3", DependsOn: []string{"2"}},
		{ID: "4", DependsOn: []string{"3"}},
	}

	wave, cycle := Waves(Plan{Phases: phases})

	if len(cycle) != 1 || cycle[0] != "2" {
		t.Errorf("cycle = %v", cycle)
	}
	if len(wave) != 4 || wave["1"] != 0 {
		t.Errorf("waves = %v", wave)
	}
}
