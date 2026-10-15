package config

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func Set(data []byte, key, value string, below ...[]byte) ([]byte, error) {
	parts := strings.Split(key, ".")
	if slices.Contains(parts, "") {
		return nil, fmt.Errorf("key %q has an empty segment", key)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, configError(err.Error())
	}
	if len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	n := doc.Content[0]
	if isNull(n) {
		*n = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	if n.Kind != yaml.MappingNode {
		return nil, errors.New("the config document is not a mapping")
	}
	for i, part := range parts {
		where := strings.Join(parts[:i+1], ".")
		last := i == len(parts)-1
		var next *yaml.Node
		switch n.Kind {
		case yaml.MappingNode:
			next = child(n, part)
			if next == nil && !last && isIndex(parts[i+1]) {
				inherited, err := inheritedList(parts[:i+1], below)
				if err != nil {
					return nil, err
				}
				if inherited != nil {
					next = inherited
					n.Content = append(n.Content, scalar(part), next)
				}
			}
			if next == nil {
				next = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
				if last {
					next = &yaml.Node{Kind: yaml.ScalarNode}
				}
				n.Content = append(n.Content, scalar(part), next)
			}
		case yaml.SequenceNode:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(n.Content) {
				return nil, fmt.Errorf("%s: no item %s", strings.Join(parts[:i], "."), part)
			}
			next = n.Content[idx]
		default:
			return nil, fmt.Errorf("%s is not a section", strings.Join(parts[:i], "."))
		}
		if !last && isNull(next) {
			*next = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", HeadComment: next.HeadComment, LineComment: next.LineComment, FootComment: next.FootComment}
		}
		if last {
			if next.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("%s is not a scalar: set_config sets scalar values only", where)
			}
			next.Tag, next.Value, next.Style = "", value, 0
		}
		n = next
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func isIndex(part string) bool {
	_, err := strconv.Atoi(part)
	return err == nil
}

func inheritedList(path []string, below [][]byte) (*yaml.Node, error) {
	for _, data := range append(below, defaultsYAML) {
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, configError(err.Error())
		}
		if len(doc.Content) == 0 {
			continue
		}
		n := doc.Content[0]
		for _, part := range path {
			n = child(n, part)
		}
		if n == nil {
			continue
		}
		if n.Kind != yaml.SequenceNode {
			return nil, nil
		}
		return n, nil
	}
	return nil, nil
}
