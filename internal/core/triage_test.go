package core

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func backlogItem(id, title string, criteria ...string) Phase {
	ph := Phase{ID: id, Title: title, Block: "- [ ] " + title + "\n"}
	for _, c := range criteria {
		ph.Items = append(ph.Items, Item{Text: c})
		ph.Block += "      - " + c + "\n"
	}
	return ph
}

func TestGroupBacklogKeepsTheLowestIDAndFoldsMembers(t *testing.T) {
	plan := Plan{Path: "issues.md", Backlog: true, Phases: []Phase{
		backlogItem("1", "one", "a"),
		backlogItem("2", "two", "b"),
		backlogItem("3", "three", "c1", "c2"),
		backlogItem("4", "four", "d"),
		backlogItem("5", "five", "e"),
	}}
	before := slices.Clone(plan.Phases)

	got := GroupBacklog(plan, []Group{
		{ID: "g1", Items: []string{"5", "2", "3"}, Subsystem: "store"},
		{ID: "g2", Items: []string{"4"}, Subsystem: "solo"},
	})

	var ids []string
	for _, ph := range got.Phases {
		ids = append(ids, ph.ID)
	}
	if !slices.Equal(ids, []string{"1", "2", "4"}) {
		t.Fatalf("phases = %v", ids)
	}
	group := got.Phases[1]
	want := Phase{
		ID:      "2",
		Title:   "store (items 2, 3, 5)",
		Members: []string{"2", "3", "5"},
		Items:   []Item{{Text: "#2 b"}, {Text: "#3 c1"}, {Text: "#3 c2"}, {Text: "#5 e"}},
		Block:   "- [ ] two\n      - b\n\n- [ ] three\n      - c1\n      - c2\n\n- [ ] five\n      - e\n",
	}
	if !reflect.DeepEqual(group, want) {
		t.Errorf("group phase =\n%+v\nwant\n%+v", group, want)
	}
	if got.Phases[2].Members != nil || got.Phases[0].Members != nil {
		t.Errorf("a single item became a group: %+v", got.Phases)
	}
	if !reflect.DeepEqual(plan.Phases, before) {
		t.Errorf("input plan changed: %+v", plan.Phases)
	}
	if !got.Backlog || got.Path != "issues.md" {
		t.Errorf("plan fields lost: %+v", got)
	}
}

func TestTickIDsAreTheMembersOrTheID(t *testing.T) {
	if got := (Phase{ID: "3"}).TickIDs(); !slices.Equal(got, []string{"3"}) {
		t.Errorf("single = %v", got)
	}
	if got := (Phase{ID: "3", Members: []string{"3", "7"}}).TickIDs(); !slices.Equal(got, []string{"3", "7"}) {
		t.Errorf("group = %v", got)
	}
}

func citeAGo(c string) string {
	if strings.HasPrefix(c, "a.go:") {
		return ""
	}
	return "citation " + c + " is not a file in the primary tree"
}

func triagePlan() Plan {
	return Plan{Path: "docs/x/todo.md", Phases: []Phase{
		{ID: "1", Title: "Model", Items: []Item{{Text: "a", Done: true}}, Milestone: 1},
		{ID: "2", Title: "Store", Items: []Item{{Text: "b"}}, Files: []string{"store.go", "store_test.go", "run.go", "dir.go"}, Risk: "persistence", DoneWhen: "`go test ./store/...` is green.\nand more", DependsOn: []string{"1"}, Milestone: 1},
		{ID: "3", Title: "Loop | core", Items: []Item{{Text: "c"}}, Files: []string{"loop.go"}, DoneWhen: "`go test ./...`", DependsOn: []string{"2"}, Milestone: 1},
		{ID: "4", Title: "Face", Items: []Item{{Text: "d"}}, DependsOn: []string{"1"}, Milestone: 2},
		{ID: "5", Title: "TUI", Items: []Item{{Text: "e"}}, DependsOn: []string{"4"}, Milestone: 2},
	}, Milestones: []Milestone{{Number: 1, Name: "Core", Phases: []string{"1", "2", "3"}}, {Number: 2, Name: "Face", Phases: []string{"4", "5"}}}}
}

