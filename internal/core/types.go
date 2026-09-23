package core

import (
	"cmp"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type StepKey struct {
	Run     string
	Phase   string
	Kind    string
	Attempt int
}

type Phase struct {
	ID         string
	Title      string
	Implements []string
	DependsOn  []string
	Files      []string
	Risk       string
	Items      []Item
	DoneWhen   string
	Milestone  int
	Block      string
	Members    []string
}

func (p Phase) TickIDs() []string {
	if len(p.Members) > 0 {
		return p.Members
	}
	return []string{p.ID}
}

type Item struct {
	Text string
	Done bool
}

type Milestone struct {
	Number int
	Name   string
	Phases []string
}

const (
	EntryDecision     = "decision"
	EntryPerson       = "person"
	EntryUnclassified = "unclassified"
)

type Entry struct {
	Name, Body, Kind                         string
	Ticked, HasBox                           bool
	Owner, Blocks, Timebox, Output, Resolved string
	Alternative, Outstanding                 string
	BlocksAll                                bool
	BlocksPhases                             []string
	Malformed                                []string
}

type Plan struct {
	Path, Topic  string
	Backlog      bool
	Phases       []Phase
	Milestones   []Milestone
	ResolveFirst []Entry
}

type SignalKind string

const (
	SignalWarn SignalKind = "warn"
	SignalHalt SignalKind = "halt"
)

type SignalSource string

const (
	SourceDriver   SignalSource = "driver"
	SourceWatchdog SignalSource = "watchdog"
)

type Signal struct {
	Seq              int
	Kind             SignalKind
	Source           SignalSource
	Step             StepKey
	Reason, Evidence string
	At               time.Time
	Rejected         bool
	RejectReason     string
}

type Question struct {
	ID                           string
	Step                         StepKey
	Text                         string
	Options                      []string
	Recommended                  string
	AskedAt                      time.Time
	Answer, AnsweredBy, Citation string
	AnsweredAt                   time.Time
}

type Remedy struct {
	ID                           string
	Step                         StepKey
	Class, Command, Why, Consent string
	ProposedAt, DecidedAt        time.Time
}

type Landing struct {
	Phase       string
	MergeSHA    string
	GateSkipped bool
	GateOutput  string
	Added       int
	Deleted     int
}

type Event struct {
	At     time.Time
	Kind   string
	Phase  string
	Step   string
	Fields map[string]string
}

type RunMeta struct {
	Todo           string
	ResolvedConfig []byte
	Started        time.Time
}

const (
	RecordStep     = "step"
	RecordRun      = "run"
	RecordLanding  = "landing"
	RecordQuestion = "question"
	RecordSignal   = "signal"
	RecordRemedy   = "remedy"
	RecordEvent    = "event"
)

const ReasonAborted = "aborted"

type Record struct {
	Kind     string
	At       time.Time
	Step     *StepKey
	State    StepState
	Run      RunStatus
	Reason   string
	Question *Question
	Signal   *Signal
	Remedy   *Remedy
	Landing  *Landing
	Event    *Event
}

type RunState struct {
	ID, Todo  string
	Started   time.Time
	Status    RunStatus
	Steps     map[StepKey]StepState
	LastStep  *StepKey
	Landed    []Landing
	Questions []Question
	Signals   []Signal
	Remedies  []Remedy
	Events    []Event
	Warnings  []string
	Spans     map[StepKey]StepSpan
}

type StepSpan struct {
	Started, Ended time.Time
}

func (st *RunState) Span(key StepKey, state StepState, at time.Time) {
	if st.Spans == nil {
		st.Spans = map[StepKey]StepSpan{}
	}
	sp := st.Spans[key]
	if state == StepRunning && sp.Started.IsZero() {
		sp.Started = at
	}
	if state == StepOK || state == StepFailed {
		sp.Ended = at
	}
	st.Spans[key] = sp
}

func (p Plan) Unticked() []string {
	var out []string
	for _, ph := range p.Phases {
		for _, it := range ph.Items {
			if !it.Done {
				out = append(out, ph.ID)
				break
			}
		}
	}
	return out
}

var phaseIDRe = regexp.MustCompile(`^[1-9][0-9]*[a-z]?$`)

func ValidPhaseID(id string) bool {
	return phaseIDRe.MatchString(id)
}

func ParseStepName(step string) (phase, kind string, ok bool) {
	rest, found := strings.CutPrefix(step, "phase-")
	if !found {
		return "", "", false
	}
	phase, kind, found = strings.Cut(rest, "/")
	if !found || !ValidPhaseID(phase) || kind == "" || strings.ContainsAny(kind, "/ \t\n") {
		return "", "", false
	}
	return phase, kind, true
}

func ComparePhaseIDs(a, b string) int {
	na, sa := SplitPhaseID(a)
	nb, sb := SplitPhaseID(b)
	if c := cmp.Compare(na, nb); c != 0 {
		return c
	}
	return strings.Compare(sa, sb)
}

func SplitPhaseID(id string) (int, string) {
	i := len(id)
	for i > 0 && (id[i-1] < '0' || id[i-1] > '9') {
		i--
	}
	n, _ := strconv.Atoi(id[:i])
	return n, id[i:]
}

func (p Plan) Blocking(phases []string) []Entry {
	var out []Entry
	for _, e := range p.ResolveFirst {
		if e.Ticked {
			continue
		}
		if e.BlocksAll || meets(e.BlocksPhases, phases) {
			out = append(out, e)
		}
	}
	return out
}

func meets(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

type ProviderArgs struct {
	Kind   string
	Args   []string
	Ask    bool
	Review string
}
