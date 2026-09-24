package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMigrateTurnsBareNamesIntoBlocksWithTheDefaultsModelAndEffort(t *testing.T) {
	d := newDirs(t)
	old := "# my config\nsteps:\n  plan:\n    fallback: codex\n    reviewers:\n      - codex\n  implement:\n    fallback: claude # keep me\n    reviewers:\n      - claude\n      - name: ui\n        provider: claude\n        model: opus\n        effort: high\nnotify:\n  onHalt: \"\"\n"
	d.writeProject(t, old)
	path := filepath.Join(d.project, ".r-loop", "config.yaml")

	m, err := Migrate(path)

	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"steps.plan.fallback: codex → codex gpt-5.6-sol medium",
		"steps.plan.reviewers.0: codex → codex gpt-5.6-sol medium",
		"steps.implement.fallback: claude → claude opus medium",
		"steps.implement.reviewers.0: claude → claude opus medium",
	}
	if !reflect.DeepEqual(m.Changes, want) || len(m.Unresolved) != 0 {
		t.Errorf("migration = %+v", m)
	}
	cfg := d.load(t)
	if cfg.Steps["plan"].Fallback != (Fallback{"codex", "gpt-5.6-sol", "medium"}) {
		t.Errorf("plan fallback = %+v", cfg.Steps["plan"].Fallback)
	}
	if got := cfg.Steps["implement"].Reviewers; len(got) != 2 || got[0] != (Reviewer{Provider: "claude", Model: "opus", Effort: "medium"}) || got[1].Name != "ui" {
		t.Errorf("implement reviewers = %+v", got)
	}
	data, _ := os.ReadFile(path)
	for _, keep := range []string{"# my config", "# keep me", `onHalt: ""`} {
		if !strings.Contains(string(data), keep) {
			t.Errorf("%q lost from:\n%s", keep, data)
		}
	}
	if bak, _ := os.ReadFile(path + ".bak"); string(bak) != old {
		t.Errorf("backup = %q", bak)
	}
}

func TestMigrateLeavesAnUpToDateFileAlone(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  plan:\n    fallback:\n      provider: codex\n      model: gpt-6-sol\n      effort: high\n")
	path := filepath.Join(d.project, ".r-loop", "config.yaml")

	m, err := Migrate(path)

	if err != nil || len(m.Changes) != 0 || len(m.Unresolved) != 0 {
		t.Fatalf("migration = %+v, %v", m, err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Errorf("backup written for an unchanged file: %v", err)
	}
}

func TestMigrateReportsAProviderWithNoBuiltInDefault(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  plan:\n    fallback: gemini\n")
	path := filepath.Join(d.project, ".r-loop", "config.yaml")

	m, err := Migrate(path)

	if err != nil || len(m.Changes) != 0 {
		t.Fatalf("migration = %+v, %v", m, err)
	}
	if !reflect.DeepEqual(m.Unresolved, []string{"steps.plan.fallback: gemini has no built-in model and effort, set them by hand"}) {
		t.Errorf("unresolved = %v", m.Unresolved)
	}
	if data, _ := os.ReadFile(path); string(data) != "steps:\n  plan:\n    fallback: gemini\n" {
		t.Errorf("file changed: %q", data)
	}
}

func TestMigrateFillsAMissingModelOrEffortInABlock(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  plan:\n    fallback:\n      provider: codex\n      effort: high\n    reviewers:\n      - name: deep\n        provider: claude\n        model: \"\"\n")
	path := filepath.Join(d.project, ".r-loop", "config.yaml")

	m, err := Migrate(path)

	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"steps.plan.fallback.model: unset → gpt-5.6-sol",
		"steps.plan.reviewers.0.model: unset → opus",
		"steps.plan.reviewers.0.effort: unset → medium",
	}
	if !reflect.DeepEqual(m.Changes, want) {
		t.Errorf("changes = %q", m.Changes)
	}
	cfg := d.load(t)
	if cfg.Steps["plan"].Fallback != (Fallback{"codex", "gpt-5.6-sol", "high"}) {
		t.Errorf("fallback = %+v", cfg.Steps["plan"].Fallback)
	}
	if got := cfg.Steps["plan"].Reviewers; len(got) != 1 || got[0] != (Reviewer{Provider: "claude", Model: "opus", Effort: "medium", Name: "deep"}) {
		t.Errorf("reviewers = %+v", got)
	}
}

func TestMigrateKeepsTheFirstBackup(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  plan:\n    fallback: codex\n")
	path := filepath.Join(d.project, ".r-loop", "config.yaml")
	writeFile(t, path+".bak", "the original\n")

	if _, err := Migrate(path); err != nil {
		t.Fatal(err)
	}

	if bak, _ := os.ReadFile(path + ".bak"); string(bak) != "the original\n" {
		t.Errorf("backup = %q", bak)
	}
}
