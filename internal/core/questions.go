package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	answeredByWatchdog = "watchdog"
	maintainerCitation = "maintainer"
	defaultRouterPoll  = 5 * time.Second
	watchdogGone       = "the watchdog is gone"
)

var citationPattern = regexp.MustCompile(`^[^\s:]+:\d+$`)

type QuestionRouter struct {
	Dog     *Watchdog
	Deliver func(id, answer, by, citation string) error
	Repo    Repo

	poll time.Duration
	mu   sync.Mutex
	open map[string]chan struct{}
}

func (r *QuestionRouter) Route(ctx context.Context, q Question) bool {
	if r.Dog == nil || !r.Dog.live() {
		return false
	}
	done := make(chan struct{})
	r.mu.Lock()
	if r.open == nil {
		r.open = map[string]chan struct{}{}
	}
	r.open[q.ID] = done
	r.mu.Unlock()
	text := fmt.Sprintf("question %s from phase-%d/%s: %s options: %s recommended: %s", q.ID, q.Step.Phase, q.Step.Kind, q.Text, strings.Join(q.Options, ", "), q.Recommended)
	if err := r.Dog.Notify(text, false, 0); err != nil {
		r.close(q.ID)
		return false
	}
	poll := r.poll
	if poll <= 0 {
		poll = defaultRouterPoll
	}
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return true
		case <-tick.C:
			if r.Dog.live() {
				continue
			}
		case <-ctx.Done():
		}
		if r.close(q.ID) {
			return false
		}
		<-done
		return true
	}
}

func (r *QuestionRouter) close(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.open[id]
	delete(r.open, id)
	return ok
}

func (r *QuestionRouter) Answer(id, answer, citation string) (bool, string) {
	citation = strings.TrimSpace(citation)
	if reason := r.rejectCitation(citation); reason != "" {
		return false, reason + "; the question stays open"
	}
	r.mu.Lock()
	done, ok := r.open[id]
	delete(r.open, id)
	r.mu.Unlock()
	if !ok {
		return false, fmt.Sprintf("question %s is not open", id)
	}
	by := answeredByWatchdog
	if citation == maintainerCitation {
		by, citation = maintainerCitation, ""
	}
	err := r.Deliver(id, answer, by, citation)
	close(done)
	if err != nil {
		return false, err.Error()
	}
	return true, ""
}

func (r *QuestionRouter) rejectCitation(citation string) string {
	if citation == maintainerCitation {
		return ""
	}
	if citation == "" {
		return "citation is empty: cite path:line, or maintainer once the maintainer answered in your session"
	}
	if !citationPattern.MatchString(citation) {
		return fmt.Sprintf("citation %q is not path:line or maintainer", citation)
	}
	path := filepath.Clean(citation[:strings.LastIndex(citation, ":")])
	if filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
		return fmt.Sprintf("citation %s is outside the repository", path)
	}
	if path == ".r-loop" || strings.HasPrefix(path, ".r-loop/") {
		return fmt.Sprintf("citation %s is under .r-loop/", path)
	}
	info, err := os.Stat(filepath.Join(r.Repo.Root(), path))
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Sprintf("citation %s is not a file in the primary tree", path)
	}
	return ""
}
