package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	actionRetry  = "retry"
	actionKeys   = "keys"
	actionSwitch = "switch"
	actionSkip   = "skip"
	actionBlock  = "block"
	actionStop   = "stop"

	sourceStep      = "step"
	sourceReviewer  = "reviewer"
	sourceLand      = "land"
	sourceGatefix   = "gatefix"
	sourceGateProbe = "gate-probe"
	sourceMilestone = "milestone"

	byTimeout        = "timeout"
	byWithdrawn      = "withdrawn"
	citationAlways   = "always"
	citationFallback = "fallback"
	runStopped       = "run stopped"

	unattendedBlockOrStop = "unattended: block or stop"
	askForBlocker         = "ask the maintainer in your session: what blocks, what you tried and the options; then call again with maintainer_said set to their reply"
)

var blockerOptions = []struct{ action, option string }{
	{actionRetry, "retry"}, {actionSkip, "skip"}, {actionSwitch, "switch provider"}, {actionBlock, "block this phase"}, {actionStop, "stop the run"},
}

var blockerID = regexp.MustCompile(`^b\d+$`)

type blockerHold struct {
	key     StepKey
	out     *Outcome
	restart *Restart
	halt    *Signal
}

func (l *RunLoop) holds() bool {
	return l.Watcher != nil && l.BlockerTimeout > 0
}

func (l *RunLoop) TryRaise(ctx context.Context, b Blocker) (Resolution, bool) {
	if !l.holds() {
		return Resolution{}, false
	}
	return l.Raise(ctx, b), true
}

func (l *RunLoop) Raise(ctx context.Context, b Blocker) Resolution {
	hold := &blockerHold{key: StepKey{Run: l.RunID, Phase: b.Phase, Kind: b.Step}, out: &Outcome{}}
	res := l.raise(ctx, b, hold)
	if hold.restart != nil {
		hold.restart.answer("")
	}
	if hold.halt != nil {
		res.halt = hold.out.Reason
		if b.Source == sourceMilestone {
			if l.halted == nil {
				l.halted = hold.halt
			}
			l.haltLanded = true
		}
	}
	return res
}

func stoppedAt(id, reason string) string {
	return fmt.Sprintf("stopped by the watchdog at %s: %s", id, strings.Join(strings.Fields(reason), " "))
}

type runStop struct {
	reason, phase, step string
}

func (l *RunLoop) markStopped(id string, b Blocker) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped.reason == "" {
		l.stopped = runStop{reason: stoppedAt(id, b.Reason), phase: b.Phase, step: b.Step}
	}
}

func (l *RunLoop) stopping() runStop {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stopped
}

func (l *RunLoop) PostHalting(reason string) {
	if p, ok := l.watcher().(interface{ Post(string) }); ok {
		quietly(func() { p.Post("run halting: " + reason) })
	}
}

func (l *RunLoop) raise(ctx context.Context, b Blocker, main *blockerHold) Resolution {
	res := l.hold(ctx, b, main)
	if res.Action == actionStop {
		l.markStopped(res.ID, b)
	}
	return res
}

