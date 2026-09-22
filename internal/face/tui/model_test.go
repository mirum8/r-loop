package tui

import (
	"bytes"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"r-loop/internal/core"
	"r-loop/internal/face/plain"
)

var t0 = time.Date(2026, 9, 18, 14, 0, 0, 0, time.Local)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func plan() []core.Phase {
	return []core.Phase{
		{Number: 1, Title: "scaffold"},
		{Number: 2, Title: "config reader"},
		{Number: 3, Title: "state store"},
		{Number: 4, Title: "session manager"},
	}
}

func step(min, phase int, kind, state, provider, model, effort, ws string) core.Event {
	return core.Event{At: at(min), Kind: "step", Phase: phase, Step: kind, Fields: map[string]string{
		"state": state, "attempt": "1", "provider": provider, "model": model, "effort": effort,
		"backstop": "4h0m0s", "rounds": "2", "reason": "", "workspace": ws,
	}}
}

func phaseState(min, phase int, state string) core.Event {
	return core.Event{At: at(min), Kind: "phase-state", Phase: phase, Fields: map[string]string{"phase": strconv.Itoa(phase), "state": state}}
}

func recorded() []core.Event {
	return []core.Event{
		{At: at(0), Kind: "phase-start", Phase: 1, Fields: map[string]string{"phase": "1", "title": "scaffold"}},
		step(0, 1, "plan", "running", "claude", "opus", "high", "ws-1"),
		step(5, 1, "plan", "ok", "claude", "opus", "high", "ws-1"),
		phaseState(5, 1, "planned"),
		step(6, 1, "implement", "running", "codex", "gpt-5", "medium", "ws-2"),
		step(20, 1, "implement", "ok", "codex", "gpt-5", "medium", "ws-2"),
		phaseState(20, 1, "implemented"),
		phaseState(21, 1, "landed"),
		{At: at(21), Kind: "landed", Phase: 1, Fields: map[string]string{"phase": "1", "merge": "abc", "gateSkipped": "false"}},
		{At: at(22), Kind: "phase-start", Phase: 2, Fields: map[string]string{"phase": "2", "title": "config reader"}},
		step(22, 2, "plan", "running", "claude", "opus", "high", "ws-3"),
		{At: at(23), Kind: "question", Phase: 2, Step: "plan", Fields: map[string]string{"id": "q1", "text": "Which config format?"}},
		{At: at(25), Kind: "human", Phase: 2, Step: "plan", Fields: map[string]string{"what": "answer", "id": "q1"}},
		step(30, 2, "plan", "ok", "claude", "opus", "high", "ws-3"),
		phaseState(30, 2, "planned"),
		step(31, 2, "implement", "queued", "codex", "gpt-5", "medium", ""),
		step(31, 2, "implement", "spawned", "codex", "gpt-5", "medium", "ws-4"),
		step(31, 2, "implement", "running", "codex", "gpt-5", "medium", "ws-4"),
		{At: at(35), Kind: "warning", Phase: 2, Step: "implement", Fields: map[string]string{"reason": "diff is large"}},
		{At: at(40), Kind: "question", Phase: 2, Step: "implement", Fields: map[string]string{"id": "q2", "text": "Which port?"}},
		step(40, 2, "implement", "waiting-input", "codex", "gpt-5", "medium", "ws-4"),
	}
}

func newModel(events []core.Event) Model {
	m := NewModel(Header{RunID: "20260918-140000", Todo: "docs/x/todo.md", Started: t0}, plan(), NewTheme(lipgloss.NewRenderer(io.Discard), false))
	for _, ev := range events {
		m = m.Apply(ev)
	}
	return m
}

type plainView struct {
	states map[int]string
	live   string
}

func readPlain(out string) plainView {
	v := plainView{states: map[int]string{}}
	stateRe := regexp.MustCompile(`phase-state  phase=(\d+) state=(\S+)`)
	stepRe := regexp.MustCompile(`^\S+  phase (\d+)  (\S+)  (\S+)  (\S+)`)
	for _, line := range strings.Split(out, "\n") {
		if m := stateRe.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[1])
			v.states[n] = m[2]
		} else if m := stepRe.FindStringSubmatch(line); m != nil {
			v.live = strings.Join(m[1:], " ")
		}
	}
	return v
}

func TestPlainFaceAndTUIModelShowTheSameRun(t *testing.T) {
	var out bytes.Buffer
	pf := &plain.Face{Out: &out}
	for _, ev := range recorded() {
		pf.Emit(ev)
	}
	want := readPlain(out.String())

	m := newModel(recorded())

	for _, row := range m.Phases {
		if got, ok := want.states[row.Number]; ok && string(row.State) != got {
			t.Errorf("phase %d: tui %s, plain %s", row.Number, row.State, got)
		}
		if _, ok := want.states[row.Number]; !ok && row.State != core.PhaseUnticked {
			t.Errorf("phase %d: tui %s, plain shows no state", row.Number, row.State)
		}
	}
	if len(want.states) != 2 {
		t.Fatalf("plain states %v", want.states)
	}
	live := strings.Join([]string{strconv.Itoa(m.Live.Phase), m.Live.Kind, m.Live.State, m.Live.Provider}, " ")
	if live != want.live || live != "2 implement waiting-input codex" {
		t.Errorf("live: tui %q, plain %q", live, want.live)
	}
	if view := m.View(); strings.Contains(view, "Which port?") || strings.Contains(view, "QUESTIONS") {
		t.Errorf("a question is shown in the TUI:\n%s", view)
	}
}

