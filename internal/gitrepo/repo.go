package gitrepo

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"r-loop/internal/core"
)

var identity = []string{
	"GIT_AUTHOR_NAME=r-loop", "GIT_AUTHOR_EMAIL=r-loop@local",
	"GIT_COMMITTER_NAME=r-loop", "GIT_COMMITTER_EMAIL=r-loop@local",
}

type Repo struct {
	root string
}

func Open(dir string) (*Repo, error) {
	root, err := run(dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	return &Repo{root: strings.TrimSpace(root)}, nil
}

func run(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func split0(out string) []string {
	var paths []string
	for _, l := range strings.Split(out, "\x00") {
		if l != "" {
			paths = append(paths, l)
		}
	}
	return paths
}

func (r *Repo) path(dir string) string {
	if dir == "" {
		return r.root
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(r.root, dir)
}

func (r *Repo) git(dir string, args ...string) (string, error) {
	return run(r.path(dir), nil, args...)
}

func (r *Repo) Root() string { return r.root }

func (r *Repo) Clean() ([]string, error) { return r.Dirty("") }

func (r *Repo) HeadBranch() (string, error) {
	out, err := r.git("", "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil {
		return "", fmt.Errorf("HEAD is detached or unreadable: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func (r *Repo) HeadSHA(dir string) (string, error) {
	out, err := r.git(dir, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

func (r *Repo) AddWorktree(dir, branch, base string) error {
	abs := r.path(dir)
	current, found, err := r.worktreeBranch(abs)
	if err != nil {
		return err
	}
	if found {
		if current != "refs/heads/"+branch {
			return fmt.Errorf("worktree %s is on %s, not %s", abs, strings.TrimPrefix(current, "refs/heads/"), branch)
		}
		return nil
	}
	if _, err := r.git("", "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err != nil {
		if _, err := r.git("", "branch", branch, base); err != nil {
			return err
		}
	}
	if _, err := r.git("", "worktree", "prune"); err != nil {
		return err
	}
	_, err = r.git("", "worktree", "add", "--quiet", abs, branch)
	return err
}

func (r *Repo) worktreeBranch(abs string) (string, bool, error) {
	want, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", false, nil
	}
	out, err := r.git("", "worktree", "list", "--porcelain")
	if err != nil {
		return "", false, err
	}
	for _, block := range strings.Split(out, "\n\n") {
		var path, branch string
		for _, l := range strings.Split(block, "\n") {
			if p, ok := strings.CutPrefix(l, "worktree "); ok {
				path = p
			}
			if b, ok := strings.CutPrefix(l, "branch "); ok {
				branch = b
			}
		}
		if path == "" {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved == want {
			return branch, true, nil
		}
	}
	return "", false, nil
}

func (r *Repo) RemoveWorktree(dir string) error {
	if _, err := r.git("", "worktree", "remove", "--force", r.path(dir)); err != nil {
		return err
	}
	_, err := r.git("", "worktree", "prune")
	return err
}

func (r *Repo) Dirty(dir string) ([]string, error) {
	out, err := r.git(dir, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		paths = append(paths, f[3:])
		if f[0] == 'R' || f[0] == 'C' {
			i++
		}
	}
	slices.Sort(paths)
	return paths, nil
}

func (r *Repo) CommitAll(dir, message string) (string, error) {
	dirty, err := r.Dirty(dir)
	if err != nil {
		return "", err
	}
	if len(dirty) == 0 {
		return r.HeadSHA(dir)
	}
	if _, err := r.git(dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := run(r.path(dir), identity, "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	return r.HeadSHA(dir)
}

func (r *Repo) ChangedFiles(dir, ref string) ([]string, error) {
	tracked, err := r.git(dir, "diff", "--name-only", "-z", "--no-renames", ref)
	if err != nil {
		return nil, err
	}
	untracked, err := r.git(dir, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	paths := append(split0(tracked), split0(untracked)...)
	slices.Sort(paths)
	return slices.Compact(paths), nil
}

func (r *Repo) DiffNonEmpty(dir, ref string) (bool, error) {
	paths, err := r.ChangedFiles(dir, ref)
	return len(paths) > 0, err
}

func (r *Repo) DiffStat(dir, ref string) (int, int, error) {
	out, err := r.git(dir, "diff", "--numstat", "-z", "--no-renames", ref)
	if err != nil {
		return 0, 0, err
	}
	added, deleted := 0, 0
	for _, l := range split0(out) {
		f := strings.SplitN(l, "\t", 3)
		if len(f) < 3 {
			continue
		}
		a, _ := strconv.Atoi(f[0])
		d, _ := strconv.Atoi(f[1])
		added += a
		deleted += d
	}
	untracked, err := r.git(dir, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return 0, 0, err
	}
	for _, p := range split0(untracked) {
		b, err := blob(filepath.Join(r.path(dir), p))
		if err != nil {
			return 0, 0, err
		}
		added += bytes.Count(b, []byte("\n"))
		if len(b) > 0 && b[len(b)-1] != '\n' {
			added++
		}
	}
	return added, deleted, nil
}

func blob(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		return []byte(target), err
	}
	return os.ReadFile(path)
}

func (r *Repo) Snapshot(dir string) (string, error) {
	gitDir, err := r.git(dir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(strings.TrimSpace(gitDir), "r-loop-index-")
	if err != nil {
		return "", err
	}
	index := f.Name()
	defer os.Remove(index)
	err = r.seedIndex(dir, f)
	f.Close()
	if err != nil {
		return "", err
	}
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := run(r.path(dir), env, "add", "-A"); err != nil {
		return "", err
	}
	tree, err := run(r.path(dir), env, "write-tree")
	return strings.TrimSpace(tree), err
}

func (r *Repo) seedIndex(dir string, f *os.File) error {
	out, err := r.git(dir, "rev-parse", "--git-path", "index")
	if err != nil {
		return err
	}
	real := strings.TrimSpace(out)
	if !filepath.IsAbs(real) {
		real = filepath.Join(r.path(dir), real)
	}
	src, err := os.Open(real)
	if errors.Is(err, os.ErrNotExist) {
		return os.Remove(f.Name())
	}
	if err != nil {
		return err
	}
	defer src.Close()
	_, err = io.Copy(f, src)
	return err
}

func (r *Repo) TreeDiff(from, to string) ([]string, error) {
	out, err := r.git("", "diff-tree", "-r", "-z", "--name-only", from, to)
	return split0(out), err
}

func (r *Repo) MergeNoFF(branch string) error {
	_, mergeErr := r.git("", "merge", "--no-ff", "--no-commit", branch)
	if mergeErr == nil {
		return nil
	}
	out, err := r.git("", "diff", "--name-only", "-z", "--diff-filter=U")
	if err != nil {
		return errors.Join(mergeErr, err)
	}
	conflicts := split0(out)
	if len(conflicts) == 0 {
		return mergeErr
	}
	if err := r.AbortMerge(); err != nil {
		return errors.Join(fmt.Errorf("%w: %s", core.ErrMergeConflict, strings.Join(conflicts, ", ")), err)
	}
	return fmt.Errorf("%w: %s", core.ErrMergeConflict, strings.Join(conflicts, ", "))
}

func (r *Repo) AbortMerge() error {
	_, err := r.git("", "merge", "--abort")
	return err
}

func (r *Repo) Commit(message string) (string, error) {
	if _, err := r.git("", "add", "-A"); err != nil {
		return "", err
	}
	if _, err := run(r.root, identity, "commit", "-q", "-m", message); err != nil {
		return "", err
	}
	return r.HeadSHA("")
}

func (r *Repo) CommitTouches(sha string) ([]string, error) {
	out, err := r.git("", "show", "--name-only", "-z", "--no-renames", "--format=", "--diff-merges=first-parent", sha)
	return split0(out), err
}

func (r *Repo) ResetHard(ref string) error {
	_, err := r.git("", "reset", "--hard", "-q", ref)
	return err
}

func (r *Repo) Run(dir, command string, timeout time.Duration) (int, string, error) {
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = r.path(dir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return -1, "", err
	}
	var timedOut atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		timedOut.Store(true)
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	})
	err := cmd.Wait()
	timer.Stop()
	output := out.String()
	if timedOut.Load() {
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		return -1, output + "timed out after " + timeout.String(), nil
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return -1, output, err
	}
	return cmd.ProcessState.ExitCode(), output, nil
}
