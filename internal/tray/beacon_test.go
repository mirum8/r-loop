package tray

import (
	"os"
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
	b.Start("20261118-101500", 42)
	b.Emit(core.Event{Kind: "step", Phase: "3", Step: "land"})
	b.Emit(core.Event{Kind: "watchdog-waiting"})

	got := Read(dir)
	want := Snapshot{Repo: "/r/app", Run: "20261118-101500", PID: 42, State: Waiting, Phase: "3", Step: "land", At: time.Unix(0, 0)}
	if len(got) != 1 || !got[0].At.Equal(want.At) {
		t.Fatalf("got %+v", got)
	}
	got[0].At = want.At
	if got[0] != want {
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
	old.Start("20261118-101500", 41)
	fresh := &Beacon{Dir: dir, Repo: "/r/app", Now: now}
	fresh.Start("20261118-111500", 42)

	old.Close()

	got := Read(dir)
	if len(got) != 1 || got[0].Run != "20261118-111500" {
		t.Fatalf("got %+v", got)
	}
}
