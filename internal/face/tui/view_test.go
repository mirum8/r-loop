package tui

import (
	"bytes"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"r-loop/internal/core"
)

var update = flag.Bool("update", false, "rewrite golden files")

func frame(t *testing.T, width, height int) string {
	t.Helper()
	m := newModel(recorded())
	m.Report = "/repo/.r-loop/runs/20260918-140000/report.md"
	next, _ := m.Update(tickMsg(at(43)))
	m = next.(Model)
	next, _ = m.Update(usageMsg(limits(42, 3)))
	m = next.(Model)
	next, _ = m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return next.(Model).View()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

func fits(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) > height {
		t.Errorf("%d lines, height %d", len(lines), height)
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w > width {
			t.Errorf("line %q is %d wide, width %d", l, w, width)
		}
	}
}

func TestFrameAt120x40(t *testing.T) {
	got := frame(t, 120, 40)

	fits(t, got, 120, 40)
	golden(t, "frame-120x40.golden", got)
}

func TestFrameAt70x30StacksTheRailAboveThePanel(t *testing.T) {
	got := frame(t, 70, 30)

	fits(t, got, 70, 30)
	golden(t, "frame-70x30.golden", got)
	lines := strings.Split(got, "\n")
	rail, panel := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "session manager") {
			rail = i
		}
		if strings.Contains(l, "PHASE 2 · implement") {
			panel = i
		}
	}
	if rail < 0 || panel < rail {
		t.Fatalf("rail row %d, panel row %d", rail, panel)
	}
}

func TestTheStepsLineStaysOneRowWithNoAmberOrBold(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	m := NewModel(Header{RunID: "r1", Todo: "todo.md", Started: t0, Steps: []string{"plan", "implement", "document"}}, plan(), NewTheme(r, false))
	m = m.Apply(core.Event{At: at(0), Kind: "phase-start", Phase: "1"})
	for i, kind := range []string{"plan", "implement", "document"} {
		m = m.Apply(step(i*2+1, 1, kind, "running", "claude", "opus", "high", "ws-x"))
		m = m.Apply(step(i*2+2, 1, kind, "ok", "claude", "opus", "high", "ws-x"))
	}
	m = m.Apply(step(7, 1, "milestone", "running", "claude", "opus", "high", "ws-m"))
	for _, tc := range []struct {
		width int
		want  string
	}{
		{80, "steps      … › document ✓ › milestone"},
		{70, "steps      plan ✓ › implement ✓ › document ✓ › milestone"},
		{50, "steps      … › document ✓ › milestone"},
	} {
		t.Run(strconv.Itoa(tc.width), func(t *testing.T) {
			m.Width, m.Height = tc.width, 40
			view := m.View()
			fits(t, view, tc.width, 40)
			lines := strings.Split(view, "\n")
			count := 0
			for i, line := range lines {
				if !strings.Contains(ansiStrip(line), "steps") {
					continue
				}
				count++
				start := strings.LastIndex(line[:strings.Index(line, "steps")], "\x1b[")
				if start < 0 {
					t.Fatalf("steps label has no style: %q", line)
				}
				stepsLine := line[start:]
				if i+1 >= len(lines) || !strings.Contains(ansiStrip(lines[i+1]), "started") {
					t.Fatalf("steps line is not followed by started:\n%s", view)
				}
				if got := strings.TrimSpace(ansiStrip(stepsLine)); got != tc.want {
					t.Errorf("steps line %q, want %q", got, tc.want)
				}
				if !regexp.MustCompile(`\x1b\[38;2;110;159;19[56]mmilestone`).MatchString(stepsLine) {
					t.Errorf("live milestone is not primary: %q", stepsLine)
				}
				if regexp.MustCompile(`\x1b\[38;2;224;16[34];88`).MatchString(stepsLine) {
					t.Errorf("steps line is amber: %q", stepsLine)
				}
				for _, sgr := range regexp.MustCompile(`\x1b\[([0-9;]*)m`).FindAllStringSubmatch(stepsLine, -1) {
					params := strings.Split(sgr[1], ";")
					for j := 0; j < len(params); j++ {
						if (params[j] == "38" || params[j] == "48") && j+1 < len(params) && params[j+1] == "2" {
							j += 4
							continue
						}
						if params[j] == "1" {
							t.Errorf("steps line is bold: %q", stepsLine)
						}
					}
				}
			}
			if count != 1 {
				t.Errorf("found %d steps lines:\n%s", count, view)
			}
		})
	}

	first := NewModel(Header{RunID: "r1", Todo: "todo.md", Started: t0, Steps: []string{"plan", "implement", "document"}}, plan(), NewTheme(r, false))
	first = first.Apply(core.Event{At: at(0), Kind: "phase-start", Phase: "1"})
	first = first.Apply(step(1, 1, "plan", "running", "claude", "opus", "high", "ws-x"))
	first.Width, first.Height = 30, 40
	view := first.View()
	fits(t, view, 30, 40)
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(ansiStrip(line), "steps") {
			if got := strings.TrimSpace(ansiStrip(line)); got != "steps      plan › impleme…" {
				t.Fatalf("narrow steps line %q", got)
			}
			return
		}
	}
	t.Fatalf("no steps line:\n%s", view)
}

