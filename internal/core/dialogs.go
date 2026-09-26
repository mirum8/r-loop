package core

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	screenLines  = 40
	screenBytes  = 4096
	closedInPane = "dialog closed in the pane"
)

func (l *RunLoop) Blocked(s *Session) bool {
	owner := s
	if s.owner != nil {
		owner = s.owner
	}
	if !l.isStepKind(s.Ref.Key.Kind) {
		return false
	}
	key := s.Ref.Key
	if s.Reviewer != "" {
		key = reviewerKey(key, s.Reviewer)
	}
	l.mu.Lock()
	ctx, live := l.askCtx, l.live
	_, open := l.openDialog(s.Agent)
	last := l.screens[s.Agent]
	l.mu.Unlock()
	if ctx == nil || live != owner {
		return false
	}
	if open {
		return true
	}
	raw, err := l.Sessions.Host.Screen(s.Agent)
	if err != nil {
		return false
	}
	screen := normaliseScreen(raw)
	if screen == "" || screen == last {
		return false
	}
	var q Question
	raised := false
	admitted := owner.live(func() {
		l.mu.Lock()
		_, open = l.openDialog(s.Agent)
		answered := l.screens[s.Agent] == screen
		l.mu.Unlock()
		if open || answered {
			return
		}
		id, err := l.nextDialog()
		if err != nil {
			return
		}
		raised = true
		q = Question{ID: id, Kind: QuestionDialog, Step: key, Text: screen, AskedAt: time.Now()}
		l.recordQuestion(q)
		l.track(q, owner, s.Agent)
		l.emit(Event{Kind: "dialog", Phase: key.Phase, Step: key.Kind, Fields: map[string]string{"id": q.ID, "agent": s.Agent, "text": screen}})
		if l.openQuestion(owner, 1) == 1 {
			l.stepState(owner, StepWaitingInput)
		}
	})
	if !admitted || !raised {
		return admitted && open
	}
	l.questions.Add(1)
	go func() {
		defer l.questions.Done()
		defer func() {
			if r := recover(); r != nil {
				l.dialogPanicked(q, r)
			}
		}()
		l.watcher().Route(ctx, q)
	}()
	return true
}

func (l *RunLoop) isStepKind(kind string) bool {
	for _, k := range l.Kinds {
		if k.Name == kind {
			return true
		}
	}
	return false
}

func (l *RunLoop) Unblocked(s *Session) {
	l.mu.Lock()
	delete(l.screens, s.Agent)
	open, ok := l.openDialog(s.Agent)
	l.mu.Unlock()
	if ok {
		l.withdrawDialog(open, closedInPane)
	}
}

func (l *RunLoop) DeliverKeys(id string, keys []string, by, rule string) error {
	l.mu.Lock()
	open, ok := l.asked[id]
	l.mu.Unlock()
	err := fmt.Errorf("dialog %s is not open", id)
	if !ok || open.q.Kind != QuestionDialog {
		return err
	}
	open.s.live(func() {
		if _, ok := l.claim(id); !ok {
			return
		}
		if reason := l.moved(open); reason != "" {
			l.closeDialog(open, reason)
			err = fmt.Errorf("dialog %s is gone: %s", id, reason)
			return
		}
		q := open.q
		q.Answer, q.AnsweredBy, q.Citation, q.AnsweredAt = strings.Join(keys, " "), by, rule, time.Now()
		l.recordQuestion(q)
		l.emit(Event{Kind: "dialog-answered", Phase: q.Step.Phase, Step: q.Step.Kind, Fields: map[string]string{"id": id, "keys": q.Answer, "by": by, "rule": rule}})
		if by == maintainerCitation {
			l.emit(Event{Kind: "human", Phase: q.Step.Phase, Step: q.Step.Kind, Fields: map[string]string{"what": "dialog", "id": id}})
		}
		err = l.pressKeys(id, open.agent, keys)
		if err != nil {
			l.emit(Event{Kind: "warning", Phase: q.Step.Phase, Step: q.Step.Kind, Fields: map[string]string{"reason": err.Error()}})
		} else {
			l.mu.Lock()
			if l.screens == nil {
				l.screens = map[string]string{}
			}
			l.screens[open.agent] = q.Text
			l.mu.Unlock()
		}
		l.release(open.s)
	})
	return err
}

