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
	lines := []string{fill(th.Header, m.header(w), w), ""}
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
	for _, q := range m.Questions {
		lines = append(lines, fill(th.Question, fmt.Sprintf("?  %s  phase %d %s: %s", q.ID, q.Phase, q.Kind, q.Text), w))
	}
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
	left := fmt.Sprintf("r-loop  %s  %s", m.RunID, m.Todo)
	watchdog := "off"
	if m.Watchdog {
		watchdog = "on"
	}
	right := fmt.Sprintf("started %s · %s · watchdog %s", m.Started.Format("15:04"), m.Now.Sub(m.Started).Truncate(time.Second), watchdog)
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) rail() []string {
	th := m.theme
	lines := []string{fill(th.Label, "PHASES", railWidth)}
	for _, r := range m.Phases {
		style := th.Text
		switch {
		case r.Number == m.Current:
			style = th.Live
		case r.State == core.PhaseBlocked:
			style = th.Failed
		case r.State == core.PhaseLanded:
			style = th.Landed
		case r.State == core.PhaseUnticked:
			style = th.Idle
		}
		lines = append(lines, fill(style, fmt.Sprintf("%s %2d %s", glyphs[r.State], r.Number, r.Title), railWidth))
	}
	return lines
}

func (m Model) panel(w int) []string {
	th := m.theme
	var lines []string
	add := func(style lipgloss.Style, s string) { lines = append(lines, style.Render(ansi.Truncate(s, w, "…"))) }
	if s := m.Live; s != nil {
		add(th.Text, fmt.Sprintf("PHASE %d · %s", s.Phase, s.Label()))
		provider := s.Provider
		for _, part := range []string{s.Model, s.Effort} {
			if part != "" {
				provider += " · " + part
			}
		}
		add(th.Text, field("provider", provider))
		add(th.Text, field("session", s.Workspace))
		add(th.Text, field("state", s.State))
		add(th.Text, field("started", s.Started.Format("15:04:05")+"   elapsed "+s.Elapsed(m.Now).String()))
		backstop := "paused"
		if left, paused := s.Remaining(m.Now); !paused {
			backstop = left.Truncate(time.Second).String() + " left"
		}
		add(th.Text, field("backstop", backstop))
	} else {
		add(th.Label, "no step running")
	}
	lines = append(lines, "")
	add(th.Label, "WARNINGS")
	if len(m.Warnings) == 0 {
		add(th.Idle, "none")
	}
	for _, warning := range m.Warnings {
		add(th.Warn, "!  "+warning)
	}
	return lines
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
