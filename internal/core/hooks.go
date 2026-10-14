package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type hookAgent struct {
	busy         bool
	tool, target string
	notice       string
	edits        map[string]time.Time
	expect       string
	expectSince  time.Time
	matched      bool
	turn         int
	started      bool
	pinned       bool
	exit         string
	heard        bool
	since        time.Time
	typed        bool
	session      string
	transcript   string
}

type hookLog struct {
	mu      sync.Mutex
	agents  map[string]*hookAgent
	warned  bool
	changed chan struct{}
}

var (
	hookPoll   = 200 * time.Millisecond
	submitWait = 10 * time.Second
)

var errTurnNotOver = errors.New("turn not over")

func isEditTool(tool string) bool {
	switch tool {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return true
	}
	return false
}

func (h *hookLog) agent(name string) *hookAgent {
	if h.agents == nil {
		h.agents = map[string]*hookAgent{}
	}
	a := h.agents[name]
	if a == nil {
		a = &hookAgent{edits: map[string]time.Time{}}
		h.agents[name] = a
	}
	return a
}

func (h *hookLog) apply(ev HookEvent) {
	a := h.agent(ev.Agent)
	if ev.At.Before(a.since) {
		return
	}
	a.heard, a.typed = true, false
	if a.transcript == "" && ev.Transcript != "" {
		a.session, a.transcript = ev.Session, ev.Transcript
	}
	wasBusy := a.busy
	defer func() {
		if a.busy && (!wasBusy || ev.Event == "UserPromptSubmit") {
			a.turn++
		}
	}()
	switch ev.Event {
	case "UserPromptSubmit":
		a.busy, a.notice = true, ""
		if a.expect != "" && !ev.At.Before(a.expectSince) && sameText(a.expect, ev.Prompt, ev.PromptCut) {
			a.matched = true
		}
	case "PreToolUse":
		a.busy, a.notice, a.tool, a.target = true, "", ev.Tool, ev.Target
	case "PostToolUse":
		a.busy, a.notice = true, ""
		if isEditTool(ev.Tool) && ev.Target != "" {
			a.edits[ev.Target] = ev.At
		}
	case "Notification":
		if ev.Type == "idle_prompt" {
			a.busy, a.tool, a.target = false, "", ""
		} else {
			a.notice = ev.Notice
		}
	case "Stop":
		a.busy, a.notice, a.tool, a.target = false, "", "", ""
	case "SessionStart":
		a.started = true
	case "SessionEnd":
		switch ev.Reason {
		case "exit", "logout", "prompt_input_exit", "other":
			a.exit = ev.Reason
		}
	}
}

func (m *SessionManager) drainHooks() error {
	if m.Hooks == nil {
		return nil
	}
	h := &m.hooks
	h.mu.Lock()
	defer h.mu.Unlock()
	events, err := m.Hooks.Read()
	for _, ev := range events {
		h.apply(ev)
	}
	if len(events) > 0 && h.changed != nil {
		close(h.changed)
		h.changed = nil
	}
	if err != nil && !h.warned {
		h.warned = true
		return err
	}
	return nil
}

func (m *SessionManager) hookChanged() <-chan struct{} {
	h := &m.hooks
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.changed == nil {
		h.changed = make(chan struct{})
	}
	return h.changed
}

func sameText(sent, seen string, cut bool) bool {
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	a, b := norm(sent), norm(unwrapPaste(seen))
	if b == "" {
		return false
	}
	if !cut {
		return a == b
	}
	return strings.HasPrefix(a, b)
}

func unwrapPaste(s string) string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(s), "<pasted_content ")
	if !ok {
		return s
	}
	attrs, body, ok := strings.Cut(rest, ">")
	if !ok {
		return s
	}
	return strings.TrimSuffix(strings.TrimSpace(body), "</pasted_content "+attrs+">")
}

func (m *SessionManager) expectPrompt(agent, text string, since time.Time) {
	m.hookAgent(agent, func(a *hookAgent) {
		a.expect, a.expectSince, a.matched = text, since, false
		switch {
		case text != "" && !a.busy:
			a.busy, a.typed = true, true
		case text == "" && a.typed:
			a.busy, a.typed = false, false
		}
	})
}

func (m *SessionManager) submitted(agent string) bool {
	_ = m.drainHooks()
	var matched bool
	m.hookAgent(agent, func(a *hookAgent) { matched = a.matched })
	return matched
}

func (m *SessionManager) awaitSubmit(agent string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if m.submitted(agent) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(hookPoll)
	}
}

