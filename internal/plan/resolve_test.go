package plan

import (
	"os"
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

func TestBoxlessEntryStamped(t *testing.T) {
	before := `# Plan

## Resolve first
- **Legacy blocker** — still open?
      Owner: platform. Blocks: Phase 2.

` + phasesTail
	path := writePlan(t, before)

	if err := (Reader{}).Stamp(path, "Legacy blocker", "2026-09-18 — yes; it is."); err != nil {
		t.Fatalf("Stamp: %v", err)
	}

	after, _ := os.ReadFile(path)
	want := strings.Replace(before,
		"- **Legacy blocker** — still open?\n      Owner: platform. Blocks: Phase 2.\n",
		"- [x] **Legacy blocker** — still open?\n      Owner: platform. Blocks: Phase 2.\n      Resolved: 2026-09-18 — yes; it is.\n", 1)
	if string(after) != want {
		t.Errorf("after stamp:\n%s\nwant:\n%s", after, want)
	}
	p, err := Reader{}.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	e := onlyEntry(t, p)
	if !e.Ticked || !e.HasBox || e.Resolved != "2026-09-18 — yes; it is" {
		t.Errorf("stamped entry = %+v", e)
	}
	if got := p.Blocking([]int{1, 2, 3}); len(got) != 0 {
		t.Errorf("Blocking after stamp = %+v, want none", got)
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
