package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultPoll       = 10 * time.Second
	defaultStallGrace = 2 * time.Minute
)

type SessionManager struct {
	Host       SessionHost
	Repo       Repo
	Prompts    Prompts
	Store      Store
	Ask        AskChannel
	Resolve    func(provider, model, effort, askURL, mcpConfigPath string) (ProviderArgs, error)
	Now        func() time.Time
	Poll       time.Duration
	StallGrace time.Duration
}

type StepRef struct {
	Key                                    StepKey
	Kind                                   StepKind
	Phase                                  Phase
	InPrimary                              bool
	Worktree, Branch, Base, RunDir, AskURL string
	Vars                                   map[string]any
	ReviewFrom                             int
	PrevRoundTree                          string
	KeepUncommitted                        bool
}

type Session struct {
	Ref                              StepRef
	Dir, StartSHA, StartTree         string
	Workspace, Pane, Agent, Sentinel string
	Reviewer                         string
	OpenQuestion, Reviewing          atomic.Bool
	fix                              *fixHalf
}

type fixHalf struct {
	verdictPath, roundTree string
	findings               []string
	spent                  time.Duration
}

type FindingsFile struct {
	Reviewer, Path string
}

type Observer interface {
	Started(*Session)
	Stalled(*Session)
	Resumed(*Session)
	Reviewing(s *Session, round int)
}

type Outcome struct {
	State   StepState
	Reason  string
	Session *Session
	Stalled bool
	Halted  bool
	Warning string
	active  time.Duration
}

func (m *SessionManager) Spawn(ctx context.Context, ref StepRef) (*Session, error) {
	s := &Session{Ref: ref}
	if ref.InPrimary {
		s.Dir = m.Repo.Root()
	} else {
		if err := m.Repo.AddWorktree(ref.Worktree, ref.Branch, ref.Base); err != nil {
			return s, fmt.Errorf("spawn: %w", err)
		}
		s.Dir = ref.Worktree
		if !filepath.IsAbs(s.Dir) {
			s.Dir = filepath.Join(m.Repo.Root(), s.Dir)
		}
	}
	var err error
	if s.StartSHA, err = m.Repo.HeadSHA(s.Dir); err != nil {
		return s, fmt.Errorf("spawn: %w", err)
	}
	key := ref.Key
	stepDir := filepath.Join(ref.RunDir, "phase-"+strconv.Itoa(key.Phase))
	base := fmt.Sprintf("%s-a%d", key.Kind, key.Attempt)
	if s.StartTree, err = m.baseline(s, base); err != nil {
		return s, fmt.Errorf("spawn: %w", err)
	}
	s.Sentinel = filepath.Join(stepDir, base+".sentinel")
	s.Agent = fmt.Sprintf("rloop-p%d-%s", key.Phase, key.Kind)
	if key.Attempt > 1 {
		s.Agent += fmt.Sprintf("-a%d", key.Attempt)
	}
	if err := os.MkdirAll(stepDir, 0o755); err != nil {
		return s, fmt.Errorf("spawn: %w", err)
	}
	if err := m.record(key, StepSpawned, ""); err != nil {
		return s, err
	}
	ws, err := m.Host.Open(OpenSpec{CWD: s.Dir, Label: s.Agent, Env: map[string]string{
		"R_LOOP_SENTINEL": s.Sentinel,
		"R_LOOP_RUN":      key.Run,
		"R_LOOP_PHASE":    strconv.Itoa(key.Phase),
		"R_LOOP_STEP":     key.Kind,
	}})
	s.Workspace, s.Pane = ws.ID, ws.RootPane
	if err != nil {
		return s, fmt.Errorf("spawn: %w", err)
	}
	if err := m.start(s, stepDir); err != nil {
		return s, fmt.Errorf("spawn: %w", err)
	}
	return s, m.record(key, StepRunning, "")
}

func (m *SessionManager) start(s *Session, stepDir string) error {
	row := s.Ref.Kind.Row
	args, err := m.Resolve(row.Provider, row.Model, row.Effort, "", "")
	if err != nil {
		return err
	}
	if !args.Ask {
		if err := m.event(m.now(), s, Event{Kind: "ask-none", Fields: map[string]string{"provider": row.Provider}}); err != nil {
			return err
		}
	} else if m.Ask != nil {
		s.Ref.AskURL = m.Ask.StepURL(s.Ref.Key)
		mcpPath := filepath.Join(stepDir, s.Agent+".mcp.json")
		if args, err = m.Resolve(row.Provider, row.Model, row.Effort, s.Ref.AskURL, mcpPath); err != nil {
			return err
		}
		if slices.ContainsFunc(args.Args, func(a string) bool { return strings.Contains(a, mcpPath) }) {
			if err := writeMCPConfig(mcpPath, s.Ref.AskURL); err != nil {
				return err
			}
		}
	}
	ref := s.Ref
	if _, err := m.Host.Start(s.Pane, s.Agent, args.Kind, args.Args); err != nil {
		return err
	}
	if ref.ReviewFrom > 0 {
		return nil
	}
	vars := make(map[string]any, len(ref.Vars)+2)
	for k, v := range ref.Vars {
		vars[k] = v
	}
	vars["Sentinel"] = s.Sentinel
	if ref.AskURL != "" {
		vars["AskURL"] = ref.AskURL
	}
	text, _, err := m.Prompts.Render(ref.Kind.Prompt, vars)
	if err != nil {
		return err
	}
	return m.Host.Prompt(s.Agent, text, false, 0)
}

