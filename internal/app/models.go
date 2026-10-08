package app

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"r-loop/internal/providers"
)

type modelCache struct {
	mu    sync.Mutex
	seen  map[string]string
	lines []string
}

func (c *modelCache) resolve(p providers.Provider, model string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := p.Name + "\x00" + model
	if got, ok := c.seen[key]; ok {
		return got, nil
	}
	got, err := providers.ResolveModel(p, model, runCatalog)
	if err != nil {
		return "", err
	}
	if c.seen == nil {
		c.seen = map[string]string{}
	}
	c.seen[key] = got
	if got != model {
		c.lines = append(c.lines, fmt.Sprintf("model: %s %s → %s", p.Name, model, got))
	}
	return got, nil
}

func (c *modelCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = nil
}

func runCatalog(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

const versionTimeout = 10 * time.Second

func providerVersion(p providers.Provider) string {
	if p.Version == "" {
		return "-"
	}
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.Kind, strings.Fields(p.Version)...)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return "?"
	}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return "?"
}
