package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"r-loop/internal/core"
	"r-loop/internal/store"
)

func TestCleanupRemovesTheWorktreesBranchesAndWorkspacesOfEveryEndedRun(t *testing.T) {
	f := newFixture(t)
	halted := f.seedRun(
		core.Record{Kind: core.RecordRun, Run: core.RunHalted},
		ev(t0, "phase-start", 2, "", map[string]string{"phase": "2"}),
		ev(t0, "step", 2, "implement", map[string]string{"state": "running", "attempt": "1", "workspace": "w7"}),
	)
	finished := f.seedRun(
		core.Record{Kind: core.RecordRun, Run: core.RunFinished},
		ev(t0, "phase-start", 4, "", map[string]string{"phase": "4"}),
	)
	f.commit()
	git(t, f.root, "worktree", "add", "-q", "-b", "r-loop/phase-2", ".r-loop/wt/phase-2")
	f.write(".r-loop/wt/phase-2/wip.txt", "unmerged\n")
	git(t, filepath.Join(f.root, ".r-loop/wt/phase-2"), "add", "-A")
	git(t, filepath.Join(f.root, ".r-loop/wt/phase-2"), "commit", "-q", "-m", "wip")
	git(t, f.root, "branch", "r-loop/phase-4")
	f.write(".r-loop/wt/phase-2-red/stale.txt", "x\n")

	code := f.main("--cleanup")

	want := "closed workspace w7 (run " + halted + ", phase 2)\n" +
		"removed worktree .r-loop/wt/phase-2\n" +
		"deleted branch r-loop/phase-2\n" +
		"deleted branch r-loop/phase-4\n" +
		"removed worktree .r-loop/wt/phase-2-red\n"
	if code != 0 || f.out.String() != want || f.err.String() != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, f.out.String(), f.err.String())
	}
	if entries, _ := os.ReadDir(filepath.Join(f.root, ".r-loop/wt")); len(entries) != 0 {
		t.Fatalf("left in .r-loop/wt: %v", entries)
	}
	if got := git(t, f.root, "branch", "--list", "r-loop/*"); got != "" {
		t.Fatalf("branches left: %q", got)
	}
	if got := git(t, f.root, "worktree", "list", "--porcelain"); strings.Contains(got, ".r-loop") {
		t.Fatalf("worktrees still registered: %q", got)
	}
	calls, _ := os.ReadFile(f.herdr + ".calls")
	if string(calls) != "workspace close w7\n" {
		t.Fatalf("herdr calls = %q", calls)
	}
	assertEvent(t, f, halted, "workspace-closed", "2", map[string]string{"workspace": "w7"})
	assertEvent(t, f, halted, "worktree-removed", "2", map[string]string{"worktree": ".r-loop/wt/phase-2", "branch": "r-loop/phase-2"})
	assertEvent(t, f, finished, "worktree-removed", "4", map[string]string{"branch": "r-loop/phase-4"})
}

func TestCleanupDoesNotCloseAWorkspaceTwice(t *testing.T) {
	f := newFixture(t)
	f.seedRun(
		core.Record{Kind: core.RecordRun, Run: core.RunHalted},
		ev(t0, "step", 2, "implement", map[string]string{"state": "running", "attempt": "1", "workspace": "w7"}),
	)
	if code := f.main("--cleanup"); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
	f.out.Reset()

	code := f.main("--cleanup")

	calls, _ := os.ReadFile(f.herdr + ".calls")
	if code != 0 || f.out.String() != "nothing to clean up\n" || string(calls) != "workspace close w7\n" {
		t.Fatalf("code=%d stdout=%q herdr calls=%q", code, f.out.String(), calls)
	}
}

func TestCleanupExits1WhenAWorkspaceCannotBeClosedAndCleansTheRest(t *testing.T) {
	f := newFixture(t)
	f.seedRun(
		core.Record{Kind: core.RecordRun, Run: core.RunHalted},
		ev(t0, "step", 2, "implement", map[string]string{"state": "running", "attempt": "1", "workspace": "w7"}),
	)
	f.commit()
	git(t, f.root, "branch", "r-loop/phase-2")
	f.fakeHerdr(1)

	code := f.main("--cleanup")

	if code != 1 || !strings.Contains(f.err.String(), "r-loop: close workspace w7") || f.out.String() != "deleted branch r-loop/phase-2\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, f.out.String(), f.err.String())
	}
}

