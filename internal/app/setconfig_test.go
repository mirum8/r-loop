package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"r-loop/internal/core"
)

func (f *fixture) setConfigRun() *Wiring {
	f.t.Helper()
	w, err := f.preflight(f.todo, "--plain")
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(w.release)
	return w
}

func (f *fixture) read(rel string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(f.root, rel))
	return string(data), err == nil
}

func configEvents(t *testing.T, w *Wiring, kind string) []core.Event {
	t.Helper()
	st, err := w.Store.Load(w.Loop.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var out []core.Event
	for _, ev := range st.Events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

func TestSetConfigWritesLoadsAndCommitsTheProjectConfigAlone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "# mine\nlabel: x\n")
	f.commit()
	w := f.setConfigRun()
	f.write("notes.txt", "maintainer's\n")

	decision, reason := w.ConfigSet.Set("steps.implement.model", "opus", "the row named a retired model")

	if decision != "authorised" || reason != "" {
		t.Fatalf("decision %q reason %q", decision, reason)
	}
	if got, _ := f.read(".r-loop/config.yaml"); got != "# mine\nlabel: x\nsteps:\n  implement:\n    model: opus\n" {
		t.Errorf("config %q", got)
	}
	if m := w.Config.Steps["implement"].Model; m != "opus" {
		t.Errorf("loaded model %q", m)
	}
	for _, k := range w.Loop.Kinds {
		if k.Name == "implement" && k.Row.Model != "opus" {
			t.Errorf("implement row %+v", k.Row)
		}
	}
	if s := git(t, f.root, "log", "-1", "--format=%s"); s != "chore(r-loop): set steps.implement.model" {
		t.Errorf("subject %q", s)
	}
	if files := git(t, f.root, "show", "--name-only", "--format=", "HEAD"); files != ".r-loop/config.yaml" {
		t.Errorf("committed %q", files)
	}
	if st := git(t, f.root, "status", "--porcelain"); st != "?? notes.txt" {
		t.Errorf("status %q", st)
	}
	if evs := configEvents(t, w, core.EventConfigSet); len(evs) != 1 || evs[0].Fields["value"] != "opus" {
		t.Errorf("events %+v", evs)
	}
}

func TestSetConfigCreatesAMissingProjectConfig(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.commit()
	w := f.setConfigRun()

	decision, reason := w.ConfigSet.Set("land.gateTimeout", "45m", "the suite takes 40 minutes")

	if decision != "authorised" {
		t.Fatalf("decision %q reason %q", decision, reason)
	}
	if got, _ := f.read(".r-loop/config.yaml"); got != "land:\n  gateTimeout: 45m\n" {
		t.Errorf("config %q", got)
	}
	if files := git(t, f.root, "show", "--name-only", "--format=", "HEAD"); files != ".r-loop/config.yaml" {
		t.Errorf("committed %q", files)
	}
}

func TestSetConfigRefusesAValueOrKeyTheConfigRejectsAndLeavesTheFile(t *testing.T) {
	t.Parallel()
	for name, kv := range map[string][2]string{
		"bad duration": {"land.gateTimeout", "soon"},
		"unknown key":  {"steps.implement.modle", "opus"},
		"own powers":   {"watchdog.maxRestarts", "99"},
		"a section":    {"steps", "x"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.write(".r-loop/config.yaml", "label: x\n")
			f.commit()
			w := f.setConfigRun()
			head := git(t, f.root, "rev-parse", "HEAD")

			decision, reason := w.ConfigSet.Set(kv[0], kv[1], "why")

			if decision != "refused" || reason == "" {
				t.Fatalf("decision %q reason %q", decision, reason)
			}
			if got, _ := f.read(".r-loop/config.yaml"); got != "label: x\n" {
				t.Errorf("config %q", got)
			}
			if h := git(t, f.root, "rev-parse", "HEAD"); h != head {
				t.Errorf("HEAD moved to %s", h)
			}
		})
	}
}

