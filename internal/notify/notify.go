package notify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"syscall"
	"time"

	"r-loop/internal/core"
)

type Shell struct {
	Log     string
	Emit    func(core.Event)
	Timeout time.Duration
}

func (s *Shell) timeout() time.Duration {
	if s.Timeout <= 0 {
		return 60 * time.Second
	}
	return s.Timeout
}

func (s *Shell) Fire(hook string, env map[string]string) {
	if hook == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", hook)
	cmd.Env = os.Environ()
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err == nil || (errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState.Success()) {
		return
	}
	reason := err.Error()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		reason = "timed out after " + s.timeout().String()
	}
	now := time.Now()
	s.log(fmt.Sprintf("%s hook %q status %s: %s\n%s\n", now.Format(time.RFC3339), hook, env["R_LOOP_STATUS"], reason, out))
	if s.Emit != nil {
		s.Emit(core.Event{At: now, Kind: "notify-failed", Fields: map[string]string{"hook": hook, "status": env["R_LOOP_STATUS"], "reason": reason}})
	}
}

func (s *Shell) log(entry string) {
	if s.Log == "" {
		return
	}
	f, err := os.OpenFile(s.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(entry)
}
