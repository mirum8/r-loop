package tui

import (
	"bytes"
	"errors"
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
		{ID: "1", Title: "scaffold"},
		{ID: "2", Title: "config reader"},
		{ID: "3", Title: "state store"},
		{ID: "4", Title: "session manager"},
	}
}

func step(min, phase int, kind, state, provider, model, effort, ws string) core.Event {
	return core.Event{At: at(min), Kind: "step", Phase: strconv.Itoa(phase), Step: kind, Fields: map[string]string{
		"state": state, "attempt": "1", "provider": provider, "model": model, "effort": effort,
		"backstop": "4h0m0s", "rounds": "2", "reason": "", "workspace": ws,
	}}
}

func phaseState(min, phase int, state string) core.Event {
	return core.Event{At: at(min), Kind: "phase-state", Phase: strconv.Itoa(phase), Fields: map[string]string{"phase": strconv.Itoa(phase), "state": state}}
}

func recorded() []core.Event {
	return []core.Event{
		{At: at(0), Kind: "phase-start", Phase: "1", Fields: map[string]string{"phase": "1", "title": "scaffold"}},
		step(0, 1, "plan", "running", "claude", "opus", "high", "ws-1"),
		step(5, 1, "plan", "ok", "claude", "opus", "high", "ws-1"),
		phaseState(5, 1, "planned"),
		step(6, 1, "implement", "running", "codex", "gpt-5", "medium", "ws-2"),
		step(20, 1, "implement", "ok", "codex", "gpt-5", "medium", "ws-2"),
		phaseState(20, 1, "implemented"),
		phaseState(21, 1, "landed"),
		{At: at(21), Kind: "landed", Phase: "1", Fields: map[string]string{"phase": "1", "merge": "abc", "gateSkipped": "false"}},
		{At: at(22), Kind: "phase-start", Phase: "2", Fields: map[string]string{"phase": "2", "title": "config reader"}},
		step(22, 2, "plan", "running", "claude", "opus", "high", "ws-3"),
		{At: at(23), Kind: "question", Phase: "2", Step: "plan", Fields: map[string]string{"id": "q1", "text": "Which config format?"}},
		{At: at(25), Kind: "question-answered", Phase: "2", Step: "plan", Fields: map[string]string{"id": "q1", "answer": "yaml", "by": "watchdog", "citation": "docs/spec.md:12"}},
		{At: at(25), Kind: "human", Phase: "2", Step: "plan", Fields: map[string]string{"what": "answer", "id": "q1"}},
		step(30, 2, "plan", "ok", "claude", "opus", "high", "ws-3"),
		phaseState(30, 2, "planned"),
		step(31, 2, "implement", "queued", "codex", "gpt-5", "medium", ""),
		step(31, 2, "implement", "spawned", "codex", "gpt-5", "medium", "ws-4"),
		step(31, 2, "implement", "running", "codex", "gpt-5", "medium", "ws-4"),
		{At: at(35), Kind: "warning", Phase: "2", Step: "implement", Fields: map[string]string{"reason": "diff is large"}},
		{At: at(40), Kind: "question", Phase: "2", Step: "implement", Fields: map[string]string{"id": "q2", "text": "Which port?"}},
		step(40, 2, "implement", "waiting-input", "codex", "gpt-5", "medium", "ws-4"),
	}
}

func newModel(events []core.Event) Model {
	m := NewModel(Header{RunID: "20260918-140000", Todo: "docs/x/todo.md", Started: t0, Steps: []string{"plan", "implement"}}, plan(), NewTheme(lipgloss.NewRenderer(io.Discard), false))
	for _, ev := range events {
		m = m.Apply(ev)
	}
	return m
}

func TestAPhaseCheckReplacesThePreviousStepInThePanelUntilItEnds(t *testing.T) {
	m := newModel(recorded()[:10])
	m = m.Apply(core.Event{At: at(22), Kind: "phase-check-start", Phase: "2", Fields: map[string]string{"phase": "2"}})
	if m.Live != nil {
		t.Fatalf("previous step remains live: %+v", m.Live)
	}
	next, _ := m.Update(tickMsg(at(24)))
	m = next.(Model)
	view := m.View()
	if !strings.Contains(view, "phase 2 · watchdog checking the plan · 2m0s") {
		t.Fatalf("checking line missing:\n%s", view)
	}
	for _, unwanted := range []string{"no step running", "PHASE 1", "backstop"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("view contains %q:\n%s", unwanted, view)
		}
	}
	m = m.Apply(core.Event{At: at(25), Kind: "phase-check", Phase: "2", Fields: map[string]string{"phase": "2", "result": "no disagreement"}})
	if view := m.View(); strings.Contains(view, "watchdog checking the plan") {
		t.Fatalf("check still shown:\n%s", view)
	}
}

