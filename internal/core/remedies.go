package core

import (
	"fmt"
	"slices"
	"sync"
	"time"
)

var remedyClasses = []string{"deps", "ports", "containers", "locks", "restart", "retry", "provider"}

const (
	consentAllowList  = "allow-list"
	consentMaintainer = "maintainer"
	consentRefused    = "refused"
)

type Remedies struct {
	Allow       []string
	Face        Face
	Store       Store
	Window      time.Duration
	Now         func() time.Time
	Watch       *Watch
	MaxRestarts int

	mu sync.Mutex
}

func (r *Remedies) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *Remedies) Propose(class, command, why string) string {
	if !slices.Contains(remedyClasses, class) {
		return fmt.Sprintf("refused: class %q is not a remedy class", class)
	}
	step, ok := r.Watch.target()
	if !ok {
		return "refused: no step to remedy"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, err := r.Store.Load(step.Run)
	if err != nil {
		return "refused: " + err.Error()
	}
	rem := Remedy{ID: fmt.Sprintf("remedy-%d", len(st.Remedies)+1), Step: step, Class: class, Command: command, Why: why, ProposedAt: r.now()}
	rem.Consent = r.consent(rem)
	rem.DecidedAt = r.now()
	if err := r.Store.Append(step.Run, Record{Kind: RecordRemedy, At: rem.DecidedAt, Step: &step, Remedy: &rem}); err != nil {
		return "refused: record: " + err.Error()
	}
	if rem.Consent == consentRefused {
		return "refused"
	}
	return "authorised"
}

func (r *Remedies) consent(rem Remedy) string {
	if slices.Contains(r.Allow, rem.Class) {
		return consentAllowList
	}
	if r.Window <= 0 {
		return consentRefused
	}
	q := Question{ID: rem.ID, Step: rem.Step, Text: fmt.Sprintf("watchdog proposes (%s): %s — %s", rem.Class, rem.Command, rem.Why), Options: []string{"yes", "no"}, AskedAt: rem.ProposedAt}
	answers := make(chan string, 1)
	go func() {
		a, err := r.Face.Ask(q)
		if err != nil {
			a = ""
		}
		answers <- a
	}()
	timeout := time.NewTimer(r.Window)
	defer timeout.Stop()
	select {
	case a := <-answers:
		if a == "" {
			return consentRefused
		}
		ev := Event{At: r.now(), Kind: "human", Phase: rem.Step.Phase, Step: rem.Step.Kind, Fields: map[string]string{"what": "consent"}}
		r.Store.Append(rem.Step.Run, Record{Kind: RecordEvent, At: ev.At, Event: &ev})
		if a == "yes" {
			return consentMaintainer
		}
		return consentRefused
	case <-timeout.C:
		if w, ok := r.Face.(interface{ Withdraw(id string) }); ok {
			w.Withdraw(q.ID)
		}
		return consentRefused
	}
}

func (r *Remedies) Restart(step, addendum, provider string) (bool, string) {
	var phase int
	var kind string
	if _, err := fmt.Sscanf(step, "phase-%d/%s", &phase, &kind); err != nil || fmt.Sprintf("phase-%d/%s", phase, kind) != step {
		return false, fmt.Sprintf("step %q is not phase-<N>/<kind>", step)
	}
	if state, ok := r.Watch.latest(phase, kind); ok && state != StepFailed && state != StepStalled {
		return false, "step is " + string(state)
	}
	key, closed, ok := r.Watch.hold()
	if !ok || key.Phase != phase || key.Kind != kind {
		return false, "run halted"
	}
	st, err := r.Store.Load(key.Run)
	if err != nil {
		return false, err.Error()
	}
	restarts, spent := 0, map[string]bool{}
	for _, ev := range st.Events {
		if ev.Kind != "restart" {
			continue
		}
		if ev.Fields["step"] == step {
			restarts++
		}
		spent[ev.Fields["remedy"]] = true
	}
	if restarts >= r.MaxRestarts {
		return false, fmt.Sprintf("restart limit %d reached", r.MaxRestarts)
	}
	fits := func(class string) bool {
		switch class {
		case "restart":
			return true
		case "retry":
			return addendum != ""
		case "provider":
			return provider != ""
		}
		return false
	}
	remedy := ""
	for _, rem := range st.Remedies {
		if rem.Step == key && rem.Consent != consentRefused && !spent[rem.ID] && fits(rem.Class) {
			remedy = rem.ID
			break
		}
	}
	if remedy == "" && slices.ContainsFunc(r.Allow, fits) {
		remedy = consentAllowList
	}
	if remedy == "" {
		return false, "no authorised remedy"
	}
	r.Watch.init()
	select {
	case r.Watch.restarts <- Restart{Step: key, Addendum: addendum, Provider: provider, Remedy: remedy}:
		return true, ""
	case <-closed:
		return false, "run halted"
	}
}
