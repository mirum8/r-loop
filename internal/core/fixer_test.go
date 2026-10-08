package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWriteIncidentWritesEverySection(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	var records []Record
	for i := 1; i <= 52; i++ {
		ev := Event{Kind: "nudge", Fields: map[string]string{"n": fmt.Sprint(i)}}
		records = append(records, Record{Kind: RecordEvent, At: at, Event: &ev})
	}
	in := Incident{
		Question:  Question{ID: "b3", Kind: QuestionBlocker, Text: "blocker b3 from phase-2/implement (step): failed: flag --foo unknown\nactions: retry, block, stop; resolve it with resolve_blocker"},
		Diagnosis: "codex 0.48 renamed --foo",
		Excerpt:   "error: unexpected argument '--foo'",
		Start:     map[string]string{"claude": "2.1.220", "codex": "0.47.0"},
		Now:       map[string]string{"claude": "2.1.220", "codex": "0.48.1"},
		Records:   records,
		Config:    "watchdog:\n  provider: claude # default\n",
		Root:      "/repo", Worktree: "/repo/.r-loop/wt/phase-2", RunDir: "/repo/.r-loop/runs/r1",
	}

	if err := writeIncident(dir, in); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "fix-b3", "incident.md"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []string
	for i := 3; i <= 52; i++ {
		recs = append(recs, `{"Kind":"event","At":"2026-10-08T12:00:00Z","Step":null,"State":"","Run":"","Reason":"","Question":null,"Signal":null,"Remedy":null,"Landing":null,"Event":{"At":"0001-01-01T00:00:00Z","Kind":"nudge","Phase":"","Step":"","Fields":{"n":"`+fmt.Sprint(i)+`"}}}`)
	}
	want := "# Incident b3\n\n" +
		"## Blocker\n\nb3 phase-2/implement (step): failed: flag --foo unknown (open)\n\n" +
		"```\nblocker b3 from phase-2/implement (step): failed: flag --foo unknown\nactions: retry, block, stop; resolve it with resolve_blocker\n```\n\n" +
		"## Diagnosis\n\ncodex 0.48 renamed --foo\n\n" +
		"## Excerpt\n\n```\nerror: unexpected argument '--foo'\n```\n\n" +
		"## Versions\n\nrun start → now\n\n- claude 2.1.220\n- codex 0.47.0 → 0.48.1\n\n" +
		"## Recent records\n\n```\n" + strings.Join(recs, "\n") + "\n```\n\n" +
		"## Config\n\n```\nwatchdog:\n  provider: claude # default\n```\n\n" +
		"## Paths\n\n- root: /repo\n- worktree: /repo/.r-loop/wt/phase-2\n- run: /repo/.r-loop/runs/r1\n"
	if got := string(data); got != want {
		t.Errorf("incident:\n%s\nwant:\n%s", got, want)
	}
}

func TestWriteIncidentNamesWhatIsMissing(t *testing.T) {
	dir := t.TempDir()
	in := Incident{Question: Question{ID: "b1", Kind: QuestionBlocker, Text: "blocker b1 from phase-1/land (land): merge conflict"}, Root: "/repo", RunDir: "/repo/.r-loop/runs/r1"}

	if err := writeIncident(dir, in); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "fix-b1", "incident.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Diagnosis\n\nnone\n\n",
		"## Excerpt\n\n```\n\n```\n\n",
		"## Versions\n\nrun start → now\n\nnone recorded\n\n",
		"## Recent records\n\n```\n\n```\n\n",
		"- worktree: none\n",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("incident lacks %q:\n%s", want, data)
		}
	}
}

type fixerRig struct {
	*blockerRig
	fixer    *Fixer
	prompts  *varsCapture
	mu       sync.Mutex
	posts    []string
	resolved []string
	checked  []string
	badCfg   error
	reloads  int
	badLoad  error
}

