package store

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
)

var t0 = time.Date(2026, 9, 18, 14, 3, 5, 0, time.UTC)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	s := New(root)
	s.now = func() time.Time { return t0 }
	return s, root
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCreateMakesRunDirectory(t *testing.T) {
	s, root := newStore(t)

	id, err := s.Create(core.RunMeta{Todo: "docs/todo.md", ResolvedConfig: []byte("pipeline:\n  - plan\n"), Started: t0})
	if err != nil {
		t.Fatal(err)
	}

	if id != "20260918-140305" {
		t.Fatalf("runID = %q", id)
	}
	dir := filepath.Join(root, ".r-loop", "runs", id)
	if s.Dir(id) != dir {
		t.Fatalf("Dir = %q, want %q", s.Dir(id), dir)
	}
	if got := readFile(t, filepath.Join(dir, "config.resolved.yaml")); got != "pipeline:\n  - plan\n" {
		t.Fatalf("config.resolved.yaml = %q", got)
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "meta.json"))), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["todo"] != "docs/todo.md" || meta["started"] != "2026-09-18T14:03:05Z" {
		t.Fatalf("meta.json = %v", meta)
	}
	for _, f := range []string{"events.jsonl", "questions.jsonl", "signals.jsonl", "remedies.jsonl"} {
		if got := readFile(t, filepath.Join(dir, f)); got != "" {
			t.Fatalf("%s = %q, want empty", f, got)
		}
	}
}

func TestCreateInSameSecondGetsSuffix(t *testing.T) {
	s, _ := newStore(t)

	first, _ := s.Create(core.RunMeta{Todo: "todo.md"})
	second, err := s.Create(core.RunMeta{Todo: "todo.md"})
	if err != nil {
		t.Fatal(err)
	}
	third, _ := s.Create(core.RunMeta{Todo: "todo.md"})

	if first != "20260918-140305" || second != "20260918-140305-2" || third != "20260918-140305-3" {
		t.Fatalf("ids = %q %q %q", first, second, third)
	}
}

func TestAppendWritesOneLineToTheKindsFile(t *testing.T) {
	cases := map[string]string{
		core.RecordStep:     "events.jsonl",
		core.RecordRun:      "events.jsonl",
		core.RecordLanding:  "events.jsonl",
		core.RecordEvent:    "events.jsonl",
		core.RecordQuestion: "questions.jsonl",
		core.RecordSignal:   "signals.jsonl",
		core.RecordRemedy:   "remedies.jsonl",
	}
	for kind, file := range cases {
		t.Run(kind, func(t *testing.T) {
			s, _ := newStore(t)
			id, _ := s.Create(core.RunMeta{Todo: "todo.md"})

			if err := s.Append(id, core.Record{Kind: kind, At: t0, Reason: "why"}); err != nil {
				t.Fatal(err)
			}

			got := readFile(t, filepath.Join(s.Dir(id), file))
			if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, "\n") {
				t.Fatalf("%s = %q, want one line", file, got)
			}
			var rec core.Record
			if err := json.Unmarshal([]byte(got), &rec); err != nil {
				t.Fatal(err)
			}
			if rec.Kind != kind || rec.Reason != "why" || !rec.At.Equal(t0) {
				t.Fatalf("record = %+v", rec)
			}
		})
	}
}

func TestAppendUnknownKindFails(t *testing.T) {
	s, _ := newStore(t)
	id, _ := s.Create(core.RunMeta{Todo: "todo.md"})

	if err := s.Append(id, core.Record{Kind: "bogus"}); err == nil {
		t.Fatal("want error for unknown kind")
	}
}

