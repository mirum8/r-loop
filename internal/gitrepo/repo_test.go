package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"r-loop/internal/core"
)

var _ core.Repo = (*Repo)(nil)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@local",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) (*Repo, string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, filepath.Join(dir, "a.txt"), "one\ntwo\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return r, dir
}

func status(t *testing.T, dir string) string {
	t.Helper()
	return git(t, dir, "status", "--porcelain")
}

func refs(t *testing.T, dir string) string {
	t.Helper()
	return git(t, dir, "for-each-ref", "--format=%(refname) %(objectname)")
}

func TestOpenRootAndHead(t *testing.T) {
	r, dir := newRepo(t)
	sub := filepath.Join(dir, "sub")
	write(t, filepath.Join(sub, "x"), "x\n")

	fromSub, err := Open(sub)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got, _ := filepath.EvalSymlinks(fromSub.Root()); got != want {
		t.Fatalf("Root = %q, want %q", fromSub.Root(), want)
	}
	if b, err := r.HeadBranch(); err != nil || b != "main" {
		t.Fatalf("HeadBranch = %q, %v", b, err)
	}
	sha, err := r.HeadSHA("")
	if err != nil || sha != git(t, dir, "rev-parse", "HEAD") {
		t.Fatalf("HeadSHA = %q, %v", sha, err)
	}

	git(t, dir, "checkout", "-q", "--detach")
	if _, err := r.HeadBranch(); err == nil {
		t.Fatal("HeadBranch on detached HEAD returned no error")
	}
}

func TestOpenOutsideRepoFails(t *testing.T) {
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("Open outside a repository returned no error")
	}
}

func TestCleanListsUntracked(t *testing.T) {
	r, dir := newRepo(t)
	if paths, err := r.Clean(); err != nil || len(paths) != 0 {
		t.Fatalf("Clean on clean tree = %v, %v", paths, err)
	}
	write(t, filepath.Join(dir, "a.txt"), "changed\n")
	write(t, filepath.Join(dir, "new", "b.txt"), "b\n")

	paths, err := r.Clean()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a.txt", "new/b.txt"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("Clean = %v, want %v", paths, want)
	}
}

func TestAddWorktreeCreatesBranchAndReuses(t *testing.T) {
	r, dir := newRepo(t)
	wt := ".r-loop/wt/phase-1"

	if err := r.AddWorktree(wt, "r-loop/phase-1", "main"); err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	abs := filepath.Join(dir, wt)
	if got := git(t, abs, "symbolic-ref", "--short", "HEAD"); got != "r-loop/phase-1" {
		t.Fatalf("worktree branch = %q", got)
	}
	if got, _ := r.HeadSHA(wt); got != git(t, dir, "rev-parse", "main") {
		t.Fatalf("worktree HEAD = %q", got)
	}

	write(t, filepath.Join(abs, "wip.txt"), "wip\n")
	if err := r.AddWorktree(wt, "r-loop/phase-1", "main"); err != nil {
		t.Fatalf("AddWorktree reuse: %v", err)
	}
	if _, err := os.Stat(filepath.Join(abs, "wip.txt")); err != nil {
		t.Fatalf("reuse lost the worktree's files: %v", err)
	}

	if err := r.RemoveWorktree(wt); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Fatalf("worktree dir still present: %v", err)
	}
	if strings.Contains(git(t, dir, "worktree", "list"), "phase-1") {
		t.Fatal("worktree still listed")
	}
}

