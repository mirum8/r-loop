package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	watchdogAgent      = "rloop-watchdog"
	watchdogRetryAfter = 30 * time.Second
)

type Watchdog struct {
	Host                                   SessionHost
	Prompts                                Prompts
	Store                                  Store
	Face                                   Face
	Provider                               ProviderArgs
	RunID, Root, TodoPath, SpecDir, RunDir string
	Allow                                  []string
	Sleep                                  func(time.Duration)

	mu   sync.Mutex
	gone bool
	pane string
}

func (d *Watchdog) Start(ctx context.Context) error {
	pane, err := d.Host.Split("", "right", d.Root)
	if err != nil {
		return fmt.Errorf("split: %w", err)
	}
	d.mu.Lock()
	d.pane = pane
	d.mu.Unlock()
	if _, err := d.Host.Start(pane, watchdogAgent, d.Provider.Kind, d.Provider.Args); err != nil {
		return fmt.Errorf("start %s: %w", watchdogAgent, err)
	}
	text, _, err := d.Prompts.Render("watchdog", map[string]any{"TodoPath": d.TodoPath, "SpecDir": d.SpecDir, "RunDir": d.RunDir, "Allow": d.Allow})
	if err != nil {
		return fmt.Errorf("render watchdog: %w", err)
	}
	if err := d.Host.Prompt(watchdogAgent, text, false, 0); err != nil {
		return fmt.Errorf("prompt %s: %w", watchdogAgent, err)
	}
	return nil
}

func (d *Watchdog) Notify(text string, wait bool, timeout time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gone {
		return nil
	}
	err := d.Host.Prompt(watchdogAgent, text, wait, timeout)
	if !blocked(err) {
		return err
	}
	d.sleep(watchdogRetryAfter)
	if err = d.Host.Prompt(watchdogAgent, text, wait, timeout); !blocked(err) {
		return err
	}
	ev := Event{At: time.Now(), Kind: "watchdog-unreachable", Fields: map[string]string{"reason": err.Error()}}
	if rerr := d.Store.Append(d.RunID, Record{Kind: RecordEvent, At: ev.At, Event: &ev}); rerr != nil {
		return fmt.Errorf("record watchdog-unreachable: %w", rerr)
	}
	d.gone = true
	if d.Face != nil {
		d.Face.Emit(ev)
	}
	return err
}

func (d *Watchdog) live() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.gone
}

func (d *Watchdog) Stop() error {
	d.mu.Lock()
	pane := d.pane
	d.pane, d.gone = "", true
	d.mu.Unlock()
	if pane == "" {
		return nil
	}
	return d.Host.ClosePane(pane)
}

func (d *Watchdog) sleep(t time.Duration) {
	if d.Sleep == nil {
		time.Sleep(t)
		return
	}
	d.Sleep(t)
}

func blocked(err error) bool {
	return err != nil && strings.Contains(err.Error(), "agent_blocked")
}
