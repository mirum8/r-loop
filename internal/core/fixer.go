package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	EventProviderVersions = "provider-versions"
	incidentRecords       = 50
)

type Incident struct {
	Question               Question
	Diagnosis, Excerpt     string
	Start, Now             map[string]string
	Records                []Record
	Config                 string
	Root, Worktree, RunDir string
}

func fixDir(runDir, id string) string {
	return filepath.Join(runDir, "fix-"+id)
}

func writeIncident(dir string, in Incident) error {
	records := in.Records[max(0, len(in.Records)-incidentRecords):]
	lines := make([]string, len(records))
	for i, rec := range records {
		data, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("incident: record %d: %w", i, err)
		}
		lines[i] = string(data)
	}
	versions := versionMoves(in.Start, in.Now)
	for i, v := range versions {
		versions[i] = "- " + v
	}
	if len(versions) == 0 {
		versions = []string{"none recorded"}
	}
	worktree := in.Worktree
	if worktree == "" {
		worktree = "none"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Incident %s\n\n", in.Question.ID)
	fmt.Fprintf(&b, "## Blocker\n\n%s\n\n%s\n\n", BlockerLine(in.Question), fenced(in.Question.Text))
	fmt.Fprintf(&b, "## Diagnosis\n\n%s\n\n", orNone(in.Diagnosis))
	fmt.Fprintf(&b, "## Excerpt\n\n%s\n\n", fenced(in.Excerpt))
	fmt.Fprintf(&b, "## Versions\n\nrun start → now\n\n%s\n\n", strings.Join(versions, "\n"))
	fmt.Fprintf(&b, "## Recent records\n\n%s\n\n", fenced(strings.Join(lines, "\n")))
	fmt.Fprintf(&b, "## Config\n\n%s\n\n", fenced(in.Config))
	fmt.Fprintf(&b, "## Paths\n\n- root: %s\n- worktree: %s\n- run: %s\n", in.Root, worktree, in.RunDir)
	fix := fixDir(dir, in.Question.ID)
	if err := os.MkdirAll(fix, 0o755); err != nil {
		return fmt.Errorf("incident: %w", err)
	}
	return writeFileAtomic(filepath.Join(fix, "incident.md"), []byte(b.String()))
}

func fenced(text string) string {
	return "```\n" + strings.TrimRight(text, "\n") + "\n```"
}

func startVersions(store Store, runID string) map[string]string {
	st, _ := store.Load(runID)
	for _, ev := range st.Events {
		if ev.Kind == EventProviderVersions {
			return ev.Fields
		}
	}
	return nil
}

func versionMoves(start, now map[string]string) []string {
	names := slices.Sorted(maps.Keys(now))
	for name := range start {
		if _, ok := now[name]; !ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	out := make([]string, len(names))
	for i, name := range names {
		was, ok := start[name]
		is, live := now[name]
		switch {
		case !live:
			out[i] = name + " " + was
		case ok && was != is:
			out[i] = name + " " + was + " → " + is
		default:
			out[i] = name + " " + is
		}
	}
	return out
}

const (
	EventFixStarted  = "fix-started"
	EventFixProposed = "fix-proposed"
	EventFixFailed   = "fix-failed"
	EventFixApplied  = "fix-applied"
	EventFixRejected = "fix-rejected"

	fixApply  = "apply"
	fixReject = "reject"

	fixKindPrefix = "fix-"
	fixerPrefix   = "rloop-fix-"
	fixStateOpen  = "open"
	fixProposed   = "proposed"
	fixFailed     = "failed"
	fixApplied    = "applied"
	fixRejected   = "rejected"
)

type BlockerFixer interface {
	Start(ctx context.Context, b Blocker, q Question, addendum string) error
	Open(id string) bool
	Withdraw(id string)
}

type Fixer struct {
	Host                    SessionHost
	Prompts                 Prompts
	Store                   Store
	Repo                    Repo
	Resolve                 func(provider, model, effort, askURL, mcpConfigPath, dir string) (ProviderArgs, error)
	Dialogs                 Dialogs
	Face                    Face
	Post                    func(text string)
	CheckConfig             func(file, content string) error
	Reload                  func() error
	Blocker                 func(id string) (Blocker, bool)
	Settle                  func(Resolution) error
	Versions                func() map[string]string
	Provider, Model, Effort string
	RunID, Root, RunDir     string
	Home                    string
	Pane, Label             string
	Timeout, Poll           time.Duration

	mu    sync.Mutex
	open  string
	fixes map[string]*Fix
}

type Fix struct {
	ID                    string
	Blocker               Blocker
	Session               *Session
	Worktree              string
	PrimaryTree, WorkTree string
	configs               map[string]*[]byte
	Proposal              *Proposal
	State                 string
	prompt                string
	args                  ProviderArgs
	stop                  chan struct{}
}

func fixCitation(citation string) bool {
	return isFixKind(citation)
}

func isFixKind(kind string) bool {
	return strings.HasPrefix(kind, fixKindPrefix+"b")
}

func FixerName(id, runID string) string {
	suffix := []byte(strings.ToLower(runID))
	for i, c := range suffix {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			suffix[i] = '-'
		}
	}
	prefix := fixerPrefix + id + "-"
	if room := max(watchdogNameMax-len(prefix), 0); len(suffix) > room {
		suffix = suffix[len(suffix)-room:]
	}
	return prefix + string(suffix)
}