func TestAddWorktreeUsesExistingBranch(t *testing.T) {
	r, dir := newRepo(t)
	git(t, dir, "branch", "r-loop/phase-2")
	write(t, filepath.Join(dir, "later.txt"), "later\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "later")
	branchSHA := git(t, dir, "rev-parse", "r-loop/phase-2")

	if err := r.AddWorktree(".r-loop/wt/phase-2", "r-loop/phase-2", "main"); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.HeadSHA(".r-loop/wt/phase-2"); got != branchSHA {
		t.Fatalf("existing branch moved: HEAD %q, want %q", got, branchSHA)
	}
}

func TestAddWorktreeOnOtherBranchFails(t *testing.T) {
	r, _ := newRepo(t)
	if err := r.AddWorktree("wt", "one", "main"); err != nil {
		t.Fatal(err)
	}
	if err := r.AddWorktree("wt", "two", "main"); err == nil {
		t.Fatal("worktree on another branch reused without error")
	}
}

func TestCommitAllDirtyAndClean(t *testing.T) {
	r, dir := newRepo(t)
	head := git(t, dir, "rev-parse", "HEAD")

	sha, err := r.CommitAll("", "noop")
	if err != nil || sha != head {
		t.Fatalf("CommitAll on clean tree = %q, %v; want %q", sha, err, head)
	}
	if got := git(t, dir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Fatalf("clean CommitAll made a commit: count %s", got)
	}

	write(t, filepath.Join(dir, "a.txt"), "edited\n")
	write(t, filepath.Join(dir, "new.txt"), "new\n")
	dirty, err := r.Dirty("")
	if err != nil || !reflect.DeepEqual(dirty, []string{"a.txt", "new.txt"}) {
		t.Fatalf("Dirty = %v, %v", dirty, err)
	}

	sha, err = r.CommitAll("", "r-loop: phase 1 implement")
	if err != nil {
		t.Fatal(err)
	}
	if sha == head || sha != git(t, dir, "rev-parse", "HEAD") {
		t.Fatalf("CommitAll sha = %q", sha)
	}
	if got := git(t, dir, "log", "-1", "--format=%an <%ae>|%s"); got != "r-loop <r-loop@local>|r-loop: phase 1 implement" {
		t.Fatalf("commit = %q", got)
	}
	if s := status(t, dir); s != "" {
		t.Fatalf("tree not clean after CommitAll: %q", s)
	}
}

func TestDiffCallsSeeUncommittedAndUntracked(t *testing.T) {
	r, dir := newRepo(t)
	base := git(t, dir, "rev-parse", "HEAD")

	if ok, err := r.DiffNonEmpty("", base); err != nil || ok {
		t.Fatalf("DiffNonEmpty on clean tree = %v, %v", ok, err)
	}

	write(t, filepath.Join(dir, "a.txt"), "one\nTWO\nthree\n")
	write(t, filepath.Join(dir, "u.txt"), "x\ny\nz")

	if ok, err := r.DiffNonEmpty("", base); err != nil || !ok {
		t.Fatalf("DiffNonEmpty = %v, %v", ok, err)
	}
	files, err := r.ChangedFiles("", base)
	if err != nil || !reflect.DeepEqual(files, []string{"a.txt", "u.txt"}) {
		t.Fatalf("ChangedFiles = %v, %v", files, err)
	}
	added, deleted, err := r.DiffStat("", base)
	if err != nil || added != 5 || deleted != 1 {
		t.Fatalf("DiffStat = %d, %d, %v; want 5, 1", added, deleted, err)
	}
}

func TestDiffOnlyUntracked(t *testing.T) {
	r, dir := newRepo(t)
	write(t, filepath.Join(dir, "only.txt"), "1\n2\n")

	if ok, _ := r.DiffNonEmpty("", "HEAD"); !ok {
		t.Fatal("untracked file not seen by DiffNonEmpty")
	}
	if added, deleted, _ := r.DiffStat("", "HEAD"); added != 2 || deleted != 0 {
		t.Fatalf("DiffStat = %d, %d", added, deleted)
	}
}

func TestSnapshotTouchesNothing(t *testing.T) {
	r, dir := newRepo(t)
	write(t, filepath.Join(dir, "a.txt"), "edited\n")
	write(t, filepath.Join(dir, "untracked.txt"), "u\n")
	indexBefore, _ := os.ReadFile(filepath.Join(dir, ".git", "index"))
	statusBefore, refsBefore := status(t, dir), refs(t, dir)

	tree, err := r.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}

	if git(t, dir, "cat-file", "-t", tree) != "tree" {
		t.Fatalf("%s is not a tree", tree)
	}
	if got := git(t, dir, "ls-tree", "--name-only", tree); got != "a.txt\nuntracked.txt" {
		t.Fatalf("snapshot tree = %q", got)
	}
	if s := status(t, dir); s != statusBefore {
		t.Fatalf("status changed:\n%s\nwant\n%s", s, statusBefore)
	}
	if rf := refs(t, dir); rf != refsBefore {
		t.Fatalf("refs changed:\n%s\nwant\n%s", rf, refsBefore)
	}
	if indexAfter, _ := os.ReadFile(filepath.Join(dir, ".git", "index")); string(indexAfter) != string(indexBefore) {
		t.Fatal("real index changed")
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".git", "r-loop-index*"))
	if len(leftovers) != 0 {
		t.Fatalf("throwaway index left behind: %v", leftovers)
	}
}

