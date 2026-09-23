package core_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
	"r-loop/internal/gitrepo"
)

func TestLandRefusesADirtyPrimaryTreeAndLeavesTheFileAlone(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	writeFile(t, filepath.Join(e.root, "notes.txt"), "mine\n")
	writeFile(t, filepath.Join(e.root, "a.txt"), "edited\n")
	_, err := e.gate().Land(context.Background(), phaseOne("true"))
	if !errors.Is(err, core.ErrDirtyTree) || !strings.Contains(err.Error(), "notes.txt") || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("Land = %v", err)
	}
	if e.head() != head || readFile(t, filepath.Join(e.root, "notes.txt")) != "mine\n" || readFile(t, filepath.Join(e.root, "a.txt")) != "edited\n" {
		t.Fatal("primary tree changed")
	}
	assertNoMergeHead(t, e.root)
	if len(e.plan.ticks) != 0 || len(e.store.landings()) != 0 {
		t.Fatal("land advanced")
	}
}

func TestLandRefusesWhenThePrimaryTreeLeftTheStartBranch(t *testing.T) {
	e := newLandEnv(t)
	e.store.branch = "main"
	e.phaseWork(1, "feature.txt", "new\n")
	gitCmd(t, e.root, "checkout", "-q", "-b", "other")
	head := e.head()
	_, err := e.gate().Land(context.Background(), phaseOne("true"))
	if !errors.Is(err, core.ErrLanding) || !strings.Contains(err.Error(), "other") || !strings.Contains(err.Error(), "main") {
		t.Fatalf("Land = %v", err)
	}
	if e.head() != head {
		t.Fatal("HEAD changed")
	}
	assertNoMergeHead(t, e.root)
}

func TestLandRechecksTheStartBranchAfterGateDiscovery(t *testing.T) {
	e := newLandEnv(t)
	e.store.branch = "main"
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	g := e.gate()
	g.Suite = suiteFunc(func(context.Context, core.Phase) (string, error) {
		gitCmd(t, e.root, "checkout", "-q", "-b", "other")
		return "true", nil
	})
	_, err := g.Land(context.Background(), phaseOne(""))
	if !errors.Is(err, core.ErrLanding) || !strings.Contains(err.Error(), "other") || !strings.Contains(err.Error(), "main") {
		t.Fatalf("Land = %v", err)
	}
	if e.head() != head {
		t.Fatal("HEAD changed")
	}
	assertNoMergeHead(t, e.root)
}

func TestLandRefusesAnUnfinishedMergeInThePrimaryTree(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	gitCmd(t, e.root, "checkout", "-q", "-b", "side")
	writeFile(t, filepath.Join(e.root, "side.txt"), "side\n")
	gitCmd(t, e.root, "add", "side.txt")
	gitCmd(t, e.root, "commit", "-q", "-m", "side")
	side := e.head()
	gitCmd(t, e.root, "checkout", "-q", "main")
	gitCmd(t, e.root, "merge", "--no-ff", "--no-commit", "side")
	_, err := e.gate().Land(context.Background(), phaseOne("true"))
	if !errors.Is(err, core.ErrUnfinishedMerge) || !strings.Contains(err.Error(), "MERGE_HEAD") {
		t.Fatalf("Land = %v", err)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(e.root, ".git/MERGE_HEAD"))); got != side {
		t.Fatalf("MERGE_HEAD = %s", got)
	}
}

func TestLandRefusesAnEditMadeWhileTheGateRanAndKeepsIt(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	_, err := e.gate().Land(context.Background(), phaseOne("printf mine > notes.txt"))
	if !errors.Is(err, core.ErrDirtyTree) || !strings.Contains(err.Error(), "notes.txt") {
		t.Fatalf("Land = %v", err)
	}
	if got := readFile(t, filepath.Join(e.root, "notes.txt")); got != "mine" {
		t.Fatalf("notes = %q", got)
	}
	if e.head() != head || readFile(t, e.todo) != todoText || len(e.plan.ticks) != 0 {
		t.Fatal("land advanced")
	}
	assertNoMergeHead(t, e.root)
}

type lateFileRepo struct{ *gitrepo.Repo }

