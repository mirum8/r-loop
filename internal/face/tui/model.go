package tui

import (
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"r-loop/internal/core"
)

const keptWarnings = 5

type Header struct {
	RunID, Todo, Report string
	Started             time.Time
}

type Row struct {
	Number int
	Title  string
	State  core.PhaseState
}

type Step struct {
	Phase                                           int
	Kind, State, Provider, Model, Effort, Workspace string
	Started, Ended                                  time.Time
	Backstop                                        time.Duration
	Round, Rounds                                   int
	pausedSince                                     time.Time
	pausedFor                                       time.Duration
}

func (s Step) Label() string {
	if s.Round > 0 {
		return fmt.Sprintf("%s · review r%d/%d", s.Kind, s.Round, s.Rounds)
	}
	return s.Kind
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

type Warning struct {
	Text  string
	Error bool
}

type Model struct {
	Header
	Phases   []Row
	Current  int
	Live     *Step
	Warnings []Warning
	Status   string
	Blocked  string
	Resume   string
	Notice   string
	stopping bool
	abort    func() error
	Now      time.Time
	Width    int
	Height   int
	theme    Theme
}

func NewModel(h Header, phases []core.Phase, th Theme) Model {
	m := Model{Header: h, Now: h.Started, Width: 80, Height: 24, theme: th}
	for _, ph := range phases {
		state := core.PhaseUnticked
		if landed(ph) {
			state = core.PhaseLanded
		}
		m.Phases = append(m.Phases, Row{Number: ph.Number, Title: ph.Title, State: state})
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
	case "warning", "error":
		m.warn(ev)
	case "finished":
		m.end("finished")
	case "halt":
		m.end("halted")
		m.Blocked, m.Resume = ev.Fields["blocked"], ev.Fields["resume"]
		if r := ev.Fields["reason"]; r != "" {
			m.warn(ev)
		}
	case "aborted":
		m.end("halted")
		m.Resume = "r-loop resume"
	}
	return m
}

func replay(m Model, history []core.Event) Model {
	for _, ev := range history {
		m = m.Apply(ev)
	}
	m.Status, m.Blocked, m.Resume, m.Current, m.Live = "", "", "", 0, nil
	return m
}

func (m *Model) setPhase(n int, state core.PhaseState) {
	m.Phases = append([]Row(nil), m.Phases...)
	for i := range m.Phases {
		if m.Phases[i].Number == n {
			m.Phases[i].State = state
		}
	}
	if n == m.Current && (state == core.PhaseLanded || state == core.PhaseBlocked) {
		m.Current = 0
	}
}

func (m *Model) step(ev core.Event) {
	f := ev.Fields
	var s Step
	if m.Live != nil && m.Live.Phase == ev.Phase && m.Live.Kind == ev.Step && f["state"] != string(core.StepQueued) {
		s = *m.Live
	} else {
		s = Step{Phase: ev.Phase, Kind: ev.Step, Started: ev.At}
	}
	if f["state"] == string(core.StepRunning) && (s.State == string(core.StepQueued) || s.State == string(core.StepSpawned)) {
		s.Started = ev.At
	}
	s.State, s.Provider, s.Model, s.Effort = f["state"], f["provider"], f["model"], f["effort"]
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
	}
	m.Live = &s
}

func (m *Model) warn(ev core.Event) {
	line := ev.At.Format("15:04") + "  "
	if ev.Phase > 0 {
		line += fmt.Sprintf("phase %d %s: ", ev.Phase, ev.Step)
	}
	line += ev.Fields["reason"]
	m.Warnings = append(append([]Warning(nil), m.Warnings...), Warning{Text: line, Error: ev.Kind != "warning"})
	if len(m.Warnings) > keptWarnings {
		m.Warnings = m.Warnings[len(m.Warnings)-keptWarnings:]
	}
}

func (m *Model) end(status string) {
	m.Status = status
	m.Current = 0
	m.Notice = ""
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
			m.end("ended")
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
