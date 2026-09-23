package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

const (
	VerdictBuild       = "build"
	VerdictAlreadyDone = "already-done"
	VerdictBlocked     = "blocked"

	VerdictFix  = "fix"
	VerdictSkip = "skip"

	RiskCosmetic = "cosmetic"
	RiskLocal    = "local"
	RiskDeep     = "deep"

	GateGo     = "go"
	GateRevise = "revise"
	GateAbort  = "abort"

	TriageSkipped = "triage-skipped"
)

var (
	phaseStatuses = []string{VerdictBuild, VerdictAlreadyDone, VerdictBlocked}
	itemVerdicts  = []string{VerdictFix, VerdictSkip}
	categories    = []string{"bug", "feature", "chore", "question", "docs", "duplicate", "stale", "not-enough-info"}
	confidences   = []string{"low", "medium", "high"}
	risks         = []string{RiskCosmetic, RiskLocal, RiskDeep}
	citedLineRe   = regexp.MustCompile(`[A-Za-z0-9_./-]+:\d+`)
)

type PhaseVerdict struct {
	Phase  string `json:"phase"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

type ItemVerdict struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Verdict    string   `json:"verdict"`
	Category   string   `json:"category"`
	Confidence string   `json:"confidence"`
	RootCause  string   `json:"root_cause_or_scope"`
	Touches    []string `json:"touches,omitempty"`
	Risk       string   `json:"risk,omitempty"`
	SkipReason string   `json:"skip_reason,omitempty"`
}

type Triage struct {
	Phases  []PhaseVerdict `json:"phases,omitempty"`
	Items   []ItemVerdict  `json:"items,omitempty"`
	Groups  []Group        `json:"groups,omitempty"`
	Dropped []string       `json:"dropped,omitempty"`
}

type Split struct {
	Group string     `json:"group"`
	Into  [][]string `json:"into"`
}

type GateDecision struct {
	Decision       string     `json:"decision"`
	Drop           []string   `json:"drop,omitempty"`
	Split          []Split    `json:"split,omitempty"`
	Merge          [][]string `json:"merge,omitempty"`
	MaintainerSaid string     `json:"maintainer_said,omitempty"`
}

type Group struct {
	ID         string   `json:"group_id"`
	Items      []string `json:"items"`
	Subsystem  string   `json:"subsystem"`
	Risk       string   `json:"risk,omitempty"`
	Rationale  string   `json:"rationale,omitempty"`
	Confidence string   `json:"confidence,omitempty"`
}

func RunListGroups(fields map[string]string) []Group {
	var groups []Group
	if err := json.Unmarshal([]byte(fields["groups"]), &groups); err != nil {
		return nil
	}
	return groups
}

func GroupBacklog(p Plan, groups []Group) Plan {
	byID := map[string]Phase{}
	for _, ph := range p.Phases {
		byID[ph.ID] = ph
	}
	merged := map[string]Phase{}
	folded := map[string]bool{}
	for _, g := range groups {
		var ids []string
		for _, id := range g.Items {
			if _, ok := byID[id]; ok && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		if len(ids) < 2 {
			continue
		}
		slices.SortFunc(ids, ComparePhaseIDs)
		head := byID[ids[0]]
		head.Members = ids
		head.Title = strings.TrimSpace(fmt.Sprintf("%s (items %s)", g.Subsystem, strings.Join(ids, ", ")))
		head.Items = nil
		var blocks []string
		for _, id := range ids {
			member := byID[id]
			for _, it := range member.Items {
				head.Items = append(head.Items, Item{Text: "#" + id + " " + it.Text, Done: it.Done})
			}
			blocks = append(blocks, strings.TrimRight(member.Block, "\n"))
			if id != head.ID {
				folded[id] = true
			}
		}
		head.Block = strings.Join(blocks, "\n\n") + "\n"
		merged[head.ID] = head
	}
	out := p
	out.Phases = nil
	for _, ph := range p.Phases {
		if folded[ph.ID] {
			continue
		}
		if g, ok := merged[ph.ID]; ok {
			ph = g
		}
		out.Phases = append(out.Phases, ph)
	}
	return out
}

func TriageText(plan Plan, list []Phase, ask bool) string {
	var b strings.Builder
	if plan.Backlog {
		fmt.Fprintf(&b, "triage backlog %s items %s. Follow your prompt's \"Triage\" section: verify each item against the code, group the fixes, and call submit_triage with one verdict per item and the groups.\n", plan.Path, joinIDs(phaseIDs(list)))
	} else {
		fmt.Fprintf(&b, "triage plan %s phases %s. Follow your prompt's \"Triage\" section: verify each phase against the code and call submit_triage with one verdict per phase.\n", plan.Path, joinIDs(phaseIDs(list)))
	}
	if ask {
		b.WriteString("When submit_triage is accepted, show the table submit_triage returns, ask the maintainer, then call submit_gate.\n")
	} else {
		b.WriteString("Do not ask the maintainer; the run starts when submit_triage is accepted.\n")
	}
	return b.String()
}

func ValidateTriage(plan Plan, list []Phase, t Triage, cite func(string) string) (Triage, error) {
	if plan.Backlog {
		return validateBacklog(list, t, cite)
	}
	return validatePlan(list, t, cite)
}

func validatePlan(list []Phase, t Triage, cite func(string) string) (Triage, error) {
	if len(t.Items) > 0 || len(t.Groups) > 0 {
		return Triage{}, errors.New("a plan triage carries phases only, not items or groups")
	}
	ids := phaseIDs(list)
	seen := map[string]bool{}
	for _, v := range t.Phases {
		switch {
		case !slices.Contains(ids, v.Phase):
			return Triage{}, fmt.Errorf("phase %s is not in the run list %s", v.Phase, joinIDs(ids))
		case seen[v.Phase]:
			return Triage{}, fmt.Errorf("phase %s has more than one verdict", v.Phase)
		case hasControl(v.Note):
			return Triage{}, fmt.Errorf("phase %s: note contains a control character", v.Phase)
		case !slices.Contains(phaseStatuses, v.Status):
			return Triage{}, fmt.Errorf("phase %s: status %q is not one of %s", v.Phase, v.Status, strings.Join(phaseStatuses, ", "))
		case v.Status != VerdictBuild && strings.TrimSpace(v.Note) == "":
			return Triage{}, fmt.Errorf("phase %s: %s needs a note saying why", v.Phase, v.Status)
		}
		if v.Status == VerdictAlreadyDone {
			if reason := cited(v.Note, cite); reason != "" {
				return Triage{}, fmt.Errorf("phase %s: already-done needs a path:line in the note that exists: %s", v.Phase, reason)
			}
		}
		seen[v.Phase] = true
	}
	for _, id := range ids {
		if !seen[id] {
			return Triage{}, fmt.Errorf("phase %s has no verdict", id)
		}
	}
	return t, nil
}

func validateBacklog(list []Phase, t Triage, cite func(string) string) (Triage, error) {
	if len(t.Phases) > 0 {
		return Triage{}, errors.New("a backlog triage carries items and groups, not phases")
	}
	ids := phaseIDs(list)
	byID := map[string]ItemVerdict{}
	for _, v := range t.Items {
		if err := checkItem(v, ids, byID, cite); err != nil {
			return Triage{}, err
		}
		byID[v.ID] = v
	}
	for _, id := range ids {
		if _, ok := byID[id]; !ok {
			return Triage{}, fmt.Errorf("item %s has no verdict", id)
		}
	}
	groupOf := map[string]string{}
	groupIDs := map[string]bool{}
	out := t
	out.Groups = make([]Group, len(t.Groups))
	for i, g := range t.Groups {
		if strings.TrimSpace(g.ID) == "" {
			return Triage{}, errors.New("a group has no group_id")
		}
		if groupIDs[g.ID] {
			return Triage{}, fmt.Errorf("group_id %s is used by more than one group", g.ID)
		}
		groupIDs[g.ID] = true
		if field := controlField([2]string{"subsystem", g.Subsystem}, [2]string{"rationale", g.Rationale}); field != "" {
			return Triage{}, fmt.Errorf("group %s: %s contains a control character", g.ID, field)
		}
		if len(g.Items) == 0 {
			return Triage{}, fmt.Errorf("group %s has no items", g.ID)
		}
		if len(g.Items) > 1 && strings.TrimSpace(g.Rationale) == "" {
			return Triage{}, fmt.Errorf("group %s holds %d items but gives no rationale", g.ID, len(g.Items))
		}
		g.Items = slices.Clone(g.Items)
		g.Risk, g.Confidence = "", ""
		for _, id := range g.Items {
			v, ok := byID[id]
			switch {
			case !ok:
				return Triage{}, fmt.Errorf("group %s: item %s is not in the run list", g.ID, id)
			case v.Verdict == VerdictSkip:
				return Triage{}, fmt.Errorf("group %s: item %s is a skip", g.ID, id)
			case groupOf[id] == g.ID:
				return Triage{}, fmt.Errorf("group %s lists item %s twice", g.ID, id)
			case groupOf[id] != "":
				return Triage{}, fmt.Errorf("item %s is in groups %s and %s", id, groupOf[id], g.ID)
			}
			groupOf[id] = g.ID
			if g.Risk == "" || slices.Index(risks, v.Risk) > slices.Index(risks, g.Risk) {
				g.Risk = v.Risk
			}
			if g.Confidence == "" || slices.Index(confidences, v.Confidence) < slices.Index(confidences, g.Confidence) {
				g.Confidence = v.Confidence
			}
		}
		if mixesCosmeticAndDeep(g, byID) {
			return Triage{}, fmt.Errorf("group %s mixes cosmetic and deep items", g.ID)
		}
		out.Groups[i] = g
	}
	for _, v := range t.Items {
		if v.Verdict == VerdictFix && groupOf[v.ID] == "" && !slices.Contains(t.Dropped, v.ID) {
			return Triage{}, fmt.Errorf("item %s is a fix but in no group", v.ID)
		}
	}
	return out, nil
}

func checkItem(v ItemVerdict, ids []string, seen map[string]ItemVerdict, cite func(string) string) error {
	if !slices.Contains(ids, v.ID) {
		return fmt.Errorf("item %s is not in the run list %s", v.ID, joinIDs(ids))
	}
	if _, dup := seen[v.ID]; dup {
		return fmt.Errorf("item %s has more than one verdict", v.ID)
	}
	if field := controlField([2]string{"root_cause_or_scope", v.RootCause}, [2]string{"touches", strings.Join(v.Touches, " ")}, [2]string{"skip_reason", v.SkipReason}); field != "" {
		return fmt.Errorf("item %s: %s contains a control character", v.ID, field)
	}
	for _, f := range []struct {
		name, value string
		allowed     []string
	}{
		{"verdict", v.Verdict, itemVerdicts},
		{"category", v.Category, categories},
		{"confidence", v.Confidence, confidences},
	} {
		if !slices.Contains(f.allowed, f.value) {
			return fmt.Errorf("item %s: %s %q is not one of %s", v.ID, f.name, f.value, strings.Join(f.allowed, ", "))
		}
	}
	if !slices.Contains(risks, v.Risk) && (v.Verdict == VerdictFix || v.Risk != "") {
		return fmt.Errorf("item %s: risk %q is not one of %s", v.ID, v.Risk, strings.Join(risks, ", "))
	}
	if v.Verdict == VerdictFix {
		if len(v.Touches) == 0 {
			return fmt.Errorf("item %s: a fix needs touches", v.ID)
		}
		return nil
	}
	if strings.TrimSpace(v.SkipReason) == "" {
		return fmt.Errorf("item %s: a skip needs a skip_reason", v.ID)
	}
	if v.Category == "stale" || v.Category == "duplicate" {
		if reason := cited(v.SkipReason, cite); reason != "" {
			return fmt.Errorf("item %s: a %s skip needs a path:line in skip_reason that exists: %s", v.ID, v.Category, reason)
		}
	}
	return nil
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, unicode.IsControl)
}

func controlField(fields ...[2]string) string {
	for _, f := range fields {
		if hasControl(f[1]) {
			return f[0]
		}
	}
	return ""
}

func cited(text string, cite func(string) string) string {
	refs := citedLineRe.FindAllString(text, -1)
	if len(refs) == 0 {
		return "none cited"
	}
	var reasons []string
	for _, ref := range refs {
		reason := cite(ref)
		if reason == "" {
			return ""
		}
		reasons = append(reasons, reason)
	}
	return strings.Join(reasons, "; ")
}

func mixesCosmeticAndDeep(g Group, byID map[string]ItemVerdict) bool {
	var cosmetic, deep bool
	for _, id := range g.Items {
		cosmetic = cosmetic || byID[id].Risk == RiskCosmetic
		deep = deep || byID[id].Risk == RiskDeep
	}
	return cosmetic && deep
}

func ApplyGate(plan Plan, list []Phase, t Triage, g GateDecision) (Triage, error) {
	if !slices.Contains([]string{GateGo, GateRevise, GateAbort}, g.Decision) {
		return Triage{}, fmt.Errorf("decision %q is not one of go, revise, abort", g.Decision)
	}
	if strings.TrimSpace(g.MaintainerSaid) == "" {
		return Triage{}, errors.New("maintainer_said is empty: quote the maintainer's reply")
	}
	out := Triage{Phases: slices.Clone(t.Phases), Items: slices.Clone(t.Items), Dropped: slices.Clone(t.Dropped)}
	for _, gr := range t.Groups {
		gr.Items = slices.Clone(gr.Items)
		out.Groups = append(out.Groups, gr)
	}
	if !plan.Backlog {
		if len(g.Split) > 0 || len(g.Merge) > 0 {
			return Triage{}, errors.New("split and merge apply to a backlog's groups, not to a plan's phases")
		}
		ids := phaseIDs(list)
		for _, id := range g.Drop {
			if !slices.Contains(ids, id) {
				return Triage{}, fmt.Errorf("drop: phase %s is not in the run list %s", id, joinIDs(ids))
			}
			if !slices.Contains(out.Dropped, id) {
				out.Dropped = append(out.Dropped, id)
			}
		}
		return validatePlan(list, out, acceptAll)
	}
	for _, id := range g.Drop {
		if err := dropFromBacklog(&out, id); err != nil {
			return Triage{}, err
		}
	}
	for _, sp := range g.Split {
		if err := splitGroup(&out, sp); err != nil {
			return Triage{}, err
		}
	}
	for _, m := range g.Merge {
		if err := mergeGroups(&out, m); err != nil {
			return Triage{}, err
		}
	}
	return validateBacklog(list, out, acceptAll)
}

func acceptAll(string) string { return "" }

func dropFromBacklog(t *Triage, id string) error {
	var members []string
	if slices.ContainsFunc(t.Items, func(v ItemVerdict) bool { return v.ID == id }) {
		members = []string{id}
	} else if i := groupIndex(t.Groups, id); i >= 0 {
		members = t.Groups[i].Items
	} else {
		return fmt.Errorf("drop: %s is neither an item nor a group", id)
	}
	for _, m := range members {
		if !slices.Contains(t.Dropped, m) {
			t.Dropped = append(t.Dropped, m)
		}
	}
	var groups []Group
	for _, gr := range t.Groups {
		gr.Items = slices.DeleteFunc(gr.Items, func(m string) bool { return slices.Contains(members, m) })
		if len(gr.Items) > 0 {
			groups = append(groups, gr)
		}
	}
	t.Groups = groups
	return nil
}

func splitGroup(t *Triage, sp Split) error {
	i := groupIndex(t.Groups, sp.Group)
	if i < 0 {
		return fmt.Errorf("split: group %s does not exist", sp.Group)
	}
	g := t.Groups[i]
	if len(sp.Into) < 2 {
		return fmt.Errorf("split: group %s needs at least two parts", sp.Group)
	}
	var seen []string
	parts := make([]Group, len(sp.Into))
	for n, part := range sp.Into {
		if len(part) == 0 {
			return fmt.Errorf("split: part %d of group %s is empty", n+1, sp.Group)
		}
		for _, id := range part {
			if !slices.Contains(g.Items, id) {
				return fmt.Errorf("split: item %s is not in group %s", id, sp.Group)
			}
			if slices.Contains(seen, id) {
				return fmt.Errorf("split: item %s appears in more than one part of group %s", id, sp.Group)
			}
			seen = append(seen, id)
		}
		parts[n] = Group{ID: fmt.Sprintf("%s.%d", g.ID, n+1), Items: slices.Clone(part), Subsystem: g.Subsystem, Rationale: g.Rationale}
	}
	for _, id := range g.Items {
		if !slices.Contains(seen, id) {
			return fmt.Errorf("split: item %s of group %s is in no part", id, sp.Group)
		}
	}
	t.Groups = slices.Concat(t.Groups[:i], parts, t.Groups[i+1:])
	return nil
}

func mergeGroups(t *Triage, ids []string) error {
	if len(ids) < 2 {
		return fmt.Errorf("merge: %s names fewer than two groups", strings.Join(ids, ", "))
	}
	head := groupIndex(t.Groups, ids[0])
	if head < 0 {
		return fmt.Errorf("merge: group %s does not exist", ids[0])
	}
	merged := t.Groups[head]
	rationales := []string{merged.Rationale}
	var gone []string
	for _, id := range ids[1:] {
		i := groupIndex(t.Groups, id)
		if i < 0 {
			return fmt.Errorf("merge: group %s does not exist", id)
		}
		if id == merged.ID || slices.Contains(gone, id) {
			return fmt.Errorf("merge: group %s is named twice", id)
		}
		gone = append(gone, id)
		merged.Items = append(merged.Items, t.Groups[i].Items...)
		if r := t.Groups[i].Rationale; r != "" {
			rationales = append(rationales, r)
		}
	}
	merged.Rationale = strings.Join(slices.DeleteFunc(rationales, func(r string) bool { return r == "" }), "; ")
	t.Groups[head] = merged
	t.Groups = slices.DeleteFunc(t.Groups, func(g Group) bool { return slices.Contains(gone, g.ID) })
	return nil
}

func groupIndex(groups []Group, id string) int {
	return slices.IndexFunc(groups, func(g Group) bool { return g.ID == id })
}

func TriageResult(plan Plan, list []Phase, t Triage) ([]Phase, []Event, []Group) {
	if plan.Backlog {
		return backlogResult(plan, list, t)
	}
	status := map[string]PhaseVerdict{}
	for _, v := range t.Phases {
		status[v.Phase] = v
	}
	ids := phaseIDs(list)
	reason := map[string][2]string{}
	var roots []string
	for _, id := range ids {
		v := status[id]
		switch {
		case v.Status == VerdictAlreadyDone:
			reason[id] = [2]string{VerdictAlreadyDone, "already done: " + v.Note}
		case v.Status == VerdictBlocked:
			reason[id] = [2]string{VerdictBlocked, "blocked: " + v.Note}
			roots = append(roots, id)
		case slices.Contains(t.Dropped, id):
			reason[id] = [2]string{"dropped", "dropped by the maintainer"}
			roots = append(roots, id)
		}
	}
	for _, root := range roots {
		for _, d := range Dependents(plan, root) {
			if _, done := reason[d]; !done && slices.Contains(ids, d) {
				reason[d] = [2]string{"dependent", fmt.Sprintf("depends on phase %s (%s)", root, reason[root][0])}
			}
		}
	}
	var kept []Phase
	var skipped []Event
	for _, ph := range list {
		r, drop := reason[ph.ID]
		if !drop {
			kept = append(kept, ph)
			continue
		}
		skipped = append(skipped, triageSkipped(ph.ID, r[0], r[1]))
	}
	return kept, skipped, nil
}

func backlogResult(plan Plan, list []Phase, t Triage) ([]Phase, []Event, []Group) {
	verdicts := map[string]ItemVerdict{}
	for _, v := range t.Items {
		verdicts[v.ID] = v
	}
	dropped := map[string]bool{}
	var skipped []Event
	var keptItems []Phase
	for _, ph := range list {
		v := verdicts[ph.ID]
		switch {
		case v.Verdict == VerdictSkip:
			skipped = append(skipped, triageSkipped(ph.ID, v.Category, v.Category+": "+v.SkipReason))
		case slices.Contains(t.Dropped, ph.ID):
			skipped = append(skipped, triageSkipped(ph.ID, "dropped", "dropped by the maintainer"))
		default:
			keptItems = append(keptItems, ph)
			continue
		}
		dropped[ph.ID] = true
	}
	var groups []Group
	for _, g := range t.Groups {
		g.Items = slices.DeleteFunc(slices.Clone(g.Items), func(id string) bool { return dropped[id] })
		if len(g.Items) > 0 {
			groups = append(groups, g)
		}
	}
	scoped := plan
	scoped.Phases = keptItems
	return GroupBacklog(scoped, groups).Phases, skipped, groups
}

func triageSkipped(id, status, reason string) Event {
	return Event{Kind: TriageSkipped, Phase: id, Fields: map[string]string{"phase": id, "status": status, "reason": reason}}
}

type TriageView struct {
	Plan      Plan
	List      []Phase
	Checks    PlanFindings
	Deferrals []Deferral
	Kinds     []StepKind
}

const notVerified = "verification: not run (--dry-run starts no sessions)"

func RenderTriage(v TriageView, t *Triage) (string, string) {
	if v.Plan.Backlog {
		return renderBacklog(v, t)
	}
	return renderPlan(v, t)
}

func renderPlan(v TriageView, t *Triage) (string, string) {
	kept, skipped := v.List, []Event(nil)
	if t != nil {
		kept, skipped, _ = TriageResult(v.Plan, v.List, *t)
	}
	why := map[string]string{}
	for _, ev := range skipped {
		why[ev.Phase] = ev.Fields["reason"]
	}
	notes := map[string]string{}
	var done, blocked, dropped int
	if t != nil {
		for _, pv := range t.Phases {
			notes[pv.Phase] = pv.Note
			switch pv.Status {
			case VerdictAlreadyDone:
				done++
			case VerdictBlocked:
				blocked++
			}
		}
		dropped = len(skipped) - done - blocked
	}
	unticked := v.Plan.Unticked()
	var b strings.Builder
	fmt.Fprintf(&b, "Plan: %s — %s, %d already done, running %s (%s)\n\n", v.Plan.Path, Plural(len(v.Plan.Phases), "phase"),
		len(v.Plan.Phases)-len(unticked), orNone(joinIDs(phaseIDs(kept))), Plural(len(kept), "phase"))
	cols := []string{"Phase", "Title", "Risk"}
	withMilestones := len(v.Plan.Milestones) > 0
	if withMilestones {
		cols = append(cols, "Milestone")
	}
	cols = append(cols, "Wave", "Files", "Done when")
	if t != nil {
		cols = append(cols, "Verified")
	}
	wave, _ := Waves(v.Plan)
	var rows [][]string
	for _, ph := range v.List {
		risk := "—"
		if ph.Risk != "" {
			risk = "yes"
		}
		row := []string{ph.ID, ph.Title, risk}
		if withMilestones {
			row = append(row, milestoneCell(ph.Milestone))
		}
		row = append(row, fmt.Sprint(wave[ph.ID]), filesCell(ph.Files), clip(firstLine(ph.DoneWhen), 48))
		if t != nil {
			verified := why[ph.ID]
			if verified == "" {
				verified = VerdictBuild
				if n := notes[ph.ID]; n != "" {
					verified += ": " + n
				}
			}
			row = append(row, clip(verified, 60))
		}
		rows = append(rows, row)
	}
	writeTable(&b, cols, rows)
	b.WriteString("\n")
	if len(v.Checks.Notes) == 0 {
		b.WriteString("Plan check: no notes\n")
	} else {
		fmt.Fprintf(&b, "Plan check: %s\n", Plural(len(v.Checks.Notes), "note"))
		for _, n := range v.Checks.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}
	if len(v.Deferrals) == 0 {
		b.WriteString("Resolve first: none outstanding\n")
	}
	for _, d := range v.Deferrals {
		fmt.Fprintf(&b, "Resolve first: %s → phases %s\n", d.Entry, joinIDs(d.Phases))
	}
	fmt.Fprintf(&b, "%s: %s (%s), up to %s\n", Plural(len(kept), "phase"), Plural(len(kept)*len(v.Kinds), "step session"),
		strings.Join(kindNames(v.Kinds), ", "), Plural(len(kept)*reviewRounds(v.Kinds), "review round"))
	if withMilestones {
		fmt.Fprintf(&b, "Milestones completed by this run: %s\n", orNone(strings.Join(completedMilestones(v.Plan, unticked, kept), ", ")))
	}
	if t == nil {
		b.WriteString(notVerified + "\n")
		return fmt.Sprintf("%s to run, verification not run", Plural(len(kept), "phase")), b.String()
	}
	summary := fmt.Sprintf("%s to run, %d already done, %d blocked", Plural(len(kept), "phase"), done, blocked)
	if dropped > 0 {
		summary += fmt.Sprintf(", %d dropped", dropped)
	}
	return summary, b.String()
}

func renderBacklog(v TriageView, t *Triage) (string, string) {
	var b strings.Builder
	fmt.Fprintf(&b, "Backlog: %s (%s)\n\n", v.Plan.Path, Plural(len(v.List), "item"))
	rounds := reviewRounds(v.Kinds)
	if t == nil {
		var rows [][]string
		for _, ph := range v.List {
			rows = append(rows, []string{ph.ID, ph.Title})
		}
		writeTable(&b, []string{"Item", "Title"}, rows)
		fmt.Fprintf(&b, "\n%s: up to %s, up to %s\n%s\n", Plural(len(v.List), "item"), Plural(len(v.List), "phase"),
			Plural(len(v.List)*rounds, "review round"), notVerified)
		return fmt.Sprintf("%s, verification not run", Plural(len(v.List), "item")), b.String()
	}
	kept, skipped, groups := TriageResult(v.Plan, v.List, *t)
	verdicts := map[string]ItemVerdict{}
	for _, it := range t.Items {
		verdicts[it.ID] = it
	}
	var rows [][]string
	for _, g := range groups {
		ids := slices.Clone(g.Items)
		slices.SortFunc(ids, ComparePhaseIDs)
		var items, kinds []string
		for _, id := range ids {
			items = append(items, "#"+id)
			if c := verdicts[id].Category; !slices.Contains(kinds, c) {
				kinds = append(kinds, c)
			}
		}
		rows = append(rows, []string{g.ID, ids[0], strings.Join(items, " "), g.Subsystem, strings.Join(kinds, "/"), g.Risk, g.Confidence})
	}
	writeTable(&b, []string{"Group", "Phase", "Items", "Subsystem", "Kind", "Risk", "Conf"}, rows)
	b.WriteString("\n")
	if len(skipped) == 0 {
		b.WriteString("Skipped (verification): none\n")
	} else {
		b.WriteString("Skipped (verification):\n")
		for _, ev := range skipped {
			reason := strings.TrimPrefix(ev.Fields["reason"], ev.Fields["status"]+": ")
			fmt.Fprintf(&b, "- #%s %s — %s\n", ev.Phase, ev.Fields["status"], reason)
		}
	}
	fmt.Fprintf(&b, "%s: %s, up to %s\n", Plural(len(groups), "group"), Plural(len(kept), "phase"), Plural(len(kept)*rounds, "review round"))
	return fmt.Sprintf("%s from %s, %d skipped", Plural(len(groups), "group"), Plural(len(v.List), "item"), len(skipped)), b.String()
}

func writeTable(b *strings.Builder, cols []string, rows [][]string) {
	b.WriteString("| " + strings.Join(cols, " | ") + " |\n")
	b.WriteString("|" + strings.Repeat("---|", len(cols)) + "\n")
	for _, row := range rows {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = strings.ReplaceAll(Printable(c), "|", `\|`)
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
	}
}

func Printable(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)
}

func milestoneCell(n int) string {
	if n == 0 {
		return "—"
	}
	return fmt.Sprintf("M%d", n)
}

func filesCell(files []string) string {
	if len(files) <= 3 {
		return orDash(strings.Join(files, ", "))
	}
	return fmt.Sprintf("%s +%d", strings.Join(files[:3], ", "), len(files)-3)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return orDash(s)
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func Plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func kindNames(kinds []StepKind) []string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = k.Name
	}
	return names
}

func reviewRounds(kinds []StepKind) int {
	n := 0
	for _, k := range kinds {
		if len(k.Row.Reviewers) > 0 {
			n += k.Row.Rounds
		}
	}
	return n
}

func completedMilestones(p Plan, unticked []string, kept []Phase) []string {
	running := phaseIDs(kept)
	var out []string
	for _, m := range p.Milestones {
		complete, touched := len(m.Phases) > 0, false
		for _, id := range m.Phases {
			if slices.Contains(running, id) {
				touched = true
			} else if slices.Contains(unticked, id) {
				complete = false
			}
		}
		if complete && touched {
			out = append(out, strings.TrimSpace(fmt.Sprintf("M%d %s", m.Number, m.Name)))
		}
	}
	return out
}
