package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	StopAfterPhase = "after-phase"
	StopNow        = "now"
	noConsent      = "maintainer_said is empty: quote the maintainer's reply"
	noReason       = "reason is empty: say why, in the maintainer's terms"
)

type runControl struct {
	stopAfter  string
	pauseAfter string
	paused     bool
	continued  bool
	unpause    chan struct{}
}

func (l *RunLoop) StopRun(when, reason, maintainerSaid string) (bool, string) {
	if when != StopAfterPhase && when != StopNow {
		return false, fmt.Sprintf("when %q is not %s or %s", when, StopAfterPhase, StopNow)
	}
	if oneLine(reason) == "" {
		return false, noReason
	}
	if err := l.consent("stop_run", reason, maintainerSaid); err != nil {
		return false, err.Error()
	}
	if when == StopNow {
		if err := l.Store.MarkAbort(l.RunID); err != nil {
			return false, "abort: " + err.Error()
		}
		l.wake()
		return true, ""
	}
	l.mu.Lock()
	l.control.stopAfter = oneLine(reason)
	l.mu.Unlock()
	l.emit(Event{Kind: "stop-requested", Fields: map[string]string{"reason": oneLine(reason)}})
	l.wake()
	return true, ""
}

func (l *RunLoop) PauseRun(reason, maintainerSaid string) (bool, string) {
	l.mu.Lock()
	c := l.control
	l.mu.Unlock()
	switch {
	case c.paused:
		return false, "the run is already paused"
	case c.pauseAfter != "":
		return false, "a pause is already pending"
	}
	if oneLine(reason) == "" {
		return false, noReason
	}
	if err := l.consent("pause_run", reason, maintainerSaid); err != nil {
		return false, err.Error()
	}
	l.mu.Lock()
	l.control.pauseAfter = oneLine(reason)
	l.mu.Unlock()
	l.emit(Event{Kind: "pause-requested", Fields: map[string]string{"reason": oneLine(reason)}})
	return true, ""
}

func (l *RunLoop) ContinueRun(maintainerSaid string) (bool, string) {
	l.mu.Lock()
	c := l.control
	l.mu.Unlock()
	if !c.paused && c.pauseAfter == "" {
		return false, "the run is not paused"
	}
	if err := l.consent("continue_run", "", maintainerSaid); err != nil {
		return false, err.Error()
	}
	l.mu.Lock()
	pending := !l.control.paused && l.control.pauseAfter != ""
	l.control.pauseAfter = ""
	if l.control.paused {
		l.control.continued = true
	}
	l.mu.Unlock()
	if pending {
		l.emit(Event{Kind: "continued", Fields: map[string]string{"pending": "true"}})
		return true, ""
	}
	l.wake()
	return true, ""
}

func (l *RunLoop) consent(tool, reason, maintainerSaid string) error {
	if strings.TrimSpace(maintainerSaid) == "" {
		return fmt.Errorf("%s", noConsent)
	}
	ev := Event{At: time.Now(), Kind: "human", Fields: map[string]string{"what": "consent", "tool": tool, "reason": oneLine(reason), "maintainer_said": maintainerSaid}}
	if err := l.Store.Append(l.RunID, Record{Kind: RecordEvent, At: ev.At, Event: &ev}); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	l.Face.Emit(ev)
	return nil
}

func (l *RunLoop) wake() {
	l.mu.Lock()
	ch := l.unpauseChan()
	l.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (l *RunLoop) unpauseChan() chan struct{} {
	if l.control.unpause == nil {
		l.control.unpause = make(chan struct{}, 1)
	}
	return l.control.unpause
}

func (l *RunLoop) atBoundary(ctx context.Context, phase string) (brk bool, code int, exit bool) {
	l.mu.Lock()
	c := l.control
	last := len(l.pending) == 0
	if last {
		l.control.stopAfter, l.control.pauseAfter = "", ""
	} else if c.stopAfter == "" && c.pauseAfter != "" {
		l.control.pauseAfter, l.control.paused, l.control.continued = "", true, false
	}
	l.mu.Unlock()
	if last {
		for _, r := range []struct{ what, reason string }{{"stop", c.stopAfter}, {"pause", c.pauseAfter}} {
			if r.reason != "" {
				l.emit(Event{Kind: "warning", Phase: phase, Fields: map[string]string{"reason": fmt.Sprintf("%s requested after phase %s, the last one: the run ends here anyway", r.what, phase)}})
			}
		}
		return false, 0, false
	}
	if l.stopAfterPhase(phase) {
		return true, 0, false
	}
	if c.pauseAfter == "" {
		return false, 0, false
	}
	return l.waitPaused(ctx, phase, c.pauseAfter)
}

func (l *RunLoop) resumed() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.control.continued && l.control.stopAfter == ""
}

func (l *RunLoop) stopAfterPhase(phase string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.control.stopAfter == "" {
		return false
	}
	if l.stopped.reason == "" {
		l.stopped = runStop{reason: "stopped by the watchdog: " + l.control.stopAfter, phase: phase}
	}
	return true
}

func (l *RunLoop) waitPaused(ctx context.Context, phase, reason string) (bool, int, bool) {
	l.mu.Lock()
	wake := l.unpauseChan()
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.control.paused, l.control.continued = false, false
		l.mu.Unlock()
	}()
	l.setRun(RunPaused, reason)
	l.emit(Event{Kind: "paused", Phase: phase, Fields: map[string]string{"reason": reason}})
	if p, ok := l.watcher().(interface{ Post(string) }); ok {
		quietly(func() {
			p.Post("run paused after phase " + phase + ": call continue_run or stop_run when the maintainer says")
		})
	}
	tick := time.NewTicker(l.poll())
	defer tick.Stop()
	for {
		if l.Store.Aborted(l.RunID) || ctx.Err() != nil {
			l.stop(ctx, phase, "")
			return false, l.stopCode(), true
		}
		if l.stopAfterPhase(phase) {
			return true, 0, false
		}
		if l.dogGone(phase) {
			return true, 0, false
		}
		if l.resumed() {
			l.setRun(RunRunning, "")
			l.emit(Event{Kind: "continued", Phase: phase})
			return false, 0, false
		}
		select {
		case <-wake:
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