func TestCleanupRetriesAWorkspaceWhoseCloseFailed(t *testing.T) {
	f := newFixture(t)
	id := f.seedRun(
		core.Record{Kind: core.RecordRun, Run: core.RunHalted},
		ev(t0, "step", 2, "implement", map[string]string{"state": "running", "attempt": "1", "workspace": "w7"}),
	)
	f.fakeHerdr(1)
	if code := f.main("--cleanup"); code != 1 {
		t.Fatalf("first cleanup code=%d stderr=%q", code, f.err.String())
	}
	f.fakeHerdr(0)
	f.out.Reset()

	code := f.main("--cleanup")

	if code != 0 || f.out.String() != "closed workspace w7 (run "+id+", phase 2)\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, f.out.String(), f.err.String())
	}
	assertEvent(t, f, id, "workspace-closed", "2", map[string]string{"workspace": "w7"})
}

func TestCleanupExits1WhenARunCannotBeRead(t *testing.T) {
	f := newFixture(t)
	id := f.seedRun(ev(t0, "step", 2, "implement", map[string]string{"state": "running", "attempt": "1", "workspace": "w7"}))
	if err := os.Remove(filepath.Join(f.root, ".r-loop/runs", id, "meta.json")); err != nil {
		t.Fatal(err)
	}

	code := f.main("--cleanup")

	if code != 1 || !strings.Contains(f.err.String(), "r-loop: skipped run "+id) {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, f.out.String(), f.err.String())
	}
}

func TestCleanupIsRefusedWhileARunIsLive(t *testing.T) {
	f := newFixture(t)
	f.seedRun(core.Record{Kind: core.RecordRun, Run: core.RunRunning})
	f.commit()
	git(t, f.root, "branch", "r-loop/phase-2")
	st := store.New(f.root)
	lock, err := st.Lock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release("", 0)
	if err := st.SetCurrent("20260918-120000", 4242); err != nil {
		t.Fatal(err)
	}
	lock.Publish()

	code := f.main("--cleanup")

	if code != 4 || !strings.Contains(f.err.String(), "run 20260918-120000 is live in pid 4242") {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
	if got := git(t, f.root, "branch", "--list", "r-loop/phase-2"); got == "" {
		t.Fatal("branch removed while a run is live")
	}
}

func TestCleanupKeepsTheCheckedOutBranch(t *testing.T) {
	f := newFixture(t)
	f.commit()
	git(t, f.root, "checkout", "-q", "-b", "r-loop/phase-2")

	code := f.main("--cleanup")

	if code != 0 || !strings.Contains(f.err.String(), "kept branch r-loop/phase-2") || git(t, f.root, "branch", "--show-current") != "r-loop/phase-2" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, f.out.String(), f.err.String())
	}
}

func TestCleanupOnAFreshRepositoryTakesTheLockAndLeavesTheTreeClean(t *testing.T) {
	f := newFixture(t)
	f.commit()

	code := f.main("--cleanup")

	if code != 0 || f.out.String() != "nothing to clean up\n" || f.herdrCalled() {
		t.Fatalf("code=%d stdout=%q stderr=%q herdr called=%v", code, f.out.String(), f.err.String(), f.herdrCalled())
	}
	if _, err := os.Stat(filepath.Join(f.root, ".r-loop/runs/lock")); err != nil {
		t.Fatalf("no run lock taken: %v", err)
	}
	if got := git(t, f.root, "status", "--porcelain"); got != "" {
		t.Fatalf("tree not clean: %q", got)
	}
}

func assertEvent(t *testing.T, f *fixture, runID, kind, phase string, fields map[string]string) {
	t.Helper()
	run, err := store.New(f.root).Load(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range run.Events {
		if e.Kind != kind || e.Phase != phase || len(e.Fields) != len(fields) {
			continue
		}
		match := true
		for k, v := range fields {
			match = match && e.Fields[k] == v
		}
		if match {
			return
		}
	}
	t.Fatalf("run %s has no %s event for phase %s with %v: %v", runID, kind, phase, fields, run.Events)
}