func newFixerRig(t *testing.T) *fixerRig {
	r := &fixerRig{blockerRig: newBlockerRig(t), prompts: &varsCapture{}}
	r.fixer = &Fixer{
		Host: r.dhost, Prompts: r.prompts, Store: r.store, Repo: r.repo, Dialogs: r.loop, Face: r.face,
		Resolve: func(provider, model, effort, askURL, mcp, dir string) (ProviderArgs, error) {
			r.mu.Lock()
			r.resolved = append(r.resolved, strings.Join([]string{provider, model, effort, askURL, mcp, dir}, "|"))
			r.mu.Unlock()
			return ProviderArgs{Kind: provider, Args: []string{"--model", model}}, nil
		},
		Post: func(text string) {
			r.mu.Lock()
			r.posts = append(r.posts, text)
			r.mu.Unlock()
		},
		CheckConfig: func(file, content string) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.checked = append(r.checked, file+"\n"+content)
			return r.badCfg
		},
		Reload: func() error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.reloads++
			return r.badLoad
		},
		Blocker: r.loop.OpenBlocker, Settle: r.loop.SettleBlocker,
		Provider: "claude", Model: "opus", Effort: "high",
		RunID: "run-1", Root: r.repo.RootDir, RunDir: r.store.dir, Home: t.TempDir(), Pane: "driver-pane",
		Timeout: time.Hour, Poll: time.Millisecond,
	}
	r.loop.Fixer = r.fixer
	r.router.Fix = r.loop.StartFix
	r.router.FixOpen = r.loop.FixOpen
	return r
}

func fixableBlocker() Blocker {
	return Blocker{Source: "land", Phase: "2", Step: "land", Reason: "merge conflict in a.go", Excerpt: "CONFLICT (content): a.go", Actions: []string{"retry", "fix", "block", "stop"}}
}

func (r *fixerRig) started(t *testing.T) <-chan Resolution {
	t.Helper()
	ch := r.open(t, fixableBlocker())
	if d, reason := r.resolve("b1", "fix", addendum("codex 0.48 renamed --foo")); d != decisionAuthorised {
		t.Fatalf("fix %s %q", d, reason)
	}
	return ch
}

func (r *fixerRig) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.store.dir, "fix-b1", name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *fixerRig) propose(t *testing.T, proposal string) {
	t.Helper()
	r.write(t, "proposal.json", proposal)
	r.write(t, "fixer.sentinel", `{"outcome":"ok","reason":""}`)
}

func (r *fixerRig) posted() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.posts...)
}

func (r *fixerRig) failedWith(t *testing.T) string {
	t.Helper()
	waitFor(t, func() bool { return len(r.events("fix-failed")) == 1 })
	return r.events("fix-failed")[0].Fields["reason"]
}

const configProposal = `{"kind":"config","cause":"codex 0.48 renamed --foo","evidence":"codex --help lists --bar","config":{"file":".r-loop/config.yaml","content":"providers:\n  codex:\n    flags: \"--bar\"\n"},"risk":"none known"}`

