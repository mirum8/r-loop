package quota

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var now = time.Unix(1_790_000_000, 0)

func rollout(t *testing.T, root, name string, lines ...string) {
	t.Helper()
	dir := filepath.Join(root, "sessions", "2026", "09", "25")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCodexTakesTheLastRateLimitsOfTheNewestRollout(t *testing.T) {
	root := t.TempDir()
	rollout(t, root, "rollout-2026-09-25T10-00-00-a.jsonl",
		`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":90,"window_minutes":300,"resets_at":1790009000}}}}`)
	rollout(t, root, "rollout-2026-09-25T11-00-00-b.jsonl",
		`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":1,"window_minutes":300,"resets_at":1790009000}}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":42,"window_minutes":10080,"resets_at":1790500000},"secondary":{"used_percent":7,"window_minutes":300,"resets_at":1790009000}}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","rate_limits":null}}`,
		`{"type":"response_item","payload":{"type":"message"}}`)
	old := now.Add(-time.Hour)
	os.Chtimes(filepath.Join(root, "sessions", "2026", "09", "25", "rollout-2026-09-25T10-00-00-a.jsonl"), old, old)

	got, ok := Codex(root, now)

	want := Limits{Provider: "codex", Windows: []Window{
		{Label: "5h", Percent: 7, Resets: time.Unix(1790009000, 0)},
		{Label: "7d", Percent: 42, Resets: time.Unix(1790500000, 0)},
	}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("Codex = %+v, %v; want %+v", got, ok, want)
	}
}

func TestCodexSkipsARolloutWithoutRateLimits(t *testing.T) {
	root := t.TempDir()
	rollout(t, root, "rollout-2026-09-25T10-00-00-a.jsonl",
		`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":3,"window_minutes":10080,"resets_at":1790500000},"secondary":null}}}`)
	rollout(t, root, "rollout-2026-09-25T11-00-00-b.jsonl", `{"type":"session_meta","payload":{}}`)

	got, ok := Codex(root, now)

	want := Limits{Provider: "codex", Windows: []Window{{Label: "7d", Percent: 3, Resets: time.Unix(1790500000, 0)}}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("Codex = %+v, %v; want %+v", got, ok, want)
	}
}

func TestCodexWithoutSessionsHasNoLimits(t *testing.T) {
	if _, ok := Codex(filepath.Join(t.TempDir(), "missing"), now); ok {
		t.Fatal("Codex found limits in a missing directory")
	}
}

func TestAWindowWhoseResetHasPassedIsEmpty(t *testing.T) {
	root := t.TempDir()
	rollout(t, root, "rollout-2026-09-25T10-00-00-a.jsonl",
		`{"type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":80,"window_minutes":300,"resets_at":1789999999}}}}`)

	got, _ := Codex(root, now)

	if got.Windows[0].Percent != 0 {
		t.Fatalf("percent after reset = %v, want 0", got.Windows[0].Percent)
	}
}

func TestClaudeReadsTheTappedRateLimits(t *testing.T) {
	file := filepath.Join(t.TempDir(), "claude.json")
	os.WriteFile(file, []byte(`{"five_hour":{"used_percentage":42.5,"resets_at":1790009000},"seven_day":{"used_percentage":49,"resets_at":1789999999}}`), 0o600)

	got, ok := Claude(file, now)

	want := Limits{Provider: "claude", Windows: []Window{
		{Label: "5h", Percent: 42.5, Resets: time.Unix(1790009000, 0)},
		{Label: "7d", Percent: 0, Resets: time.Unix(1789999999, 0)},
	}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("Claude = %+v, %v; want %+v", got, ok, want)
	}
}

func TestClaudeWithoutATapFileHasNoLimits(t *testing.T) {
	if _, ok := Claude(filepath.Join(t.TempDir(), "claude.json"), now); ok {
		t.Fatal("Claude found limits without a tap file")
	}
}

func TestTapSavesRateLimitsAndRunsTheUserStatusline(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "settings.json")
	os.WriteFile(settings, []byte(`{"statusLine":{"type":"command","command":"cat | wc -c | tr -d ' '; printf hi"}}`), 0o600)
	out := filepath.Join(dir, "usage", "claude.json")
	input := `{"model":{"id":"x"},"rate_limits":{"five_hour":{"used_percentage":12,"resets_at":1790009000}}}`
	var stdout bytes.Buffer

	Tap(strings.NewReader(input), out, settings, &stdout)

	saved, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != `{"five_hour":{"used_percentage":12,"resets_at":1790009000}}` {
		t.Fatalf("saved = %s", saved)
	}
	if want := "94\nhi"; stdout.String() != want {
		t.Fatalf("statusline output = %q, want %q", stdout.String(), want)
	}
}

func TestTapWithoutRateLimitsOrUserStatuslineDoesNothing(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "claude.json")
	var stdout bytes.Buffer

	Tap(strings.NewReader(`{"model":{"id":"x"}}`), out, filepath.Join(dir, "missing.json"), &stdout)

	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("tap file written without rate_limits: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("statusline output = %q, want none", stdout.String())
	}
}
