package tray

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func Reveal(dir string, r Row) {
	req := requestPath(dir, r.ID)
	if os.WriteFile(req, nil, 0o644) != nil {
		_ = exec.Command("open", r.Repo).Start()
		return
	}
	go func() {
		time.Sleep(3 * time.Second)
		if os.Remove(req) == nil {
			_ = exec.Command("open", r.Repo).Start()
		}
	}()
}

func Activate(pid int) {
	if app := appOf(pid, parent, command); app != "" {
		_ = exec.Command("open", "-a", app).Run()
		return
	}
	if id := os.Getenv("__CFBundleIdentifier"); id != "" {
		_ = exec.Command("open", "-b", id).Run()
	}
}

func appOf(pid int, parent func(int) int, command func(int) string) string {
	for range 64 {
		if pid <= 1 {
			return ""
		}
		c := command(pid)
		if i := strings.Index(c, ".app/Contents/MacOS/"); i >= 0 {
			return c[:i+len(".app")]
		}
		pid = parent(pid)
	}
	return ""
}

func parent(pid int) int {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

func command(pid int) string {
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