func TestEachPhaseCheckResultIsOneDimFeedLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		ev   core.Event
		want string
		tone tone
	}{
		{"clear", core.Event{Kind: "phase-check", Fields: map[string]string{"result": "no disagreement"}}, "phase 2: phase check found no disagreement", toneDim},
		{"warned", core.Event{Kind: "phase-check", Fields: map[string]string{"result": "Files: none leaves out core; Risk: none is too low"}}, "phase 2: phase check warned", toneDim},
		{"timeout", core.Event{Kind: "phase-check-timeout", Fields: map[string]string{"reason": "herdr: timeout: no answer within 10m0s"}}, "phase 2: phase check timed out: herdr: timeout: no answer within 10m0s", toneDim},
		{"skipped reason", core.Event{Kind: "phase-check-skipped", Fields: map[string]string{"reason": "watchdog unreachable"}}, "phase 2: phase check skipped: watchdog unreachable", toneDim},
		{"skipped", core.Event{Kind: "phase-check-skipped"}, "phase 2: phase check skipped", toneDim},
		{"warning", core.Event{Kind: "warning", Step: "check", Fields: map[string]string{"reason": "Risk: none is too low"}}, "phase 2 check: Risk: none is too low", toneWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.ev.At, tc.ev.Phase = at(5), "2"
			m := newModel([]core.Event{tc.ev})
			if len(m.Feed) != 1 || !strings.HasSuffix(m.Feed[0].Text, tc.want) || m.Feed[0].Tone != tc.tone {
				t.Fatalf("feed %+v, want suffix %q and tone %v", m.Feed, tc.want, tc.tone)
			}
		})
	}
	if feed := newModel([]core.Event{{At: at(5), Kind: "phase-check-start", Phase: "2"}}).Feed; len(feed) != 0 {
		t.Fatalf("start added feed entries %+v", feed)
	}
}

func TestTheCheckingLineClearsOnAStepOnTheRunsEndAndOnResume(t *testing.T) {
	start := core.Event{At: at(22), Kind: "phase-check-start", Phase: "2", Fields: map[string]string{"phase": "2"}}
	for _, tc := range []struct {
		name string
		ev   core.Event
		want string
	}{
		{"step", step(23, 2, "plan", "running", "claude", "opus", "high", "ws-5"), "PHASE 2 · plan"},
		{"finished", core.Event{At: at(23), Kind: "finished"}, ""},
		{"halt", core.Event{At: at(23), Kind: "halt"}, ""},
		{"aborted", core.Event{At: at(23), Kind: "aborted"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel(recorded()[:10]).Apply(start).Apply(tc.ev)
			view := m.View()
			if strings.Contains(view, "watchdog checking the plan") || (tc.want != "" && !strings.Contains(view, tc.want)) {
				t.Fatalf("view:\n%s", view)
			}
		})
	}
	t.Run("resume", func(t *testing.T) {
		m := replay(newModel(nil), append(recorded()[:10], start))
		if view := m.View(); strings.Contains(view, "watchdog checking the plan") {
			t.Fatalf("view:\n%s", view)
		}
	})
}

type plainView struct {
	states map[string]string
	live   string
}

