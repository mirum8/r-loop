package rollout

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Finder struct{ Home string }

type meta struct {
	Type    string `json:"type"`
	Payload struct {
		ID        string          `json:"id"`
		Timestamp time.Time       `json:"timestamp"`
		Cwd       string          `json:"cwd"`
		Source    json.RawMessage `json:"source"`
	} `json:"payload"`
}

type item struct {
	Type    string `json:"type"`
	Payload struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"payload"`
}

func (f Finder) Find(dir, marker string, since time.Time) (string, string) {
	sessions := filepath.Join(f.Home, "sessions")
	from := since.UTC().AddDate(0, 0, -1).Format("2006/01/02")
	for _, day := range subdirs(sessions, 3) {
		rel, err := filepath.Rel(sessions, day)
		if err != nil || filepath.ToSlash(rel) < from {
			continue
		}
		entries, _ := os.ReadDir(day)
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			file := filepath.Join(day, name)
			if id, ok := match(file, dir, marker, since); ok {
				return file, id
			}
		}
	}
	return "", ""
}

func subdirs(root string, depth int) []string {
	if depth == 0 {
		return []string{root}
	}
	entries, _ := os.ReadDir(root)
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, subdirs(filepath.Join(root, e.Name()), depth-1)...)
		}
	}
	return out
}

func match(path, dir, marker string, since time.Time) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	if !sc.Scan() {
		return "", false
	}
	var m meta
	var src string
	if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "session_meta" ||
		filepath.Clean(m.Payload.Cwd) != filepath.Clean(dir) ||
		m.Payload.Timestamp.Before(since.Truncate(time.Millisecond)) ||
		json.Unmarshal(m.Payload.Source, &src) != nil {
		return "", false
	}
	for sc.Scan() {
		var it item
		if json.Unmarshal(sc.Bytes(), &it) != nil || it.Type != "response_item" || it.Payload.Type != "message" || it.Payload.Role != "user" {
			continue
		}
		for _, c := range it.Payload.Content {
			if strings.Contains(c.Text, marker) {
				return m.Payload.ID, true
			}
		}
	}
	return "", false
}
