package tray

import (
	"reflect"
	"testing"
)

func alive(pids ...int) func(int) bool {
	return func(pid int) bool {
		for _, p := range pids {
			if p == pid {
				return true
			}
		}
		return false
	}
}

func TestNoActiveRunHidesTheIcon(t *testing.T) {
	snaps := []Snapshot{
		{Repo: "/r/a", PID: 1, State: Finished},
		{Repo: "/r/b", PID: 2, State: Halted},
		{Repo: "/r/c", PID: 3, State: Running},
	}

	v := Aggregate(snaps, alive(1, 2))

	if v.Visible || len(v.Rows) != 0 {
		t.Fatalf("got %+v", v)
	}
}

func TestTheWorstActiveStateWins(t *testing.T) {
	snaps := []Snapshot{
		{Repo: "/r/b", PID: 1, State: Running, Phase: "2", Step: "plan"},
		{Repo: "/r/a", PID: 2, State: Waiting, Phase: "7"},
		{Repo: "/r/c", PID: 3, State: Paused},
		{Repo: "/r/d", PID: 4, State: Finished},
	}

	v := Aggregate(snaps, alive(1, 2, 3, 4))

	want := View{Visible: true, State: Waiting, Rows: []Row{
		{Repo: "/r/a", Label: "a · phase 7 · waiting"},
		{Repo: "/r/b", Label: "b · phase 2 plan · running"},
		{Repo: "/r/c", Label: "c · paused"},
	}}
	if !reflect.DeepEqual(v, want) {
		t.Fatalf("got %+v", v)
	}
}

func TestPausedOutranksRunning(t *testing.T) {
	v := Aggregate([]Snapshot{{PID: 1, State: Running}, {PID: 2, State: Paused}}, alive(1, 2))

	if v.State != Paused {
		t.Fatalf("got %s", v.State)
	}
}

func TestProgressAddsUpAcrossRuns(t *testing.T) {
	snaps := []Snapshot{
		{Repo: "/r/a", PID: 1, State: Running, Phase: "3", Step: "implement", Done: 2, Total: 5},
		{Repo: "/r/b", PID: 2, State: Waiting, Done: 1, Total: 7},
	}

	v := Aggregate(snaps, alive(1, 2))

	if v.Done != 3 || v.Total != 12 || v.Rows[0].Label != "a · 2/5 · phase 3 implement · running" {
		t.Fatalf("got %+v", v)
	}
}