func (m *SessionManager) typingLock(agent string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.typing == nil {
		m.typing = map[string]*sync.Mutex{}
	}
	if m.typing[agent] == nil {
		m.typing[agent] = &sync.Mutex{}
	}
	return m.typing[agent]
}

func (m *SessionManager) prompt(agent, text string, gate *handOff) error {
	lock := m.typingLock(agent)
	lock.Lock()
	defer lock.Unlock()
	_ = m.drainHooks()
	if seen, _, _ := m.hookTurn(agent); !seen {
		return m.Host.Prompt(agent, text, false, 0)
	}
	if gate != nil {
		if _, ready, err := gate.check(); err != nil || !ready {
			return errTurnNotOver
		}
	}
	m.expectPrompt(agent, text, m.now())
	defer m.expectPrompt(agent, "", time.Time{})
	err := m.Host.Submit(agent, text)
	stalled := errors.Is(err, ErrPromptStalled)
	if err != nil && !stalled {
		return err
	}
	wait := submitWait
	if stalled {
		wait = 0
	}
	if m.awaitSubmit(agent, wait) {
		return nil
	}
	if err := m.Host.SendKeys(agent, "enter"); err != nil {
		return err
	}
	if m.awaitSubmit(agent, submitWait) {
		return nil
	}
	return fmt.Errorf("prompt to %s not submitted: no matching UserPromptSubmit", agent)
}

type handOff struct {
	m         *SessionManager
	agent     string
	fast      bool
	busySince time.Time
	busyTurn  int
	changed   <-chan struct{}
}

func (m *SessionManager) handOff(agent string) *handOff {
	return &handOff{m: m, agent: agent}
}

func (g *handOff) check() (AgentState, bool, error) {
	g.changed = g.m.hookChanged()
	if _, _, held := g.turn(); held {
		return "", false, nil
	}
	state, err := g.m.Host.State(g.agent)
	if err != nil {
		return state, false, err
	}
	seen, busy, held := g.turn()
	if held {
		return state, false, nil
	}
	if seen && !busy {
		return state, state != AgentGone, nil
	}
	return state, state != AgentWorking && state != AgentGone, nil
}

func (g *handOff) turn() (seen, busy, held bool) {
	_ = g.m.drainHooks()
	seen, busy, turn := g.m.hookTurn(g.agent)
	g.fast = seen
	if !busy {
		g.busySince = time.Time{}
		return seen, busy, false
	}
	if !seen {
		return seen, busy, false
	}
	grace := g.m.StallGrace
	if grace <= 0 {
		grace = defaultStallGrace
	}
	now := g.m.now()
	if g.busySince.IsZero() || turn != g.busyTurn {
		g.busySince, g.busyTurn = now, turn
	}
	return seen, busy, now.Sub(g.busySince) < grace
}

func (g *handOff) wait() {
	if g.fast {
		select {
		case <-g.changed:
		case <-time.After(hookPoll):
		}
		return
	}
	poll := g.m.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	time.Sleep(poll)
}

func (m *SessionManager) hookAgent(agent string, fn func(a *hookAgent)) bool {
	h := &m.hooks
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.agents[agent]
	if a == nil {
		return false
	}
	fn(a)
	return true
}

func (m *SessionManager) hookTurn(agent string) (seen, busy bool, turn int) {
	var heard, pinned bool
	seen = m.hookAgent(agent, func(a *hookAgent) { busy, turn, heard, pinned = a.busy, a.turn, a.heard, a.pinned })
	return seen && heard && !pinned, busy, turn
}

func (m *SessionManager) hookExit(agent string) string {
	var exit string
	m.hookAgent(agent, func(a *hookAgent) { exit = a.exit })
	return exit
}

func (m *SessionManager) hookReset(agent string) {
	if m.Hooks == nil {
		return
	}
	_ = m.drainHooks()
	h := &m.hooks
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.agent(agent)
	*a = hookAgent{edits: a.edits, since: m.now()}
}