func fixState(events []Event, id string) string {
	state := ""
	for _, ev := range events {
		if ev.Fields["id"] != id || !strings.HasPrefix(ev.Kind, fixKindPrefix) {
			continue
		}
		switch ev.Kind {
		case EventFixStarted:
			state = fixStateOpen
		default:
			state = strings.TrimPrefix(ev.Kind, fixKindPrefix)
		}
	}
	return state
}

func (f *Fixer) Open(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open == id && id != ""
}

func (f *Fixer) Fix(id string) (Fix, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fx, ok := f.fixes[id]
	if !ok {
		return Fix{}, false
	}
	return *fx, true
}

func (f *Fixer) Start(ctx context.Context, b Blocker, q Question, addendum string) error {
	id := q.ID
	if err := f.reserve(id); err != nil {
		return err
	}
	fx, err := f.prepare(b, q, addendum)
	if err != nil {
		f.unreserve(id)
		return err
	}
	if err := f.record(EventFixStarted, b, map[string]string{"id": id, "provider": f.Provider, "model": f.Model}); err != nil {
		f.unreserve(id)
		return err
	}
	err = f.launch(fx)
	f.mu.Lock()
	if f.fixes == nil {
		f.fixes = map[string]*Fix{}
	}
	f.fixes[id] = fx
	f.mu.Unlock()
	if err != nil {
		f.fail(fx, err.Error(), false)
		return fmt.Errorf("fixer for %s did not start: %w", id, err)
	}
	go f.watch(ctx, fx)
	return nil
}

func (f *Fixer) reserve(id string) error {
	st, err := f.Store.Load(f.RunID)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if state := fixState(st.Events, id); state != "" {
		return fmt.Errorf("already fixed: %s%s %s", fixKindPrefix, id, state)
	}
	if f.open != "" {
		return fmt.Errorf("fixer busy on %s", f.open)
	}
	f.open = id
	return nil
}

func (f *Fixer) unreserve(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.open == id {
		f.open = ""
	}
}