func TestAFixStartsTheFixerInANewTabWithTheIncident(t *testing.T) {
	r := newFixerRig(t)
	wt := filepath.Join(r.repo.RootDir, ".r-loop", "wt", "phase-2")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}

	ch := r.started(t)

	stillHeld(t, ch)
	if got := r.calls("Repo.Snapshot "); !reflect.DeepEqual(got, []string{"", wt}) {
		t.Errorf("snapshots %q", got)
	}
	if got := r.calls("SessionHost.Split "); len(got) != 0 {
		t.Errorf("splits %q", got)
	}
	if got := r.calls("SessionHost.OpenTab "); !reflect.DeepEqual(got, []string{"ws-of-driver-pane " + r.repo.RootDir + " ◆ fixer b1 map[]"}) {
		t.Errorf("tabs %q", got)
	}
	if got := r.calls("SessionHost.Start "); !reflect.DeepEqual(got, []string{"pane-1 rloop-fix-b1-run-1 claude [--model opus]"}) {
		t.Errorf("starts %q", got)
	}
	if got := r.calls("SessionHost.Prompt "); !reflect.DeepEqual(got, []string{`rloop-fix-b1-run-1 "watch the run" false 0s`}) {
		t.Errorf("prompts %q", got)
	}
	if !reflect.DeepEqual(r.resolved, []string{"claude|opus|high|||"}) {
		t.Errorf("resolved %q", r.resolved)
	}
	dir := filepath.Join(r.store.dir, "fix-b1")
	want := map[string]any{
		"IncidentPath": filepath.Join(dir, "incident.md"), "ProposalPath": filepath.Join(dir, "proposal.json"),
		"Sentinel": filepath.Join(dir, "fixer.sentinel"), "RunDir": r.store.dir, "Root": r.repo.RootDir, "Addendum": "",
	}
	if r.prompts.name != "fixer" || !reflect.DeepEqual(r.prompts.vars, want) {
		t.Errorf("prompt %s %v", r.prompts.name, r.prompts.vars)
	}
	started := r.events("fix-started")
	if len(started) != 1 || started[0].Phase != "2" || started[0].Step != "land" || !reflect.DeepEqual(started[0].Fields, map[string]string{"id": "b1", "provider": "claude", "model": "opus"}) {
		t.Errorf("fix-started %+v", started)
	}
	data, err := os.ReadFile(filepath.Join(dir, "incident.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"## Diagnosis\n\ncodex 0.48 renamed --foo\n\n", "## Excerpt\n\n```\nCONFLICT (content): a.go\n```\n\n", "- worktree: " + wt + "\n"} {
		if !strings.Contains(string(data), part) {
			t.Errorf("incident lacks %q:\n%s", part, data)
		}
	}
	if !r.fixer.Open("b1") {
		t.Error("the fix is not open")
	}
}

func TestAFixerWhoseTabCannotOpenGetsAWorkspaceOfItsOwn(t *testing.T) {
	r := newFixerRig(t)
	r.dhost.TabErr = errors.New("no tab")

	r.started(t)

	if got := r.calls("SessionHost.Split "); len(got) != 0 {
		t.Errorf("splits %q", got)
	}
	if got := r.calls("SessionHost.Open "); len(got) != 1 || !strings.HasPrefix(got[0], r.repo.RootDir+" ◆ fixer b1") {
		t.Errorf("opens %q", got)
	}
}

func TestAFixerNameKeepsTheRunIDsTailWithin32Characters(t *testing.T) {
	if got := FixerName("b12", "20261008-120000-2"); got != "rloop-fix-b12-20261008-120000-2" {
		t.Errorf("name %q", got)
	}
	if got := FixerName("b12", "Release run 20261008-120000-12"); got != "rloop-fix-b12-20261008-120000-12" {
		t.Errorf("name %q", got)
	}
}

func TestAValidProposalClosesThePaneAndIsPostedToTheWatchdog(t *testing.T) {
	r := newFixerRig(t)
	ch := r.started(t)

	r.propose(t, configProposal)

	waitFor(t, func() bool { return len(r.events("fix-proposed")) == 1 })
	ev := r.events("fix-proposed")[0]
	if ev.Phase != "2" || ev.Step != "land" || !reflect.DeepEqual(ev.Fields, map[string]string{"id": "b1", "kind": "config", "cause": "codex 0.48 renamed --foo"}) {
		t.Errorf("fix-proposed %+v", ev)
	}
	if got := r.calls("SessionHost.ClosePane "); !reflect.DeepEqual(got, []string{"pane-1"}) {
		t.Errorf("closed %q", got)
	}
	waitFor(t, func() bool { return len(r.posted()) == 1 })
	want := "fix b1 proposed (config): codex 0.48 renamed --foo\n\n" +
		"cause: codex 0.48 renamed --foo\nevidence: codex --help lists --bar\ncommands: none\n" +
		"config: .r-loop/config.yaml, new content:\n```\nproviders:\n  codex:\n    flags: \"--bar\"\n```\n" +
		"manual: none\nrisk: none known\n\napply it with apply_fix after the maintainer confirms"
	if got := r.posted(); !reflect.DeepEqual(got, []string{want}) {
		t.Errorf("posts %q\nwant %q", got, want)
	}
	if !reflect.DeepEqual(r.checked, []string{".r-loop/config.yaml\nproviders:\n  codex:\n    flags: \"--bar\"\n"}) {
		t.Errorf("checked %q", r.checked)
	}
	fx, _ := r.fixer.Fix("b1")
	if fx.State != "proposed" || fx.Proposal == nil || fx.Proposal.Config.File != ".r-loop/config.yaml" || fx.PrimaryTree != "tree" {
		t.Errorf("fix %+v", fx)
	}
	if r.fixer.Open("b1") {
		t.Error("the fix is still open")
	}
	stillHeld(t, ch)
}

