package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/gitrepo"
	"r-loop/internal/store"
)

var answerPoll = time.Second

func Answer(args []string, env Env) int {
	if len(args) < 2 {
		fmt.Fprintln(env.Stderr, "usage: r-loop answer <id> <text>")
		return 2
	}
	id, text := args[0], strings.Join(args[1:], " ")
	repo, err := gitrepo.Open(env.Dir)
	if err != nil {
		return fail(env, exit(2, "%v", err))
	}
	st := store.New(repo.Root())
	runID, pid, ok := st.Current()
	if !ok || !alive(pid) {
		return fail(env, exit(2, "no live run to answer"))
	}
	run, err := st.Load(runID)
	if err != nil {
		return fail(env, exit(2, "load run %s: %v", runID, err))
	}
	if !slices.ContainsFunc(run.Questions, func(q core.Question) bool { return q.ID == id && q.AnsweredBy == "" }) {
		return fail(env, exit(2, "question %s is not open in run %s", id, runID))
	}
	if err := writeAnswer(filepath.Join(st.Dir(runID), "answers"), id, text); err != nil {
		return fail(env, exit(2, "%v", err))
	}
	return 0
}

func writeAnswer(dir, id, text string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+id+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), filepath.Join(dir, id)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("question %s already has an answer waiting", id)
		}
		return err
	}
	return nil
}

func (w *Wiring) pollAnswers(ctx context.Context) {
	dir := filepath.Join(w.Store.Dir(w.Loop.RunID), "answers")
	ticker := time.NewTicker(answerPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			w.Loop.Answer(e.Name(), strings.TrimSpace(string(data)), "maintainer")
		}
	}
}
