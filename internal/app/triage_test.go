package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/plan"
)

const backlogTodo = `# Backlog

- [ ] [#1] first
      - one works

- [ ] [#2] second
      - two works

- [ ] [#3] third
      - three works
`

type triaged struct {
	f    *fixture
	w    *Wiring
	dog  *dogHost
	land *landRecorder
}

func startTriage(t *testing.T, onTriage func(w *Wiring, text string), args ...string) *triaged {
	t.Helper()
	f := newResumeFixture(t, noReviewConfig)
	w, err := f.preflight(append([]string{f.todo, "--plain"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	land := f.sim(w, newSim())
	dog := &dogHost{triage: func(text string) { onTriage(w, text) }}
	w.Dog.Host = dog
	return &triaged{f: f, w: w, dog: dog, land: land}
}

func (k *triaged) run() int {
	return k.w.Execute(core.RunOptions{Phases: []string{"1", "2", "3"}})
}

func verdicts(statuses map[string]core.PhaseVerdict) core.Triage {
	var t core.Triage
	for _, id := range []string{"1", "2", "3"} {
		v, ok := statuses[id]
		if !ok {
			v = core.PhaseVerdict{Phase: id, Status: core.VerdictBuild}
		}
		t.Phases = append(t.Phases, v)
	}
	return t
}

func submit(t *testing.T, w *Wiring, tr core.Triage) string {
	t.Helper()
	ok, reason, table := w.submitTriage(tr)
	if !ok {
		t.Errorf("submit_triage refused: %s", reason)
	}
	return table
}

func gate(t *testing.T, w *Wiring, g core.GateDecision) string {
	t.Helper()
	ok, reason, table := w.submitGate(g)
	if !ok {
		t.Errorf("submit_gate refused: %s", reason)
	}
	return table
}

func skippedPhases(run core.RunState) map[string]string {
	out := map[string]string{}
	for _, e := range stepEvents(run, core.TriageSkipped) {
		out[e.Phase] = e.Fields["status"]
	}
	return out
}

func TestTriageStartsTheRunOnGoAndRecordsTheRunList(t *testing.T) {
	var table string
	k := startTriage(t, func(w *Wiring, text string) {
		table = submit(t, w, allBuild(text))
		gate(t, w, core.GateDecision{Decision: core.GateGo, MaintainerSaid: "go"})
	})

	code := k.run()

	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, k.f.out, k.f.err)
	}
	if !k.dog.prompted("triage plan " + k.f.todo + " phases 1, 2, 3.") {
		t.Errorf("no triage request: %q", k.dog.Calls())
	}
	if !strings.Contains(table, "| 1 | one |") || !strings.Contains(table, "Verified") {
		t.Errorf("table:\n%s", table)
	}
	if !slices.Equal(k.land.landed, []string{"1", "2", "3"}) {
		t.Errorf("landed %v", k.land.landed)
	}
	run := k.f.load(k.w.Loop.RunID)
	if got := recordedRunList(run); !slices.Equal(got, []string{"1", "2", "3"}) {
		t.Errorf("run list %v", got)
	}
	kinds := []string{}
	for _, e := range run.Events {
		if slices.Contains([]string{"triage-start", "triage", "run-list", "phase-start"}, e.Kind) && !slices.Contains(kinds, e.Kind) {
			kinds = append(kinds, e.Kind)
		}
	}
	if !slices.Equal(kinds, []string{"triage-start", "triage", "run-list", "phase-start"}) {
		t.Errorf("event order %v", kinds)
	}
	dir := k.w.Store.Dir(k.w.Loop.RunID)
	var g gateRecord
	data, _ := os.ReadFile(filepath.Join(dir, "gate.json"))
	if err := json.Unmarshal(data, &g); err != nil || g.Decision != "go" || g.By != "maintainer" || g.MaintainerSaid != "go" || len(g.Triage.Phases) != 3 {
		t.Errorf("gate.json %s: %v", data, err)
	}
	for _, name := range []string{"triage.json", "triage.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Error(err)
		}
	}
	if !strings.Contains(k.f.out.String(), "| 2 | two |") || !strings.Contains(k.f.out.String(), "run list: 1, 2, 3\n") {
		t.Errorf("plain face:\n%s", k.f.out)
	}
}