func TestAnRLoopProposalIsPostedAsNotApplied(t *testing.T) {
	r := newFixerRig(t)
	r.started(t)

	r.propose(t, `{"kind":"r-loop","cause":"blockers.go:12 drops the excerpt","manual":["upgrade r-loop"]}`)

	waitFor(t, func() bool { return len(r.posted()) == 1 })
	want := "fix b1 proposed (r-loop): blockers.go:12 drops the excerpt\n\n" +
		"cause: blockers.go:12 drops the excerpt\nevidence: none\ncommands: none\nconfig: unchanged\nmanual:\n- upgrade r-loop\nrisk: none\n\n" +
		"r-loop does not apply a r-loop fix: show it to the maintainer, then resolve the blocker with its other actions"
	if got := r.posted()[0]; got != want {
		t.Errorf("post %q\nwant %q", got, want)
	}
}

func TestAnInvalidProposalFailsTheFixWithItsReason(t *testing.T) {
	for name, tc := range map[string]struct {
		proposal, reason string
		badCfg           error
	}{
		"kind":            {`{"kind":"magic","cause":"x"}`, `proposal: kind "magic" is not env, config, r-loop or project`, nil},
		"cause":           {`{"kind":"env","cause":"  ","commands":["brew upgrade codex"]}`, "proposal: cause is empty", nil},
		"r-loop commands": {`{"kind":"r-loop","cause":"x","commands":["make"]}`, "proposal: a r-loop fix carries no commands and no config", nil},
		"project config":  {`{"kind":"project","cause":"x","config":{"file":".r-loop/config.yaml","content":"label: x\n"}}`, "proposal: a project fix carries no commands and no config", nil},
		"empty command":   {`{"kind":"env","cause":"x","commands":["npm i -g codex@0.47.0"," "]}`, "proposal: command 2 is empty", nil},
		"config file":     {`{"kind":"config","cause":"x","config":{"file":"/etc/r-loop.yaml","content":"label: x\n"}}`, `proposal: config file "/etc/r-loop.yaml" is not .r-loop/config.yaml or ~/.config/r-loop/config.yaml`, nil},
		"config check":    {`{"kind":"config","cause":"x","config":{"file":"~/.config/r-loop/config.yaml","content":"steps: [plan]\n"}}`, "proposal: config ~/.config/r-loop/config.yaml: ~/.config/r-loop/config.yaml:1: flow-style", errors.New("~/.config/r-loop/config.yaml:1: flow-style")},
		"unknown key":     {`{"kind":"env","cause":"x","fix":"now"}`, `proposal: proposal.json: json: unknown field "fix"`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			r := newFixerRig(t)
			r.badCfg = tc.badCfg
			ch := r.started(t)

			r.propose(t, tc.proposal)

			if got := r.failedWith(t); got != tc.reason {
				t.Errorf("reason %q, want %q", got, tc.reason)
			}
			waitFor(t, func() bool { return len(r.posted()) == 1 })
			if got := r.posted(); !reflect.DeepEqual(got, []string{"fix b1 failed: " + tc.reason}) {
				t.Errorf("posts %q", got)
			}
			if got := r.calls("SessionHost.ClosePane "); !reflect.DeepEqual(got, []string{"pane-1"}) {
				t.Errorf("closed %q", got)
			}
			if len(r.events("fix-proposed")) != 0 {
				t.Error("an invalid proposal was proposed")
			}
			stillHeld(t, ch)
		})
	}
}

