package gitrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
)

func TestAGitCallRetriesWhileIndexLockIsHeldThenSucceeds(t *testing.T) {
	r, dir := newRepo(t)
	write(t, filepath.Join(dir, "new.txt"), "new\n")
	lock := filepath.Join(dir, ".git", "index.lock")
	write(t, lock, "")
	go func() { time.Sleep(150 * time.Millisecond); os.Remove(lock) }()
	if _, err := r.git("", "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "diff", "--cached", "--name-only"); got != "new.txt" {
		t.Fatalf("staged = %q", got)
	}
}

func TestAGitCallGivesUpOnAHeldIndexLockAfterBoundedRetries(t *testing.T) {
	r, dir := newRepo(t)
	write(t, filepath.Join(dir, "new.txt"), "new\n")
	write(t, filepath.Join(dir, ".git", "index.lock"), "")
	old := lockBackoff
	lockBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { lockBackoff = old })
	start := time.Now()
	_, err := r.git("", "add", "-A")
	if err == nil || !strings.Contains(err.Error(), "index.lock") || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
}

func TestAMergeRetriesWhenGitOnlySaysUnableToWriteIndex(t *testing.T) {
	r, dir := newRepo(t)
	branchWith(t, dir, "side", "side.txt", "side\n")
	lock := filepath.Join(dir, ".git", "index.lock")
	write(t, lock, "")
	go func() { time.Sleep(150 * time.Millisecond); os.Remove(lock) }()
	if err := r.MergeNoFF(context.Background(), "side"); err != nil {
		t.Fatal(err)
	}
	if merging, err := r.MergeInProgress(); err != nil || !merging {
		t.Fatalf("MergeInProgress = %t, %v", merging, err)
	}
}

func TestAbortMergeThatFailsSaysThePrimaryTreeStillHoldsTheMerge(t *testing.T) {
	r, dir := newRepo(t)
	branchWith(t, dir, "side", "side.txt", "side\n")
	if err := r.MergeNoFF(context.Background(), "side"); err != nil {
		t.Fatal(err)
	}
	old := lockBackoff
	lockBackoff = []time.Duration{time.Millisecond}
	t.Cleanup(func() { lockBackoff = old })
	write(t, filepath.Join(dir, ".git", "index.lock"), "")
	err := r.AbortMerge()
	if !errors.Is(err, core.ErrUnfinishedMerge) || !strings.Contains(err.Error(), "unfinished merge") {
		t.Fatalf("AbortMerge = %v", err)
	}
	if merging, err := r.MergeInProgress(); err != nil || !merging {
		t.Fatalf("MergeInProgress = %t, %v", merging, err)
	}
}

func TestMergeInProgressSeesMergeHead(t *testing.T) {
	r, dir := newRepo(t)
	if merging, err := r.MergeInProgress(); err != nil || merging {
		t.Fatalf("initial = %t, %v", merging, err)
	}
	branchWith(t, dir, "side", "side.txt", "side\n")
	if err := r.MergeNoFF(context.Background(), "side"); err != nil {
		t.Fatal(err)
	}
	if merging, err := r.MergeInProgress(); err != nil || !merging {
		t.Fatalf("merged = %t, %v", merging, err)
	}
	if err := r.AbortMerge(); err != nil {
		t.Fatal(err)
	}
	if merging, err := r.MergeInProgress(); err != nil || merging {
		t.Fatalf("aborted = %t, %v", merging, err)
	}
}

