package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxAgentName = 32

type ReviewHalf struct {
	Sessions *SessionManager
	Store    Store
}

type reviewRound struct {
	n, rounds       int
	tree, prevTree  string
	prior, verdicts []string
}

func (h ReviewHalf) Run(ctx context.Context, ref StepRef, worker *Session, obs Observer) Outcome {
	sm := h.Sessions
	row := ref.Kind.Row
	rows, required, out := h.present(worker, row.Reviewers)
	if out.State == StepFailed {
		return out
	}
	if len(rows) == 0 {
		return Outcome{State: StepOK, Session: worker}
	}
	args := make([]ProviderArgs, len(rows))
	urls := make([]string, len(rows))
	for i, rv := range rows {
		key := reviewerKey(ref.Key, rv.ID())
		var a ProviderArgs
		var url string
		var err error
		switch ref.Key.Kind {
		case "gatefix", "gate", "milestone":
			a, err = sm.Resolve(rv.Provider, rv.Model, rv.Effort, "", "")
		default:
			a, url, err = sm.askArgs(key, rv.Provider, rv.Model, rv.Effort, filepath.Join(stepDir(worker), fmt.Sprintf("%s-a%d.mcp.json", key.Kind, key.Attempt)))
		}
		if err != nil {
			return sm.fail(worker, "reviewer "+rv.ID()+": "+err.Error())
		}
		if a.Review == "" && rv.Template() == "review" {
			return sm.fail(worker, "reviewer "+rv.ID()+" declares no native reviewer")
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
				rd.prior = append(rd.prior, filepath.Join(stepDir(worker), fmt.Sprintf("%s-findings-%s-r%d.json", ref.Key.Kind, rv.ID(), n)))
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
		var out Outcome
		if reviewers, out = h.open(worker, rows, required, args, urls, reviewers, rd); out.State == StepFailed {
			return out
		}
		worker.Reviewing.Store(true)
		outs := sm.WaitAll(ctx, reviewers)
		worker.Reviewing.Store(false)
		var spent time.Duration
		for _, o := range outs {
			spent = max(spent, o.active)
		}
		findings, out := h.join(worker, reviewers, outs, rd.n)
		if out.State == StepFailed {
			return out
		}
		if out := h.checkTree(worker, tree); out.State == StepFailed {
			return out
		}
		fixed := false
		verdictPath := filepath.Join(stepDir(worker), fmt.Sprintf("%s-verdict-r%d.json", worker.Ref.Key.Kind, rd.n))
		if findings > 0 {
			if fixed, out = h.fix(ctx, worker, reviewers, rd, verdictPath, spent, obs); out.State == StepFailed {
				return out
			}
		}
		if !fixed {
			if err := h.event(worker, "review-clean", map[string]string{"round": strconv.Itoa(rd.n)}); err != nil {
				return sm.fail(worker, "record: "+err.Error())
			}
			return Outcome{State: StepOK, Session: worker}
		}
		for _, r := range reviewers {
			rd.prior = append(rd.prior, r.Ref.Vars["FindingsPath"].(string))
		}
		rd.verdicts = append(rd.verdicts, verdictPath)
		rd.prevTree = tree
	}
	return Outcome{State: StepOK, Session: worker, Warning: fmt.Sprintf("review round limit reached; round %d fixes unreviewed", row.Rounds)}
}

func (h ReviewHalf) fix(ctx context.Context, worker *Session, reviewers []*Session, rd reviewRound, verdictPath string, spent time.Duration, obs Observer) (bool, Outcome) {
	sm := h.Sessions
	key := worker.Ref.Key
	files := make([]FindingsFile, len(reviewers))
	paths := make([]string, len(reviewers))
	for i, r := range reviewers {
		paths[i] = r.Ref.Vars["FindingsPath"].(string)
		files[i] = FindingsFile{Reviewer: r.Reviewer, Path: paths[i]}
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

func (h ReviewHalf) open(worker *Session, rows []Reviewer, required []string, args []ProviderArgs, urls []string, prev []*Session, rd reviewRound) ([]*Session, Outcome) {
	sm := h.Sessions
	for _, p := range prev {
		if err := sm.Host.ClosePane(p.Pane); err != nil {
			return nil, sm.fail(worker, "reviewer "+p.Reviewer+": "+err.Error())
		}
	}
	sessions := make([]*Session, len(rows))
	for i, rv := range rows {
		sessions[i] = h.reviewer(worker, rv, required[i], args[i], urls[i], rd)
		name, err := sm.freeAgent(worker.Ref.Key, "-rv-"+rv.ID(), agentSuffix(rd.n, worker.Ref.Key.Attempt))
		if err != nil {
			return nil, sm.fail(worker, "reviewer "+rv.ID()+": "+err.Error())
		}
		sessions[i].Agent = name
		target, direction := worker.Pane, "right"
		if i > 0 {
			target, direction = sessions[i-1].Pane, "down"
		}
		pane, err := sm.Host.Split(target, direction, worker.Dir, map[string]string{
			"R_LOOP_RUN":      worker.Ref.Key.Run,
			"R_LOOP_PHASE":    worker.Ref.Key.Phase,
			"R_LOOP_STEP":     worker.Ref.Key.Kind,
			"R_LOOP_REVIEWER": rv.ID(),
		})
		if err != nil {
			return nil, sm.fail(worker, "reviewer "+rv.ID()+": "+err.Error())
		}
		sessions[i].Pane = pane
	}
	worker.setReviewers(sessions)
	for i, s := range sessions {
		if err := os.Remove(s.Ref.Vars["FindingsPath"].(string)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, sm.fail(worker, "reviewer "+s.Reviewer+": "+err.Error())
		}
		if err := os.MkdirAll(s.Ref.Vars["ArtifactsDir"].(string), 0o755); err != nil {
			return nil, sm.fail(worker, "reviewer "+s.Reviewer+": "+err.Error())
		}
		if err := h.event(worker, "agent-named", map[string]string{"attempt": strconv.Itoa(worker.Ref.Key.Attempt), "agent": s.Agent, "reviewer": s.Reviewer, "round": strconv.Itoa(rd.n)}); err != nil {
			return nil, sm.fail(worker, "record: "+err.Error())
		}
		if _, err := sm.Host.Start(s.Pane, s.Agent, args[i].Kind, args[i].Args); err != nil {
			return nil, sm.fail(worker, "reviewer "+s.Reviewer+": "+err.Error())
		}
	}
	for i, s := range sessions {
		text, _, err := sm.Prompts.Render(rows[i].Template(), s.Ref.Vars)
		if err == nil {
			err = sm.Host.Prompt(s.Agent, text, false, 0)
		}
		if err != nil {
			return nil, sm.fail(worker, "reviewer "+s.Reviewer+": "+err.Error())
		}
	}
	return sessions, Outcome{}
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
	vars["ReviewCommand"] = strings.ReplaceAll(args.Review, "{output}", shellQuote(filepath.Join(artifacts, "native-review.txt")))
	vars["FindingsPath"] = filepath.Join(dir, fmt.Sprintf("%s-findings-%s-r%d.json", key.Kind, id, rd.n))
	vars["ArtifactsDir"] = artifacts
	vars["RequiredPath"] = required
	vars["RoundTree"] = rd.prevTree
	vars["PriorFindings"] = bullets(rd.prior)
	vars["PriorVerdicts"] = bullets(rd.verdicts)
	s.Ref = StepRef{
		Key:      key,
		Kind:     StepKind{Name: key.Kind, Prompt: rv.Template(), Check: "findings", Row: StepRow{Provider: rv.Provider, Model: rv.Model, Effort: rv.Effort, Timeout: worker.Ref.Kind.Row.ReviewTimeout}},
		Phase:    worker.Ref.Phase,
		Worktree: worker.Ref.Worktree,
		RunDir:   worker.Ref.RunDir,
		AskURL:   url,
		Vars:     vars,
	}
	return s
}

func (h ReviewHalf) join(worker *Session, reviewers []*Session, outs []Outcome, round int) (int, Outcome) {
	total := 0
	failed := Outcome{}
	for i, s := range reviewers {
		n := 0
		if outs[i].State == StepOK {
			f, err := ReadFindings(s.Ref.Vars["FindingsPath"].(string))
			if err != nil {
				outs[i] = h.Sessions.fail(worker, "evidence missing: "+err.Error())
			}
			n = len(f.Findings)
		}
		total += n
		if err := h.event(worker, "review-find", map[string]string{"round": strconv.Itoa(round), "reviewer": s.Reviewer, "state": string(outs[i].State), "findings": strconv.Itoa(n), "command": reviewCommand(s)}); err != nil {
			return 0, h.Sessions.fail(worker, "record: "+err.Error())
		}
		if outs[i].State != StepOK && failed.State == "" {
			failed = Outcome{State: StepFailed, Reason: "reviewer " + s.Reviewer + ": " + outs[i].Reason, Session: worker, Stalled: outs[i].Stalled}
		}
	}
	return total, failed
}

func reviewCommand(s *Session) string {
	if s.Ref.Kind.Prompt == "review" {
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
	return h.Store.Append(worker.Ref.Key.Run, Record{Kind: RecordEvent, At: ev.At, Event: &ev})
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
