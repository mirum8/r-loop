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

const answeredByWatchdog = "watchdog"

var citationPattern = regexp.MustCompile(`^[^\s:]+:\d+$`)

const maintainerCitation = "maintainer:"

type QuestionRouter struct {
	Dog          *Watchdog
	Deliver      func(id, answer, by, citation string) error
	Answered     func(id string) (Question, bool)
	Repo         Repo
	AnswerWindow time.Duration

	mu   sync.Mutex
	open map[string]routed
}

type routed struct {
	done chan bool
}

func (r *QuestionRouter) Route(ctx context.Context, q Question) bool {
	if r.Dog == nil || !r.Dog.live() {
		return false
	}
	p := routed{done: make(chan bool, 1)}
	r.mu.Lock()
	if r.open == nil {
		r.open = map[string]routed{}
	}
	r.open[q.ID] = p
	r.mu.Unlock()
	text := fmt.Sprintf("question %s from phase-%d/%s: %s options: %s", q.ID, q.Step.Phase, q.Step.Kind, q.Text, strings.Join(q.Options, ", "))
	if err := r.Dog.Notify(text, false, 0); err != nil {
		r.close(q.ID)
		return false
	}
	tick := time.NewTicker(r.AnswerWindow)
	defer tick.Stop()
	for waiting := true; waiting; {
		select {
		case ok := <-p.done:
			return ok
		case <-tick.C:
			waiting = r.Dog.live()
		case <-ctx.Done():
			waiting = false
		}
	}
	if r.close(q.ID) {
		return false
	}
	return <-p.done
}

func (r *QuestionRouter) close(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.open[id]
	delete(r.open, id)
	return ok
}

func (r *QuestionRouter) Answer(id, answer, citation string) (bool, string) {
	r.mu.Lock()
	p, ok := r.open[id]
	delete(r.open, id)
	r.mu.Unlock()
	if !ok {
		return false, fmt.Sprintf("question %s is not open", id)
	}
	if citation == "" {
		p.done <- false
		return false, "escalated to the maintainer"
	}
	if qid, ok := strings.CutPrefix(citation, maintainerCitation); ok {
		if a, found := r.answered(qid); !found || a.Step.Kind != "watchdog" || a.AnsweredBy != "maintainer" {
			r.reopen(id, p)
			return false, fmt.Sprintf("%s is not a question the maintainer answered", qid)
		}
		err := r.Deliver(id, answer, "maintainer", citation)
		p.done <- true
		if err != nil {
			return false, err.Error()
		}
		return true, ""
	}
	if reason := r.rejectCitation(citation); reason != "" {
		p.done <- false
		return false, reason + "; escalated to the maintainer"
	}
	err := r.Deliver(id, answer, answeredByWatchdog, citation)
	p.done <- true
	if err != nil {
		return false, err.Error()
	}
	return true, ""
}

func (r *QuestionRouter) answered(id string) (Question, bool) {
	if r.Answered == nil {
		return Question{}, false
	}
	return r.Answered(id)
}

func (r *QuestionRouter) reopen(id string, p routed) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.open[id] = p
}

func (r *QuestionRouter) rejectCitation(citation string) string {
	if !citationPattern.MatchString(citation) {
		return fmt.Sprintf("citation %q is not path:line", citation)
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