func readPlain(out string) plainView {
	v := plainView{states: map[string]string{}}
	stateRe := regexp.MustCompile(`phase-state  phase=(\d+) state=(\S+)`)
	stepRe := regexp.MustCompile(`^\S+  phase (\d+)  (\S+)  (\S+)  (\S+)`)
	for _, line := range strings.Split(out, "\n") {
		if m := stateRe.FindStringSubmatch(line); m != nil {
			v.states[m[1]] = m[2]
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
		if got, ok := want.states[row.ID]; ok && string(row.State) != got {
			t.Errorf("phase %s: tui %s, plain %s", row.ID, row.State, got)
		}
		if _, ok := want.states[row.ID]; !ok && row.State != core.PhaseUnticked {
			t.Errorf("phase %s: tui %s, plain shows no state", row.ID, row.State)
		}
	}
	if len(want.states) != 2 {
		t.Fatalf("plain states %v", want.states)
	}
	live := strings.Join([]string{m.Live.Phase, m.Live.Kind, m.Live.State, m.Live.Provider}, " ")
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

func TestOnlyTheLastSixFeedEntriesAreKept(t *testing.T) {
	var events []core.Event
	for i := 1; i <= 8; i++ {
		events = append(events, core.Event{At: at(i), Kind: "warning", Fields: map[string]string{"reason": "w" + strconv.Itoa(i)}})
	}

	m := newModel(events)

	if len(m.Feed) != 6 || !strings.HasSuffix(m.Feed[0].Text, "w3") || !strings.HasSuffix(m.Feed[5].Text, "w8") {
		t.Fatalf("feed %+v", m.Feed)
	}
}

func TestTheFeedNamesWhatHappenedInItsTone(t *testing.T) {
	cases := []struct {
		ev   core.Event
		text string
		tone tone
	}{
		{core.Event{Kind: "warning", Phase: "2", Step: "implement", Fields: map[string]string{"reason": "diff is large"}}, "phase 2 implement: diff is large", toneWarn},
		{core.Event{Kind: "error", Fields: map[string]string{"reason": "bad flag"}}, "bad flag", toneError},
		{core.Event{Kind: "watchdog-unreachable", Fields: map[string]string{"reason": "pane closed"}}, "watchdog gone: pane closed", toneError},
		{core.Event{Kind: "stalled", Phase: "2", Step: "implement", Fields: map[string]string{"workspace": "ws-4"}}, "phase 2 implement: stalled", toneError},
		{core.Event{Kind: "restart-refused", Phase: "2", Step: "implement", Fields: map[string]string{"reason": "restart limit 2 reached"}}, "restart limit 2 reached", toneError},
		{core.Event{Kind: "restart", Phase: "2", Step: "implement"}, "phase 2 implement: restarted", toneDim},
		{core.Event{Kind: "nudge", Phase: "2", Step: "implement"}, "nudged", toneDim},
		{core.Event{Kind: "landed", Phase: "1", Fields: map[string]string{"merge": "0123456789abcdef", "gateSkipped": "true"}}, "phase 1: landed 0123456 · gate skipped", toneDim},
		{core.Event{Kind: "gate-fix", Phase: "1", Step: "land", Fields: map[string]string{"round": "2"}}, "land gate fix r2", toneDim},
		{core.Event{Kind: "assumption", Phase: "1", Step: "plan", Fields: map[string]string{"text": "yaml config"}}, "assumed: yaml config", toneDim},
		{core.Event{Kind: "question", Phase: "2", Step: "implement", Fields: map[string]string{"id": "q2", "text": "Which port?"}}, "asked q2", toneDim},
		{core.Event{Kind: "question-answered", Phase: "2", Step: "implement", Fields: map[string]string{"id": "q2", "by": "maintainer"}}, "q2 answered by maintainer", toneDim},
		{core.Event{Kind: "human", Fields: map[string]string{"what": "resume"}}, "resumed", toneDim},
		{core.Event{Kind: "signal-rejected", Fields: map[string]string{"reason": "step is not running"}}, "step is not running", toneDim},
		{core.Event{Kind: "watchdog-waiting", Fields: map[string]string{"question": "Retry phase 2\n  with the helper renamed?"}}, "watchdog asks you: Retry phase 2 with the helper renamed?", toneWarn},
	}
	for _, c := range cases {
		c.ev.At = at(5)
		m := newModel([]core.Event{c.ev})
		if len(m.Feed) != 1 || !strings.HasSuffix(m.Feed[0].Text, c.text) || m.Feed[0].Tone != c.tone {
			t.Errorf("%s: feed %+v, want %q tone %d", c.ev.Kind, m.Feed, c.text, c.tone)
		}
	}
	for _, kind := range []string{"workspace-closed", "worktree-removed", "watchdog-waiting", "watchdog-resumed"} {
		if m := newModel([]core.Event{{At: at(5), Kind: kind, Phase: "1"}}); len(m.Feed) != 0 {
			t.Errorf("%s logged: %+v", kind, m.Feed)
		}
	}
	if m := newModel([]core.Event{{At: at(5), Kind: "human", Fields: map[string]string{"what": "answer", "id": "q1"}}}); len(m.Feed) != 0 {
		t.Errorf("an answer is logged twice: %+v", m.Feed)
	}
}

func TestAnOpenQuestionPointsAtTheWatchdogUntilItIsAnswered(t *testing.T) {
	m := newModel(recorded())
	m.Now = at(43)

	view := m.View()

	if !strings.Contains(view, "waiting    watchdog · q2 · 3m0s") {
		t.Fatalf("view lacks the waiting line:\n%s", view)
	}
	if strings.Contains(view, "Which port?") || strings.Contains(view, "q1 ·") {
		t.Fatalf("view shows question text or an answered question:\n%s", view)
	}
	m = m.Apply(core.Event{At: at(44), Kind: "question", Phase: "2", Step: "implement", Fields: map[string]string{"id": "q3", "text": "Which host?"}})
	if !strings.Contains(m.View(), "watchdog · q2 · 3m0s (+1)") {
		t.Fatalf("view does not count the second question:\n%s", m.View())
	}

	m = m.Apply(core.Event{At: at(45), Kind: "question-answered", Phase: "2", Step: "implement", Fields: map[string]string{"id": "q2", "by": "watchdog"}})
	m = m.Apply(core.Event{At: at(45), Kind: "question-answered", Phase: "2", Step: "implement", Fields: map[string]string{"id": "q3", "by": "maintainer"}})

	if strings.Contains(m.View(), "waiting    ") {
		t.Fatalf("answered questions still shown:\n%s", m.View())
	}
}

func TestAGoneWatchdogIsMarkedUntilResume(t *testing.T) {
	history := append(recorded(), core.Event{At: at(50), Kind: "watchdog-unreachable", Fields: map[string]string{"reason": "pane closed"}})
	m := newModel(history)

	if !m.DogGone || !strings.Contains(m.View(), "watchdog gone") {
		t.Fatalf("gone %v view:\n%s", m.DogGone, m.View())
	}

	m = replay(newModel(nil), history)
	if m.DogGone || len(m.Questions) != 0 {
		t.Fatalf("resume keeps gone=%v questions=%+v", m.DogGone, m.Questions)
	}
}

func TestAWaitingWatchdogIsNotCarriedIntoAResume(t *testing.T) {
	history := append(recorded(), core.Event{At: at(50), Kind: "watchdog-waiting"})

	if m := newModel(history); !m.DogWaiting || !strings.Contains(m.View(), "watchdog waiting for you") {
		t.Fatalf("waiting %v view:\n%s", m.DogWaiting, m.View())
	}
	if m := replay(newModel(nil), history); m.DogWaiting {
		t.Fatal("resume keeps the watchdog waiting")
	}
}

func TestAGoneWatchdogIsNoLongerWaiting(t *testing.T) {
	m := newModel(append(recorded(), core.Event{At: at(50), Kind: "watchdog-waiting"}, core.Event{At: at(51), Kind: "watchdog-unreachable", Fields: map[string]string{"reason": "pane closed"}}))

	if m.DogWaiting || !strings.Contains(m.View(), "watchdog gone") {
		t.Fatalf("waiting %v view:\n%s", m.DogWaiting, m.View())
	}
}

func TestAFinishedRunDropsTheWatchdogMarker(t *testing.T) {
	m := newModel(recorded())

	m = m.Apply(core.Event{At: at(90), Kind: "finished"})

	if header := strings.Split(m.View(), "\n")[0]; strings.Contains(header, "watchdog") {
		t.Fatalf("header %q", header)
	}
}

func TestTheStepsLineTracksDoneFailedLiveAndPendingKinds(t *testing.T) {
	m := newModel(recorded()[:11])
	if got := ansiStrip(m.pipeline(m.Live, 200)); got != "plan › implement" {
		t.Fatalf("pending %q", got)
	}

	m = newModel(recorded())
	if got := ansiStrip(m.pipeline(m.Live, 200)); got != "plan ✓ › implement" {
		t.Fatalf("pipeline %q", got)
	}

	fail := step(50, 2, "implement", "failed", "codex", "gpt-5", "medium", "ws-4")
	retry := step(51, 2, "implement", "running", "codex", "gpt-5", "medium", "ws-5")
	retry.Fields["attempt"] = "2"
	retry.Fields["round"], retry.Fields["half"] = "1", "fix"
	m = m.Apply(fail)
	if got := ansiStrip(m.pipeline(m.Live, 200)); got != "plan ✓ › implement ×" {
		t.Fatalf("after failure %q", got)
	}

	m = m.Apply(retry)
	if got := ansiStrip(m.pipeline(m.Live, 200)); got != "plan ✓ › implement" {
		t.Fatalf("after retry %q", got)
	}
	if got := m.Live.Label(); got != "implement a2 · review r1/2 fix" {
		t.Fatalf("label %q", got)
	}
}

func TestTheStepsLineShowsTheMilestoneWhileItRunsAndWhenItEnds(t *testing.T) {
	m := newModel(recorded()[:9]).Apply(step(22, 1, "milestone", "running", "claude", "opus", "high", "ws-m"))
	if got := ansiStrip(m.pipeline(m.Live, 200)); got != "plan ✓ › implement ✓ › milestone" {
		t.Fatalf("running %q", got)
	}
	if view := m.View(); !strings.Contains(view, "PHASE 1 · milestone") {
		t.Fatalf("title missing:\n%s", view)
	}
	for _, tc := range []struct{ state, want string }{
		{"ok", "plan ✓ › implement ✓ › milestone ✓"},
		{"failed", "plan ✓ › implement ✓ › milestone ×"},
	} {
		ended := m.Apply(step(23, 1, "milestone", tc.state, "claude", "opus", "high", "ws-m"))
		if got := ansiStrip(ended.pipeline(ended.Live, 200)); got != tc.want {
			t.Errorf("%s: %q", tc.state, got)
		}
	}
}

func TestTheStepsLineShowsNoMilestoneForAPhaseThatClosesNone(t *testing.T) {
	m := newModel(recorded()[:9])
	if got := ansiStrip(m.pipeline(m.Live, 200)); got != "plan ✓ › implement ✓" {
		t.Fatalf("without milestone %q", got)
	}
	m = m.Apply(step(22, 1, "milestone", "running", "claude", "opus", "high", "ws-m"))
	m = m.Apply(step(23, 1, "milestone", "ok", "claude", "opus", "high", "ws-m"))
	m = m.Apply(core.Event{At: at(24), Kind: "phase-start", Phase: "2"})
	m = m.Apply(step(24, 2, "plan", "running", "claude", "opus", "high", "ws-3"))
	if got := ansiStrip(m.pipeline(m.Live, 200)); got != "plan › implement" {
		t.Fatalf("next phase %q", got)
	}
}

func TestTheStepsLineShowsEveryKindTheTitleNames(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kinds  []string
		states []string
		title  string
		want   string
	}{
		{"gatefix running", []string{"gatefix"}, []string{"running"}, "gatefix", "plan ✓ › implement ✓ › gatefix"},
		{"gatefix ok", []string{"gatefix", "gatefix"}, []string{"running", "ok"}, "gatefix", "plan ✓ › implement ✓ › gatefix ✓"},
		{"backlog gate", []string{"gate"}, []string{"running"}, "gate", "plan ✓ › implement ✓ › gate"},
		{"milestone after gatefix", []string{"gatefix", "gatefix", "milestone"}, []string{"running", "ok", "running"}, "milestone", "plan ✓ › implement ✓ › milestone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel(recorded()[:9])
			for i, kind := range tc.kinds {
				m = m.Apply(step(22+i, 1, kind, tc.states[i], "claude", "opus", "high", "ws-x"))
			}
			if view := m.View(); !strings.Contains(view, "PHASE 1 · "+tc.title) {
				t.Fatalf("title missing:\n%s", view)
			}
			if got := ansiStrip(m.pipeline(m.Live, 200)); got != tc.want {
				t.Fatalf("pipeline %q", got)
			}
		})
	}

	m := NewModel(Header{RunID: "r1", Todo: "todo.md", Started: t0}, plan(), NewTheme(lipgloss.NewRenderer(io.Discard), false))
	m = m.Apply(step(1, 1, "milestone", "running", "claude", "opus", "high", "ws-m"))
	if view := m.View(); !strings.Contains(view, "steps      milestone") {
		t.Fatalf("empty pipeline missing milestone:\n%s", view)
	}
}