func TestARunListThatCannotBeRecordedHaltsBeforeAnyStep(t *testing.T) {
	k := startTriage(t, func(w *Wiring, text string) {
		submit(t, w, allBuild(text))
		gate(t, w, core.GateDecision{Decision: core.GateGo, MaintainerSaid: "go"})
	})
	k.w.records.Store = failingStore{Store: k.w.Store, fail: func(rec core.Record) bool {
		return rec.Kind == core.RecordEvent && rec.Event != nil && rec.Event.Kind == "run-list"
	}}
	code := k.run()
	if code != 2 || !strings.Contains(k.f.err.String(), "record: disk full") || !strings.Contains(k.f.out.String(), "!  record: disk full") || len(k.land.landed) != 0 {
		t.Fatalf("code=%d stderr=%q out=%q landed=%v", code, k.f.err, k.f.out, k.land.landed)
	}
	if sim, ok := k.w.Loop.Sessions.Host.(*simHost); ok && len(sim.startedAgents()) != 0 {
		t.Fatalf("started %v", sim.startedAgents())
	}
}

func TestAnAlreadyDonePhaseIsDroppedNotTicked(t *testing.T) {
	k := startTriage(t, func(w *Wiring, text string) {
		submit(t, w, verdicts(map[string]core.PhaseVerdict{"2": {Phase: "2", Status: core.VerdictAlreadyDone, Note: "built at docs/topic/todo.md:9"}}))
		gate(t, w, core.GateDecision{Decision: core.GateGo, MaintainerSaid: "go"})
	})
	head := git(t, k.f.root, "rev-parse", "HEAD")

	code := k.run()

	if code != 0 || !slices.Equal(k.land.landed, []string{"1", "3"}) {
		t.Fatalf("exit %d landed %v\n%s", code, k.land.landed, k.f.err)
	}
	run := k.f.load(k.w.Loop.RunID)
	if got := skippedPhases(run); len(got) != 1 || got["2"] != core.VerdictAlreadyDone {
		t.Errorf("skipped %v", got)
	}
	if got := recordedRunList(run); !slices.Equal(got, []string{"1", "3"}) {
		t.Errorf("run list %v", got)
	}
	if data, _ := os.ReadFile(k.f.todo); string(data) != resumeTodo {
		t.Errorf("todo changed:\n%s", data)
	}
	if got := git(t, k.f.root, "rev-parse", "HEAD"); got != head {
		t.Errorf("a commit was made")
	}
}

