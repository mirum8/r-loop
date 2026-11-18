package tray

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"r-loop/internal/core"
)

func Dir(home string) string {
	return filepath.Join(home, ".local", "state", "r-loop")
}

func runsDir(dir string) string {
	return filepath.Join(dir, "runs")
}

type Beacon struct {
	Dir   string
	Repo  string
	Now   func() time.Time
	Focus func()

	mu     sync.Mutex
	snap   Snapshot
	active bool
	quit   chan struct{}
}

func (b *Beacon) id() string {
	sum := sha1.Sum([]byte(b.Repo))
	return hex.EncodeToString(sum[:6])
}

func (b *Beacon) path() string {
	return filepath.Join(runsDir(b.Dir), b.id()+".json")
}

func requestPath(dir, id string) string {
	return filepath.Join(runsDir(dir), id+".focus")
}

func (b *Beacon) answer(quit chan struct{}) {
	req := requestPath(b.Dir, b.id())
	for {
		select {
		case <-quit:
			return
		case <-time.After(300 * time.Millisecond):
		}
		if os.Remove(req) == nil {
			b.Focus()
		}
	}
}

func (b *Beacon) Start(run string, pid int, past []core.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.snap = Snapshot{Repo: b.Repo, Run: run, PID: pid, State: Running}
	for _, ev := range past {
		if ev.Kind == "run-list" || ev.Kind == "landed" {
			b.snap, _ = next(b.snap, ev)
		}
	}
	b.active = true
	b.write()
	if b.Focus != nil {
		_ = os.Remove(requestPath(b.Dir, b.id()))
		b.quit = make(chan struct{})
		go b.answer(b.quit)
	}
}

func (b *Beacon) Emit(ev core.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.active {
		return
	}
	n, changed := next(b.snap, ev)
	if changed {
		b.snap = n
		b.write()
	}
}

func (b *Beacon) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.active {
		return
	}
	b.active = false
	if b.quit != nil {
		close(b.quit)
		b.quit = nil
	}
	data, err := os.ReadFile(b.path())
	if err != nil {
		return
	}
	var on Snapshot
	if json.Unmarshal(data, &on) == nil && on.Run == b.snap.Run && on.PID == b.snap.PID {
		_ = os.Remove(b.path())
	}
}

func (b *Beacon) write() {
	b.snap.At = b.Now()
	data, err := json.Marshal(b.snap)
	if err != nil {
		return
	}
	dir := runsDir(b.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, ".beacon-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), b.path()) != nil {
		_ = os.Remove(tmp.Name())
	}
}
