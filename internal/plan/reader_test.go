package plan

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"r-loop/internal/core"
)

var _ core.PlanSource = Reader{}

func readFixture(t *testing.T) core.Plan {
	t.Helper()
	p, err := Reader{}.Read("testdata/todo.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return p
}

func writePlan(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "docs", "my-topic")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "todo.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadFixturePhaseCountAndTopic(t *testing.T) {
	p := readFixture(t)

	if len(p.Phases) != 31 {
		t.Fatalf("phases = %d, want 31", len(p.Phases))
	}
	for i, ph := range p.Phases {
		if ph.Number != i+1 {
			t.Fatalf("phase %d has number %d", i, ph.Number)
		}
	}
	if p.Path != "testdata/todo.md" {
		t.Errorf("Path = %q", p.Path)
	}
	if p.Backlog {
		t.Error("a todo reads as a backlog")
	}
	if p.Topic != "testdata" {
		t.Errorf("Topic = %q, want testdata", p.Topic)
	}
}

func TestTopicIsParentDirectoryName(t *testing.T) {
	path := writePlan(t, "### Phase 1 — One\n- [ ] a\n")

	p, err := Reader{}.Read(path)

	if err != nil {
		t.Fatal(err)
	}
	if p.Topic != "my-topic" {
		t.Errorf("Topic = %q, want my-topic", p.Topic)
	}
}

func TestReadFixturePhaseOneEveryField(t *testing.T) {
	raw, err := os.ReadFile("testdata/todo.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "### Phase 1 — ")
	end := strings.Index(text, "### Phase 2 — ")

	ph := readFixture(t).Phases[0]

	if ph.Block != text[start:end] {
		t.Errorf("Block = %q\nwant %q", ph.Block, text[start:end])
	}
	if ph.Number != 1 {
		t.Errorf("Number = %d", ph.Number)
	}
	if ph.Title != "Module skeleton, core types, ports and the boundary test" {
		t.Errorf("Title = %q", ph.Title)
	}
	if !reflect.DeepEqual(ph.Implements, []string{"Run every remaining phase of a plan"}) {
		t.Errorf("Implements = %q", ph.Implements)
	}
	if len(ph.DependsOn) != 0 {
		t.Errorf("DependsOn = %v", ph.DependsOn)
	}
	wantFiles := []string{
		"go.mod",
		"cmd/r-loop/main.go",
		"internal/core/types.go",
		"internal/core/states.go",
		"internal/core/ports.go",
		"internal/core/fakes_test.go",
		"internal/core/boundary_test.go",
		"internal/core/states_test.go",
	}
	if !reflect.DeepEqual(ph.Files, wantFiles) {
		t.Errorf("Files = %q", ph.Files)
	}
	if ph.Risk != "" {
		t.Errorf("Risk = %q", ph.Risk)
	}
	if len(ph.Items) != 10 {
		t.Fatalf("Items = %d, want 10", len(ph.Items))
	}
	for _, it := range ph.Items {
		if !it.Done {
			t.Errorf("item not done: %q", it.Text)
		}
	}
	if !strings.HasPrefix(ph.Items[0].Text, "`go.mod` declares `module r-loop`") {
		t.Errorf("Items[0].Text = %q", ph.Items[0].Text)
	}
	if !strings.HasPrefix(ph.Items[9].Text, "`internal/core/states_test.go` proves every legal transition") {
		t.Errorf("Items[9].Text = %q", ph.Items[9].Text)
	}
	if ph.DoneWhen != "`go build ./... && go test ./internal/core/...` is green." {
		t.Errorf("DoneWhen = %q", ph.DoneWhen)
	}
	if ph.Milestone != 1 {
		t.Errorf("Milestone = %d", ph.Milestone)
	}
}

func TestReadFixtureOtherFields(t *testing.T) {
	p := readFixture(t)

	last := p.Phases[30]
	if last.Title != "Unattended mode" {
		t.Errorf("Title = %q", last.Title)
	}
	if last.Risk != "security" {
		t.Errorf("Risk = %q", last.Risk)
	}
	if !reflect.DeepEqual(last.DependsOn, []int{30}) {
		t.Errorf("DependsOn = %v", last.DependsOn)
	}
	if !reflect.DeepEqual(last.Implements, []string{"Leave a run to finish on its own", "Pre-authorise the blockers worth fixing automatically"}) {
		t.Errorf("Implements = %q", last.Implements)
	}
	if strings.Contains(last.Block, "Open questions") {
		t.Errorf("Block runs past the next ## heading")
	}
	if !reflect.DeepEqual(p.Phases[10].DependsOn, []int{6, 7, 8, 9, 10}) {
		t.Errorf("Phase 11 DependsOn = %v", p.Phases[10].DependsOn)
	}
	if p.Phases[1].Items[0].Done {
		t.Errorf("Phase 2 item 0 should be open")
	}
}

func TestReadFixtureMilestones(t *testing.T) {
	p := readFixture(t)

	want := []core.Milestone{
		{Number: 1, Name: "Core, plan file, config and state", Phases: []int{1, 2, 3, 4, 5}},
		{Number: 2, Name: "Sessions and providers", Phases: []int{6, 7, 8, 9, 10, 11}},
		{Number: 3, Name: "The serial loop, landing and the plain face", Phases: []int{12, 13, 14, 15, 16, 17}},
		{Number: 4, Name: "The review half", Phases: []int{18, 19}},
		{Number: 5, Name: "The ask channel", Phases: []int{20, 21}},
		{Number: 6, Name: "The TUI", Phases: []int{22, 23}},
		{Number: 7, Name: "The watchdog", Phases: []int{24, 25, 26, 27, 28, 29, 30, 31}},
	}
	if !reflect.DeepEqual(p.Milestones, want) {
		t.Errorf("Milestones = %+v", p.Milestones)
	}
	if p.Phases[19].Milestone != 5 {
		t.Errorf("Phase 20 Milestone = %d, want 5", p.Phases[19].Milestone)
	}
}

func TestReadFixtureUnticked(t *testing.T) {
	p := readFixture(t)

	got := p.Unticked()

	want := make([]int, 0, 30)
	for n := 2; n <= 31; n++ {
		want = append(want, n)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Unticked = %v", got)
	}
}

func TestPhaseWithoutMilestoneIsZero(t *testing.T) {
	path := writePlan(t, "# Plan\n\n### Phase 1 — Lone\n- [ ] a\n")

	p, err := Reader{}.Read(path)

	if err != nil {
		t.Fatal(err)
	}
	if p.Phases[0].Milestone != 0 {
		t.Errorf("Milestone = %d, want 0", p.Phases[0].Milestone)
	}
	if len(p.Milestones) != 0 {
		t.Errorf("Milestones = %+v", p.Milestones)
	}
}

func TestHeadingDashesAndSpacing(t *testing.T) {
	path := writePlan(t, "## Milestone 1 - First\n### Phase 1   -  Hyphen title\n- [ ] a\n### Phase 2 —\tEm title ✅\n- [X] b\n")

	p, err := Reader{}.Read(path)

	if err != nil {
		t.Fatal(err)
	}
	if p.Phases[0].Title != "Hyphen title" || p.Phases[1].Title != "Em title" {
		t.Errorf("titles = %q, %q", p.Phases[0].Title, p.Phases[1].Title)
	}
	if p.Milestones[0].Name != "First" {
		t.Errorf("milestone name = %q", p.Milestones[0].Name)
	}
	if !p.Phases[1].Items[0].Done {
		t.Errorf("[X] should be done")
	}
	if !reflect.DeepEqual(p.Unticked(), []int{1}) {
		t.Errorf("Unticked = %v", p.Unticked())
	}
}

func TestDependsOnForms(t *testing.T) {
	path := writePlan(t, strings.Join([]string{
		"### Phase 1 — A",
		"**Depends on:** —",
		"### Phase 2 — B",
		"**Depends on:** -",
		"### Phase 3 — C",
		"**Depends on:** none",
		"### Phase 4 — D",
		"### Phase 5 — E",
		"**Depends on:** Phase 2, Phase 4 · Phase 3",
		"",
	}, "\n"))

	p, err := Reader{}.Read(path)

	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if len(p.Phases[i].DependsOn) != 0 {
			t.Errorf("phase %d DependsOn = %v", i+1, p.Phases[i].DependsOn)
		}
	}
	if !reflect.DeepEqual(p.Phases[4].DependsOn, []int{2, 4, 3}) {
		t.Errorf("phase 5 DependsOn = %v", p.Phases[4].DependsOn)
	}
}

