package tray

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type View struct {
	Visible     bool
	State       State
	Done, Total int
	Rows        []Row
}

type Row struct {
	ID    string
	Repo  string
	Label string
}

var rank = map[State]int{Running: 1, Paused: 2, Waiting: 3}

func Aggregate(snaps []Snapshot, alive func(pid int) bool) View {
	var v View
	for _, s := range snaps {
		if rank[s.State] == 0 || !alive(s.PID) {
			continue
		}
		if rank[s.State] > rank[v.State] {
			v.State = s.State
		}
		v.Done += s.Done
		v.Total += s.Total
		v.Rows = append(v.Rows, Row{ID: s.id, Repo: s.Repo, Label: label(s)})
	}
	sort.Slice(v.Rows, func(i, j int) bool { return v.Rows[i].Label < v.Rows[j].Label })
	v.Visible = len(v.Rows) > 0
	return v
}

func label(s Snapshot) string {
	parts := []string{filepath.Base(s.Repo)}
	if s.Total > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d", s.Done, s.Total))
	}
	if s.Phase != "" {
		where := "phase " + s.Phase
		if s.Step != "" {
			where += " " + s.Step
		}
		parts = append(parts, where)
	}
	parts = append(parts, string(s.State))
	return strings.Join(parts, " · ")
}

func Read(dir string) []Snapshot {
	paths, _ := filepath.Glob(filepath.Join(runsDir(dir), "*.json"))
	var snaps []Snapshot
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		s := Snapshot{id: strings.TrimSuffix(filepath.Base(p), ".json")}
		if json.Unmarshal(data, &s) == nil {
			snaps = append(snaps, s)
		}
	}
	return snaps
}

func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
