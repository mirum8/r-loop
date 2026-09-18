package core

import (
	"sort"
	"time"
)

type StepKey struct {
	Run     string
	Phase   int
	Kind    string
	Attempt int
}

type Phase struct {
	Number     int
	Title      string
	Implements []string
	DependsOn  []int
	Files      []string
	Risk       string
	Items      []Item
	DoneWhen   string
	Milestone  int
	Block      string
}

type Item struct {
	Text string
	Done bool
}

type Milestone struct {
	Number int
	Name   string
	Phases []int
}

type Entry struct {
	Name, Body                               string
	Ticked, HasBox                           bool
	Owner, Blocks, Timebox, Output, Resolved string
	BlocksAll                                bool
	BlocksPhases                             []int
	Malformed                                []string
}

type Plan struct {
	Path, Topic  string
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
	Phase       int
	MergeSHA    string
	GateSkipped bool
	GateOutput  string
}

type Event struct {
	At     time.Time
	Kind   string
	Phase  int
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
}

func (p Plan) Unticked() []int {
	var out []int
	for _, ph := range p.Phases {
		for _, it := range ph.Items {
			if !it.Done {
				out = append(out, ph.Number)
				break
			}
		}
	}
	sort.Ints(out)
	return out
}

func (p Plan) Blocking(phases []int) []Entry {
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

func meets(a, b []int) bool {
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
