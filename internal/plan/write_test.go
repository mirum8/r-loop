package plan

import (
	"errors"
	"os"
	"strings"
	"testing"
)

const tickPlan = `# Plan

## Resolve first
- [ ] **Open** — still?
      Owner: platform. Blocks: Phase 3.

### Phase 1 — One
**Depends on:** —
- [x] done already
- [ ] first open
  - [ ] nested stays
- [ ] second open
**Done when:** ` + "`go test`" + ` is green.

### Phase 2 — Two
**Depends on:** Phase 1
- [ ] untouched
#### Notes
- [ ] also untouched in phase 2 notes

### Phase 3 — Three
**Depends on:** Phase 2
- [x] all done
`

func fileLines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.SplitAfter(string(b), "\n")
}

func TestTickRewritesOnlyThatPhase(t *testing.T) {
	path := writePlan(t, tickPlan)
	before := fileLines(t, path)

	if err := (Reader{}).Tick(path, 1); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	after := fileLines(t, path)
	if len(after) != len(before) {
		t.Fatalf("line count %d -> %d", len(before), len(after))
	}
	changed := map[string]string{
		"- [ ] first open\n":  "- [x] first open\n",
		"- [ ] second open\n": "- [x] second open\n",
	}
	for i := range before {
		if want, ok := changed[before[i]]; ok {
			if after[i] != want {
				t.Errorf("line %d = %q, want %q", i+1, after[i], want)
			}
			continue
		}
		if after[i] != before[i] {
			t.Errorf("line %d changed: %q -> %q", i+1, before[i], after[i])
		}
	}
	p, err := Reader{}.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Unticked(); len(got) != 1 || got[0] != 2 {
		t.Errorf("Unticked = %v, want [2]", got)
	}
}

func TestTickNothingOpen(t *testing.T) {
	path := writePlan(t, tickPlan)

	err := Reader{}.Tick(path, 3)

	if !errors.Is(err, ErrNothingToTick) {
		t.Errorf("err = %v, want ErrNothingToTick", err)
	}
	if b, _ := os.ReadFile(path); string(b) != tickPlan {
		t.Errorf("file changed")
	}
}

func TestTickUnknownPhase(t *testing.T) {
	path := writePlan(t, tickPlan)

	if err := (Reader{}).Tick(path, 9); err == nil {
		t.Errorf("Tick(9) = nil, want an error")
	}
}

func TestTickZeroPaddedPhaseHeading(t *testing.T) {
	path := writePlan(t, "### Phase 01 — One\n- [ ] a\n")

	if err := (Reader{}).Tick(path, 1); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if b, _ := os.ReadFile(path); string(b) != "### Phase 01 — One\n- [x] a\n" {
		t.Errorf("got %q", b)
	}
}
