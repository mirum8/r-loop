package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	EventConfigSet       = "config-set"
	EventConfigSetFailed = "config-set-failed"
	EventPlanCorrected   = "plan-corrected"
	ProjectConfig        = ".r-loop/config.yaml"
)

var lockedConfig = []string{"watchdog", "unattended"}

type ConfigSetter struct {
	Store Store
	Face  Face
	Repo  Repo
	RunID string
	Write func(key, value string) error
	mu    sync.Mutex
}

func (c *ConfigSetter) Set(key, value, why string) (string, string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return decisionRefused, "key is empty"
	}
	if strings.TrimSpace(why) == "" {
		return decisionRefused, "why is empty: say what the change fixes"
	}
	if section, _, _ := strings.Cut(key, "."); slices.Contains(lockedConfig, section) {
		return decisionRefused, section + ".* is not yours to set: ask the maintainer to edit " + ProjectConfig
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.record(Event{Kind: EventConfigSet, Fields: map[string]string{"key": key, "value": value, "why": why}}); err != nil {
		return decisionRefused, err.Error()
	}
	if err := c.Write(key, value); err != nil {
		_ = c.record(Event{Kind: EventConfigSetFailed, Fields: map[string]string{"key": key, "reason": err.Error()}})
		return decisionRefused, err.Error()
	}
	if err := c.commit(key); err != nil {
		reason := fmt.Sprintf("%s is set and loaded, but committing %s failed: %v", key, ProjectConfig, err)
		_ = c.record(Event{Kind: "warning", Fields: map[string]string{"reason": reason}})
		return decisionAuthorised, reason
	}
	return decisionAuthorised, ""
}

func (c *ConfigSetter) commit(key string) error {
	dirty, err := c.Repo.Dirty("")
	if err != nil {
		return err
	}
	if !slices.Contains(dirty, ProjectConfig) {
		return nil
	}
	_, err = c.Repo.Commit(context.Background(), Subject("chore(r-loop): ", "set "+key), ProjectConfig)
	return err
}

func (c *ConfigSetter) record(ev Event) error {
	ev.At = time.Now()
	if err := c.Store.Append(c.RunID, Record{Kind: RecordEvent, At: ev.At, Event: &ev}); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	c.Face.Emit(ev)
	return nil
}