func TestRailGlyphPerPhaseState(t *testing.T) {
	m := newModel([]core.Event{
		phaseState(1, 1, "landed"), phaseState(1, 2, "planned"), phaseState(1, 3, "implemented"),
		{At: at(2), Kind: "phase-blocked", Phase: "4", Fields: map[string]string{"phase": "4", "reason": "x"}},
	})
	m.Phases = append(m.Phases, Row{ID: "5", Title: "later", State: core.PhaseUnticked})

	view := m.View()

	for _, want := range []string{"✓  1 scaffold", "p  2 config reader", "i  3 state store", "×  4 session manager", "·  5 later"} {
		if !strings.Contains(view, want) {
			t.Errorf("rail lacks %q:\n%s", want, view)
		}
	}
}

func coloured(noColor bool) Model {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	if noColor {
		r.SetColorProfile(termenv.ANSI)
	}
	m := NewModel(Header{RunID: "r1", Todo: "todo.md", Started: t0}, plan(), NewTheme(r, noColor))
	return m.Apply(core.Event{At: at(1), Kind: "phase-blocked", Phase: "4", Fields: map[string]string{"phase": "4", "reason": "x"}})
}

func TestBlockedGlyphIsInTheErrorColour(t *testing.T) {
	view := coloured(false).View()

	if !regexp.MustCompile(`\x1b\[38;2;224;115;10[56]m×`).MatchString(view) {
		t.Fatalf("view %q", view)
	}
}

func TestNoColorFallsBackToBoldDimAndInverse(t *testing.T) {
	m := coloured(true)
	m = m.Apply(core.Event{At: at(2), Kind: "phase-start", Phase: "2", Fields: map[string]string{"phase": "2"}})

	view := m.View()

	if strings.Contains(view, "38;2;") || strings.Contains(view, "48;2;") {
		t.Fatalf("colour in NO_COLOR view %q", view)
	}
	for _, want := range []string{"\x1b[7m×", "\x1b[1m·  2 config reader", "\x1b[2m·  3 state store"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q: %q", want, view)
		}
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestAZeroWindowSizeKeepsTheLastUsableWidth(t *testing.T) {
	m := newModel(recorded())

	next, _ := m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})

	if !strings.Contains(next.(Model).View(), "r-loop  20260918-140000") {
		t.Fatalf("view:\n%s", next.(Model).View())
	}
}

func TestAGoneWatchdogIsInTheErrorColour(t *testing.T) {
	m := coloured(false)
	m.Width = 120
	m = m.Apply(core.Event{At: at(2), Kind: "watchdog-unreachable", Fields: map[string]string{"reason": "pane closed"}})

	view := m.View()

	if !regexp.MustCompile(`\x1b\[38;2;224;115;10[56][0-9;]*mwatchdog gone`).MatchString(view) {
		t.Fatalf("view %q", view)
	}
}