func TestLiveStepKeepsProviderModelEffortWorkspaceAndStart(t *testing.T) {
	m := newModel(recorded())

	l := m.Live
	if l.Model != "gpt-5" || l.Effort != "medium" || l.Workspace != "ws-4" || !l.Started.Equal(at(31)) || l.Backstop != 4*time.Hour {
		t.Fatalf("live %+v", l)
	}
}

func TestBackstopCountsDownAndPausesWhileAQuestionIsOpen(t *testing.T) {
	events := recorded()
	m := newModel(events[:len(events)-1])
	if left, paused := m.Live.Remaining(at(41)); paused || left != 4*time.Hour-10*time.Minute {
		t.Fatalf("left %v paused %v", left, paused)
	}

	m = m.Apply(events[len(events)-1])
	if _, paused := m.Live.Remaining(at(45)); !paused {
		t.Fatal("not paused while waiting for input")
	}

	m = m.Apply(step(50, 2, "implement", "running", "codex", "gpt-5", "medium", "ws-4"))
	if left, paused := m.Live.Remaining(at(51)); paused || left != 4*time.Hour-10*time.Minute {
		t.Fatalf("after resume left %v paused %v", left, paused)
	}
}

func TestAReviewRoundNamesTheRoundAndPausesTheBackstop(t *testing.T) {
	m := newModel(recorded())
	ev := step(60, 2, "implement", "running", "codex", "gpt-5", "medium", "ws-4")
	ev.Fields["round"] = "1"

	m = m.Apply(ev)

	if got := m.Live.Label(); got != "implement · review r1/2" {
		t.Fatalf("label %q", got)
	}
	if _, paused := m.Live.Remaining(at(70)); !paused {
		t.Fatal("backstop runs during review")
	}
}

func TestOnlyTheLastFiveWarningsAreKept(t *testing.T) {
	var events []core.Event
	for i := 1; i <= 7; i++ {
		events = append(events, core.Event{At: at(i), Kind: "warning", Fields: map[string]string{"reason": "w" + strconv.Itoa(i)}})
	}

	m := newModel(events)

	if len(m.Warnings) != 5 || !strings.HasSuffix(m.Warnings[0].Text, "w3") || !strings.HasSuffix(m.Warnings[4].Text, "w7") {
		t.Fatalf("warnings %+v", m.Warnings)
	}
}

func TestBlockedPhaseAndSkippedDependents(t *testing.T) {
	m := newModel([]core.Event{
		{At: at(1), Kind: "phase-blocked", Phase: 2, Step: "implement", Fields: map[string]string{"phase": "2", "reason": "backstop"}},
	})

	if m.Phases[1].State != core.PhaseBlocked {
		t.Fatalf("phase 2 %s", m.Phases[1].State)
	}
}

func TestElapsedTicksEverySecond(t *testing.T) {
	m := newModel(recorded())

	next, cmd := m.Update(tickMsg(at(33)))

	if next.(Model).Now != at(33) || cmd == nil {
		t.Fatalf("now %v cmd %v", next.(Model).Now, cmd)
	}
	if !strings.Contains(next.(Model).View(), "elapsed 2m0s") {
		t.Fatalf("view:\n%s", next.(Model).View())
	}
}

func TestCtrlCDuringALiveStepAsksBeforeStopping(t *testing.T) {
	m := newModel(recorded())
	aborted := 0
	m.abort = func() error { aborted++; return nil }

	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if cmd != nil || aborted != 0 {
		t.Fatalf("ctrl+c alone quit or aborted: cmd=%v aborted=%d", cmd, aborted)
	}
	if !strings.Contains(next.(Model).View(), "stop the run?") {
		t.Fatalf("view:\n%s", next.(Model).View())
	}
	next, cmd = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd != nil || aborted != 1 {
		t.Fatalf("y: cmd=%v aborted=%d", cmd, aborted)
	}
	if !strings.Contains(next.(Model).View(), "abort requested") {
		t.Fatalf("view:\n%s", next.(Model).View())
	}
	if _, cmd := next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); cmd != nil {
		t.Fatal("q quit a live run")
	}
}

func TestAnyKeyButYCancelsTheStop(t *testing.T) {
	m := newModel(recorded())
	aborted := 0
	m.abort = func() error { aborted++; return nil }

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	next, _ = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})

	if aborted != 0 || strings.Contains(next.(Model).View(), "stop the run?") {
		t.Fatalf("aborted=%d view:\n%s", aborted, next.(Model).View())
	}
}

