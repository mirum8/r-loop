package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Migration struct {
	Changes, Unresolved []string
}

func Migrate(path string) (Migration, error) {
	var m Migration
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return m, configError(fmt.Sprintf("%s: %v", path, err))
	}
	if len(doc.Content) == 0 {
		return m, nil
	}
	byPath, byProvider, err := defaultRoles()
	if err != nil {
		return m, err
	}
	builtIn := func(where, provider string) (Fallback, bool) {
		fb, ok := byPath[where]
		if !ok || fb.Provider != provider {
			fb, ok = byProvider[provider]
		}
		if !ok {
			m.Unresolved = append(m.Unresolved, fmt.Sprintf("%s: %s has no built-in model and effort, set them by hand", where, provider))
		}
		return fb, ok
	}
	fill := func(where string, n *yaml.Node) {
		switch {
		case n.Kind == yaml.ScalarNode && !isNull(n) && n.Value != "":
			fb, ok := builtIn(where, n.Value)
			if !ok {
				return
			}
			m.Changes = append(m.Changes, fmt.Sprintf("%s: %s → %s %s %s", where, n.Value, fb.Provider, fb.Model, fb.Effort))
			*n = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
				scalar("provider"), scalar(fb.Provider), scalar("model"), scalar(fb.Model), scalar("effort"), scalar(fb.Effort),
			}, HeadComment: n.HeadComment, LineComment: n.LineComment, FootComment: n.FootComment}
		case n.Kind == yaml.MappingNode:
			provider := child(n, "provider")
			if provider == nil || provider.Value == "" {
				return
			}
			var missing []string
			for _, key := range []string{"model", "effort"} {
				if v := child(n, key); v == nil || v.Value == "" {
					missing = append(missing, key)
				}
			}
			if len(missing) == 0 {
				return
			}
			fb, ok := builtIn(where, provider.Value)
			if !ok {
				return
			}
			for _, key := range missing {
				value := map[string]string{"model": fb.Model, "effort": fb.Effort}[key]
				if v := child(n, key); v != nil {
					*v = *scalar(value)
				} else {
					n.Content = append(n.Content, scalar(key), scalar(value))
				}
				m.Changes = append(m.Changes, fmt.Sprintf("%s.%s: unset → %s", where, key, value))
			}
		}
	}
	forEachRole(doc.Content[0], fill)
	if len(m.Changes) == 0 {
		return m, nil
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return m, err
	}
	if err := enc.Close(); err != nil {
		return m, err
	}
	if _, err := os.Stat(path + ".bak"); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(path+".bak", data, 0o644); err != nil {
			return m, err
		}
	}
	return m, os.WriteFile(path, out.Bytes(), 0o644)
}

func scalar(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}

func child(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func forEachRole(root *yaml.Node, visit func(path string, n *yaml.Node)) {
	steps := child(root, "steps")
	if steps == nil || steps.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(steps.Content); i += 2 {
		p := "steps." + steps.Content[i].Value
		row := steps.Content[i+1]
		if fb := child(row, "fallback"); fb != nil {
			visit(p+".fallback", fb)
		}
		if rvs := child(row, "reviewers"); rvs != nil && rvs.Kind == yaml.SequenceNode {
			for j, rv := range rvs.Content {
				visit(p+".reviewers."+strconv.Itoa(j), rv)
			}
		}
	}
}

func defaultRoles() (byPath, byProvider map[string]Fallback, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(defaultsYAML, &doc); err != nil {
		return nil, nil, err
	}
	byPath, byProvider = map[string]Fallback{}, map[string]Fallback{}
	forEachRole(doc.Content[0], func(path string, n *yaml.Node) {
		rv, err := reviewer(defaultSource, path, n, true)
		if err != nil || rv.Name != "" {
			return
		}
		fb := Fallback{rv.Provider, rv.Model, rv.Effort}
		byPath[path] = fb
		if _, ok := byProvider[fb.Provider]; !ok {
			byProvider[fb.Provider] = fb
		}
	})
	return byPath, byProvider, nil
}