func TestAWatchdogAskingTheMaintainerIsAmberUntilItResumes(t *testing.T) {
	m := coloured(false)
	m.Width = 120
	m = m.Apply(core.Event{At: at(2), Kind: "watchdog-waiting"})

	header := strings.Split(m.View(), "\n")[0]

	if !regexp.MustCompile(`\x1b\[38;2;224;16[34];88[0-9;]*mwatchdog waiting for you`).MatchString(header) {
		t.Fatalf("header %q", header)
	}
	m = m.Apply(core.Event{At: at(3), Kind: "watchdog-resumed"})
	header = strings.Split(m.View(), "\n")[0]
	if strings.Contains(header, "waiting for you") || !strings.Contains(header, "watchdog live") {
		t.Fatalf("header after resume %q", header)
	}
	if regexp.MustCompile(`\x1b\[38;2;224;16[34];88`).MatchString(header) {
		t.Fatalf("header still amber %q", header)
	}
}

func TestErrorsAndHaltReasonsAreInTheErrorColourAndWarningsInAmber(t *testing.T) {
	m := coloured(false)
	m = m.Apply(core.Event{At: at(2), Kind: "warning", Fields: map[string]string{"reason": "round limit"}})
	m = m.Apply(core.Event{At: at(3), Kind: "error", Fields: map[string]string{"reason": "bad flag"}})
	m = m.Apply(core.Event{At: at(4), Kind: "halt", Fields: map[string]string{"reason": "invariant broken"}})

	view := m.View()

	amber := `\x1b\[38;2;224;16[34];88[0-9;]*m[^\x1b]*`
	red := `\x1b\[38;2;224;115;10[56][0-9;]*m[^\x1b]*`
	if !regexp.MustCompile(amber + `round limit`).MatchString(view) {
		t.Errorf("warning is not amber: %q", view)
	}
	for _, reason := range []string{"bad flag", "invariant broken"} {
		if !regexp.MustCompile(red + reason).MatchString(view) {
			t.Errorf("%q is not in the error colour: %q", reason, view)
		}
		if regexp.MustCompile(amber + reason).MatchString(view) {
			t.Errorf("%q is amber: %q", reason, view)
		}
	}
}

func TestTheCheckingLineIsDimAndItsWarningsAmber(t *testing.T) {
	m := coloured(false)
	m.Now = at(4)
	m = m.Apply(core.Event{At: at(2), Kind: "phase-start", Phase: "2"})
	m = m.Apply(core.Event{At: at(2), Kind: "phase-check-start", Phase: "2"})
	m = m.Apply(core.Event{At: at(3), Kind: "warning", Phase: "2", Step: "check", Fields: map[string]string{"reason": "Risk: none is too low"}})
	view := m.View()
	if !regexp.MustCompile(`\x1b\[38;2;138;146;158mphase 2 · watchdog checking the plan · 2m0s`).MatchString(view) {
		t.Fatalf("checking line is not dim:\n%s", view)
	}
	amber := `\x1b\[38;2;224;16[34];88[0-9;]*m[^\x1b]*`
	if !regexp.MustCompile(amber + `Risk: none is too low`).MatchString(view) {
		t.Fatalf("warning is not amber:\n%s", view)
	}
	m = m.Apply(core.Event{At: at(3), Kind: "phase-check", Phase: "2", Fields: map[string]string{"phase": "2", "result": "Risk: none is too low"}})
	view = m.View()
	if !regexp.MustCompile(`\x1b\[38;2;138;146;158m {3}14:03  phase 2: phase check warned`).MatchString(view) {
		t.Fatalf("result is not dim:\n%s", view)
	}
	if regexp.MustCompile(amber + `phase check warned`).MatchString(view) {
		t.Fatalf("result is amber:\n%s", view)
	}
}

