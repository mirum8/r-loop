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
		detail := ev.Fields["reason"]
		if r := ev.Fields["round"]; r != "" {
			detail = strings.TrimSpace(fmt.Sprintf("review r%s/%s %s", r, ev.Fields["rounds"], ev.Fields["half"]))
		}
		line := fmt.Sprintf("%s  phase %s  %s  %s  %s  %s", ev.At.Format("15:04:05"), ev.Phase, ev.Step,
			ev.Fields["state"], ev.Fields["provider"], detail)
		fmt.Fprintln(f.Out, strings.TrimRight(line, " "))
	case "nudge":
		fmt.Fprintf(f.Out, "%s  phase %s  %s  nudge\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step)
	case "watchdog-waiting":
		fmt.Fprintln(f.Out, "!  watchdog waiting for you")
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
		if ev.Phase != "" {
			phase = fmt.Sprintf("phase %s  ", ev.Phase)
		}
		fmt.Fprintf(f.Out, "%s  %s%s  %s\n", ev.At.Format("15:04:05"), phase, ev.Kind, strings.Join(pairs, " "))
	}
}

func (f *Face) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Report != "" {
		fmt.Fprintf(f.Out, "report: %s\n", f.Report)
	}
}

func where(phase, step string) string {
	if phase == "" {
		return ""
	}
	return fmt.Sprintf("phase %s %s: ", phase, step)
}
