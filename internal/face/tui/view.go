package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"r-loop/internal/core"
)

const (
	limitHot      = 90
	margin        = 2
	railWidth     = 24
	stackBelow    = 80
	sepWidth      = 5
	endFeed       = 3
	minReportPath = 24
	weekly        = "7d"
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
	footer := m.footer(w)
	pw, room := m.panelSize()
	if m.Width < stackBelow {
		lines = append(lines, rail...)
		lines = append(lines, "")
		lines = append(lines, m.panel(pw, room)...)
	} else {
		sep := "  " + th.Border.Render("│") + "  "
		panel := m.panel(pw, room)
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
	lines = append(lines, footer...)
	pad := strings.Repeat(" ", margin)
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}

func (m Model) panelSize() (int, int) {
	w := m.Width - 2*margin
	footer := len(m.footer(w))
	if m.Width < stackBelow {
		return w, m.Height - 5 - len(m.Phases) - footer
	}
	return w - railWidth - sepWidth, m.Height - 3 - footer
}

func (m Model) header(w int) string {
	th := m.theme
	left := fmt.Sprintf("r-loop  %s  %s", m.RunID, m.Todo)
	started := fmt.Sprintf("started %s · %s", m.Started.Format("15:04"), m.clock().Sub(m.Started).Truncate(time.Second))
	dog, dogStyle := "", th.Header
	switch {
	case m.DogGone:
		dog, dogStyle = "watchdog gone", th.HeaderFailed
	case m.DogWaiting && m.Status == "":
		dog, dogStyle = "watchdog waiting for you", th.HeaderWaiting
	case m.RunID != "" && m.Status == "":
		dog = "watchdog live"
	}
	sep := ""
	if dog != "" {
		sep = " · "
	}
	right := ""
	for _, r := range []string{m.eta() + " · " + started, started} {
		if strings.HasPrefix(r, " · ") {
			continue
		}
		if w-lipgloss.Width(left)-lipgloss.Width(dog+sep+r) >= 2 {
			right = sep + r
			break
		}
	}
	if right == "" && dog == "" {
		right = started
	}
	gap := w - lipgloss.Width(left) - lipgloss.Width(dog+right)
	if gap < 2 {
		return fill(th.Header, left, w)
	}
	limits, width := "", 0
	if right != "" {
		limits, width = m.limits(gap - 2)
	}
	return th.Header.Render(left+strings.Repeat(" ", gap-width)) + limits + dogStyle.Render(dog) + th.Header.Render(right)
}

func (m Model) limits(room int) (string, int) {
	th := m.theme
	var out strings.Builder
	width := 0
	for _, l := range m.Limits {
		if len(l.Windows) == 0 {
			continue
		}
		var b strings.Builder
		b.WriteString(th.Header.Render(l.Provider))
		plain := len(l.Provider)
		for _, win := range l.Windows {
			style, text := th.Header, fmt.Sprintf("%.0f%%", win.Percent)
			switch {
			case win.Percent >= limitHot:
				style = th.HeaderFailed
				if win.Label != weekly {
					text = win.Label + " " + text
				}
			case win.Label != weekly:
				continue
			}
			b.WriteString(th.Header.Render(" ") + style.Render(text))
			plain += 1 + lipgloss.Width(text)
		}
		if plain == len(l.Provider) {
			continue
		}
		b.WriteString(th.Header.Render(" · "))
		plain += lipgloss.Width(" · ")
		if width+plain > room {
			break
		}
		out.WriteString(b.String())
		width += plain
	}
	return out.String(), width
}

func (m Model) rail() []string {
	th := m.theme
	noun, landed := "PHASES", 0
	if m.Backlog {
		noun = "ITEMS"
	}
	for _, r := range m.Phases {
		if r.State == core.PhaseLanded {
			landed++
		}
	}
	lines := []string{fill(th.Label, fmt.Sprintf("%s %d/%d", noun, landed, len(m.Phases)), railWidth)}
	for _, r := range m.Phases {
		style := m.rowStyle(r)
		if r.ID == m.selected {
			style = th.Selected.Bold(r.ID == m.Current)
		}
		lines = append(lines, fill(style, fmt.Sprintf("%s %2s %s", glyphs[r.State], r.ID, r.Title), railWidth))
	}
	return lines
}

func (m Model) rowStyle(r Row) lipgloss.Style {
	th := m.theme
	switch {
	case r.ID == m.Current:
		return th.Live
	case r.State == core.PhaseBlocked:
		return th.Failed
	case r.State == core.PhaseLanded:
		return th.Landed
	case r.State == core.PhaseUnticked:
		return th.Idle
	}
	return th.Text
}

func (m Model) panel(w, room int) []string {
	th := m.theme
	if m.details {
		if lines := m.detailsPane(w); lines != nil {
			return lines[:min(len(lines), max(1, room))]
		}
	}
	var lines []string
	add := func(style lipgloss.Style, s string) { lines = append(lines, style.Render(ansi.Truncate(s, w, "…"))) }
	if m.Status != "" {
		report, window := m.endReport(w), m.reportWindow(room)
		from := min(m.scroll, max(0, len(report)-window))
		lines = append(lines, report[from:min(len(report), from+window)]...)
	} else if m.checking != "" {
		add(th.Label, fmt.Sprintf("phase %s · watchdog checking the plan · %s", m.checking, m.clock().Sub(m.checkFrom).Truncate(time.Second)))
	} else if m.landing != "" {
		add(th.Label, fmt.Sprintf("phase %s · landing · %s · %s", m.landing, m.landStage, m.clock().Sub(m.landFrom).Truncate(time.Second)))
		if m.landCmd != "" {
			add(th.Text, field("command", m.landCmd))
		}
		if m.landLog != "" {
			add(th.Text, field("log", m.landLog))
		}
	} else if s := m.Live; s != nil {
		add(th.Text, fmt.Sprintf("PHASE %s · %s", s.Phase, stepName(s)))
		lines = append(lines, m.summary(s.Phase, w)...)
		label := th.Text.Render(fmt.Sprintf("%-10s ", "steps"))
		lines = append(lines, ansi.Truncate(label+m.pipeline(s, w-lipgloss.Width(label)), w, "…"))
		if q := m.waiting(s); q != "" {
			add(th.Text, field("waiting", q))
		}
		if s.Round > 0 || len(s.Reviews) > 0 {
			add(th.Text, field("review", m.reviewLine(s)))
		}
		add(th.Text, field("started", s.Started.Format("15:04:05")+"   elapsed "+s.Elapsed(m.clock()).String()))
		if s.Ended.IsZero() {
			left, paused := s.Remaining(m.clock())
			backstop := left.Truncate(time.Second).String() + " left"
			if paused {
				backstop = "paused (" + backstop + ")"
			}
			add(th.Text, field("backstop", backstop))
		}
	} else if i := slices.IndexFunc(m.Questions, func(q Question) bool { return q.Blocker }); i >= 0 {
		s := &Step{Phase: m.Questions[i].Phase, Kind: m.Questions[i].Step}
		add(th.Text, fmt.Sprintf("PHASE %s · %s", s.Phase, s.Kind))
		lines = append(lines, m.summary(s.Phase, w)...)
		add(th.Text, field("waiting", m.waiting(s)))
	} else {
		add(th.Label, "no step running")
	}
	if m.narration != "" {
		if m.Status != "" && !m.details {
			lines = append(lines, "")
		}
		add(th.Idle, field("watchdog", m.narration+" · "+age(m.clock().Sub(m.narratedAt))))
	}
	if m.Live != nil || m.checking != "" {
		lines = append(lines, "")
		add(th.Label, "AGENTS")
		agents := m.agents(w, 0)
		if over := len(lines) + len(agents) + 3 - room; over > 0 && len(m.past) > 0 {
			agents = m.agents(w, min(len(m.past), over+1))
		}
		lines = append(lines, agents...)
	}
	lines = append(lines, "")
	add(th.Label, "EVENTS")
	if len(m.Feed) == 0 {
		add(th.Idle, "none")
	}
	feed := m.Feed
	if keep := max(1, room-len(lines)); len(feed) > keep {
		feed = feed[len(feed)-keep:]
	}
	for _, e := range feed {
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

func (m Model) summary(phase string, w int) []string {
	text := m.info[phase].summary
	if text == "" {
		return nil
	}
	label := m.theme.Label.Render(fmt.Sprintf("%-10s ", "summary"))
	return []string{label + m.theme.Text.Render(ansi.Truncate(text, w-lipgloss.Width(label), "…"))}
}

func age(d time.Duration) string {
	if d < time.Minute {
		return "<1m ago"
	}
	return minutes(d) + " ago"
}

const (
	nameWidth = 22
	metaWidth = 26
	metaBelow = 72
)

func (m Model) agents(w, fold int) []string {
	th := m.theme
	dog, dogState, dogText := th.Text, th.Idle, "live"
	switch {
	case m.DogGone:
		dog, dogState, dogText = th.Failed, th.Failed, "gone"
	case m.DogWaiting:
		dog, dogState, dogText = th.Asking, th.Asking, "asking you "+m.clock().Sub(m.dogSince).Truncate(time.Second).String()
	case m.checking != "":
		dogState, dogText = th.Current, "checking phase "+m.checking
	}
	steps := slices.Clone(m.past)
	if m.Live != nil {
		steps = append(steps, *m.Live)
	}
	lines := []string{m.node(w, branch(len(steps) == 0), dog, dog, dogState, "◆", "watchdog", "", dogText)}
	if fold > 0 {
		lines = append(lines, ansi.Truncate(th.Idle.Render(branch(false)+"… "+core.Plural(fold, "earlier step")), w, "…"))
	}
	for i := fold; i < len(steps); i++ {
		s := &steps[i]
		glyph, name, state, text := m.stepNode(s)
		lines = append(lines, m.node(w, branch(i == len(steps)-1), glyph, name, state, glyphFor(s), stepName(s), stepMeta(s), text))
	}
	if m.Live != nil {
		lines = append(lines, m.reviewNodes(&steps[len(steps)-1], w)...)
	}
	return lines
}

func (m Model) node(w int, prefix string, glyph, name, state lipgloss.Style, g, n, meta, text string) string {
	th := m.theme
	head := th.Idle.Render(prefix) + glyph.Render(g) + " " + name.Render(n)
	line := head + strings.Repeat(" ", max(1, nameWidth-lipgloss.Width(head)))
	if w >= metaBelow {
		meta = ansi.Truncate(meta, metaWidth-1, "…")
		line += th.Idle.Render(meta) + strings.Repeat(" ", metaWidth-lipgloss.Width(meta))
	}
	return ansi.Truncate(line+state.Render(text), w, "…")
}

func branch(last bool) string {
	if last {
		return "└─ "
	}
	return "├─ "
}

func glyphFor(s *Step) string {
	switch s.State {
	case string(core.StepOK):
		return "✓"
	case string(core.StepFailed):
		return "×"
	}
	return "●"
}

func stepName(s *Step) string {
	if s.Attempt > 1 {
		return fmt.Sprintf("%s a%d", s.Kind, s.Attempt)
	}
	return s.Kind
}

func stepMeta(s *Step) string {
	var parts []string
	for _, p := range []string{s.Provider, s.Model, s.Workspace} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

func (m Model) stepNode(s *Step) (glyph, name, state lipgloss.Style, text string) {
	th := m.theme
	switch s.State {
	case string(core.StepOK):
		return th.Landed, th.Idle, th.Idle, "ok " + s.Elapsed(m.clock()).String()
	case string(core.StepFailed):
		return th.Failed, th.Idle, th.Failed, "failed"
	case string(core.StepStalled):
		return th.Failed, th.Current, th.Failed, "stalled"
	}
	if q := m.waiting(s); q != "" {
		return th.Current, th.Current, th.Text, "waiting ⇢ " + strings.ReplaceAll(strings.TrimPrefix(q, "watchdog · "), " · ", " ")
	}
	activity := m.activity[activityKey(s.Phase, s.Kind)]
	switch {
	case s.Round > 0 && s.timedHalf == "fix" && activity != "":
		return th.Current, th.Current, th.Text, fmt.Sprintf("fixing r%d · %s", s.Round, activity)
	case s.Round > 0 && s.timedHalf == "fix":
		return th.Current, th.Current, th.Text, fmt.Sprintf("fixing r%d", s.Round)
	case s.Round > 0:
		return th.Current, th.Current, th.Idle, fmt.Sprintf("awaits r%d", s.Round)
	case s.State == string(core.StepRunning) && activity != "":
		return th.Current, th.Current, th.Text, activity
	}
	return th.Current, th.Current, th.Text, s.State
}

func reported(r Round) bool {
	return len(r.Reviewers) > 0 && !slices.ContainsFunc(r.Reviewers, func(rv Reviewer) bool { return rv.State == "" })
}

func (m Model) reviewNodes(s *Step, w int) []string {
	th := m.theme
	var lines []string
	open := len(s.Reviews) - 1
	if open >= 0 && (s.Reviews[open].Clean || !s.Ended.IsZero() || reported(s.Reviews[open])) {
		open = -1
	}
	count := len(s.Reviews)
	if open >= 0 {
		count += len(s.Reviews[open].Reviewers) - 1
	}
	n := 0
	prefix := func() string {
		n++
		return "   " + branch(n == count)
	}
	for i, r := range s.Reviews {
		if i == open {
			break
		}
		glyph, style := "✓", th.Landed
		if slices.ContainsFunc(r.Reviewers, func(rv Reviewer) bool { return rv.State != "" && rv.State != string(core.StepOK) }) {
			glyph, style = "×", th.Failed
		}
		head := th.Idle.Render(prefix()) + style.Render(glyph) + th.Text.Render(fmt.Sprintf(" r%d  ", r.N))
		lines = append(lines, ansi.Truncate(head+m.round(r), w, "…"))
	}
	if open < 0 {
		return lines
	}
	r := s.Reviews[open]
	for _, rv := range r.Reviewers {
		name := fmt.Sprintf("r%d %s", r.N, rv.ID)
		switch rv.State {
		case "":
			text := "reviewing"
			if a := m.activity[activityKey(s.Phase, s.Kind+"-rv-"+rv.ID)]; a != "" {
				text = a
			}
			lines = append(lines, m.node(w, prefix(), th.Current, th.Current, th.Current, "●", name, rv.Agent, text))
		case string(core.StepOK):
			lines = append(lines, m.node(w, prefix(), th.Landed, th.Text, th.Text, "✓", name, rv.Agent, core.Plural(rv.Findings, "finding")))
		default:
			lines = append(lines, m.node(w, prefix(), th.Failed, th.Text, th.Failed, "×", name, rv.Agent, "failed"))
		}
	}
	return lines
}

func (m Model) pipeline(s *Step, w int) string {
	th := m.theme
	kinds := m.Steps
	if !slices.Contains(kinds, s.Kind) {
		kinds = append(slices.Clone(m.Steps), s.Kind)
	}
	parts := make([]string, 0, len(kinds))
	live := 0
	for _, kind := range kinds {
		state := m.done[stepID{s.Phase, kind}]
		if state == stepSkipped {
			continue
		}
		if kind == s.Kind {
			live = len(parts)
		}
		switch {
		case kind == s.Kind && s.State != string(core.StepOK) && s.State != string(core.StepFailed):
			parts = append(parts, th.Current.Render(kind))
		case state == string(core.StepOK):
			parts = append(parts, th.Landed.Render(kind+" ✓"))
		case state == string(core.StepFailed):
			parts = append(parts, th.Failed.Render(kind+" ×"))
		default:
			parts = append(parts, th.Idle.Render(kind))
		}
	}
	sep := th.Idle.Render(" › ")
	line := strings.Join(parts, sep)
	for drop := 1; lipgloss.Width(line) > w && drop <= live; drop++ {
		line = th.Idle.Render("…") + sep + strings.Join(parts[drop:], sep)
	}
	return ansi.Truncate(line, w, "…")
}

func (m Model) reviewLine(s *Step) string {
	text := fmt.Sprintf("r%d/%d", s.Round, s.Rounds)
	last := len(s.Reviews) - 1
	switch {
	case last >= 0 && s.Reviews[last].Clean:
		return text + " · clean"
	case !s.Ended.IsZero():
		return text
	case s.timedHalf == "find":
		text += " · finding"
	case s.timedHalf == "fix":
		text += " · fixing"
	default:
		return text
	}
	return text + " " + s.HalfElapsed(m.clock()).String()
}

func (m Model) round(r Round) string {
	th := m.theme
	parts := make([]string, 0, len(r.Reviewers))
	for _, rv := range r.Reviewers {
		switch rv.State {
		case "":
			parts = append(parts, th.Text.Render(strings.TrimSpace(rv.ID+" "+rv.Agent)+" …"))
		case string(core.StepOK):
			parts = append(parts, th.Text.Render(fmt.Sprintf("%s %d", rv.ID, rv.Findings)))
		default:
			parts = append(parts, th.Failed.Render(rv.ID+" ×"))
		}
	}
	line := strings.Join(parts, th.Text.Render(" · "))
	var outcome []string
	if r.Fixed > 0 {
		outcome = append(outcome, fmt.Sprintf("fixed %d (%s)", r.Fixed, strings.Join(r.Severities, " ")))
	}
	if r.Unfixed > 0 {
		outcome = append(outcome, fmt.Sprintf("%d real unfixed", r.Unfixed))
	}
	if r.Dismissed > 0 {
		outcome = append(outcome, fmt.Sprintf("%d dismissed", r.Dismissed))
	}
	if r.Clean && len(outcome) == 0 {
		outcome = append(outcome, "clean")
	}
	if len(outcome) > 0 {
		line += th.Text.Render(" → " + strings.Join(outcome, " · "))
	}
	return line
}

func (m Model) waiting(s *Step) string {
	var open []Question
	for _, q := range m.Questions {
		if q.Phase == s.Phase && (q.Step == s.Kind || q.Blocker) {
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
		if m.Paused != "" {
			return []string{fill(th.Warn, "paused · "+m.Paused+" · tell the watchdog to continue or stop", w)}
		}
		return nil
	case "halted":
		banner := "halted"
		if m.Aborted {
			banner += " · aborted"
		}
		if m.Blocked != "" {
			banner += " · blocked " + m.Blocked
		}
		if m.Resume != "" {
			banner += " · resume: " + m.Resume
		}
		return []string{fill(th.Halt, banner, w), fill(th.Status, m.hint(nil, w), w)}
	default:
		return []string{fill(th.Status, m.hint([]string{m.Status}, w), w)}
	}
}

func (m Model) hint(status []string, w int) string {
	keys := []string{"↑↓ select", "enter details", "pgup/pgdn scroll"}
	for n := len(keys); ; n-- {
		line := strings.Join(slices.Concat(status, keys[:n], []string{"q quit"}), " · ")
		if m.Report == "" {
			if lipgloss.Width(line) <= w || n == 0 {
				return line
			}
			continue
		}
		line += " · report: "
		if full := line + m.Report; lipgloss.Width(full) <= w {
			return full
		}
		keep := w - lipgloss.Width(line) - 1
		if n > 0 && keep < minReportPath {
			continue
		}
		if keep > 0 {
			return line + "…" + ansi.TruncateLeft(m.Report, lipgloss.Width(m.Report)-keep, "")
		}
		return strings.TrimSuffix(line, " · report: ")
	}
}

func reviewStats(p phaseInfo) string {
	line := fmt.Sprintf("%s · %d real · %d fixed", core.Plural(p.rounds, "round"), p.realFindings, p.fixed)
	if p.minor > 0 {
		line += fmt.Sprintf(" · %d minor left", p.minor)
	}
	return line
}

func (m Model) reportWindow(room int) int {
	tail := 2 + max(1, min(len(m.Feed), endFeed))
	if m.narration != "" {
		tail += 2
	}
	return max(1, room-tail)
}

func (m Model) endReport(w int) []string {
	th := m.theme
	var lines []string
	add := func(label, value string) { lines = append(lines, m.labeled(label, value, th.Text, w)...) }
	landed, blocked, left := 0, 0, 0
	for _, r := range m.Phases {
		switch r.State {
		case core.PhaseLanded:
			landed++
		case core.PhaseBlocked:
			blocked++
		default:
			left++
		}
	}
	var sum phaseInfo
	for _, p := range m.info {
		sum.added, sum.deleted, sum.files = sum.added+p.added, sum.deleted+p.deleted, sum.files+p.files
		sum.rounds, sum.realFindings, sum.fixed, sum.minor = sum.rounds+p.rounds, sum.realFindings+p.realFindings, sum.fixed+p.fixed, sum.minor+p.minor
		sum.restarts, sum.nudges = sum.restarts+p.restarts, sum.nudges+p.nudges
	}
	noun := "phases"
	if m.Backlog {
		noun = "items"
	}
	add(noun, fmt.Sprintf("%d landed · %d blocked · %d left", landed, blocked, left))
	add("time", minutes(m.clock().Sub(m.Started)))
	add("diff", fmt.Sprintf("+%d −%d · %s", sum.added, sum.deleted, core.Plural(sum.files, "file")))
	add("review", reviewStats(sum))
	add("restarts", fmt.Sprintf("%d · nudges %d", sum.restarts, sum.nudges))
	add("questions", fmt.Sprintf("%d · human touches %d", m.asked, m.touches))
	var done []string
	for _, r := range m.Phases {
		p := m.info[r.ID]
		if p.sha == "" {
			continue
		}
		parts := []string{r.ID + " " + r.Title}
		if !p.started.IsZero() && !p.landedAt.Before(p.started) {
			parts = append(parts, minutes(p.landedAt.Sub(p.started)))
		}
		parts = append(parts, fmt.Sprintf("+%d −%d", p.added, p.deleted), short(p.sha))
		done = append(done, ansi.Truncate(th.Landed.Render("✓")+th.Idle.Render(" "+strings.Join(parts, " · ")), w, "…"))
	}
	if len(done) > 0 {
		lines = append(append(lines, ""), done...)
	}
	switch {
	case m.story != "":
		lines = append(append(lines, ""), m.storyLines(w)...)
	case m.storyGone != "":
		lines = append(append(lines, ""), wrap(th.Idle, m.storyGone, w)...)
	case !m.closed && !m.Aborted && !m.DogGone:
		lines = append(lines, "", th.Idle.Render(ansi.Truncate("watchdog is writing the summary…", w, "…")))
	}
	return lines
}

func (m Model) storyLines(w int) []string {
	th := m.theme
	var lines []string
	heading := false
	for _, raw := range strings.Split(m.story, "\n") {
		line := strings.TrimSpace(core.Printable(raw))
		title := strings.TrimLeft(line, "#")
		switch {
		case len(title) < len(line) && (title == "" || title[0] == ' '):
			if len(lines) > 0 && lines[len(lines)-1] != "" {
				lines = append(lines, "")
			}
			lines = append(lines, th.Label.Render(ansi.Truncate(strings.ToUpper(strings.TrimSpace(title)), w, "…")))
			heading = true
			continue
		case line == "":
			if len(lines) > 0 && lines[len(lines)-1] != "" && !heading {
				lines = append(lines, "")
			}
		default:
			lines = append(lines, wrap(th.Text, line, w)...)
		}
		heading = false
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func (m Model) detailsPane(w int) []string {
	i := slices.IndexFunc(m.Phases, func(r Row) bool { return r.ID == m.selected })
	if i < 0 {
		return nil
	}
	th := m.theme
	r, p := m.Phases[i], m.info[m.Phases[i].ID]
	lines := []string{th.Text.Render(ansi.Truncate(fmt.Sprintf("PHASE %s · %s", r.ID, r.Title), w, "…"))}
	add := func(label, value string, style lipgloss.Style) {
		lines = append(lines, m.labeled(label, value, style, w)...)
	}
	state := string(r.State)
	if r.ID == m.Current {
		state += " · live"
		if m.Live != nil {
			state += " · " + m.Live.Kind
		}
	}
	add("state", state, m.rowStyle(r))
	if p.summary != "" {
		lines = append(lines, wrap(th.Text, p.summary, w)...)
	}
	if !p.started.IsZero() {
		started := p.started.Format("15:04")
		switch {
		case !p.landedAt.IsZero() && !p.landedAt.Before(p.started):
			started += " · " + minutes(p.landedAt.Sub(p.started))
		case r.ID == m.Current:
			started += " · " + minutes(m.clock().Sub(p.started))
		}
		add("started", started, th.Text)
	}
	if p.sha != "" {
		add("commit", fmt.Sprintf("%s · +%d −%d · %s", short(p.sha), p.added, p.deleted, core.Plural(p.files, "file")), th.Text)
	}
	if p.rounds > 0 || p.realFindings > 0 {
		add("review", reviewStats(p), th.Text)
	}
	if p.restarts > 0 || p.nudges > 0 {
		add("restarts", fmt.Sprintf("%d · nudges %d", p.restarts, p.nudges), th.Text)
	}
	if p.blocker != "" && (r.State == core.PhaseBlocked || slices.ContainsFunc(m.Questions, func(q Question) bool { return q.Blocker && q.Phase == r.ID })) {
		add("blocked", p.blocker, th.Failed)
	}
	if r.State != core.PhaseUnticked || !p.started.IsZero() {
		add("plan", core.PhasePlanPath(r.ID, r.Title), th.Idle)
	}
	return lines
}

func (m Model) labeled(label, value string, style lipgloss.Style, w int) []string {
	const indent = 11
	head := m.theme.Label.Render(fmt.Sprintf("%-10s ", label))
	if w <= indent {
		return []string{ansi.Truncate(head+style.Render(value), w, "…")}
	}
	body := wrap(style, value, w-indent)
	lines := []string{head + body[0]}
	for _, l := range body[1:] {
		lines = append(lines, strings.Repeat(" ", indent)+l)
	}
	return lines
}

func wrap(style lipgloss.Style, text string, w int) []string {
	var lines []string
	for _, l := range strings.Split(ansi.Wrap(text, max(1, w), ""), "\n") {
		lines = append(lines, style.Render(strings.TrimRight(l, " ")))
	}
	return lines
}

func short(sha string) string {
	return sha[:min(7, len(sha))]
}

func field(label, value string) string {
	return fmt.Sprintf("%-10s %s", label, value)
}

func fill(style lipgloss.Style, s string, w int) string {
	s = ansi.Truncate(s, w, "…")
	return style.Render(s + strings.Repeat(" ", max(0, w-lipgloss.Width(s))))
}