func (f *Fixer) prepare(b Blocker, q Question, addendum string) (*Fix, error) {
	id := q.ID
	fx := &Fix{ID: id, Blocker: b, State: fixStateOpen, stop: make(chan struct{})}
	primary, err := f.Repo.Snapshot("")
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", f.Root, err)
	}
	fx.PrimaryTree = primary
	if fx.configs, err = f.readConfigs(); err != nil {
		return nil, err
	}
	wt := filepath.Join(f.Root, ".r-loop", "wt", "phase-"+b.Phase)
	if info, err := os.Stat(wt); err == nil && info.IsDir() {
		tree, err := f.Repo.Snapshot(wt)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", wt, err)
		}
		fx.Worktree, fx.WorkTree = wt, tree
	}
	st, err := f.Store.Load(f.RunID)
	if err != nil {
		return nil, err
	}
	var now map[string]string
	if f.Versions != nil {
		now = f.Versions()
	}
	in := Incident{
		Question: q, Diagnosis: addendum, Excerpt: b.Excerpt,
		Start: startVersions(f.Store, f.RunID), Now: now,
		Records: runRecords(st), Config: f.resolvedConfig(),
		Root: f.Root, Worktree: fx.Worktree, RunDir: f.RunDir,
	}
	if err := writeIncident(f.RunDir, in); err != nil {
		return nil, err
	}
	dir := fixDir(f.RunDir, id)
	text, _, err := f.Prompts.Render("fixer", map[string]any{
		"IncidentPath": filepath.Join(dir, "incident.md"),
		"ProposalPath": filepath.Join(dir, "proposal.json"),
		"Sentinel":     filepath.Join(dir, "fixer.sentinel"),
		"RunDir":       f.RunDir,
		"Root":         f.Root,
		"Addendum":     "",
	})
	if err != nil {
		return nil, fmt.Errorf("render fixer: %w", err)
	}
	args, err := f.Resolve(f.Provider, f.Model, f.Effort, "", "", "")
	if err != nil {
		return nil, fmt.Errorf("watchdog.fixer: %w", err)
	}
	fx.Session = &Session{
		Ref:      StepRef{Key: StepKey{Run: f.RunID, Phase: b.Phase, Kind: fixKindPrefix + id, Attempt: 1}, RunDir: f.RunDir},
		Dir:      f.Root,
		Agent:    FixerName(id, f.RunID),
		Sentinel: filepath.Join(dir, "fixer.sentinel"),
	}
	fx.prompt, fx.args = text, args
	return fx, nil
}

func (f *Fixer) resolvedConfig() string {
	data, err := os.ReadFile(filepath.Join(f.RunDir, "config.resolved.yaml"))
	if err != nil {
		return "config.resolved.yaml: " + err.Error()
	}
	return string(data)
}

func runRecords(st RunState) []Record {
	var recs []Record
	for i := range st.Events {
		recs = append(recs, Record{Kind: RecordEvent, At: st.Events[i].At, Event: &st.Events[i]})
	}
	for i := range st.Questions {
		q := &st.Questions[i]
		recs = append(recs, Record{Kind: RecordQuestion, At: q.AskedAt, Question: q})
	}
	for i := range st.Signals {
		recs = append(recs, Record{Kind: RecordSignal, At: st.Signals[i].At, Signal: &st.Signals[i]})
	}
	for i := range st.Remedies {
		r := &st.Remedies[i]
		recs = append(recs, Record{Kind: RecordRemedy, At: r.ProposedAt, Remedy: r})
	}
	slices.SortStableFunc(recs, func(a, b Record) int { return a.At.Compare(b.At) })
	return recs
}

func (f *Fixer) launch(fx *Fix) error {
	s := fx.Session
	label := "◆ " + labelPrefix(f.Label) + "fixer " + fx.ID
	pane, ws, err := openTab(f.Host, f.Pane, OpenSpec{CWD: f.Root, Label: label})
	if err != nil {
		return err
	}
	s.Pane, s.Workspace = pane, ws
	if _, err := f.Host.Start(pane, s.Agent, fx.args.Kind, fx.args.Args); err != nil {
		return fmt.Errorf("start %s: %w", s.Agent, err)
	}
	if err := f.Host.Prompt(s.Agent, fx.prompt, false, 0); err != nil {
		return fmt.Errorf("prompt %s: %w", s.Agent, err)
	}
	return nil
}

func (f *Fixer) watch(ctx context.Context, fx *Fix) {
	poll := f.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var deadline <-chan time.Time
	if f.Timeout > 0 {
		timer := time.NewTimer(f.Timeout)
		defer timer.Stop()
		deadline = timer.C
	}
	var malformedAt time.Time
	s := fx.Session
	for {
		select {
		case <-fx.stop:
			return
		case <-ctx.Done():
			f.fail(fx, runStopped, true)
			return
		case <-deadline:
			f.fail(fx, "timeout "+f.Timeout.String(), true)
			return
		case now := <-ticker.C:
			sentinel, err := ReadSentinel(s.Sentinel)
			switch {
			case err == nil && sentinel.Outcome == "ok":
				f.propose(fx)
				return
			case err == nil:
				f.fail(fx, "the fixer failed: "+orNone(sentinel.Reason), true)
				return
			case errors.Is(err, ErrSentinelMalformed):
				if malformedAt.IsZero() {
					malformedAt = now
				}
				if now.Sub(malformedAt) >= sentinelGrace {
					f.fail(fx, "sentinel unreadable: "+err.Error(), true)
					return
				}
			}
			state, err := f.Host.State(s.Agent)
			if err == nil && state == AgentGone {
				f.fail(fx, "agent gone", true)
				return
			}
			if f.Dialogs != nil && err == nil {
				switch state {
				case AgentBlocked:
					f.Dialogs.Blocked(s)
				case AgentWorking, AgentIdle, AgentDone:
					f.Dialogs.Unblocked(s)
				}
			}
		}
	}
}

