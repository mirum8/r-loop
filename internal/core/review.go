package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	maxAgentName   = 32
	staticReviewer = "static"
)

var staticActions = []string{actionRetry, actionSkip, actionBlock, actionStop}

type ReviewHalf struct {
	Sessions *SessionManager
	Store    Store
	Analyzer Analyzer
	obs      Observer
}

type reviewRound struct {
	n, rounds       int
	tree, prevTree  string
	prior, verdicts []string
}

func (h ReviewHalf) Run(ctx context.Context, ref StepRef, worker *Session, obs Observer) Outcome {
	h.obs = obs
	sm := h.Sessions
	row := ref.Kind.Row
	rows, required, out := h.present(worker, row.Reviewers)
	if out.State == StepFailed {
		return out
	}
	if len(rows) == 0 && !h.static(worker) {
		return Outcome{State: StepOK, Session: worker}
	}
	args := make([]ProviderArgs, len(rows))
	urls := make([]string, len(rows))
	for i, rv := range rows {
		a, url, err := h.args(worker, rv)
		if err != nil {
			return sm.fail(worker, err.Error())
		}
		args[i], urls[i] = a, url
	}
	var reviewers []*Session
	rd := reviewRound{rounds: row.Rounds}
	start := min(max(ref.ReviewFrom, 1), row.Rounds)
	if start > 1 {
		rd.prevTree = ref.PrevRoundTree
		for n := 1; n < start; n++ {
			for _, rv := range rows {
				rd.prior = append(rd.prior, reviewFindingsPath(worker, rv.ID(), n))
			}
			if h.static(worker) {
				path := reviewFindingsPath(worker, staticReviewer, n)
				_, err := os.Stat(path)
				if err == nil {
					rd.prior = append(rd.prior, path)
				} else if !errors.Is(err, fs.ErrNotExist) {
					return sm.fail(worker, "reviewer static: "+err.Error())
				}
			}
			rd.verdicts = append(rd.verdicts, filepath.Join(stepDir(worker), fmt.Sprintf("%s-verdict-r%d.json", ref.Key.Kind, n)))
		}
	}
	for rd.n = start; rd.n <= row.Rounds; rd.n++ {
		tree, err := sm.Repo.Snapshot(worker.Dir)
		if err != nil {
			return sm.fail(worker, "snapshot: "+err.Error())
		}
		rd.tree = tree
		if err := h.event(worker, "review-round", map[string]string{"round": strconv.Itoa(rd.n), "tree": tree, "attempt": strconv.Itoa(ref.Key.Attempt)}); err != nil {
			return sm.fail(worker, "record: "+err.Error())
		}
		obs.Reviewing(worker, rd.n)
		var static *staticRun
		if h.static(worker) {
			static = h.analyze(ctx, worker)
		}
		runs, out := h.open(ctx, worker, rows, required, args, urls, reviewers, rd)
		if out.State == StepFailed {
			if static != nil {
				static.stop()
			}
			return out
		}
		var waiting []*reviewerRun
		var sessions []*Session
		for _, r := range runs {
			if r.fail == nil {
				waiting, sessions = append(waiting, r), append(sessions, r.s)
			}
		}
		worker.Reviewing.Store(true)
		outs := sm.WaitAll(ctx, sessions)
		worker.Reviewing.Store(false)
		for i, r := range waiting {
			r.out = outs[i]
		}
		if static != nil {
			static.wait()
		}
		findings, out := h.join(ctx, worker, runs, rd)
		if out.State == StepFailed {
			return out
		}
		staticFile, staticN, out := h.settleStatic(ctx, worker, static, rd)
		if out.State == StepFailed {
			return out
		}
		findings += staticN
		var spent time.Duration
		var files []FindingsFile
		reviewers = nil
		for _, r := range runs {
			spent = max(spent, r.out.active)
			if !r.skipped {
				reviewers = append(reviewers, r.s)
				files = append(files, FindingsFile{Reviewer: r.s.Reviewer, Path: r.s.Ref.Vars["FindingsPath"].(string)})
			}
		}
		if staticFile != nil {
			files = append(files, *staticFile)
		}
		if out := h.checkTree(worker, tree); out.State == StepFailed {
			return out
		}
		fixed := false
		verdictPath := filepath.Join(stepDir(worker), fmt.Sprintf("%s-verdict-r%d.json", worker.Ref.Key.Kind, rd.n))
		if findings > 0 {
			if fixed, out = h.fix(ctx, worker, files, rd, verdictPath, spent, obs); out.State == StepFailed {
				return out
			}
		}
		if !fixed {
			if err := h.event(worker, "review-clean", map[string]string{"round": strconv.Itoa(rd.n)}); err != nil {
				return sm.fail(worker, "record: "+err.Error())
			}
			return Outcome{State: StepOK, Session: worker}
		}
		for _, f := range files {
			rd.prior = append(rd.prior, f.Path)
		}
		rd.verdicts = append(rd.verdicts, verdictPath)
		rd.prevTree = tree
	}
	return Outcome{State: StepOK, Session: worker, Warning: fmt.Sprintf("review round limit reached; round %d fixes unreviewed", row.Rounds)}
}

