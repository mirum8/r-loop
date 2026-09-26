package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/gitrepo"
	"r-loop/internal/plan"
	"r-loop/internal/store"
)

func Status(args []string, env Env) int {
	fs := flag.NewFlagSet("r-loop status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Bool("plain", false, "")
	positional, err := parsePositional(fs, args)
	if err != nil || len(positional) > 1 {
		fmt.Fprintln(env.Stderr, "usage: r-loop status [--plain] [<run-id>]")
		return 2
	}
	repo, err := gitrepo.Open(env.Dir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "r-loop: %v\n", err)
		return 2
	}
	st := store.New(repo.Root())
	named := ""
	if len(positional) == 1 {
		named = positional[0]
	}
	id, err := selectRun(st, named, env.Stderr)
	if err != nil {
		return fail(env, err)
	}
	if id == "" {
		fmt.Fprintln(env.Stdout, "no run")
		return 0
	}
	run, err := st.Load(id)
	if err != nil {
		fmt.Fprintf(env.Stderr, "r-loop: load run %s: %v\n", id, err)
		return 2
	}
	pl, err := plan.Reader{}.Read(run.Todo)
	if err != nil {
		fmt.Fprintf(env.Stderr, "r-loop: %v\n", err)
		return 2
	}
	now := time.Now
	if env.Now != nil {
		now = env.Now
	}
	deadPID := 0
	liveID, _, live := st.Live()
	if cur, pid, ok := st.Current(); ok && cur == id && !(live && liveID == id) {
		deadPID = pid
	}
	for _, line := range StatusLines(run, pl, now(), deadPID) {
		fmt.Fprintln(env.Stdout, line)
	}
	return 0
}

var runIDRe = regexp.MustCompile(`^\d{8}-\d{6}(-\d+)?$`)

func selectRun(st *store.Store, id string, stderr io.Writer) (string, error) {
	if id != "" {
		if !runIDRe.MatchString(id) {
			return "", exit(2, "no run %s in .r-loop/runs", id)
		}
		info, err := os.Stat(st.Dir(id))
		if err != nil || !info.IsDir() {
			return "", exit(2, "no run %s in .r-loop/runs", id)
		}
		return id, nil
	}
	if cur, _, ok := st.Live(); ok {
		return cur, nil
	}
	return newestProgressed(st, stderr)
}

func newestProgressed(st *store.Store, stderr io.Writer) (string, error) {
	entries, err := os.ReadDir(filepath.Dir(st.Dir("x")))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		switch {
		case newerRun(a, b):
			return -1
		case newerRun(b, a):
			return 1
		default:
			return 0
		}
	})
	for _, name := range names {
		run, err := st.Load(name)
		if errors.Is(err, store.ErrMeta) {
			fmt.Fprintf(stderr, "r-loop: skipped run %s: %v\n", name, err)
			continue
		}
		if err != nil || progressed(run) {
			return name, nil
		}
	}
	return "", nil
}

func progressed(run core.RunState) bool {
	if run.Status != core.RunCreated || len(run.Steps) > 0 {
		return true
	}
	for _, event := range run.Events {
		if event.Kind != "watchdog-start" && event.Kind != "watchdog-stale-closed" {
			return true
		}
	}
	return false
}

func newerRun(a, b string) bool {
	if b == "" {
		return true
	}
	return runOrder(a) > runOrder(b)
}

func runOrder(id string) string {
	base, n := id, 1
	if len(id) > 15 && id[15] == '-' {
		base = id[:15]
		n, _ = strconv.Atoi(id[16:])
	}
	return fmt.Sprintf("%s-%09d", base, n)
}

func StatusLines(run core.RunState, pl core.Plan, now time.Time, deadPID int) []string {
	head := fmt.Sprintf("run %s %s", run.ID, run.Status)
	dead := deadPID != 0 && run.Status == core.RunRunning
	if dead {
		head += fmt.Sprintf(" (driver pid %d not alive — r-loop resume)", deadPID)
	}
	lines := append([]string{head}, phaseLines(run, pl)...)
	if l := liveLine(run, now); l != "" && !dead {
		lines = append(lines, l)
	}
	for _, q := range run.Questions {
		switch {
		case q.Kind == core.QuestionDialog:
			lines = append(lines, core.DialogLine(q))
		case q.Kind == core.QuestionBlocker:
			lines = append(lines, core.BlockerLine(q))
		case q.AnsweredBy == "":
			lines = append(lines, fmt.Sprintf("question %s %s", q.ID, q.Text))
		}
	}
	for _, w := range run.Warnings {
		lines = append(lines, "warning "+w)
	}
	for _, e := range run.Events {
		if e.Kind == "warning" {
			lines = append(lines, "warning "+e.Fields["reason"])
		}
	}
	return lines
}

func phaseLines(run core.RunState, pl core.Plan) []string {
	pl = core.GroupBacklog(pl, recordedGroups(run))
	state := map[string]string{}
	unticked := pl.Unticked()
	listed := recordedRunList(run)
	for _, ph := range pl.Phases {
		state[ph.ID] = string(core.PhaseLanded)
		switch {
		case !slices.Contains(unticked, ph.ID):
		case len(listed) > 0 && !slices.Contains(listed, ph.ID):
			state[ph.ID] = "not in this run"
		default:
			state[ph.ID] = string(core.PhaseUnticked)
		}
	}
	blocked := map[string]string{}
	for _, e := range run.Events {
		switch e.Kind {
		case "phase-start":
			delete(blocked, e.Phase)
		case "phase-state":
			state[e.Phase] = e.Fields["state"]
		case "phase-blocked":
			blocked[e.Phase] = e.Fields["reason"]
		case "phase-skipped":
			blocked[e.Phase] = "waits on phase " + e.Fields["because"]
		}
	}
	var lines []string
	for _, ph := range pl.Phases {
		n := ph.ID
		if reason, ok := blocked[n]; ok {
			lines = append(lines, fmt.Sprintf("phase %s %s %s", n, core.PhaseBlocked, reason))
			continue
		}
		lines = append(lines, fmt.Sprintf("phase %s %s", n, state[n]))
	}
	return lines
}

func liveLine(run core.RunState, now time.Time) string {
	if run.Status != core.RunRunning || run.LastStep == nil {
		return ""
	}
	key := *run.LastStep
	if s := run.Steps[key]; s == core.StepOK || s == core.StepFailed {
		return ""
	}
	var start time.Time
	provider, workspace := "", ""
	for _, e := range run.Events {
		if e.Kind != "step" || e.Phase != key.Phase || e.Step != key.Kind || e.Fields["attempt"] != strconv.Itoa(key.Attempt) {
			continue
		}
		if start.IsZero() {
			start = e.At
		}
		if e.Fields["provider"] != "" {
			provider = e.Fields["provider"]
		}
		if e.Fields["workspace"] != "" {
			workspace = e.Fields["workspace"]
		}
	}
	elapsed := time.Duration(0)
	if !start.IsZero() {
		elapsed = now.Sub(start).Round(time.Second)
	}
	return fmt.Sprintf("live %s %s %s %s", key.Kind, provider, workspace, elapsed)
}