func TestAFixerThatFailsFailsTheFixWithTheReason(t *testing.T) {
	for name, tc := range map[string]struct {
		act    func(t *testing.T, r *fixerRig)
		reason string
	}{
		"failed sentinel": {func(t *testing.T, r *fixerRig) {
			r.write(t, "fixer.sentinel", `{"outcome":"failed","reason":"no network"}`)
		}, "the fixer failed: no network"},
		"agent gone": {func(t *testing.T, r *fixerRig) {
			r.dhost.set("rloop-fix-b1-run-1", AgentGone, "")
		}, "agent gone"},
		"no proposal": {func(t *testing.T, r *fixerRig) {
			r.write(t, "fixer.sentinel", `{"outcome":"ok","reason":""}`)
		}, "proposal: open " + "%s" + ": no such file or directory"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newFixerRig(t)
			r.started(t)

			tc.act(t, r)

			want := tc.reason
			if strings.Contains(want, "%s") {
				want = fmt.Sprintf(want, filepath.Join(r.store.dir, "fix-b1", "proposal.json"))
			}
			if got := r.failedWith(t); got != want {
				t.Errorf("reason %q, want %q", got, want)
			}
			waitFor(t, func() bool { return len(r.posted()) == 1 })
			if got := r.posted(); !reflect.DeepEqual(got, []string{"fix b1 failed: " + want}) {
				t.Errorf("posts %q", got)
			}
			if got := r.calls("SessionHost.ClosePane "); !reflect.DeepEqual(got, []string{"pane-1"}) {
				t.Errorf("closed %q", got)
			}
		})
	}
}

func TestAFixerThatChangedTheTreeFailsTheFix(t *testing.T) {
	r := newFixerRig(t)
	r.started(t)
	r.repo.setChanges("go.mod")

	r.propose(t, configProposal)

	if got, want := r.failedWith(t), "the fixer changed "+r.repo.RootDir+": go.mod"; got != want {
		t.Errorf("reason %q, want %q", got, want)
	}
	if len(r.events("fix-proposed")) != 0 {
		t.Error("a fix that changed the tree was proposed")
	}
}

