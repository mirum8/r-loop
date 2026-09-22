package core

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

var remedyClasses = []string{"deps", "ports", "containers", "locks", "restart", "retry", "provider"}

const (
	consentAllowList  = "allow-list"
	consentMaintainer = "maintainer"
	consentRefused    = "refused"

	decisionAuthorised = "authorised"
	decisionRefused    = "refused"
	decisionAsk        = "ask"

	askForConsent = "ask the maintainer in your session: what fails, the command, and why it is safe; then call again with maintainer_said set to their reply"
)

type Remedies struct {
	Allow       []string
	Store       Store
	Now         func() time.Time
	Watch       *Watch
	MaxRestarts int
	Fallbacks   map[string]Fallback
	Asks        func(provider string) bool

	mu sync.Mutex
}

func (r *Remedies) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *Remedies) Propose(class, command, why, maintainerSaid string) (string, string) {
	if !slices.Contains(remedyClasses, class) {
		return decisionRefused, fmt.Sprintf("class %q is not a remedy class", class)
	}
	step, ok := r.Watch.target()
	if !ok {
		return decisionRefused, "no step to remedy"
	}
	consent := consentAllowList
	if !slices.Contains(r.Allow, class) {
		if strings.TrimSpace(maintainerSaid) == "" {
			return decisionAsk, askForConsent
		}
		consent = consentMaintainer
	}
	rem, err := r.decide(step, class, command, why, func(Remedy) string { return consent })
	if err != nil {
		return decisionRefused, err.Error()
	}
	if rem.Consent == consentRefused {
		return decisionRefused, ""
	}
	return decisionAuthorised, ""
}

func (r *Remedies) decide(step StepKey, class, command, why string, consent func(Remedy) string) (Remedy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, err := r.Store.Load(step.Run)
	if err != nil {
		return Remedy{}, err
	}
	rem := Remedy{ID: fmt.Sprintf("remedy-%d", len(st.Remedies)+1), Step: step, Class: class, Command: command, Why: why, ProposedAt: r.now()}
	rem.Consent = consent(rem)
	rem.DecidedAt = r.now()
	if err := r.Store.Append(step.Run, Record{Kind: RecordRemedy, At: rem.DecidedAt, Step: &step, Remedy: &rem}); err != nil {
		return Remedy{}, fmt.Errorf("record: %w", err)
	}
	return rem, nil
}

func (r *Remedies) Restart(step, addendum, provider, maintainerSaid string) (bool, string) {
	phase, kind, ok := ParseStepName(step)
	if !ok {
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
	if provider != "" && !r.Asks(provider) {
		return false, "provider " + provider + " has no MCP ask channel"
	}
	fallback := provider == "" || provider == r.Fallbacks[kind].Provider
	fits := func(rem Remedy) bool {
		switch {
		case provider != "":
			return rem.Class == "provider" && (fallback || rem.Consent == consentMaintainer && slices.Contains(strings.Fields(rem.Command), provider))
		case rem.Class == "restart":
			return true
		case rem.Class == "retry":
			return addendum != ""
		}
		return false
	}
	remedy := ""
	for _, rem := range st.Remedies {
		if rem.Step == key && rem.Consent != consentRefused && !spent[rem.ID] && fits(rem) {
			remedy = rem.ID
			break
		}
	}
	if remedy == "" && fallback && slices.ContainsFunc(r.Allow, func(class string) bool { return fits(Remedy{Class: class}) }) {
		remedy = consentAllowList
	}
	if remedy == "" && !fallback {
		if strings.TrimSpace(maintainerSaid) == "" {
			return false, provider + " is not the row's fallback: " + askForConsent
		}
		rem, err := r.decide(key, "provider", fmt.Sprintf("restart %s on %s", step, provider), provider+" is not the row's fallback", func(Remedy) string { return consentMaintainer })
		if err != nil {
			return false, err.Error()
		}
		remedy = rem.ID
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
