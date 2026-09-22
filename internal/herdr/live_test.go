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

	pane, err := c.Split(ws.RootPane, "right", dir)
	if err != nil || pane == "" || pane == ws.RootPane {
		t.Fatalf("Split: %q, %v", pane, err)
	}

	extra, err := c.Split(ws.RootPane, "down", dir)
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
	deadline := time.Now().Add(15 * time.Second)
	for {
		s, err := c.State(first)
		if err != nil {
			t.Fatalf("State: %v", err)
		}
		if s == core.AgentGone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first agent still %q after interrupt", s)
		}
		time.Sleep(250 * time.Millisecond)
	}
	startTolerant(t, c, pane, "rl-live-b-"+suffix)

	if s, err := c.State("rl-live-missing-" + suffix); err != nil || s != core.AgentGone {
		t.Fatalf("State of missing agent: %q, %v", s, err)
	}

	if err := c.Close(ws.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
