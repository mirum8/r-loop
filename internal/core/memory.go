package core

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	EventMemoryRemember  = "memory-remember"
	EventMemoryForget    = "memory-forget"
	EventMemoryCompacted = "memory-compacted"

	MaxMemoryDescription = 150
	MaxMemoryBody        = 2048
	MaxMemoryFacts       = 60
)

var memoryScopes = []string{"all", "step", "review", "watchdog"}

type MemoryKeeper struct {
	Memory Memory
	Store  Store
	Face   Face
	RunID  string
	Now    func() time.Time

	mu sync.Mutex
}

func (k *MemoryKeeper) Remember(scope, description, body, why string) (bool, string) {
	if k == nil || k.Memory == nil {
		return false, "not available"
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	description = strings.TrimSpace(description)
	switch {
	case !validScope(scope):
		return false, fmt.Sprintf("scope %q: want one of %s", scope, strings.Join(memoryScopes, ", "))
	case description == "":
		return false, "description is empty"
	case strings.ContainsAny(description, "\r\n"):
		return false, "description must be one line"
	case strings.ContainsFunc(description, unicode.IsControl):
		return false, "description has a control character"
	case utf8.RuneCountInString(description) > MaxMemoryDescription:
		return false, fmt.Sprintf("description is over %d characters", MaxMemoryDescription)
	case len(body) > MaxMemoryBody:
		return false, fmt.Sprintf("body is over %d bytes", MaxMemoryBody)
	}
	facts, err := k.Memory.List()
	if err != nil {
		return false, "memory: " + err.Error()
	}
	if len(facts) >= MaxMemoryFacts {
		return false, fmt.Sprintf("memory holds %d facts, the most it keeps: forget or merge some first", len(facts))
	}
	now := k.now()
	if err := k.Store.Append(k.RunID, k.event(now, EventMemoryRemember, map[string]string{"scope": scope, "description": description, "why": why})); err != nil {
		return false, "store: " + err.Error()
	}
	id, err := k.Memory.Write(MemoryFact{Scope: scope, Description: description, Body: body, Run: k.RunID, At: now})
	if err != nil {
		return false, "memory: " + err.Error()
	}
	k.show(now, EventMemoryRemember, map[string]string{"id": id, "scope": scope, "description": description})
	return true, "remembered as " + id
}

func (k *MemoryKeeper) Forget(id, why string) (bool, string) {
	if k == nil || k.Memory == nil {
		return false, "not available"
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	facts, err := k.Memory.List()
	if err != nil {
		return false, "memory: " + err.Error()
	}
	found := false
	for _, f := range facts {
		found = found || f.ID == id
	}
	if !found {
		return false, fmt.Sprintf("fact %s not found", id)
	}
	now := k.now()
	if err := k.Store.Append(k.RunID, k.event(now, EventMemoryForget, map[string]string{"id": id, "why": why})); err != nil {
		return false, "store: " + err.Error()
	}
	if err := k.Memory.Delete(id); err != nil {
		return false, "memory: " + err.Error()
	}
	k.show(now, EventMemoryForget, map[string]string{"id": id})
	return true, ""
}

func (k *MemoryKeeper) Facts() int {
	if k == nil || k.Memory == nil {
		return 0
	}
	facts, err := k.Memory.List()
	if err != nil {
		return 0
	}
	return len(facts)
}

func (k *MemoryKeeper) Index(scopes ...string) string {
	if k == nil || k.Memory == nil {
		return ""
	}
	facts, err := k.Memory.List()
	if err != nil {
		k.show(k.now(), "warning", map[string]string{"reason": "memory: " + err.Error()})
		return ""
	}
	var b strings.Builder
	for _, f := range facts {
		switch {
		case len(scopes) == 0:
			fmt.Fprintf(&b, "- [%s] %s — %s\n", f.Scope, f.Path, f.Description)
		case slices.Contains(scopes, f.Scope):
			fmt.Fprintf(&b, "- %s — %s\n", f.Path, f.Description)
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (k *MemoryKeeper) event(at time.Time, kind string, fields map[string]string) Record {
	return Record{Kind: RecordEvent, At: at, Event: &Event{At: at, Kind: kind, Fields: fields}}
}

func (k *MemoryKeeper) show(at time.Time, kind string, fields map[string]string) {
	if k.Face != nil {
		k.Face.Emit(Event{At: at, Kind: kind, Fields: fields})
	}
}

func (k *MemoryKeeper) now() time.Time {
	if k.Now != nil {
		return k.Now()
	}
	return time.Now()
}

func validScope(scope string) bool {
	return slices.Contains(memoryScopes, scope)
}