func TestSnapshotTreeDiffNamesTheEdit(t *testing.T) {
	r, dir := newRepo(t)
	write(t, filepath.Join(dir, "untracked.txt"), "u\n")
	if err := r.AddWorktree("wt", "side", "main"); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(dir, "wt")
	write(t, filepath.Join(wt, "keep.txt"), "k\n")

	first, err := r.Snapshot("wt")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(wt, "sub", "edit.txt"), "e\n")
	second, err := r.Snapshot("wt")
	if err != nil {
		t.Fatal(err)
	}

	paths, err := r.TreeDiff(first, second)
	if err != nil || !reflect.DeepEqual(paths, []string{"sub/edit.txt"}) {
		t.Fatalf("TreeDiff = %v, %v", paths, err)
	}
	if paths, _ := r.TreeDiff(first, first); len(paths) != 0 {
		t.Fatalf("TreeDiff of equal trees = %v", paths)
	}
}

func branchWith(t *testing.T, dir, branch, file, content string) {
	t.Helper()
	git(t, dir, "checkout", "-q", "-b", branch)
	write(t, filepath.Join(dir, file), content)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", branch)
	git(t, dir, "checkout", "-q", "main")
}

func TestMergeNoFFThenCommit(t *testing.T) {
	r, dir := newRepo(t)
	branchWith(t, dir, "r-loop/phase-1", "feature.txt", "f\n")
	head := git(t, dir, "rev-parse", "HEAD")

	if err := r.MergeNoFF(context.Background(), "r-loop/phase-1"); err != nil {
		t.Fatalf("MergeNoFF: %v", err)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatal("MergeNoFF committed")
	}
	write(t, filepath.Join(dir, "todo.md"), "- [x] ticked\n")

	sha, err := r.Commit(context.Background(), "r-loop: land phase 1")
	if err != nil {
		t.Fatal(err)
	}
	if parents := strings.Fields(git(t, dir, "log", "-1", "--format=%P", sha)); len(parents) != 2 || parents[0] != head {
		t.Fatalf("parents = %v", parents)
	}
	touched, err := r.CommitTouches(sha)
	if err != nil || !reflect.DeepEqual(touched, []string{"feature.txt", "todo.md"}) {
		t.Fatalf("CommitTouches = %v, %v", touched, err)
	}
	if s := status(t, dir); s != "" {
		t.Fatalf("tree not clean: %q", s)
	}
}

func TestRevParseResolvesMergeHeadAndABranch(t *testing.T) {
	r, dir := newRepo(t)
	branchWith(t, dir, "r-loop/phase-1", "feature.txt", "feature\n")
	tip := git(t, dir, "rev-parse", "r-loop/phase-1")
	git(t, dir, "merge", "--no-ff", "--no-commit", "r-loop/phase-1")

	for _, ref := range []string{"MERGE_HEAD", "r-loop/phase-1"} {
		got, err := r.RevParse(ref)
		if err != nil || got != tip {
			t.Fatalf("RevParse(%q) = %q, %v; want %q", ref, got, err, tip)
		}
	}
}

func TestLandedCommitFindsTheMergeWithTheRecordedParentsAndTree(t *testing.T) {
	r, dir := newRepo(t)
	branchWith(t, dir, "r-loop/phase-1", "feature.txt", "feature\n")
	base := git(t, dir, "rev-parse", "HEAD")
	tip := git(t, dir, "rev-parse", "r-loop/phase-1")
	git(t, dir, "merge", "--no-ff", "--no-commit", "r-loop/phase-1")
	tree, err := r.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	landing, err := r.Commit(context.Background(), "phase 1: feature")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "later.txt"), "later\n")
	git(t, dir, "add", "later.txt")
	git(t, dir, "commit", "-q", "-m", "later")

	got, err := r.LandedCommit(base, tip, tree)
	if err != nil || got != landing {
		t.Fatalf("LandedCommit = %q, %v; want %q", got, err, landing)
	}
}

func TestLandedCommitIgnoresASameSubjectCommitWithOtherParentsOrTree(t *testing.T) {
	t.Run("single parent", func(t *testing.T) {
		r, dir := newRepo(t)
		branchWith(t, dir, "r-loop/phase-1", "feature.txt", "feature\n")
		base := git(t, dir, "rev-parse", "HEAD")
		tip := git(t, dir, "rev-parse", "r-loop/phase-1")
		write(t, filepath.Join(dir, "feature.txt"), "feature\n")
		tree, err := r.Snapshot("")
		if err != nil {
			t.Fatal(err)
		}
		git(t, dir, "add", "feature.txt")
		git(t, dir, "commit", "-q", "-m", "phase 1: feature")

		got, err := r.LandedCommit(base, tip, tree)
		if err != nil || got != "" {
			t.Fatalf("LandedCommit for single-parent commit = %q, %v; want empty", got, err)
		}
	})

	t.Run("different tree", func(t *testing.T) {
		r, dir := newRepo(t)
		branchWith(t, dir, "r-loop/phase-1", "feature.txt", "feature\n")
		base := git(t, dir, "rev-parse", "HEAD")
		tip := git(t, dir, "rev-parse", "r-loop/phase-1")
		git(t, dir, "merge", "--no-ff", "--no-commit", "r-loop/phase-1")
		tree, err := r.Snapshot("")
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "extra.txt"), "unrecorded\n")
		if _, err := r.Commit(context.Background(), "phase 1: feature"); err != nil {
			t.Fatal(err)
		}

		got, err := r.LandedCommit(base, tip, tree)
		if err != nil || got != "" {
			t.Fatalf("LandedCommit for changed tree = %q, %v; want empty", got, err)
		}
	})
}

