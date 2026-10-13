package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"r-loop/internal/core"
)

func activity(min int, step, text string) core.Event {
	return core.Event{At: at(min), Kind: "activity", Phase: "2", Step: step, Fields: map[string]string{"agent": "rloop-x", "text": text}}
}

func running() []core.Event {
	return recorded()[:len(recorded())-2]
}

func TestTheLiveStepShowsWhatItsAgentIsDoing(t *testing.T) {
	m := newModel(append(running(), activity(41, "implement", "editing internal/core/land.go · 2 files")))
	m.Width, m.Height = 120, 40

	if line := lineWith(ansi.Strip(m.View()), "● implement"); !strings.Contains(line, "editing internal/core/land.go · 2 files") {
		t.Fatalf("live node = %q", line)
	}
}

func TestAnEmptyActivityGivesTheStepItsStateBack(t *testing.T) {
	m := newModel(append(running(), activity(41, "implement", "editing a.go"), activity(42, "implement", "")))
	m.Width, m.Height = 120, 40

	if line := lineWith(ansi.Strip(m.View()), "● implement"); strings.Contains(line, "editing") || !strings.Contains(line, "running") {
		t.Fatalf("live node = %q", line)
	}
}

func TestAWaitingStepKeepsItsPointerOverItsActivity(t *testing.T) {
	m := newModel(append(recorded(), activity(41, "implement", "asking watchdog")))
	m.Width, m.Height = 120, 40

	if line := lineWith(ansi.Strip(m.View()), "● implement"); !strings.Contains(line, "waiting ⇢ q2") {
		t.Fatalf("live node = %q", line)
	}
}

func TestANewAttemptStartsWithoutTheOldActivity(t *testing.T) {
	events := append(running(), activity(41, "implement", "editing a.go"), step(42, 2, "implement", "failed", "codex", "gpt-5", "medium", "ws-4"))
	retry := step(43, 2, "implement", "running", "codex", "gpt-5", "medium", "ws-5")
	retry.Fields["attempt"] = "2"
	m := newModel(append(events, retry))
	m.Width, m.Height = 120, 40

	if line := lineWith(ansi.Strip(m.View()), "● implement a2"); strings.Contains(line, "editing") {
		t.Fatalf("live node = %q", line)
	}
}

func TestAReviewerShowsWhatItsAgentIsDoing(t *testing.T) {
	events := append(running(),
		reviewEvent(41, "review-round", map[string]string{"round": "1", "attempt": "1"}),
		roundStep(41, 1, "find"),
		reviewEvent(41, "agent-named", map[string]string{"round": "1", "reviewer": "claude", "agent": "p2-impl-rv-claude-r1", "attempt": "1"}),
		reviewEvent(41, "agent-named", map[string]string{"round": "1", "reviewer": "codex", "agent": "p2-impl-rv-codex-r1", "attempt": "1"}),
		activity(42, "implement-rv-claude", "reading internal/core/land.go"),
	)
	m := newModel(events)
	m.Width, m.Height = 120, 40
	view := ansi.Strip(m.View())

	if line := lineWith(view, "r1 claude"); !strings.Contains(line, "reading internal/core/land.go") {
		t.Fatalf("claude node = %q", line)
	}
	if line := lineWith(view, "r1 codex"); !strings.Contains(line, "reviewing") {
		t.Fatalf("codex node = %q", line)
	}
}

func TestALongActivityStaysOneLineAtEightyColumns(t *testing.T) {
	m := newModel(append(running(), activity(41, "implement", "running "+strings.Repeat("go test ./internal/core/... ", 10))))
	m.Width, m.Height = 80, 30

	for _, l := range strings.Split(ansi.Strip(m.View()), "\n") {
		if w := ansi.StringWidth(l); w > 80 {
			t.Fatalf("line is %d cells: %q", w, l)
		}
	}
	if line := lineWith(ansi.Strip(m.View()), "● implement"); !strings.Contains(line, "running go test") || !strings.HasSuffix(strings.TrimRight(line, " │"), "…") {
		t.Fatalf("live node = %q", line)
	}
}
