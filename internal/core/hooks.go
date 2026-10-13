package core

import (
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
}

type hookLog struct {
	mu     sync.Mutex
	agents map[string]*hookAgent
	warned bool
}

func isEditTool(tool string) bool {
	switch tool {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return true
	}
	return false
}

func (h *hookLog) apply(ev HookEvent) {
	if h.agents == nil {
		h.agents = map[string]*hookAgent{}
	}
	a := h.agents[ev.Agent]
	if a == nil {
		a = &hookAgent{edits: map[string]time.Time{}}
		h.agents[ev.Agent] = a
	}
	switch ev.Event {
	case "UserPromptSubmit":
		a.busy, a.notice = true, ""
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
	if err != nil && !h.warned {
		h.warned = true
		return err
	}
	return nil
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

func (m *SessionManager) hookTurn(agent string) (seen, busy bool) {
	seen = m.hookAgent(agent, func(a *hookAgent) { busy = a.busy })
	return seen, busy
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
