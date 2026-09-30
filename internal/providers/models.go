package providers

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	alias     = regexp.MustCompile(`^[a-z]+$`)
	versioned = regexp.MustCompile(`^gpt-(\d+(?:\.\d+)*)-([a-z]+)$`)
)

func ResolveModel(p Provider, model string, run func(name string, args ...string) ([]byte, error)) (string, error) {
	if p.Models == "" || !alias.MatchString(model) {
		return model, nil
	}
	cmd := p.Kind + " " + p.Models
	out, err := run(p.Kind, strings.Fields(p.Models)...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", cmd, err)
	}
	var catalog struct {
		Models []struct{ Slug, Visibility string }
	}
	if err := json.Unmarshal(out, &catalog); err != nil {
		return "", fmt.Errorf("%s: %w", cmd, err)
	}
	best, bestVersion := "", []int(nil)
	for _, m := range catalog.Models {
		match := versioned.FindStringSubmatch(m.Slug)
		if m.Visibility != "list" || match == nil || match[2] != model {
			continue
		}
		if v := version(match[1]); best == "" || slices.Compare(v, bestVersion) > 0 {
			best, bestVersion = m.Slug, v
		}
	}
	if best == "" {
		return "", fmt.Errorf("%s has no model matching %q (%s)", p.Name, model, cmd)
	}
	return best, nil
}

func version(s string) []int {
	var v []int
	for _, part := range strings.Split(s, ".") {
		n, _ := strconv.Atoi(part)
		v = append(v, n)
	}
	return v
}
