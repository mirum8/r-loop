package tui

import (
	"fmt"
	"io"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"r-loop/internal/core"
)

const keptFeed = 6

type Header struct {
	RunID, Todo, Report string
	Started             time.Time
	Steps               []string
}

type Row struct {
	ID    string
	Title string
	State core.PhaseState
}

type Step struct {
	Phase                                           string
	Kind, State, Provider, Model, Effort, Workspace string
	Half                                            string
	Started, Ended                                  time.Time
	Backstop                                        time.Duration
	Attempt, Round, Rounds                          int
	pausedSince                                     time.Time
	pausedFor                                       time.Duration
}

func (s Step) Label() string {
	label := s.Kind
	if s.Attempt > 1 {
		label += fmt.Sprintf(" a%d", s.Attempt)
	}
	if s.Round > 0 {
		label += fmt.Sprintf(" · review r%d/%d", s.Round, s.Rounds)
		if s.Half != "" {
			label += " " + s.Half
		}
	}
	return label
}

func (s Step) Elapsed(now time.Time) time.Duration {
	if !s.Ended.IsZero() {
		now = s.Ended
	}
	return now.Sub(s.Started).Truncate(time.Second)
}

func (s Step) Remaining(now time.Time) (time.Duration, bool) {
	if !s.pausedSince.IsZero() {
		return s.Backstop - (s.pausedSince.Sub(s.Started) - s.pausedFor), true
	}
	if !s.Ended.IsZero() {
		now = s.Ended
	}
	return s.Backstop - (now.Sub(s.Started) - s.pausedFor), false
}

type tone int

const (
	toneDim tone = iota
	toneWarn
	toneError
)

type Entry struct {
	Text string
	Tone tone
}

type Question struct {
	ID    string
	Phase string
	Step  string
	At    time.Time
}

type stepID struct {
	phase string
	kind  string
}

type Model struct {
	Header
	Phases     []Row
	Current    string
	Live       *Step
	done       map[stepID]string
	Questions  []Question
	DogGone    bool
	DogWaiting bool
	Feed       []Entry
	Status     string
	Blocked    string
	Resume     string
	Notice     string
	stopping   bool
	abort      func() error
	Now        time.Time
	ended      time.Time
	Width      int
	Height     int
	theme      Theme
}

func NewModel(h Header, phases []core.Phase, th Theme) Model {
	m := Model{Header: h, Now: h.Started, Width: 80, Height: 24, theme: th}
	for _, ph := range phases {
		state := core.PhaseUnticked
		if landed(ph) {
			state = core.PhaseLanded
		}
		m.Phases = append(m.Phases, Row{ID: ph.ID, Title: ph.Title, State: state})
	}
	return m
}

func landed(ph core.Phase) bool {
	for _, it := range ph.Items {
		if !it.Done {
			return false
		}
	}
	return len(ph.Items) > 0
}

func (m Model) Apply(ev core.Event) Model {
	switch ev.Kind {
	case "phase-start":
		m.Current = ev.Phase
	case "phase-state":
		m.setPhase(ev.Phase, core.PhaseState(ev.Fields["state"]))
	case "phase-blocked", "phase-skipped", "item-skipped":
		m.setPhase(ev.Phase, core.PhaseBlocked)
	case "step":
		m.step(ev)
	case "warning":
		m.log(ev, toneWarn, ev.Fields["reason"])
	case "error", "restart-refused":
		m.log(ev, toneError, ev.Fields["reason"])
	case "watchdog-waiting":
		m.DogWaiting = true
		if q := ev.Fields["question"]; q != "" {
			m.log(ev, toneWarn, "watchdog asks you: "+strings.Join(strings.Fields(q), " "))
		}
	case "watchdog-resumed":
		m.DogWaiting = false
	case "watchdog-unreachable":
		m.DogGone, m.DogWaiting = true, false
		m.log(ev, toneError, "watchdog gone: "+ev.Fields["reason"])
	case "stalled":
		m.log(ev, toneError, "stalled")
	case "restart":
		m.log(ev, toneDim, "restarted")
	case "nudge":
		m.log(ev, toneDim, "nudged")
	case "landed":
		m.log(ev, toneDim, landedText(ev.Fields))
	case "gate-fix":
		m.log(ev, toneDim, "land gate fix r"+ev.Fields["round"])
	case "assumption":
		m.log(ev, toneDim, "assumed: "+ev.Fields["text"])
	case "question":
		m.Questions = append(append([]Question(nil), m.Questions...), Question{ID: ev.Fields["id"], Phase: ev.Phase, Step: ev.Step, At: ev.At})
		m.log(ev, toneDim, "asked "+ev.Fields["id"])
	case "question-answered":
		m.settle(ev.Fields["id"])
		m.log(ev, toneDim, ev.Fields["id"]+" answered by "+ev.Fields["by"])
	case "human":
		if ev.Fields["what"] == "resume" {
			m.log(ev, toneDim, "resumed")
		}
	case "signal-rejected", "note", "report-skipped", "reviewer-skipped":
		m.log(ev, toneDim, ev.Fields["reason"])
	case "finished":
		m.end("finished", ev.At)
	case "halt":
		m.end("halted", ev.At)
		m.Blocked, m.Resume = ev.Fields["blocked"], ev.Fields["resume"]
		if r := ev.Fields["reason"]; r != "" {
			m.log(ev, toneError, r)
		}
	case "aborted":
		m.end("halted", ev.At)
		m.Resume = "r-loop resume"
	}
	return m
}

