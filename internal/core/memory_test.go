package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func newKeeper(facts ...MemoryFact) (*MemoryKeeper, *fakeMemory, *fakeStore, *callLog) {
	shared := &callLog{}
	mem := &fakeMemory{callLog: callLog{Shared: shared}, Facts: facts}
	st := &fakeStore{callLog: callLog{Shared: shared}}
	k := &MemoryKeeper{Memory: mem, Store: st, Face: &fakeFace{}, RunID: "run-1", Now: func() time.Time { return time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC) }}
	return k, mem, st, shared
}

func TestRememberRecordsTheEventBeforeItWritesTheFact(t *testing.T) {
	// given
	k, mem, st, shared := newKeeper()

	// when
	ok, reason := k.Remember("step", "port 5432 must be free", "stop postgres", "tests failed twice on it")

	// then
	if !ok || reason != "remembered as fact-1" {
		t.Fatalf("ok = %v, reason = %q", ok, reason)
	}
	want := `Store.Append run-1 event,Memory.Write step "port 5432 must be free"`
	if got := strings.Join(shared.Calls(), ","); got != want {
		t.Fatalf("calls = %s\nwant %s", got, want)
	}
	ev := st.Records["run-1"][0].Event
	if ev.Kind != EventMemoryRemember || ev.Fields["why"] != "tests failed twice on it" || ev.Fields["scope"] != "step" {
		t.Fatalf("event = %+v", ev)
	}
	if mem.Facts[0].Run != "run-1" || mem.Facts[0].Body != "stop postgres" {
		t.Fatalf("fact = %+v", mem.Facts[0])
	}
}

func TestRememberRefusesABadFactAndWritesNothing(t *testing.T) {
	full := make([]MemoryFact, MaxMemoryFacts)
	cases := []struct {
		name, scope, description, body string
		facts                          []MemoryFact
		reason                         string
	}{
		{"scope", "phase", "d", "", nil, `scope "phase": want one of all, step, review, watchdog`},
		{"empty", "all", "  ", "", nil, "description is empty"},
		{"two lines", "all", "a\nb", "", nil, "description must be one line"},
		{"control character", "all", "clear \x1b[2J here", "", nil, "description has a control character"},
		{"long description", "all", strings.Repeat("x", MaxMemoryDescription+1), "", nil, "description is over 150 characters"},
		{"long body", "all", "d", strings.Repeat("x", MaxMemoryBody+1), nil, "body is over 2048 bytes"},
		{"full", "all", "d", "", full, "memory holds 60 facts, the most it keeps: forget or merge some first"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			k, _, _, shared := newKeeper(c.facts...)

			ok, reason := k.Remember(c.scope, c.description, c.body, "why")

			if ok || reason != c.reason {
				t.Fatalf("ok = %v, reason = %q, want %q", ok, reason, c.reason)
			}
			if calls := shared.Calls(); len(calls) != 0 {
				t.Fatalf("calls = %v", calls)
			}
		})
	}
}

func TestRememberDoesNotWriteWhenTheEventCannotBeRecorded(t *testing.T) {
	k, _, st, shared := newKeeper()
	st.Err = errors.New("disk full")

	ok, reason := k.Remember("all", "d", "", "why")

	if ok || reason != "store: disk full" {
		t.Fatalf("ok = %v, reason = %q", ok, reason)
	}
	if got := strings.Join(shared.Calls(), ","); got != "Store.Append run-1 event" {
		t.Fatalf("calls = %s", got)
	}
}

func TestForgetRecordsTheEventBeforeItDeletesTheFact(t *testing.T) {
	// given
	k, mem, st, shared := newKeeper(MemoryFact{ID: "f1", Scope: "all", Description: "wrong"})

	// when
	ok, reason := k.Forget("f1", "the port moved")

	// then
	if !ok {
		t.Fatalf("refused: %s", reason)
	}
	if got := strings.Join(shared.Calls(), ","); got != "Store.Append run-1 event,Memory.Delete f1" {
		t.Fatalf("calls = %s", got)
	}
	if ev := st.Records["run-1"][0].Event; ev.Kind != EventMemoryForget || ev.Fields["id"] != "f1" || ev.Fields["why"] != "the port moved" {
		t.Fatalf("event = %+v", ev)
	}
	if len(mem.Facts) != 0 {
		t.Fatalf("facts = %+v", mem.Facts)
	}
}

