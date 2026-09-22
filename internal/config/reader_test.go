package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type dirs struct{ project, home string }

func newDirs(t *testing.T) dirs {
	t.Helper()
	return dirs{project: t.TempDir(), home: t.TempDir()}
}

func (d dirs) writeProject(t *testing.T, content string) {
	t.Helper()
	writeFile(t, filepath.Join(d.project, ".r-loop", "config.yaml"), content)
}

func (d dirs) writeHome(t *testing.T, content string) {
	t.Helper()
	writeFile(t, filepath.Join(d.home, ".config", "r-loop", "config.yaml"), content)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (d dirs) load(t *testing.T, overrides ...Override) LoopConfig {
	t.Helper()
	cfg, err := Load(d.project, d.home, overrides)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func (d dirs) loadErr(t *testing.T, want string, overrides ...Override) {
	t.Helper()
	_, err := Load(d.project, d.home, overrides)
	if err == nil {
		t.Fatalf("Load succeeded, want error containing %q", want)
	}
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error %v is not ErrConfig", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
}

func TestDefaults(t *testing.T) {
	cfg := newDirs(t).load(t)

	if !reflect.DeepEqual(cfg.Pipeline, []string{"plan", "implement"}) {
		t.Errorf("Pipeline = %v", cfg.Pipeline)
	}
	wantPlan := StepRow{Prompt: "plan", Check: "plan-file", Provider: "claude", Model: "fable", Effort: "medium",
		Fallback: Fallback{Provider: "codex"}, Timeout: time.Hour,
		Reviewers: []Reviewer{{Provider: "codex"}}, Rounds: 2, ReviewTimeout: 20 * time.Minute}
	if !reflect.DeepEqual(cfg.Steps["plan"], wantPlan) {
		t.Errorf("plan = %+v", cfg.Steps["plan"])
	}
	wantImpl := StepRow{Prompt: "implement", Check: "diff", Provider: "codex", Model: "gpt-5.6-sol", Effort: "medium",
		Fallback: Fallback{Provider: "claude"}, Timeout: 4 * time.Hour,
		Reviewers: []Reviewer{{Provider: "claude"}, {Name: "ui", Provider: "claude", Model: "opus", Effort: "high", Prompt: "review-ui", Requires: ".claude/skills/test-app/SKILL.md"}}, Rounds: 3, ReviewTimeout: 45 * time.Minute}
	if !reflect.DeepEqual(cfg.Steps["implement"], wantImpl) {
		t.Errorf("implement = %+v", cfg.Steps["implement"])
	}
	wantMilestone := StepRow{Prompt: "milestone", Check: "report", Provider: "claude", Model: "opus", Effort: "medium", Timeout: time.Hour}
	if !reflect.DeepEqual(cfg.Steps["milestone"], wantMilestone) {
		t.Errorf("milestone = %+v", cfg.Steps["milestone"])
	}
	wantLand := Land{FixRounds: 1, GateTimeout: 30 * time.Minute, Fix: GateFix{Provider: "codex", Model: "gpt-5.6-sol", Effort: "medium"}}
	if cfg.Land != wantLand {
		t.Errorf("Land = %+v", cfg.Land)
	}
	wantUnattended := Unattended{Allow: []string{"deps", "ports", "locks", "restart", "retry", "provider"}}
	if !reflect.DeepEqual(cfg.Unattended, wantUnattended) {
		t.Errorf("Unattended = %+v", cfg.Unattended)
	}
	wantWatchdog := Watchdog{Provider: "claude", Model: "opus", Effort: "high", Allow: []string{}, RemedyWindow: 10 * time.Minute,
		CheckTimeout: 10 * time.Minute, StallGrace: 2 * time.Minute, UnblockTimeout: 2 * time.Hour,
		OvertimeFactor: 2, DiffFactor: 3, MaxRestarts: 2}
	if !reflect.DeepEqual(cfg.Watchdog, wantWatchdog) {
		t.Errorf("Watchdog = %+v", cfg.Watchdog)
	}
	if cfg.Notify != (Notify{}) {
		t.Errorf("Notify = %+v", cfg.Notify)
	}
	if cfg.Provenance["steps.plan.provider"] != "default" {
		t.Errorf("provenance = %q", cfg.Provenance["steps.plan.provider"])
	}
}

func TestProjectFileOverridesOneKeyOfMachineFile(t *testing.T) {
	d := newDirs(t)
	d.writeHome(t, "steps:\n  plan:\n    model: sonnet\n    effort: low\n")
	d.writeProject(t, "steps:\n  plan:\n    model: haiku\n")

	cfg := d.load(t)

	if cfg.Steps["plan"].Model != "haiku" || cfg.Steps["plan"].Effort != "low" || cfg.Steps["plan"].Provider != "claude" {
		t.Errorf("plan = %+v", cfg.Steps["plan"])
	}
	if got := cfg.Provenance["steps.plan.model"]; got != ".r-loop/config.yaml:steps.plan.model" {
		t.Errorf("model provenance = %q", got)
	}
	if got := cfg.Provenance["steps.plan.effort"]; got != "~/.config/r-loop/config.yaml:steps.plan.effort" {
		t.Errorf("effort provenance = %q", got)
	}
	if got := cfg.Provenance["steps.plan.provider"]; got != "default" {
		t.Errorf("provider provenance = %q", got)
	}
}

func TestFlowStyleListRejectedWithItsLine(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  plan:\n    reviewers: [codex, claude]\n")

	d.loadErr(t, "config.yaml:3: flow style is not accepted, write it block style")
}

func TestUnknownKeyRejected(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "watchdog:\n  colour: red\n")

	d.loadErr(t, `config.yaml:2: unknown key "watchdog.colour"`)
}

func TestMixedReviewerList(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  implement:\n    reviewers:\n      - claude\n      - provider: codex\n        model: gpt-5.6-sol\n        effort: high\n")

	cfg := d.load(t)

	want := []Reviewer{{Provider: "claude"}, {Provider: "codex", Model: "gpt-5.6-sol", Effort: "high"}}
	if !reflect.DeepEqual(cfg.Steps["implement"].Reviewers, want) {
		t.Errorf("Reviewers = %+v", cfg.Steps["implement"].Reviewers)
	}
}

func TestNamedReviewerWithPromptAndRequires(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  implement:\n    reviewers:\n      - claude\n      - name: ui\n        provider: claude\n        prompt: review-ui\n        requires: .claude/skills/test-app/SKILL.md\n")

	cfg := d.load(t)

	want := []Reviewer{{Provider: "claude"}, {Provider: "claude", Name: "ui", Prompt: "review-ui", Requires: ".claude/skills/test-app/SKILL.md"}}
	if !reflect.DeepEqual(cfg.Steps["implement"].Reviewers, want) {
		t.Errorf("Reviewers = %+v", cfg.Steps["implement"].Reviewers)
	}
}

func TestTwoReviewersWithTheSameNameRejected(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  implement:\n    reviewers:\n      - claude\n      - provider: claude\n        model: opus\n")

	d.loadErr(t, `config.yaml:5: steps.implement.reviewers: two reviewers named "claude", give one a name`)
}

func TestReviewerNameMustBeAToken(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  implement:\n    reviewers:\n      - name: UI check\n        provider: claude\n")

	d.loadErr(t, "config.yaml:4: steps.implement.reviewers.name must be lowercase letters, digits and dashes")
}

func TestReviewerRequiresMustStayInTheRepository(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  implement:\n    reviewers:\n      - name: ui\n        provider: claude\n        requires: ../skill.md\n")

	d.loadErr(t, "config.yaml:4: steps.implement.reviewers.requires must be a path inside the repository")
}

func TestFallbackTakesNoReviewerKeys(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  implement:\n    fallback:\n      provider: claude\n      name: fb\n")

	d.loadErr(t, `config.yaml:5: unknown key "steps.implement.fallback.name"`)
}

func TestReviewerBlockWithoutProviderRejected(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  implement:\n    reviewers:\n      - model: opus\n")

	d.loadErr(t, "config.yaml:4:")
}

func TestRoundsZeroTurnsReviewOff(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  plan:\n    rounds: 0\n")

	cfg := d.load(t)

	if cfg.Steps["plan"].Rounds != 0 {
		t.Errorf("Rounds = %d", cfg.Steps["plan"].Rounds)
	}
	if strings.Contains(Banner(cfg), "review rounds 0") {
		t.Errorf("banner shows a review half for rounds 0:\n%s", Banner(cfg))
	}
}

func TestNegativeRoundsRejected(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  plan:\n    rounds: -1\n")

	d.loadErr(t, "config.yaml:3:")
}

func TestScalarFallback(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  plan:\n    fallback: gemini\n")

	cfg := d.load(t)

	if cfg.Steps["plan"].Fallback != (Fallback{Provider: "gemini"}) {
		t.Errorf("Fallback = %+v", cfg.Steps["plan"].Fallback)
	}
}

func TestBlockFallbackWithModelAndEffort(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  implement:\n    fallback:\n      provider: claude\n      model: sonnet\n      effort: high\n")

	cfg := d.load(t)

	if cfg.Steps["implement"].Fallback != (Fallback{Provider: "claude", Model: "sonnet", Effort: "high"}) {
		t.Errorf("Fallback = %+v", cfg.Steps["implement"].Fallback)
	}
}

func TestFallbackNamingOwnProviderRejected(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  plan:\n    fallback: claude\n")

	d.loadErr(t, "config.yaml:3:")
}

func TestGateTimeoutNotADurationRejected(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "land:\n  gateTimeout: soon\n")

	d.loadErr(t, "config.yaml:2:")
}