func (m *SessionManager) hookStart(s *Session, hooks bool) {
	if !hooks || m.Hooks == nil {
		return
	}
	wait := m.HookStartWait
	if wait <= 0 {
		wait = defaultHookStartWait
	}
	deadline := time.Now().Add(wait)
	for {
		_ = m.drainHooks()
		var started bool
		m.hookAgent(s.Agent, func(a *hookAgent) { started = a.started })
		if started {
			return
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(hookPoll)
	}
	m.hooks.mu.Lock()
	m.hooks.agent(s.Agent).pinned = true
	m.hooks.mu.Unlock()
	if m.Face == nil {
		return
	}
	key := s.Ref.Key
	if s.Reviewer != "" {
		key = reviewerKey(key, s.Reviewer)
	}
	m.Face.Emit(Event{
		At:     m.now(),
		Kind:   "warning",
		Phase:  key.Phase,
		Step:   key.Kind,
		Fields: map[string]string{"reason": fmt.Sprintf("hook events: no SessionStart from %s within %s (hook tap %s hook-tap); its turn ends follow herdr", s.Agent, wait, m.HookTap)},
	})
}

func (m *SessionManager) hookNotice(agent string) string {
	_ = m.drainHooks()
	var notice string
	m.hookAgent(agent, func(a *hookAgent) { notice = a.notice })
	return notice
}

func (m *SessionManager) hookActivity(s *Session) string {
	var text string
	m.hookAgent(s.Agent, func(a *hookAgent) { text = activityText(a.tool, a.target, s.Dir, len(a.edits)) })
	return text
}

func activityText(tool, target, dir string, files int) string {
	var text string
	switch {
	case tool == "":
		return ""
	case isEditTool(tool):
		text = "editing " + relPath(dir, target)
	case tool == "Read":
		text = "reading " + relPath(dir, target)
	case tool == "Bash":
		text = "running " + strings.Join(strings.Fields(strings.SplitN(strings.TrimSpace(target), "\n", 2)[0]), " ")
	case tool == "Grep", tool == "Glob":
		text = "searching " + target
	case tool == "Task", tool == "Agent":
		text = "subagent"
	case tool == "mcp__r-loop__ask_watchdog":
		text = "asking watchdog"
	default:
		text = strings.ToLower(tool)
	}
	if files > 0 {
		text += " · " + Plural(files, "file")
	}
	return text
}

func relPath(dir, path string) string {
	if dir == "" || !filepath.IsAbs(path) {
		return path
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}

func (m *SessionManager) showActivity(w *watch) {
	if m.Hooks == nil {
		return
	}
	text := m.hookActivity(w.s)
	if text == w.activity {
		return
	}
	w.activity = text
	show, ok := w.obs.(interface{ Show(Event) })
	if !ok {
		return
	}
	key := w.s.Ref.Key
	if w.s.Reviewer != "" {
		key = reviewerKey(key, w.s.Reviewer)
	}
	show.Show(Event{At: m.now(), Kind: "activity", Phase: key.Phase, Step: key.Kind, Fields: map[string]string{"agent": w.s.Agent, "text": text}})
}

func (m *SessionManager) treeBlame(worker *Session, reviewers []*Session, changed []string, since time.Time) string {
	_ = m.drainHooks()
	editors := map[string][]string{}
	for _, s := range append([]*Session{worker}, reviewers...) {
		who := "step " + worker.Ref.Key.Kind + " edited the tree during review"
		if s != worker {
			who = "reviewer " + s.Reviewer + " modified the tree"
		}
		m.hookAgent(s.Agent, func(a *hookAgent) {
			for path, at := range a.edits {
				if at.Before(since) {
					continue
				}
				file := filepath.ToSlash(relPath(worker.Dir, path))
				if slices.Contains(changed, file) && !slices.Contains(editors[file], who) {
					editors[file] = append(editors[file], who)
				}
			}
		})
	}
	groups := map[string][]string{}
	var order []string
	for _, file := range changed {
		whos := editors[file]
		if len(whos) == 0 {
			whos = []string{"reviewer modified the tree"}
		}
		for _, who := range whos {
			if _, ok := groups[who]; !ok {
				order = append(order, who)
			}
			groups[who] = append(groups[who], file)
		}
	}
	parts := make([]string, len(order))
	for i, who := range order {
		parts[i] = who + ": " + strings.Join(groups[who], ", ")
	}
	return strings.Join(parts, "; ")
}

func (m *SessionManager) hookWarning(w *watch, err error) {
	if show, ok := w.obs.(interface{ Show(Event) }); ok {
		show.Show(Event{At: m.now(), Kind: "warning", Phase: w.s.Ref.Key.Phase, Step: w.s.Ref.Key.Kind, Fields: map[string]string{"reason": "hook events: " + err.Error()}})
	}
}

type showOnly struct {
	nopObserver
	show interface{ Show(Event) }
}

func (o showOnly) Show(ev Event) { o.show.Show(ev) }

func showsOnly(obs Observer) Observer {
	if s, ok := obs.(interface{ Show(Event) }); ok {
		return showOnly{show: s}
	}
	return nopObserver{}
}