func TestTheReviewBlockShowsEachRoundOnOneLine(t *testing.T) {
	m := newModel(reviewed())
	m.Now = at(50)

	for _, w := range []int{120, 70} {
		m.Width = w
		view := ansi.Strip(m.View())
		fits(t, m.View(), w, 40)
		for _, want := range []string{
			"review     r2/2 · finding 2m0s",
			"└─ ● implement",
			"   ├─ ✓ r1  claude 2 · codex 1 → fixed 2 (P1 P2) · 1 dismissed",
			"   ├─ ● r2 claude",
			"   └─ ● r2 codex",
			"backstop   paused (",
		} {
			if !strings.Contains(view, want) {
				t.Errorf("width %d: missing %q in\n%s", w, want, view)
			}
		}
		if row := lineWith(view, "● implement"); !strings.HasSuffix(row, "awaits r2") {
			t.Errorf("width %d: step row %q", w, row)
		}
	}
	m.Width = 120
	if row := lineWith(ansi.Strip(m.View()), "● r2 claude"); !strings.Contains(row, "p2-impl-rv-claude-r2") || !strings.HasSuffix(row, "reviewing") {
		t.Errorf("reviewer row %q", row)
	}
}

func TestAFailedReviewerIsInTheErrorColour(t *testing.T) {
	events := append(reviewed(), reviewEvent(50, "review-find", map[string]string{"round": "2", "reviewer": "codex", "state": "failed", "findings": "0"}))
	m := newModel(events)
	m.Now, m.Width = at(50), 120

	if row := lineWith(m.View(), "r2 codex"); !strings.Contains(row, m.theme.Failed.Render("×")) || !strings.Contains(row, m.theme.Failed.Render("failed")) {
		t.Fatalf("failed reviewer row %q", row)
	}
}

func TestACleanRoundSaysClean(t *testing.T) {
	events := append(reviewed(),
		reviewEvent(50, "review-find", map[string]string{"round": "2", "reviewer": "claude", "state": "ok", "findings": "0"}),
		reviewEvent(50, "review-find", map[string]string{"round": "2", "reviewer": "codex", "state": "ok", "findings": "0"}),
		reviewEvent(50, "review-clean", map[string]string{"round": "2"}),
	)
	m := newModel(events)
	m.Now, m.Width = at(51), 120
	view := ansi.Strip(m.View())

	for _, want := range []string{"review     r2/2 · clean", "   └─ ✓ r2  claude 0 · codex 0 → clean"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in\n%s", want, view)
		}
	}
}

func TestTheRailCountsLandedRowsAndNamesItemsForABacklog(t *testing.T) {
	m := newModel(recorded())
	if got := ansi.Strip(m.View()); !strings.Contains(got, "PHASES 1/4") {
		t.Errorf("no phase count in\n%s", got)
	}
	m.Backlog = true
	if got := ansi.Strip(m.View()); !strings.Contains(got, "ITEMS 1/4") {
		t.Errorf("no item count in\n%s", got)
	}
}

func TestTheRailListsOnlyThePhasesItWasGiven(t *testing.T) {
	m := NewModel(Header{RunID: "r"}, plan()[1:3], NewTheme(lipgloss.NewRenderer(io.Discard), false))

	got := m.rail()

	if len(got) != 3 || !strings.Contains(got[1], "config reader") || !strings.Contains(got[2], "state store") {
		t.Fatalf("rail %q", got)
	}
}

func TestAnEndedStepNoLongerShowsAnActiveReviewHalf(t *testing.T) {
	for _, state := range []string{"ok", "failed"} {
		t.Run(state, func(t *testing.T) {
			events := append(reviewed(), roundStep(50, 2, "fix"), step(52, 2, "implement", state, "codex", "gpt-5", "medium", "ws-4"))
			m := newModel(events)
			m.Now, m.Width = at(55), 120
			view := ansi.Strip(m.View())

			if !strings.HasSuffix(lineWith(view, "review     "), "review     r2/2") {
				t.Fatalf("review line %q", lineWith(view, "review     "))
			}
		})
	}
}