type watch struct {
	s              *Session
	obs            Observer
	elapsed, quiet time.Duration
	stalled        bool
}

func (m *SessionManager) Wait(ctx context.Context, s *Session, obs Observer) Outcome {
	obs.Started(s)
	return m.waitAll(ctx, []*Session{s}, obs)[0]
}

func (m *SessionManager) WaitAll(ctx context.Context, sessions []*Session) []Outcome {
	return m.waitAll(ctx, sessions, nopObserver{})
}

type nopObserver struct{}

func (nopObserver) Started(*Session) {}
func (nopObserver) Stalled(*Session) {}
func (nopObserver) Resumed(*Session) {}

func (nopObserver) Reviewing(*Session, int) {}

func (m *SessionManager) waitAll(ctx context.Context, sessions []*Session, obs Observer) []Outcome {
	poll := m.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	outs := make([]Outcome, len(sessions))
	watches := make([]*watch, len(sessions))
	for i, s := range sessions {
		watches[i] = &watch{s: s, obs: obs}
		if s.fix != nil {
			watches[i].elapsed = s.fix.spent
		}
	}
	pending := len(sessions)
	last := m.now()
	for pending > 0 {
		select {
		case <-ctx.Done():
			for i, w := range watches {
				if w != nil {
					outs[i] = m.fail(w.s, "interrupted: "+ctx.Err().Error())
				}
			}
			return outs
		case <-ticker.C:
		}
		now := m.now()
		dt := now.Sub(last)
		last = now
		for i, w := range watches {
			if w == nil {
				continue
			}
			if out, done := m.tick(w, now, dt); done {
				out.active = w.elapsed
				outs[i], watches[i] = out, nil
				pending--
			}
		}
	}
	return outs
}

func (m *SessionManager) tick(w *watch, now time.Time, dt time.Duration) (Outcome, bool) {
	s := w.s
	grace := m.StallGrace
	if grace <= 0 {
		grace = defaultStallGrace
	}
	sentinel, err := ReadSentinel(s.Sentinel)
	if !errors.Is(err, ErrNoSentinel) {
		return m.judge(s, sentinel, err), true
	}
	state, _ := m.Host.State(s.Agent)
	if state == AgentGone {
		return m.fail(s, "agent gone"), true
	}
	if s.OpenQuestion.Load() || s.Reviewing.Load() {
		return Outcome{}, false
	}
	w.elapsed += dt
	timeout := s.Ref.Kind.Row.Timeout
	if s.fix != nil {
		timeout = s.Ref.Kind.Row.ReviewTimeout
	}
	if timeout > 0 && w.elapsed > timeout {
		return m.fail(s, "backstop "+timeout.String()), true
	}
	switch state {
	case AgentWorking:
		w.quiet = 0
		if w.stalled {
			w.stalled = false
			m.recordState(now, s, StepRunning)
			w.obs.Resumed(s)
		}
		return Outcome{}, false
	case AgentIdle, AgentBlocked:
		w.quiet += dt
	default:
		return Outcome{}, false
	}
	if w.quiet < grace {
		return Outcome{}, false
	}
	if w.stalled {
		out := m.fail(s, "stalled: no response to nudge")
		out.Stalled = true
		return out, true
	}
	w.stalled, w.quiet = true, 0
	m.recordState(now, s, StepStalled)
	w.obs.Stalled(s)
	if err := m.Host.Prompt(s.Agent, nudge(grace), false, 0); err != nil {
		out := m.fail(s, "stalled: nudge not delivered: "+err.Error())
		out.Stalled = true
		return out, true
	}
	m.event(now, s, Event{Kind: "nudge"})
	return Outcome{}, false
}

func (m *SessionManager) recordState(at time.Time, s *Session, state StepState) {
	if s.Reviewer == "" {
		m.recordAt(at, s.Ref.Key, state, "")
	}
}

func writeMCPConfig(path, url string) error {
	data, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"r-loop": map[string]string{"type": "http", "url": url}}})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func nudge(grace time.Duration) string {
	return "r-loop: no sentinel and no activity for " + grace.String() + ". If your work is done, write the sentinel now. If you are blocked, call ask_user, or write a failed sentinel with the reason."
}