func (l *RunLoop) hold(ctx context.Context, b Blocker, main *blockerHold) Resolution {
	b.Reason = strings.Join(strings.Fields(b.Reason), " ")
	id, err := l.nextID(QuestionBlocker, "b", &l.blockerSeq)
	if err != nil {
		l.emit(Event{Kind: "warning", Phase: b.Phase, Step: b.Step, Fields: map[string]string{"reason": "blocker: " + err.Error()}})
		return Resolution{Action: actionBlock, By: byWithdrawn}
	}
	s, agent := l.blockerSession(b)
	key := StepKey{Run: l.RunID, Phase: b.Phase, Kind: b.Step}
	if s != nil {
		key = s.Ref.Key
		key.Kind = b.Step
	}
	q := Question{ID: id, Kind: QuestionBlocker, Step: key, Text: blockerText(id, b), Options: b.Actions, AskedAt: time.Now()}
	open := openAsk{q: q, s: s, agent: agent, b: b, done: make(chan Resolution, 1)}
	admitted := s != nil && s.live(func() {
		l.recordQuestion(q)
		l.trackOpen(open)
		if l.openQuestion(s, 1) == 1 {
			l.stepState(s, StepWaitingInput)
		}
	})
	if !admitted {
		open.s, open.agent = nil, ""
		l.recordQuestion(q)
		l.trackOpen(open)
		if h, ok := l.watcher().(remedyHolder); ok && b.Source != sourceStep {
			h.Hold(key)
			defer h.Release(key)
		}
	}
	l.emit(Event{Kind: "blocked-on", Phase: b.Phase, Step: b.Step, Fields: map[string]string{"id": id, "source": b.Source, "phase": b.Phase, "step": b.Step, "reason": b.Reason}})
	routed := make(chan bool, 1)
	l.questions.Add(1)
	go func() {
		defer l.questions.Done()
		defer func() {
			if r := recover(); r != nil {
				ev, err := panicked(b.Phase, b.Step, "blocker "+id, r)
				quietly(func() { l.emit(ev) })
				quietly(func() { l.withdrawBlocker(id, err.Error()) })
			}
		}()
		routed <- l.watcher().Route(ctx, q)
	}()
	return l.wait(ctx, open, routed, main)
}

func (l *RunLoop) wait(ctx context.Context, open openAsk, routed <-chan bool, main *blockerHold) Resolution {
	id := open.q.ID
	var signals <-chan Signal
	var restarts <-chan Restart
	if l.signalsMu.TryLock() {
		defer l.signalsMu.Unlock()
		signals, restarts = l.watcher().Signals(), l.watcher().Restarts()
	}
	left := l.BlockerTimeout
	ticker := time.NewTicker(l.poll())
	defer ticker.Stop()
	last := time.Now()
	for {
		select {
		case res := <-open.done:
			return res
		case ok := <-routed:
			routed = nil
			if !ok && ctx.Err() == nil {
				l.withdrawBlocker(id, watchdogGone)
				return <-open.done
			}
		case <-ctx.Done():
			l.withdrawBlocker(id, runStopped)
			return <-open.done
		case sig := <-signals:
			if l.haltsWindow(sig, main.key, main.out) {
				main.halt = &sig
				l.withdrawBlocker(id, main.out.Reason)
				return <-open.done
			}
		case rs := <-restarts:
			if res, done := l.restartBlocker(open, main, rs); done {
				return res
			}
		case now := <-ticker.C:
			if l.Store.Aborted(l.RunID) {
				l.withdrawBlocker(id, runStopped)
				return <-open.done
			}
			if !l.dogWaiting() {
				left -= now.Sub(last)
			}
			last = now
			if left <= 0 {
				l.expire(id)
				return <-open.done
			}
		}
	}
}

func (l *RunLoop) restartBlocker(open openAsk, main *blockerHold, rs Restart) (Resolution, bool) {
	key, id := main.key, open.q.ID
	if open.b.Source != sourceStep {
		rs.answer(fmt.Sprintf("blocker %s from phase-%s/%s is not a step's: resolve it with resolve_blocker", id, open.b.Phase, open.b.Step))
		return Resolution{}, false
	}
	if rs.Step != key {
		rs.answer(fmt.Sprintf("phase-%s/%s attempt %d is not waiting for a restart", rs.Step.Phase, rs.Step.Kind, rs.Step.Attempt))
		return Resolution{}, false
	}
	if l.drainHalts(key, main.out) {
		rs.answer("run halted: " + main.out.Reason)
		l.withdrawBlocker(id, main.out.Reason)
		return <-open.done, true
	}
	step := fmt.Sprintf("phase-%s/%s", key.Phase, key.Kind)
	if l.restarts[step] >= l.MaxRestarts {
		reason := fmt.Sprintf("restart limit %d reached", l.MaxRestarts)
		l.emit(Event{Kind: "restart-refused", Phase: key.Phase, Step: key.Kind, Fields: map[string]string{"step": step, "reason": reason}})
		rs.answer(reason)
		l.withdrawBlocker(id, reason)
		return <-open.done, true
	}
	res := Resolution{ID: id, Action: actionRetry, By: answeredByWatchdog, Citation: rs.Remedy, Addendum: rs.Addendum, Provider: rs.Provider, Model: rs.Model, Effort: rs.Effort}
	if err := l.SettleBlocker(res); err != nil {
		rs.answer(err.Error())
		return Resolution{}, false
	}
	main.restart = &rs
	return <-open.done, true
}

