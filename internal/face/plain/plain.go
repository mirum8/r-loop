package plain

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"

	"r-loop/internal/core"
)

type Face struct {
	Out    io.Writer
	In     io.Reader
	TTY    bool
	Report string
	mu     sync.Mutex
	readMu sync.Mutex
	once   sync.Once
	lines  chan string
	gone   map[string]chan struct{}
	prompt string
}

func (f *Face) Emit(ev core.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prompt != "" {
		fmt.Fprintln(f.Out)
		defer fmt.Fprint(f.Out, f.prompt)
	}
	switch ev.Kind {
	case "step":
		detail := ev.Fields["reason"]
		if r := ev.Fields["round"]; r != "" {
			detail = strings.TrimSpace(fmt.Sprintf("review r%s/%s %s", r, ev.Fields["rounds"], ev.Fields["half"]))
		}
		line := fmt.Sprintf("%s  phase %d  %s  %s  %s  %s", ev.At.Format("15:04:05"), ev.Phase, ev.Step,
			ev.Fields["state"], ev.Fields["provider"], detail)
		fmt.Fprintln(f.Out, strings.TrimRight(line, " "))
	case "warning", "error":
		fmt.Fprintf(f.Out, "!  %s%s\n", where(ev.Phase, ev.Step), ev.Fields["reason"])
	default:
		keys := make([]string, 0, len(ev.Fields))
		for k := range ev.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		pairs := make([]string, len(keys))
		for i, k := range keys {
			pairs[i] = k + "=" + ev.Fields[k]
		}
		phase := ""
		if ev.Phase > 0 {
			phase = fmt.Sprintf("phase %d  ", ev.Phase)
		}
		fmt.Fprintf(f.Out, "%s  %s%s  %s\n", ev.At.Format("15:04:05"), phase, ev.Kind, strings.Join(pairs, " "))
	}
}

func (f *Face) Ask(q core.Question) (string, error) {
	f.readMu.Lock()
	defer f.readMu.Unlock()
	block := fmt.Sprintf("?  %s  %s%s\n", q.ID, where(q.Step.Phase, q.Step.Kind), q.Text)
	for i, o := range q.Options {
		block += fmt.Sprintf("   %d. %s\n", i+1, o)
	}
	f.print("%s", block)
	if !f.TTY || f.In == nil {
		f.print("question %s stays open — answer from the TUI or resume later\n", q.ID)
		return "", core.ErrNoInput
	}
	f.once.Do(f.read)
	gone := f.withdrawn(q.ID)
	for {
		f.setPrompt(fmt.Sprintf("answer %s> ", q.ID))
		var line string
		select {
		case l, ok := <-f.lines:
			f.setPrompt("")
			if !ok {
				return "", core.ErrNoInput
			}
			line = strings.TrimSpace(l)
		case <-gone:
			f.setPrompt("")
			f.print("\n%s answered elsewhere\n", q.ID)
			return "", core.ErrNoInput
		}
		if line == "" {
			continue
		}
		if n, err := strconv.Atoi(line); err == nil && n >= 1 && n <= len(q.Options) {
			return q.Options[n-1], nil
		}
		return line, nil
	}
}

func (f *Face) read() {
	f.lines = make(chan string)
	go func() {
		r := bufio.NewReader(f.In)
		for {
			line, err := r.ReadString('\n')
			if line != "" {
				f.lines <- line
			}
			if err != nil {
				close(f.lines)
				return
			}
		}
	}()
}

func (f *Face) Withdraw(id string) {
	gone := f.withdrawn(id)
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-gone:
	default:
		close(gone)
	}
}

func (f *Face) withdrawn(id string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gone == nil {
		f.gone = map[string]chan struct{}{}
	}
	if f.gone[id] == nil {
		f.gone[id] = make(chan struct{})
	}
	return f.gone[id]
}

func (f *Face) setPrompt(p string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompt = p
	fmt.Fprint(f.Out, p)
}

func (f *Face) print(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fmt.Fprintf(f.Out, format, args...)
}

func (f *Face) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Report != "" {
		fmt.Fprintf(f.Out, "report: %s\n", f.Report)
	}
}

func where(phase int, step string) string {
	if phase == 0 {
		return ""
	}
	return fmt.Sprintf("phase %d %s: ", phase, step)
}
