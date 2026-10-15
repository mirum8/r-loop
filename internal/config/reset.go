package config

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
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
			} else {
				changes = append(changes, moveUIReviewers("steps."+name, steps.Content[i+1], child(defSteps, "implement"))...)
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
	changes = append(changes, dropFixer(root)...)
	changes = append(changes, addAllow(child(root, "watchdog"))...)
	changes = append(changes, fillDialogs(child(root, "watchdog"), child(def, "watchdog"))...)
	changes = append(changes, raiseRestarts(child(root, "watchdog"), child(def, "watchdog"))...)
	changes = append(changes, addDesign(root)...)
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

func dropFixer(root *yaml.Node) []string {
	watchdog := child(root, "watchdog")
	fixer := child(watchdog, "fixer")
	if fixer == nil {
		return nil
	}
	deleteKey(watchdog, "fixer")
	return []string{fmt.Sprintf("watchdog.fixer: %s → removed", roleText(fixer))}
}

func addAllow(watchdog *yaml.Node) []string {
	allow := child(watchdog, "allow")
	if allow == nil || (!isNull(allow) && allow.Kind != yaml.SequenceNode) {
		return nil
	}
	before := "none"
	if isNull(allow) {
		*allow = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq",
			HeadComment: allow.HeadComment, LineComment: allow.LineComment, FootComment: allow.FootComment}
	} else if len(allow.Content) > 0 {
		before = roleText(allow)
	}
	added := false
	for _, class := range remedyClasses {
		if !slices.ContainsFunc(allow.Content, func(n *yaml.Node) bool { return n.Value == class }) {
			allow.Content = append(allow.Content, scalar(class))
			added = true
		}
	}
	if !added {
		return nil
	}
	return []string{fmt.Sprintf("watchdog.allow: %s → %s", before, roleText(allow))}
}

func fillDialogs(watchdog, def *yaml.Node) []string {
	dialogs := child(watchdog, "dialogs")
	if dialogs == nil || !(isNull(dialogs) || (dialogs.Kind == yaml.SequenceNode && len(dialogs.Content) == 0)) {
		return nil
	}
	want := child(def, "dialogs")
	*dialogs = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: want.Content,
		HeadComment: dialogs.HeadComment, LineComment: dialogs.LineComment, FootComment: dialogs.FootComment}
	return []string{fmt.Sprintf("watchdog.dialogs: none → %d default rules", len(want.Content))}
}

func raiseRestarts(watchdog, def *yaml.Node) []string {
	got := child(watchdog, "maxRestarts")
	if got == nil || got.Kind != yaml.ScalarNode {
		return nil
	}
	want := child(def, "maxRestarts")
	n, err := strconv.Atoi(got.Value)
	floor, _ := strconv.Atoi(want.Value)
	if err != nil || n >= floor {
		return nil
	}
	before := got.Value
	got.Value = want.Value
	return []string{fmt.Sprintf("watchdog.maxRestarts: %s → %s", before, want.Value)}
}

func moveUIReviewers(where string, row, defImplement *yaml.Node) []string {
	reviewers := child(row, "reviewers")
	if reviewers == nil || reviewers.Kind != yaml.SequenceNode {
		return nil
	}
	var ui *yaml.Node
	for _, rv := range child(defImplement, "reviewers").Content {
		if p := child(rv, "prompt"); p != nil && p.Value == "review-ui" {
			ui = rv
		}
	}
	before := roleText(reviewers)
	moved := false
	for _, rv := range reviewers.Content {
		if rv.Kind != yaml.MappingNode {
			continue
		}
		prompt, provider := child(rv, "prompt"), child(rv, "provider")
		if prompt == nil || prompt.Value != "review-ui" || provider == nil || provider.Value != "codex" {
			continue
		}
		for _, k := range roleKeys {
			setKey(rv, k, scalar(child(ui, k).Value))
		}
		moved = true
	}
	if !moved {
		return nil
	}
	return []string{fmt.Sprintf("%s.reviewers: %s → %s", where, before, roleText(reviewers))}
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