func TestAnAlreadyDoneVerdictWithoutAnExistingCitationIsRefused(t *testing.T) {
	var reason string
	k := startTriage(t, func(w *Wiring, text string) {
		_, reason, _ = w.submitTriage(verdicts(map[string]core.PhaseVerdict{"2": {Phase: "2", Status: core.VerdictAlreadyDone, Note: "built at nowhere.go:9"}}))
		submit(t, w, allBuild(text))
		gate(t, w, core.GateDecision{Decision: core.GateGo, MaintainerSaid: "go"})
	})

	if code := k.run(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(reason, "citation nowhere.go is not a file in the primary tree") {
		t.Errorf("reason %q", reason)
	}
}

func TestABlockedPhaseAndItsDependentsAreDropped(t *testing.T) {
	k := startTriage(t, func(w *Wiring, text string) {
		submit(t, w, verdicts(map[string]core.PhaseVerdict{"1": {Phase: "1", Status: core.VerdictBlocked, Note: "needs an API key"}}))
		gate(t, w, core.GateDecision{Decision: core.GateGo, MaintainerSaid: "go"})
	})

	code := k.run()

	if code != 0 || !slices.Equal(k.land.landed, []string{"2"}) {
		t.Fatalf("exit %d landed %v\n%s", code, k.land.landed, k.f.err)
	}
	if got := skippedPhases(k.f.load(k.w.Loop.RunID)); got["1"] != core.VerdictBlocked || got["3"] != "dependent" {
		t.Errorf("skipped %v", got)
	}
}

func TestAMaintainerDropIsApplied(t *testing.T) {
	k := startTriage(t, func(w *Wiring, text string) {
		submit(t, w, allBuild(text))
		gate(t, w, core.GateDecision{Decision: core.GateGo, Drop: []string{"2"}, MaintainerSaid: "go without 2"})
	})

	code := k.run()

	if code != 0 || !slices.Equal(k.land.landed, []string{"1", "3"}) {
		t.Fatalf("exit %d landed %v\n%s", code, k.land.landed, k.f.err)
	}
	if got := skippedPhases(k.f.load(k.w.Loop.RunID)); len(got) != 1 || got["2"] != "dropped" {
		t.Errorf("skipped %v", got)
	}
}

func TestReviseReturnsTheNewTableAndAsksAgain(t *testing.T) {
	var revised string
	var landedAtRevise int
	var k *triaged
	k = startTriage(t, func(w *Wiring, text string) {
		submit(t, w, allBuild(text))
		revised = gate(t, w, core.GateDecision{Decision: core.GateRevise, Drop: []string{"3"}, MaintainerSaid: "not 3"})
		time.Sleep(200 * time.Millisecond)
		k.land.mu.Lock()
		landedAtRevise = len(k.land.landed)
		k.land.mu.Unlock()
		gate(t, w, core.GateDecision{Decision: core.GateGo, MaintainerSaid: "go"})
	})

	code := k.run()

	if code != 0 || !slices.Equal(k.land.landed, []string{"1", "2"}) {
		t.Fatalf("exit %d landed %v\n%s", code, k.land.landed, k.f.err)
	}
	if !strings.Contains(revised, "dropped by the maintainer") {
		t.Errorf("revised table:\n%s", revised)
	}
	if landedAtRevise != 0 {
		t.Errorf("the run started on revise")
	}
	if got := stepEvents(k.f.load(k.w.Loop.RunID), "triage"); len(got) != 3 {
		t.Errorf("triage events %d, want 3", len(got))
	}
}

func TestAbortAtTheGateExits1AndResumeTriagesAgain(t *testing.T) {
	k := startTriage(t, func(w *Wiring, text string) {
		submit(t, w, allBuild(text))
		gate(t, w, core.GateDecision{Decision: core.GateAbort, MaintainerSaid: "stop"})
	})

	code := k.run()

	if code != 1 || !strings.Contains(k.f.err.String(), "the maintainer aborted at the triage gate; r-loop resume triages again") || len(k.land.landed) != 0 {
		t.Fatalf("exit %d landed %v: %s", code, k.land.landed, k.f.err)
	}
	run := k.f.load(k.w.Loop.RunID)
	if run.Status != core.RunHalted || len(stepEvents(run, "run-list")) != 0 {
		t.Errorf("status %s, run lists %d", run.Status, len(stepEvents(run, "run-list")))
	}

	next, lander, err := k.f.resume(newSim())

	if err != nil || next != 0 {
		t.Fatalf("resume exit %d: %v\n%s%s", next, err, k.f.out, k.f.err)
	}
	if !slices.Equal(lander.landed, []string{"1", "2", "3"}) {
		t.Errorf("landed %v", lander.landed)
	}
	if got := stepEvents(k.f.load(k.w.Loop.RunID), "triage-start"); len(got) != 2 {
		t.Errorf("triage-start %d, want 2", len(got))
	}
}

func TestUnattendedAndYesStartWithoutAGate(t *testing.T) {
	for flag, by := range map[string]string{"--unattended": "unattended", "--yes": "yes"} {
		t.Run(flag, func(t *testing.T) {
			var refused string
			var asked bool
			k := startTriage(t, func(w *Wiring, text string) {
				asked = asksTheMaintainer(text) || !strings.Contains(text, "Do not ask the maintainer")
				_, refused, _ = w.submitGate(core.GateDecision{Decision: core.GateGo, MaintainerSaid: "go"})
				submit(t, w, allBuild(text))
			}, flag)

			code := k.run()

			if code != 0 || len(k.land.landed) != 3 {
				t.Fatalf("exit %d landed %v\n%s", code, k.land.landed, k.f.err)
			}
			if asked || refused != "this run starts without asking the maintainer; there is no gate" {
				t.Errorf("asked %v, gate %q", asked, refused)
			}
			var g gateRecord
			data, _ := os.ReadFile(filepath.Join(k.w.Store.Dir(k.w.Loop.RunID), "gate.json"))
			if err := json.Unmarshal(data, &g); err != nil || g.By != by || g.Decision != "go" {
				t.Errorf("gate.json %s", data)
			}
		})
	}
}

func TestARefusedTriageCanBeResubmitted(t *testing.T) {
	var reason string
	k := startTriage(t, func(w *Wiring, text string) {
		var ok bool
		ok, reason, _ = w.submitTriage(core.Triage{Phases: []core.PhaseVerdict{{Phase: "1", Status: core.VerdictBuild}, {Phase: "3", Status: core.VerdictBuild}}})
		if ok {
			t.Error("an incomplete triage was accepted")
		}
		submit(t, w, allBuild(text))
		gate(t, w, core.GateDecision{Decision: core.GateGo, MaintainerSaid: "go"})
	})

	code := k.run()

	if code != 0 || len(k.land.landed) != 3 {
		t.Fatalf("exit %d landed %v", code, k.land.landed)
	}
	if reason != "phase 2 has no verdict" {
		t.Errorf("reason %q", reason)
	}
}

func TestAGateWithoutTheMaintainersWordsIsRefused(t *testing.T) {
	var reason string
	k := startTriage(t, func(w *Wiring, text string) {
		submit(t, w, allBuild(text))
		_, reason, _ = w.submitGate(core.GateDecision{Decision: core.GateGo})
		gate(t, w, core.GateDecision{Decision: core.GateGo, MaintainerSaid: "go"})
	})

	if code := k.run(); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(reason, "maintainer_said is empty") {
		t.Errorf("reason %q", reason)
	}
}

func TestTriageTimeoutHaltsWithExit4(t *testing.T) {
	k := startTriage(t, func(w *Wiring, text string) {})
	k.w.Config.Watchdog.TriageTimeout = 200 * time.Millisecond

	code := k.run()

	if code != 4 || !strings.Contains(k.f.err.String(), "the watchdog did not finish triage within 200ms; r-loop resume triages again") {
		t.Fatalf("exit %d: %s", code, k.f.err)
	}
	if run := k.f.load(k.w.Loop.RunID); run.Status != core.RunHalted || len(k.land.landed) != 0 {
		t.Errorf("status %s landed %v", run.Status, k.land.landed)
	}
	if ok, reason, _ := k.w.submitTriage(core.Triage{}); ok || reason != "no triage is open" {
		t.Errorf("late submit %v %q", ok, reason)
	}
}

func TestAGoneWatchdogDuringTriageExits5(t *testing.T) {
	k := startTriage(t, func(w *Wiring, text string) {})
	k.w.Dog.Host = &dogHost{blocked: "triage ", state: core.AgentGone}

	code := k.run()

	if code != 5 || !strings.Contains(k.f.err.String(), "the watchdog is gone during triage") || len(k.land.landed) != 0 {
		t.Fatalf("exit %d landed %v: %s", code, k.land.landed, k.f.err)
	}
}

type tickingLander struct {
	*landRecorder
	todo    string
	mu      sync.Mutex
	members [][]string
}

func (l *tickingLander) Land(ctx context.Context, ph core.Phase) (core.Landing, error) {
	if err := (plan.Reader{}).Tick(l.todo, ph); err != nil {
		return core.Landing{}, err
	}
	l.mu.Lock()
	l.members = append(l.members, ph.Members)
	l.mu.Unlock()
	return l.landRecorder.Land(ctx, ph)
}

func groupTwoItems(w *Wiring, text string) {
	w.submitTriage(core.Triage{
		Items:  []core.ItemVerdict{fixItem("1"), fixItem("2"), fixItem("3")},
		Groups: []core.Group{{ID: "g1", Items: []string{"2", "1"}, Subsystem: "parser", Rationale: "same function"}, {ID: "g2", Items: []string{"3"}, Subsystem: "cli"}},
	})
	if asksTheMaintainer(text) {
		w.submitGate(core.GateDecision{Decision: core.GateGo, MaintainerSaid: "go"})
	}
}

func backlogRun(t *testing.T, sim *simHost) (*fixture, *Wiring, *tickingLander) {
	t.Helper()
	f := newResumeFixture(t, noReviewConfig)
	f.write("docs/topic/todo.md", backlogTodo)
	f.commit()
	w, err := f.preflight(f.todo, "--plain")
	if err != nil {
		t.Fatal(err)
	}
	lander := &tickingLander{landRecorder: f.sim(w, sim), todo: f.todo}
	w.Loop.Lander = lander
	w.Loop.Sessions.ItemGates = false
	w.Dog.Host = &dogHost{triage: func(text string) { groupTwoItems(w, text) }}
	return f, w, lander
}

func TestABacklogGroupRunsAsOnePhase(t *testing.T) {
	f, w, lander := backlogRun(t, newSim())

	code := w.Execute(core.RunOptions{})

	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, f.out, f.err)
	}
	if !slices.Equal(lander.landed, []string{"1", "3"}) || !slices.Equal(lander.members[0], []string{"1", "2"}) {
		t.Fatalf("landed %v members %v", lander.landed, lander.members)
	}
	data, _ := os.ReadFile(f.todo)
	for _, want := range []string{"- [x] [#1] first", "- [x] [#2] second", "- [x] [#3] third"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%q missing:\n%s", want, data)
		}
	}
	run := f.load(w.Loop.RunID)
	if got := recordedGroups(run); len(got) != 2 || !slices.Equal(got[0].Items, []string{"2", "1"}) {
		t.Errorf("groups %+v", got)
	}
	if got := recordedRunList(run); !slices.Equal(got, []string{"1", "3"}) {
		t.Errorf("run list %v", got)
	}
	if !strings.Contains(f.out.String(), "run list: 1 (items 1, 2), 3\n") {
		t.Errorf("plain face:\n%s", f.out)
	}
}