func (m *SessionManager) judge(s *Session, sentinel Sentinel, sErr error) Outcome {
	if sErr != nil || sentinel.Outcome != "ok" {
		state, reason := Judge(sentinel, sErr, false, "")
		return Outcome{State: state, Reason: reason, Session: s}
	}
	head, err := m.Repo.HeadSHA(s.Dir)
	if err != nil {
		return m.fail(s, "head: "+err.Error())
	}
	if head != s.StartSHA {
		return m.fail(s, "step committed before review")
	}
	if f := s.fix; f != nil {
		ctx := m.evidence(s)
		ctx.VerdictPath, ctx.RoundTree, ctx.FindingsFiles = f.verdictPath, f.roundTree, f.findings
		if ok, missing := verdictCheck(ctx); !ok {
			state, reason := Judge(sentinel, nil, false, missing)
			return Outcome{State: state, Reason: reason, Session: s}
		}
	}
	check, found := LookupCheck(s.Ref.Kind.Check)
	if !found {
		return m.fail(s, "no check "+s.Ref.Kind.Check)
	}
	ok, missing := check(m.evidence(s))
	state, reason := Judge(sentinel, nil, ok, missing)
	return Outcome{State: state, Reason: reason, Session: s}
}

func (m *SessionManager) evidence(s *Session) EvidenceContext {
	str := func(k string) string {
		v, _ := s.Ref.Vars[k].(string)
		return v
	}
	ctx := EvidenceContext{
		Repo:        m.Repo,
		Worktree:    s.Dir,
		StartSHA:    s.StartSHA,
		StartTree:   s.StartTree,
		PlanPath:    str("PlanPath"),
		VerdictPath: str("VerdictPath"),
		RoundTree:   str("RoundTree"),
		ReportPath:  str("ReportPath"),
		FS:          os.DirFS(s.Dir),
	}
	if s.Reviewer != "" {
		p := str("FindingsPath")
		ctx.FS, ctx.FindingsFiles = os.DirFS(filepath.Dir(p)), []string{filepath.Base(p)}
	}
	return ctx
}

func (m *SessionManager) Finish(s *Session, out Outcome) Outcome {
	key := s.Ref.Key
	if out.State == StepOK && !s.Ref.InPrimary && !s.Ref.KeepUncommitted {
		if _, err := m.Repo.CommitAll(s.Dir, fmt.Sprintf("r-loop: phase %d %s", key.Phase, key.Kind)); err != nil {
			out.State, out.Reason = StepFailed, "commit: "+err.Error()
		}
	}
	if out.State != StepOK {
		if err := m.snapshot(s); err != nil {
			out.Reason += "; snapshot: " + err.Error()
		}
	}
	if err := m.record(key, out.State, out.Reason); err != nil {
		out.State, out.Reason = StepFailed, "record: "+err.Error()
	}
	return out
}

func (m *SessionManager) snapshot(s *Session) error {
	tree, err := m.Repo.Snapshot(s.Dir)
	if err != nil {
		return err
	}
	key := s.Ref.Key
	return m.event(m.now(), s, Event{Kind: "snapshot", Fields: map[string]string{"step": fmt.Sprintf("%s-a%d", key.Kind, key.Attempt), "tree": tree}})
}

func (m *SessionManager) baseline(s *Session, step string) (string, error) {
	key := s.Ref.Key
	if key.Attempt > 1 {
		st, err := m.Store.Load(key.Run)
		if err != nil {
			return "", err
		}
		prev := key
		prev.Attempt--
		if st.Steps[prev] != StepOK {
			if tree := recordedBaseline(st, key); tree != "" {
				return tree, nil
			}
		}
	}
	tree, err := m.Repo.Snapshot(s.Dir)
	if err != nil {
		return "", err
	}
	return tree, m.event(m.now(), s, Event{Kind: "baseline", Fields: map[string]string{"step": step, "tree": tree}})
}

func recordedBaseline(st RunState, key StepKey) string {
	tree := ""
	for _, e := range st.Events {
		if e.Kind == "baseline" && e.Phase == key.Phase && e.Step == key.Kind {
			tree = e.Fields["tree"]
		}
	}
	return tree
}

func (m *SessionManager) Stop(s *Session) error {
	return m.Host.Interrupt(s.Agent)
}

func (m *SessionManager) fail(s *Session, reason string) Outcome {
	return Outcome{State: StepFailed, Reason: reason, Session: s}
}

func (m *SessionManager) record(key StepKey, state StepState, reason string) error {
	return m.recordAt(m.now(), key, state, reason)
}

func (m *SessionManager) recordAt(at time.Time, key StepKey, state StepState, reason string) error {
	return m.Store.Append(key.Run, Record{Kind: RecordStep, At: at, Step: &key, State: state, Reason: reason})
}

func (m *SessionManager) event(at time.Time, s *Session, ev Event) error {
	ev.At, ev.Phase, ev.Step = at, s.Ref.Key.Phase, s.Ref.Key.Kind
	return m.Store.Append(s.Ref.Key.Run, Record{Kind: RecordEvent, At: ev.At, Event: &ev})
}

func (m *SessionManager) now() time.Time {
	if m.Now == nil {
		return time.Now()
	}
	return m.Now()
}
