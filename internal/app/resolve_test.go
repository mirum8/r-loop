package app

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
)

type script struct{ calls []string }

func (s *script) record(format string, args ...any) {
	s.calls = append(s.calls, fmt.Sprintf(format, args...))
}

type fakePlan struct{ s *script }

func (p fakePlan) Read(path string) (core.Plan, error) { return core.Plan{}, nil }
func (p fakePlan) Tick(path string, phase int) error   { return nil }
func (p fakePlan) Stamp(path, entryName, resolvedLine string) error {
	p.s.record("stamp %s %s: %s", path, entryName, resolvedLine)
	return nil
}

type fakeRepo struct{ s *script }

func (r fakeRepo) Commit(message string) (string, error) {
	r.s.record("commit %s", message)
	return "abc", nil
}

type answeringFace struct {
	s       *script
	answers []string
	asked   []core.Question
	events  []core.Event
}

func (f *answeringFace) Emit(ev core.Event) { f.events = append(f.events, ev) }
func (f *answeringFace) Close()             {}
func (f *answeringFace) Ask(q core.Question) (string, error) {
	f.asked = append(f.asked, q)
	if f.s != nil {
		f.s.record("ask %s", q.ID)
	}
	if len(f.answers) == 0 {
		return "", core.ErrNoInput
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a, nil
}

func TestEachBlockingEntryIsAskedStampedAndCommittedInDocumentOrder(t *testing.T) {
	s := &script{}
	face := &answeringFace{s: s, answers: []string{"Postgres", "keep v1"}}
	entries := []core.Entry{
		{Name: "Pick the database", Body: "- [ ] **Pick the database** — Owner: me · Blocks: Phase 2"},
		{Name: "API version", Body: "- [ ] **API version** — Owner: me · Blocks: all"},
	}

	_, err := resolveFirst(fakePlan{s}, fakeRepo{s}, face, "/repo/todo.md", entries, time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local))

	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ask r1",
		"stamp /repo/todo.md Pick the database: 2026-09-18 — Postgres",
		"commit plan: resolve Pick the database",
		"ask r2",
		"stamp /repo/todo.md API version: 2026-09-18 — keep v1",
		"commit plan: resolve API version",
	}
	if !reflect.DeepEqual(s.calls, want) {
		t.Fatalf("calls\n%q\nwant\n%q", s.calls, want)
	}
	q := face.asked[0]
	if q.Text != "Pick the database\n- [ ] **Pick the database** — Owner: me · Blocks: Phase 2" || len(q.Options) != 0 || q.Recommended != "" || q.Step.Kind != "resolve first" {
		t.Fatalf("question %+v", q)
	}
}

func TestAnUnansweredEntryStopsBeforeStamping(t *testing.T) {
	s := &script{}

	_, err := resolveFirst(fakePlan{s}, fakeRepo{s}, &answeringFace{s: s}, "/repo/todo.md", []core.Entry{{Name: "Pick the database"}}, time.Now())

	if code := exitCode(t, err); code != 4 || !strings.Contains(err.Error(), "Pick the database") {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if !reflect.DeepEqual(s.calls, []string{"ask r1"}) {
		t.Fatalf("calls %q", s.calls)
	}
}

func TestPreflightInTUIModeResolvesABlockingEntryAndCommitsItBeforeTheRun(t *testing.T) {
	f := newFixture(t)
	data, _ := os.ReadFile(f.todo)
	f.write("docs/topic/todo.md", strings.Replace(string(data), "## Waves",
		"## Resolve first\n- [ ] **Pick the database** — Owner: me · Blocks: Phase 2\n\n## Waves", 1))
	f.commit()
	opts, err := ParseArgs([]string{f.todo})
	if err != nil {
		t.Fatal(err)
	}
	w, err := Wire(opts, f.env)
	if err != nil {
		t.Fatal(err)
	}
	w.Face = &answeringFace{answers: []string{"Postgres"}}

	err = Preflight(w)

	if err != nil {
		t.Fatal(err)
	}
	if got := git(t, f.root, "log", "-1", "--format=%s"); got != "plan: resolve Pick the database" {
		t.Fatalf("head %q", got)
	}
	if got := git(t, f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("tree not clean: %q", got)
	}
	todo, _ := os.ReadFile(f.todo)
	today := time.Now().Format("2006-01-02")
	if !strings.Contains(string(todo), "- [x] **Pick the database**") || !strings.Contains(string(todo), "Resolved: "+today+" — Postgres") {
		t.Fatalf("todo:\n%s", todo)
	}
	if len(w.Plan.Blocking([]int{2})) != 0 || len(w.Loop.Plan.Blocking([]int{2})) != 0 {
		t.Fatal("the loop still sees the entry as blocking")
	}
	if w.Loop.RunID == "" {
		t.Fatal("no run created after resolving")
	}
	run, err := w.Store.Load(w.Loop.RunID)
	if err != nil {
		t.Fatal(err)
	}
	rep := core.Report(run, w.Plan)
	if !strings.Contains(rep, "human touches: 1\n") || !strings.Contains(rep, "- r1 resolve first: Pick the database → Postgres (maintainer)\n") {
		t.Fatalf("report:\n%s", rep)
	}
}

func TestAnAnsweredEntryIsReportedAsAnsweredByTheMaintainer(t *testing.T) {
	face := &answeringFace{answers: []string{"Postgres"}}
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.Local)

	events, err := resolveFirst(fakePlan{&script{}}, fakeRepo{&script{}}, face, "/repo/todo.md", []core.Entry{{Name: "Pick the database"}}, now)

	if err != nil {
		t.Fatal(err)
	}
	want := []core.Event{{At: now, Kind: "human", Step: "resolve first", Fields: map[string]string{"what": "answer", "id": "r1", "by": "maintainer", "entry": "Pick the database", "answer": "Postgres"}}}
	if !reflect.DeepEqual(face.events, want) {
		t.Fatalf("events %+v", face.events)
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("returned events %+v", events)
	}
}