func (f *Fixer) propose(fx *Fix) {
	p, err := ReadProposal(filepath.Join(fixDir(f.RunDir, fx.ID), "proposal.json"))
	if err == nil {
		err = p.Validate(f.CheckConfig)
	}
	if err != nil {
		f.fail(fx, "proposal: "+err.Error(), true)
		return
	}
	if reason := f.treeChanged(fx); reason != "" {
		f.fail(fx, reason, true)
		return
	}
	if !f.settle(fx, fixProposed, func() { fx.Proposal = &p }) {
		return
	}
	f.closePane(fx)
	f.record(EventFixProposed, fx.Blocker, map[string]string{"id": fx.ID, "kind": p.Kind, "cause": p.Cause})
	f.post(fmt.Sprintf("fix %s proposed (%s): %s\n\n%s\n\n%s", fx.ID, p.Kind, p.Cause, p.Render(), p.next()))
}

func (f *Fixer) configPath(file string) string {
	if rest, ok := strings.CutPrefix(file, "~/"); ok {
		return filepath.Join(f.Home, rest)
	}
	return filepath.Join(f.Root, file)
}

func (f *Fixer) readConfigs() (map[string]*[]byte, error) {
	out := map[string]*[]byte{}
	for _, file := range []string{projectConfigFile, userConfigFile} {
		data, err := os.ReadFile(f.configPath(file))
		switch {
		case err == nil:
			out[file] = &data
		case errors.Is(err, os.ErrNotExist):
			out[file] = nil
		default:
			return nil, fmt.Errorf("config %s: %w", file, err)
		}
	}
	return out, nil
}

func (f *Fixer) configChanged(fx *Fix) string {
	now, err := f.readConfigs()
	if err != nil {
		return "config check: " + err.Error()
	}
	for _, file := range []string{projectConfigFile, userConfigFile} {
		was, is := fx.configs[file], now[file]
		if (was == nil) != (is == nil) || (was != nil && !bytes.Equal(*was, *is)) {
			return "the fixer changed " + file
		}
	}
	return ""
}

func (f *Fixer) treeChanged(fx *Fix) string {
	if reason := f.configChanged(fx); reason != "" {
		return reason
	}
	trees := []struct{ dir, from string }{{"", fx.PrimaryTree}}
	if fx.Worktree != "" {
		trees = append(trees, struct{ dir, from string }{fx.Worktree, fx.WorkTree})
	}
	for _, t := range trees {
		now, err := f.Repo.Snapshot(t.dir)
		if err != nil {
			return "tree check: " + err.Error()
		}
		changed, err := f.Repo.TreeDiff(t.from, now)
		if err != nil {
			return "tree check: " + err.Error()
		}
		if len(changed) > 0 {
			where := f.Root
			if t.dir != "" {
				where = t.dir
			}
			return fmt.Sprintf("the fixer changed %s: %s", where, strings.Join(changed, ", "))
		}
	}
	return ""
}

func (f *Fixer) Withdraw(id string) {
	f.mu.Lock()
	fx := f.fixes[id]
	f.mu.Unlock()
	if fx != nil {
		f.fail(fx, byWithdrawn, false)
	}
}

func (f *Fixer) fail(fx *Fix, reason string, post bool) {
	if !f.settle(fx, fixFailed, nil) {
		return
	}
	f.closePane(fx)
	f.record(EventFixFailed, fx.Blocker, map[string]string{"id": fx.ID, "reason": reason})
	if post {
		f.post(fmt.Sprintf("fix %s failed: %s", fx.ID, reason))
	}
}

