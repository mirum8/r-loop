package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const (
	answeredByWatchdog = "watchdog"
	maintainerCitation = "maintainer"
	watchdogGone       = "the watchdog is gone"
)

var citationPattern = regexp.MustCompile(`^[^\s:]+:\d+$`)

type QuestionRouter struct {
	Dog     *Watchdog
	Deliver func(id, answer, by, citation string) error
	Repo    Repo

	mu   sync.Mutex
	open map[string]bool
}

func (r *QuestionRouter) Route(ctx context.Context, q Question) bool {
	if r.Dog == nil || !r.Dog.live() {
		return false
	}
	r.mu.Lock()
	if r.open == nil {
		r.open = map[string]bool{}
	}
	r.open[q.ID] = true
	r.mu.Unlock()
	text := fmt.Sprintf("question %s from phase-%s/%s: %s options: %s recommended: %s", q.ID, q.Step.Phase, q.Step.Kind, q.Text, strings.Join(q.Options, ", "), q.Recommended)
	if err := r.Dog.Notify(text, false, 0); err != nil || !r.Dog.live() {
		return !r.close(q.ID)
	}
	return true
}

func (r *QuestionRouter) close(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ok := r.open[id]
	delete(r.open, id)
	return ok
}

func (r *QuestionRouter) Answer(id, answer, citation string) (bool, string) {
	citation = strings.TrimSpace(citation)
	if reason := r.rejectCitation(citation); reason != "" {
		return false, reason + "; the question stays open"
	}
	if !r.close(id) {
		return false, fmt.Sprintf("question %s is not open", id)
	}
	by := answeredByWatchdog
	if citation == maintainerCitation {
		by, citation = maintainerCitation, ""
	}
	if err := r.Deliver(id, answer, by, citation); err != nil {
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
