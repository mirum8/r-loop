package plain

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"r-loop/internal/core"
)

type Face struct {
	Out    io.Writer
	Report string
	mu     sync.Mutex
}

func (f *Face) Emit(ev core.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch ev.Kind {
	case "step":
		line := fmt.Sprintf("%s  phase %d  %s  %s  %s  %s", ev.At.Format("15:04:05"), ev.Phase, ev.Step,
			ev.Fields["state"], ev.Fields["provider"], ev.Fields["reason"])
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
	f.mu.Lock()
	defer f.mu.Unlock()
	fmt.Fprintf(f.Out, "?  %s  %s%s\n", q.ID, where(q.Step.Phase, q.Step.Kind), q.Text)
	for i, o := range q.Options {
		fmt.Fprintf(f.Out, "   %d. %s\n", i+1, o)
	}
	return "", core.ErrNoInput
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