func (f *Fixer) settle(fx *Fix, state string, then func()) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fx.State != fixStateOpen {
		return false
	}
	fx.State = state
	if f.open == fx.ID {
		f.open = ""
	}
	if then != nil {
		then()
	}
	close(fx.stop)
	return true
}

func (f *Fixer) closePane(fx *Fix) {
	s := fx.Session
	if f.Dialogs != nil {
		f.Dialogs.Unblocked(s)
	}
	s.end()
	var err error
	switch {
	case s.Workspace != "":
		err = f.Host.Close(s.Workspace)
	case s.Pane != "":
		err = f.Host.ClosePane(s.Pane)
	}
	if err != nil {
		f.record("warning", fx.Blocker, map[string]string{"reason": fmt.Sprintf("close fixer %s: %s", fx.ID, err)})
	}
}

func (f *Fixer) record(kind string, b Blocker, fields map[string]string) error {
	ev := Event{At: time.Now(), Kind: kind, Phase: b.Phase, Step: b.Step, Fields: fields}
	err := f.Store.Append(f.RunID, Record{Kind: RecordEvent, At: ev.At, Event: &ev})
	if f.Face != nil {
		f.Face.Emit(ev)
	}
	if err != nil {
		return fmt.Errorf("record %s: %w", kind, err)
	}
	return nil
}

func (f *Fixer) post(text string) {
	if f.Post != nil {
		quietly(func() { f.Post(text) })
	}
}

func (f *Fixer) Apply(id, decision, maintainerSaid string) (string, string) {
	if decision != fixApply && decision != fixReject {
		return decisionRefused, fmt.Sprintf("decision %q is not apply or reject", decision)
	}
	f.mu.Lock()
	fx, state := f.fixes[id], "none"
	if fx != nil {
		state = fx.State
	}
	f.mu.Unlock()
	if state != fixProposed {
		return decisionRefused, fmt.Sprintf("no proposed fix for %s: %s", id, state)
	}
	if f.Blocker == nil {
		return decisionRefused, fmt.Sprintf("blocker %s is not open", id)
	}
	if _, ok := f.Blocker(id); !ok {
		return decisionRefused, fmt.Sprintf("blocker %s is not open", id)
	}
	p := fx.Proposal
	if decision == fixApply {
		if strings.TrimSpace(maintainerSaid) == "" {
			return decisionRefused, "apply needs maintainer_said: show the proposal to the maintainer, then call again with their reply, quoted"
		}
		if p.Kind == FixRLoop || p.Kind == FixProject {
			return decisionRefused, p.next()
		}
	}
	next := fixApplied
	if decision == fixReject {
		next = fixRejected
	}
	if !f.claim(fx, next) {
		return decisionRefused, fmt.Sprintf("no proposed fix for %s: it is being decided", id)
	}
	if decision == fixReject {
		defer f.release(id)
		if err := f.record(EventFixRejected, fx.Blocker, map[string]string{"id": id}); err != nil {
			return decisionRefused, err.Error()
		}
		return decisionAuthorised, fmt.Sprintf("blocker %s stays open: resolve it with resolve_blocker", id)
	}
	return f.apply(fx)
}

func (f *Fixer) claim(fx *Fix, state string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fx.State != fixProposed {
		return false
	}
	fx.State = state
	if f.open == "" {
		f.open = fx.ID
	}
	return true
}

func (f *Fixer) release(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.open == id {
		f.open = ""
	}
}

