package core

import (
	"context"
	"errors"
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
	Allow, Dialogs                         []string
	Unattended                             bool
	Sleep                                  func(time.Duration)
	OnGone                                 func()

	mu           sync.Mutex
	send         sync.Mutex
	wait         sync.Mutex
	cond         *sync.Cond
	quit         chan struct{}
	drained      chan struct{}
	queue        []string
	stopping     bool
	dropQueue    bool
	gone         bool
	onGoneCalled bool
	asking       bool
	blocked      bool
	pane         string
	workspace    string
	notifyCancel context.CancelFunc
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
	text, _, err := d.Prompts.Render("watchdog", map[string]any{"TodoPath": d.TodoPath, "SpecDir": d.SpecDir, "RunDir": d.RunDir, "Allow": d.Allow, "Dialogs": d.Dialogs, "Unattended": d.Unattended})
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
	defer func() {
		if r := recover(); r != nil {
			ev, err := panicked("", "", "watchdog delivery", r)
			var rerr error
			if qerr := quietly(func() { rerr = d.emit(ev.Kind, ev.Fields, func() {}) }); qerr != nil {
				rerr = qerr
			}
			d.mu.Lock()
			active, gone := !d.stopping, d.gone
			d.mu.Unlock()
			if active && !gone {
				var eerr error
				qerr := quietly(func() {
					eerr = d.emit("watchdog-unreachable", map[string]string{"reason": err.Error()}, func() { d.gone = true })
				})
				rerr = errors.Join(rerr, eerr, qerr)
				if eerr != nil || qerr != nil {
					d.mu.Lock()
					d.gone = true
					d.mu.Unlock()
				}
			}
			if rerr != nil && d.Face != nil {
				quietly(func() {
					d.Face.Emit(Event{At: time.Now(), Kind: "warning", Fields: map[string]string{"reason": "watchdog: " + rerr.Error()}})
				})
			}
			if active {
				quietly(d.callOnGone)
			}
		}
	}()
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
		func() {
			d.send.Lock()
			defer d.send.Unlock()
			d.flush()
		}()
	}
}

func (d *Watchdog) flush() {
	for {
		d.mu.Lock()
		if len(d.queue) == 0 || d.dropQueue {
			if d.dropQueue {
				d.queue = nil
			}
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

func (d *Watchdog) NotifyContext(ctx context.Context, text string, wait bool, timeout time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	d.mu.Lock()
	if d.stopping {
		d.mu.Unlock()
		cancel()
		return context.Canceled
	}
	d.notifyCancel = cancel
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		d.notifyCancel = nil
		d.mu.Unlock()
		cancel()
	}()
	d.send.Lock()
	defer func() {
		if ctx.Err() != nil {
			d.mu.Lock()
			d.dropQueue = true
			d.queue = nil
			d.mu.Unlock()
		}
		d.send.Unlock()
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	d.flush()
	if err := ctx.Err(); err != nil {
		return err
	}
	if host, ok := d.Host.(interface {
		PromptContext(context.Context, string, string, bool, time.Duration) error
	}); ok {
		return d.promptWith(ctx, text, wait, timeout, func() error {
			return host.PromptContext(ctx, d.agent(), text, wait, timeout)
		})
	}
	return d.promptWith(ctx, text, wait, timeout, func() error {
		return d.Host.Prompt(d.agent(), text, wait, timeout)
	})
}

func (d *Watchdog) prompt(text string, wait bool, timeout time.Duration) error {
	return d.promptWith(context.Background(), text, wait, timeout, func() error {
		return d.Host.Prompt(d.agent(), text, wait, timeout)
	})
}

func (d *Watchdog) promptWith(ctx context.Context, text string, wait bool, timeout time.Duration, call func() error) error {
	if !d.live() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := call()
	for blocked(err) {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
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
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		err = call()
	}
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	if !blocked(err) {
		if err == nil {
			return d.markResumed(true)
		}
		if state, serr := d.Host.State(d.agent()); serr != nil || state != AgentGone {
			return err
		}
	}
	if lerr := d.lost(err.Error()); lerr != nil {
		return lerr
	}
	return err
}

func (d *Watchdog) lost(reason string) error {
	marked, err := func() (bool, error) {
		d.wait.Lock()
		defer d.wait.Unlock()
		d.mu.Lock()
		skip := d.gone || d.stopping
		d.mu.Unlock()
		if skip {
			return false, nil
		}
		return true, d.emit("watchdog-unreachable", map[string]string{"reason": reason}, func() { d.gone = true })
	}()
	if !marked || err != nil {
		return err
	}
	d.callOnGone()
	return nil
}

func (d *Watchdog) callOnGone() {
	d.mu.Lock()
	if d.onGoneCalled {
		d.mu.Unlock()
		return
	}
	d.onGoneCalled = true
	fn := d.OnGone
	d.mu.Unlock()
	if fn != nil {
		fn()
	}
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
	if err := d.emit("watchdog-waiting", fields, func() { d.asking, d.blocked = true, byPrompt }); err != nil {
		return err
	}
	host, ok := d.Host.(interface{ Focus(string) error })
	if !ok {
		return nil
	}
	if err := host.Focus(d.agent()); err != nil {
		return d.emit("warning", map[string]string{"reason": "focus watchdog: " + err.Error()}, func() {})
	}
	return nil
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

func (d *Watchdog) Waiting() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.asking
}

func (d *Watchdog) Gone() bool {
	return !d.live()
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
		if d.notifyCancel != nil {
			d.dropQueue = true
			d.notifyCancel()
			d.queue = nil
		}
		d.cond.Broadcast()
	}
	drained := d.drained
	_, cancellable := d.Host.(interface {
		PromptContext(context.Context, string, string, bool, time.Duration) error
	})
	canWait := d.notifyCancel == nil || cancellable
	d.mu.Unlock()
	if drained != nil && canWait {
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