func replay(m Model, history []core.Event) Model {
	for _, ev := range history {
		m = m.Apply(ev)
	}
	m.Status, m.Blocked, m.Resume, m.Current, m.Live = "", "", "", "", nil
	m.ended = time.Time{}
	m.Questions, m.DogGone, m.DogWaiting = nil, false, false
	return m
}

func (m *Model) setPhase(n string, state core.PhaseState) {
	m.Phases = append([]Row(nil), m.Phases...)
	for i := range m.Phases {
		if m.Phases[i].ID == n {
			m.Phases[i].State = state
		}
	}
	if n == m.Current && (state == core.PhaseLanded || state == core.PhaseBlocked) {
		m.Current = ""
	}
}

func (m *Model) step(ev core.Event) {
	f := ev.Fields
	var s Step
	attempt, _ := strconv.Atoi(f["attempt"])
	terminal := f["state"] == string(core.StepOK) || f["state"] == string(core.StepFailed)
	if m.Live != nil && m.Live.Phase == ev.Phase && m.Live.Kind == ev.Step && m.Live.Attempt == attempt && f["state"] != string(core.StepQueued) && (m.Live.Ended.IsZero() || terminal) {
		s = *m.Live
	} else {
		s = Step{Phase: ev.Phase, Kind: ev.Step, Started: ev.At}
	}
	if f["state"] == string(core.StepRunning) && (s.State == string(core.StepQueued) || s.State == string(core.StepSpawned)) {
		s.Started = ev.At
	}
	s.State, s.Provider, s.Model, s.Effort, s.Half = f["state"], f["provider"], f["model"], f["effort"], f["half"]
	s.Attempt = attempt
	if f["workspace"] != "" {
		s.Workspace = f["workspace"]
	}
	s.Backstop, _ = time.ParseDuration(f["backstop"])
	s.Rounds, _ = strconv.Atoi(f["rounds"])
	if r, err := strconv.Atoi(f["round"]); err == nil {
		s.Round = r
	}
	paused := s.State == string(core.StepWaitingInput) || s.Round > 0
	switch {
	case paused && s.pausedSince.IsZero():
		s.pausedSince = ev.At
	case !paused && !s.pausedSince.IsZero():
		s.pausedFor += ev.At.Sub(s.pausedSince)
		s.pausedSince = time.Time{}
	}
	if s.State == string(core.StepOK) || s.State == string(core.StepFailed) {
		s.Ended = ev.At
		m.done = maps.Clone(m.done)
		if m.done == nil {
			m.done = map[stepID]string{}
		}
		m.done[stepID{s.Phase, s.Kind}] = s.State
	}
	m.Live = &s
}

