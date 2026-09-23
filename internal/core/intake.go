package core

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrIntakeGone = errors.New("intake session ended before the maintainer confirmed a command")

type Intake struct {
	Host     SessionHost
	Prompts  Prompts
	Provider ProviderArgs
	Name     string
	Root     string
	Pane     string
	Label    string
	Vars     map[string]any
	Poll     time.Duration
}

func (in *Intake) Run(ctx context.Context, accepted <-chan []string) ([]string, error) {
	pane, workspace, err := in.open()
	if err != nil {
		return nil, err
	}
	argv, err := in.converse(ctx, pane, accepted)
	var cerr error
	if workspace != "" {
		cerr = in.Host.Close(workspace)
	} else {
		cerr = in.Host.ClosePane(pane)
	}
	if err != nil {
		return nil, err
	}
	if cerr != nil {
		return nil, fmt.Errorf("close intake: %w", cerr)
	}
	return argv, nil
}

func (in *Intake) open() (string, string, error) {
	if in.Pane != "" {
		pane, err := in.Host.Split(in.Pane, "right", in.Root, nil)
		if err != nil {
			return "", "", fmt.Errorf("split: %w", err)
		}
		return pane, "", nil
	}
	ws, err := in.Host.Open(OpenSpec{CWD: in.Root, Label: "◆ " + labelPrefix(in.Label) + "intake"})
	if err != nil {
		return "", "", fmt.Errorf("open workspace: %w", err)
	}
	return ws.RootPane, ws.ID, nil
}

func (in *Intake) converse(ctx context.Context, pane string, accepted <-chan []string) ([]string, error) {
	if _, err := in.Host.Start(pane, in.Name, in.Provider.Kind, in.Provider.Args); err != nil {
		return nil, fmt.Errorf("start %s: %w", in.Name, err)
	}
	text, _, err := in.Prompts.Render("intake", in.Vars)
	if err != nil {
		return nil, fmt.Errorf("render intake: %w", err)
	}
	if err := in.Host.Prompt(in.Name, text, false, 0); err != nil {
		return nil, fmt.Errorf("prompt %s: %w", in.Name, err)
	}
	tick := time.NewTicker(in.Poll)
	defer tick.Stop()
	for {
		select {
		case argv := <-accepted:
			return argv, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-tick.C:
			if st, err := in.Host.State(in.Name); err == nil && st == AgentGone {
				return nil, ErrIntakeGone
			}
		}
	}
}