func (l *RunLoop) dogWaiting() bool {
	w, ok := l.watcher().(interface{ Waiting() bool })
	return ok && w.Waiting()
}

func (l *RunLoop) blockerSession(b Blocker) (*Session, string) {
	s := l.liveSession()
	if s == nil {
		return nil, ""
	}
	k := s.Ref.Key
	if k.Phase != b.Phase || b.Step != k.Kind && !strings.HasPrefix(b.Step, k.Kind+"-rv-") {
		return nil, ""
	}
	agent := s.asker(b.Step)
	if agent == "" {
		return nil, ""
	}
	return s, agent
}

func blockerText(id string, b Blocker) string {
	text := fmt.Sprintf("blocker %s from phase-%s/%s (%s): %s\nactions: %s; resolve it with resolve_blocker", id, b.Phase, b.Step, b.Source, b.Reason, strings.Join(b.Actions, ", "))
	excerpt := strings.TrimRight(b.Excerpt, " \t\r\n")
	if len(excerpt) > screenBytes {
		excerpt = excerpt[len(excerpt)-screenBytes:]
		for excerpt != "" && !utf8.RuneStart(excerpt[0]) {
			excerpt = excerpt[1:]
		}
	}
	if excerpt != "" {
		text += "\n\n" + excerpt
	}
	return text
}

func (l *RunLoop) trackOpen(open openAsk) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.asked == nil {
		l.asked = map[string]openAsk{}
	}
	l.asked[open.q.ID] = open
}

func (l *RunLoop) openBlocker(id string) (openAsk, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	open, ok := l.asked[id]
	return open, ok && open.q.Kind == QuestionBlocker
}

func (l *RunLoop) OpenBlocker(id string) (Blocker, bool) {
	open, ok := l.openBlocker(id)
	return open.b, ok
}