func TestForgetRefusesAnUnknownFact(t *testing.T) {
	k, _, _, shared := newKeeper(MemoryFact{ID: "f1"})

	ok, reason := k.Forget("f2", "why")

	if ok || reason != "fact f2 not found" {
		t.Fatalf("ok = %v, reason = %q", ok, reason)
	}
	if calls := shared.Calls(); len(calls) != 0 {
		t.Fatalf("calls = %v", calls)
	}
}

func TestIndexFiltersByScope(t *testing.T) {
	// given
	k, _, _, _ := newKeeper(
		MemoryFact{ID: "a", Scope: "all", Description: "everyone", Path: "/m/a.md"},
		MemoryFact{ID: "b", Scope: "step", Description: "steps", Path: "/m/b.md"},
		MemoryFact{ID: "c", Scope: "review", Description: "reviewers", Path: "/m/c.md"},
		MemoryFact{ID: "d", Scope: "watchdog", Description: "the dog", Path: "/m/d.md"},
	)

	// when
	step, review, dog := k.Index("all", "step"), k.Index("all", "review"), k.Index()

	// then
	if want := "- /m/a.md — everyone\n- /m/b.md — steps"; step != want {
		t.Fatalf("step index = %q", step)
	}
	if want := "- /m/a.md — everyone\n- /m/c.md — reviewers"; review != want {
		t.Fatalf("review index = %q", review)
	}
	if want := "- [all] /m/a.md — everyone\n- [step] /m/b.md — steps\n- [review] /m/c.md — reviewers\n- [watchdog] /m/d.md — the dog"; dog != want {
		t.Fatalf("watchdog index = %q", dog)
	}
}

func TestIndexOfAnUnreadableFolderIsEmptyWithAWarning(t *testing.T) {
	k, mem, _, _ := newKeeper()
	mem.ListErr = errors.New("permission denied")
	face := k.Face.(*fakeFace)

	if got := k.Index("all"); got != "" {
		t.Fatalf("index = %q", got)
	}
	evs := face.events()
	if len(evs) != 1 || evs[0].Kind != "warning" || evs[0].Fields["reason"] != "memory: permission denied" {
		t.Fatalf("events = %+v", evs)
	}
}

func TestANilKeeperIndexesNothingAndRefusesWrites(t *testing.T) {
	var k *MemoryKeeper

	ok, reason := k.Remember("all", "d", "", "why")

	if k.Index() != "" || ok || reason != "not available" {
		t.Fatalf("ok = %v, reason = %q", ok, reason)
	}
}

func compactingDog(t *testing.T, status RunStatus, facts int) (*Watchdog, *reportingHost, *fakeStore, *fakeFace) {
	t.Helper()
	dog, host, store, face := haltedDog(t, status)
	all := make([]MemoryFact, facts)
	for i := range all {
		all[i] = MemoryFact{ID: fmt.Sprintf("f%d", i+1), Scope: "step", Description: fmt.Sprintf("fact %d", i+1), Path: fmt.Sprintf("/m/f%d.md", i+1)}
	}
	dog.Memory = &MemoryKeeper{Memory: &fakeMemory{Facts: all}}
	return dog, host, store, face
}

func compactPrompts(host *reportingHost) []string {
	var out []string
	for _, c := range host.Calls() {
		if strings.HasPrefix(c, "SessionHost.Prompt") && strings.Contains(c, "compact memory") {
			out = append(out, c)
		}
	}
	return out
}

func TestAnEndedRunAsksTheWatchdogToCompactTheMemory(t *testing.T) {
	for _, status := range []RunStatus{RunFinished, RunHalted} {
		t.Run(string(status), func(t *testing.T) {
			// given
			dog, host, _, face := compactingDog(t, status, 3)

			// when
			dog.CompactMemory(context.Background(), 7*time.Minute)

			// then
			prompts := compactPrompts(host)
			if len(prompts) != 1 || !strings.Contains(prompts[0], "holds 3 facts") || !strings.Contains(prompts[0], "- [step] /m/f3.md — fact 3") || !strings.HasSuffix(prompts[0], " true 7m0s") {
				t.Fatalf("compact prompts %q", prompts)
			}
			if ev := lastEvent(face); ev.Kind != EventMemoryCompacted || ev.Fields["facts"] != "3" || ev.Fields["reason"] != "" {
				t.Fatalf("event = %+v", ev)
			}
		})
	}
}

