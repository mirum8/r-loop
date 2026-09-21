package plan

import (
	"reflect"
	"strings"
	"testing"

	"r-loop/internal/core"
)

const phasesTail = `## Milestone 1 — M

### Phase 1 — One
**Depends on:** —
- [ ] a

### Phase 2 — Two
**Depends on:** Phase 1
- [ ] b

### Phase 3 — Three
**Depends on:** Phase 2
- [ ] c
`

func readEntries(t *testing.T, content string) core.Plan {
	t.Helper()
	p, err := Reader{}.Read(writePlan(t, content))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return p
}

func onlyEntry(t *testing.T, p core.Plan) core.Entry {
	t.Helper()
	if len(p.ResolveFirst) != 1 {
		t.Fatalf("entries = %d, want 1: %+v", len(p.ResolveFirst), p.ResolveFirst)
	}
	return p.ResolveFirst[0]
}

func TestNoResolveFirstSection(t *testing.T) {
	p := readEntries(t, "# Plan\n\n"+phasesTail)

	if p.ResolveFirst != nil {
		t.Errorf("ResolveFirst = %+v, want nil", p.ResolveFirst)
	}
	if got := p.Blocking([]int{1, 2, 3}); len(got) != 0 {
		t.Errorf("Blocking = %+v, want none", got)
	}
}

func TestEmptyResolveFirstSection(t *testing.T) {
	p := readEntries(t, "# Plan\n\n## Resolve first\n\n"+phasesTail)

	if len(p.ResolveFirst) != 0 {
		t.Errorf("ResolveFirst = %+v, want empty", p.ResolveFirst)
	}
	if got := p.Blocking([]int{1, 2, 3}); len(got) != 0 {
		t.Errorf("Blocking = %+v, want none", got)
	}
}

func TestEntryFieldsSlicedByLabel(t *testing.T) {
	p := readEntries(t, `# Plan

## Resolve first
- [ ] **Debezium against RDS** — can it read our instance?
      Owner: platform. Blocks: Phase 2. Timebox: one afternoon. Output: a line in the spec's Risks.
* [x] **Queue vs cron** — which drives retries?
      Owner: platform. Blocks: Phase 3. Timebox: an hour. Output: a line in `+"`tech-design.md`"+`.
      Resolved: 2026-06-04 — a queue; cron cannot honour the 30s target. Alternative: cron.
### Phase 1 — One
- [ ] a
### Phase 2 — Two
- [ ] b
### Phase 3 — Three
- [ ] c
`)

	if len(p.ResolveFirst) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(p.ResolveFirst), p.ResolveFirst)
	}
	want := core.Entry{
		Name: "Debezium against RDS",
		Kind: core.EntryDecision,
		Body: "- [ ] **Debezium against RDS** — can it read our instance?\n" +
			"      Owner: platform. Blocks: Phase 2. Timebox: one afternoon. Output: a line in the spec's Risks.",
		HasBox:       true,
		Owner:        "platform",
		Blocks:       "Phase 2",
		Timebox:      "one afternoon",
		Output:       "a line in the spec's Risks",
		BlocksPhases: []int{2},
	}
	if got := p.ResolveFirst[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("entry 0:\n got %+v\nwant %+v", got, want)
	}
	second := p.ResolveFirst[1]
	if second.Name != "Queue vs cron" || !second.HasBox || !second.Ticked {
		t.Errorf("entry 1 = %+v", second)
	}
	if second.Resolved != "2026-06-04 — a queue; cron cannot honour the 30s target" {
		t.Errorf("Resolved = %q", second.Resolved)
	}
	if second.Output != "a line in `tech-design.md`" {
		t.Errorf("Output = %q", second.Output)
	}
	if second.Malformed != nil {
		t.Errorf("Malformed = %v, want none", second.Malformed)
	}
	if len(p.Phases) != 3 {
		t.Errorf("phases = %d, want 3: the section stops at the ### heading", len(p.Phases))
	}
}

func TestBoxlessEntryIsOutstanding(t *testing.T) {
	p := readEntries(t, `# Plan

## Resolve first
- **Legacy blocker** — still open?
      Owner: platform. Blocks: Phase 2.

`+phasesTail)

	e := onlyEntry(t, p)
	if e.HasBox || e.Ticked {
		t.Errorf("HasBox = %v, Ticked = %v, want both false", e.HasBox, e.Ticked)
	}
	if got := p.Blocking([]int{2}); len(got) != 1 || got[0].Name != "Legacy blocker" {
		t.Errorf("Blocking([2]) = %+v, want the legacy entry", got)
	}
	if got := p.Blocking([]int{1, 3}); len(got) != 0 {
		t.Errorf("Blocking([1 3]) = %+v, want none", got)
	}
}