func (h ReviewHalf) fix(ctx context.Context, worker *Session, files []FindingsFile, rd reviewRound, verdictPath string, spent time.Duration, obs Observer) (bool, Outcome) {
	sm := h.Sessions
	key := worker.Ref.Key
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	vars := make(map[string]any, len(worker.Ref.Vars)+8)
	for k, v := range worker.Ref.Vars {
		vars[k] = v
	}
	sentinel := filepath.Join(stepDir(worker), fmt.Sprintf("%s-fix-r%d-a%d.sentinel", key.Kind, rd.n, key.Attempt))
	vars["Sentinel"] = sentinel
	vars["ReviewedKind"] = key.Kind
	vars["Round"] = rd.n
	vars["Rounds"] = rd.rounds
	vars["RoundTree"] = rd.tree
	vars["FindingsFiles"] = files
	vars["VerdictPath"] = verdictPath
	if worker.Ref.AskURL != "" {
		vars["AskURL"] = worker.Ref.AskURL
	}
	if err := os.Remove(verdictPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, sm.fail(worker, "fix: "+err.Error())
	}
	if f, ok := obs.(interface{ Fixing(*Session, int) }); ok {
		f.Fixing(worker, rd.n)
	}
	text, _, err := sm.Prompts.Render("fix", vars)
	if err == nil {
		err = sm.Host.Prompt(worker.Agent, text, false, 0)
	}
	if err != nil {
		return false, sm.fail(worker, "fix: "+err.Error())
	}
	own := worker.Sentinel
	worker.Sentinel, worker.fix = sentinel, &fixHalf{verdictPath: verdictPath, roundTree: rd.tree, findings: paths, spent: spent}
	out := sm.waitAll(ctx, []*Session{worker}, obs)[0]
	worker.Sentinel, worker.fix = own, nil
	if out.State != StepOK {
		return false, out
	}
	v, err := ReadVerdict(verdictPath)
	if err != nil {
		return false, sm.fail(worker, "evidence missing: "+err.Error())
	}
	fixed := false
	for _, e := range v.Findings {
		fixed = fixed || e.blocking()
		if err := h.event(worker, "finding", map[string]string{
			"round": strconv.Itoa(rd.n), "reviewer": e.Reviewer, "id": e.ID, "title": e.Title,
			"verdict": e.Verdict, "severity": e.Severity, "fixed": strconv.FormatBool(e.Fixed), "evidence": e.Evidence,
		}); err != nil {
			return false, sm.fail(worker, "record: "+err.Error())
		}
	}
	return fixed, Outcome{}
}

func reviewFindingsPath(worker *Session, id string, round int) string {
	return filepath.Join(stepDir(worker), fmt.Sprintf("%s-findings-%s-r%d.json", worker.Ref.Key.Kind, id, round))
}

func (h ReviewHalf) static(worker *Session) bool {
	return h.Analyzer != nil && worker.Ref.Kind.Check == "diff"
}

type staticRun struct {
	done     chan struct{}
	cancel   context.CancelFunc
	analysis Analysis
	err      error
}

func (h ReviewHalf) analyze(ctx context.Context, worker *Session) *staticRun {
	ctx, cancel := context.WithCancel(ctx)
	run := &staticRun{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(run.done)
		defer func() {
			if v := recover(); v != nil {
				run.err = fmt.Errorf("%w in analyze: %v", errPanic, v)
			}
		}()
		run.analysis, run.err = h.Analyzer.Analyze(ctx, worker.Dir)
	}()
	return run
}

