package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"r-loop/internal/core"
	"r-loop/internal/store"
)

func TestPreflightRecordsTheStartBranch(t *testing.T) {
	f := newFixture(t)
	f.commit()
	w, err := f.preflight(f.todo, "--plain")
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(f.root).Load(w.Loop.RunID)
	if err != nil || st.Branch != "main" {
		t.Fatalf("branch = %q, %v", st.Branch, err)
	}
}

func TestALeftoverPhaseWorktreeIsRefusedWithExit4(t *testing.T) {
	f := newFixture(t)
	f.commit()
	f.write(".git/info/exclude", ".r-loop/\n")
	git(t, f.root, "worktree", "add", "-q", "-b", "r-loop/phase-2", ".r-loop/wt/phase-2")
	wip := filepath.Join(f.root, ".r-loop/wt/phase-2/wip.txt")
	if err := os.WriteFile(wip, []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.preflight(f.todo, "--plain")
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), ".r-loop/wt/phase-2") || !strings.Contains(err.Error(), "r-loop/phase-2") {
		t.Fatalf("preflight = %v", err)
	}
	if b, err := os.ReadFile(wip); err != nil || string(b) != "wip" {
		t.Fatalf("wip = %q, %v", b, err)
	}
	entries, err := os.ReadDir(filepath.Join(f.root, ".r-loop/runs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("run created before leftover check: %s", entry.Name())
		}
	}
}

func TestALeftoverWorktreeWithoutExcludeIsNamed(t *testing.T) {
	f := newFixture(t)
	f.commit()
	git(t, f.root, "worktree", "add", "-q", "-b", "r-loop/phase-2", ".r-loop/wt/phase-2")
	wip := filepath.Join(f.root, ".r-loop/wt/phase-2/wip.txt")
	if err := os.WriteFile(wip, []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.preflight(f.todo, "--plain")
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "leftover from an earlier run") || !strings.Contains(err.Error(), ".r-loop/wt/phase-2") {
		t.Fatalf("preflight = %v", err)
	}
	if b, err := os.ReadFile(wip); err != nil || string(b) != "mine\n" {
		t.Fatalf("wip = %q, %v", b, err)
	}
}

func TestALeftoverPlainDirectoryHasAnApplicableRemedy(t *testing.T) {
	f := newFixture(t)
	f.commit()
	f.write(".git/info/exclude", ".r-loop/\n")
	f.write(".r-loop/wt/phase-2/wip.txt", "wip")
	_, err := f.preflight(f.todo, "--plain")
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), ".r-loop/wt/phase-2") || !strings.Contains(err.Error(), "plain directory") {
		t.Fatalf("preflight = %v", err)
	}
	if strings.Contains(err.Error(), "git branch -D") {
		t.Fatalf("irrelevant branch remedy: %v", err)
	}
	if strings.Contains(err.Error(), "r-loop resume") {
		t.Fatalf("no run to resume: %v", err)
	}
}

func TestAStaleWorktreeRegistrationNamesPruneBeforeBranchDeletion(t *testing.T) {
	f := newFixture(t)
	f.commit()
	f.write(".git/info/exclude", ".r-loop/\n")
	wt := ".r-loop/wt/phase-2"
	branch := "r-loop/phase-2"
	git(t, f.root, "worktree", "add", "-q", "-b", branch, wt)
	if err := os.RemoveAll(filepath.Join(f.root, wt)); err != nil {
		t.Fatal(err)
	}
	_, err := f.preflight(f.todo, "--plain")
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), wt) || !strings.Contains(err.Error(), branch) {
		t.Fatalf("preflight = %v", err)
	}
	prune, del := strings.Index(err.Error(), "git worktree prune"), strings.Index(err.Error(), "git branch -D "+branch)
	if prune < 0 || del < prune || strings.Contains(err.Error(), "r-loop resume") {
		t.Fatalf("remedy = %v", err)
	}
}

func TestALeftoverSuggestsResumeWhenAnUnfinishedRunExists(t *testing.T) {
	f := newFixture(t)
	f.commit()
	f.write(".git/info/exclude", ".r-loop/\n")
	f.write(".r-loop/wt/phase-2/wip.txt", "wip")
	id, err := store.New(f.root).Create(core.RunMeta{Todo: f.todo})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.New(f.root).Append(id, core.Record{Kind: "run", Run: core.RunRunning}); err != nil {
		t.Fatal(err)
	}
	_, err = f.preflight(f.todo, "--plain")
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "resume run "+id+" with r-loop resume") {
		t.Fatalf("preflight = %v", err)
	}
}

func TestALeftoverPhaseBranchOnAnOlderBaseIsRefusedWithExit4(t *testing.T) {
	f := newFixture(t)
	f.commit()
	git(t, f.root, "branch", "r-loop/phase-2")
	f.write("later.txt", "later\n")
	f.commit()
	_, err := f.preflight(f.todo, "--plain")
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "r-loop/phase-2") {
		t.Fatalf("preflight = %v", err)
	}
}

func TestAnUnfinishedMergeIsNamedInPreflight(t *testing.T) {
	f := newFixture(t)
	f.commit()
	git(t, f.root, "checkout", "-q", "-b", "side")
	f.write("side.txt", "side\n")
	f.commit()
	git(t, f.root, "checkout", "-q", "main")
	git(t, f.root, "merge", "--no-ff", "--no-commit", "side")
	_, err := f.preflight(f.todo, "--plain")
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "unfinished merge") || !strings.Contains(err.Error(), "git merge --abort") || strings.Contains(err.Error(), "primary tree is not clean") {
		t.Fatalf("preflight = %v", err)
	}
}

func TestResumeNamesAnUnfinishedMerge(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	if _, code := f.firstRun(first, "--phases", "1"); code != 1 {
		t.Fatalf("first exit = %d", code)
	}
	git(t, f.root, "checkout", "-q", "-b", "side")
	f.write("side.txt", "side\n")
	f.commit()
	git(t, f.root, "checkout", "-q", "main")
	git(t, f.root, "merge", "--no-ff", "--no-commit", "side")
	_, _, err := f.resume(newSim())
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "unfinished merge") || !strings.Contains(err.Error(), "git merge --abort") || strings.Contains(err.Error(), "primary tree is not clean") {
		t.Fatalf("resume = %v", err)
	}
}

func TestResumeRefusesWhenThePrimaryTreeLeftTheStartBranch(t *testing.T) {
	f := newResumeFixture(t, noReviewConfig)
	first := newSim()
	first.fail["rloop-p1-implement"] = true
	if _, code := f.firstRun(first, "--phases", "1"); code != 1 {
		t.Fatalf("first exit = %d", code)
	}
	git(t, f.root, "checkout", "-q", "-b", "other")
	_, _, err := f.resume(newSim())
	if exitCode(t, err) != 4 || !strings.Contains(err.Error(), "other") || !strings.Contains(err.Error(), "main") {
		t.Fatalf("resume = %v", err)
	}
}