func TestBlocksNamingTwoPhases(t *testing.T) {
	p := readEntries(t, `# Plan

## Resolve first
- [ ] **Two phases** — which?
      Owner: platform. Blocks: Phase 1, Phase 3. Timebox: an hour.

`+phasesTail)

	e := onlyEntry(t, p)
	if !reflect.DeepEqual(e.BlocksPhases, []int{1, 3}) || e.BlocksAll {
		t.Errorf("BlocksPhases = %v, BlocksAll = %v", e.BlocksPhases, e.BlocksAll)
	}
	if got := p.Blocking([]int{2}); len(got) != 0 {
		t.Errorf("Blocking([2]) = %+v, want none", got)
	}
	if got := p.Blocking([]int{2, 3}); len(got) != 1 {
		t.Errorf("Blocking([2 3]) = %+v, want the entry", got)
	}
}

func TestBlocksWithProseOnlyBlocksAll(t *testing.T) {
	p := readEntries(t, `# Plan

## Resolve first
- [ ] **Vague** — something?
      Owner: platform. Blocks: the whole ingest path. Timebox: an hour.

`+phasesTail)

	e := onlyEntry(t, p)
	if !e.BlocksAll || e.BlocksPhases != nil {
		t.Errorf("BlocksAll = %v, BlocksPhases = %v", e.BlocksAll, e.BlocksPhases)
	}
	if got := p.Blocking([]int{3}); len(got) != 1 {
		t.Errorf("Blocking([3]) = %+v, want the entry", got)
	}
}

func TestMissingBlocksBlocksAll(t *testing.T) {
	p := readEntries(t, `# Plan

## Resolve first
- [ ] **No edge** — something?
      Owner: platform. Timebox: an hour.

`+phasesTail)

	if e := onlyEntry(t, p); !e.BlocksAll {
		t.Errorf("BlocksAll = false, want true")
	}
	if got := p.Blocking([]int{1}); len(got) != 1 {
		t.Errorf("Blocking([1]) = %+v, want the entry", got)
	}
}

func TestTickedWithoutResolved(t *testing.T) {
	p := readEntries(t, `# Plan

## Resolve first
- [x] **Closed silently** — was it?
      Owner: platform. Blocks: Phase 1.

`+phasesTail)

	e := onlyEntry(t, p)
	if !e.Ticked {
		t.Errorf("Ticked = false")
	}
	if !reflect.DeepEqual(e.Malformed, []string{"ticked without Resolved"}) {
		t.Errorf("Malformed = %v", e.Malformed)
	}
	if got := p.Blocking([]int{1, 2, 3}); len(got) != 0 {
		t.Errorf("Blocking = %+v, want none: a tick counts as resolved", got)
	}
}

func TestInformsLabelIsMalformedNotBlocks(t *testing.T) {
	p := readEntries(t, `# Plan

## Resolve first
- [ ] **Informs** — does it?
      Owner: platform. Blocks: Phase 1. Informs: Phases 2 and 3. Timebox: an hour.

`+phasesTail)

	e := onlyEntry(t, p)
	if e.Blocks != "Phase 1" || !reflect.DeepEqual(e.BlocksPhases, []int{1}) {
		t.Errorf("Blocks = %q, BlocksPhases = %v", e.Blocks, e.BlocksPhases)
	}
	if e.Timebox != "an hour" {
		t.Errorf("Timebox = %q", e.Timebox)
	}
	if !reflect.DeepEqual(e.Malformed, []string{"unknown label Informs:"}) {
		t.Errorf("Malformed = %v", e.Malformed)
	}
	if got := p.Blocking([]int{2, 3}); len(got) != 0 {
		t.Errorf("Blocking([2 3]) = %+v, want none", got)
	}
}

func TestFixtureHasNoResolveFirst(t *testing.T) {
	if p := readFixture(t); len(p.ResolveFirst) != 0 {
		t.Errorf("ResolveFirst = %+v", p.ResolveFirst)
	}
}

func TestUnknownLabelBeforeAnyKnownLabelIsMalformed(t *testing.T) {
	p := readEntries(t, `# Plan

## Resolve first
- [ ] **Informs** — does it?
      Informs: Phase 2. Blocks: Phase 1. Timebox: an hour.

`+phasesTail)

	e := onlyEntry(t, p)
	if e.Blocks != "Phase 1" || !reflect.DeepEqual(e.BlocksPhases, []int{1}) {
		t.Errorf("Blocks = %q, BlocksPhases = %v", e.Blocks, e.BlocksPhases)
	}
	if !reflect.DeepEqual(e.Malformed, []string{"unknown label Informs:"}) {
		t.Errorf("Malformed = %v", e.Malformed)
	}
}

