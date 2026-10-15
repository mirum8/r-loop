package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSetAddsANestedKeyInBlockStyleAndKeepsTheRest(t *testing.T) {
	in := "# project config\nwatchdog:\n  model: opus\nsteps:\n  plan:\n    model: sonnet\n"

	out, err := Set([]byte(in), "steps.implement.model", "gpt-5.5")

	if err != nil {
		t.Fatal(err)
	}
	want := "# project config\nwatchdog:\n  model: opus\nsteps:\n  plan:\n    model: sonnet\n  implement:\n    model: gpt-5.5\n"
	if string(out) != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}

func TestSetReplacesAnExistingScalarAndKeepsItsComment(t *testing.T) {
	in := "land:\n  gateTimeout: 10m # slow suite\n"

	out, err := Set([]byte(in), "land.gateTimeout", "30m")

	if err != nil {
		t.Fatal(err)
	}
	if want := "land:\n  gateTimeout: 30m # slow suite\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestSetOnAMissingFileCreatesTheDocument(t *testing.T) {
	out, err := Set(nil, "steps.implement.provider", "claude")

	if err != nil {
		t.Fatal(err)
	}
	if want := "steps:\n  implement:\n    provider: claude\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestSetFillsANullSection(t *testing.T) {
	out, err := Set([]byte("land:\n"), "land.gateTimeout", "30m")

	if err != nil {
		t.Fatal(err)
	}
	if want := "land:\n  gateTimeout: 30m\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestSetReachesIntoAListByIndex(t *testing.T) {
	in := "steps:\n  implement:\n    reviewers:\n      - name: codex\n        model: gpt-5\n"

	out, err := Set([]byte(in), "steps.implement.reviewers.0.model", "gpt-5.5")

	if err != nil {
		t.Fatal(err)
	}
	if want := "steps:\n  implement:\n    reviewers:\n      - name: codex\n        model: gpt-5.5\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestSetQuotesAValueThatIsNotAPlainScalar(t *testing.T) {
	out, err := Set(nil, "label", "a: b")

	if err != nil {
		t.Fatal(err)
	}
	if want := "label: 'a: b'\n"; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func TestSetRefusesAPathItCannotFollow(t *testing.T) {
	for name, tc := range map[string]struct{ in, key, err string }{
		"empty key":             {"", "", "empty"},
		"empty segment":         {"", "steps..model", "empty"},
		"through a scalar":      {"label: x\n", "label.name", "label is not a section"},
		"index out of range":    {"steps:\n  implement:\n    reviewers:\n      - name: a\n", "steps.implement.reviewers.3.model", "no item 3"},
		"replace a section":     {"land:\n  gateTimeout: 10m\n", "land", "land is not a scalar"},
		"document is not a map": {"- a\n", "label", "not a mapping"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Set([]byte(tc.in), tc.key, "v")

			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("err %v, want %q", err, tc.err)
			}
		})
	}
}

func TestSetCopiesAnInheritedListIntoTheProjectBeforeEditingIt(t *testing.T) {
	home := "steps:\n  implement:\n    reviewers:\n      - name: a\n        provider: codex\n        model: sol\n        effort: high\n      - name: b\n        provider: claude\n        model: opus\n        effort: high\n"

	out, err := Set([]byte("label: x\n"), "steps.implement.reviewers.1.model", "sonnet", []byte(home))

	if err != nil {
		t.Fatal(err)
	}
	want := "label: x\nsteps:\n  implement:\n    reviewers:\n      - name: a\n        provider: codex\n        model: sol\n        effort: high\n      - name: b\n        provider: claude\n        model: sonnet\n        effort: high\n"
	if string(out) != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}

func TestSetCopiesTheDefaultListWhenNoLayerSetsIt(t *testing.T) {
	out, err := Set(nil, "steps.implement.reviewers.2.model", "opus", []byte("watchdog:\n  model: opus\n"))

	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Steps map[string]struct {
			Reviewers []map[string]string `yaml:"reviewers"`
		} `yaml:"steps"`
	}
	if err := yaml.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	got := doc.Steps["implement"].Reviewers
	if len(got) != 3 || got[0]["provider"] != "codex" || got[1]["name"] != "ui" || got[2]["name"] != "security" || got[2]["model"] != "opus" || got[2]["prompt"] != "review-security" {
		t.Errorf("reviewers %+v", got)
	}
}
