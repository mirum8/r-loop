package hc

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
)

func TestLiveHC(t *testing.T) {
	if os.Getenv("HC_TERMINAL_ID") == "" {
		t.Skip("set HC_TERMINAL_ID (run inside herdr-app) to run against the installed hc")
	}
	c := Client{HC: "hc", App: "herdr-app"}
	if err := c.Reachable(); err != nil {
		t.Fatalf("Reachable: %v", err)
	}
	dir := t.TempDir()
	suffix := strconv.FormatInt(time.Now().UnixMilli()%1e8, 10)
	env := map[string]string{"R_LOOP_LIVE": "1"}

	ws, err := c.Open(core.OpenSpec{CWD: dir, Label: "r-loop-live-" + suffix, Env: env})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(ws.ID) })
	if ws.ID == "" || ws.RootPane == "" {
		t.Fatalf("workspace %+v", ws)
	}

	pane, err := c.Split(ws.RootPane, "right", dir, 0, env)
	if err != nil || pane == "" {
		t.Fatalf("Split: %q, %v", pane, err)
	}

	name := "rloop-live-" + suffix
	agent, err := c.Start(pane, name, "sh", []string{"-c", "printf '\\033]7501;state=idle:app=rloop-live\\007'; echo rloop-live-ready; exec cat"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if agent != (core.Agent{Name: name, Pane: pane}) {
		t.Fatalf("agent %+v", agent)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		screen, err := c.Screen(name)
		if err == nil && strings.Contains(screen, "rloop-live-ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Screen never showed the agent: %q, %v", screen, err)
		}
		time.Sleep(250 * time.Millisecond)
	}

	if err := c.Tag(ws.ID, map[string]string{"rloop": "◆ live", "rloop_wait": ""}); err != nil {
		t.Fatalf("Tag: %v", err)
	}

	if err := c.Close(ws.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		st, err := c.State(name)
		if err == nil && st == core.AgentGone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("State after Close: %q, %v", st, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
