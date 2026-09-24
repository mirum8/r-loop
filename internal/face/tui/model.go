package tui

import (
	"fmt"
	"io"
	"maps"
	"slices"
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
	Backlog             bool
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
	Reviews                                         []Round
	pausedSince                                     time.Time
	pausedFor                                       time.Duration
	halfSince                                       time.Time
	timedRound                                      int
	timedHalf                                       string
}

type Round struct {
	N                         int
	Reviewers                 []Reviewer
	Fixed, Unfixed, Dismissed int
	Severities                []string
	Clean                     bool
}

type Reviewer struct {
	ID, Agent, State string
	Findings         int
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

func (s Step) HalfElapsed(now time.Time) time.Duration {
	if !s.Ended.IsZero() {
		now = s.Ended
	}
	return now.Sub(s.halfSince).Truncate(time.Second)
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
	checking   string
	checkFrom  time.Time
	Questions  []Question
	DogGone    bool
	DogWaiting bool
	Feed       []Entry
	Status     string
	Blocked    string
	Resume     string
	Notice     string
	stopping   bool
	aborting   bool
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
		m.checking = ""
		m.step(ev)
	case "review-round", "agent-named", "review-find", "finding", "review-clean":
		m.review(ev)
	case "phase-check-start":
		m.checking, m.checkFrom, m.Live = ev.Phase, ev.At, nil
	case "phase-check", "phase-check-timeout", "phase-check-skipped":
		m.checking = ""
		m.log(ev, toneDim, "phase check "+checkDetail(ev))
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
	case "triage-start":
		noun := "phase"
		if ev.Fields["kind"] == "backlog" {
			noun = "item"
		}
		m.log(ev, toneDim, "triage: watchdog verifying "+core.Plural(len(strings.Split(ev.Fields["phases"], ", ")), noun))
	case "triage":
		m.log(ev, toneDim, "triage: "+ev.Fields["summary"])
	case core.TriageSkipped:
		m.setPhase(ev.Phase, core.PhaseBlocked)
		m.log(ev, toneDim, "skipped by triage: "+ev.Fields["reason"])
	case "run-list":
		m.group(core.RunListGroups(ev.Fields))
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
	m.checking = ""
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

func (m *Model) group(groups []core.Group) {
	if len(groups) == 0 {
		return
	}
	state := map[string]core.PhaseState{}
	var pl core.Plan
	for _, r := range m.Phases {
		state[r.ID] = r.State
		pl.Phases = append(pl.Phases, core.Phase{ID: r.ID, Title: r.Title})
	}
	rows := make([]Row, 0, len(m.Phases))
	for _, ph := range core.GroupBacklog(pl, groups).Phases {
		rows = append(rows, Row{ID: ph.ID, Title: ph.Title, State: state[ph.ID]})
	}
	m.Phases = rows
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
	if s.Half != "" && (s.Round != s.timedRound || s.Half != s.timedHalf) {
		s.timedRound, s.timedHalf, s.halfSince = s.Round, s.Half, ev.At
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

func (m *Model) review(ev core.Event) {
	f := ev.Fields
	if m.Live == nil || m.Live.Phase != ev.Phase || m.Live.Kind != ev.Step {
		return
	}
	if a := f["attempt"]; a != "" && a != strconv.Itoa(m.Live.Attempt) {
		return
	}
	n, _ := strconv.Atoi(f["round"])
	s := *m.Live
	s.Reviews = slices.Clone(s.Reviews)
	i := slices.IndexFunc(s.Reviews, func(r Round) bool { return r.N == n })
	if i < 0 {
		s.Reviews = append(s.Reviews, Round{N: n})
		i = len(s.Reviews) - 1
	}
	r := s.Reviews[i]
	r.Reviewers = slices.Clone(r.Reviewers)
	switch ev.Kind {
	case "review-round":
		r = Round{N: n}
	case "agent-named":
		rv := r.reviewer(f["reviewer"])
		rv.Agent = f["agent"]
	case "review-find":
		rv := r.reviewer(f["reviewer"])
		rv.State = f["state"]
		rv.Findings, _ = strconv.Atoi(f["findings"])
	case "finding":
		switch {
		case f["verdict"] != "real":
			r.Dismissed++
		case f["fixed"] == "true":
			r.Fixed++
			r.Severities = append(slices.Clone(r.Severities), f["severity"])
			slices.Sort(r.Severities)
		default:
			r.Unfixed++
		}
	case "review-clean":
		r.Clean = true
	}
	s.Reviews[i] = r
	m.Live = &s
}

func (r *Round) reviewer(id string) *Reviewer {
	i := slices.IndexFunc(r.Reviewers, func(rv Reviewer) bool { return rv.ID == id })
	if i < 0 {
		r.Reviewers = append(r.Reviewers, Reviewer{ID: id})
		i = len(r.Reviewers) - 1
	}
	return &r.Reviewers[i]
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

func checkDetail(ev core.Event) string {
	f := ev.Fields
	switch ev.Kind {
	case "phase-check":
		if f["result"] == "no disagreement" {
			return "found no disagreement"
		}
		return "warned"
	case "phase-check-timeout":
		return withReason("timed out", f["reason"])
	}
	return withReason("skipped", f["reason"])
}

func withReason(text, reason string) string {
	if reason == "" {
		return text
	}
	return text + ": " + reason
}

func (m *Model) end(status string, at time.Time) {
	m.checking = ""
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
		case m.Status != "" && quitKey(msg):
			return m, tea.Quit
		case msg.Type == tea.KeyCtrlC && (m.stopping || m.aborting):
			if m.Status == "" && !m.aborting && m.RunID != "" && m.abort != nil {
				_ = m.abort()
			}
			return m, tea.Quit
		case m.stopping:
			m.confirmStop(msg)
		case msg.Type == tea.KeyCtrlC && m.Status == "":
			m.stopping, m.Notice = true, "stop the run? keep session for resume [y/n; ctrl+c again to quit now]"
		}
	}
	return m, nil
}

func quitKey(key tea.KeyMsg) bool {
	switch key.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		return true
	case tea.KeyRunes:
		switch key.String() {
		case "q", "Q", "й", "Й":
			return true
		}
	}
	return false
}

type Face struct {
	In        io.Reader
	Out       io.Writer
	NoColor   bool
	Abort     func() error
	OnExit    func(error)
	mu        sync.Mutex
	prog      *tea.Program
	backlog   []core.Event
	done      chan struct{}
	report    string
	endKind   string
	endReason string
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
	f.prog = tea.NewProgram(m, tea.WithInput(f.In), tea.WithOutput(f.Out), tea.WithAltScreen(), tea.WithoutSignalHandler())
	f.done = make(chan struct{})
	prog, done := f.prog, f.done
	go func() {
		defer close(done)
		_, err := prog.Run()
		if f.OnExit != nil {
			f.OnExit(err)
		}
	}()
	for _, ev := range f.backlog {
		f.prog.Send(eventMsg(ev))
	}
	f.backlog = nil
}

func (f *Face) Emit(ev core.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ev.Kind == "halt" || ev.Kind == "aborted" {
		f.endKind, f.endReason = ev.Kind, ev.Fields["reason"]
	}
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
	if prog != nil {
		prog.Quit()
	}
	if done != nil {
		<-done
	}
}

func (f *Face) Close() {
	f.mu.Lock()
	prog, done := f.prog, f.done
	f.mu.Unlock()
	if prog != nil {
		prog.Send(closedMsg{})
	}
	if done != nil {
		<-done
	}
	f.mu.Lock()
	endKind, endReason, report := f.endKind, f.endReason, f.report
	f.mu.Unlock()
	switch endKind {
	case "halt":
		if endReason != "" {
			fmt.Fprintf(f.Out, "halted: %s\n", endReason)
		} else {
			fmt.Fprintln(f.Out, "halted")
		}
		fmt.Fprintln(f.Out, "r-loop resume")
	case "aborted":
		fmt.Fprintln(f.Out, "aborted")
		fmt.Fprintln(f.Out, "r-loop resume")
	}
	if report != "" {
		fmt.Fprintf(f.Out, "report: %s\n", report)
	}
}

func (m *Model) confirmStop(key tea.KeyMsg) {
	m.stopping = false
	if key.String() != "y" {
		m.Notice = ""
		return
	}
	if m.Status != "" {
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
	m.aborting = true
	m.Notice = "abort requested; stopping now (ctrl+c again to quit now)"
}
