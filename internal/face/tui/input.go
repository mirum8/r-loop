package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"r-loop/internal/core"
)

type askMsg struct {
	q     core.Question
	reply chan string
}

type withdrawMsg string

var lineBreaks = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ")

func (o Open) consent() bool {
	return len(o.Options) == 2 && o.Options[0] == "yes" && o.Options[1] == "no"
}

func (o Open) where() string {
	if o.Phase == 0 {
		return o.Kind
	}
	return fmt.Sprintf("phase %d %s", o.Phase, o.Kind)
}

func (m Model) ask(msg askMsg) Model {
	q := msg.q
	if m.done[q.ID] {
		close(msg.reply)
		return m
	}
	m.Questions = append([]Open(nil), m.Questions...)
	for i := range m.Questions {
		if m.Questions[i].ID == q.ID {
			m.Questions[i].Options, m.Questions[i].reply = q.Options, msg.reply
			return m
		}
	}
	m.Questions = append(m.Questions, Open{ID: q.ID, Phase: q.Step.Phase, Kind: q.Step.Kind, Text: q.Text, Options: q.Options, reply: msg.reply})
	return m
}

func (m Model) target() int {
	for i, q := range m.Questions {
		if q.reply != nil {
			return i
		}
	}
	return -1
}

func (m *Model) input(key tea.KeyMsg) {
	i := m.target()
	if i < 0 {
		return
	}
	q := m.Questions[i]
	if m.draftFor != q.ID {
		m.Draft, m.draftFor = "", q.ID
	}
	if q.consent() {
		switch key.String() {
		case "y":
			m.submit(q, "yes")
		case "n":
			m.submit(q, "no")
		}
		return
	}
	switch key.Type {
	case tea.KeyRunes:
		m.Draft += lineBreaks.Replace(string(key.Runes))
	case tea.KeySpace:
		m.Draft += " "
	case tea.KeyBackspace:
		if r := []rune(m.Draft); len(r) > 0 {
			m.Draft = string(r[:len(r)-1])
		}
	case tea.KeyEsc:
		m.Draft = ""
	case tea.KeyEnter:
		answer := strings.TrimSpace(m.Draft)
		if answer == "" {
			return
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(q.Options) {
			answer = q.Options[n-1]
		}
		m.Draft = ""
		m.submit(q, answer)
	}
}

func (m *Model) submit(q Open, answer string) {
	q.reply <- answer
	m.drop(q.ID)
}

func (m *Model) settle(id, by string) {
	if m.done[id] {
		return
	}
	m.done[id] = true
	m.drop(id)
	if by == "" {
		return
	}
	m.Answered = append(append([]string(nil), m.Answered...), id+"  answered by "+by)
	if len(m.Answered) > keptWarnings {
		m.Answered = m.Answered[len(m.Answered)-keptWarnings:]
	}
}

func (m *Model) drop(id string) {
	var open []Open
	for _, q := range m.Questions {
		if q.ID != id {
			open = append(open, q)
		} else if q.reply != nil {
			close(q.reply)
		}
	}
	m.Questions = open
	if id == m.draftFor {
		m.Draft, m.draftFor = "", ""
	}
}

func (m Model) questions(w int) []string {
	th := m.theme
	var lines []string
	if len(m.Questions) > 0 {
		lines = append(lines, fill(th.Label, fmt.Sprintf("QUESTIONS · %d open", len(m.Questions)), w))
	}
	target := m.target()
	for i, q := range m.Questions {
		head, rest, _ := strings.Cut(q.Text, "\n")
		line := fmt.Sprintf("?  %s  %s: %s", q.ID, q.where(), head)
		if i == target && q.consent() {
			line += "  [y/n]"
		}
		lines = append(lines, fill(th.Question, line, w))
		if i != target {
			continue
		}
		if rest != "" {
			for _, l := range strings.Split(rest, "\n") {
				lines = append(lines, fill(th.Text, "   "+l, w))
			}
		}
		if q.consent() {
			continue
		}
		for n, o := range q.Options {
			lines = append(lines, fill(th.Text, fmt.Sprintf("   %d. %s", n+1, o), w))
		}
		draft := ""
		if m.draftFor == q.ID {
			draft = m.Draft
		}
		lines = append(lines, fill(th.Text, fmt.Sprintf("answer %s> %s", q.ID, draft), w))
	}
	for _, a := range m.Answered {
		lines = append(lines, fill(th.Label, a, w))
	}
	return lines
}
