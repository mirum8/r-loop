package tray

import (
	"os"
	"reflect"
	"testing"
	"time"

	"r-loop/internal/core"
)

func TestTheBeaconWritesTheRunStateAndRemovesItOnClose(t *testing.T) {
	dir := t.TempDir()
	b := &Beacon{Dir: dir, Repo: "/r/app", Now: func() time.Time { return time.Unix(0, 0) }}

	b.Emit(core.Event{Kind: "watchdog-waiting"})
	if got := Read(dir); len(got) != 0 {
		t.Fatalf("before start: %+v", got)
	}
	b.Start("20261118-101500", 42, nil)
	b.Emit(core.Event{Kind: "step", Phase: "3", Step: "land"})
	b.Emit(core.Event{Kind: "watchdog-waiting"})

	got := Read(dir)
	want := Snapshot{Repo: "/r/app", Run: "20261118-101500", PID: 42, State: Waiting, Phase: "3", Step: "land", At: time.Unix(0, 0), id: b.id()}
	if len(got) != 1 || !got[0].At.Equal(want.At) {
		t.Fatalf("got %+v", got)
	}
	got[0].At = want.At
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}

	b.Close()
	if got := Read(dir); len(got) != 0 {
		t.Fatalf("after close: %+v", got)
	}
	entries, _ := os.ReadDir(runsDir(dir))
	if len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}

func TestOnlyOneProcessOwnsTheTray(t *testing.T) {
	dir := t.TempDir()
	first := tryLock(dir)
	if first == nil {
		t.Fatal("first lock failed")
	}
	defer first.Close()

	if second := tryLock(dir); second != nil {
		second.Close()
		t.Fatal("second lock succeeded")
	}
}

func TestADeadPidIsNotAlive(t *testing.T) {
	if !Alive(os.Getpid()) || Alive(0) || Alive(99999999) {
		t.Fatal("liveness wrong")
	}
}

func TestClosingAnOldRunKeepsTheNextRunsBeacon(t *testing.T) {
	dir := t.TempDir()
	now := func() time.Time { return time.Unix(0, 0) }
	old := &Beacon{Dir: dir, Repo: "/r/app", Now: now}
	old.Start("20261118-101500", 41, nil)
	fresh := &Beacon{Dir: dir, Repo: "/r/app", Now: now}
	fresh.Start("20261118-111500", 42, nil)

	old.Close()

	got := Read(dir)
	if len(got) != 1 || got[0].Run != "20261118-111500" {
		t.Fatalf("got %+v", got)
	}
}

func TestAResumedRunStartsFromItsRecordedProgress(t *testing.T) {
	dir := t.TempDir()
	b := &Beacon{Dir: dir, Repo: "/r/app", Now: time.Now}

	b.Start("20261118-101500", 42, []core.Event{
		{Kind: "run-list", Fields: map[string]string{"phases": "1,2,3"}},
		{Kind: "landed", Phase: "1"},
		{Kind: "watchdog-waiting"},
		{Kind: "halt"},
	})

	got := Read(dir)
	if len(got) != 1 || got[0].State != Running || got[0].Done != 1 || got[0].Total != 3 {
		t.Fatalf("got %+v", got)
	}
}

func TestTheBeaconAnswersAFocusRequestForItsRun(t *testing.T) {
	dir := t.TempDir()
	focused := make(chan struct{}, 1)
	b := &Beacon{Dir: dir, Repo: "/r/app", Now: time.Now, Focus: func() { focused <- struct{}{} }}
	b.Start("20261118-101500", 42, nil)
	defer b.Close()
	rows := Aggregate(Read(dir), func(int) bool { return true }).Rows

	if err := os.WriteFile(requestPath(dir, rows[0].ID), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-focused:
	case <-time.After(3 * time.Second):
		t.Fatal("not focused")
	}
	if _, err := os.Stat(requestPath(dir, rows[0].ID)); !os.IsNotExist(err) {
		t.Fatalf("request left: %v", err)
	}
}

func TestTheTerminalAppIsTheFirstAppAncestor(t *testing.T) {
	commands := map[int]string{
		42: "/Users/me/.local/bin/r-loop",
		41: "-zsh",
		40: "/Applications/herdr-app.app/Contents/MacOS/herdr-app",
		1:  "/sbin/launchd",
	}
	parents := map[int]int{42: 41, 41: 40, 40: 1}

	app := appOf(42, func(p int) int { return parents[p] }, func(p int) string { return commands[p] })

	if app != "/Applications/herdr-app.app" {
		t.Fatalf("got %q", app)
	}
	if got := appOf(42, func(int) int { return 1 }, func(int) string { return "r-loop" }); got != "" {
		t.Fatalf("got %q", got)
	}
}