func TestSetConfigRestoresTheFileWhenTheRunCannotLoadIt(t *testing.T) {
	t.Parallel()
	for name, existing := range map[string]string{"existing file": "label: x\n", "no file": ""} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if existing != "" {
				f.write(".r-loop/config.yaml", existing)
			}
			f.commit()
			w := f.setConfigRun()
			head := git(t, f.root, "rev-parse", "HEAD")

			decision, reason := w.ConfigSet.Set("steps.implement.provider", "ghost", "try ghost")

			if decision != "refused" || !strings.Contains(reason, "ghost") || !strings.Contains(reason, "restored") {
				t.Fatalf("decision %q reason %q", decision, reason)
			}
			got, ok := f.read(".r-loop/config.yaml")
			if existing == "" && ok || existing != "" && got != existing {
				t.Errorf("config %q exists %v", got, ok)
			}
			if p := w.Config.Steps["implement"].Provider; p == "ghost" {
				t.Errorf("loaded provider %q", p)
			}
			if h := git(t, f.root, "rev-parse", "HEAD"); h != head {
				t.Errorf("HEAD moved to %s", h)
			}
		})
	}
}

func TestSetConfigEditsAReviewerTheProjectInheritsFromHome(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	home := filepath.Join(f.env.Home, ".config", "r-loop", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home, []byte("steps:\n  implement:\n    reviewers:\n      - name: only\n        provider: codex\n        model: sol\n        effort: high\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.write(".r-loop/config.yaml", "label: x\n")
	f.commit()
	w := f.setConfigRun()

	decision, reason := w.ConfigSet.Set("steps.implement.reviewers.0.effort", "medium", "high effort times out")

	if decision != "authorised" || reason != "" {
		t.Fatalf("decision %q reason %q", decision, reason)
	}
	got := w.Config.Steps["implement"].Reviewers
	if len(got) != 1 || got[0].Name != "only" || got[0].Provider != "codex" || got[0].Model != "sol" || got[0].Effort != "medium" {
		t.Errorf("reviewers %+v", got)
	}
}

func TestSetConfigRefusesAProviderChangeAConfiguredReviewerCannotRunOn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	cfg := "providers:\n  codex:\n    kind: codex\n    ask: mcp\n    doneSignal: sentinel\n    reviewExec: codex review --uncommitted\n"
	f.write(".r-loop/config.yaml", cfg)
	f.commit()
	w := f.setConfigRun()
	head := git(t, f.root, "rev-parse", "HEAD")

	decision, reason := w.ConfigSet.Set("providers.codex.reviewExec", "", "the review command hangs")

	if decision != "refused" || !strings.Contains(reason, "steps.implement.reviewers: provider codex has no review command") {
		t.Fatalf("decision %q reason %q", decision, reason)
	}
	if got, _ := f.read(".r-loop/config.yaml"); got != cfg {
		t.Errorf("config %q", got)
	}
	if h := git(t, f.root, "rev-parse", "HEAD"); h != head {
		t.Errorf("HEAD moved to %s", h)
	}
}

func TestSetConfigReachesTheReviewersAReopenReadsFor(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "label: x\n")
	f.commit()
	w := f.setConfigRun()

	decision, reason := w.ConfigSet.Set("steps.implement.reviewers.0.effort", "low", "high effort times out")

	if decision != "authorised" {
		t.Fatalf("decision %q reason %q", decision, reason)
	}
	for _, kind := range []string{"implement", "gatefix"} {
		got := w.Loop.Sessions.Reviewers(kind)
		if len(got) == 0 || got[0].Provider != "codex" || got[0].Effort != "low" {
			t.Errorf("%s reviewers %+v", kind, got)
		}
	}
	if got := w.Loop.Sessions.Reviewers("plan"); len(got) != 1 || got[0].Effort != "high" {
		t.Errorf("plan reviewers %+v", got)
	}
}
