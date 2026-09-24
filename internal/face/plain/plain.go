package plain

import (
	"fmt"
	"io"
	"slices"
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
		if q := ev.Fields["question"]; q != "" {
			fmt.Fprintln(f.Out, "!  watchdog waiting for you: "+q)
		} else {
			fmt.Fprintln(f.Out, "!  watchdog waiting for you")
		}
	case "phase-check-start", "phase-check", "phase-check-timeout", "phase-check-skipped":
		fmt.Fprintf(f.Out, "%s  phase %s  phase check  %s\n", ev.At.Format("15:04:05"), ev.Phase, checkDetail(ev))
	case "triage-start":
		noun := "phase"
		if ev.Fields["kind"] == "backlog" {
			noun = "item"
		}
		fmt.Fprintf(f.Out, "%s  triage: watchdog verifying %s\n", ev.At.Format("15:04:05"), core.Plural(len(strings.Split(ev.Fields["phases"], ", ")), noun))
	case "triage":
		fmt.Fprintf(f.Out, "%s  triage: %s\n%s", ev.At.Format("15:04:05"), ev.Fields["summary"], ev.Fields["table"])
	case core.TriageSkipped:
		fmt.Fprintf(f.Out, "%s  phase %s  skipped by triage: %s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Fields["reason"])
	case "run-list":
		fmt.Fprintf(f.Out, "run list: %s\n", runList(ev.Fields))
	case "warning", "error":
		fmt.Fprintf(f.Out, "!  %s%s\n", where(ev.Phase, ev.Step), ev.Fields["reason"])
	case "review-round":
		fmt.Fprintf(f.Out, "%s  phase %s  %s  review r%s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, ev.Fields["round"])
	case "agent-named":
		fmt.Fprintf(f.Out, "%s  phase %s  %s  reviewer %s r%s  agent %s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, ev.Fields["reviewer"], ev.Fields["round"], ev.Fields["agent"])
	case "finding":
		fields := ev.Fields
		fmt.Fprintf(f.Out, "%s  phase %s  %s  r%s %s %s  %s %s fixed=%s: %s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, fields["round"], fields["reviewer"], fields["id"], fields["verdict"], fields["severity"], fields["fixed"], fields["title"])
	case "review-clean":
		fmt.Fprintf(f.Out, "%s  phase %s  %s  review r%s clean\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, ev.Fields["round"])
	case "review-find":
		fields := ev.Fields
		fmt.Fprintf(f.Out, "%s  phase %s  %s  reviewer %s r%s  %s  %s findings", ev.At.Format("15:04:05"), ev.Phase, ev.Step, fields["reviewer"], fields["round"], fields["state"], fields["findings"])
		if fields["command"] != "" {
			fmt.Fprintf(f.Out, "  ran `%s`", fields["command"])
		}
		fmt.Fprintln(f.Out)
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

func runList(fields map[string]string) string {
	heads := map[string][]string{}
	for _, g := range core.RunListGroups(fields) {
		ids := slices.Clone(g.Items)
		slices.SortFunc(ids, core.ComparePhaseIDs)
		if len(ids) > 1 {
			heads[ids[0]] = ids
		}
	}
	var parts []string
	for _, id := range strings.Split(fields["phases"], ",") {
		if id == "" {
			continue
		}
		if members, ok := heads[id]; ok {
			id = fmt.Sprintf("%s (items %s)", id, strings.Join(members, ", "))
		}
		parts = append(parts, id)
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func where(phase, step string) string {
	if phase == "" {
		return ""
	}
	return fmt.Sprintf("phase %s %s: ", phase, step)
}

func checkDetail(ev core.Event) string {
	f := ev.Fields
	switch ev.Kind {
	case "phase-check-start":
		return "watchdog checking the plan"
	case "phase-check":
		if f["result"] == "no disagreement" {
			return "found no disagreement"
		}
		return "warned"
	case "phase-check-timeout":
		return withReason("timed out", f["reason"])
	}
	return withReason("skipped", f["reason"])
}

func withReason(text, reason string) string {
	if reason == "" {
		return text
	}
	return text + ": " + reason
}
