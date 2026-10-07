package reviewexec

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunReviewReturnsOnlyStdoutFromTheDir(t *testing.T) {
	// given
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// when
	out, err := New().RunReview(context.Background(), dir, "echo transcript >&2; pwd -P; echo '- [P1] bug'", time.Minute)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if expected := dir + "\n- [P1] bug\n"; out != expected {
		t.Fatalf("actual %q, expected %q", out, expected)
	}
}

func TestRunReviewFailsOnANonZeroExitNamingTheLastStderrLine(t *testing.T) {
	// when
	out, err := New().RunReview(context.Background(), t.TempDir(), "echo partial; echo 'transcript' >&2; echo 'not logged in' >&2; exit 3", time.Minute)

	// then
	if err == nil || err.Error() != "exit status 3: not logged in" {
		t.Fatalf("error %v", err)
	}
	if out != "partial\n" {
		t.Fatalf("out %q", out)
	}
}

func TestRunReviewKillsTheProcessGroupWhenItTimesOut(t *testing.T) {
	// given
	pidFile := filepath.Join(t.TempDir(), "pid")
	begin := time.Now()

	// when
	_, err := New().RunReview(context.Background(), t.TempDir(), "sleep 30 & echo $! > "+pidFile+"; wait", 300*time.Millisecond)

	// then
	if err == nil || !strings.Contains(err.Error(), "timed out after 300ms") {
		t.Fatalf("error %v", err)
	}
	if took := time.Since(begin); took > 5*time.Second {
		t.Fatalf("took %s", took)
	}
	assertGone(t, pidFile)
}

func TestRunReviewKeepsTheReportAndKillsAChildLeftHoldingStdout(t *testing.T) {
	// given
	pidFile := filepath.Join(t.TempDir(), "pid")

	// when
	out, err := New().RunReview(context.Background(), t.TempDir(), "sleep 30 & echo $! > "+pidFile+"; echo '- [P1] bug'", time.Minute)

	// then
	if err != nil {
		t.Fatal(err)
	}
	if out != "- [P1] bug\n" {
		t.Fatalf("out %q", out)
	}
	assertGone(t, pidFile)
}

func TestRunReviewReportsAnInterruptedParent(t *testing.T) {
	// given
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	// when
	_, err := New().RunReview(ctx, t.TempDir(), "sleep 30", time.Minute)

	// then
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("error %v", err)
	}
}

func assertGone(t *testing.T, pidFile string) {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
