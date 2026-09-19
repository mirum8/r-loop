package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		fmt.Fprintln(env.Stderr, "usage: r-loop status [--plain]")
		return 2
	}
	repo, err := gitrepo.Open(env.Dir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "r-loop: %v\n", err)
		return 2
	}
	st := store.New(repo.Root())
	id, err := runToShow(st)
	if err != nil {
		fmt.Fprintf(env.Stderr, "r-loop: %v\n", err)
		return 2
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
	if cur, pid, ok := st.Current(); ok && cur == id && !alive(pid) {
		deadPID = pid
	}
	for _, line := range StatusLines(run, pl, now(), deadPID) {
		fmt.Fprintln(env.Stdout, line)
	}
	return 0
}

func runToShow(st *store.Store) (string, error) {
	if id, _, ok := st.Current(); ok {
		return id, nil
	}
	entries, err := os.ReadDir(filepath.Dir(st.Dir("x")))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	newest := ""
	for _, e := range entries {
		if e.IsDir() && newerRun(e.Name(), newest) {
			newest = e.Name()
		}
	}
	return newest, nil
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
		if q.AnsweredBy == "" {
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
	state := map[int]string{}
	unticked := pl.Unticked()
	listed := recordedRunList(run)
	for _, ph := range pl.Phases {
		state[ph.Number] = string(core.PhaseLanded)
		switch {
		case !slices.Contains(unticked, ph.Number):
		case len(listed) > 0 && !slices.Contains(listed, ph.Number):
			state[ph.Number] = "not in this run"
		default:
			state[ph.Number] = string(core.PhaseUnticked)
		}
	}
	blocked := map[int]string{}
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
		n := ph.Number
		if reason, ok := blocked[n]; ok {
			lines = append(lines, fmt.Sprintf("phase %d %s %s", n, core.PhaseBlocked, reason))
			continue
		}
		lines = append(lines, fmt.Sprintf("phase %d %s", n, state[n]))
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