func triageBacklog() Plan {
	return Plan{Path: "issues.md", Backlog: true, Phases: []Phase{
		backlogItem("1", "one", "a"),
		backlogItem("2", "two", "b"),
		backlogItem("3", "three", "c"),
		backlogItem("4", "four", "d"),
	}}
}

func fixItem(id, risk, confidence string) ItemVerdict {
	return ItemVerdict{ID: id, Title: "item " + id, Verdict: VerdictFix, Category: "bug", Confidence: confidence, RootCause: "cause", Touches: []string{"x.go"}, Risk: risk}
}

func backlogTriage() Triage {
	return Triage{
		Items: []ItemVerdict{
			fixItem("1", RiskLocal, "high"),
			fixItem("2", RiskDeep, "medium"),
			{ID: "3", Title: "item 3", Verdict: VerdictSkip, Category: "stale", Confidence: "high", RootCause: "gone", SkipReason: "fixed at a.go:12"},
			fixItem("4", RiskCosmetic, "high"),
		},
		Groups: []Group{
			{ID: "G1", Items: []string{"2", "1"}, Subsystem: "store", Rationale: "same writer"},
			{ID: "G2", Items: []string{"4"}, Subsystem: "face"},
		},
	}
}

func planTriage() Triage {
	return Triage{Phases: []PhaseVerdict{
		{Phase: "2", Status: VerdictBuild},
		{Phase: "3", Status: VerdictBuild},
		{Phase: "4", Status: VerdictBuild},
		{Phase: "5", Status: VerdictBuild},
	}}
}