func TestLoadReplaysAMixedLog(t *testing.T) {
	s, _ := newStore(t)
	id, _ := s.Create(core.RunMeta{Todo: "docs/todo.md", Started: t0})
	plan := core.StepKey{Run: id, Phase: 1, Kind: "plan", Attempt: 1}
	impl := core.StepKey{Run: id, Phase: 1, Kind: "implement", Attempt: 1}
	asked := core.Question{ID: "q1", Step: impl, Text: "which port?", AskedAt: t0}
	answered := asked
	answered.Answer, answered.AnsweredBy, answered.AnsweredAt = "8080", "human", t0.Add(time.Minute)
	signal := core.Signal{Seq: 1, Kind: core.SignalWarn, Source: core.SourceWatchdog, Step: impl, Reason: "slow", At: t0}
	remedy := core.Remedy{ID: "r1", Step: impl, Class: "retry", Command: "go mod tidy", ProposedAt: t0}
	landing := core.Landing{Phase: 1, MergeSHA: "abc123"}
	event := core.Event{At: t0, Kind: "review-round", Phase: 1, Step: "plan", Fields: map[string]string{"round": "1"}}
	records := []core.Record{
		{Kind: core.RecordRun, At: t0, Run: core.RunRunning},
		{Kind: core.RecordStep, At: t0, Step: &plan, State: core.StepQueued},
		{Kind: core.RecordStep, At: t0, Step: &plan, State: core.StepOK},
		{Kind: core.RecordEvent, At: t0, Event: &event},
		{Kind: core.RecordStep, At: t0, Step: &impl, State: core.StepRunning},
		{Kind: core.RecordQuestion, At: t0, Question: &asked},
		{Kind: core.RecordQuestion, At: t0, Question: &answered},
		{Kind: core.RecordSignal, At: t0, Signal: &signal},
		{Kind: core.RecordRemedy, At: t0, Remedy: &remedy},
		{Kind: core.RecordLanding, At: t0, Landing: &landing},
		{Kind: core.RecordRun, At: t0, Run: core.RunHalted},
	}
	for _, rec := range records {
		if err := s.Append(id, rec); err != nil {
			t.Fatal(err)
		}
	}

	st, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}

	want := core.RunState{
		ID:        id,
		Todo:      "docs/todo.md",
		Started:   t0,
		Status:    core.RunHalted,
		Steps:     map[core.StepKey]core.StepState{plan: core.StepOK, impl: core.StepRunning},
		LastStep:  &impl,
		Landed:    []core.Landing{landing},
		Questions: []core.Question{answered},
		Signals:   []core.Signal{signal},
		Remedies:  []core.Remedy{remedy},
		Events:    []core.Event{event},
	}
	if !reflect.DeepEqual(st, want) {
		t.Fatalf("Load =\n%+v\nwant\n%+v", st, want)
	}
}

func TestLoadLastStepPrefersTheLastNonTerminalStep(t *testing.T) {
	s, _ := newStore(t)
	id, _ := s.Create(core.RunMeta{Todo: "todo.md"})
	impl := core.StepKey{Run: id, Phase: 1, Kind: "implement", Attempt: 1}
	next := core.StepKey{Run: id, Phase: 2, Kind: "plan", Attempt: 1}
	s.Append(id, core.Record{Kind: core.RecordStep, Step: &impl, State: core.StepRunning})
	s.Append(id, core.Record{Kind: core.RecordStep, Step: &next, State: core.StepFailed})

	st, _ := s.Load(id)

	if st.LastStep == nil || *st.LastStep != impl {
		t.Fatalf("LastStep = %v, want %v", st.LastStep, impl)
	}
}

func TestLoadLastStepFallsBackToTheLastStep(t *testing.T) {
	s, _ := newStore(t)
	id, _ := s.Create(core.RunMeta{Todo: "todo.md"})
	plan := core.StepKey{Run: id, Phase: 1, Kind: "plan", Attempt: 1}
	impl := core.StepKey{Run: id, Phase: 1, Kind: "implement", Attempt: 1}
	s.Append(id, core.Record{Kind: core.RecordStep, Step: &plan, State: core.StepOK})
	s.Append(id, core.Record{Kind: core.RecordStep, Step: &impl, State: core.StepFailed})

	st, _ := s.Load(id)

	if st.LastStep == nil || *st.LastStep != impl {
		t.Fatalf("LastStep = %v, want %v", st.LastStep, impl)
	}
}

func TestLoadEmptyRunIsCreated(t *testing.T) {
	s, _ := newStore(t)
	id, _ := s.Create(core.RunMeta{Todo: "todo.md"})

	st, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}

	if st.Status != core.RunCreated || st.LastStep != nil || len(st.Steps) != 0 || len(st.Warnings) != 0 {
		t.Fatalf("Load = %+v", st)
	}
}

