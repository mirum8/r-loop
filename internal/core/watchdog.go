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
	Pane, Label                            string
	Allow                                  []string
	Unattended                             bool
	Sleep                                  func(time.Duration)
	OnGone                                 func()

	mu        sync.Mutex
	send      sync.Mutex
	wait      sync.Mutex
	cond      *sync.Cond
	quit      chan struct{}
	drained   chan struct{}
	queue     []string
	stopping  bool
	gone      bool
	asking    bool
	blocked   bool
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
	text, _, err := d.Prompts.Render("watchdog", map[string]any{"TodoPath": d.TodoPath, "SpecDir": d.SpecDir, "RunDir": d.RunDir, "Allow": d.Allow, "Unattended": d.Unattended})
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
		pane, err := d.Host.Split(d.Pane, "right", d.Root, nil)
		if err != nil {
			return "", "", fmt.Errorf("split: %w", err)
		}
		return pane, "", nil
	}
	ws, err := d.Host.Open(OpenSpec{CWD: d.Root, Label: "◆ " + labelPrefix(d.Label) + "watchdog"})
	if err != nil {
		return "", "", fmt.Errorf("open workspace: %w", err)
	}
	if err := d.Host.Tag(ws.ID, map[string]string{"rloop": "◆ run " + d.RunID}); err != nil {
		if err := d.record("warning", map[string]string{"reason": "tag watchdog workspace: " + err.Error()}); err != nil {
			return "", "", err
		}
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

func (d *Watchdog) Post(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gone || d.stopping {
		return
	}
	if d.drained == nil {
		d.lazy()
		d.drained = make(chan struct{})
		go d.deliver()
	}
	d.queue = append(d.queue, text)
	d.cond.Signal()
}

func (d *Watchdog) deliver() {
	defer close(d.drained)
	for {
		d.mu.Lock()
		for len(d.queue) == 0 && !d.stopping {
			d.cond.Wait()
		}
		empty := len(d.queue) == 0
		d.mu.Unlock()
		if empty {
			return
		}
		d.send.Lock()
		d.flush()
		d.send.Unlock()
	}
}

func (d *Watchdog) flush() {
	for {
		d.mu.Lock()
		if len(d.queue) == 0 {
			d.mu.Unlock()
			return
		}
		text := d.queue[0]
		d.queue = d.queue[1:]
		d.mu.Unlock()
		d.prompt(text, false, 0)
	}
}

func (d *Watchdog) Notify(text string, wait bool, timeout time.Duration) error {
	d.send.Lock()
	defer d.send.Unlock()
	d.flush()
	return d.prompt(text, wait, timeout)
}

func (d *Watchdog) prompt(text string, wait bool, timeout time.Duration) error {
	if !d.live() {
		return nil
	}
	err := d.Host.Prompt(d.agent(), text, wait, timeout)
	for blocked(err) {
		state, serr := d.Host.State(d.agent())
		if serr != nil {
			return serr
		}
		if state == AgentGone {
			break
		}
		if werr := d.markWaiting(nil, true); werr != nil {
			return werr
		}
		if !d.pause() {
			return err
		}
		err = d.Host.Prompt(d.agent(), text, wait, timeout)
	}
	if !blocked(err) {
		if err == nil {
			if werr := d.markResumed(true); werr != nil {
				return werr
			}
		}
		return err
	}
	if rerr := d.emit("watchdog-unreachable", map[string]string{"reason": err.Error()}, func() { d.gone = true }); rerr != nil {
		return rerr
	}
	if d.OnGone != nil {
		d.OnGone()
	}
	return err
}

func (d *Watchdog) AskMaintainer(question string, options []string, recommended string) error {
	fields := map[string]string{"question": question}
	if len(options) > 0 {
		fields["options"] = strings.Join(options, "; ")
	}
	if recommended != "" {
		fields["recommended"] = recommended
	}
	return d.markWaiting(fields, false)
}

func (d *Watchdog) Resume() error {
	return d.markResumed(false)
}

func (d *Watchdog) markWaiting(fields map[string]string, byPrompt bool) error {
	d.wait.Lock()
	defer d.wait.Unlock()
	d.mu.Lock()
	was := d.asking
	if was && byPrompt {
		d.blocked = true
	}
	d.mu.Unlock()
	if was {
		return nil
	}
	return d.emit("watchdog-waiting", fields, func() { d.asking, d.blocked = true, byPrompt })
}

func (d *Watchdog) markResumed(byPrompt bool) error {
	d.wait.Lock()
	defer d.wait.Unlock()
	d.mu.Lock()
	was := d.asking && (d.blocked || !byPrompt)
	d.mu.Unlock()
	if !was {
		return nil
	}
	return d.emit("watchdog-resumed", nil, func() { d.asking, d.blocked = false, false })
}

func (d *Watchdog) emit(kind string, fields map[string]string, apply func()) error {
	ev := Event{At: time.Now(), Kind: kind, Fields: fields}
	if err := d.Store.Append(d.RunID, Record{Kind: RecordEvent, At: ev.At, Event: &ev}); err != nil {
		return fmt.Errorf("record %s: %w", kind, err)
	}
	d.mu.Lock()
	apply()
	d.mu.Unlock()
	if d.Face != nil {
		d.Face.Emit(ev)
	}
	return nil
}

func (d *Watchdog) pause() bool {
	d.mu.Lock()
	d.lazy()
	quit, stopping := d.quit, d.stopping
	d.mu.Unlock()
	if stopping {
		return false
	}
	if d.Sleep != nil {
		d.Sleep(watchdogRetryAfter)
	} else {
		t := time.NewTimer(watchdogRetryAfter)
		select {
		case <-t.C:
		case <-quit:
			t.Stop()
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.stopping
}

func (d *Watchdog) lazy() {
	if d.cond == nil {
		d.cond = sync.NewCond(&d.mu)
		d.quit = make(chan struct{})
	}
}

func (d *Watchdog) live() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return !d.gone
}

func (d *Watchdog) Stop() error {
	d.mu.Lock()
	d.lazy()
	if !d.stopping {
		d.stopping = true
		close(d.quit)
		d.cond.Broadcast()
	}
	drained := d.drained
	d.mu.Unlock()
	if drained != nil {
		<-drained
	}
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