func (l *RunLoop) pressKeys(id, agent string, keys []string) error {
	for i, k := range keys {
		if i > 0 {
			state, serr := l.Sessions.Host.State(agent)
			if serr != nil || state != AgentBlocked {
				why := fmt.Sprintf("the agent is %s", state)
				if serr != nil {
					why = "state: " + serr.Error()
				}
				return fmt.Errorf("dialog %s closed before %s was pressed: %s", id, strings.Join(keys[i:], " "), why)
			}
		}
		if serr := l.Sessions.Host.SendKeys(agent, k); serr != nil {
			return fmt.Errorf("keys %s for dialog %s not sent: %w", strings.Join(keys[i:], " "), id, serr)
		}
	}
	return nil
}

func (l *RunLoop) moved(open openAsk) string {
	state, err := l.Sessions.Host.State(open.agent)
	if err != nil {
		return "state: " + err.Error()
	}
	if state != AgentBlocked {
		return fmt.Sprintf("the agent is %s, not blocked", state)
	}
	raw, err := l.Sessions.Host.Screen(open.agent)
	if err != nil {
		return "screen: " + err.Error()
	}
	if normaliseScreen(raw) != open.q.Text {
		return "the screen changed"
	}
	return ""
}

func (l *RunLoop) openDialog(agent string) (openAsk, bool) {
	for _, a := range l.asked {
		if a.q.Kind == QuestionDialog && a.agent == agent {
			return a, true
		}
	}
	return openAsk{}, false
}

func (l *RunLoop) withdrawDialog(open openAsk, reason string) {
	open.s.live(func() {
		if _, ok := l.claim(open.q.ID); ok {
			l.closeDialog(open, reason)
		}
	})
}

func (l *RunLoop) closeDialog(open openAsk, reason string) {
	if w, ok := l.watcher().(interface{ Withdraw(id string) }); ok {
		w.Withdraw(open.q.ID)
	}
	l.recordWithdrawn(open.q, reason)
	l.emitDialogClosed(open.q, reason)
	l.release(open.s)
}

func (l *RunLoop) emitDialogClosed(q Question, reason string) {
	l.emit(Event{Kind: "dialog-closed", Phase: q.Step.Phase, Step: q.Step.Kind, Fields: map[string]string{"id": q.ID, "reason": reason}})
}

func (l *RunLoop) dialogPanicked(q Question, v any) {
	ev, err := panicked(q.Step.Phase, q.Step.Kind, "dialog "+q.ID, v)
	quietly(func() { l.emit(ev) })
	l.mu.Lock()
	open, ok := l.asked[q.ID]
	l.mu.Unlock()
	if ok {
		quietly(func() { l.withdrawDialog(open, err.Error()) })
	}
}

func (l *RunLoop) nextDialog() (string, error) {
	return l.nextID(QuestionDialog, "d", &l.dialogSeq)
}

func (l *RunLoop) nextID(kind, prefix string, seq *int) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if *seq == 0 {
		st, err := l.Store.Load(l.RunID)
		if err != nil {
			return "", err
		}
		for _, q := range st.Questions {
			if n, err := strconv.Atoi(strings.TrimPrefix(q.ID, prefix)); q.Kind == kind && err == nil && n > *seq {
				*seq = n
			}
		}
	}
	*seq++
	return prefix + strconv.Itoa(*seq), nil
}

func normaliseScreen(raw string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > screenLines {
		lines = lines[len(lines)-screenLines:]
	}
	size := len(lines) - 1
	for _, line := range lines {
		size += len(line)
	}
	for len(lines) > 0 && size > screenBytes {
		size -= len(lines[0]) + 1
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

func DialogLine(q Question) string {
	line := fmt.Sprintf("%s phase-%s/%s: dialog", q.ID, q.Step.Phase, q.Step.Kind)
	if q.AnsweredBy == "" {
		return line + " (open)"
	}
	who := q.AnsweredBy
	if q.Citation != "" {
		who += ", " + q.Citation
	}
	return fmt.Sprintf("%s → %s (%s)", line, q.Answer, who)
}