func TestLandFixWithOnlyEffortInheritsImplementProviderAndModel(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "land:\n  fix:\n    effort: high\n")

	cfg := d.load(t)

	if cfg.Land.Fix != (GateFix{Provider: "codex", Model: "gpt-5.6-sol", Effort: "high"}) {
		t.Errorf("Fix = %+v", cfg.Land.Fix)
	}
	if got := cfg.Provenance["land.fix.effort"]; got != ".r-loop/config.yaml:land.fix.effort" {
		t.Errorf("effort provenance = %q", got)
	}
	if got := cfg.Provenance["land.fix.provider"]; got != "default" {
		t.Errorf("provider provenance = %q", got)
	}
}

func TestLandFixNamingAnotherProviderLeavesModelAndEffortEmpty(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "land:\n  fix:\n    provider: claude\n")

	cfg := d.load(t)

	if cfg.Land.Fix != (GateFix{Provider: "claude"}) {
		t.Errorf("Fix = %+v", cfg.Land.Fix)
	}
}

func TestUnattendedAllowOutsideRemedyClassesRejected(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "unattended:\n  allow:\n    - deps\n    - reboot\n")

	d.loadErr(t, "config.yaml:4:")
}

func TestFactorBelowOneRejected(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "watchdog:\n  diffFactor: 0.5\n")

	d.loadErr(t, "config.yaml:2:")
}

