package core

import (
	"fmt"
	"strings"
	"time"
)

const ResolveFirstStep = "resolve first"

func Report(state RunState, plan Plan) string {
	var b strings.Builder
	human := 0
	for _, ev := range state.Events {
		if ev.Kind == "human" {
			human++
		}
	}
	fmt.Fprintf(&b, "human touches: %d\n", human)
	writeSection(&b, "Automatic decisions", decisions(state))
	writeSection(&b, "Landed", landedLines(state))
	writeSection(&b, "Blockers", blockerLines(state))
	writeSection(&b, "Phase checks", phaseCheckLines(state))
	if state.Status == RunHalted {
		b.WriteString("\n## Halt\n\n")
		for _, l := range haltLines(state) {
			b.WriteString("- " + l + "\n")
		}
		b.WriteString("\nr-loop resume\n")
	}
	assumptions(&b, state)
	writeSection(&b, "Questions", questionLines(state))
	writeSection(&b, "Signals", signalLines(state))
	writeSection(&b, "Remedies", remedyLines(state))
	writeSection(&b, "Findings", findingLines(state))
	writeSection(&b, "Skips", skipLines(state))
	return b.String()
}

func writeSection(b *strings.Builder, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## %s\n\n", title)
	for _, l := range lines {
		b.WriteString("- " + l + "\n")
	}
}

func where(phase int, step string) string {
	if step == "" {
		return fmt.Sprintf("phase %d", phase)
	}
	return fmt.Sprintf("phase %d %s", phase, step)
}

func decisions(st RunState) []string {
	var out []string
	commands := map[string]string{}
	for _, r := range st.Remedies {
		commands[r.ID] = r.Command
	}
	for _, ev := range st.Events {
		f := ev.Fields
		switch ev.Kind {
		case "nudge":
			out = append(out, where(ev.Phase, ev.Step)+": nudge")
		case "restart":
			line := where(ev.Phase, ev.Step) + ": restart as attempt " + f["attempt"]
			if f["provider"] != "" {
				line += fmt.Sprintf(" on %s model %s effort %s", f["provider"], orDefault(f["model"]), orDefault(f["effort"]))
			}
			if f["addendum"] != "" {
				line += " — " + f["addendum"]
			}
			if remedy := f["remedy"]; remedy != "" {
				if cmd, ok := commands[remedy]; ok {
					remedy = cmd
				}
				line += " (remedy: " + remedy + ")"
			}
			out = append(out, line)
		case "gate-fix":
			out = append(out, where(ev.Phase, ev.Step)+": gate-fix round "+f["round"])
		case "warning":
			if strings.HasPrefix(f["reason"], "review round limit") {
				out = append(out, where(ev.Phase, ev.Step)+": "+f["reason"])
			}
		case "phase-blocked":
			out = append(out, where(ev.Phase, "")+" blocked: "+f["reason"])
		case "phase-skipped":
			out = append(out, skippedLine(ev))
		}
	}
	return out
}

func skippedLine(ev Event) string {
	return where(ev.Phase, "") + " skipped: depends on blocked phase " + ev.Fields["because"]
}

func phaseCheckLines(st RunState) []string {
	var phases []int
	result, outcome := map[int]string{}, map[int]string{}
	for _, ev := range st.Events {
		p := ev.Phase
		switch ev.Kind {
		case phaseCheckRan, phaseCheckTimeout, phaseCheckSkipped:
			if _, seen := result[p]; !seen {
				phases = append(phases, p)
			}
			result[p] = map[string]string{phaseCheckRan: ev.Fields["result"], phaseCheckTimeout: "timed out", phaseCheckSkipped: "skipped"}[ev.Kind]
			outcome[p] = ""
		case "landed":
			outcome[p] = "landed"
		case "phase-blocked":
			outcome[p] = "failed"
			if strings.HasPrefix(ev.Fields["reason"], "watchdog: ") {
				outcome[p] = "halted"
			}
		case "aborted", "halt":
			if p > 0 {
				outcome[p] = "halted"
			}
		}
	}
	var out []string
	for _, p := range phases {
		line := fmt.Sprintf("phase %d phase check: %s", p, result[p])
		if outcome[p] != "" {
			line += " — " + outcome[p]
		}
		out = append(out, line)
	}
	return out
}

func landedLines(st RunState) []string {
	var out []string
	for _, l := range st.Landed {
		line := fmt.Sprintf("phase %d %s", l.Phase, l.MergeSHA)
		if l.GateSkipped {
			line += " gate skipped"
		}
		out = append(out, line)
	}
	return out
}

func haltLines(st RunState) []string {
	var out []string
	for _, ev := range st.Events {
		f := ev.Fields
		switch {
		case ev.Kind == "human" && f["what"] == "resume":
			out = nil
		case ev.Kind == "phase-blocked":
			out = append(out, where(ev.Phase, ev.Step)+": "+f["reason"]+place(f))
		case ev.Kind == "phase-skipped":
			out = append(out, skippedLine(ev))
		case ev.Kind == "aborted":
			out = append(out, "aborted during "+where(ev.Phase, ev.Step)+place(f))
		}
	}
	return out
}

