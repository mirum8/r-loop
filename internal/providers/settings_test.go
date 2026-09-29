package providers

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestClaudeGetsTheStatusSettingsFlagAndCodexDoesNot(t *testing.T) {
	r := NewRegistry(nil, nil, t.TempDir())
	claude, _ := r.Resolve("claude")
	codex, _ := r.Resolve("codex")
	claude.Settings, codex.Settings = "/run/claude.settings.json", "/run/claude.settings.json"

	got := Args(claude, "", "", "", "", "")
	codexArgs := Args(codex, "", "", "", "", "")

	if want := []string{"--settings", "/run/claude.settings.json"}; !reflect.DeepEqual(got, want) {
		t.Errorf("claude args %q, want %q", got, want)
	}
	if slices.Contains(codexArgs, "/run/claude.settings.json") {
		t.Errorf("codex args %q carry the claude settings", codexArgs)
	}
}

func TestSettingsFlagWithoutThePlaceholderIsRejected(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pdev.yaml"), []byte("kind: pdev\ndoneSignal: sentinel\nsettingsFlag: --settings\n"), 0o644)

	_, err := NewRegistry(nil, nil, dir).Resolve("pdev")

	if err == nil || !strings.Contains(err.Error(), "settingsFlag must contain {settings}") {
		t.Fatalf("err = %v", err)
	}
}

func TestWriteStatusSettingsPointsTheStatuslineAtTheTap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude.settings.json")

	if err := WriteStatusSettings(path, "/opt/r loop/r-loop", "/run/usage/claude.json"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	want := `{"statusLine":{"command":"'/opt/r loop/r-loop' statusline-tap /run/usage/claude.json","type":"command"}}`
	if string(data) != want {
		t.Fatalf("settings = %s\nwant %s", data, want)
	}
}
