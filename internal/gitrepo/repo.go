package gitrepo

import (
	"bufio"
	"bytes"
	"context"
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

var gitTimeout = 10 * time.Minute
var lockBackoff = []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond}

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
	return runCtx(context.Background(), dir, env, args...)
}

func runCtx(parent context.Context, dir string, env []string, args ...string) (string, error) {
	return runCtxStarted(parent, dir, env, nil, nil, args...)
}

func runCtxStarted(parent context.Context, dir string, env []string, beforeStart func(), started *bool, args ...string) (string, error) {
	if err := parent.Err(); err != nil {
		return "", fmt.Errorf("git %s: interrupted: %w", strings.Join(args, " "), err)
	}
	for i := 0; ; i++ {
		out, stderr, err := runOnce(parent, dir, env, beforeStart, started, args...)
		locked := err != nil && (strings.Contains(stderr, "index.lock': File exists") ||
			strings.Contains(stderr, "Unable to write index") && indexLockExists(dir, env))
		if locked && started != nil {
			*started = false
		}
		if err == nil || i == len(lockBackoff) || !locked {
			return out, err
		}
		select {
		case <-parent.Done():
			return out, fmt.Errorf("git %s: interrupted: %w", strings.Join(args, " "), parent.Err())
		case <-time.After(lockBackoff[i]):
		}
	}
}

func indexLockExists(dir string, env []string) bool {
	path, _, err := runOnce(context.Background(), dir, env, nil, nil, "rev-parse", "--git-path", "index.lock")
	if err != nil {
		return false
	}
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	_, err = os.Stat(path)
	return err == nil
}

func runOnce(parent context.Context, dir string, env []string, beforeStart func(), started *bool, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(parent, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if beforeStart != nil {
		beforeStart()
	}
	err := cmd.Start()
	if err == nil {
		if started != nil {
			*started = true
		}
		err = cmd.Wait()
	}
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		err = nil
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return stdout.String(), stderr.String(), fmt.Errorf("git %s: timed out after %s: %w", strings.Join(args, " "), gitTimeout, context.DeadlineExceeded)
		}
		if parent.Err() != nil {
			return stdout.String(), stderr.String(), fmt.Errorf("git %s: interrupted: %w", strings.Join(args, " "), parent.Err())
		}
		return stdout.String(), stderr.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), stderr.String(), nil
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

