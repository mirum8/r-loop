package gitrepo

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
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

	if err := r.MergeNoFF("r-loop/phase-1"); err != nil {
		t.Fatalf("MergeNoFF: %v", err)
	}
	if got := git(t, dir, "rev-parse", "HEAD"); got != head {
		t.Fatal("MergeNoFF committed")
	}
	write(t, filepath.Join(dir, "todo.md"), "- [x] ticked\n")

	sha, err := r.Commit("r-loop: land phase 1")
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

func TestMergeConflictRestoresTree(t *testing.T) {
	r, dir := newRepo(t)
	branchWith(t, dir, "side", "a.txt", "side\n")
	write(t, filepath.Join(dir, "a.txt"), "main\n")
	git(t, dir, "commit", "-q", "-am", "main edit")
	head := git(t, dir, "rev-parse", "HEAD")

	err := r.MergeNoFF("side")
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

	if err := r.MergeNoFF("side"); err != nil {
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
	exit, out, err := r.Run("", "pwd; echo err >&2; exit 3", time.Minute)
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
	exit, out, err := r.Run("", "echo started; sleep 30 & sleep 30", 200*time.Millisecond)
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

func TestDeleteBranchDeletesAMergedBranchAndRefusesAnUnmergedOne(t *testing.T) {
	r, dir := newRepo(t)
	git(t, dir, "branch", "r-loop/phase-1")
	git(t, dir, "checkout", "-q", "-b", "r-loop/phase-2")
	write(t, filepath.Join(dir, "wip.txt"), "wip\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "wip")
	git(t, dir, "checkout", "-q", "main")

	if err := r.DeleteBranch("r-loop/phase-1"); err != nil {
		t.Fatalf("DeleteBranch merged: %v", err)
	}
	if err := r.DeleteBranch("r-loop/phase-2"); err == nil {
		t.Fatal("DeleteBranch deleted an unmerged branch")
	}
	if got := git(t, dir, "branch", "--list", "r-loop/*"); got != "r-loop/phase-2" {
		t.Fatalf("branches = %q", got)
	}
}