func TestIndexTreeKeepsAStagedOnlyChange(t *testing.T) {
	r, dir := newRepo(t)
	headTree := git(t, dir, "rev-parse", "HEAD^{tree}")
	write(t, filepath.Join(dir, "a.txt"), "staged\n")
	git(t, dir, "add", "a.txt")
	git(t, dir, "restore", "--worktree", "a.txt")
	write(t, filepath.Join(dir, "a.txt"), "one\ntwo\n")

	snapshot, err := r.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	index, err := r.IndexTree()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot != headTree || index == snapshot {
		t.Fatalf("Snapshot = %q, IndexTree = %q, HEAD tree = %q", snapshot, index, headTree)
	}
	paths, err := r.TreeDiff(index, snapshot)
	if err != nil || !reflect.DeepEqual(paths, []string{"a.txt"}) {
		t.Fatalf("TreeDiff = %v, %v; want a.txt", paths, err)
	}
	if got := git(t, dir, "diff", "--cached", "--name-only"); got != "a.txt" {
		t.Fatalf("real index changed: %q", got)
	}
}

func TestGitlinkPathsExcludesOrdinaryFiles(t *testing.T) {
	r, dir := newRepo(t)
	source := t.TempDir()
	git(t, source, "init", "-q", "-b", "main")
	write(t, filepath.Join(source, "version.txt"), "one\n")
	git(t, source, "add", "-A")
	git(t, source, "commit", "-q", "-m", "one")
	git(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", "-q", source, "sub")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "add submodule")

	paths, err := r.GitlinkPaths("HEAD^{tree}")
	if err != nil || !reflect.DeepEqual(paths, []string{"sub"}) {
		t.Fatalf("GitlinkPaths = %v, %v; want sub only", paths, err)
	}
}

func TestMergeConflictRestoresTree(t *testing.T) {
	r, dir := newRepo(t)
	branchWith(t, dir, "side", "a.txt", "side\n")
	write(t, filepath.Join(dir, "a.txt"), "main\n")
	git(t, dir, "commit", "-q", "-am", "main edit")
	head := git(t, dir, "rev-parse", "HEAD")

	err := r.MergeNoFF(context.Background(), "side")
	if !errors.Is(err, core.ErrMergeConflict) {
		t.Fatalf("err = %v, want ErrMergeConflict", err)
	}
	if !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("error does not name the conflicting path: %v", err)
	}
	if s := status(t, dir); s != "" {
		t.Fatalf("tree not restored: %q", s)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatal("HEAD moved")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatal("merge still in progress")
	}
}

func TestAbortMergeAfterCleanMerge(t *testing.T) {
	r, dir := newRepo(t)
	branchWith(t, dir, "side", "feature.txt", "f\n")

	if err := r.MergeNoFF(context.Background(), "side"); err != nil {
		t.Fatal(err)
	}
	if err := r.AbortMerge(); err != nil {
		t.Fatalf("AbortMerge: %v", err)
	}
	if s := status(t, dir); s != "" {
		t.Fatalf("tree not restored: %q", s)
	}
	if _, err := os.Stat(filepath.Join(dir, "feature.txt")); !os.IsNotExist(err) {
		t.Fatal("merged file still present")
	}
}