func place(f map[string]string) string {
	if f["workspace"] == "" && f["worktree"] == "" {
		return ""
	}
	return fmt.Sprintf(" — workspace %s, worktree %s", f["workspace"], f["worktree"])
}

func assumptions(b *strings.Builder, st RunState) {
	var phases []int
	byPhase := map[int][]string{}
	for _, ev := range st.Events {
		if ev.Kind != "assumption" {
			continue
		}
		if _, seen := byPhase[ev.Phase]; !seen {
			phases = append(phases, ev.Phase)
		}
		byPhase[ev.Phase] = append(byPhase[ev.Phase], ev.Fields["text"])
	}
	if len(phases) == 0 {
		return
	}
	b.WriteString("\n## Assumptions\n")
	for _, p := range phases {
		fmt.Fprintf(b, "\n### Phase %d\n\n", p)
		for _, a := range byPhase[p] {
			b.WriteString("- " + a + "\n")
		}
	}
}

func blockerLines(st RunState) []string {
	var out []string
	for _, ev := range st.Events {
		switch f := ev.Fields; ev.Kind {
		case "entry-resolved":
			out = append(out, fmt.Sprintf("%s → %s", f["entry"], f["resolved"]))
		case "entry-deferred":
			out = append(out, fmt.Sprintf("%s still open: phase %s skipped", f["entry"], f["phases"]))
		}
	}
	return out
}

func questionLines(st RunState) []string {
	var out []string
	for _, ev := range st.Events {
		if f := ev.Fields; ev.Kind == "human" && ev.Step == ResolveFirstStep && f["what"] == "answer" {
			out = append(out, fmt.Sprintf("%s %s: %s → %s (%s)", f["id"], ResolveFirstStep, f["entry"], f["answer"], f["by"]))
		}
	}
	for _, q := range st.Questions {
		line := fmt.Sprintf("%s %s: %s", q.ID, where(q.Step.Phase, q.Step.Kind), q.Text)
		if q.AnsweredBy == "" {
			line += " (open)"
		} else {
			who := q.AnsweredBy
			if q.Citation != "" {
				who += ", cites " + q.Citation
			}
			line += fmt.Sprintf(" → %s (%s, waited %s)", q.Answer, who, max(q.AnsweredAt.Sub(q.AskedAt), 0).Round(time.Second))
		}
		out = append(out, line)
	}
	return out
}

func signalLines(st RunState) []string {
	var out []string
	for _, s := range st.Signals {
		line := fmt.Sprintf("%s from %s, %s: %s", s.Kind, s.Source, where(s.Step.Phase, s.Step.Kind), s.Reason)
		if s.Rejected {
			line += " (rejected: " + s.RejectReason + ")"
		}
		out = append(out, line)
	}
	return out
}

func remedyLines(st RunState) []string {
	var out []string
	restarted := map[string]bool{}
	for _, ev := range st.Events {
		if ev.Kind == "restart" {
			restarted[ev.Fields["remedy"]] = true
		}
	}
	for _, r := range st.Remedies {
		consent := r.Consent
		if consent == "" {
			consent = "pending"
		}
		followed := "no restart"
		if r.ID != "" && restarted[r.ID] {
			followed = "restart followed"
		}
		out = append(out, fmt.Sprintf("%s: %s `%s` — %s; %s", where(r.Step.Phase, r.Step.Kind), r.Class, r.Command, consent, followed))
	}
	return out
}

func findingLines(st RunState) []string {
	var out []string
	for _, ev := range st.Events {
		if ev.Kind != "finding" {
			continue
		}
		f := ev.Fields
		line := fmt.Sprintf("%s r%s %s %s %s: %s %s fixed %s", where(ev.Phase, f["step"]), f["round"], f["reviewer"], f["id"], f["title"], f["verdict"], f["severity"], f["fixed"])
		if f["evidence"] != "" {
			line += " — " + f["evidence"]
		}
		out = append(out, line)
	}
	return out
}

func skipLines(st RunState) []string {
	var out []string
	askNone := map[string]bool{}
	for _, ev := range st.Events {
		if ev.Kind == "ask-none" {
			step := where(ev.Phase, ev.Step)
			if !askNone[step] {
				askNone[step] = true
				out = append(out, fmt.Sprintf("%s: ask: none (%s)", step, ev.Fields["provider"]))
			}
			continue
		}
		if ev.Kind == "phase-skipped" || ev.Kind == phaseCheckSkipped || !strings.HasSuffix(ev.Kind, "-skipped") {
			continue
		}
		line := ev.Kind
		if ev.Phase > 0 {
			line = where(ev.Phase, "") + ": " + ev.Kind
		}
		if r := ev.Fields["reason"]; r != "" {
			line += ": " + r
		}
		out = append(out, line)
	}
	return out
}

func orDefault(v string) string {
	if v == "" {
		return "provider default"
	}
	return v
}
