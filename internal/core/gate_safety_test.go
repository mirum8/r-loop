package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"r-loop/internal/core"
)

func TestGateProbeRefusesADirtyPrimaryTreeWithoutStartingASession(t *testing.T) {
	e := newLandEnv(t)
	p, host := e.probe("ok", "test -f a.txt")
	if err := os.WriteFile(filepath.Join(e.root, "notes.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.root, "a.txt"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := p.Command(context.Background(), phaseOne(""))
	if !errors.Is(err, core.ErrNoGate) || !errors.Is(err, core.ErrDirtyTree) || !strings.Contains(err.Error(), "notes.txt") || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("Command = %v", err)
	}
	if len(host.opened) != 0 || len(e.store.events("step")) != 0 {
		t.Fatal("probe started")
	}
	if readFile(t, filepath.Join(e.root, "notes.txt")) != "mine\n" || readFile(t, filepath.Join(e.root, "a.txt")) != "edited\n" {
		t.Fatal("files changed")
	}
}

func TestGateProbeRemovesAnUntrackedFileLeftByItsSession(t *testing.T) {
	e := newLandEnv(t)
	p, host := e.probe("ok", "test -f a.txt")
	file := filepath.Join(e.root, "probe-output.txt")
	host.hook = func() {
		if err := os.WriteFile(file, []byte("probe\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Command(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe output remains: %v", err)
	}
}

func TestGateProbeRemovesAnUntrackedFileLeftByItsCommand(t *testing.T) {
	e := newLandEnv(t)
	p, _ := e.probe("ok", "printf probe > gate-output.txt")
	if _, err := p.Command(context.Background(), phaseOne("")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(e.root, "gate-output.txt")
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gate output remains: %v", err)
	}
}

func TestGateProbeFailedSessionCanRestartAfterLeavingAnUntrackedFile(t *testing.T) {
	e := newLandEnv(t)
	p, host := e.probe("failed", "true")
	file := filepath.Join(e.root, "probe-output.txt")
	host.hook = func() {
		if err := os.WriteFile(file, []byte("probe\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Command(context.Background(), phaseOne("")); !errors.Is(err, core.ErrNoGate) {
		t.Fatalf("first Command = %v", err)
	}
	host.outcome = "ok"
	if _, err := p.Command(context.Background(), phaseOne("")); err != nil {
		t.Fatalf("restarted Command = %v", err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe output remains: %v", err)
	}
}