func (r *Repo) RevParse(ref string) (string, error) {
	out, err := r.git("", "rev-parse", "--verify", "-q", ref+"^{commit}")
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
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
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
	abs := r.path(dir)
	if _, found, err := r.worktreeBranch(abs); err != nil {
		return err
	} else if !found {
		if _, err := os.Lstat(abs); err == nil {
			return fmt.Errorf("worktree directory %s remains but is not registered", dir)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_, err := r.git("", "worktree", "prune")
		return err
	}
	if _, err := r.git("", "worktree", "remove", "--force", abs); err != nil {
		return err
	}
	if _, err := os.Lstat(abs); err == nil {
		return fmt.Errorf("worktree directory %s remains after removal", dir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err := r.git("", "worktree", "prune")
	return err
}

func (r *Repo) WorktreePath(branch string) (string, error) {
	out, err := r.git("", "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	for _, block := range strings.Split(out, "\n\n") {
		var path, current string
		for _, line := range strings.Split(block, "\n") {
			if value, ok := strings.CutPrefix(line, "worktree "); ok {
				path = value
			}
			if value, ok := strings.CutPrefix(line, "branch "); ok {
				current = value
			}
		}
		if current == "refs/heads/"+branch {
			return path, nil
		}
	}
	return "", nil
}

func (r *Repo) BranchExists(branch string) (bool, error) {
	out, err := r.git("", "branch", "--list", branch)
	return strings.TrimSpace(out) != "", err
}

func (r *Repo) DeleteBranch(branch string, force bool) error {
	exists, err := r.BranchExists(branch)
	if err != nil || !exists {
		return err
	}
	flag := "-d"
	if force {
		flag = "-D"
	}
	_, err = r.git("", "branch", flag, branch)
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
	if _, err := r.git(dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := r.git(dir, "add", "--renormalize", "-u"); err != nil {
		return "", err
	}
	dirty, err := r.Dirty(dir)
	if err != nil {
		return "", err
	}
	if len(dirty) == 0 {
		return r.HeadSHA(dir)
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
		n, err := untrackedLines(filepath.Join(r.path(dir), p))
		if err != nil {
			return 0, 0, err
		}
		added += n
	}
	return added, deleted, nil
}

func untrackedLines(path string) (int, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	var src io.Reader
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return 0, err
		}
		src = strings.NewReader(target)
	} else {
		f, err := os.Open(path)
		if err != nil {
			return 0, err
		}
		defer f.Close()
		src = f
	}
	br := bufio.NewReaderSize(src, 32<<10)
	head, err := br.Peek(8000)
	if err != nil && err != io.EOF && err != bufio.ErrBufferFull {
		return 0, err
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return 0, nil
	}
	buf := make([]byte, 32<<10)
	n, last := 0, byte('\n')
	for {
		k, err := br.Read(buf)
		n += bytes.Count(buf[:k], []byte{'\n'})
		if k > 0 {
			last = buf[k-1]
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if last != '\n' {
		n++
	}
	return n, nil
}

func (r *Repo) Snapshot(dir string) (string, error) {
	return r.tempTree(dir, "add", "-A")
}

func (r *Repo) IndexTree(paths ...string) (string, error) {
	if len(paths) == 0 {
		return r.tempTree("")
	}
	return r.tempTree("", append([]string{"add", "--"}, paths...)...)
}

func (r *Repo) tempTree(dir string, add ...string) (string, error) {
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
	if len(add) > 0 {
		if _, err := run(r.path(dir), env, add...); err != nil {
			return "", err
		}
		if len(add) == 2 && add[0] == "add" && add[1] == "-A" {
			if _, err := run(r.path(dir), env, "add", "--renormalize", "-u"); err != nil {
				return "", err
			}
		} else if len(add) > 2 && add[0] == "add" {
			if _, err := run(r.path(dir), env, append([]string{"add", "--renormalize", "--"}, add[2:]...)...); err != nil {
				return "", err
			}
		}
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

func (r *Repo) GitlinkPaths(tree string) ([]string, error) {
	out, err := r.git("", "ls-tree", "-r", "-z", tree)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range split0(out) {
		if strings.HasPrefix(entry, "160000 commit ") {
			_, path, ok := strings.Cut(entry, "\t")
			if !ok {
				return nil, fmt.Errorf("git ls-tree: malformed gitlink entry")
			}
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func (r *Repo) MergeTree(base, tip string) (string, error) {
	out, err := r.git("", "merge-tree", "--write-tree", base, tip)
	return strings.TrimSpace(out), err
}

func (r *Repo) ReadTreeFile(tree, path string) ([]byte, error) {
	out, err := r.git("", "show", tree+":"+path)
	return []byte(out), err
}

func (r *Repo) MergeNoFF(ctx context.Context, branch string, keep ...string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("git merge --no-ff --no-commit %s: interrupted: %w", branch, err)
	}
	pre, err := r.HeadSHA("")
	if err != nil {
		return err
	}
	lockPath, err := r.git("", "rev-parse", "--git-path", "index.lock")
	if err != nil {
		return err
	}
	lockPath = strings.TrimSpace(lockPath)
	if !filepath.IsAbs(lockPath) {
		lockPath = filepath.Join(r.root, lockPath)
	}
	var lockWasAbsent bool
	var started bool
	_, mergeErr := runCtxStarted(ctx, r.root, nil, func() { _, err := os.Stat(lockPath); lockWasAbsent = errors.Is(err, os.ErrNotExist) }, &started, "merge", "--no-ff", "--no-commit", branch)
	if mergeErr == nil {
		if len(keep) > 0 {
			if _, err := r.git("", append([]string{"checkout", "HEAD", "--"}, keep...)...); err != nil {
				return errors.Join(err, r.AbortMerge())
			}
		}
		return nil
	}
	if ctx.Err() != nil || errors.Is(mergeErr, context.DeadlineExceeded) {
		if !started {
			return mergeErr
		}
		if lockWasAbsent {
			if err := os.Remove(lockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return errors.Join(mergeErr, fmt.Errorf("remove interrupted merge lock: %w", err))
			}
		}
		if abortErr := r.AbortMerge(); abortErr != nil {
			if resetErr := r.ResetHard(pre); resetErr != nil {
				return errors.Join(mergeErr, abortErr, resetErr)
			}
		}
		return mergeErr
	}
	out, err := r.git("", "diff", "--name-only", "-z", "--diff-filter=U")
	if err != nil {
		return errors.Join(mergeErr, err)
	}
	conflicts := split0(out)
	if len(conflicts) == 0 {
		return mergeErr
	}
	if len(keep) > 0 {
		allowed := make(map[string]bool, len(keep))
		for _, p := range keep {
			allowed[p] = true
		}
		onlyKeep := true
		for _, p := range conflicts {
			if !allowed[p] {
				onlyKeep = false
				break
			}
		}
		if onlyKeep {
			if _, err := r.git("", append([]string{"checkout", "HEAD", "--"}, keep...)...); err != nil {
				return errors.Join(err, r.AbortMerge())
			}
			return nil
		}
	}
	if err := r.AbortMerge(); err != nil {
		return errors.Join(fmt.Errorf("%w: %s", core.ErrMergeConflict, strings.Join(conflicts, ", ")), err)
	}
	return fmt.Errorf("%w: %s", core.ErrMergeConflict, strings.Join(conflicts, ", "))
}

func (r *Repo) AbortMerge() error {
	_, _ = r.git("", "update-index", "-q", "--refresh")
	_, err := r.git("", "merge", "--abort")
	if err != nil {
		return fmt.Errorf("%w: git merge --abort failed, so the primary tree still holds an unfinished merge; finish it with git merge --abort: %v", core.ErrUnfinishedMerge, err)
	}
	return nil
}

func (r *Repo) MergeInProgress() (bool, error) {
	path, err := r.git("", "rev-parse", "--git-path", "MERGE_HEAD")
	if err != nil {
		return false, err
	}
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.root, path)
	}
	_, err = os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (r *Repo) Commit(ctx context.Context, message string, paths ...string) (string, error) {
	add := []string{"add", "-A"}
	if len(paths) > 0 {
		add = append([]string{"add", "--"}, paths...)
	}
	if _, err := r.git("", add...); err != nil {
		return "", err
	}
	if len(paths) > 0 {
		if _, err := r.git("", append([]string{"add", "--renormalize", "--"}, paths...)...); err != nil {
			return "", err
		}
	} else if _, err := r.git("", "add", "--renormalize", "-u"); err != nil {
		return "", err
	}
	before, err := r.HeadSHA("")
	if err != nil {
		return "", err
	}
	_, commitErr := runCtx(ctx, r.root, identity, "commit", "-q", "-m", message)
	after, headErr := r.HeadSHA("")
	if headErr != nil {
		return "", errors.Join(commitErr, headErr)
	}
	if after != before {
		return after, nil
	}
	if commitErr == nil {
		return "", fmt.Errorf("git commit did not advance HEAD")
	}
	return "", commitErr
}

func (r *Repo) CommitTouches(sha string) ([]string, error) {
	out, err := r.git("", "show", "--name-only", "-z", "--no-renames", "--format=", "--diff-merges=first-parent", sha)
	return split0(out), err
}

func (r *Repo) LandedCommit(base, tip, tree string) (string, error) {
	out, err := r.git("", "log", "--first-parent", "--reverse", "--format=%H%x00%P%x00%T", base+"..HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.Split(line, "\x00")
		if len(parts) != 3 {
			continue
		}
		parents := strings.Fields(parts[1])
		if len(parents) == 2 && parents[0] == base && parents[1] == tip && strings.TrimSpace(parts[2]) == tree {
			return parts[0], nil
		}
	}
	return "", nil
}

func (r *Repo) ResetHard(ref string) error {
	_, err := r.git("", "reset", "--hard", "-q", ref)
	return err
}

func (r *Repo) ResetKeep(ref string) error {
	_, err := r.git("", "reset", "--keep", "-q", ref)
	return err
}

func (r *Repo) Run(ctx context.Context, dir, command string, timeout time.Duration) (int, string, error) {
	if err := ctx.Err(); err != nil {
		return -1, "", fmt.Errorf("interrupted: %w", err)
	}
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = r.path(dir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		return -1, "", err
	}
	kill := func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	stop := context.AfterFunc(ctx, kill)
	var timedOut atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		timedOut.Store(true)
		kill()
	})
	err := cmd.Wait()
	timer.Stop()
	stop()
	kill()
	output := out.String()
	if timedOut.Load() {
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		return -1, output + "timed out after " + timeout.String(), nil
	}
	if err := ctx.Err(); err != nil {
		return -1, output, fmt.Errorf("interrupted: %w", err)
	}
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) && !errors.Is(err, exec.ErrWaitDelay) {
		return -1, output, err
	}
	return cmd.ProcessState.ExitCode(), output, nil
}