func lineWith(view, prefix string) string {
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, prefix) {
			return l
		}
	}
	return ""
}

func TestTheWatchdogRowIsAmberOnlyWhileItAsksYou(t *testing.T) {
	amber := regexp.MustCompile(`\x1b\[38;2;224;16[34];88m`)
	m := coloured(false)
	m.Width = 120
	m = m.Apply(core.Event{At: at(1), Kind: "phase-start", Phase: "2"})
	m = m.Apply(step(1, 2, "implement", "running", "codex", "gpt-5", "medium", "ws-4"))
	m.Now = at(2)
	if row := lineWith(m.View(), "◆"); !strings.HasSuffix(ansiStrip(row), "live") || amber.MatchString(row) {
		t.Fatalf("watchdog row %q", row)
	}

	m = m.Apply(core.Event{At: at(2), Kind: "watchdog-waiting"})
	m.Now = at(5)
	agents := m.agents(87, 0)
	if !strings.HasSuffix(ansiStrip(agents[0]), "asking you 3m0s") || !amber.MatchString(agents[0]) {
		t.Fatalf("asking row %q", agents[0])
	}
	for _, row := range agents[1:] {
		if amber.MatchString(row) {
			t.Errorf("amber outside the watchdog row: %q", row)
		}
	}

	m = m.Apply(core.Event{At: at(6), Kind: "watchdog-unreachable", Fields: map[string]string{"reason": "pane closed"}})
	if row := m.agents(87, 0)[0]; !strings.Contains(row, m.theme.Failed.Render("gone")) {
		t.Fatalf("gone row %q", row)
	}
}

func TestAPhaseCheckKeepsTheFinishedStepInTheTree(t *testing.T) {
	m := newModel([]core.Event{
		{At: at(0), Kind: "phase-start", Phase: "2"},
		step(0, 2, "plan", "running", "claude", "opus", "high", "ws-3"),
		step(8, 2, "plan", "ok", "claude", "opus", "high", "ws-3"),
		{At: at(8), Kind: "phase-check-start", Phase: "2"},
	})
	m.Now, m.Width = at(9), 120
	view := ansiStrip(m.View())

	if row := lineWith(view, "◆ watchdog"); !strings.HasSuffix(row, "checking phase 2") {
		t.Errorf("watchdog row %q", row)
	}
	if row := lineWith(view, "└─ ✓ plan"); !strings.Contains(row, "claude · opus · ws-3") || !strings.HasSuffix(row, "ok 8m0s") {
		t.Errorf("plan row %q\n%s", row, view)
	}

	m = m.Apply(step(10, 2, "implement", "running", "codex", "gpt-5", "medium", "ws-4"))
	view = ansiStrip(m.View())
	for _, want := range []string{"├─ ✓ plan", "└─ ● implement"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in\n%s", want, view)
		}
	}
}

func TestTheTreeForgetsTheStepsOfAnEarlierPhase(t *testing.T) {
	m := newModel(recorded())
	m = m.Apply(core.Event{At: at(44), Kind: "phase-start", Phase: "3"})
	m = m.Apply(step(44, 3, "plan", "running", "claude", "opus", "high", "ws-7"))
	m.Width = 120

	agents := ansiStrip(strings.Join(m.agents(87, 0), "\n"))
	if strings.Contains(agents, "implement") || !strings.Contains(agents, "└─ ● plan") {
		t.Fatalf("agents\n%s", agents)
	}
}

func TestANarrowPanelDropsTheProviderColumn(t *testing.T) {
	m := newModel(reviewed())
	m.Now = at(50)

	for _, w := range []int{80, 50} {
		m.Width, m.Height = w, 40
		view := m.View()
		fits(t, view, w, 40)
		if strings.Contains(ansiStrip(view), "codex · gpt-5") {
			t.Errorf("width %d keeps the provider column:\n%s", w, ansiStrip(view))
		}
	}
}