func (l *RunLoop) StepBlocker(phase, kind string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var ids []string
	for id, a := range l.asked {
		if a.q.Kind == QuestionBlocker && a.b.Source == sourceStep && a.b.Phase == phase && a.b.Step == kind {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "", false
	}
	slices.Sort(ids)
	return ids[0], true
}

func (l *RunLoop) SettleBlocker(res Resolution) error {
	open, ok := l.openBlocker(res.ID)
	err := fmt.Errorf("blocker %s is not open", res.ID)
	if !ok {
		return err
	}
	if res.Action == actionKeys && open.agent == "" {
		return fmt.Errorf("blocker %s has no pane to press keys in", res.ID)
	}
	run := func() {
		if res.Action == actionKeys {
			if reason := l.paneMoved(open); reason != "" {
				err = errors.New(reason)
				return
			}
		}
		if _, ok := l.claim(res.ID); !ok {
			return
		}
		err = nil
		l.finish(open, res, res.Action, open.s != nil, func() {
			if res.Action != actionKeys {
				return
			}
			if serr := l.Sessions.Host.SendKeys(open.agent, res.Keys...); serr != nil {
				err = fmt.Errorf("keys %s for blocker %s not sent: %w", strings.Join(res.Keys, " "), res.ID, serr)
				l.emit(Event{Kind: "warning", Phase: open.b.Phase, Step: open.b.Step, Fields: map[string]string{"reason": err.Error()}})
			}
		})
	}
	if open.s != nil {
		open.s.live(run)
	} else {
		run()
	}
	return err
}

func (l *RunLoop) paneMoved(open openAsk) string {
	if open.b.Excerpt == "" {
		return ""
	}
	raw, err := l.Sessions.Host.Screen(open.agent)
	if err != nil {
		return fmt.Sprintf("blocker %s: screen: %s", open.q.ID, err)
	}
	if normaliseScreen(raw) != normaliseScreen(open.b.Excerpt) {
		return fmt.Sprintf("the pane of blocker %s changed since it was raised; read it again", open.q.ID)
	}
	return ""
}

func (l *RunLoop) expire(id string) {
	open, ok := l.openBlocker(id)
	if !ok {
		return
	}
	action := actionBlock
	if !slices.Contains(open.b.Actions, actionBlock) && slices.Contains(open.b.Actions, actionSkip) {
		action = actionSkip
	}
	l.closeBlocker(open, Resolution{ID: id, Action: action, By: byTimeout, Citation: byTimeout}, action)
}

func (l *RunLoop) withdrawBlocker(id, reason string) {
	if open, ok := l.openBlocker(id); ok {
		l.closeBlocker(open, Resolution{ID: id, Action: actionBlock, By: byWithdrawn}, reason)
	}
}

func (l *RunLoop) closeBlocker(open openAsk, res Resolution, answer string) {
	if open.s != nil && open.s.live(func() {
		if _, ok := l.claim(open.q.ID); ok {
			l.finish(open, res, answer, true, nil)
		}
	}) {
		return
	}
	if _, ok := l.claim(open.q.ID); ok {
		l.finish(open, res, answer, false, nil)
	}
}

func (l *RunLoop) finish(open openAsk, res Resolution, answer string, release bool, act func()) {
	q := open.q
	q.Answer, q.AnsweredBy, q.Citation, q.AnsweredAt = answer, res.By, res.Citation, time.Now()
	l.recordQuestion(q)
	if w, ok := l.watcher().(interface{ Withdraw(id string) }); ok {
		w.Withdraw(q.ID)
	}
	b := open.b
	l.emit(Event{Kind: "blocker-resolved", Phase: b.Phase, Step: b.Step, Fields: map[string]string{"id": q.ID, "action": res.Action, "by": res.By, "source": b.Source, "step": fmt.Sprintf("phase-%s/%s", b.Phase, b.Step)}})
	if res.By == maintainerCitation {
		l.emit(Event{Kind: "human", Phase: b.Phase, Step: b.Step, Fields: map[string]string{"what": "blocker", "id": q.ID}})
	}
	if act != nil {
		act()
	}
	if release {
		l.release(open.s)
	}
	open.done <- res
}

func (r *QuestionRouter) ResolveBlocker(id, action, rule, addendum string, keys []string, provider, model, effort, maintainerSaid string) (string, string) {
	var b Blocker
	ok := false
	if r.Blocker != nil {
		b, ok = r.Blocker(id)
	}
	if !ok {
		return decisionRefused, fmt.Sprintf("blocker %s is not open", id)
	}
	if !slices.Contains(b.Actions, action) {
		return decisionRefused, fmt.Sprintf("%s is not an action for blocker %s (%s): %s", action, id, b.Source, orList(b.Actions))
	}
	rem := r.Remedies
	if rem == nil {
		rem = &Remedies{}
	}
	if action == actionRetry || action == actionSwitch {
		target := fmt.Sprintf("phase-%s/%s", b.Phase, b.Step)
		n, err := r.retries(target)
		if err != nil {
			return decisionRefused, err.Error()
		}
		if n >= rem.MaxRestarts {
			return decisionRefused, fmt.Sprintf("%s limit %d reached for %s", action, rem.MaxRestarts, target)
		}
	}
	res := Resolution{ID: id, Action: action, By: answeredByWatchdog}
	authorised := false
	maintainer := strings.TrimSpace(maintainerSaid) != ""
	switch action {
	case actionBlock, actionStop:
		res.Citation, authorised = citationAlways, true
	case actionRetry:
		res.Addendum = addendum
		if slices.Contains(rem.Allow, "restart") {
			res.Citation, authorised = consentAllowList, true
		}
	case actionKeys:
		if len(keys) == 0 || slices.ContainsFunc(keys, func(k string) bool { return strings.TrimSpace(k) == "" }) {
			return decisionRefused, "keys are empty: name the keys to press, such as enter, esc, down or a digit"
		}
		res.Keys = keys
		rule = strings.TrimSpace(rule)
		if rule != "" && slices.ContainsFunc(r.Rules, func(s string) bool { return strings.TrimSpace(s) == rule }) {
			res.Citation, authorised = rule, true
		}
	case actionSwitch:
		if provider == "" {
			return decisionRefused, "switch needs a provider"
		}
		if rem.Asks != nil && !rem.Asks(provider) {
			return decisionRefused, "provider " + provider + " has no MCP ask channel"
		}
		base, _, _ := strings.Cut(b.Step, "-rv-")
		if fb := rem.Fallbacks[base]; fb.Provider == provider {
			if model == "" {
				model, effort = fb.Model, fb.Effort
			}
			res.Citation, authorised = citationFallback, true
		} else if maintainer && (model == "" || effort == "") {
			return decisionRefused, fmt.Sprintf("switch to %s needs a model and an effort: it is not the row's fallback", provider)
		}
		res.Provider, res.Model, res.Effort = provider, model, effort
	}
	if !authorised {
		switch {
		case r.Dog != nil && r.Dog.Unattended:
			return decisionRefused, unattendedBlockOrStop
		case maintainer:
			res.By, res.Citation = maintainerCitation, maintainerCitation
		default:
			if r.Dog != nil {
				if err := r.Dog.AskMaintainer(fmt.Sprintf("blocker %s from phase-%s/%s (%s): %s — what now?", id, b.Phase, b.Step, b.Source, b.Reason), blockerChoices(b.Actions), ""); err != nil {
					return decisionRefused, err.Error()
				}
			}
			return decisionAsk, askForBlocker
		}
	}
	if r.Settle == nil {
		return decisionRefused, fmt.Sprintf("blocker %s is not open", id)
	}
	if err := r.Settle(res); err != nil {
		return decisionRefused, err.Error()
	}
	return decisionAuthorised, ""
}

func (r *QuestionRouter) retries(target string) (int, error) {
	if r.Remedies == nil || r.Remedies.Store == nil || r.Dog == nil {
		return 0, nil
	}
	st, err := r.Remedies.Store.Load(r.Dog.RunID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ev := range st.Events {
		f := ev.Fields
		switch {
		case ev.Kind == "blocker-resolved" && (f["action"] == actionRetry || f["action"] == actionSwitch) && f["step"] == target:
			n++
		case ev.Kind == "restart" && f["step"] == target && !blockerID.MatchString(f["remedy"]):
			n++
		}
	}
	return n, nil
}

func blockerChoices(actions []string) []string {
	var out []string
	for _, o := range blockerOptions {
		if slices.Contains(actions, o.action) {
			out = append(out, o.option)
		}
	}
	return out
}

func orList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

func BlockerLine(q Question) string {
	first, _, _ := strings.Cut(q.Text, "\n")
	line := q.ID + " " + strings.TrimPrefix(first, "blocker "+q.ID+" from ")
	if q.AnsweredBy == "" {
		return line + " (open)"
	}
	return fmt.Sprintf("%s → %s (%s)", line, q.Answer, q.AnsweredBy)
}
