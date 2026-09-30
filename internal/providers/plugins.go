package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var pluginSelector = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+$`)

type pluginCLI struct {
	list, install, enable []string
}

var pluginCLIs = map[string]pluginCLI{
	"codex":  {list: []string{"plugin", "list", "--json"}, install: []string{"plugin", "add"}},
	"claude": {list: []string{"plugin", "list", "--json"}, install: []string{"plugin", "install"}, enable: []string{"plugin", "enable"}},
}

func EnsurePlugin(p Provider, run func(name string, args ...string) ([]byte, error)) (installed bool, err error) {
	cli, ok := pluginCLIs[p.Kind]
	if !ok {
		return false, fmt.Errorf("provider %s: kind %s has no plugin installer", p.Name, p.Kind)
	}
	installed, enabled, err := pluginState(p, cli, run)
	if err != nil || enabled {
		return false, err
	}
	verb := cli.install
	if installed && cli.enable != nil {
		verb = cli.enable
	}
	args := append(append([]string{}, verb...), p.SecurityPlugin)
	cmd := p.Kind + " " + strings.Join(args, " ")
	if _, err := run(p.Kind, args...); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return false, fmt.Errorf("%s: %w: %s", cmd, err, strings.TrimSpace(string(exit.Stderr)))
		}
		return false, fmt.Errorf("%s: %w", cmd, err)
	}
	if _, enabled, err = pluginState(p, cli, run); err != nil {
		return false, err
	}
	if !enabled {
		return false, fmt.Errorf("%s ran, but %s is still not installed and enabled", cmd, p.SecurityPlugin)
	}
	return true, nil
}

func pluginState(p Provider, cli pluginCLI, run func(name string, args ...string) ([]byte, error)) (installed, enabled bool, err error) {
	cmd := p.Kind + " " + strings.Join(cli.list, " ")
	out, err := run(p.Kind, cli.list...)
	if err != nil {
		return false, false, fmt.Errorf("%s: %w", cmd, err)
	}
	var plugins []struct {
		ID       string `json:"id"`
		PluginID string `json:"pluginId"`
		Enabled  bool   `json:"enabled"`
	}
	if p.Kind == "codex" {
		var doc struct {
			Installed json.RawMessage `json:"installed"`
		}
		if err := json.Unmarshal(out, &doc); err != nil {
			return false, false, fmt.Errorf("%s: %w", cmd, err)
		}
		out = doc.Installed
	}
	if len(out) > 0 {
		if err := json.Unmarshal(out, &plugins); err != nil {
			return false, false, fmt.Errorf("%s: %w", cmd, err)
		}
	}
	for _, pl := range plugins {
		if pl.ID == p.SecurityPlugin || pl.PluginID == p.SecurityPlugin {
			return true, pl.Enabled, nil
		}
	}
	return false, false, nil
}