func TestDoneWhenRunsToNextBoldLineOrHeading(t *testing.T) {
	path := writePlan(t, strings.Join([]string{
		"### Phase 1 — A",
		"**Done when:** `go test ./a/...`",
		"is green.",
		"**Note:** other",
		"### Phase 2 — B",
		"**Done when:** `go vet`",
		"and more",
		"## Open questions",
		"- x",
		"",
	}, "\n"))

	p, err := Reader{}.Read(path)

	if err != nil {
		t.Fatal(err)
	}
	if p.Phases[0].DoneWhen != "`go test ./a/...`\nis green." {
		t.Errorf("phase 1 DoneWhen = %q", p.Phases[0].DoneWhen)
	}
	if p.Phases[1].DoneWhen != "`go vet`\nand more" {
		t.Errorf("phase 2 DoneWhen = %q", p.Phases[1].DoneWhen)
	}
}

func TestFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "duplicate phase number",
			content: "### Phase 1 — A\n### Phase 2 — B\n### Phase 2 — C\n",
			want:    []string{"line 3", "phase 2"},
		},
		{
			name:    "skipped phase number",
			content: "### Phase 1 — A\n### Phase 3 — C\n",
			want:    []string{"line 2", "phase 3"},
		},
		{
			name:    "first phase is not 1",
			content: "### Phase 2 — B\n",
			want:    []string{"line 1", "phase 2"},
		},
		{
			name:    "depends on a missing phase",
			content: "### Phase 1 — A\n### Phase 2 — B\n**Depends on:** Phase 1, Phase 7\n",
			want:    []string{"line 3", "phase 7"},
		},
		{
			name:    "depends on prose naming no phase",
			content: "### Phase 1 — A\n**Depends on:** the config work\n",
			want:    []string{"line 2", "the config work"},
		},
		{
			name:    "heading without dash",
			content: "### Phase 1 — A\n\n### Phase 2 B\n",
			want:    []string{"line 3", "### Phase 2 B"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writePlan(t, tc.content)

			_, err := Reader{}.Read(path)

			if err == nil {
				t.Fatal("want error, got nil")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

func TestReadMissingFile(t *testing.T) {
	_, err := Reader{}.Read(filepath.Join(t.TempDir(), "todo.md"))

	if err == nil {
		t.Fatal("want error")
	}
}

func TestDoneWhenStopsAtNestedHeading(t *testing.T) {
	path := writePlan(t, "### Phase 1 — A\n**Done when:** `go test`\nis green.\n#### Notes\nmore text\n")

	p, err := Reader{}.Read(path)

	if err != nil {
		t.Fatal(err)
	}
	if p.Phases[0].DoneWhen != "`go test`\nis green." {
		t.Errorf("DoneWhen = %q", p.Phases[0].DoneWhen)
	}
}
