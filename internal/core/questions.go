package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

const (
	answeredByWatchdog = "watchdog"
	maintainerCitation = "maintainer"
	watchdogGone       = "the watchdog is gone"
	declineRule        = "decline"
	askForDialog       = "ask the maintainer in your session: show the dialog and the keys you would press; then call again with maintainer_said set to their reply"
)

var citationPattern = regexp.MustCompile(`^[^\s:]+:\d+$`)

type QuestionRouter struct {
	Dog     *Watchdog
	Deliver func(id, answer, by, citation string) error
	Keys    func(id string, keys []string, by, rule string) error
	Rules   []string
	Repo    Repo

	Remedies *Remedies
	Blocker  func(id string) (Blocker, bool)
	Settle   func(Resolution) error

	mu        sync.Mutex
	open      map[string]string
	withdrawn map[string]bool
}

func (r *QuestionRouter) Route(ctx context.Context, q Question) bool {
	r.mu.Lock()
	if r.withdrawn[q.ID] {
		r.mu.Unlock()
		return true
	}
	if r.Dog == nil || !r.Dog.live() {
		r.mu.Unlock()
		return false
	}
	if r.open == nil {
		r.open = map[string]string{}
	}
	r.open[q.ID] = q.Kind
	r.mu.Unlock()
	text := fmt.Sprintf("question %s from phase-%s/%s: %s options: %s recommended: %s", q.ID, q.Step.Phase, q.Step.Kind, q.Text, strings.Join(q.Options, ", "), q.Recommended)
	switch q.Kind {
	case QuestionDialog:
		text = fmt.Sprintf("dialog %s from phase-%s/%s: answer with answer_dialog\n\n%s", q.ID, q.Step.Phase, q.Step.Kind, q.Text)
	case QuestionBlocker:
		text = q.Text
	}
	if err := r.Dog.Notify(text, false, 0); err != nil || !r.Dog.live() {
		return !r.close(q.ID)
	}
	return true
}

func (r *QuestionRouter) close(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.open[id]
	delete(r.open, id)
	return ok
}

func (r *QuestionRouter) kind(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kind, ok := r.open[id]
	return kind, ok
}

func (r *QuestionRouter) Withdraw(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.withdrawn == nil {
		r.withdrawn = map[string]bool{}
	}
	delete(r.open, id)
	r.withdrawn[id] = true
}

func (r *QuestionRouter) Answer(id, answer, citation string) (bool, string) {
	switch kind, _ := r.kind(id); kind {
	case QuestionDialog:
		return false, id + " is a dialog: answer it with answer_dialog"
	case QuestionBlocker:
		return false, id + " is a blocker: resolve it with resolve_blocker"
	}
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

func (r *QuestionRouter) AnswerDialog(id string, keys []string, rule, maintainerSaid string) (string, string) {
	if len(keys) == 0 || slices.ContainsFunc(keys, func(k string) bool { return strings.TrimSpace(k) == "" }) {
		return decisionRefused, "keys are empty: name the keys to press, such as enter, esc, down or a digit"
	}
	if kind, ok := r.kind(id); !ok || kind != QuestionDialog {
		return decisionRefused, fmt.Sprintf("dialog %s is not open", id)
	}
	rule = strings.TrimSpace(rule)
	by := answeredByWatchdog
	switch {
	case rule == declineRule && !slices.Equal(keys, []string{"esc"}):
		return decisionRefused, fmt.Sprintf("decline presses esc only, not %s; the dialog stays open", strings.Join(keys, " "))
	case rule == declineRule:
	case rule != "" && slices.ContainsFunc(r.Rules, func(s string) bool { return strings.TrimSpace(s) == rule }):
	case rule != "":
		return decisionRefused, fmt.Sprintf("rule %q is not in watchdog.dialogs; the dialog stays open", rule)
	case r.Dog != nil && r.Dog.Unattended:
		return decisionRefused, "unattended: decline, or answer under a rule"
	case strings.TrimSpace(maintainerSaid) != "":
		by = maintainerCitation
	default:
		if r.Dog != nil {
			if err := r.Dog.AskMaintainer(fmt.Sprintf("press %s in dialog %s?", strings.Join(keys, " "), id), nil, ""); err != nil {
				return decisionRefused, err.Error()
			}
		}
		return decisionAsk, askForDialog
	}
	if !r.close(id) {
		return decisionRefused, fmt.Sprintf("dialog %s is not open", id)
	}
	if err := r.Keys(id, keys, by, rule); err != nil {
		return decisionRefused, err.Error()
	}
	return decisionAuthorised, ""
}

func (r *QuestionRouter) rejectCitation(citation string) string {
	if citation == maintainerCitation {
		return ""
	}
	return CheckCitation(r.Repo.Root(), citation)
}

func CheckCitation(root, citation string) string {
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
	info, err := os.Stat(filepath.Join(root, path))
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Sprintf("citation %s is not a file in the primary tree", path)
	}
	return ""
}