func TestLoadSkipsATruncatedLastLine(t *testing.T) {
	s, _ := newStore(t)
	id, _ := s.Create(core.RunMeta{Todo: "todo.md"})
	s.Append(id, core.Record{Kind: core.RecordRun, Run: core.RunRunning})
	f, _ := os.OpenFile(filepath.Join(s.Dir(id), "signals.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"Kind":"signal","Sig`)
	f.Close()

	st, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}

	if st.Status != core.RunRunning || len(st.Signals) != 0 {
		t.Fatalf("Load = %+v", st)
	}
	if len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "signals.jsonl") {
		t.Fatalf("Warnings = %q", st.Warnings)
	}
}

func TestLoadCorruptMiddleLineIsAnError(t *testing.T) {
	s, _ := newStore(t)
	id, _ := s.Create(core.RunMeta{Todo: "todo.md"})
	os.WriteFile(filepath.Join(s.Dir(id), "events.jsonl"), []byte("garbage\n{\"Kind\":\"run\",\"Run\":\"running\"}\n"), 0o644)

	if _, err := s.Load(id); err == nil {
		t.Fatal("want error for a corrupt middle line")
	}
}

func TestLoadCorruptTerminatedLastLineIsAnError(t *testing.T) {
	s, _ := newStore(t)
	id, _ := s.Create(core.RunMeta{Todo: "todo.md"})
	os.WriteFile(filepath.Join(s.Dir(id), "events.jsonl"), []byte("{\"Kind\":\"run\",\"Run\":\"running\"}\ngarbage\n"), 0o644)

	if _, err := s.Load(id); err == nil {
		t.Fatal("want error for a complete but corrupt last line")
	}
}

func TestLoadMissingRunIsAnError(t *testing.T) {
	s, _ := newStore(t)

	if _, err := s.Load("20260101-000000"); err == nil {
		t.Fatal("want error for a missing run")
	}
}

func TestCurrentRoundTrip(t *testing.T) {
	s, root := newStore(t)

	if _, _, ok := s.Current(); ok {
		t.Fatal("Current before SetCurrent: ok = true")
	}
	if err := s.SetCurrent("20260918-140305", 4242); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, ".r-loop", "runs", "current")); strings.TrimSpace(got) != "20260918-140305 4242" {
		t.Fatalf("current = %q", got)
	}
	id, pid, ok := s.Current()
	if !ok || id != "20260918-140305" || pid != 4242 {
		t.Fatalf("Current = %q %d %v", id, pid, ok)
	}
	if err := s.ClearCurrent(); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Current(); ok {
		t.Fatal("Current after ClearCurrent: ok = true")
	}
	if err := s.ClearCurrent(); err != nil {
		t.Fatalf("second ClearCurrent: %v", err)
	}
}

func TestSetCurrentLeavesNoTempFile(t *testing.T) {
	s, root := newStore(t)

	s.SetCurrent("a", 1)
	s.SetCurrent("b", 2)

	entries, _ := os.ReadDir(filepath.Join(root, ".r-loop", "runs"))
	if len(entries) != 1 || entries[0].Name() != "current" {
		t.Fatalf("runs dir = %v", entries)
	}
	if id, pid, _ := s.Current(); id != "b" || pid != 2 {
		t.Fatalf("Current = %q %d", id, pid)
	}
}

func TestAbortMarker(t *testing.T) {
	s, _ := newStore(t)
	id, _ := s.Create(core.RunMeta{Todo: "todo.md"})

	if s.Aborted(id) {
		t.Fatal("Aborted before MarkAbort")
	}
	if err := s.MarkAbort(id); err != nil {
		t.Fatal(err)
	}
	if !s.Aborted(id) {
		t.Fatal("Aborted after MarkAbort = false")
	}
	if _, err := os.Stat(filepath.Join(s.Dir(id), "abort")); err != nil {
		t.Fatal(err)
	}
}

func gitInit(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput()
	if err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return root
}

func TestEnsureExcludedAppendsOnce(t *testing.T) {
	root := gitInit(t)
	exclude := filepath.Join(root, ".git", "info", "exclude")
	os.WriteFile(exclude, []byte("*.log"), 0o644)

	if err := EnsureExcluded(root); err != nil {
		t.Fatal(err)
	}
	if err := EnsureExcluded(root); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, exclude); got != "*.log\n.r-loop/runs/\n.r-loop/wt/\n" {
		t.Fatalf("exclude = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".gitignore")); !os.IsNotExist(err) {
		t.Fatalf(".gitignore touched: %v", err)
	}
}

func TestEnsureExcludedAddsOnlyTheMissingLine(t *testing.T) {
	root := gitInit(t)
	exclude := filepath.Join(root, ".git", "info", "exclude")
	os.WriteFile(exclude, []byte(".r-loop/wt/\n"), 0o644)

	if err := EnsureExcluded(root); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, exclude); got != ".r-loop/wt/\n.r-loop/runs/\n" {
		t.Fatalf("exclude = %q", got)
	}
}

func TestEnsureExcludedUsesTheCommonDirFromAWorktree(t *testing.T) {
	root := gitInit(t)
	run := func(args ...string) {
		out, err := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	run("worktree", "add", "-q", "--detach", wt)

	if err := EnsureExcluded(wt); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, filepath.Join(root, ".git", "info", "exclude")); !strings.Contains(got, ".r-loop/runs/\n.r-loop/wt/\n") {
		t.Fatalf("common exclude = %q", got)
	}
}

func TestEnsureExcludedOutsideGitFails(t *testing.T) {
	if err := EnsureExcluded(t.TempDir()); err == nil {
		t.Fatal("want error outside a git repository")
	}
}

var _ core.Store = (*Store)(nil)
