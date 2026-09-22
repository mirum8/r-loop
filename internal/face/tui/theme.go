package tui

import "github.com/charmbracelet/lipgloss"

const (
	Surface   = "#0F1115"
	Raised    = "#171A20"
	Text      = "#D6DAE0"
	Dim       = "#8A929E"
	Primary   = "#6E9FC4"
	Secondary = "#E0A458"
	Tertiary  = "#8FA87F"
	Error     = "#E0736A"
	Outline   = "#2E343D"
)

type Theme struct {
	Header, HeaderFailed, Label, Idle, Text, Live, Current, Landed, Failed, Warn, Halt, Status, Border lipgloss.Style
}

func NewTheme(r *lipgloss.Renderer, noColor bool) Theme {
	s := r.NewStyle
	if noColor {
		return Theme{
			Header: s(), HeaderFailed: s().Reverse(true), Label: s().Faint(true), Idle: s().Faint(true), Text: s(),
			Live: s().Bold(true), Current: s(), Landed: s().Faint(true), Failed: s().Reverse(true),
			Warn: s(), Halt: s().Reverse(true), Status: s(), Border: s().Faint(true),
		}
	}
	fg := func(c string) lipgloss.Style { return s().Foreground(lipgloss.Color(c)) }
	return Theme{
		Header:       fg(Dim).Background(lipgloss.Color(Raised)),
		HeaderFailed: fg(Error).Background(lipgloss.Color(Raised)),
		Label:        fg(Dim),
		Idle:         fg(Dim),
		Text:         fg(Text),
		Live:         fg(Primary).Bold(true),
		Current:      fg(Primary),
		Landed:       fg(Tertiary),
		Failed:       fg(Error),
		Warn:         fg(Secondary).Background(lipgloss.Color(Raised)),
		Halt:         fg(Surface).Background(lipgloss.Color(Error)),
		Status:       fg(Dim).Background(lipgloss.Color(Raised)),
		Border:       fg(Outline),
	}
}