func TestPipelineEntryWithoutStepsRowRejected(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "pipeline:\n  - plan\n  - docs\n")

	d.loadErr(t, `"docs"`)
}

func TestAddedStepRow(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "pipeline:\n  - plan\n  - implement\n  - docs\nsteps:\n  docs:\n    prompt: docs\n    check: diff\n    provider: claude\n    timeout: 30m\n")

	cfg := d.load(t)

	want := StepRow{Prompt: "docs", Check: "diff", Provider: "claude", Timeout: 30 * time.Minute}
	if !reflect.DeepEqual(cfg.Steps["docs"], want) {
		t.Errorf("docs = %+v", cfg.Steps["docs"])
	}
}

func TestCheckOutsideKnownSetRejected(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  plan:\n    check: vibes\n")

	d.loadErr(t, "config.yaml:3:")
}

func TestProvidersKeptAsRawBlocks(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "providers:\n  gemini:\n    kind: gemini\n")

	cfg := d.load(t)

	node, ok := cfg.Providers["gemini"]
	if !ok || len(node.Content) != 2 || node.Content[1].Value != "gemini" {
		t.Errorf("Providers = %+v", cfg.Providers)
	}
}

func TestOverridesWithProvenance(t *testing.T) {
	cfg := newDirs(t).load(t,
		Override{Key: "provider", Step: "implement", Value: "gemini"},
		Override{Key: "model", Step: "plan", Value: "sonnet"},
		Override{Key: "effort", Step: "milestone", Value: "low"},
	)

	if cfg.Steps["implement"].Provider != "gemini" || cfg.Provenance["steps.implement.provider"] != "flag:--provider" {
		t.Errorf("implement provider = %q from %q", cfg.Steps["implement"].Provider, cfg.Provenance["steps.implement.provider"])
	}
	if cfg.Steps["plan"].Model != "sonnet" || cfg.Provenance["steps.plan.model"] != "flag:--model" {
		t.Errorf("plan model = %q from %q", cfg.Steps["plan"].Model, cfg.Provenance["steps.plan.model"])
	}
	if cfg.Steps["milestone"].Effort != "low" || cfg.Provenance["steps.milestone.effort"] != "flag:--effort" {
		t.Errorf("milestone effort = %q from %q", cfg.Steps["milestone"].Effort, cfg.Provenance["steps.milestone.effort"])
	}
}