func TestResetKeepUndoesACommitAndKeepsUnrelatedEdits(t *testing.T) {
	r, dir := newRepo(t)
	base := git(t, dir, "rev-parse", "HEAD")
	write(t, filepath.Join(dir, "feature.txt"), "feature\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "feature")
	write(t, filepath.Join(dir, "a.txt"), "mine\n")
	if err := r.ResetKeep(base); err != nil {
		t.Fatal(err)
	}
	if git(t, dir, "rev-parse", "HEAD") != base {
		t.Fatal("HEAD moved incorrectly")
	}
	if _, err := os.Stat(filepath.Join(dir, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("feature exists: %v", err)
	}
	if got := stringMustRead(t, filepath.Join(dir, "a.txt")); string(got) != "mine\n" {
		t.Fatalf("a.txt = %q", got)
	}
}

func TestResetKeepRefusesToOverwriteALocalEdit(t *testing.T) {
	r, dir := newRepo(t)
	base := git(t, dir, "rev-parse", "HEAD")
	write(t, filepath.Join(dir, "feature.txt"), "feature\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "feature")
	head := git(t, dir, "rev-parse", "HEAD")
	write(t, filepath.Join(dir, "feature.txt"), "mine\n")
	if err := r.ResetKeep(base); err == nil {
		t.Fatal("ResetKeep overwrote local edit")
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s", got)
	}
	if got := stringMustRead(t, filepath.Join(dir, "feature.txt")); string(got) != "mine\n" {
		t.Fatalf("feature = %q", got)
	}
}

func TestCommitWithPathsStagesOnlyThoseOnTopOfTheMerge(t *testing.T) {
	r, dir := newRepo(t)
	write(t, filepath.Join(dir, "todo.md"), "before\n")
	git(t, dir, "add", "todo.md")
	git(t, dir, "commit", "-q", "-m", "todo")
	branchWith(t, dir, "side", "feature.txt", "feature\n")
	if err := r.MergeNoFF(context.Background(), "side"); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "todo.md"), "after\n")
	write(t, filepath.Join(dir, "notes.txt"), "mine\n")
	sha, err := r.Commit(context.Background(), "land", "todo.md")
	if err != nil {
		t.Fatal(err)
	}
	touched, err := r.CommitTouches(sha)
	if err != nil || !reflect.DeepEqual(touched, []string{"feature.txt", "todo.md"}) {
		t.Fatalf("touched = %v, %v", touched, err)
	}
	if got := stringMustRead(t, filepath.Join(dir, "notes.txt")); string(got) != "mine\n" {
		t.Fatalf("notes = %q", got)
	}
	if got := status(t, dir); got != "?? notes.txt" {
		t.Fatalf("status = %q", got)
	}
}

func TestBranchExists(t *testing.T) {
	r, dir := newRepo(t)
	if yes, err := r.BranchExists("r-loop/phase-1"); err != nil || yes {
		t.Fatalf("before = %t, %v", yes, err)
	}
	git(t, dir, "branch", "r-loop/phase-1")
	if yes, err := r.BranchExists("r-loop/phase-1"); err != nil || !yes {
		t.Fatalf("after = %t, %v", yes, err)
	}
}

func TestDeleteBranchForcedDeletesAnUnmergedBranch(t *testing.T) {
	r, dir := newRepo(t)
	branchWith(t, dir, "r-loop/phase-2", "feature.txt", "feature\n")
	if err := r.DeleteBranch("r-loop/phase-2", true); err != nil {
		t.Fatal(err)
	}
	if yes, err := r.BranchExists("r-loop/phase-2"); err != nil || yes {
		t.Fatalf("exists = %t, %v", yes, err)
	}
}

func TestRemoveWorktreeAndDeleteBranchAreNoOpsWhenAlreadyGone(t *testing.T) {
	r, _ := newRepo(t)
	if err := r.RemoveWorktree(".r-loop/wt/phase-9"); err != nil {
		t.Fatal(err)
	}
	if err := r.AddWorktree(".r-loop/wt/phase-2", "r-loop/phase-2", "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := r.RemoveWorktree(".r-loop/wt/phase-2"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := r.DeleteBranch("r-loop/phase-2", true); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRemoveWorktreeRefusesAnUnregisteredDirectoryStillOnDisk(t *testing.T) {
	r, dir := newRepo(t)
	leftover := filepath.Join(dir, ".r-loop/wt/phase-2")
	write(t, filepath.Join(leftover, "wip.txt"), "mine\n")
	if err := r.RemoveWorktree(".r-loop/wt/phase-2"); err == nil || !strings.Contains(err.Error(), ".r-loop/wt/phase-2") {
		t.Fatalf("RemoveWorktree = %v", err)
	}
	if got := stringMustRead(t, filepath.Join(leftover, "wip.txt")); string(got) != "mine\n" {
		t.Fatalf("wip = %q", got)
	}
}

func TestRemoveWorktreePrunesAStaleRegistration(t *testing.T) {
	r, dir := newRepo(t)
	wt := ".r-loop/wt/phase-2"
	branch := "r-loop/phase-2"
	if err := r.AddWorktree(wt, branch, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, wt)); err != nil {
		t.Fatal(err)
	}
	if err := r.RemoveWorktree(wt); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteBranch(branch, true); err != nil {
		t.Fatal(err)
	}
}

func TestACancelledLockRetryDoesNotClaimTheCommandStarted(t *testing.T) {
	r, dir := newRepo(t)
	lock := filepath.Join(dir, ".git", "index.lock")
	write(t, lock, "")
	old := lockBackoff
	lockBackoff = []time.Duration{time.Second, time.Second}
	t.Cleanup(func() { lockBackoff = old })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	var started bool
	_, err := runCtxStarted(ctx, r.root, nil, nil, &started, "add", "-A")
	if !errors.Is(err, context.Canceled) || started {
		t.Fatalf("err = %v, started = %t", err, started)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("lock removed: %v", err)
	}
}

func TestAMergeRetriedAfterAForeignLockClearsRemovesItsOwnLockWhenCancelled(t *testing.T) {
	r, dir, head, pidFile := slowMerge(t)
	lock := filepath.Join(dir, ".git", "index.lock")
	write(t, lock, "")
	old := lockBackoff
	lockBackoff = []time.Duration{300 * time.Millisecond, 300 * time.Millisecond}
	t.Cleanup(func() { lockBackoff = old })
	go func() { time.Sleep(100 * time.Millisecond); os.Remove(lock) }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.MergeNoFF(ctx, "side") }()
	pid := waitPID(t, pidFile)
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("merge = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("merge did not stop")
	}
	waitProcessGone(t, pid)
	assertCleanMerge(t, dir, head)
}

func TestDeleteBranchReportsAFailedLookup(t *testing.T) {
	r, dir := newRepo(t)
	git(t, dir, "branch", "r-loop/phase-2")
	old := gitTimeout
	gitTimeout = time.Nanosecond
	if err := r.DeleteBranch("r-loop/phase-2", true); err == nil {
		t.Fatal("lookup failure lost")
	}
	gitTimeout = old
	t.Cleanup(func() { gitTimeout = old })
	if yes, err := r.BranchExists("r-loop/phase-2"); err != nil || !yes {
		t.Fatalf("exists = %t, %v", yes, err)
	}
}