func TestOnlyAnUnknownLabelIsMalformed(t *testing.T) {
	p := readEntries(t, `# Plan

## Resolve first
- [ ] **Informs** — does it?
      Informs: Phase 2.

`+phasesTail)

	e := onlyEntry(t, p)
	if !reflect.DeepEqual(e.Malformed, []string{"unknown label Informs:"}) {
		t.Errorf("Malformed = %v", e.Malformed)
	}
	if !e.BlocksAll {
		t.Errorf("BlocksAll = false, want true")
	}
}

func TestColonInTheQuestionProseIsNotALabel(t *testing.T) {
	p := readEntries(t, `# Plan

## Resolve first
- [ ] **Postgres versus Aurora** — which engine, Postgres: or Aurora?
      Owner: platform. Blocks: Phase 1.

`+phasesTail)

	e := onlyEntry(t, p)
	if e.Malformed != nil {
		t.Errorf("Malformed = %v, want none", e.Malformed)
	}
	if e.Owner != "platform" || e.Blocks != "Phase 1" {
		t.Errorf("Owner = %q, Blocks = %q", e.Owner, e.Blocks)
	}
}

func TestAPhaseBlockCarriesTheResolvedEntriesThatBlockedIt(t *testing.T) {
	p := readEntries(t, `# Plan

## Resolve first
- [x] **Test style** — table-driven or not?
      Owner: me. Blocks: Phase 2.
      Resolved: 2026-09-19 — table-driven
- [x] **Licence** — which?
      Owner: me. Blocks: all.
      Resolved: 2026-09-18 — MIT
- [ ] **Naming** — open still
      Owner: me. Blocks: Phase 2.
- [x] **Logging** — which lib?
      Owner: me. Blocks: Phase 3.
      Resolved: 2026-09-18 — slog

`+phasesTail)

	want := "### Phase 2 — Two\n**Depends on:** Phase 1\n- [ ] b\n\n" +
		"Resolved first:\n\n" +
		"- [x] **Test style** — table-driven or not?\n      Owner: me. Blocks: Phase 2.\n      Resolved: 2026-09-19 — table-driven\n" +
		"- [x] **Licence** — which?\n      Owner: me. Blocks: all.\n      Resolved: 2026-09-18 — MIT\n"
	if got := p.Phases[1].Block; got != want {
		t.Errorf("Block:\n%q\nwant\n%q", got, want)
	}
	if got := p.Phases[0].Block; !strings.HasSuffix(got, "Resolved first:\n\n- [x] **Licence** — which?\n      Owner: me. Blocks: all.\n      Resolved: 2026-09-18 — MIT\n") || strings.Contains(got, "Test style") {
		t.Errorf("phase 1 Block:\n%s", got)
	}
}

func TestAPhaseBlockWithNoResolvedEntryIsUnchanged(t *testing.T) {
	p := readEntries(t, "# Plan\n\n## Resolve first\n- [ ] **Naming** — open\n      Owner: me. Blocks: Phase 2.\n\n"+phasesTail)

	if got := p.Phases[1].Block; got != "### Phase 2 — Two\n**Depends on:** Phase 1\n- [ ] b\n\n" {
		t.Errorf("Block = %q", got)
	}
}

func TestEntriesAreSortedAsDecisionPersonOrUnclassified(t *testing.T) {
	path := writePlan(t, "# P\n\n## Resolve first\n"+
		"- **Debezium against RDS** — can it read our instance, or do we need a polling fallback?\n  Owner: platform. Blocks: Phase 1.\n"+
		"- [ ] **Sign the payments DPA** — the processor needs it before live traffic.\n  Owner: legal. Blocks: Phase 1.\n"+
		"- [ ] **Decide whether to sign the DPA** — legal wants an answer.\n  Owner: platform. Blocks: Phase 1.\n"+
		"- [ ] **The thing about the stuff** — no pattern matches this.\n  Owner: platform. Blocks: Phase 1.\n"+
		"- [ ] **Pick a queue** — Kafka or SQS?\n  Owner: finance. Blocks: Phase 1.\n"+
		"- [x] **Queue vs cron** — which drives retries?\n  Owner: platform. Blocks: Phase 1.\n  Resolved: 2026-09-03 — queue; cron cannot honour the 30s target. Alternative: cron. Outstanding: the spec's Risks line.\n"+
		"\n### Phase 1 — A\n- [ ] a thing\n")

	p, err := Reader{}.Read(path)
	if err != nil {
		t.Fatal(err)
	}

	var kinds []string
	for _, e := range p.ResolveFirst {
		kinds = append(kinds, e.Kind)
	}
	want := []string{"decision", "person", "person", "unclassified", "person", "decision"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	last := p.ResolveFirst[5]
	if last.Alternative != "cron" || last.Outstanding != "the spec's Risks line" {
		t.Errorf("alternative %q outstanding %q", last.Alternative, last.Outstanding)
	}
}
