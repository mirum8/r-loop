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
	args := make([]ProviderArgs, len(row.Reviewers))
	for i, rv := range row.Reviewers {
		a, err := sm.Resolve(rv.Provider, rv.Model, rv.Effort, "", "")
		if err != nil {
			return sm.fail(worker, "reviewer "+rv.Provider+": "+err.Error())
		}
		if a.Review == "" {
			return sm.fail(worker, "reviewer "+rv.Provider+" declares no native reviewer")
		}
		args[i] = a
	}
	var reviewers []*Session
	rd := reviewRound{rounds: row.Rounds}
	for rd.n = 1; rd.n <= row.Rounds; rd.n++ {
		tree, err := sm.Repo.Snapshot(worker.Dir)
		if err != nil {
			return sm.fail(worker, "snapshot: "+err.Error())
		}
		rd.tree = tree
		if err := h.event(worker, "review-round", map[string]string{"round": strconv.Itoa(rd.n), "tree": tree}); err != nil {
			return sm.fail(worker, "record: "+err.Error())
		}
		var out Outcome
		if reviewers, out = h.open(worker, row.Reviewers, args, reviewers, rd); out.State == StepFailed {
			return out
		}
		worker.Reviewing.Store(true)
		outs := sm.WaitAll(ctx, reviewers)
		worker.Reviewing.Store(false)
		findings, out := h.join(worker, reviewers, outs, rd.n)
		if out.State == StepFailed {
			return out
		}
		if out := h.checkTree(worker, tree); out.State == StepFailed {
			return out
		}
		if findings == 0 {
			if err := h.event(worker, "review-clean", map[string]string{"round": strconv.Itoa(rd.n)}); err != nil {
				return sm.fail(worker, "record: "+err.Error())
			}
			return Outcome{State: StepOK, Session: worker}
		}
		for _, r := range reviewers {
			rd.prior = append(rd.prior, r.Ref.Vars["FindingsPath"].(string))
		}
		rd.verdicts = append(rd.verdicts, filepath.Join(stepDir(worker), fmt.Sprintf("%s-verdict-r%d.json", worker.Ref.Key.Kind, rd.n)))
		rd.prevTree = tree
	}
	return Outcome{State: StepOK, Session: worker}
}

func (h ReviewHalf) open(worker *Session, rows []Reviewer, args []ProviderArgs, prev []*Session, rd reviewRound) ([]*Session, Outcome) {
	sm := h.Sessions
	sessions := make([]*Session, len(rows))
	for i, rv := range rows {
		sessions[i] = h.reviewer(worker, rv, args[i], rd)
		if prev != nil {
			sessions[i].Pane = prev[i].Pane
			sm.Host.Interrupt(prev[i].Agent)
			continue
		}
		target, direction := worker.Pane, "right"
		if i > 0 {
			target, direction = sessions[i-1].Pane, "down"
		}
		pane, err := sm.Host.Split(target, direction, worker.Dir)
		if err != nil {
			return nil, sm.fail(worker, "reviewer "+rv.Provider+": "+err.Error())
		}
		sessions[i].Pane = pane
	}
	for i, s := range sessions {
		if err := os.Remove(s.Ref.Vars["FindingsPath"].(string)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, sm.fail(worker, "reviewer "+s.Reviewer+": "+err.Error())
		}
		if _, err := sm.Host.Start(s.Pane, s.Agent, args[i].Kind, args[i].Args); err != nil {
			return nil, sm.fail(worker, "reviewer "+s.Reviewer+": "+err.Error())
		}
	}
	for _, s := range sessions {
		text, _, err := sm.Prompts.Render("review", s.Ref.Vars)
		if err == nil {
			err = sm.Host.Prompt(s.Agent, text, false, 0)
		}
		if err != nil {
			return nil, sm.fail(worker, "reviewer "+s.Reviewer+": "+err.Error())
		}
	}
	return sessions, Outcome{}
}

func (h ReviewHalf) reviewer(worker *Session, rv Reviewer, args ProviderArgs, rd reviewRound) *Session {
	key := worker.Ref.Key
	dir := stepDir(worker)
	base := fmt.Sprintf("%s-rv-%s-r%d", key.Kind, rv.Provider, rd.n)
	vars := make(map[string]any, len(worker.Ref.Vars)+9)
	for k, v := range worker.Ref.Vars {
		vars[k] = v
	}
	s := &Session{
		Dir:       worker.Dir,
		StartSHA:  worker.StartSHA,
		StartTree: rd.tree,
		Workspace: worker.Workspace,
		Agent:     agentName(fmt.Sprintf("rloop-p%d-%s-rv-%s", key.Phase, key.Kind, rv.Provider), agentSuffix(rd.n, key.Attempt)),
		Sentinel:  filepath.Join(dir, fmt.Sprintf("%s-a%d.sentinel", base, key.Attempt)),
		Reviewer:  rv.Provider,
	}
	vars["Sentinel"] = s.Sentinel
	vars["ReviewedKind"] = key.Kind
	vars["Round"] = rd.n
	vars["Rounds"] = rd.rounds
	vars["ReviewCommand"] = args.Review
	vars["FindingsPath"] = filepath.Join(dir, fmt.Sprintf("%s-findings-%s-r%d.json", key.Kind, rv.Provider, rd.n))
	vars["RoundTree"] = rd.prevTree
	vars["PriorFindings"] = bullets(rd.prior)
	vars["PriorVerdicts"] = bullets(rd.verdicts)
	s.Ref = StepRef{
		Key:      key,
		Kind:     StepKind{Name: key.Kind, Prompt: "review", Check: "findings", Row: StepRow{Provider: rv.Provider, Model: rv.Model, Effort: rv.Effort, Timeout: worker.Ref.Kind.Row.ReviewTimeout}},
		Phase:    worker.Ref.Phase,
		Worktree: worker.Ref.Worktree,
		RunDir:   worker.Ref.RunDir,
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
		if err := h.event(worker, "review-find", map[string]string{"round": strconv.Itoa(round), "reviewer": s.Reviewer, "state": string(outs[i].State), "findings": strconv.Itoa(n)}); err != nil {
			return 0, h.Sessions.fail(worker, "record: "+err.Error())
		}
		if outs[i].State != StepOK && failed.State == "" {
			failed = Outcome{State: StepFailed, Reason: "reviewer " + s.Reviewer + ": " + outs[i].Reason, Session: worker, Stalled: outs[i].Stalled}
		}
	}
	return total, failed
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
	return filepath.Join(s.Ref.RunDir, "phase-"+strconv.Itoa(s.Ref.Key.Phase))
}

func agentName(prefix, suffix string) string {
	if room := maxAgentName - len(suffix); len(prefix) > room {
		prefix = strings.TrimRight(prefix[:room], "-")
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
