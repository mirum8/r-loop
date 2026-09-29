package config

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

var roleKeys = []string{"provider", "model", "effort"}

func ResetModels(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, configError(fmt.Sprintf("%s: %v", path, err))
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil
	}
	var defaults yaml.Node
	if err := yaml.Unmarshal(defaultsYAML, &defaults); err != nil {
		return nil, err
	}
	root, def := doc.Content[0], defaults.Content[0]
	var changes []string
	resetRow := func(where string, row, defRow *yaml.Node) {
		if row == nil || row.Kind != yaml.MappingNode {
			return
		}
		if slices.ContainsFunc(roleKeys, func(k string) bool { return child(row, k) != nil }) {
			before, after := roleText(row), roleText(defRow)
			if before != after {
				for _, k := range roleKeys {
					setKey(row, k, scalar(child(defRow, k).Value))
				}
				changes = append(changes, fmt.Sprintf("%s: %s → %s", where, before, after))
			}
		}
		for _, key := range []string{"fallback", "reviewers"} {
			got := child(row, key)
			if got == nil {
				continue
			}
			want := child(defRow, key)
			switch {
			case want == nil:
				deleteKey(row, key)
				changes = append(changes, fmt.Sprintf("%s.%s: %s → removed", where, key, roleText(got)))
			case !sameNode(got, want):
				changes = append(changes, fmt.Sprintf("%s.%s: %s → %s", where, key, roleText(got), roleText(want)))
				*got = *want
			}
		}
	}
	if steps, defSteps := child(root, "steps"), child(def, "steps"); steps != nil && steps.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(steps.Content); i += 2 {
			name := steps.Content[i].Value
			if defRow := child(defSteps, name); defRow != nil {
				resetRow("steps."+name, steps.Content[i+1], defRow)
			}
		}
		pruneEmpty(steps)
	}
	for _, section := range []string{"watchdog", "intake"} {
		resetRow(section, child(root, section), child(def, section))
	}
	if land := child(root, "land"); land != nil {
		if fix := child(land, "fix"); fix != nil {
			deleteKey(land, "fix")
			changes = append(changes, fmt.Sprintf("land.fix: %s → removed", roleText(fix)))
		}
	}
	pruneEmpty(root)
	if len(changes) == 0 {
		return nil, nil
	}
	var out bytes.Buffer
	if len(root.Content) > 0 {
		enc := yaml.NewEncoder(&out)
		enc.SetIndent(2)
		if err := enc.Encode(&doc); err != nil {
			return nil, err
		}
		if err := enc.Close(); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(path+".bak", data, 0o644); err != nil {
		return nil, err
	}
	return changes, os.WriteFile(path, out.Bytes(), 0o644)
}

func roleText(n *yaml.Node) string {
	switch n.Kind {
	case yaml.SequenceNode:
		parts := make([]string, len(n.Content))
		for i, c := range n.Content {
			parts[i] = roleText(c)
		}
		return strings.Join(parts, ", ")
	case yaml.MappingNode:
		var parts []string
		if name := child(n, "name"); name != nil && name.Value != "" {
			parts = append(parts, name.Value)
		}
		for _, k := range roleKeys {
			if v := child(n, k); v != nil && v.Value != "" {
				parts = append(parts, v.Value)
			}
		}
		return strings.Join(parts, " ")
	}
	return n.Value
}

func sameNode(a, b *yaml.Node) bool {
	var x, y any
	return a.Decode(&x) == nil && b.Decode(&y) == nil && reflect.DeepEqual(x, y)
}

func setKey(n *yaml.Node, key string, value *yaml.Node) {
	if v := child(n, key); v != nil {
		*v = *value
		return
	}
	n.Content = append(n.Content, scalar(key), value)
}

func deleteKey(n *yaml.Node, key string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content = slices.Delete(n.Content, i, i+2)
			return
		}
	}
}

func pruneEmpty(n *yaml.Node) {
	for i := 0; i+1 < len(n.Content); {
		if v := n.Content[i+1]; v.Kind == yaml.MappingNode && len(v.Content) == 0 {
			n.Content = slices.Delete(n.Content, i, i+2)
			continue
		}
		i += 2
	}
}