func TestResetHard(t *testing.T) {
	r, dir := newRepo(t)
	base := git(t, dir, "rev-parse", "HEAD")
	write(t, filepath.Join(dir, "a.txt"), "x\n")
	git(t, dir, "commit", "-q", "-am", "x")

	if err := r.ResetHard(base); err != nil {
		t.Fatal(err)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != base {
		t.Fatalf("HEAD = %q, want %q", got, base)
	}
}

func TestRunExitAndOutput(t *testing.T) {
	r, dir := newRepo(t)
	exit, out, err := r.Run(context.Background(), "", "pwd; echo err >&2; exit 3", time.Minute)
	if err != nil || exit != 3 {
		t.Fatalf("Run = %d, %v", exit, err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if !strings.Contains(out, want) || !strings.Contains(out, "err") {
		t.Fatalf("output = %q", out)
	}
}

func TestRunTimesOut(t *testing.T) {
	r, _ := newRepo(t)
	start := time.Now()
	exit, out, err := r.Run(context.Background(), "", "echo started; sleep 30 & sleep 30", 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run returned after %v", elapsed)
	}
	if exit != -1 || !strings.Contains(out, "started") || !strings.HasSuffix(out, "timed out after 200ms") {
		t.Fatalf("Run = %d, %q", exit, out)
	}
}

func waitPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			var pid int
			if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pid); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid was not written to %s", path)
	return 0
}

func waitGroupGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process group %d still exists", pgid)
}

func waitProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still exists", pid)
}

func TestRunThatPassesButLeavesABackgroundChildExitsZero(t *testing.T) {
	r, _ := newRepo(t)
	pidFile := filepath.Join(t.TempDir(), "gate.pid")
	start := time.Now()
	exit, out, err := r.Run(context.Background(), "", "echo $$ > '"+pidFile+"'; sleep 30 & echo ok", time.Minute)
	if err != nil || exit != 0 || !strings.Contains(out, "ok") || time.Since(start) > 5*time.Second {
		t.Fatalf("Run = %d, %q, %v after %v", exit, out, err, time.Since(start))
	}
	waitGroupGone(t, waitPID(t, pidFile))
}

func TestRunThatFailsAndLeavesABackgroundChildKeepsItsExitCode(t *testing.T) {
	r, _ := newRepo(t)
	start := time.Now()
	exit, out, err := r.Run(context.Background(), "", "sleep 30 & exit 3", time.Minute)
	if err != nil || exit != 3 || time.Since(start) > 5*time.Second {
		t.Fatalf("Run = %d, %q, %v after %v", exit, out, err, time.Since(start))
	}
}

func TestRunCancelledKillsTheProcessGroup(t *testing.T) {
	r, _ := newRepo(t)
	pidFile := filepath.Join(t.TempDir(), "gate.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		exit int
		err  error
	}
	done := make(chan result, 1)
	go func() {
		exit, _, err := r.Run(ctx, "", "echo $$ > '"+pidFile+"'; sleep 300", time.Minute)
		done <- result{exit, err}
	}()
	pid := waitPID(t, pidFile)
	cancel()
	select {
	case got := <-done:
		if got.exit != -1 || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("Run = %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not cancel")
	}
	waitGroupGone(t, pid)
}

func TestRunWithAnEndedContextStartsNothing(t *testing.T) {
	r, _ := newRepo(t)
	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := r.Run(ctx, "", "touch '"+marker+"'", time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker exists: %v", err)
	}
}

func TestRunCommandsSeeTerminalPromptOff(t *testing.T) {
	r, _ := newRepo(t)
	_, out, err := r.Run(context.Background(), "", "echo $GIT_TERMINAL_PROMPT", time.Minute)
	if err != nil || strings.TrimSpace(out) != "0" {
		t.Fatalf("Run = %q, %v", out, err)
	}
}

func TestAHangingGitCallIsKilledAfterTheTimeoutNamingTheCommand(t *testing.T) {
	r, dir := newRepo(t)
	old := gitTimeout
	gitTimeout = 300 * time.Millisecond
	t.Cleanup(func() { gitTimeout = old })
	pidFile := filepath.Join(t.TempDir(), "hook.pid")
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	write(t, hook, "#!/bin/sh\necho $$ > '"+pidFile+"'\nsleep 300\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "a.txt"), "change\n")
	for attempt := 0; attempt < 10; attempt++ {
		start := time.Now()
		_, err := r.CommitAll("", "change")
		if err == nil || !strings.Contains(err.Error(), "git commit") || !strings.Contains(err.Error(), "timed out after 300ms") || time.Since(start) > 5*time.Second {
			t.Fatalf("CommitAll = %v after %v", err, time.Since(start))
		}
		if _, err := os.Stat(pidFile); err == nil {
			waitProcessGone(t, waitPID(t, pidFile))
			return
		}
	}
	t.Fatal("pre-commit hook never started before the 300ms timeout")
}

func TestEveryGitCallRunsWithTerminalPromptOff(t *testing.T) {
	r, dir := newRepo(t)
	valueFile := filepath.Join(t.TempDir(), "prompt")
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	write(t, hook, "#!/bin/sh\nprintf '%s' \"$GIT_TERMINAL_PROMPT\" > '"+valueFile+"'\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "a.txt"), "change\n")
	if _, err := r.CommitAll("", "change"); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(stringMustRead(t, valueFile))); got != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT = %q", got)
	}
}

func stringMustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func slowMerge(t *testing.T) (*Repo, string, string, string) {
	t.Helper()
	r, dir := newRepo(t)
	pidFile := filepath.Join(t.TempDir(), "smudge.pid")
	write(t, filepath.Join(dir, ".gitattributes"), "*.slow filter=slow\n")
	git(t, dir, "add", ".gitattributes")
	git(t, dir, "commit", "-q", "-m", "attributes")
	git(t, dir, "config", "filter.slow.clean", "cat")
	git(t, dir, "config", "filter.slow.smudge", "sh -c 'echo $$ > "+pidFile+"; sleep 300'")
	branchWith(t, dir, "side", "a.slow", "slow\n")
	head := git(t, dir, "rev-parse", "HEAD")
	return r, dir, head, pidFile
}

func assertCleanMerge(t *testing.T, dir, head string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".git", "index.lock")); !os.IsNotExist(err) {
		t.Fatalf("index.lock remains: %v", err)
	}
	if got := status(t, dir); got != "" {
		t.Fatalf("status = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("MERGE_HEAD remains: %v", err)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want %s", got, head)
	}
	write(t, filepath.Join(dir, "after-recovery.txt"), "ok\n")
	git(t, dir, "add", "after-recovery.txt")
}

func TestMergeNoFFWithCancelledContextPreservesExistingEdits(t *testing.T) {
	r, dir := newRepo(t)
	branchWith(t, dir, "side", "feature.txt", "feature\n")
	head := git(t, dir, "rev-parse", "HEAD")
	write(t, filepath.Join(dir, "a.txt"), "uncommitted edit\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.MergeNoFF(ctx, "side"); !errors.Is(err, context.Canceled) {
		t.Fatalf("MergeNoFF = %v", err)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want %s", got, head)
	}
	if got := string(stringMustRead(t, filepath.Join(dir, "a.txt"))); got != "uncommitted edit\n" {
		t.Fatalf("tracked edit lost: %q", got)
	}
}

func TestMergeNoFFCancelledMidMergeLeavesThePreMergeHead(t *testing.T) {
	r, dir, head, pidFile := slowMerge(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.MergeNoFF(ctx, "side") }()
	pid := waitPID(t, pidFile)
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("MergeNoFF = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("MergeNoFF did not cancel")
	}
	waitProcessGone(t, pid)
	assertCleanMerge(t, dir, head)
}

func TestMergeNoFFTimedOutLeavesThePreMergeHead(t *testing.T) {
	r, dir, head, pidFile := slowMerge(t)
	old := gitTimeout
	gitTimeout = 2 * time.Second
	t.Cleanup(func() { gitTimeout = old })
	done := make(chan error, 1)
	go func() { done <- r.MergeNoFF(context.Background(), "side") }()
	pid := waitPID(t, pidFile)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "git merge --no-ff --no-commit") || !strings.Contains(err.Error(), "timed out after 2s") {
			t.Fatalf("MergeNoFF = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("MergeNoFF did not time out")
	}
	waitProcessGone(t, pid)
	assertCleanMerge(t, dir, head)
}

func TestCommitCancelledInAHangingHookMakesNoCommit(t *testing.T) {
	r, dir := newRepo(t)
	head := git(t, dir, "rev-parse", "HEAD")
	pidFile := filepath.Join(t.TempDir(), "hook.pid")
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	write(t, hook, "#!/bin/sh\necho $$ > '"+pidFile+"'\nsleep 300\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "a.txt"), "change\n")
	git(t, dir, "add", "-A")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := r.Commit(ctx, "change"); done <- err }()
	pid := waitPID(t, pidFile)
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("Commit = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Commit did not cancel")
	}
	waitProcessGone(t, pid)
	if got := git(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want %s", got, head)
	}
}

