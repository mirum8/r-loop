package core

import (
	"strings"
	"testing"
)

func TestValidSubject(t *testing.T) {
	cases := []struct {
		name, subject, err string
	}{
		{"scoped", "feat(store): append run state as JSONL", ""},
		{"unscoped", "fix: close the run file before rename", ""},
		{"breaking", "refactor(core)!: split the land gate from the loop", ""},
		{"nested scope", "test(face/tui): cover the quit hint at 80 columns", ""},
		{"empty", "", "no commit subject"},
		{"unknown type", "feature(store): append run state", "is not <type>(<scope>): <description>"},
		{"no colon space", "feat(store):append run state", "is not <type>(<scope>): <description>"},
		{"upper type", "Feat(store): append run state", "is not <type>(<scope>): <description>"},
		{"empty scope", "feat(): append run state", "is not <type>(<scope>): <description>"},
		{"empty description", "feat(store): ", "surrounding spaces"},
		{"newline", "feat(store): append run state\n\nbody", "one line"},
		{"too long", "feat(store): " + strings.Repeat("a", 90), "longer than 100"},
		{"phase label", "feat(core): phase 3", "describe the change"},
		{"phase and title", "feat: Phase 3 implement", "describe the change"},
		{"step label", "chore(r-loop): implement", "describe the change"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidSubject(c.subject)
			if c.err == "" {
				if err != nil {
					t.Fatalf("ValidSubject(%q) = %v, want nil", c.subject, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("ValidSubject(%q) = %v, want an error containing %q", c.subject, err, c.err)
			}
		})
	}
}

func TestSubjectCutsAtAWordBoundary(t *testing.T) {
	got := Subject("docs(plan): settle ", strings.Repeat("word ", 30))
	if len(got) > 100 || strings.HasSuffix(got, " ") || !strings.HasSuffix(got, "word") {
		t.Fatalf("Subject = %q", got)
	}
	if err := ValidSubject(got); err != nil {
		t.Fatalf("ValidSubject(%q) = %v", got, err)
	}
	if got := Subject("docs(report): ", "core loop"); got != "docs(report): core loop" {
		t.Fatalf("Subject = %q", got)
	}
}