func (r *staticRun) wait() *staticRun {
	<-r.done
	r.cancel()
	return r
}

func (r *staticRun) stop() {
	r.cancel()
	<-r.done
}

func (h ReviewHalf) settleStatic(ctx context.Context, worker *Session, run *staticRun, rd reviewRound) (*FindingsFile, int, Outcome) {
	if run == nil {
		return nil, 0, Outcome{}
	}
	sm := h.Sessions
	for {
		if run.err == nil {
			path := reviewFindingsPath(worker, staticReviewer, rd.n)
			n := len(run.analysis.Findings)
			if err := writeStaticFindings(path, rd.n, run.analysis.Findings); err != nil {
				return nil, 0, sm.fail(worker, "reviewer static: "+err.Error())
			}
			if err := h.foundStatic(worker, rd.n, StepOK, n, run.analysis.Command); err != nil {
				return nil, 0, sm.fail(worker, "record: "+err.Error())
			}
			return &FindingsFile{Reviewer: staticReviewer, Path: path}, n, Outcome{}
		}
		reason := "reviewer static: " + run.err.Error()
		if err := h.foundStatic(worker, rd.n, StepFailed, 0, ""); err != nil {
			return nil, 0, sm.fail(worker, "record: "+err.Error())
		}
		raiser := h.raiser()
		if raiser == nil {
			return nil, 0, Outcome{State: StepFailed, Reason: reason, Session: worker}
		}
		res := raiser.raise(ctx, Blocker{Source: sourceReviewer, Phase: worker.Ref.Key.Phase, Step: reviewerKey(worker.Ref.Key, staticReviewer).Kind, Reason: reason, Actions: staticActions})
		switch res.Action {
		case actionSkip:
			if err := h.event(worker, "reviewer-skipped", map[string]string{"reviewer": staticReviewer, "reason": reason}); err != nil {
				return nil, 0, sm.fail(worker, "record: "+err.Error())
			}
			if err := os.Remove(reviewFindingsPath(worker, staticReviewer, rd.n)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, 0, sm.fail(worker, "reviewer static: "+err.Error())
			}
			return nil, 0, Outcome{}
		case actionRetry:
			run = h.analyze(ctx, worker).wait()
		case actionStop:
			return nil, 0, Outcome{State: StepFailed, Reason: stoppedAt(res.ID, reason), Session: worker, Halted: true}
		default:
			return nil, 0, Outcome{State: StepFailed, Reason: reason, Session: worker, blocked: true}
		}
	}
}

func (h ReviewHalf) foundStatic(worker *Session, round int, state StepState, n int, command string) error {
	return h.event(worker, "review-find", map[string]string{"round": strconv.Itoa(round), "reviewer": staticReviewer, "state": string(state), "findings": strconv.Itoa(n), "command": strings.TrimSpace("analyze " + command)})
}

