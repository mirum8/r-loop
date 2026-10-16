package memory

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"r-loop/internal/core"
)

const maxSlug = 48

type Dir struct {
	Path string
	Warn func(string)
}

func New(root string) *Dir {
	return &Dir{Path: filepath.Join(root, ".r-loop", "memory")}
}

func (d *Dir) List() ([]core.MemoryFact, error) {
	entries, err := os.ReadDir(d.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var facts []core.MemoryFact
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		path := filepath.Join(d.Path, e.Name())
		f, err := read(path)
		if err != nil {
			d.warn(fmt.Sprintf("memory: skip %s: %v", path, err))
			continue
		}
		facts = append(facts, f)
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].ID < facts[j].ID })
	return facts, nil
}

func (d *Dir) Write(f core.MemoryFact) (string, error) {
	if err := os.MkdirAll(d.Path, 0o755); err != nil {
		return "", err
	}
	base := f.At.Format("20060102") + "-" + slug(f.Description)
	id := base
	for n := 2; ; n++ {
		_, err := os.Stat(filepath.Join(d.Path, id+".md"))
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "---\nscope: %s\ndescription: %s\nrun: %s\nat: %s\n---\n", f.Scope, f.Description, f.Run, f.At.UTC().Format(time.RFC3339))
	b.WriteString(f.Body)
	if f.Body != "" && !strings.HasSuffix(f.Body, "\n") {
		b.WriteString("\n")
	}
	tmp, err := os.CreateTemp(d.Path, ".tmp-*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(d.Path, id+".md")); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return id, nil
}

func (d *Dir) Delete(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return fmt.Errorf("fact id %q is not a file in %s", id, d.Path)
	}
	err := os.Remove(filepath.Join(d.Path, id+".md"))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("fact %s not found", id)
	}
	return err
}

func (d *Dir) warn(msg string) {
	if d.Warn != nil {
		d.Warn(msg)
		return
	}
	fmt.Fprintln(os.Stderr, "warning: "+msg)
}

func read(path string) (core.MemoryFact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return core.MemoryFact{}, err
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return core.MemoryFact{}, errors.New("no front matter")
	}
	f := core.MemoryFact{ID: strings.TrimSuffix(filepath.Base(path), ".md"), Path: path}
	closed := false
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return core.MemoryFact{}, fmt.Errorf("front matter line %q", line)
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "scope":
			f.Scope = value
		case "description":
			f.Description = value
		case "run":
			f.Run = value
		case "at":
			f.At, _ = time.Parse(time.RFC3339, value)
		}
	}
	if !closed {
		return core.MemoryFact{}, errors.New("front matter is not closed")
	}
	if f.Scope == "" || f.Description == "" {
		return core.MemoryFact{}, errors.New("front matter needs scope and description")
	}
	var body strings.Builder
	for sc.Scan() {
		body.WriteString(sc.Text() + "\n")
	}
	f.Body = body.String()
	return f, sc.Err()
}

func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= maxSlug {
			break
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		return "fact"
	}
	return out
}