func ansiStrip(s string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(s, "")
}

func TestBlockedPhaseAndSkippedDependents(t *testing.T) {
	m := newModel([]core.Event{
		{At: at(1), Kind: "phase-blocked", Phase: "2", Step: "implement", Fields: map[string]string{"phase": "2", "reason": "backstop"}},
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

func TestARunningStepShowsBackstopCountdown(t *testing.T) {
	events := recorded()
	m := newModel(events[:len(events)-1])
	next, _ := m.Update(tickMsg(at(33)))
	if view := next.(Model).View(); !strings.Contains(view, "backstop   3h58m0s left") {
		t.Fatalf("running step lacks countdown:\n%s", view)
	}
}

func TestARunningGateFixRetryStartsFresh(t *testing.T) {
	first := step(10, 2, "gatefix", "running", "claude", "opus", "high", "ws-5")
	ended := step(15, 2, "gatefix", "ok", "claude", "opus", "high", "ws-5")
	retry := step(17, 2, "gatefix", "running", "claude", "opus", "high", "ws-6")
	retry.Fields["attempt"] = "2"
	m := newModel([]core.Event{
		{At: at(0), Kind: "phase-start", Phase: "2", Fields: map[string]string{"phase": "2"}},
		first,
		ended,
		{At: at(16), Kind: "gate-fix", Phase: "2", Step: "land", Fields: map[string]string{"phase": "2", "round": "2"}},
		retry,
	})
	next, _ := m.Update(tickMsg(at(30)))
	m = next.(Model)
	view := m.View()
	for _, want := range []string{"PHASE 2 · gatefix a2", "state      running", "elapsed 13m0s", "backstop   3h47m0s left"} {
		if !strings.Contains(view, want) {
			t.Errorf("retry view lacks %q:\n%s", want, view)
		}
	}
	if m.Live == nil || !m.Live.Started.Equal(at(17)) || !m.Live.Ended.IsZero() {
		t.Errorf("retry step has stale timing: %+v", m.Live)
	}
}

func TestAnEndedStepShowsNoBackstopCountdown(t *testing.T) {
	cases := []struct {
		kind  string
		state string
	}{
		{"plan", "ok"},
		{"implement", "failed"},
		{"milestone", "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.kind+"-"+tc.state, func(t *testing.T) {
			m := newModel([]core.Event{
				{At: at(0), Kind: "phase-start", Phase: "3", Fields: map[string]string{"phase": "3"}},
				step(0, 3, tc.kind, "running", "claude", "opus", "high", "ws-9"),
				step(4, 3, tc.kind, tc.state, "claude", "opus", "high", "ws-9"),
			})
			next, _ := m.Update(tickMsg(at(30)))
			view := next.(Model).View()
			for _, want := range []string{"PHASE 3 · " + tc.kind, "state      " + tc.state} {
				if !strings.Contains(view, want) {
					t.Errorf("view lacks %q:\n%s", want, view)
				}
			}
			for _, unwanted := range []string{"backstop", " left"} {
				if strings.Contains(view, unwanted) {
					t.Errorf("view contains %q:\n%s", unwanted, view)
				}
			}
		})
	}
}

func TestAnEndedStepsElapsedStaysEndedMinusStarted(t *testing.T) {
	m := newModel([]core.Event{
		step(31, 2, "implement", "running", "codex", "gpt-5", "medium", "ws-4"),
		step(40, 2, "implement", "ok", "codex", "gpt-5", "medium", "ws-4"),
	})
	for _, minute := range []int{60, 90} {
		next, _ := m.Update(tickMsg(at(minute)))
		m = next.(Model)
		if view := m.View(); !strings.Contains(view, "elapsed 9m0s") {
			t.Errorf("at %d, view lacks frozen elapsed:\n%s", minute, view)
		}
	}
	if got := m.Live.Elapsed(at(90)); got != 9*time.Minute {
		t.Errorf("elapsed at 90 = %v, want 9m", got)
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
	if !strings.Contains(next.(Model).View(), "ctrl+c again to quit now") {
		t.Fatalf("stop prompt omits force quit: %s", next.(Model).View())
	}
	next, cmd = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if cmd != nil || aborted != 1 {
		t.Fatalf("y: cmd=%v aborted=%d", cmd, aborted)
	}
	if !strings.Contains(next.(Model).View(), "abort requested") {
		t.Fatalf("view:\n%s", next.(Model).View())
	}
	if !strings.Contains(next.(Model).View(), "ctrl+c again to quit now") || strings.Contains(next.(Model).View(), "before its next step") {
		t.Fatalf("abort notice is inaccurate: %s", next.(Model).View())
	}
	if _, cmd := next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); cmd != nil {
		t.Fatal("q quit a live run")
	}
}

func TestConfirmingAStopAfterTheRunEndedDoesNotAbort(t *testing.T) {
	m := newModel(recorded())
	aborted := 0
	m.abort = func() error { aborted++; return nil }
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	m = next.(Model)
	if !m.stopping {
		t.Fatal("ctrl+c did not open the stop prompt")
	}
	m = m.Apply(core.Event{At: at(90), Kind: "halt", Fields: map[string]string{"resume": "r-loop resume"}})
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(Model)
	if aborted != 0 || m.stopping {
		t.Fatalf("abort calls = %d, stop prompt open = %v", aborted, m.stopping)
	}
	if m.Notice != "" {
		t.Fatalf("notice after the run ended = %q", m.Notice)
	}
	if view := m.View(); !strings.Contains(view, "halted") {
		t.Fatalf("halted banner missing:\n%s", view)
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

func TestASecondCtrlCAtTheStopPromptAbortsAndQuits(t *testing.T) {
	m := newModel(recorded())
	aborted := 0
	m.abort = func() error { aborted++; return nil }
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	_, cmd := next.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || aborted != 1 {
		t.Fatalf("second ctrl+c: command=%v abort calls=%d", cmd, aborted)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("second ctrl+c did not quit")
	}
}

func TestCtrlCAfterAnAbortWasRequestedQuitsWithoutAbortingTwice(t *testing.T) {
	m := newModel(recorded())
	aborted := 0
	m.abort = func() error { aborted++; return nil }
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	next, _ = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	_, cmd := next.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || aborted != 1 {
		t.Fatalf("ctrl+c after abort: command=%v abort calls=%d", cmd, aborted)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c after abort did not quit")
	}
}

func TestCtrlCAfterTheAbortedEventStillQuits(t *testing.T) {
	m := newModel(recorded())
	aborted := 0
	m.abort = func() error { aborted++; return nil }
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	next, _ = next.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = next.(Model).Apply(core.Event{Kind: "aborted", Phase: "2", Step: "implement"})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil || aborted != 1 {
		t.Fatalf("ctrl+c after event: command=%v abort calls=%d", cmd, aborted)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("ctrl+c after event did not quit")
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
	for _, want := range []string{"started 14:00 · 1h30m0s", "no step running"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	for _, unwanted := range []string{"PHASE 2 · implement", "elapsed", " left"} {
		if strings.Contains(view, unwanted) {
			t.Errorf("view contains %q:\n%s", unwanted, view)
		}
	}
	if next.(Model).Live != nil {
		t.Errorf("finished run still has a live step: %+v", next.(Model).Live)
	}
}

func TestAHaltedOrAbortedRunShowsNoLiveStep(t *testing.T) {
	cases := []struct {
		name string
		ev   core.Event
	}{
		{"halt", core.Event{At: at(90), Kind: "halt", Fields: map[string]string{"blocked": "2", "resume": "r-loop resume"}}},
		{"aborted", core.Event{At: at(90), Kind: "aborted", Phase: "2", Step: "implement"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := recorded()
			m := newModel(events[:len(events)-1])
			m = m.Apply(tc.ev)
			next, _ := m.Update(tickMsg(at(120)))
			view := next.(Model).View()
			for _, want := range []string{"no step running", "halted"} {
				if !strings.Contains(view, want) {
					t.Errorf("view lacks %q:\n%s", want, view)
				}
			}
			for _, unwanted := range []string{"PHASE 2 · implement", " left"} {
				if strings.Contains(view, unwanted) {
					t.Errorf("view contains %q:\n%s", unwanted, view)
				}
			}
		})
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

func TestFaceReportsARunErrorToOnExit(t *testing.T) {
	in, writer := io.Pipe()
	defer in.Close()
	var out syncBuffer
	exits := make(chan error, 1)
	f := &Face{In: in, Out: &out, OnExit: func(err error) { exits <- err }}
	f.Start(Header{RunID: "r1", Started: t0}, plan(), nil)
	writer.CloseWithError(errors.New("tty gone"))
	select {
	case err := <-exits:
		if err == nil || !strings.Contains(err.Error(), "tty gone") {
			t.Fatalf("exit error = %v", err)
		}
	case <-time.After(2 * time.Second):
		f.Stop()
		t.Fatal("Run exit was not reported")
	}
}

func TestFaceReportsItsProgramExitToOnExit(t *testing.T) {
	in, writer := io.Pipe()
	defer in.Close()
	defer writer.Close()
	var out syncBuffer
	exits := make(chan error, 2)
	f := &Face{In: in, Out: &out, OnExit: func(err error) { exits <- err }}
	f.Start(Header{RunID: "r1", Started: t0}, plan(), nil)
	f.Stop()
	select {
	case <-exits:
	case <-time.After(2 * time.Second):
		t.Fatal("program exit was not reported")
	}
	select {
	case err := <-exits:
		t.Fatalf("OnExit called twice; second error = %v", err)
	default:
	}
}

func TestConcurrentStopAndCloseWaitForProgramExit(t *testing.T) {
	in, writer := io.Pipe()
	defer in.Close()
	defer writer.Close()
	var out syncBuffer
	exiting := make(chan struct{})
	release := make(chan struct{})
	f := &Face{In: in, Out: &out, OnExit: func(error) {
		close(exiting)
		<-release
	}}
	f.Start(Header{RunID: "r1", Started: t0}, plan(), nil)
	f.Emit(core.Event{Kind: "halt", Fields: map[string]string{"reason": "SIGTERM"}})
	first := make(chan struct{})
	go func() { f.Stop(); close(first) }()
	select {
	case <-exiting:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("program did not exit")
	}
	second := make(chan struct{})
	closed := make(chan struct{})
	go func() { f.Stop(); close(second) }()
	go func() { f.Close(); close(closed) }()
	select {
	case <-second:
		t.Error("second Stop returned before program exit completed")
	case <-time.After(50 * time.Millisecond):
	}
	select {
	case <-closed:
		t.Error("Close returned before program exit completed")
	case <-time.After(50 * time.Millisecond):
	}
	if strings.Contains(out.String(), "halted: SIGTERM") {
		t.Error("Close printed the halt reason before program exit completed")
	}
	close(release)
	for name, done := range map[string]<-chan struct{}{"first Stop": first, "second Stop": second, "Close": closed} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Errorf("%s did not return", name)
		}
	}
	if !strings.Contains(out.String(), "halted: SIGTERM") {
		t.Fatalf("missing halt reason: %q", out.String())
	}
}

func TestSkippedDependentsAreBlocked(t *testing.T) {
	m := newModel([]core.Event{
		{At: at(1), Kind: "phase-skipped", Phase: "3", Fields: map[string]string{"phase": "3", "because": "2"}},
	})

	if m.Phases[2].State != core.PhaseBlocked {
		t.Fatalf("phase 3 %s", m.Phases[2].State)
	}
}

func TestAnAbortedRunIsHaltedWithTheResumeLine(t *testing.T) {
	m := newModel(recorded())

	m = m.Apply(core.Event{At: at(90), Kind: "aborted", Phase: "2", Step: "implement"})

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

	if len(m.Feed) != 5 || m.Phases[0].State != core.PhaseLanded || m.Phases[1].State != core.PhasePlanned {
		t.Fatalf("feed %+v phases %+v", m.Feed, m.Phases)
	}
	if m.Status != "" || m.Live != nil || m.Resume != "" {
		t.Fatalf("stale ending: status %q live %+v", m.Status, m.Live)
	}
}

func TestARunListWithGroupsMergesTheMemberRowsOnce(t *testing.T) {
	list := core.Event{At: at(0), Kind: "run-list", Fields: map[string]string{"phases": "1,4", "groups": `[{"group_id":"g1","items":["3","1","2"],"subsystem":"store"}]`}}

	once := newModel([]core.Event{list})
	twice := once.Apply(list)

	want := []Row{{ID: "1", Title: "store (items 1, 2, 3)", State: core.PhaseUnticked}, {ID: "4", Title: "session manager", State: core.PhaseUnticked}}
	for _, m := range []Model{once, twice} {
		if len(m.Phases) != len(want) || m.Phases[0] != want[0] || m.Phases[1] != want[1] {
			t.Fatalf("rows %+v", m.Phases)
		}
	}
}

func TestTriageIsASummaryLineInTheFeedAndMarksTheSkippedRow(t *testing.T) {
	m := newModel([]core.Event{
		{At: at(0), Kind: "triage-start", Fields: map[string]string{"kind": "backlog", "phases": "1, 2, 3, 4"}},
		{At: at(1), Kind: "triage", Fields: map[string]string{"summary": "2 groups from 4 items, 1 skipped", "table": "| Group |\n"}},
		{At: at(1), Kind: core.TriageSkipped, Phase: "2", Fields: map[string]string{"reason": "stale: fixed at a.go:3"}},
		{At: at(1), Kind: core.TriageSkipped, Phase: "9", Fields: map[string]string{"reason": "dropped by the maintainer"}},
		{At: at(9), Kind: "finished"},
	})

	var feed []string
	for _, e := range m.Feed {
		feed = append(feed, e.Text)
	}
	want := []string{"14:00  triage: watchdog verifying 4 items", "14:01  triage: 2 groups from 4 items, 1 skipped", "14:01  phase 2: skipped by triage: stale: fixed at a.go:3", "14:01  phase 9: skipped by triage: dropped by the maintainer"}
	if strings.Join(feed, "\n") != strings.Join(want, "\n") {
		t.Fatalf("feed %q", feed)
	}
	if m.Phases[1].State != core.PhaseBlocked || len(m.Phases) != len(plan()) {
		t.Errorf("rows %+v", m.Phases)
	}
	if view := m.View(); !strings.Contains(view, "×  2 ") || strings.Contains(view, "·  2 ") {
		t.Errorf("row 2 not skipped:\n%s", view)
	}
}

func TestTriageStartCountsOnePhaseInTheSingular(t *testing.T) {
	m := newModel([]core.Event{
		{At: at(0), Kind: "triage-start", Fields: map[string]string{"kind": "plan", "phases": "3"}},
		{At: at(1), Kind: "triage-start", Fields: map[string]string{"kind": "backlog", "phases": "4"}},
	})

	if got := m.Feed[0].Text + "\n" + m.Feed[1].Text; got != "14:00  triage: watchdog verifying 1 phase\n14:01  triage: watchdog verifying 1 item" {
		t.Fatalf("feed %q", got)
	}
}