func (m *Model) log(ev core.Event, t tone, text string) {
	line := ev.At.Format("15:04") + "  "
	if ev.Phase != "" {
		line += strings.TrimRight(fmt.Sprintf("phase %s %s", ev.Phase, ev.Step), " ") + ": "
	}
	m.Feed = append(append([]Entry(nil), m.Feed...), Entry{Text: line + text, Tone: t})
	if len(m.Feed) > keptFeed {
		m.Feed = m.Feed[len(m.Feed)-keptFeed:]
	}
}

func (m *Model) settle(id string) {
	var open []Question
	for _, q := range m.Questions {
		if q.ID != id {
			open = append(open, q)
		}
	}
	m.Questions = open
}

func landedText(f map[string]string) string {
	text := "landed"
	if sha := f["merge"]; sha != "" {
		text += " " + sha[:min(7, len(sha))]
	}
	if f["gateSkipped"] == "true" {
		text += " · gate skipped"
	}
	return text
}

func (m *Model) end(status string, at time.Time) {
	m.Status = status
	m.ended = at
	m.Current = ""
	m.Live = nil
	m.Notice = ""
}

func (m Model) clock() time.Time {
	if !m.ended.IsZero() {
		return m.ended
	}
	return m.Now
}

type tickMsg time.Time

type eventMsg core.Event

type closedMsg struct{}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Init() tea.Cmd { return tick() }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.Now = time.Time(msg)
		return m, tick()
	case eventMsg:
		return m.Apply(core.Event(msg)), nil
	case closedMsg:
		if m.Status == "" {
			m.end("ended", m.Now)
		}
	case tea.WindowSizeMsg:
		if msg.Width > 0 && msg.Height > 0 {
			m.Width, m.Height = msg.Width, msg.Height
		}
	case tea.KeyMsg:
		switch {
		case msg.String() == "q" && m.Status != "":
			return m, tea.Quit
		case m.stopping:
			m.confirmStop(msg)
		case msg.Type == tea.KeyCtrlC && m.Status == "":
			m.stopping, m.Notice = true, "stop the run? the live step's session and worktree are left for resume [y/n]"
		}
	}
	return m, nil
}

type Face struct {
	In      io.Reader
	Out     io.Writer
	NoColor bool
	Abort   func() error
	mu      sync.Mutex
	prog    *tea.Program
	backlog []core.Event
	done    chan struct{}
	report  string
}

func (f *Face) Start(h Header, phases []core.Phase, history []core.Event) {
	r := lipgloss.NewRenderer(f.Out)
	if f.NoColor {
		r.SetColorProfile(termenv.ANSI)
	}
	m := NewModel(h, phases, NewTheme(r, f.NoColor))
	m.abort = f.Abort
	m.Now = time.Now()
	m = replay(m, history)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.report = h.Report
	f.prog = tea.NewProgram(m, tea.WithInput(f.In), tea.WithOutput(f.Out), tea.WithAltScreen())
	f.done = make(chan struct{})
	go func() {
		defer close(f.done)
		f.prog.Run()
	}()
	for _, ev := range f.backlog {
		f.prog.Send(eventMsg(ev))
	}
	f.backlog = nil
}

func (f *Face) Emit(ev core.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prog == nil {
		f.backlog = append(f.backlog, ev)
		return
	}
	f.prog.Send(eventMsg(ev))
}

func (f *Face) Stop() {
	f.mu.Lock()
	prog, done := f.prog, f.done
	f.prog = nil
	f.mu.Unlock()
	if prog == nil {
		return
	}
	prog.Quit()
	<-done
}

func (f *Face) Close() {
	f.mu.Lock()
	prog, done := f.prog, f.done
	f.mu.Unlock()
	if prog == nil {
		return
	}
	prog.Send(closedMsg{})
	<-done
	if f.report != "" {
		fmt.Fprintf(f.Out, "report: %s\n", f.report)
	}
}

func (m *Model) confirmStop(key tea.KeyMsg) {
	m.stopping = false
	if key.String() != "y" {
		m.Notice = ""
		return
	}
	if m.RunID == "" {
		m.Notice = "stopping before the run starts"
		return
	}
	if m.abort == nil {
		m.Notice = "use r-loop abort to stop the run"
		return
	}
	if err := m.abort(); err != nil {
		m.Notice = "abort failed: " + err.Error()
		return
	}
	m.Notice = "abort requested; the run stops before its next step"
}
