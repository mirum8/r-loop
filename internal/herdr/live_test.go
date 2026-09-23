package herdr

import (
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"r-loop/internal/core"
)

func startTolerant(t *testing.T, c Client, pane, name string) {
	t.Helper()
	agent, err := c.Start(pane, name, "claude", nil)
	var herr Error
	if errors.As(err, &herr) && herr.Code == "agent_not_ready" {
		s, err := c.State(name)
		if err != nil || s != core.AgentBlocked {
			t.Fatalf("Start %s: not ready and state %q, %v", name, s, err)
		}
		out, err := c.Read(name, 20)
		if err != nil || out == "" {
			t.Fatalf("Read %s: %q, %v", name, out, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("Start %s: %v", name, err)
	}
	if agent != (core.Agent{Name: name, Pane: pane}) {
		t.Fatalf("agent %+v", agent)
	}
}

func TestLiveHerdr(t *testing.T) {
	if os.Getenv("R_LOOP_LIVE_HERDR") != "1" {
		t.Skip("set R_LOOP_LIVE_HERDR=1 to run against the installed herdr")
	}
	c := Client{Bin: "herdr"}
	if err := c.Reachable(); err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	dir := t.TempDir()
	suffix := strconv.FormatInt(time.Now().UnixMilli()%1e8, 10)

	ws, err := c.Open(core.OpenSpec{CWD: dir, Label: "r-loop-live-" + suffix, Env: map[string]string{"R_LOOP_LIVE": "1"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(ws.ID) })
	if ws.ID == "" || ws.RootPane == "" {
		t.Fatalf("workspace %+v", ws)
	}

	if err := c.Tag(ws.ID, map[string]string{"rloop": "◆ live", "rloop_wait": ""}); err != nil {
		t.Fatalf("Tag: %v", err)
	}

	runID := "run-" + suffix
	reviewerEnv := map[string]string{
		"R_LOOP_RUN":      runID,
		"R_LOOP_PHASE":    "3",
		"R_LOOP_STEP":     "implement",
		"R_LOOP_REVIEWER": "claude",
	}
	pane, err := c.Split(ws.RootPane, "right", dir, reviewerEnv)
	if err != nil || pane == "" || pane == ws.RootPane {
		t.Fatalf("Split: %q, %v", pane, err)
	}
	command := "echo split-env=$R_LOOP_RUN/$R_LOOP_PHASE/$R_LOOP_STEP/$R_LOOP_REVIEWER"
	want := "split-env=" + runID + "/3/implement/claude"
	for attempt := 0; attempt < 3; attempt++ {
		_, err := c.exec("pane", "run", pane, command)
		if err == nil {
			_, err = c.exec("pane", "wait-output", "--match", want, "--timeout", "3000", pane)
		}
		if err == nil {
			break
		}
		if attempt == 2 {
			t.Fatalf("split pane environment: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}

	extra, err := c.Split(ws.RootPane, "down", dir, nil)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if err := c.ClosePane(extra); err != nil {
		t.Fatalf("ClosePane: %v", err)
	}
	if err := c.ClosePane(extra); err == nil {
		t.Fatal("ClosePane of a closed pane succeeded")
	}

	first := "rl-live-a-" + suffix
	startTolerant(t, c, pane, first)
	if err := c.Interrupt(first); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	if err := c.ClosePane(pane); err != nil {
		t.Fatalf("ClosePane of first reviewer: %v", err)
	}
	nextPane, err := c.Split(ws.RootPane, "right", dir, reviewerEnv)
	if err != nil || nextPane == "" || nextPane == pane {
		t.Fatalf("Split fresh reviewer pane: %q, %v", nextPane, err)
	}
	startTolerant(t, c, nextPane, "rl-live-b-"+suffix)

	if s, err := c.State("rl-live-missing-" + suffix); err != nil || s != core.AgentGone {
		t.Fatalf("State of missing agent: %q, %v", s, err)
	}

	if err := c.Close(ws.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
