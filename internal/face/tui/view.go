package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"r-loop/internal/core"
)

const (
	margin     = 2
	railWidth  = 24
	stackBelow = 80
)

var glyphs = map[core.PhaseState]string{
	core.PhaseUnticked:    "·",
	core.PhasePlanned:     "p",
	core.PhaseImplemented: "i",
	core.PhaseLanded:      "✓",
	core.PhaseBlocked:     "×",
}

func (m Model) View() string {
	th := m.theme
	w := m.Width - 2*margin
	lines := []string{m.header(w), ""}
	rail := m.rail()
	if m.Width < stackBelow {
		lines = append(lines, rail...)
		lines = append(lines, "")
		lines = append(lines, m.panel(w)...)
	} else {
		sep := "  " + th.Border.Render("│") + "  "
		pw := w - railWidth - lipgloss.Width(sep)
		panel := m.panel(pw)
		for i := range max(len(rail), len(panel)) {
			left, right := strings.Repeat(" ", railWidth), ""
			if i < len(rail) {
				left = rail[i]
			}
			if i < len(panel) {
				right = panel[i]
			}
			lines = append(lines, strings.TrimRight(left+sep+right, " "))
		}
	}
	lines = append(lines, "")
	lines = append(lines, m.footer(w)...)
	pad := strings.Repeat(" ", margin)
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) header(w int) string {
	th := m.theme
	left := fmt.Sprintf("r-loop  %s  %s", m.RunID, m.Todo)
	right := fmt.Sprintf("started %s · %s", m.Started.Format("15:04"), m.clock().Sub(m.Started).Truncate(time.Second))
	dog, dogStyle := "", th.Header
	switch {
	case m.DogGone:
		dog, dogStyle = "watchdog gone", th.HeaderFailed
	case m.DogWaiting && m.Status == "":
		dog, dogStyle = "watchdog waiting for you", th.HeaderWaiting
	case m.RunID != "" && m.Status == "":
		dog = "watchdog live"
	}
	if dog != "" {
		right = " · " + right
		if w-lipgloss.Width(left)-lipgloss.Width(dog+right) < 2 {
			right = ""
		}
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(dog+right)
	if gap < 2 {
		return fill(th.Header, left, w)
	}
	return th.Header.Render(left+strings.Repeat(" ", gap)) + dogStyle.Render(dog) + th.Header.Render(right)
}

func (m Model) rail() []string {
	th := m.theme
	lines := []string{fill(th.Label, "PHASES", railWidth)}
	for _, r := range m.Phases {
		style := th.Text
		switch {
		case r.ID == m.Current:
			style = th.Live
		case r.State == core.PhaseBlocked:
			style = th.Failed
		case r.State == core.PhaseLanded:
			style = th.Landed
		case r.State == core.PhaseUnticked:
			style = th.Idle
		}
		lines = append(lines, fill(style, fmt.Sprintf("%s %2s %s", glyphs[r.State], r.ID, r.Title), railWidth))
	}
	return lines
}

func (m Model) panel(w int) []string {
	th := m.theme
	var lines []string
	add := func(style lipgloss.Style, s string) { lines = append(lines, style.Render(ansi.Truncate(s, w, "…"))) }
	if s := m.Live; s != nil {
		add(th.Text, fmt.Sprintf("PHASE %s · %s", s.Phase, s.Label()))
		if len(m.Steps) > 0 {
			lines = append(lines, ansi.Truncate(th.Text.Render(fmt.Sprintf("%-10s ", "steps"))+m.pipeline(s), w, "…"))
		}
		provider := s.Provider
		for _, part := range []string{s.Model, s.Effort} {
			if part != "" {
				provider += " · " + part
			}
		}
		add(th.Text, field("provider", provider))
		add(th.Text, field("session", s.Workspace))
		add(th.Text, field("state", s.State))
		if q := m.waiting(s); q != "" {
			add(th.Text, field("waiting", q))
		}
		add(th.Text, field("started", s.Started.Format("15:04:05")+"   elapsed "+s.Elapsed(m.clock()).String()))
		if s.Ended.IsZero() {
			backstop := "paused"
			if left, paused := s.Remaining(m.clock()); !paused {
				backstop = left.Truncate(time.Second).String() + " left"
			}
			add(th.Text, field("backstop", backstop))
		}
	} else {
		add(th.Label, "no step running")
	}
	lines = append(lines, "")
	add(th.Label, "EVENTS")
	if len(m.Feed) == 0 {
		add(th.Idle, "none")
	}
	for _, e := range m.Feed {
		switch e.Tone {
		case toneError:
			add(th.Failed, "!  "+e.Text)
		case toneWarn:
			add(th.Warn, "!  "+e.Text)
		default:
			add(th.Idle, "   "+e.Text)
		}
	}
	return lines
}

func (m Model) pipeline(s *Step) string {
	th := m.theme
	parts := make([]string, 0, len(m.Steps))
	for _, kind := range m.Steps {
		switch state := m.done[stepID{s.Phase, kind}]; {
		case kind == s.Kind:
			parts = append(parts, th.Current.Render(kind))
		case state == string(core.StepOK):
			parts = append(parts, th.Landed.Render(kind+" ✓"))
		case state == string(core.StepFailed):
			parts = append(parts, th.Failed.Render(kind+" ×"))
		default:
			parts = append(parts, th.Idle.Render(kind))
		}
	}
	return strings.Join(parts, th.Idle.Render(" › "))
}

func (m Model) waiting(s *Step) string {
	var open []Question
	for _, q := range m.Questions {
		if q.Phase == s.Phase && q.Step == s.Kind {
			open = append(open, q)
		}
	}
	if len(open) == 0 {
		return ""
	}
	text := fmt.Sprintf("watchdog · %s · %s", open[0].ID, m.clock().Sub(open[0].At).Truncate(time.Second))
	if len(open) > 1 {
		text += fmt.Sprintf(" (+%d)", len(open)-1)
	}
	return text
}

func (m Model) footer(w int) []string {
	th := m.theme
	switch m.Status {
	case "":
		if m.Notice != "" {
			return []string{fill(th.Status, m.Notice, w)}
		}
		return nil
	case "halted":
		banner := "halted"
		if m.Blocked != "" {
			banner += " · blocked " + m.Blocked
		}
		if m.Resume != "" {
			banner += " · resume: " + m.Resume
		}
		return []string{fill(th.Halt, banner, w), fill(th.Status, m.report()+"q quit", w)}
	default:
		return []string{fill(th.Status, m.Status+" · "+m.report()+"q quit", w)}
	}
}

func (m Model) report() string {
	if m.Report == "" {
		return ""
	}
	return "report: " + m.Report + " · "
}

func field(label, value string) string {
	return fmt.Sprintf("%-10s %s", label, value)
}

func fill(style lipgloss.Style, s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return style.Render(s + strings.Repeat(" ", max(0, w-lipgloss.Width(s))))
}
