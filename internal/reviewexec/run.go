package reviewexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

const maxLine = 500

type Runner struct{}

func New() Runner { return Runner{} }

func (Runner) RunReview(parent context.Context, dir, command string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		err = nil
	}
	if err == nil {
		return out.String(), nil
	}
	switch {
	case parent.Err() != nil:
		err = fmt.Errorf("interrupted: %w", parent.Err())
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("timed out after %s: %w", timeout, context.DeadlineExceeded)
	}
	if last := lastLine(errOut.Bytes()); last != "" {
		err = fmt.Errorf("%w: %s", err, last)
	}
	return out.String(), err
}

func lastLine(output []byte) string {
	output = bytes.TrimSpace(output)
	if i := bytes.LastIndexByte(output, '\n'); i >= 0 {
		output = output[i+1:]
	}
	return string(bytes.TrimSpace(output[max(0, len(output)-maxLine):]))
}
