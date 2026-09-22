package tui

import (
	"bytes"
	"flag"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

func TestRailGlyphPerPhaseState(t *testing.T) {
	m := newModel([]core.Event{
		phaseState(1, 1, "landed"), phaseState(1, 2, "planned"), phaseState(1, 3, "implemented"),
		{At: at(2), Kind: "phase-blocked", Phase: 4, Fields: map[string]string{"phase": "4", "reason": "x"}},
	})
	m.Phases = append(m.Phases, Row{Number: 5, Title: "later", State: core.PhaseUnticked})

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
	return m.Apply(core.Event{At: at(1), Kind: "phase-blocked", Phase: 4, Fields: map[string]string{"phase": "4", "reason": "x"}})
}

func TestBlockedGlyphIsInTheErrorColour(t *testing.T) {
	view := coloured(false).View()

	if !regexp.MustCompile(`\x1b\[38;2;224;115;10[56]m×`).MatchString(view) {
		t.Fatalf("view %q", view)
	}
}

func TestNoColorFallsBackToBoldDimAndInverse(t *testing.T) {
	m := coloured(true)
	m = m.Apply(core.Event{At: at(2), Kind: "phase-start", Phase: 2, Fields: map[string]string{"phase": "2"}})

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
