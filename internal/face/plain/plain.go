package plain

import (
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"r-loop/internal/core"
)

type Face struct {
	Out       io.Writer
	Report    string
	mu        sync.Mutex
	gate      *core.Event
	diagnosis string
}

func (f *Face) Emit(ev core.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	f.closeGate(&b, ev)
	if ev.Kind == core.EventFailureReport {
		f.diagnosis = ev.Fields["path"]
	}
	f.emit(&b, ev)
	io.WriteString(f.Out, core.Displayable(b.String()))
}

func (f *Face) emit(w io.Writer, ev core.Event) {
	switch ev.Kind {
	case "activity":
	case "dialog":
		if ev.Fields["notice"] == "" {
			fields(w, ev)
			return
		}
		fmt.Fprintf(w, "%s  phase %s  %s  dialog %s  %s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, ev.Fields["id"], strings.Join(strings.Fields(ev.Fields["notice"]), " "))
	case "step":
		detail := ev.Fields["reason"]
		if r := ev.Fields["round"]; r != "" {
			detail = strings.TrimSpace(fmt.Sprintf("review r%s/%s %s", r, ev.Fields["rounds"], ev.Fields["half"]))
		}
		line := fmt.Sprintf("%s  phase %s  %s  %s  %s  %s", ev.At.Format("15:04:05"), ev.Phase, ev.Step,
			ev.Fields["state"], ev.Fields["provider"], detail)
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	case "land-stage":
		line := fmt.Sprintf("%s  phase %s  land  %s", ev.At.Format("15:04:05"), ev.Phase, ev.Fields["stage"])
		if cmd := ev.Fields["command"]; cmd != "" {
			line += "  " + cmd
			f.gate = &ev
		}
		if log := ev.Fields["log"]; log != "" {
			line += "  log " + log
		}
		fmt.Fprintln(w, line)
	case "nudge":
		fmt.Fprintf(w, "%s  phase %s  %s  nudge\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step)
	case "watchdog-waiting":
		if q := ev.Fields["question"]; q != "" {
			fmt.Fprintln(w, "!  watchdog waiting for you: "+q)
		} else {
			fmt.Fprintln(w, "!  watchdog waiting for you")
		}
	case "phase-check-start", "phase-check", "phase-check-timeout", "phase-check-skipped":
		fmt.Fprintf(w, "%s  phase %s  phase check  %s\n", ev.At.Format("15:04:05"), ev.Phase, checkDetail(ev))
	case "triage-start":
		noun := "phase"
		if ev.Fields["kind"] == "backlog" {
			noun = "item"
		}
		fmt.Fprintf(w, "%s  triage: watchdog verifying %s\n", ev.At.Format("15:04:05"), core.Plural(len(strings.Split(ev.Fields["phases"], ", ")), noun))
	case "triage":
		fmt.Fprintf(w, "%s  triage: %s\n%s", ev.At.Format("15:04:05"), ev.Fields["summary"], ev.Fields["table"])
	case core.TriageSkipped:
		fmt.Fprintf(w, "%s  phase %s  skipped by triage: %s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Fields["reason"])
	case "run-list":
		fmt.Fprintf(w, "run list: %s\n", runList(ev.Fields))
	case "warning", "error":
		fmt.Fprintf(w, "!  %s%s\n", where(ev.Phase, ev.Step), ev.Fields["reason"])
	case "review-round":
		fmt.Fprintf(w, "%s  phase %s  %s  review r%s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, ev.Fields["round"])
	case "agent-named":
		fmt.Fprintf(w, "%s  phase %s  %s  reviewer %s r%s  agent %s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, ev.Fields["reviewer"], ev.Fields["round"], ev.Fields["agent"])
	case "finding":
		fields := ev.Fields
		fmt.Fprintf(w, "%s  phase %s  %s  r%s %s %s  %s %s fixed=%s: %s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, fields["round"], fields["reviewer"], fields["id"], fields["verdict"], fields["severity"], fields["fixed"], fields["title"])
	case "review-clean":
		fmt.Fprintf(w, "%s  phase %s  %s  review r%s clean\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, ev.Fields["round"])
	case "blocked-on":
		fmt.Fprintf(w, "%s  phase %s  %s  blocker %s (%s): %s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, ev.Fields["id"], ev.Fields["source"], ev.Fields["reason"])
	case "blocker-resolved":
		fmt.Fprintf(w, "%s  phase %s  %s  blocker %s → %s (%s)\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, ev.Fields["id"], ev.Fields["action"], ev.Fields["by"])
	case core.EventFixStarted, core.EventFixProposed, core.EventFixApplied, core.EventFixRejected, core.EventFixFailed:
		fmt.Fprintf(w, "%s  phase %s  %s  %s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, fixDetail(ev))
	case "step-skipped":
		fmt.Fprintf(w, "%s  phase %s  %s  skipped: %s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Step, ev.Fields["reason"])
	case "design":
		fmt.Fprintf(w, "%s  phase %s  design: %s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Fields["intent"])
	case "design-skipped":
		fmt.Fprintf(w, "%s  phase %s  design skipped: %s\n", ev.At.Format("15:04:05"), ev.Phase, ev.Fields["reason"])
	case "review-find":
		fields := ev.Fields
		fmt.Fprintf(w, "%s  phase %s  %s  reviewer %s r%s  %s  %s findings", ev.At.Format("15:04:05"), ev.Phase, ev.Step, fields["reviewer"], fields["round"], fields["state"], fields["findings"])
		if fields["command"] != "" {
			fmt.Fprintf(w, "  ran `%s`", fields["command"])
		}
		fmt.Fprintln(w)
	default:
		fields(w, ev)
	}
}

func fields(w io.Writer, ev core.Event) {
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
	fmt.Fprintf(w, "%s  %s%s  %s\n", ev.At.Format("15:04:05"), phase, ev.Kind, strings.Join(pairs, " "))
}

func (f *Face) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Report != "" {
		fmt.Fprintf(f.Out, "report: %s\n", f.Report)
	}
	if f.diagnosis != "" {
		fmt.Fprintf(f.Out, "diagnosis: %s\n", f.diagnosis)
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

func fixDetail(ev core.Event) string {
	id := ev.Fields["id"]
	switch ev.Kind {
	case core.EventFixStarted:
		return fmt.Sprintf("fixer %s started (%s %s)", id, ev.Fields["provider"], ev.Fields["model"])
	case core.EventFixProposed:
		return fmt.Sprintf("fix %s proposed (%s): %s", id, ev.Fields["kind"], ev.Fields["cause"])
	case core.EventFixFailed:
		return fmt.Sprintf("fix %s failed: %s", id, ev.Fields["reason"])
	case core.EventFixApplied:
		return "fix " + id + " applied"
	}
	return "fix " + id + " rejected"
}

func where(phase, step string) string {
	if phase == "" {
		return ""
	}
	return fmt.Sprintf("phase %s %s: ", phase, step)
}

func (f *Face) closeGate(w io.Writer, ev core.Event) {
	g := f.gate
	if g == nil || ev.Phase != g.Phase {
		return
	}
	switch ev.Kind {
	case "land-stage", "landed", "gate-fix", "blocked-on", "phase-blocked", "error":
		f.gate = nil
		fmt.Fprintf(w, "%s  phase %s  land  %s took %s\n", ev.At.Format("15:04:05"), g.Phase, g.Fields["stage"], ev.At.Sub(g.At).Truncate(time.Second))
	}
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