func TestCommitWithPostCommitBackgroundChildReturnsNewHead(t *testing.T) {
	r, dir := newRepo(t)
	hook := filepath.Join(dir, ".git", "hooks", "post-commit")
	write(t, hook, "#!/bin/sh\nsleep 30 &\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "a.txt"), "change\n")
	start := time.Now()
	sha, err := r.Commit(context.Background(), "change")
	if err != nil || sha != git(t, dir, "rev-parse", "HEAD") || time.Since(start) > 5*time.Second {
		t.Fatalf("Commit = %q, %v after %s", sha, err, time.Since(start))
	}
}

func TestCommitCancelledInPostCommitHookReturnsNewHead(t *testing.T) {
	r, dir := newRepo(t)
	before := git(t, dir, "rev-parse", "HEAD")
	pidFile := filepath.Join(t.TempDir(), "hook.pid")
	hook := filepath.Join(dir, ".git", "hooks", "post-commit")
	write(t, hook, "#!/bin/sh\necho $$ > '"+pidFile+"'\nsleep 300\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "a.txt"), "change\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		sha string
		err error
	}
	done := make(chan result, 1)
	go func() { sha, err := r.Commit(ctx, "change"); done <- result{sha, err} }()
	pid := waitPID(t, pidFile)
	cancel()
	select {
	case got := <-done:
		head := git(t, dir, "rev-parse", "HEAD")
		if head == before || got.err != nil || got.sha != head {
			t.Fatalf("Commit = %q, %v; HEAD = %q, before = %q", got.sha, got.err, head, before)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Commit did not cancel")
	}
	waitProcessGone(t, pid)
}

func TestCommitTimedOutInPostCommitHookReturnsNewHead(t *testing.T) {
	r, dir := newRepo(t)
	before := git(t, dir, "rev-parse", "HEAD")
	old := gitTimeout
	gitTimeout = 300 * time.Millisecond
	t.Cleanup(func() { gitTimeout = old })
	pidFile := filepath.Join(t.TempDir(), "hook.pid")
	hook := filepath.Join(dir, ".git", "hooks", "post-commit")
	write(t, hook, "#!/bin/sh\necho $$ > '"+pidFile+"'\nsleep 300\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "a.txt"), "change\n")
	sha, err := r.Commit(context.Background(), "change")
	if head := git(t, dir, "rev-parse", "HEAD"); head == before || err != nil || sha != head {
		t.Fatalf("Commit = %q, %v; HEAD = %q, before = %q", sha, err, head, before)
	}
	waitProcessGone(t, waitPID(t, pidFile))
}

func TestSnapshotKeepsTrackedIgnoredFile(t *testing.T) {
	r, dir := newRepo(t)
	write(t, filepath.Join(dir, "gen.out"), "v1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "gen")
	write(t, filepath.Join(dir, ".gitignore"), "*.out\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "ignore")

	first, err := r.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "gen.out"), "v2\n")
	second, err := r.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}

	if paths, err := r.TreeDiff(first, second); err != nil || !reflect.DeepEqual(paths, []string{"gen.out"}) {
		t.Fatalf("TreeDiff = %v, %v", paths, err)
	}
}

func TestPathsWithSpecialCharacters(t *testing.T) {
	r, dir := newRepo(t)
	base := git(t, dir, "rev-parse", "HEAD")
	write(t, filepath.Join(dir, "a.txt"), "changed\n")
	git(t, dir, "mv", "a.txt", "ré name.txt")
	git(t, dir, "commit", "-q", "-m", "rename")
	write(t, filepath.Join(dir, "ünï\tcode.txt"), "u\n")

	files, err := r.ChangedFiles("", base)
	if want := []string{"a.txt", "ré name.txt", "ünï\tcode.txt"}; err != nil || !reflect.DeepEqual(files, want) {
		t.Fatalf("ChangedFiles = %q, %v; want %q", files, err, want)
	}
	if added, deleted, err := r.DiffStat("", base); err != nil || added != 2 || deleted != 2 {
		t.Fatalf("DiffStat = %d, %d, %v; want 2, 2", added, deleted, err)
	}
	touched, err := r.CommitTouches("HEAD")
	if want := []string{"a.txt", "ré name.txt"}; err != nil || !reflect.DeepEqual(touched, want) {
		t.Fatalf("CommitTouches = %q, %v; want %q", touched, err, want)
	}
	paths, err := r.TreeDiff(base, "HEAD^{tree}")
	if want := []string{"a.txt", "ré name.txt"}; err != nil || !reflect.DeepEqual(paths, want) {
		t.Fatalf("TreeDiff = %q, %v; want %q", paths, err, want)
	}
}

func TestDiffStatCountsSymlinkNotTarget(t *testing.T) {
	r, dir := newRepo(t)
	if err := os.Symlink("a.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(dir, "dangling")); err != nil {
		t.Fatal(err)
	}

	if added, deleted, err := r.DiffStat("", "HEAD"); err != nil || added != 2 || deleted != 0 {
		t.Fatalf("DiffStat = %d, %d, %v; want 2, 0", added, deleted, err)
	}
}

func TestDiffStatCountsUntrackedBinaryAsZero(t *testing.T) {
	r, dir := newRepo(t)
	write(t, filepath.Join(dir, "bin.dat"), "a\x00b\nc\nd\n")
	write(t, filepath.Join(dir, "t.txt"), "x\ny")

	if added, deleted, err := r.DiffStat("", "HEAD"); err != nil || added != 2 || deleted != 0 {
		t.Fatalf("DiffStat = %d, %d, %v; want 2, 0", added, deleted, err)
	}
}

func TestDiffStatTreatsNULPastFirst8000BytesAsText(t *testing.T) {
	r, dir := newRepo(t)
	write(t, filepath.Join(dir, "late.dat"), strings.Repeat("a", 8000)+"\n\x00\n")

	if added, deleted, err := r.DiffStat("", "HEAD"); err != nil || added != 2 || deleted != 0 {
		t.Fatalf("DiffStat = %d, %d, %v; want 2, 0", added, deleted, err)
	}
}

func TestDiffStatStreamsLargeUntrackedFile(t *testing.T) {
	r, dir := newRepo(t)
	data := bytes.Repeat([]byte("x\n"), 16<<20)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	data = nil

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	added, deleted, err := r.DiffStat("", "HEAD")
	runtime.ReadMemStats(&after)
	if err != nil || added != 16<<20 || deleted != 0 {
		t.Fatalf("DiffStat = %d, %d, %v; want %d, 0", added, deleted, err, 16<<20)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc >= 4<<20 {
		t.Fatalf("DiffStat allocated %d bytes; want less than %d", alloc, 4<<20)
	}
}

func TestDiffStatCountsUntrackedTextLines(t *testing.T) {
	r, dir := newRepo(t)
	write(t, filepath.Join(dir, "empty.txt"), "")
	write(t, filepath.Join(dir, "blank.txt"), "\n\n")
	write(t, filepath.Join(dir, "open.txt"), "x\ny")
	write(t, filepath.Join(dir, "long.txt"), strings.Repeat("a", 40000))

	if added, deleted, err := r.DiffStat("", "HEAD"); err != nil || added != 5 || deleted != 0 {
		t.Fatalf("DiffStat = %d, %d, %v; want 5, 0", added, deleted, err)
	}
}

func TestDiffStatReturnsErrorForUnreadableUntrackedFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read files with no permissions")
	}
	r, dir := newRepo(t)
	path := filepath.Join(dir, "secret.txt")
	write(t, path, "s\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })

	if _, _, err := r.DiffStat("", "HEAD"); err == nil {
		t.Fatal("DiffStat returned no error for an unreadable file")
	}
}

func TestDeleteBranchDeletesAMergedBranchAndRefusesAnUnmergedOne(t *testing.T) {
	r, dir := newRepo(t)
	git(t, dir, "branch", "r-loop/phase-1")
	git(t, dir, "checkout", "-q", "-b", "r-loop/phase-2")
	write(t, filepath.Join(dir, "wip.txt"), "wip\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "wip")
	git(t, dir, "checkout", "-q", "main")

	if err := r.DeleteBranch("r-loop/phase-1", false); err != nil {
		t.Fatalf("DeleteBranch merged: %v", err)
	}
	if err := r.DeleteBranch("r-loop/phase-2", false); err == nil {
		t.Fatal("DeleteBranch deleted an unmerged branch")
	}
	if got := git(t, dir, "branch", "--list", "r-loop/*"); got != "r-loop/phase-2" {
		t.Fatalf("branches = %q", got)
	}
}

func TestSnapshotSeesAnIgnoredTaskPlan(t *testing.T) {
	r, dir := newRepo(t)
	write(t, filepath.Join(dir, ".gitignore"), ".task-plans/\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "ignore plans")
	before, err := r.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, ".task-plans", "phase-1-x.md"), "status: planned\n")

	after, err := r.Snapshot("")
	if err != nil {
		t.Fatal(err)
	}

	paths, err := r.TreeDiff(before, after)
	if err != nil || !reflect.DeepEqual(paths, []string{".task-plans/phase-1-x.md"}) {
		t.Fatalf("TreeDiff = %v, %v", paths, err)
	}
}

func TestCommitAllCommitsAnIgnoredTaskPlan(t *testing.T) {
	r, dir := newRepo(t)
	write(t, filepath.Join(dir, ".gitignore"), ".task-plans/\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "ignore plans")
	write(t, filepath.Join(dir, ".task-plans", "phase-1-x.md"), "status: planned\n")

	if _, err := r.CommitAll("", "plan"); err != nil {
		t.Fatal(err)
	}

	if got := git(t, dir, "show", "--name-only", "--format=", "HEAD"); got != ".task-plans/phase-1-x.md" {
		t.Fatalf("HEAD touches %q", got)
	}
}