func TestOverrideLeavesFallbackAndReviewersUntouched(t *testing.T) {
	cfg := newDirs(t).load(t,
		Override{Key: "model", Step: "implement", Value: "gpt-6"},
		Override{Key: "effort", Step: "implement", Value: "high"},
	)

	if cfg.Steps["implement"].Fallback != (Fallback{Provider: "claude"}) {
		t.Errorf("Fallback = %+v", cfg.Steps["implement"].Fallback)
	}
	if !reflect.DeepEqual(cfg.Steps["implement"].Reviewers, []Reviewer{{Provider: "claude"}, {Name: "ui", Provider: "claude", Model: "opus", Effort: "high", Prompt: "review-ui", Requires: ".claude/skills/test-app/SKILL.md"}}) {
		t.Errorf("Reviewers = %+v", cfg.Steps["implement"].Reviewers)
	}
}

func TestAbsentLandFixFollowsModelOverride(t *testing.T) {
	cfg := newDirs(t).load(t, Override{Key: "model", Step: "implement", Value: "gpt-6"})

	if cfg.Land.Fix != (GateFix{Provider: "codex", Model: "gpt-6", Effort: "medium"}) {
		t.Errorf("Fix = %+v", cfg.Land.Fix)
	}
	if got := cfg.Provenance["land.fix.model"]; got != "flag:--model" {
		t.Errorf("fix model provenance = %q", got)
	}
}

func TestOverrideErrors(t *testing.T) {
	d := newDirs(t)
	d.loadErr(t, `"nope"`, Override{Key: "provider", Step: "nope", Value: "claude"})
	d.loadErr(t, "--model", Override{Key: "model", Step: "plan", Value: "a"}, Override{Key: "model", Step: "plan", Value: "b"})
}

func TestParseOverride(t *testing.T) {
	o, err := ParseOverride("effort", "implement=high")
	if err != nil || o != (Override{Key: "effort", Step: "implement", Value: "high"}) {
		t.Fatalf("ParseOverride = %+v, %v", o, err)
	}
	if _, err := ParseOverride("provider", "implement"); !errors.Is(err, ErrConfig) {
		t.Fatalf("missing = gave %v", err)
	}
}

func TestBannerForTwoOverrideConfig(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  implement:\n    provider: claude\n    fallback:\n      provider: gemini\n      model: pro\n      effort: high\nland:\n  fix:\n    effort: high\nwatchdog:\n  effort: low\n  allow:\n    - deps\n")

	cfg := d.load(t,
		Override{Key: "provider", Step: "implement", Value: "codex"},
		Override{Key: "model", Step: "plan", Value: "sonnet"},
	)

	want := strings.Join([]string{
		"plan  claude  sonnet  medium  1h  plan-file  ← provider default model flag:--model effort default",
		"  review rounds 2 20m  ← default",
		"  reviewer codex provider default provider default  ← default",
		"  fallback codex provider default provider default  ← default",
		"implement  codex  gpt-5.6-sol  medium  4h  diff  ← provider flag:--provider model default effort default",
		"  review rounds 3 45m  ← default",
		"  reviewer claude provider default provider default  ← default",
		"  reviewer claude opus high (name ui, prompt review-ui, requires .claude/skills/test-app/SKILL.md)  ← default",
		"  fallback gemini pro high  ← .r-loop/config.yaml:steps.implement.fallback",
		"milestone  claude  opus  medium  1h  report  ← default",
		"gatefix codex gpt-5.6-sol high  ← provider flag:--provider model default effort .r-loop/config.yaml:land.fix.effort",
		"override: implement provider codex (flag) replaces claude (.r-loop/config.yaml)",
		"override: plan model sonnet (flag) replaces fable (default)",
		"watchdog: claude opus low allow [deps]  ← provider default model default effort .r-loop/config.yaml:watchdog.effort",
	}, "\n") + "\n"
	if got := Banner(cfg); got != want {
		t.Errorf("banner:\n%s\nwant:\n%s", got, want)
	}
}

func TestProviderOverrideOntoFallbackSwapsTheFallback(t *testing.T) {
	cfg := newDirs(t).load(t, Override{Key: "provider", Step: "implement", Value: "claude"})

	row := cfg.Steps["implement"]
	if row.Provider != "claude" || row.Model != "gpt-5.6-sol" || row.Effort != "medium" {
		t.Errorf("row = %s %s %s", row.Provider, row.Model, row.Effort)
	}
	if row.Fallback != (Fallback{Provider: "codex", Model: "gpt-5.6-sol", Effort: "medium"}) {
		t.Errorf("Fallback = %+v", row.Fallback)
	}
	if got := cfg.Provenance["steps.implement.fallback"]; got != "flag:--provider" {
		t.Errorf("fallback provenance = %q", got)
	}
	if cfg.Steps["plan"].Fallback != (Fallback{Provider: "codex"}) {
		t.Errorf("plan Fallback = %+v", cfg.Steps["plan"].Fallback)
	}
}