func (r lateFileRepo) Commit(ctx context.Context, message string, paths ...string) (string, error) {
	if err := os.WriteFile(filepath.Join(r.Root(), "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		return "", err
	}
	return r.Repo.Commit(ctx, message, paths...)
}

func TestLandCommitsOnlyTheMergeAndTheTickNotALateMaintainerFile(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	g := e.gate()
	g.Repo = lateFileRepo{e.repo}
	if _, err := g.Land(context.Background(), phaseOne("true")); err != nil {
		t.Fatal(err)
	}
	if got := gitCmd(t, e.root, "show", "--name-only", "--format=", "HEAD"); strings.Contains(got, "notes.txt") {
		t.Fatalf("commit touches notes: %s", got)
	}
	if got := gitCmd(t, e.root, "status", "--porcelain"); got != "?? notes.txt" {
		t.Fatalf("status = %q", got)
	}
	if got := readFile(t, filepath.Join(e.root, "notes.txt")); got != "mine\n" {
		t.Fatalf("notes = %q", got)
	}
}

type failTickPlan struct {
	*tickPlan
	root string
}

func (p failTickPlan) Tick(string, core.Phase) error {
	os.WriteFile(filepath.Join(p.root, "a.txt"), []byte("mine\n"), 0o644)
	return errors.New("disk full")
}

func TestLandTickFailureKeepsAMaintainerEdit(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	g := e.gate()
	g.Plan = failTickPlan{e.plan, e.root}
	_, err := g.Land(context.Background(), phaseOne("true"))
	if err == nil || !strings.Contains(err.Error(), "tick:") {
		t.Fatalf("Land = %v", err)
	}
	if got := readFile(t, filepath.Join(e.root, "a.txt")); got != "mine\n" {
		t.Fatalf("a.txt = %q", got)
	}
	if e.head() != head || readFile(t, e.todo) != todoText {
		t.Fatal("land advanced")
	}
	if _, err := os.Stat(filepath.Join(e.root, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("feature exists: %v", err)
	}
	assertNoMergeHead(t, e.root)
}

type writeThenFailTickPlan struct{ *tickPlan }

func (p writeThenFailTickPlan) Tick(path string, ph core.Phase) error {
	if err := p.tickPlan.Tick(path, ph); err != nil {
		return err
	}
	time.Sleep(1100 * time.Millisecond)
	return errors.New("disk full")
}

func TestLandTickFailureRestoresATodoChangedByThePhase(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	writeFile(t, filepath.Join(e.worktree(1), "docs/demo/todo.md"), todoText+"# phase note\n")
	gitCmd(t, e.worktree(1), "add", "docs/demo/todo.md")
	gitCmd(t, e.worktree(1), "commit", "-q", "-m", "phase todo")
	head := e.head()
	g := e.gate()
	g.Plan = writeThenFailTickPlan{e.plan}
	_, err := g.Land(context.Background(), phaseOne("true"))
	if err == nil || !strings.Contains(err.Error(), "tick:") {
		t.Fatalf("Land = %v", err)
	}
	if status := gitCmd(t, e.root, "status", "--porcelain"); e.head() != head || readFile(t, e.todo) != todoText || status != "" {
		t.Fatalf("primary tree not restored: status %q, land %v", status, err)
	}
	assertNoMergeHead(t, e.root)
}

type failCommitRepo struct{ *gitrepo.Repo }

func (r failCommitRepo) Commit(context.Context, string, ...string) (string, error) {
	os.WriteFile(filepath.Join(r.Root(), "a.txt"), []byte("mine\n"), 0o644)
	return "", errors.New("disk full")
}

func TestLandCommitFailureKeepsAMaintainerEditAndUndoesTheTick(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	g := e.gate()
	g.Repo = failCommitRepo{e.repo}
	_, err := g.Land(context.Background(), phaseOne("true"))
	if err == nil || !strings.Contains(err.Error(), "commit:") {
		t.Fatalf("Land = %v", err)
	}
	if readFile(t, filepath.Join(e.root, "a.txt")) != "mine\n" || readFile(t, e.todo) != todoText || e.head() != head {
		t.Fatal("primary tree changed")
	}
	assertNoMergeHead(t, e.root)
}

func TestLandCommitFailureAfterStagingRestoresATodoChangedByThePhase(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	writeFile(t, filepath.Join(e.worktree(1), "docs/demo/todo.md"), todoText+"# phase note\n")
	gitCmd(t, e.worktree(1), "add", "docs/demo/todo.md")
	gitCmd(t, e.worktree(1), "commit", "-q", "-m", "phase todo")
	writeFile(t, filepath.Join(e.root, ".git/hooks/pre-commit"), "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(e.root, ".git/hooks/pre-commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	head := e.head()
	_, err := e.gate().Land(context.Background(), phaseOne("true"))
	if err == nil || !strings.Contains(err.Error(), "commit:") {
		t.Fatalf("Land = %v", err)
	}
	if status := gitCmd(t, e.root, "status", "--porcelain"); e.head() != head || readFile(t, e.todo) != todoText || status != "" {
		t.Fatalf("primary tree not restored: status %q, land %v", status, err)
	}
	assertNoMergeHead(t, e.root)
}

type lateTouchRepo struct{ *gitrepo.Repo }

func (r lateTouchRepo) CommitTouches(string) ([]string, error) {
	os.WriteFile(filepath.Join(r.Root(), "a.txt"), []byte("mine\n"), 0o644)
	return []string{"docs/demo/todo.md"}, nil
}

func TestLandRefusedLandingCommitIsUndoneKeepingAMaintainerEdit(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	head := e.head()
	g := e.gate()
	g.Repo = lateTouchRepo{e.repo}
	_, err := g.Land(context.Background(), phaseOne("true"))
	if !errors.Is(err, core.ErrLanding) {
		t.Fatalf("Land = %v", err)
	}
	if e.head() != head || readFile(t, filepath.Join(e.root, "a.txt")) != "mine\n" {
		t.Fatal("primary tree changed")
	}
}

type failedAbortRepo struct{ *gitrepo.Repo }

func (r failedAbortRepo) AbortMerge() error {
	r.Repo.AbortMerge()
	return fmt.Errorf("%w: still merging", core.ErrUnfinishedMerge)
}

func TestLandCarriesAFailedMergeAbortInItsError(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "feature.txt", "new\n")
	g := e.gate()
	g.Repo = failedAbortRepo{e.repo}
	_, err := g.Land(context.Background(), phaseOne("exit 1"))
	if !errors.Is(err, core.ErrGate) || !errors.Is(err, core.ErrUnfinishedMerge) {
		t.Fatalf("Land = %v", err)
	}
}

type reportLateFileRepo struct{ *gitrepo.Repo }

func (r reportLateFileRepo) CommitTouches(sha string) ([]string, error) {
	if err := os.WriteFile(filepath.Join(r.Root(), "notes.bin"), []byte{0, 1, 2, 'x', '\n'}, 0o644); err != nil {
		return nil, err
	}
	return r.Repo.CommitTouches(sha)
}

func TestMilestoneReportRefusesADirtyTreeAndKeepsTheMaintainerFile(t *testing.T) {
	e := newLandEnv(t)
	e.phaseWork(1, "one.txt", "one\n")
	e.phaseWork(2, "two.txt", "two\n")
	g, host, _ := e.boundaryGate("ok")
	if _, err := g.Land(context.Background(), phaseOne("true")); err != nil {
		t.Fatal(err)
	}
	g.Repo = reportLateFileRepo{e.repo}
	if _, err := g.Land(context.Background(), phaseTwo("true")); err != nil {
		t.Fatal(err)
	}
	if len(host.opened) != 0 {
		t.Fatalf("report opened: %v", host.opened)
	}
	ev := e.store.events("report-skipped")
	if len(ev) != 1 || !strings.Contains(ev[0].Fields["reason"], "primary tree is not clean") || !strings.Contains(ev[0].Fields["reason"], "notes.bin") {
		t.Fatalf("events = %+v", ev)
	}
	if got := stringMustReadLand(t, filepath.Join(e.root, "notes.bin")); string(got) != string([]byte{0, 1, 2, 'x', '\n'}) {
		t.Fatalf("notes = %v", got)
	}
}

func stringMustReadLand(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
