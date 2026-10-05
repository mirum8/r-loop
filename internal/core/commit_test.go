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

func TestRepairSubject(t *testing.T) {
	const run = "feat(analyze): detect modules, read findings from sarif and pmd reports, add analyze config and preflight"
	cases := []struct {
		name, raw, want string
		repaired        bool
	}{
		{"valid stays", "feat(store): append run state as JSONL", "feat(store): append run state as JSONL", false},
		{"too long is cut at a word", run, "feat(analyze): detect modules, read findings from sarif and pmd reports, add analyze config and", true},
		{"one long word keeps the type", "feat(core): " + strings.Repeat("a", 120), "feat(core): " + strings.Repeat("a", 88), true},
		{"first line only", "fix(core): close the run file\n\nbecause it leaked", "fix(core): close the run file", true},
		{"surrounding spaces", "  docs: explain the land gate  ", "docs: explain the land gate", true},
		{"wrong type keeps the words", "feature(store): append run state", "chore: feature(store): append run state", true},
		{"no type", "append run state as JSONL", "chore: append run state as JSONL", true},
		{"label uses the fallback", "feat(core): phase 3", "chore: Plan reader", true},
		{"empty uses the fallback", "", "chore: Plan reader", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, repair := RepairSubject(c.raw, "chore: Plan reader")

			if got != c.want {
				t.Fatalf("RepairSubject(%q) = %q, want %q", c.raw, got, c.want)
			}
			if (repair != "") != c.repaired {
				t.Fatalf("repair = %q, want repaired %v", repair, c.repaired)
			}
			if err := ValidSubject(got); err != nil {
				t.Fatalf("ValidSubject(%q) = %v", got, err)
			}
		})
	}
}

func TestRepairSubjectStillCommitsWhenTheFallbackIsUnusable(t *testing.T) {
	for _, fallback := range []string{"", "chore: plan", "Plan"} {
		got, repair := RepairSubject("", fallback)

		if err := ValidSubject(got); err != nil || repair == "" {
			t.Fatalf("RepairSubject(\"\", %q) = %q, %q: %v", fallback, got, repair, err)
		}
	}
}