func TestResumeRebuildsGroupsWithoutTriaging(t *testing.T) {
	sim := newSim()
	sim.fail["rloop-p1-implement"] = true
	f, w, lander := backlogRun(t, sim)
	if code := w.Execute(core.RunOptions{}); code != 1 || !slices.Equal(lander.landed, []string{"3"}) {
		t.Fatalf("first run exit %d landed %v\n%s", code, lander.landed, f.err)
	}
	f.commit()

	resumed, opts, err := PrepareResume([]string{"--plain"}, f.env)
	if err != nil {
		t.Fatal(err)
	}
	again := &tickingLander{landRecorder: f.sim(resumed, newSim()), todo: f.todo}
	resumed.Loop.Lander = again
	resumed.Loop.Sessions.ItemGates = false
	code := resumed.Execute(opts)

	if code != 0 {
		t.Fatalf("resume exit %d\n%s%s", code, f.out, f.err)
	}
	if !slices.Equal(again.landed, []string{"1"}) || !slices.Equal(again.members[0], []string{"1", "2"}) {
		t.Errorf("landed %v members %v", again.landed, again.members)
	}
	run := f.load(w.Loop.RunID)
	if got := stepEvents(run, "triage-start"); len(got) != 1 {
		t.Errorf("triage-start %d, want 1", len(got))
	}
	if lists := stepEvents(run, "run-list"); len(lists) != 2 || lists[1].Fields["groups"] != lists[0].Fields["groups"] {
		t.Errorf("run lists %+v", lists)
	}
}

func TestStatusShowsAGroupAsOneRow(t *testing.T) {
	f := newFixture(t)
	f.seedRun(
		ev(t0, "run-list", 0, "", map[string]string{"phases": "1,3", "groups": `[{"group_id":"g1","items":["1","2"],"subsystem":"parser"}]`}),
		core.Record{Kind: core.RecordRun, Run: core.RunHalted},
	)
	f.write("docs/topic/todo.md", backlogTodo)

	f.main("status", "--plain")

	out := f.out.String()
	if !strings.Contains(out, "phase 1 unticked\nphase 3 unticked\n") || strings.Contains(out, "phase 2 ") {
		t.Errorf("status:\n%s", out)
	}
}