func TestAFixerThatChangedTheUserConfigFailsTheFix(t *testing.T) {
	r := newFixerRig(t)
	r.started(t)
	dir := filepath.Join(r.fixer.Home, ".config", "r-loop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("watchdog:\n  maxRestarts: 9\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r.propose(t, configProposal)

	if got, want := r.failedWith(t), "the fixer changed ~/.config/r-loop/config.yaml"; got != want {
		t.Errorf("reason %q, want %q", got, want)
	}
	if len(r.events("fix-proposed")) != 0 {
		t.Error("a fix that changed the user config was proposed")
	}
}

func TestAFixerPastItsTimeoutFailsTheFix(t *testing.T) {
	r := newFixerRig(t)
	r.fixer.Timeout = 30 * time.Millisecond
	r.started(t)

	if got := r.failedWith(t); got != "timeout 30ms" {
		t.Errorf("reason %q", got)
	}
	if got := r.calls("SessionHost.ClosePane "); !reflect.DeepEqual(got, []string{"pane-1"}) {
		t.Errorf("closed %q", got)
	}
}

func TestADialogInTheFixersPaneGoesToTheWatchdog(t *testing.T) {
	r := newFixerRig(t)
	r.serve(t)
	r.started(t)

	r.dhost.set("rloop-fix-b1-run-1", AgentBlocked, "Allow codex --help?\n1. Yes\n2. No")

	prompts := r.waitDog(t, 2)
	if got, want := prompts[1], dialogPrompt("d1", "fix-b1", "Allow codex --help?\n1. Yes\n2. No"); got != want {
		t.Errorf("dog prompt %q\nwant %q", got, want)
	}
	if err := r.loop.DeliverKeys("d1", []string{"1"}, "watchdog", dialogRule); err != nil {
		t.Fatal(err)
	}
	if got := r.calls("SessionHost.SendKeys "); !reflect.DeepEqual(got, []string{"rloop-fix-b1-run-1 1"}) {
		t.Errorf("keys %q", got)
	}
	st, _ := r.store.Load("run-1")
	for key := range st.Steps {
		if key.Kind == "fix-b1" {
			t.Errorf("the fixer recorded a step state %+v", key)
		}
	}
}

func TestABlockerSettledWhileItsFixIsOpenClosesTheFixer(t *testing.T) {
	r := newFixerRig(t)
	ch := r.started(t)

	if d, reason := r.resolve("b1", "block"); d != decisionAuthorised {
		t.Fatalf("block %s %q", d, reason)
	}

	if res := resolved(t, ch); res.Action != "block" {
		t.Errorf("resolution %+v", res)
	}
	if got := r.failedWith(t); got != "withdrawn" {
		t.Errorf("reason %q", got)
	}
	if got := r.calls("SessionHost.ClosePane "); !reflect.DeepEqual(got, []string{"pane-1"}) {
		t.Errorf("closed %q", got)
	}
	if got := r.posted(); len(got) != 0 {
		t.Errorf("posts %q", got)
	}
	if r.fixer.Open("b1") {
		t.Error("the fix is still open")
	}
}

func (r *fixerRig) proposed(t *testing.T, proposal string) <-chan Resolution {
	t.Helper()
	ch := r.started(t)
	r.propose(t, proposal)
	waitFor(t, func() bool { return len(r.events("fix-proposed")) == 1 })
	return ch
}

func (r *fixerRig) projectConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(r.repo.RootDir, ".r-loop", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

const envConfigProposal = `{"kind":"config","cause":"codex 0.48 renamed --foo","commands":["codex --version","codex login status"],"config":{"file":".r-loop/config.yaml","content":"providers:\n  codex:\n    flags: \"--bar\"\n"}}`

func TestApplyRunsTheCommandsWritesTheConfigReloadsAndRetriesTheBlocker(t *testing.T) {
	r := newFixerRig(t)
	path := r.projectConfig(t, "label: old\n")
	r.repo.RunOutput = "codex 0.48.1"
	ch := r.proposed(t, envConfigProposal)

	d, reason := r.fixer.Apply("b1", "apply", "yes, apply it")

	if d != "authorised" || reason != "" {
		t.Fatalf("apply %s %q", d, reason)
	}
	res := resolved(t, ch)
	if res.Action != "retry" || res.By != "maintainer" || res.Citation != "fix-b1" {
		t.Errorf("resolution %+v", res)
	}
	dir := filepath.Join(r.store.dir, "fix-b1")
	if got := r.calls("Repo.Run "); !reflect.DeepEqual(got, []string{dir + ` "codex --version" 1h0m0s`, dir + ` "codex login status" 1h0m0s`}) {
		t.Errorf("runs %q", got)
	}
	for _, name := range []string{"command-1.log", "command-2.log"} {
		if got := readText(t, filepath.Join(dir, name)); got != "codex 0.48.1" {
			t.Errorf("%s = %q", name, got)
		}
	}
	if got := readText(t, filepath.Join(dir, "config.bak")); got != "label: old\n" {
		t.Errorf("config.bak = %q", got)
	}
	if got := readText(t, path); got != "providers:\n  codex:\n    flags: \"--bar\"\n" {
		t.Errorf("config = %q", got)
	}
	if r.reloads != 1 {
		t.Errorf("reloads %d", r.reloads)
	}
	applied := r.events("fix-applied")
	if len(applied) != 1 || applied[0].Phase != "2" || applied[0].Step != "land" || !reflect.DeepEqual(applied[0].Fields, map[string]string{"id": "b1", "commands": "codex --version\ncodex login status", "config": ".r-loop/config.yaml"}) {
		t.Errorf("fix-applied %+v", applied)
	}
	settled := r.events("blocker-resolved")
	if len(settled) != 1 || !reflect.DeepEqual(settled[0].Fields, map[string]string{"id": "b1", "action": "retry", "by": "maintainer", "source": "land", "step": "phase-2/land", "fix": "fix-b1"}) {
		t.Errorf("blocker-resolved %+v", settled)
	}
	if len(r.events("fix-failed")) != 0 {
		t.Errorf("fix-failed %+v", r.events("fix-failed"))
	}
	if fx, _ := r.fixer.Fix("b1"); fx.State != "applied" {
		t.Errorf("state %q", fx.State)
	}
	st, _ := r.store.Load("run-1")
	if got := fixState(st.Events, "b1"); got != "applied" {
		t.Errorf("recorded state %q", got)
	}
}

func TestApplyCommitsAProjectConfigItLeftDirtySoTheLandFindsTheTreeClean(t *testing.T) {
	r := newFixerRig(t)
	r.projectConfig(t, "label: old\n")
	ch := r.proposed(t, configProposal)
	r.repo.DirtyFiles = []string{".r-loop/config.yaml"}

	d, reason := r.fixer.Apply("b1", "apply", "yes, apply it")

	if d != "authorised" || reason != "" {
		t.Fatalf("apply %s %q", d, reason)
	}
	resolved(t, ch)
	if got := r.calls("Repo.Commit "); !reflect.DeepEqual(got, []string{`"chore(r-loop): apply fix-b1 to .r-loop/config.yaml"`}) {
		t.Errorf("commits %q", got)
	}
	if !reflect.DeepEqual(r.repo.CommitPaths, [][]string{{".r-loop/config.yaml"}}) {
		t.Errorf("committed paths %v", r.repo.CommitPaths)
	}
}

func TestApplyLeavesAnUntrackedOrIgnoredProjectConfigUncommitted(t *testing.T) {
	r := newFixerRig(t)
	r.projectConfig(t, "label: old\n")
	ch := r.proposed(t, configProposal)

	if d, reason := r.fixer.Apply("b1", "apply", "yes, apply it"); d != "authorised" {
		t.Fatalf("apply %s %q", d, reason)
	}
	resolved(t, ch)
	if got := r.calls("Repo.Commit "); len(got) != 0 {
		t.Errorf("commits %q", got)
	}
}

func TestApplyStopsAtTheFirstFailingCommandAndLeavesTheBlockerOpen(t *testing.T) {
	r := newFixerRig(t)
	path := r.projectConfig(t, "label: old\n")
	r.repo.RunExit, r.repo.RunOutput = 3, "error: not logged in"
	ch := r.proposed(t, envConfigProposal)

	d, reason := r.fixer.Apply("b1", "apply", "yes")

	if d != "refused" || reason != "fix b1 failed: command 1 exited 3" {
		t.Errorf("apply %s %q", d, reason)
	}
	if got := r.calls("Repo.Run "); len(got) != 1 {
		t.Errorf("runs %q", got)
	}
	if got := readText(t, filepath.Join(r.store.dir, "fix-b1", "command-1.log")); got != "error: not logged in" {
		t.Errorf("log %q", got)
	}
	if got := readText(t, path); got != "label: old\n" {
		t.Errorf("config = %q", got)
	}
	if got := r.failedWith(t); got != "command 1 exited 3" {
		t.Errorf("reason %q", got)
	}
	if r.reloads != 0 {
		t.Errorf("reloads %d", r.reloads)
	}
	waitFor(t, func() bool { return len(r.posted()) == 2 })
	if got := r.posted()[1]; got != "fix b1 failed: command 1 exited 3; blocker b1 stays open: resolve it with resolve_blocker" {
		t.Errorf("post %q", got)
	}
	stillHeld(t, ch)
	if _, ok := r.loop.OpenBlocker("b1"); !ok {
		t.Error("the blocker is not open")
	}
}

func TestApplyFailsWhenACommandChangedTheTree(t *testing.T) {
	r := newFixerRig(t)
	path := r.projectConfig(t, "label: old\n")
	ch := r.proposed(t, envConfigProposal)
	r.repo.setChanges("go.sum")

	d, reason := r.fixer.Apply("b1", "apply", "yes")

	want := "the fix changed " + r.repo.RootDir + ": go.sum"
	if d != "refused" || reason != "fix b1 failed: "+want {
		t.Errorf("apply %s %q", d, reason)
	}
	if got := r.failedWith(t); got != want {
		t.Errorf("reason %q", got)
	}
	if got := readText(t, path); got != "label: old\n" {
		t.Errorf("config = %q", got)
	}
	stillHeld(t, ch)
}

func TestAFailedReloadRestoresTheConfigAndFailsTheFix(t *testing.T) {
	r := newFixerRig(t)
	path := r.projectConfig(t, "label: old\n")
	r.badLoad = errors.New("steps.implement.provider: unknown provider codez")
	ch := r.proposed(t, configProposal)

	d, reason := r.fixer.Apply("b1", "apply", "yes")

	want := "reload: steps.implement.provider: unknown provider codez; .r-loop/config.yaml restored"
	if d != "refused" || reason != "fix b1 failed: "+want {
		t.Errorf("apply %s %q", d, reason)
	}
	if got := readText(t, path); got != "label: old\n" {
		t.Errorf("config = %q", got)
	}
	if got := r.failedWith(t); got != want {
		t.Errorf("reason %q", got)
	}
	stillHeld(t, ch)
}

func TestAFailedReloadRemovesAUserConfigThatDidNotExist(t *testing.T) {
	r := newFixerRig(t)
	r.badLoad = errors.New("bad")
	r.proposed(t, `{"kind":"config","cause":"x","config":{"file":"~/.config/r-loop/config.yaml","content":"label: x\n"}}`)

	if d, _ := r.fixer.Apply("b1", "apply", "yes"); d != "refused" {
		t.Fatalf("apply %s", d)
	}

	if _, err := os.Stat(filepath.Join(r.fixer.Home, ".config", "r-loop", "config.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the new user config was kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.store.dir, "fix-b1", "config.bak")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a backup of nothing was written: %v", err)
	}
}

func TestRejectRecordsItAndLeavesTheBlockerOpen(t *testing.T) {
	r := newFixerRig(t)
	ch := r.proposed(t, configProposal)

	d, reason := r.fixer.Apply("b1", "reject", "")

	if d != "authorised" || reason != "blocker b1 stays open: resolve it with resolve_blocker" {
		t.Errorf("reject %s %q", d, reason)
	}
	rejected := r.events("fix-rejected")
	if len(rejected) != 1 || rejected[0].Phase != "2" || !reflect.DeepEqual(rejected[0].Fields, map[string]string{"id": "b1"}) {
		t.Errorf("fix-rejected %+v", rejected)
	}
	if got := r.calls("Repo.Run "); len(got) != 0 {
		t.Errorf("runs %q", got)
	}
	stillHeld(t, ch)
	if d, reason := r.fixer.Apply("b1", "apply", "yes"); d != "refused" || reason != "no proposed fix for b1: rejected" {
		t.Errorf("apply after reject %s %q", d, reason)
	}
}

func TestApplyRefusals(t *testing.T) {
	r := newFixerRig(t)
	if d, reason := r.fixer.Apply("b1", "apply", "yes"); d != "refused" || reason != "no proposed fix for b1: none" {
		t.Errorf("no fix: %s %q", d, reason)
	}
	r.proposed(t, `{"kind":"r-loop","cause":"blockers.go:12 drops the excerpt"}`)

	for _, tc := range []struct{ decision, said, reason string }{
		{"apply", " ", "apply needs maintainer_said: show the proposal to the maintainer, then call again with their reply, quoted"},
		{"apply", "yes", "r-loop does not apply a r-loop fix: show it to the maintainer, then resolve the blocker with its other actions"},
		{"maybe", "yes", `decision "maybe" is not apply or reject`},
	} {
		if d, reason := r.fixer.Apply("b1", tc.decision, tc.said); d != "refused" || reason != tc.reason {
			t.Errorf("%s %q: %s %q", tc.decision, tc.said, d, reason)
		}
	}
	if d, reason := r.resolve("b1", "block"); d != "authorised" {
		t.Fatalf("block %s %q", d, reason)
	}
	if d, reason := r.fixer.Apply("b1", "reject", ""); d != "refused" || reason != "blocker b1 is not open" {
		t.Errorf("closed blocker: %s %q", d, reason)
	}
}
