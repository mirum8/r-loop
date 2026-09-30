package app

import (
	"fmt"
	"os/exec"
	"sync"

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

func runCatalog(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}