func TestNoCompactionWithoutFactsALiveWatchdogOrAnUnabortedRun(t *testing.T) {
	cases := map[string]func(*Watchdog, *fakeStore) context.Context{
		"no facts": func(d *Watchdog, _ *fakeStore) context.Context {
			d.Memory = &MemoryKeeper{Memory: &fakeMemory{}}
			return context.Background()
		},
		"no memory": func(d *Watchdog, _ *fakeStore) context.Context {
			d.Memory = nil
			return context.Background()
		},
		"gone watchdog": func(d *Watchdog, _ *fakeStore) context.Context {
			d.gone = true
			return context.Background()
		},
		"aborted": func(d *Watchdog, st *fakeStore) context.Context {
			_ = st.Append(d.RunID, Record{Kind: RecordEvent, Event: &Event{Kind: "aborted"}})
			return context.Background()
		},
		"interrupted": func(*Watchdog, *fakeStore) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dog, host, st, face := compactingDog(t, RunFinished, 2)
			ctx := setup(dog, st)

			dog.CompactMemory(ctx, time.Minute)

			if p := compactPrompts(host); len(p) != 0 || len(face.events()) != 0 {
				t.Fatalf("prompts %q, events %+v", p, face.events())
			}
		})
	}
}

func TestAStepPromptGetsTheStepAndAllFacts(t *testing.T) {
	// given
	r := newRig(t)
	var got map[string]any
	r.sm.Prompts = promptsFunc(func(name string, vars map[string]any) (string, string, error) {
		got = vars
		return "text", "embedded", nil
	})
	r.sm.Memory = &MemoryKeeper{Memory: &fakeMemory{Facts: []MemoryFact{
		{ID: "a", Scope: "all", Description: "everyone", Path: "/m/a.md"},
		{ID: "b", Scope: "review", Description: "reviewers", Path: "/m/b.md"},
		{ID: "c", Scope: "step", Description: "steps", Path: "/m/c.md"},
	}}}

	// when
	r.spawn(t, 1)

	// then
	if got["Memory"] != "- /m/a.md — everyone\n- /m/c.md — steps" {
		t.Fatalf("Memory = %q", got["Memory"])
	}
}

func TestAReviewerGetsTheReviewFactsAndAFixRoundTheStepFacts(t *testing.T) {
	// given
	r := newReviewRig(t, Reviewer{Provider: "codex"})
	r.sm.Memory = &MemoryKeeper{Memory: &fakeMemory{Facts: []MemoryFact{
		{ID: "a", Scope: "all", Description: "everyone", Path: "/m/a.md"},
		{ID: "b", Scope: "review", Description: "reviewers", Path: "/m/b.md"},
		{ID: "c", Scope: "step", Description: "steps", Path: "/m/c.md"},
	}}}
	r.behave = func(vars map[string]any) {
		r.repo.TreeChanges = nil
		findings := 0
		if vars["Round"] == 1 {
			findings = 1
		}
		writeReview(t, vars, "ok", findings)
	}
	r.onFix = func(vars map[string]any) {
		r.repo.TreeChanges = []string{"a.go"}
		writeVerdict(t, vars, entry("codex-r1-1", "real", "P1", true, ""))
	}

	// when
	r.run()

	// then
	if len(r.reviews) == 0 || r.reviews[0]["Memory"] != "- /m/a.md — everyone\n- /m/b.md — reviewers" {
		t.Fatalf("reviewer Memory = %q", r.reviews[0]["Memory"])
	}
	if len(r.fixes) == 0 || r.fixes[0]["Memory"] != "- /m/a.md — everyone\n- /m/c.md — steps" {
		t.Fatalf("fix Memory = %v", r.fixes)
	}
}
