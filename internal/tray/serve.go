package tray

import (
	"os"
	"path/filepath"
	"syscall"
	"time"
)

var (
	mainLoop bool
	startCh  = make(chan string, 1)
	held     *os.File
)

func Serve(dir string) (stop func()) {
	if !mainLoop {
		return func() {}
	}
	quit := make(chan struct{})
	go func() {
		for {
			if f := tryLock(dir); f != nil {
				held = f
				startCh <- dir
				return
			}
			select {
			case <-quit:
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	return func() { close(quit) }
}

func tryLock(dir string) *os.File {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "tray.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil
	}
	return f
}

func watch(dir string, show func(View)) {
	var last string
	for {
		v := Aggregate(Read(dir), Alive)
		if key := v.key(); key != last {
			last = key
			show(v)
		}
		time.Sleep(2 * time.Second)
	}
}

func (v View) key() string {
	k := string(v.State)
	for _, r := range v.Rows {
		k += "\n" + r.Repo + "\t" + r.Label
	}
	return k
}