func TestStoppingBeforeTheRunStartsSaysSo(t *testing.T) {
	m := NewModel(Header{Todo: "docs/x/todo.md", Started: t0}, plan(), NewTheme(lipgloss.NewRenderer(io.Discard), false))

	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	next, _ = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	if !strings.Contains(next.(Model).View(), "stopping before the run starts") {
		t.Fatalf("view:\n%s", next.(Model).View())
	}
}

func TestAHaltedRunStaysOnScreenWithReportAndResumeUntilQ(t *testing.T) {
	m := newModel(recorded())
	m.Report = "/repo/.r-loop/runs/r1/report.md"

	m = m.Apply(core.Event{At: at(90), Kind: "halt", Fields: map[string]string{"blocked": "2", "resume": "r-loop resume"}})

	view := m.View()
	for _, want := range []string{"halted", "/repo/.r-loop/runs/r1/report.md", "r-loop resume"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil {
		t.Fatal("ctrl+c quit a halted run")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("q did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q did not quit")
	}
}

func TestAFinishedRunShowsTheReportWithoutAResumeLine(t *testing.T) {
	m := newModel(recorded())
	m.Report = "/repo/report.md"

	m = m.Apply(core.Event{At: at(90), Kind: "finished"})

	view := m.View()
	if !strings.Contains(view, "finished") || !strings.Contains(view, "/repo/report.md") || strings.Contains(view, "resume") {
		t.Fatalf("view:\n%s", view)
	}
}

func TestAFinishedRunStopsTheClocks(t *testing.T) {
	events := recorded()
	m := newModel(events[:len(events)-1])
	m = m.Apply(core.Event{At: at(90), Kind: "finished"})

	next, _ := m.Update(tickMsg(at(120)))

	view := next.(Model).View()
	for _, want := range []string{"started 14:00 · 1h30m0s", "elapsed 59m0s", "3h1m0s left"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
}

func TestFaceSendsEventsToTheProgramAndClosesOnQ(t *testing.T) {
	in, w := io.Pipe()
	var out syncBuffer
	f := &Face{In: in, Out: &out}
	f.Start(Header{RunID: "r1", Todo: "todo.md", Started: t0, Report: "/repo/report.md"}, plan(), []core.Event{phaseState(0, 1, "landed")})

	f.Emit(core.Event{At: at(1), Kind: "finished"})
	closed := make(chan struct{})
	go func() { f.Close(); close(closed) }()
	for !strings.Contains(out.String(), "finished") {
		time.Sleep(time.Millisecond)
	}
	w.Write([]byte("q"))
	<-closed

	if !strings.Contains(out.String(), "report: /repo/report.md") {
		t.Fatalf("out:\n%s", out.String())
	}
}

func TestSkippedDependentsAreBlocked(t *testing.T) {
	m := newModel([]core.Event{
		{At: at(1), Kind: "phase-skipped", Phase: 3, Fields: map[string]string{"phase": "3", "because": "2"}},
	})

	if m.Phases[2].State != core.PhaseBlocked {
		t.Fatalf("phase 3 %s", m.Phases[2].State)
	}
}

func TestAnAbortedRunIsHaltedWithTheResumeLine(t *testing.T) {
	m := newModel(recorded())

	m = m.Apply(core.Event{At: at(90), Kind: "aborted", Phase: 2, Step: "implement"})

	if m.Status != "halted" || !strings.Contains(m.View(), "resume: r-loop resume") {
		t.Fatalf("status %q view:\n%s", m.Status, m.View())
	}
}

func TestTheBackstopStartsWhenTheStepStartsRunning(t *testing.T) {
	m := newModel([]core.Event{
		step(0, 2, "implement", "queued", "codex", "", "", ""),
		step(3, 2, "implement", "spawned", "codex", "", "", "ws-1"),
		step(3, 2, "implement", "running", "codex", "", "", "ws-1"),
		step(5, 2, "implement", "stalled", "codex", "", "", "ws-1"),
		step(6, 2, "implement", "running", "codex", "", "", "ws-1"),
	})

	if !m.Live.Started.Equal(at(3)) {
		t.Fatalf("started %v", m.Live.Started)
	}
}

func TestResumeReplaysHistoryButNotTheOldEnding(t *testing.T) {
	history := append(recorded(), core.Event{At: at(90), Kind: "halt", Fields: map[string]string{"blocked": "2", "resume": "r-loop resume"}})

	m := replay(newModel(nil), history)

	if len(m.Warnings) != 1 || m.Phases[0].State != core.PhaseLanded || m.Phases[1].State != core.PhasePlanned {
		t.Fatalf("warnings %+v phases %+v", m.Warnings, m.Phases)
	}
	if m.Status != "" || m.Live != nil || m.Resume != "" {
		t.Fatalf("stale ending: status %q live %+v", m.Status, m.Live)
	}
}