func TestSwappedFallbackKeepsTheConfiguredModelAndEffortNotTheOverrides(t *testing.T) {
	cfg := newDirs(t).load(t,
		Override{Key: "model", Step: "implement", Value: "opus"},
		Override{Key: "provider", Step: "implement", Value: "claude"},
		Override{Key: "effort", Step: "implement", Value: "high"},
	)

	row := cfg.Steps["implement"]
	if row.Provider != "claude" || row.Model != "opus" || row.Effort != "high" {
		t.Errorf("row = %s %s %s", row.Provider, row.Model, row.Effort)
	}
	if row.Fallback != (Fallback{Provider: "codex", Model: "gpt-5.6-sol", Effort: "medium"}) {
		t.Errorf("Fallback = %+v", row.Fallback)
	}
}

func TestPlanProviderOverrideOntoFallbackSwaps(t *testing.T) {
	cfg := newDirs(t).load(t, Override{Key: "provider", Step: "plan", Value: "codex"})

	if cfg.Steps["plan"].Fallback != (Fallback{Provider: "claude", Model: "fable", Effort: "medium"}) {
		t.Errorf("Fallback = %+v", cfg.Steps["plan"].Fallback)
	}
}

func TestBannerShowsTheSwappedFallbackWithProvenance(t *testing.T) {
	cfg := newDirs(t).load(t, Override{Key: "provider", Step: "implement", Value: "claude"})

	banner := Banner(cfg)

	for _, want := range []string{
		"implement  claude  gpt-5.6-sol  medium  4h  diff  ← provider flag:--provider model default effort default\n",
		"  fallback codex gpt-5.6-sol medium  ← flag:--provider\n",
		"override: implement provider claude (flag) replaces codex (default)\n",
		"override: steps.implement.fallback swapped to codex (--provider) replaces claude (default)\n",
	} {
		if !strings.Contains(banner, want) {
			t.Errorf("%q missing from:\n%s", want, banner)
		}
	}
}

func TestProviderOverrideOntoAConfiguredFallbackSwapsItWithItsFileProvenance(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  implement:\n    fallback:\n      provider: claude\n      model: sonnet\n      effort: low\n")

	cfg := d.load(t, Override{Key: "provider", Step: "implement", Value: "claude"})

	if cfg.Steps["implement"].Fallback != (Fallback{Provider: "codex", Model: "gpt-5.6-sol", Effort: "medium"}) {
		t.Errorf("Fallback = %+v", cfg.Steps["implement"].Fallback)
	}
	if !strings.Contains(Banner(cfg), "override: steps.implement.fallback swapped to codex (--provider) replaces claude sonnet low (.r-loop/config.yaml)\n") {
		t.Errorf("banner:\n%s", Banner(cfg))
	}
}

func TestProviderOverrideToTheRowsOwnProviderLeavesTheFallback(t *testing.T) {
	cfg := newDirs(t).load(t, Override{Key: "provider", Step: "implement", Value: "codex"})

	if cfg.Steps["implement"].Fallback != (Fallback{Provider: "claude"}) {
		t.Errorf("Fallback = %+v", cfg.Steps["implement"].Fallback)
	}
	if cfg.Provenance["steps.implement.fallback"] != "default" || strings.Contains(Banner(cfg), "swapped") {
		t.Errorf("fallback touched:\n%s", Banner(cfg))
	}
}

func TestLandFixExplicitEmptyEffortIsProviderDefault(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "land:\n  fix:\n    effort: \"\"\n")

	cfg := d.load(t)

	if cfg.Land.Fix != (GateFix{Provider: "codex", Model: "gpt-5.6-sol"}) {
		t.Errorf("Fix = %+v", cfg.Land.Fix)
	}
	if got := cfg.Provenance["land.fix.effort"]; got != ".r-loop/config.yaml:land.fix.effort" {
		t.Errorf("effort provenance = %q", got)
	}
}

func TestEmptyScalarReviewerRejected(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "steps:\n  implement:\n    reviewers:\n      - \"\"\n")

	d.loadErr(t, "config.yaml:4:")
}

func TestWatchdogFallbackIsAnUnknownKey(t *testing.T) {
	d := newDirs(t)
	d.writeProject(t, "watchdog:\n  fallback: codex\n")

	d.loadErr(t, "watchdog.fallback")
}
