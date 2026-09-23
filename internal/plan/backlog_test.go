package plan

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"r-loop/internal/core"
)

func writeBacklog(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "issues-polka-2026-08-18.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadIssuesDraftFile(t *testing.T) {
	p, err := Reader{}.Read("testdata/issues.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if !p.Backlog {
		t.Error("Backlog = false")
	}
	if p.Topic != "issues" {
		t.Errorf("Topic = %q, want issues", p.Topic)
	}
	var titles []string
	for _, ph := range p.Phases {
		titles = append(titles, ph.Title)
	}
	want := []string{
		"[#1] Везде заменить рабочее название Book Tracker на «Полка»",
		"[#2] Добавить возможность переименовывать созданную полку",
		"[#5a] Показывать обложки книг",
		"[#2/2] Экспорт книг в CSV",
	}
	if !reflect.DeepEqual(titles, want) {
		t.Fatalf("titles = %q", titles)
	}
	if got := p.Unticked(); !reflect.DeepEqual(got, []string{"1", "3", "4"}) {
		t.Errorf("Unticked = %v, want [1 3 4]", got)
	}
	wantItems := []core.Item{
		{Text: "The book page shows its cover image"},
		{Text: "The cover is cached for a day"},
	}
	if !reflect.DeepEqual(p.Phases[2].Items, wantItems) {
		t.Errorf("phase 3 items = %+v", p.Phases[2].Items)
	}
	if want := []core.Item{{Text: "[#2/2] Экспорт книг в CSV"}}; !reflect.DeepEqual(p.Phases[3].Items, want) {
		t.Errorf("phase 4 items = %+v", p.Phases[3].Items)
	}
	if !strings.Contains(p.Phases[0].Block, "Covers templates") || strings.Contains(p.Phases[0].Block, "[#2]") {
		t.Errorf("phase 1 block = %q", p.Phases[0].Block)
	}
	for _, ph := range p.Phases {
		if ph.DoneWhen != "" || ph.Files != nil || ph.DependsOn != nil {
			t.Errorf("phase %s carries plan fields: %+v", ph.ID, ph)
		}
	}
}

func TestReadBacklogVariants(t *testing.T) {
	path := writeBacklog(t, `# Bugs

* [ ] Login 500s on '+' in email
- plain bullet item
1. numbered item
   - its criterion
- ~~struck out~~
- [X] ticked upper

## Signup rejects long names

Names over 64 chars fail validation.

`+"```"+`
- not an item inside a fence
`+"```"+`

## Fixed

- old fixed bug
`)

	p, err := Reader{}.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	type row struct {
		title string
		done  bool
	}
	var got []row
	for _, ph := range p.Phases {
		got = append(got, row{ph.Title, ph.Items[0].Done})
	}
	want := []row{
		{"Login 500s on '+' in email", false},
		{"plain bullet item", false},
		{"numbered item", false},
		{"struck out", true},
		{"ticked upper", true},
		{"Signup rejects long names", false},
		{"old fixed bug", true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %+v", got)
	}
	if p.Phases[2].Items[0].Text != "its criterion" {
		t.Errorf("numbered item criteria = %+v", p.Phases[2].Items)
	}
	if !strings.Contains(p.Phases[5].Block, "Names over 64 chars") {
		t.Errorf("heading item block = %q", p.Phases[5].Block)
	}
}

func TestReadBacklogWithNoItemsFails(t *testing.T) {
	path := writeBacklog(t, "# Nothing\n\nJust prose.\n")

	_, err := Reader{}.Read(path)

	if err == nil || !strings.Contains(err.Error(), "no backlog items") {
		t.Fatalf("err = %v", err)
	}
}

func TestTickBacklogMarksTheItemLine(t *testing.T) {
	path := writeBacklog(t, "# B\n\n- [ ] [#1] first\r\n      - crit\r\n\n- second, no box\n")

	if err := (Reader{}).Tick(path, core.Phase{ID: "1"}); err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	if err := (Reader{}).Tick(path, core.Phase{ID: "2"}); err != nil {
		t.Fatalf("Tick 2: %v", err)
	}

	want := "# B\n\n- [x] [#1] first  <!-- fixed: r-loop/phase-1 -->\r\n      - crit\r\n\n- second, no box  <!-- fixed: r-loop/phase-2 -->\n"
	if got := strings.Join(fileLines(t, path), ""); got != want {
		t.Fatalf("file =\n%q\nwant\n%q", got, want)
	}
	p, err := Reader{}.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Unticked()) != 0 {
		t.Errorf("Unticked after tick = %v", p.Unticked())
	}
	if err := (Reader{}).Tick(path, core.Phase{ID: "1"}); !errors.Is(err, ErrNothingToTick) {
		t.Errorf("second tick err = %v, want ErrNothingToTick", err)
	}
}

func TestTickBacklogKeepsNumberingOfLaterItems(t *testing.T) {
	path := writeBacklog(t, "- [x] done  <!-- fixed: x -->\n- [ ] a\n- [ ] b\n")

	if err := (Reader{}).Tick(path, core.Phase{ID: "3"}); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	want := "- [x] done  <!-- fixed: x -->\n- [ ] a\n- [x] b  <!-- fixed: r-loop/phase-3 -->\n"
	if got := strings.Join(fileLines(t, path), ""); got != want {
		t.Fatalf("file = %q", got)
	}
}

func TestReadRefusesTheNotesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues-polka-2026-08-18-notes.md")
	if err := os.WriteFile(path, []byte("# Notes\n\n## Questions — need an answer\n\n**[#3] Цены** prose\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Reader{}.Read(path)

	if err == nil || !strings.Contains(err.Error(), "pass issues-polka-2026-08-18.md") {
		t.Fatalf("err = %v", err)
	}
}

func TestTickBacklogTicksEveryMemberWithTheGroupsBranch(t *testing.T) {
	path := writeBacklog(t, "- [ ] a\n- [ ] b\n- c, no box\n- [ ] d\n")

	if err := (Reader{}).Tick(path, core.Phase{ID: "1", Members: []string{"1", "3", "4"}}); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	want := "- [x] a  <!-- fixed: r-loop/phase-1 -->\n- [ ] b\n- c, no box  <!-- fixed: r-loop/phase-1 -->\n- [x] d  <!-- fixed: r-loop/phase-1 -->\n"
	if got := strings.Join(fileLines(t, path), ""); got != want {
		t.Fatalf("file =\n%q\nwant\n%q", got, want)
	}
}

func TestTickBacklogRefusesAnAlreadyDoneMember(t *testing.T) {
	const text = "- [ ] a\n- [x] b  <!-- fixed: x -->\n- [ ] c\n"
	path := writeBacklog(t, text)

	err := Reader{}.Tick(path, core.Phase{ID: "1", Members: []string{"1", "2", "3"}})

	if !errors.Is(err, ErrNothingToTick) || !strings.Contains(err.Error(), "item 2") {
		t.Fatalf("err = %v, want ErrNothingToTick naming item 2", err)
	}
	if got := strings.Join(fileLines(t, path), ""); got != text {
		t.Errorf("file changed: %q", got)
	}
}
