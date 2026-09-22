package core

import (
	"fmt"
	"time"
)

type StepKind struct {
	Name, Prompt, Check string
	Row                 StepRow
}

type StepRow struct {
	Provider, Model, Effort string
	Fallback                Fallback
	Timeout                 time.Duration
	Reviewers               []Reviewer
	Rounds                  int
	ReviewTimeout           time.Duration
}

type Reviewer struct {
	Provider, Model, Effort string
	Name, Prompt, Requires  string
}

func (r Reviewer) ID() string {
	if r.Name != "" {
		return r.Name
	}
	return r.Provider
}

func (r Reviewer) Template() string {
	if r.Prompt != "" {
		return r.Prompt
	}
	return "review"
}

type Fallback struct {
	Provider, Model, Effort string
}

type GateFix struct {
	Provider, Model, Effort string
}

var reviewHalfChecks = map[string]bool{"findings": true, "verdict": true}

func Pipeline(entries []string, rows map[string]StepRow, prompts, checks map[string]string) ([]StepKind, error) {
	kinds := make([]StepKind, 0, len(entries))
	for _, name := range entries {
		row, ok := rows[name]
		if !ok {
			return nil, fmt.Errorf("pipeline entry %q has no step row", name)
		}
		check := checks[name]
		if reviewHalfChecks[check] {
			return nil, fmt.Errorf("pipeline entry %q: check %q belongs to the review half", name, check)
		}
		if _, ok := LookupCheck(check); !ok {
			return nil, fmt.Errorf("pipeline entry %q: check %q is not registered", name, check)
		}
		kinds = append(kinds, StepKind{Name: name, Prompt: prompts[name], Check: check, Row: row})
	}
	return kinds, nil
}