func TestEventsAreTrimmedToFitTheHeight(t *testing.T) {
	m := newModel(reviewed())
	m.Now = at(50)

	for _, size := range [][2]int{{120, 24}, {80, 24}, {70, 30}} {
		m.Width, m.Height = size[0], size[1]
		view := m.View()
		fits(t, view, size[0], size[1])
		if !strings.Contains(view, "diff is large") {
			t.Errorf("%v dropped the newest event:\n%s", size, ansiStrip(view))
		}
	}
}

func TestTheFixHalfFoldsItsRoundWithTheOutcome(t *testing.T) {
	events := reviewed()
	next := slices.IndexFunc(events, func(ev core.Event) bool { return ev.Kind == "review-round" && ev.Fields["round"] == "2" })
	m := newModel(events[:next])
	m.Now, m.Width = at(49), 120
	view := ansiStrip(m.View())

	if row := lineWith(view, "r1  "); !strings.Contains(row, "└─ ✓ r1  claude 2 · codex 1 → fixed 2 (P1 P2) · 1 dismissed") {
		t.Fatalf("fix-half round row %q\n%s", row, view)
	}
	if row := lineWith(view, "● implement"); !strings.HasSuffix(row, "fixing r1") {
		t.Errorf("step row %q", row)
	}
}

func TestALongTreeFoldsItsEarlierStepsToFitTheHeight(t *testing.T) {
	events := []core.Event{{At: at(0), Kind: "phase-start", Phase: "2"}}
	for a := 1; a <= 14; a++ {
		ev := step(a, 2, "implement", "failed", "codex", "gpt-5", "medium", "ws-4")
		if a == 14 {
			ev.Fields["state"] = "running"
		}
		ev.Fields["attempt"] = strconv.Itoa(a)
		events = append(events, ev)
	}
	m := newModel(events)
	m.Now = at(15)

	for _, size := range [][2]int{{120, 24}, {70, 30}} {
		m.Width, m.Height = size[0], size[1]
		view := m.View()
		fits(t, view, size[0], size[1])
		plain := ansiStrip(view)
		for _, want := range []string{"◆ watchdog", "earlier", "└─ ● implement a14", "EVENTS"} {
			if !strings.Contains(plain, want) {
				t.Errorf("%v: missing %q in\n%s", size, want, plain)
			}
		}
	}
}

func TestALongAgentNameKeepsTheStateColumnAligned(t *testing.T) {
	events := append(reviewed(), reviewEvent(50, "agent-named", map[string]string{"round": "2", "reviewer": "codex", "agent": "rloop-test-luk3r-p1-pla-ibg6f-r2", "attempt": "1"}))
	m := newModel(events)
	m.Now, m.Width = at(50), 120
	view := ansiStrip(m.View())

	codex, claude := lineWith(view, "● r2 codex"), lineWith(view, "● r2 claude")
	if !strings.Contains(codex, "rloop-test-luk3r-p1-pla-… reviewing") || lipgloss.Width(codex[:strings.Index(codex, "reviewing")]) != lipgloss.Width(claude[:strings.Index(claude, "reviewing")]) {
		t.Fatalf("rows not aligned:\n%s\n%s", claude, codex)
	}
}

func TestTheHeadingNamesTheStepAndLeavesTheRoundToTheReviewLine(t *testing.T) {
	events := append(reviewed(), roundStep(50, 2, "fix"))
	events[len(events)-1].Fields["attempt"] = "1"
	m := newModel(events)
	m.Now, m.Width = at(51), 120
	view := ansiStrip(m.View())

	if row := strings.TrimSpace(lineWith(view, "PHASE 2")); !strings.HasSuffix(row, "│  PHASE 2 · implement") {
		t.Errorf("heading %q", row)
	}
	if row := lineWith(view, "review     "); !strings.Contains(row, "review     r2/2 · fixing") {
		t.Errorf("review line %q", row)
	}
}