func writeStaticFindings(path string, round int, fs []Finding) error {
	out := make([]Finding, 0, len(fs))
	for _, f := range fs {
		f.ID = fmt.Sprintf("%s-r%d-%s", staticReviewer, round, f.ID)
		out = append(out, f)
	}
	data, err := json.Marshal(Findings{Reviewer: staticReviewer, Findings: out})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

type reviewerRun struct {
	rv       Reviewer
	required string
	args     ProviderArgs
	url      string
	s        *Session
	out      Outcome
	fail     *reviewerFail
	findings int
	skipped  bool
	tab      bool
}

type slot struct {
	pane, direction string
	ratio           float64
}

type reviewerFail struct {
	reason  string
	stalled bool
	excerpt string
}

type blockerRaiser interface {
	raises() bool
	raise(ctx context.Context, b Blocker) Resolution
}

func (h ReviewHalf) raiser() blockerRaiser {
	if r, ok := h.obs.(blockerRaiser); ok && r.raises() {
		return r
	}
	return nil
}

func (h ReviewHalf) args(worker *Session, rv Reviewer) (ProviderArgs, string, error) {
	sm := h.Sessions
	dir := stepDir(worker)
	if worker.Ref.InPrimary {
		dir = ""
	}
	key := reviewerKey(worker.Ref.Key, rv.ID())
	var a ProviderArgs
	var url string
	var err error
	switch worker.Ref.Key.Kind {
	case "gatefix", "gate", "milestone":
		a, err = sm.Resolve(rv.Provider, rv.Model, rv.Effort, "", "", dir)
	default:
		a, url, err = sm.askArgs(key, rv.Provider, rv.Model, rv.Effort, filepath.Join(stepDir(worker), fmt.Sprintf("%s-a%d.mcp.json", key.Kind, key.Attempt)), dir)
	}
	if err != nil {
		return a, url, fmt.Errorf("reviewer %s: %w", rv.ID(), err)
	}
	if rv.TemplateFor(worker.Ref.Key.Kind) == "review" && a.Review == "" && a.ReviewExec == "" {
		return a, url, fmt.Errorf("reviewer %s declares no native reviewer", rv.ID())
	}
	return a, url, nil
}

func (h ReviewHalf) open(ctx context.Context, worker *Session, rows []Reviewer, required []string, args []ProviderArgs, urls []string, prev []*Session, rd reviewRound) ([]*reviewerRun, Outcome) {
	sm := h.Sessions
	for _, p := range prev {
		if err := sm.Host.ClosePane(p.Pane); err != nil {
			return nil, sm.fail(worker, "reviewer "+p.Reviewer+": "+err.Error())
		}
	}
	raiser := h.raiser()
	runs := make([]*reviewerRun, len(rows))
	failed := func(r *reviewerRun, f *reviewerFail) Outcome {
		if raiser == nil {
			return sm.fail(worker, f.reason)
		}
		r.fail, r.out = f, sm.fail(r.s, f.reason)
		return Outcome{}
	}
	room := h.columnRoom(worker, len(rows))
	var col int
	var last string
	for i, rv := range rows {
		runs[i] = &reviewerRun{rv: rv, required: required[i], args: args[i], url: urls[i], s: h.reviewer(worker, rv, required[i], args[i], urls[i], rd)}
		var at slot
		switch {
		case col >= room:
		case last == "":
			at = slot{pane: worker.Pane, direction: "right"}
		default:
			at = slot{pane: last, direction: "down", ratio: 1 / float64(room-col+1)}
		}
		f := h.place(worker, runs[i], at, rd)
		if f == nil && !runs[i].tab {
			last = runs[i].s.Pane
			col++
		}
		if f != nil {
			if out := failed(runs[i], f); out.State == StepFailed {
				return nil, out
			}
		}
	}
	h.adopt(worker, runs)
	for _, r := range runs {
		if r.fail != nil {
			continue
		}
		f, out := h.launch(worker, r, rd)
		if out.State == StepFailed {
			return nil, out
		}
		if f != nil {
			if out := failed(r, f); out.State == StepFailed {
				return nil, out
			}
		}
	}
	promptAll := func(native bool) Outcome {
		for _, r := range runs {
			if r.fail != nil || execReview(r) != native {
				continue
			}
			if f := h.prompt(r); f != nil {
				if out := failed(r, f); out.State == StepFailed {
					return out
				}
			}
		}
		return Outcome{}
	}
	if out := promptAll(false); out.State == StepFailed {
		return nil, out
	}
	if out := h.nativeReviews(ctx, worker, runs, failed); out.State == StepFailed {
		return nil, out
	}
	if out := promptAll(true); out.State == StepFailed {
		return nil, out
	}
	return runs, Outcome{}
}

func (h ReviewHalf) columnRoom(worker *Session, n int) int {
	info, ok := paneFits(h.Sessions.Host, worker.Pane, "right", 0)
	if !ok {
		return 0
	}
	return min(n, info.Rows/minPaneRows)
}

func (h ReviewHalf) replaceSlot(worker *Session, runs []*reviewerRun, i int) slot {
	if runs[i].tab {
		return slot{}
	}
	at, tallest := slot{pane: worker.Pane, direction: "right"}, 0
	for j, o := range runs {
		if j == i || o == nil || o.skipped || o.tab || o.s.Pane == "" {
			continue
		}
		if info, err := h.Sessions.Host.PaneInfo(o.s.Pane); err == nil && info.Rows > tallest {
			at, tallest = slot{pane: o.s.Pane, direction: "down", ratio: 0.5}, info.Rows
		}
	}
	return at
}

func (h ReviewHalf) place(worker *Session, r *reviewerRun, at slot, rd reviewRound) *reviewerFail {
	sm := h.Sessions
	id := r.rv.ID()
	name, err := sm.freeAgent(worker.Ref.Key, "-rv-"+id, agentSuffix(rd.n, worker.Ref.Key.Attempt))
	if err != nil {
		return &reviewerFail{reason: "reviewer " + id + ": " + err.Error()}
	}
	r.s.Agent = name
	env := map[string]string{
		"R_LOOP_RUN":      worker.Ref.Key.Run,
		"R_LOOP_PHASE":    worker.Ref.Key.Phase,
		"R_LOOP_STEP":     worker.Ref.Key.Kind,
		"R_LOOP_REVIEWER": id,
	}
	var pane string
	r.tab = at.pane == ""
	if !r.tab {
		_, fits := paneFits(sm.Host, at.pane, at.direction, at.ratio)
		r.tab = !fits
	}
	if r.tab {
		pane, err = sm.Host.OpenTab(worker.Workspace, OpenSpec{CWD: worker.Dir, Label: stepLabel(sm.Label, reviewerKey(worker.Ref.Key, id)), Env: env})
	} else {
		pane, err = sm.Host.Split(at.pane, at.direction, worker.Dir, at.ratio, env)
	}
	if err != nil {
		return &reviewerFail{reason: "reviewer " + id + ": " + err.Error()}
	}
	r.s.Pane = pane
	return nil
}

func (h ReviewHalf) adopt(worker *Session, runs []*reviewerRun) {
	var sessions []*Session
	for _, r := range runs {
		if !r.skipped {
			sessions = append(sessions, r.s)
		}
	}
	worker.setReviewers(sessions)
}

func (h ReviewHalf) launch(worker *Session, r *reviewerRun, rd reviewRound) (*reviewerFail, Outcome) {
	sm := h.Sessions
	s := r.s
	fail := func(err error) (*reviewerFail, Outcome) {
		return &reviewerFail{reason: "reviewer " + s.Reviewer + ": " + err.Error()}, Outcome{}
	}
	if err := os.Remove(s.Ref.Vars["FindingsPath"].(string)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail(err)
	}
	if err := os.MkdirAll(s.Ref.Vars["ArtifactsDir"].(string), 0o755); err != nil {
		return fail(err)
	}
	if err := h.event(worker, "agent-named", map[string]string{"attempt": strconv.Itoa(worker.Ref.Key.Attempt), "agent": s.Agent, "reviewer": s.Reviewer, "round": strconv.Itoa(rd.n)}); err != nil {
		return nil, sm.fail(worker, "record: "+err.Error())
	}
	if _, err := sm.Host.Start(s.Pane, s.Agent, r.args.Kind, r.args.Args); err != nil {
		return fail(err)
	}
	return nil, Outcome{}
}

func (h ReviewHalf) prompt(r *reviewerRun) *reviewerFail {
	sm := h.Sessions
	s := r.s
	text, _, err := sm.Prompts.Render(s.Ref.Kind.Prompt, s.Ref.Vars)
	if err == nil {
		err = sm.Host.Prompt(s.Agent, text, false, 0)
	}
	if err != nil {
		return &reviewerFail{reason: "reviewer " + s.Reviewer + ": " + err.Error()}
	}
	s.prompted = sm.now()
	return nil
}

func (h ReviewHalf) join(ctx context.Context, worker *Session, runs []*reviewerRun, rd reviewRound) (int, Outcome) {
	var first *reviewerFail
	for _, r := range runs {
		if r.fail == nil {
			if err := h.judge(worker, r, rd.n); err != nil {
				return 0, h.Sessions.fail(worker, "record: "+err.Error())
			}
		} else if err := h.found(worker, r, 0, rd.n); err != nil {
			return 0, h.Sessions.fail(worker, "record: "+err.Error())
		}
		if r.fail != nil && first == nil {
			first = r.fail
		}
	}
	if first != nil && h.raiser() == nil {
		return 0, Outcome{State: StepFailed, Reason: first.reason, Session: worker, Stalled: first.stalled}
	}
	total := 0
	for i, r := range runs {
		if r.fail != nil {
			if out := h.recover(ctx, worker, runs, i, rd); out.State == StepFailed {
				return 0, out
			}
		}
		if !r.skipped {
			total += r.findings
		}
	}
	h.adopt(worker, runs)
	return total, Outcome{}
}

func (h ReviewHalf) judge(worker *Session, r *reviewerRun, round int) error {
	n := 0
	if r.out.State == StepOK {
		f, err := ReadFindings(r.s.Ref.Vars["FindingsPath"].(string))
		if err != nil {
			r.out = h.Sessions.fail(worker, "evidence missing: "+err.Error())
		}
		n = len(f.Findings)
	}
	r.fail, r.findings = nil, n
	if r.out.State != StepOK {
		r.fail, r.findings = &reviewerFail{reason: "reviewer " + r.s.Reviewer + ": " + r.out.Reason, stalled: r.out.Stalled}, 0
	}
	return h.found(worker, r, n, round)
}

func (h ReviewHalf) found(worker *Session, r *reviewerRun, n, round int) error {
	return h.event(worker, "review-find", map[string]string{"round": strconv.Itoa(round), "reviewer": r.s.Reviewer, "state": string(r.out.State), "findings": strconv.Itoa(n), "command": reviewCommand(r.s)})
}

func (h ReviewHalf) recover(ctx context.Context, worker *Session, runs []*reviewerRun, i int, rd reviewRound) Outcome {
	sm := h.Sessions
	raiser := h.raiser()
	r := runs[i]
	for r.fail != nil {
		f := *r.fail
		actions := reviewerActions
		if !f.stalled {
			actions = slices.DeleteFunc(slices.Clone(actions), func(a string) bool { return a == actionKeys })
		}
		b := Blocker{Source: sourceReviewer, Phase: worker.Ref.Key.Phase, Step: reviewerKey(worker.Ref.Key, r.rv.ID()).Kind, Reason: f.reason, Excerpt: f.excerpt, Actions: actions}
		if b.Excerpt == "" {
			b.Excerpt = h.screen(r.s)
		}
		res := raiser.raise(ctx, b)
		var out Outcome
		switch res.Action {
		case actionSkip:
			if err := h.event(worker, "reviewer-skipped", map[string]string{"reviewer": r.rv.ID(), "reason": f.reason}); err != nil {
				return sm.fail(worker, "record: "+err.Error())
			}
			r.skipped, r.fail = true, nil
			if r.s.Pane != "" {
				if err := sm.Host.ClosePane(r.s.Pane); err != nil {
					return sm.fail(worker, "reviewer "+r.s.Reviewer+": "+err.Error())
				}
			}
			return Outcome{}
		case actionRetry, actionSwitch:
			out = h.reopen(ctx, worker, runs, i, rd, res)
		case actionKeys:
			out = h.await(ctx, worker, r, rd)
		case actionStop:
			return Outcome{State: StepFailed, Reason: stoppedAt(res.ID, f.reason), Session: worker, Halted: true}
		default:
			return Outcome{State: StepFailed, Reason: f.reason, Session: worker, Stalled: f.stalled, blocked: true}
		}
		if out.State == StepFailed {
			return out
		}
	}
	return Outcome{}
}

func (h ReviewHalf) screen(s *Session) string {
	if s.Agent == "" || s.Pane == "" {
		return ""
	}
	raw, err := h.Sessions.Host.Screen(s.Agent)
	if err != nil {
		return ""
	}
	return normaliseScreen(raw)
}

func (h ReviewHalf) reopen(ctx context.Context, worker *Session, runs []*reviewerRun, i int, rd reviewRound, res Resolution) Outcome {
	sm := h.Sessions
	r := runs[i]
	fail := func(reason string) Outcome {
		r.fail, r.out = &reviewerFail{reason: reason}, sm.fail(r.s, reason)
		if err := h.found(worker, r, 0, rd.n); err != nil {
			return sm.fail(worker, "record: "+err.Error())
		}
		return Outcome{}
	}
	if r.s.Pane != "" {
		if err := sm.Host.ClosePane(r.s.Pane); err != nil {
			return fail("reviewer " + r.s.Reviewer + ": " + err.Error())
		}
		r.s.Pane = ""
	}
	rv := r.rv
	if res.Provider != "" {
		rv.Provider, rv.Model, rv.Effort = res.Provider, res.Model, res.Effort
		a, url, err := h.args(worker, rv)
		if err != nil {
			return fail(err.Error())
		}
		r.args, r.url = a, url
	}
	s := h.reviewer(worker, r.rv, r.required, r.args, r.url, rd)
	s.Ref.Kind.Row.Provider, s.Ref.Kind.Row.Model, s.Ref.Kind.Row.Effort = rv.Provider, rv.Model, rv.Effort
	if res.Addendum != "" {
		s.Ref.Vars["Addendum"] = res.Addendum
	}
	if err := os.Remove(s.Sentinel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail("reviewer " + s.Reviewer + ": " + err.Error())
	}
	if err := os.Remove(filepath.Join(s.Ref.Vars["ArtifactsDir"].(string), "native-review.txt")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fail("reviewer " + s.Reviewer + ": " + err.Error())
	}
	at := h.replaceSlot(worker, runs, i)
	r.s, r.fail = s, nil
	if f := h.place(worker, r, at, rd); f != nil {
		return fail(f.reason)
	}
	h.adopt(worker, runs)
	f, out := h.launch(worker, r, rd)
	if out.State == StepFailed {
		return out
	}
	if f == nil {
		out = h.nativeReviews(ctx, worker, []*reviewerRun{r}, func(_ *reviewerRun, pf *reviewerFail) Outcome {
			f = pf
			return Outcome{}
		})
		if out.State == StepFailed {
			return out
		}
	}
	if f == nil {
		f = h.prompt(r)
	}
	if f != nil {
		return fail(f.reason)
	}
	return h.await(ctx, worker, r, rd)
}

func (h ReviewHalf) await(ctx context.Context, worker *Session, r *reviewerRun, rd reviewRound) Outcome {
	worker.Reviewing.Store(true)
	r.out = h.Sessions.WaitAll(ctx, []*Session{r.s})[0]
	worker.Reviewing.Store(false)
	if err := h.judge(worker, r, rd.n); err != nil {
		return h.Sessions.fail(worker, "record: "+err.Error())
	}
	return Outcome{}
}

func reviewerKey(key StepKey, id string) StepKey {
	key.Kind += "-rv-" + id
	return key
}

func (h ReviewHalf) present(worker *Session, rows []Reviewer) ([]Reviewer, []string, Outcome) {
	var kept []Reviewer
	var required []string
	for _, rv := range rows {
		if rv.Requires == "" {
			kept, required = append(kept, rv), append(required, "")
			continue
		}
		path, err := h.find(worker, rv.Requires)
		if err != nil {
			return nil, nil, h.Sessions.fail(worker, "reviewer "+rv.ID()+": "+err.Error())
		}
		if path == "" {
			if err := h.event(worker, "reviewer-skipped", map[string]string{"reviewer": rv.ID(), "reason": "reviewer " + rv.ID() + ": no " + rv.Requires}); err != nil {
				return nil, nil, h.Sessions.fail(worker, "record: "+err.Error())
			}
			continue
		}
		kept, required = append(kept, rv), append(required, path)
	}
	return kept, required, Outcome{}
}

func (h ReviewHalf) find(worker *Session, rel string) (string, error) {
	for _, dir := range []string{worker.Dir, h.Sessions.Repo.Root()} {
		path := filepath.Join(dir, rel)
		_, err := os.Stat(path)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return "", nil
}

func (h ReviewHalf) reviewer(worker *Session, rv Reviewer, required string, args ProviderArgs, url string, rd reviewRound) *Session {
	key := worker.Ref.Key
	dir := stepDir(worker)
	id := rv.ID()
	base := fmt.Sprintf("%s-rv-%s-r%d", key.Kind, id, rd.n)
	artifacts := filepath.Join(dir, fmt.Sprintf("%s-a%d", base, key.Attempt))
	vars := make(map[string]any, len(worker.Ref.Vars)+11)
	for k, v := range worker.Ref.Vars {
		vars[k] = v
	}
	vars["Addendum"] = ""
	s := &Session{
		Dir:       worker.Dir,
		StartSHA:  worker.StartSHA,
		StartTree: rd.tree,
		Workspace: worker.Workspace,
		Sentinel:  filepath.Join(dir, fmt.Sprintf("%s-a%d.sentinel", base, key.Attempt)),
		Reviewer:  id,
		owner:     worker,
	}
	delete(vars, "AskURL")
	if url != "" {
		vars["AskURL"] = url
	}
	vars["Sentinel"] = s.Sentinel
	vars["ReviewedKind"] = key.Kind
	vars["Round"] = rd.n
	vars["Rounds"] = rd.rounds
	vars["ReviewRan"] = false
	vars["ReviewCommand"] = strings.ReplaceAll(args.Review, "{output}", shellQuote(filepath.Join(artifacts, "native-review.txt")))
	if args.ReviewExec != "" {
		vars["ReviewCommand"] = args.ReviewExec
	}
	vars["FindingsPath"] = reviewFindingsPath(worker, id, rd.n)
	vars["ArtifactsDir"] = artifacts
	vars["RequiredPath"] = required
	vars["RoundTree"] = rd.prevTree
	vars["PriorFindings"] = bullets(rd.prior)
	vars["PriorVerdicts"] = bullets(rd.verdicts)
	s.Ref = StepRef{
		Key:      key,
		Kind:     StepKind{Name: key.Kind, Prompt: rv.TemplateFor(key.Kind), Check: "findings", Row: StepRow{Provider: rv.Provider, Model: rv.Model, Effort: rv.Effort, Timeout: worker.Ref.Kind.Row.ReviewTimeout}},
		Phase:    worker.Ref.Phase,
		Worktree: worker.Ref.Worktree,
		RunDir:   worker.Ref.RunDir,
		AskURL:   url,
		Vars:     vars,
	}
	return s
}

func nativeReview(template string) bool {
	return template == "review"
}

func reviewCommand(s *Session) string {
	if nativeReview(s.Ref.Kind.Prompt) {
		return s.Ref.Vars["ReviewCommand"].(string)
	}
	return "prompt " + s.Ref.Kind.Prompt
}

func (h ReviewHalf) checkTree(worker *Session, roundTree string) Outcome {
	sm := h.Sessions
	now, err := sm.Repo.Snapshot(worker.Dir)
	if err != nil {
		return sm.fail(worker, "snapshot: "+err.Error())
	}
	changed, err := sm.Repo.TreeDiff(roundTree, now)
	if err != nil {
		return sm.fail(worker, "tree diff: "+err.Error())
	}
	if len(changed) > 0 {
		return sm.fail(worker, "reviewer modified the tree: "+strings.Join(changed, ", "))
	}
	return Outcome{}
}

func (h ReviewHalf) event(worker *Session, kind string, fields map[string]string) error {
	fields["step"] = worker.Ref.Key.Kind
	sm := h.Sessions
	ev := Event{At: sm.now(), Kind: kind, Phase: worker.Ref.Key.Phase, Step: worker.Ref.Key.Kind, Fields: fields}
	if err := h.Store.Append(worker.Ref.Key.Run, Record{Kind: RecordEvent, At: ev.At, Event: &ev}); err != nil {
		return err
	}
	if s, ok := h.obs.(interface{ Show(Event) }); ok {
		s.Show(ev)
	}
	return nil
}

func stepDir(s *Session) string {
	return filepath.Join(s.Ref.RunDir, "phase-"+s.Ref.Key.Phase)
}

func agentName(prefix, suffix string) string {
	if len(prefix)+len(suffix) > maxAgentName {
		sum := shortHash(prefix)
		room := maxAgentName - len(suffix) - len(sum) - 1
		prefix = strings.TrimRight(prefix[:room], "-") + "-" + sum
	}
	return prefix + suffix
}

func agentSuffix(round, attempt int) string {
	if attempt > 1 {
		return fmt.Sprintf("-r%d-a%d", round, attempt)
	}
	return fmt.Sprintf("-r%d", round)
}

func bullets(paths []string) string {
	lines := make([]string, len(paths))
	for i, p := range paths {
		lines[i] = "- " + p
	}
	return strings.Join(lines, "\n")
}