func (f *Fixer) apply(fx *Fix) (string, string) {
	defer f.release(fx.ID)
	p := fx.Proposal
	file := ""
	if p.Config != nil {
		file = p.Config.File
	}
	if err := f.record(EventFixApplied, fx.Blocker, map[string]string{"id": fx.ID, "commands": strings.Join(p.Commands, "\n"), "config": file}); err != nil {
		return f.applyFailed(fx, err.Error())
	}
	dir := fixDir(f.RunDir, fx.ID)
	for i, c := range p.Commands {
		n := i + 1
		code, out, err := f.Repo.Run(context.Background(), dir, c, f.Timeout)
		if werr := os.WriteFile(filepath.Join(dir, fmt.Sprintf("command-%d.log", n)), []byte(out), 0o644); werr != nil && err == nil {
			err = werr
		}
		if err != nil {
			return f.applyFailed(fx, fmt.Sprintf("command %d: %s", n, err))
		}
		if code != 0 {
			return f.applyFailed(fx, fmt.Sprintf("command %d exited %d", n, code))
		}
	}
	if reason := f.treeChanged(fx); reason != "" {
		return f.applyFailed(fx, strings.Replace(reason, "the fixer changed", "the fix changed", 1))
	}
	if p.Config != nil {
		if err := f.writeConfig(dir, p.Config); err != nil {
			return f.applyFailed(fx, err.Error())
		}
		if err := f.commitConfig(fx.ID, p.Config.File); err != nil {
			return f.applyFailed(fx, err.Error())
		}
	}
	if f.Settle == nil {
		return f.applyFailed(fx, "no blocker to settle")
	}
	if err := f.Settle(Resolution{ID: fx.ID, Action: actionRetry, By: maintainerCitation, Citation: fixKindPrefix + fx.ID}); err != nil {
		return f.applyFailed(fx, err.Error())
	}
	return decisionAuthorised, ""
}

func (f *Fixer) writeConfig(dir string, c *ConfigChange) error {
	path := f.configPath(c.File)
	old, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("config %s: %w", c.File, err)
	}
	if existed {
		if err := os.WriteFile(filepath.Join(dir, "config.bak"), old, 0o644); err != nil {
			return fmt.Errorf("config backup: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("config %s: %w", c.File, err)
	}
	if err := writeFileAtomic(path, []byte(c.Content)); err != nil {
		return fmt.Errorf("config %s: %w", c.File, err)
	}
	if f.Reload == nil {
		return nil
	}
	rerr := f.Reload()
	if rerr == nil {
		return nil
	}
	if existed {
		err = writeFileAtomic(path, old)
	} else {
		err = os.Remove(path)
	}
	if err != nil {
		return fmt.Errorf("reload: %v; restore %s: %v", rerr, c.File, err)
	}
	return fmt.Errorf("reload: %w; %s restored", rerr, c.File)
}

func (f *Fixer) commitConfig(id, file string) error {
	if file != projectConfigFile {
		return nil
	}
	dirty, err := f.Repo.Dirty("")
	if err != nil {
		return fmt.Errorf("commit %s: %w", file, err)
	}
	if !slices.Contains(dirty, file) {
		return nil
	}
	if _, err := f.Repo.Commit(context.Background(), Subject("chore(r-loop): ", "apply "+fixKindPrefix+id+" to "+file), file); err != nil {
		return fmt.Errorf("commit %s: %w", file, err)
	}
	return nil
}

func (f *Fixer) applyFailed(fx *Fix, reason string) (string, string) {
	f.mu.Lock()
	fx.State = fixFailed
	f.mu.Unlock()
	f.record(EventFixFailed, fx.Blocker, map[string]string{"id": fx.ID, "reason": reason})
	f.post(fmt.Sprintf("fix %s failed: %s; blocker %s stays open: resolve it with resolve_blocker", fx.ID, reason, fx.ID))
	return decisionRefused, fmt.Sprintf("fix %s failed: %s", fx.ID, reason)
}

func FixLines(events []Event) []string {
	var ids []string
	type summary struct{ kind, cause, reason string }
	fixes := map[string]*summary{}
	for _, ev := range events {
		id := ev.Fields["id"]
		switch ev.Kind {
		case EventFixStarted:
			if fixes[id] == nil {
				ids = append(ids, id)
				fixes[id] = &summary{}
			}
		case EventFixProposed:
			if s := fixes[id]; s != nil {
				s.kind, s.cause = ev.Fields["kind"], ev.Fields["cause"]
			}
		case EventFixFailed:
			if s := fixes[id]; s != nil {
				s.reason = ev.Fields["reason"]
			}
		}
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		s := fixes[id]
		state := fixState(events, id)
		if state == fixFailed {
			state += " (" + s.reason + ")"
		}
		line := fixKindPrefix + id
		if s.kind != "" {
			line += " " + s.kind + ": " + s.cause
		}
		out[i] = line + " → " + state
	}
	return out
}