func TestValidateTriageRefuses(t *testing.T) {
	plan, backlog := triagePlan(), triageBacklog()
	planList, backlogList := plan.Phases[1:], backlog.Phases
	withPhase := func(v PhaseVerdict) Triage {
		tr := planTriage()
		tr.Phases[0] = v
		return tr
	}
	withItem := func(i int, edit func(*ItemVerdict)) Triage {
		tr := backlogTriage()
		edit(&tr.Items[i])
		return tr
	}
	withGroups := func(groups ...Group) Triage {
		tr := backlogTriage()
		tr.Groups = groups
		return tr
	}
	for _, c := range []struct {
		name string
		plan Plan
		tr   Triage
		want string
	}{
		{"missing phase", plan, Triage{Phases: planTriage().Phases[:3]}, "phase 5 has no verdict"},
		{"extra phase", plan, withPhase(PhaseVerdict{Phase: "1", Status: VerdictBuild}), "phase 1 is not in the run list 2, 3, 4, 5"},
		{"duplicate phase", plan, withPhase(PhaseVerdict{Phase: "3", Status: VerdictBuild}), "phase 3 has more than one verdict"},
		{"bad status", plan, withPhase(PhaseVerdict{Phase: "2", Status: "done"}), `phase 2: status "done" is not one of build, already-done, blocked`},
		{"blocked without note", plan, withPhase(PhaseVerdict{Phase: "2", Status: VerdictBlocked}), "phase 2: blocked needs a note saying why"},
		{"already-done without note", plan, withPhase(PhaseVerdict{Phase: "2", Status: VerdictAlreadyDone, Note: " "}), "phase 2: already-done needs a note saying why"},
		{"already-done uncited", plan, withPhase(PhaseVerdict{Phase: "2", Status: VerdictAlreadyDone, Note: "store exists"}), "phase 2: already-done needs a path:line in the note that exists: none cited"},
		{"already-done bad citation", plan, withPhase(PhaseVerdict{Phase: "2", Status: VerdictAlreadyDone, Note: "see b.go:3"}), "citation b.go:3 is not a file in the primary tree"},
		{"plan with items", plan, Triage{Phases: planTriage().Phases, Items: []ItemVerdict{fixItem("2", RiskLocal, "high")}}, "a plan triage carries phases only"},
		{"backlog with phases", backlog, Triage{Phases: []PhaseVerdict{{Phase: "1", Status: VerdictBuild}}}, "a backlog triage carries items and groups, not phases"},
		{"missing item", backlog, Triage{Items: backlogTriage().Items[:3], Groups: backlogTriage().Groups[:1]}, "item 4 has no verdict"},
		{"extra item", backlog, withItem(0, func(v *ItemVerdict) { v.ID = "9" }), "item 9 is not in the run list 1, 2, 3, 4"},
		{"duplicate item", backlog, withItem(1, func(v *ItemVerdict) { v.ID = "1" }), "item 1 has more than one verdict"},
		{"bad verdict", backlog, withItem(0, func(v *ItemVerdict) { v.Verdict = "maybe" }), `item 1: verdict "maybe" is not one of fix, skip`},
		{"bad category", backlog, withItem(0, func(v *ItemVerdict) { v.Category = "idea" }), `item 1: category "idea" is not one of`},
		{"bad confidence", backlog, withItem(0, func(v *ItemVerdict) { v.Confidence = "sure" }), `item 1: confidence "sure" is not one of low, medium, high`},
		{"bad risk", backlog, withItem(0, func(v *ItemVerdict) { v.Risk = "" }), `item 1: risk "" is not one of cosmetic, local, deep`},
		{"fix without touches", backlog, withItem(0, func(v *ItemVerdict) { v.Touches = nil }), "item 1: a fix needs touches"},
		{"skip without reason", backlog, withItem(2, func(v *ItemVerdict) { v.SkipReason = "" }), "item 3: a skip needs a skip_reason"},
		{"stale uncited", backlog, withItem(2, func(v *ItemVerdict) { v.SkipReason = "already fixed" }), "item 3: a stale skip needs a path:line in skip_reason that exists: none cited"},
		{"duplicate bad citation", backlog, withItem(2, func(v *ItemVerdict) { v.Category, v.SkipReason = "duplicate", "same as c.go:4" }), "item 3: a duplicate skip needs a path:line in skip_reason that exists: citation c.go:4"},
		{"fix in no group", backlog, withGroups(backlogTriage().Groups[0]), "item 4 is a fix but in no group"},
		{"fix in two groups", backlog, withGroups(backlogTriage().Groups[0], Group{ID: "G2", Items: []string{"4", "1"}, Rationale: "r"}), "item 1 is in groups G1 and G2"},
		{"group with a skip", backlog, withGroups(backlogTriage().Groups[0], Group{ID: "G2", Items: []string{"4", "3"}, Rationale: "r"}), "group G2: item 3 is a skip"},
		{"group with an unknown item", backlog, withGroups(backlogTriage().Groups[0], Group{ID: "G2", Items: []string{"4", "8"}, Rationale: "r"}), "group G2: item 8 is not in the run list"},
		{"duplicate group id", backlog, withGroups(backlogTriage().Groups[0], Group{ID: "G1", Items: []string{"4"}}), "group_id G1 is used by more than one group"},
		{"group without rationale", backlog, withGroups(Group{ID: "G1", Items: []string{"1", "2"}}, backlogTriage().Groups[1]), "group G1 holds 2 items but gives no rationale"},
		{"note with a newline", plan, withPhase(PhaseVerdict{Phase: "2", Status: VerdictBlocked, Note: "needs\na key"}), "phase 2: note contains a control character"},
		{"root cause with an escape", backlog, withItem(0, func(v *ItemVerdict) { v.RootCause = "x\x1b[2J" }), "item 1: root_cause_or_scope contains a control character"},
		{"touches with a tab", backlog, withItem(0, func(v *ItemVerdict) { v.Touches = []string{"x.go", "y.go\t"} }), "item 1: touches contains a control character"},
		{"skip reason with a carriage return", backlog, withItem(2, func(v *ItemVerdict) { v.SkipReason = "fixed at a.go:12\r" }), "item 3: skip_reason contains a control character"},
		{"subsystem with a newline", backlog, withGroups(Group{ID: "G1", Items: []string{"2", "1"}, Subsystem: "store\nfix", Rationale: "r"}, backlogTriage().Groups[1]), "group G1: subsystem contains a control character"},
		{"rationale with a bell", backlog, withGroups(Group{ID: "G1", Items: []string{"2", "1"}, Subsystem: "store", Rationale: "r\x07"}, backlogTriage().Groups[1]), "group G1: rationale contains a control character"},
		{"cosmetic with deep", backlog, withGroups(Group{ID: "G1", Items: []string{"2", "4", "1"}, Rationale: "r"}), "group G1 mixes cosmetic and deep items"},
	} {
		t.Run(c.name, func(t *testing.T) {
			list := planList
			if c.plan.Backlog {
				list = backlogList
			}
			_, err := ValidateTriage(c.plan, list, c.tr, citeAGo)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestValidateTriageNormalisesGroupRiskAndConfidence(t *testing.T) {
	in := backlogTriage()
	in.Groups[0].Risk, in.Groups[0].Confidence = "cosmetic", "high"

	got, err := ValidateTriage(triageBacklog(), triageBacklog().Phases, in, citeAGo)

	if err != nil {
		t.Fatal(err)
	}
	if g := got.Groups[0]; g.Risk != RiskDeep || g.Confidence != "medium" {
		t.Errorf("group = %+v", g)
	}
	if g := got.Groups[1]; g.Risk != RiskCosmetic || g.Confidence != "high" {
		t.Errorf("group = %+v", g)
	}
	if in.Groups[0].Risk != "cosmetic" {
		t.Errorf("input changed: %+v", in.Groups[0])
	}
}

func TestValidateTriageAcceptsACitedAlreadyDonePhase(t *testing.T) {
	tr := planTriage()
	tr.Phases[0] = PhaseVerdict{Phase: "2", Status: VerdictAlreadyDone, Note: "built in c.go:1 and a.go:40-52"}

	if _, err := ValidateTriage(triagePlan(), triagePlan().Phases[1:], tr, citeAGo); err != nil {
		t.Fatal(err)
	}
}

func TestApplyGateDropSplitMerge(t *testing.T) {
	plan, backlog := triagePlan(), triageBacklog()
	said := "ok"

	dropped, err := ApplyGate(plan, plan.Phases[1:], planTriage(), GateDecision{Decision: GateRevise, Drop: []string{"4"}, MaintainerSaid: said})
	if err != nil || !slices.Equal(dropped.Dropped, []string{"4"}) {
		t.Fatalf("plan drop = %+v, %v", dropped, err)
	}
	kept, _, _ := TriageResult(plan, plan.Phases[1:], dropped)
	if ids := phaseIDs(kept); !slices.Equal(ids, []string{"2", "3"}) {
		t.Errorf("kept after dropping 4 = %v", ids)
	}

	five := backlog
	five.Phases = append(slices.Clone(backlog.Phases), backlogItem("5", "five", "e"))
	tr := backlogTriage()
	tr.Items = append(tr.Items, fixItem("5", RiskLocal, "low"))
	tr.Groups[0].Items = append(tr.Groups[0].Items, "5")
	split, err := ApplyGate(five, five.Phases, tr, GateDecision{Decision: GateRevise, Split: []Split{{Group: "G1", Into: [][]string{{"2"}, {"1", "5"}}}}, MaintainerSaid: said})
	if err != nil {
		t.Fatal(err)
	}
	want := []Group{
		{ID: "G1.1", Items: []string{"2"}, Subsystem: "store", Rationale: "same writer", Risk: RiskDeep, Confidence: "medium"},
		{ID: "G1.2", Items: []string{"1", "5"}, Subsystem: "store", Rationale: "same writer", Risk: RiskLocal, Confidence: "low"},
		{ID: "G2", Items: []string{"4"}, Subsystem: "face", Risk: RiskCosmetic, Confidence: "high"},
	}
	if !reflect.DeepEqual(split.Groups, want) {
		t.Errorf("split groups =\n%+v\nwant\n%+v", split.Groups, want)
	}
	if !slices.Equal(tr.Groups[0].Items, []string{"2", "1", "5"}) {
		t.Errorf("input changed: %+v", tr.Groups[0])
	}

	merged, err := ApplyGate(five, five.Phases, split, GateDecision{Decision: GateRevise, Merge: [][]string{{"G1.2", "G2"}}, MaintainerSaid: said})
	if err != nil {
		t.Fatal(err)
	}
	if g := merged.Groups[1]; len(merged.Groups) != 2 || g.ID != "G1.2" || !slices.Equal(g.Items, []string{"1", "5", "4"}) || g.Rationale != "same writer" || g.Risk != RiskLocal {
		t.Errorf("merged = %+v", merged.Groups)
	}

	droppedGroup, err := ApplyGate(backlog, backlog.Phases, backlogTriage(), GateDecision{Decision: GateGo, Drop: []string{"G2", "1"}, MaintainerSaid: said})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(droppedGroup.Dropped, []string{"4", "1"}) || len(droppedGroup.Groups) != 1 || !slices.Equal(droppedGroup.Groups[0].Items, []string{"2"}) {
		t.Errorf("dropped = %+v", droppedGroup)
	}
}

func TestApplyGateRefuses(t *testing.T) {
	plan, backlog := triagePlan(), triageBacklog()
	for _, c := range []struct {
		name string
		plan Plan
		gate GateDecision
		want string
	}{
		{"bad decision", backlog, GateDecision{Decision: "yes", MaintainerSaid: "y"}, `decision "yes" is not one of go, revise, abort`},
		{"unquoted", backlog, GateDecision{Decision: GateGo}, "maintainer_said is empty"},
		{"unknown phase", plan, GateDecision{Decision: GateRevise, Drop: []string{"1"}, MaintainerSaid: "y"}, "drop: phase 1 is not in the run list"},
		{"split on a plan", plan, GateDecision{Decision: GateRevise, Split: []Split{{Group: "2"}}, MaintainerSaid: "y"}, "split and merge apply to a backlog's groups"},
		{"unknown drop", backlog, GateDecision{Decision: GateRevise, Drop: []string{"G9"}, MaintainerSaid: "y"}, "drop: G9 is neither an item nor a group"},
		{"unknown split", backlog, GateDecision{Decision: GateRevise, Split: []Split{{Group: "G9", Into: [][]string{{"1"}, {"2"}}}}, MaintainerSaid: "y"}, "split: group G9 does not exist"},
		{"one part", backlog, GateDecision{Decision: GateRevise, Split: []Split{{Group: "G1", Into: [][]string{{"1", "2"}}}}, MaintainerSaid: "y"}, "split: group G1 needs at least two parts"},
		{"item twice", backlog, GateDecision{Decision: GateRevise, Split: []Split{{Group: "G1", Into: [][]string{{"1", "2"}, {"2"}}}}, MaintainerSaid: "y"}, "split: item 2 appears in more than one part of group G1"},
		{"item left out", backlog, GateDecision{Decision: GateRevise, Split: []Split{{Group: "G1", Into: [][]string{{"1"}, {"4"}}}}, MaintainerSaid: "y"}, "split: item 4 is not in group G1"},
		{"part missing an item", backlog, GateDecision{Decision: GateRevise, Split: []Split{{Group: "G1", Into: [][]string{{"1"}, {}}}}, MaintainerSaid: "y"}, "split: part 2 of group G1 is empty"},
		{"unknown merge", backlog, GateDecision{Decision: GateRevise, Merge: [][]string{{"G1", "G7"}}, MaintainerSaid: "y"}, "merge: group G7 does not exist"},
		{"merge alone", backlog, GateDecision{Decision: GateRevise, Merge: [][]string{{"G1"}}, MaintainerSaid: "y"}, "merge: G1 names fewer than two groups"},
		{"merge breaks the risk rule", backlog, GateDecision{Decision: GateRevise, Merge: [][]string{{"G1", "G2"}}, MaintainerSaid: "y"}, "group G1 mixes cosmetic and deep items"},
	} {
		t.Run(c.name, func(t *testing.T) {
			list, tr := c.plan.Phases[1:], planTriage()
			if c.plan.Backlog {
				list, tr = c.plan.Phases, backlogTriage()
			}
			_, err := ApplyGate(c.plan, list, tr, c.gate)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestApplyGateSplitNeedsEveryItem(t *testing.T) {
	backlog := triageBacklog()
	tr := backlogTriage()
	tr.Items[2] = fixItem("3", RiskLocal, "high")
	tr.Groups[0].Items = []string{"1", "2", "3"}

	_, err := ApplyGate(backlog, backlog.Phases, tr, GateDecision{Decision: GateRevise, Split: []Split{{Group: "G1", Into: [][]string{{"1"}, {"2"}}}}, MaintainerSaid: "y"})

	if err == nil || err.Error() != "split: item 3 of group G1 is in no part" {
		t.Fatalf("err = %v", err)
	}
}

func TestTriageResultForAPlan(t *testing.T) {
	plan := triagePlan()
	plan.Phases = append(plan.Phases, Phase{ID: "6", Title: "Docs", Items: []Item{{Text: "f"}}, DependsOn: []string{"2"}})
	tr := planTriage()
	tr.Phases = append(tr.Phases, PhaseVerdict{Phase: "6", Status: VerdictBuild})
	tr.Phases[0] = PhaseVerdict{Phase: "2", Status: VerdictAlreadyDone, Note: "a.go:3"}
	tr.Phases[2] = PhaseVerdict{Phase: "4", Status: VerdictBlocked, Note: "needs a key"}
	tr.Dropped = []string{"3"}

	kept, skipped, groups := TriageResult(plan, plan.Phases[1:], tr)

	if ids := phaseIDs(kept); !slices.Equal(ids, []string{"6"}) {
		t.Errorf("kept = %v", ids)
	}
	want := []Event{
		{Kind: TriageSkipped, Phase: "2", Fields: map[string]string{"phase": "2", "status": "already-done", "reason": "already done: a.go:3"}},
		{Kind: TriageSkipped, Phase: "3", Fields: map[string]string{"phase": "3", "status": "dropped", "reason": "dropped by the maintainer"}},
		{Kind: TriageSkipped, Phase: "4", Fields: map[string]string{"phase": "4", "status": "blocked", "reason": "blocked: needs a key"}},
		{Kind: TriageSkipped, Phase: "5", Fields: map[string]string{"phase": "5", "status": "dependent", "reason": "depends on phase 4 (blocked)"}},
	}
	if !reflect.DeepEqual(skipped, want) {
		t.Errorf("skipped =\n%+v\nwant\n%+v", skipped, want)
	}
	if groups != nil {
		t.Errorf("groups = %+v", groups)
	}
	lines := skipLines(RunState{Events: skipped})
	if lines[0] != "phase 2: triage-skipped: already done: a.go:3" {
		t.Errorf("report line = %q", lines[0])
	}
}

func TestTriageResultForABacklog(t *testing.T) {
	backlog := triageBacklog()
	backlog.Phases = append(backlog.Phases, backlogItem("5", "five", "e"))
	tr := backlogTriage()
	tr.Items = append(tr.Items, fixItem("5", RiskLocal, "high"))
	tr.Groups[0].Items = []string{"1", "5", "2"}
	tr.Dropped = []string{"4"}

	kept, skipped, groups := TriageResult(backlog, backlog.Phases, tr)

	if ids := phaseIDs(kept); !slices.Equal(ids, []string{"1"}) || !slices.Equal(kept[0].Members, []string{"1", "2", "5"}) {
		t.Errorf("kept = %+v", kept)
	}
	want := []Event{
		{Kind: TriageSkipped, Phase: "3", Fields: map[string]string{"phase": "3", "status": "stale", "reason": "stale: fixed at a.go:12"}},
		{Kind: TriageSkipped, Phase: "4", Fields: map[string]string{"phase": "4", "status": "dropped", "reason": "dropped by the maintainer"}},
	}
	if !reflect.DeepEqual(skipped, want) {
		t.Errorf("skipped = %+v", skipped)
	}
	if len(groups) != 1 || groups[0].ID != "G1" {
		t.Errorf("groups = %+v", groups)
	}
}

func triageView(p Plan, list []Phase) TriageView {
	return TriageView{
		Plan: p, List: list,
		Checks:    PlanFindings{Notes: []string{"Phase 4 — Face: no 'Implements' line"}},
		Deferrals: []Deferral{{Entry: "Pick a DB", Phases: []string{"7"}}},
		Kinds: []StepKind{
			{Name: "plan", Row: StepRow{Reviewers: []Reviewer{{Provider: "codex"}}, Rounds: 1}},
			{Name: "implement", Row: StepRow{Reviewers: []Reviewer{{Provider: "codex"}}, Rounds: 2}},
			{Name: "land", Row: StepRow{Rounds: 3}},
		},
	}
}

func TestRenderTriagePlanAndBacklog(t *testing.T) {
	plan := triagePlan()
	tr := planTriage()
	tr.Phases[0] = PhaseVerdict{Phase: "2", Status: VerdictBuild, Note: "no store | yet"}
	tr.Phases[2] = PhaseVerdict{Phase: "4", Status: VerdictBlocked, Note: "needs a key"}

	summary, table := RenderTriage(triageView(plan, plan.Phases[1:]), &tr)

	wantPlan := `Plan: docs/x/todo.md — 5 phases, 1 already done, running 2, 3 (2 phases)

| Phase | Title | Risk | Milestone | Wave | Files | Done when | Verified |
|---|---|---|---|---|---|---|---|
| 2 | Store | yes | M1 | 1 | store.go, store_test.go, run.go +1 | ` + "`go test ./store/...`" + ` is green. | build: no store \| yet |
| 3 | Loop \| core | — | M1 | 2 | loop.go | ` + "`go test ./...`" + ` | build |
| 4 | Face | — | M2 | 1 | — | — | blocked: needs a key |
| 5 | TUI | — | M2 | 2 | — | — | depends on phase 4 (blocked) |

Plan check: 1 note
- Phase 4 — Face: no 'Implements' line
Resolve first: Pick a DB → phases 7
2 phases: 6 step sessions (plan, implement, land), up to 6 review rounds
Milestones completed by this run: M1 Core
`
	if table != wantPlan {
		t.Errorf("plan table =\n%s\nwant\n%s", table, wantPlan)
	}
	if summary != "2 phases to run, 0 already done, 1 blocked, 1 dropped" {
		t.Errorf("plan summary = %q", summary)
	}

	_, dry := RenderTriage(TriageView{Plan: plan, List: plan.Phases[1:2]}, nil)
	wantDry := `Plan: docs/x/todo.md — 5 phases, 1 already done, running 2 (1 phase)

| Phase | Title | Risk | Milestone | Wave | Files | Done when |
|---|---|---|---|---|---|---|
| 2 | Store | yes | M1 | 1 | store.go, store_test.go, run.go +1 | ` + "`go test ./store/...`" + ` is green. |

Plan check: no notes
Resolve first: none outstanding
1 phase: 0 step sessions (), up to 0 review rounds
Milestones completed by this run: none
verification: not run (--dry-run starts no sessions)
`
	if dry != wantDry {
		t.Errorf("dry-run table =\n%s\nwant\n%s", dry, wantDry)
	}

	backlog := triageBacklog()
	btr, err := ValidateTriage(backlog, backlog.Phases, backlogTriage(), citeAGo)
	if err != nil {
		t.Fatal(err)
	}
	btr.Items[1].Category = "feature"

	summary, table = RenderTriage(triageView(backlog, backlog.Phases), &btr)

	wantBacklog := `Backlog: issues.md (4 items)

| Group | Phase | Items | Subsystem | Kind | Risk | Conf |
|---|---|---|---|---|---|---|
| G1 | 1 | #1 #2 | store | bug/feature | deep | medium |
| G2 | 4 | #4 | face | bug | cosmetic | high |

Skipped (verification):
- #3 stale — fixed at a.go:12
2 groups: 2 phases, up to 6 review rounds
`
	if table != wantBacklog {
		t.Errorf("backlog table =\n%s\nwant\n%s", table, wantBacklog)
	}
	if summary != "2 groups from 4 items, 1 skipped" {
		t.Errorf("backlog summary = %q", summary)
	}

	summary, dry = RenderTriage(triageView(backlog, backlog.Phases[:2]), nil)
	wantBacklogDry := `Backlog: issues.md (2 items)

| Item | Title |
|---|---|
| 1 | one |
| 2 | two |

2 items: up to 2 phases, up to 6 review rounds
verification: not run (--dry-run starts no sessions)
`
	if dry != wantBacklogDry || summary != "2 items, verification not run" {
		t.Errorf("backlog dry-run = %q\n%s\nwant\n%s", summary, dry, wantBacklogDry)
	}
}

func TestRenderTriageStripsControlCharactersFromCells(t *testing.T) {
	backlog := Plan{Path: "issues.md", Backlog: true, Phases: []Phase{backlogItem("1", "a | b\x1b[2J\x1b[H\x1b]0;pwned\x07\tc\r\nd\u0085\x7f")}}

	_, table := RenderTriage(triageView(backlog, backlog.Phases), nil)

	if want := "| 1 | a \\| b[2J[H]0;pwned c  d |\n"; !strings.Contains(table, want) {
		t.Errorf("want %q in:\n%s", want, table)
	}
}

func TestTriageTextAsksOnlyWhenAttended(t *testing.T) {
	plan, backlog := triagePlan(), triageBacklog()

	attended := TriageText(plan, plan.Phases[2:], true)
	unattended := TriageText(backlog, backlog.Phases, false)

	wantAttended := "triage plan docs/x/todo.md phases 3, 4, 5. Follow your prompt's \"Triage\" section: verify each phase against the code and call submit_triage with one verdict per phase.\n" +
		"When submit_triage is accepted, show the table submit_triage returns, ask the maintainer, then call submit_gate.\n"
	if attended != wantAttended {
		t.Errorf("attended =\n%s", attended)
	}
	wantUnattended := "triage backlog issues.md items 1, 2, 3, 4. Follow your prompt's \"Triage\" section: verify each item against the code, group the fixes, and call submit_triage with one verdict per item and the groups.\n" +
		"Do not ask the maintainer; the run starts when submit_triage is accepted.\n"
	if unattended != wantUnattended {
		t.Errorf("unattended =\n%s", unattended)
	}
}

func TestCheckCitationRefusesMaintainerButTheRouterAcceptsIt(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := CheckCitation(root, "a.go:1"); got != "" {
		t.Errorf("a.go:1 = %q", got)
	}
	if got := CheckCitation(root, "maintainer"); got != `citation "maintainer" is not path:line or maintainer` {
		t.Errorf("maintainer = %q", got)
	}
	if got := CheckCitation(root, "b.go:1"); got != "citation b.go is not a file in the primary tree" {
		t.Errorf("b.go:1 = %q", got)
	}
	r := &QuestionRouter{Repo: &fakeRepo{RootDir: root}}
	if got := r.rejectCitation("maintainer"); got != "" {
		t.Errorf("router maintainer = %q", got)
	}
}
