package core

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"
)

const (
	watchdogPrefix     = "rloop-wd-"
	watchdogNameMax    = 32
	watchdogRetryAfter = 30 * time.Second
)

type Watchdog struct {
	Host                                   SessionHost
	Prompts                                Prompts
	Store                                  Store
	Face                                   Face
	Provider                               ProviderArgs
	RunID, Root, TodoPath, SpecDir, RunDir string
	Pane                                   string
	Allow                                  []string
	Sleep                                  func(time.Duration)

	mu        sync.Mutex
	gone      bool
	pane      string
	workspace string
}

func WatchdogName(runID string) string {
	id := []byte(strings.ToLower(runID))
	for i, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			id[i] = '-'
		}
	}
	if room := watchdogNameMax - len(watchdogPrefix); len(id) > room {
		h := fnv.New32a()
		h.Write([]byte(runID))
		sum := fmt.Sprintf("%08x", h.Sum32())
		id = append(id[:room-len(sum)-1], "-"+sum...)
	}
	return watchdogPrefix + string(id)
}

func (d *Watchdog) agent() string {
	return WatchdogName(d.RunID)
}

func (d *Watchdog) Start(ctx context.Context) error {
	name := d.agent()
	stale, err := d.Host.AgentPane(name)
	if err != nil {
		return fmt.Errorf("find %s: %w", name, err)
	}
	if stale != "" {
		if err := d.record("watchdog-stale-closed", map[string]string{"pane": stale}); err != nil {
			return err
		}
		if err := d.Host.ClosePane(stale); err != nil {
			return fmt.Errorf("close stale %s: %w", name, err)
		}
	}
	if err := d.record("watchdog-start", nil); err != nil {
		return err
	}
	pane, workspace, err := d.open()
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.pane, d.workspace = pane, workspace
	d.mu.Unlock()
	if _, err := d.Host.Start(pane, name, d.Provider.Kind, d.Provider.Args); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	text, _, err := d.Prompts.Render("watchdog", map[string]any{"TodoPath": d.TodoPath, "SpecDir": d.SpecDir, "RunDir": d.RunDir, "Allow": d.Allow})
	if err != nil {
		return fmt.Errorf("render watchdog: %w", err)
	}
	if err := d.Host.Prompt(name, text, false, 0); err != nil {
		return fmt.Errorf("prompt %s: %w", name, err)
	}
	return nil
}

func (d *Watchdog) open() (string, string, error) {
	if d.Pane != "" {
		pane, err := d.Host.Split(d.Pane, "right", d.Root)
		if err != nil {
			return "", "", fmt.Errorf("split: %w", err)
		}
		return pane, "", nil
	}
	ws, err := d.Host.Open(OpenSpec{CWD: d.Root, Label: d.agent()})
	if err != nil {
		return "", "", fmt.Errorf("open workspace: %w", err)
	}
	return ws.RootPane, ws.ID, nil
}

func (d *Watchdog) record(kind string, fields map[string]string) error {
	ev := Event{At: time.Now(), Kind: kind, Fields: fields}
	if err := d.Store.Append(d.RunID, Record{Kind: RecordEvent, At: ev.At, Event: &ev}); err != nil {
		return fmt.Errorf("record %s: %w", kind, err)
	}
	return nil
}

func (d *Watchdog) Notify(text string, wait bool, timeout time.Duration) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gone {
		return nil
	}
	err := d.Host.Prompt(d.agent(), text, wait, timeout)
	if !blocked(err) {
		return err
	}
	d.sleep(watchdogRetryAfter)
	if err = d.Host.Prompt(d.agent(), text, wait, timeout); !blocked(err) {
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
	pane, workspace := d.pane, d.workspace
	d.pane, d.workspace, d.gone = "", "", true
	d.mu.Unlock()
	switch {
	case workspace != "":
		return d.Host.Close(workspace)
	case pane != "":
		return d.Host.ClosePane(pane)
	}
	return nil
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
