package tui

import (
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"r-loop/internal/core"
)

func question(id string, phase int, kind, text string, options ...string) core.Question {
	return core.Question{ID: id, Step: core.StepKey{Phase: phase, Kind: kind, Attempt: 1}, Text: text, Options: options}
}

func ask(m Model, q core.Question) (Model, chan string) {
	reply := make(chan string, 1)
	next, _ := m.Update(askMsg{q: q, reply: reply})
	return next.(Model), reply
}

func typeKeys(m Model, keys ...tea.KeyMsg) Model {
	for _, k := range keys {
		next, _ := m.Update(k)
		m = next.(Model)
	}
	return m
}

func text(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

var (
	enter = tea.KeyMsg{Type: tea.KeyEnter}
	esc   = tea.KeyMsg{Type: tea.KeyEsc}
	space = tea.KeyMsg{Type: tea.KeySpace}
)

func answered(t *testing.T, reply chan string) string {
	t.Helper()
	select {
	case a, ok := <-reply:
		if !ok {
			t.Fatal("reply closed without an answer")
		}
		return a
	default:
		t.Fatal("no answer yet")
	}
	return ""
}

func unanswered(t *testing.T, reply chan string) {
	t.Helper()
	select {
	case a := <-reply:
		t.Fatalf("answered %q too early", a)
	default:
	}
}

func TestADigitPicksAnOption(t *testing.T) {
	m, reply := ask(newModel(nil), question("q1", 2, "implement", "Which port?", "8080", "9090"))

	view := m.View()
	for _, want := range []string{"?  q1  phase 2 implement: Which port?", "1. 8080", "2. 9090"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}

	m = typeKeys(m, text("2"))
	unanswered(t, reply)
	m = typeKeys(m, enter)

	if got := answered(t, reply); got != "9090" {
		t.Fatalf("answer %q", got)
	}
	if view := m.View(); strings.Contains(view, "Which port?") {
		t.Fatalf("view:\n%s", view)
	}
	m = m.Apply(core.Event{At: at(1), Kind: "human", Phase: 2, Step: "implement", Fields: map[string]string{"what": "answer", "id": "q1"}})
	if view := m.View(); !strings.Contains(view, "q1  answered by maintainer") {
		t.Fatalf("view:\n%s", view)
	}
}

func TestAnAnswerLostToAFileAnswerIsNotShownAsTheAnswer(t *testing.T) {
	m, reply := ask(newModel(nil), question("q1", 2, "plan", "Which database?"))

	m = typeKeys(m, text("Mongo"), enter)
	answered(t, reply)
	m = m.Apply(core.Event{At: at(1), Kind: "human", Phase: 2, Step: "plan", Fields: map[string]string{"what": "answer", "id": "q1"}})

	if view := m.View(); strings.Contains(view, "Mongo") || !strings.Contains(view, "q1  answered by maintainer") {
		t.Fatalf("view:\n%s", view)
	}
}

func TestAWatchdogAnswerSettlesTheQuestionAndShowsItsCitation(t *testing.T) {
	m, reply := ask(newModel(nil), question("q1", 2, "implement", "Which database?"))

	m = m.Apply(core.Event{At: at(1), Kind: "question-answered", Phase: 2, Step: "implement", Fields: map[string]string{"id": "q1", "answer": "sqlite", "by": "watchdog", "citation": "docs/x/spec.html:12"}})

	<-reply
	if view := m.View(); strings.Contains(view, "Which database?") || !strings.Contains(view, "q1  answered by watchdog  docs/x/spec.html:12") {
		t.Fatalf("view:\n%s", view)
	}
}

func TestADraftDoesNotCarryOverToTheNextQuestion(t *testing.T) {
	m, first := ask(newModel(nil), question("q1", 2, "plan", "Which database?"))
	m, second := ask(m, question("q2", 3, "plan", "Which port?"))

	m = typeKeys(m, text("Postg"))
	m = m.Apply(core.Event{At: at(1), Kind: "human", Phase: 2, Step: "plan", Fields: map[string]string{"what": "answer", "id": "q1"}})
	<-first
	if strings.Contains(m.View(), "Postg") {
		t.Fatalf("stale draft:\n%s", m.View())
	}
	m = typeKeys(m, enter)
	unanswered(t, second)
	typeKeys(m, text("8080"), enter)
	if got := answered(t, second); got != "8080" {
		t.Fatalf("answer %q", got)
	}
}

func TestPastedLineBreaksBecomeSpaces(t *testing.T) {
	m, reply := ask(newModel(nil), question("r1", 0, "resolve first", "Pick the database"))

	typeKeys(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Postgres\r\n## Phase 9\nnow"), Paste: true}, enter)

	if got := answered(t, reply); got != "Postgres ## Phase 9 now" {
		t.Fatalf("answer %q", got)
	}
}

func TestTypedTextIsAFreeAnswerAndEscClearsTheDraft(t *testing.T) {
	m, reply := ask(newModel(nil), question("q1", 2, "plan", "Which database?", "Postgres", "SQLite"))

	m = typeKeys(m, text("Mongo"), esc, text("use"), space, text("both"))
	if !strings.Contains(m.View(), "use both") {
		t.Fatalf("draft not shown:\n%s", m.View())
	}
	m = typeKeys(m, enter)

	if got := answered(t, reply); got != "use both" {
		t.Fatalf("answer %q", got)
	}
}

func TestEnterOnAnEmptyDraftDoesNotSubmit(t *testing.T) {
	m, reply := ask(newModel(nil), question("q1", 2, "plan", "Which database?"))

	typeKeys(m, enter)

	unanswered(t, reply)
}

func TestQueuedQuestionsAreAnsweredInArrivalOrder(t *testing.T) {
	m, first := ask(newModel(nil), question("q1", 2, "plan", "Which database?"))
	m, second := ask(m, question("q2", 3, "implement", "Which port?", "8080", "9090"))

	view := m.View()
	if !strings.Contains(view, "2 open") || strings.Index(view, "Which database?") > strings.Index(view, "Which port?") {
		t.Fatalf("view:\n%s", view)
	}

	m = typeKeys(m, text("Postgres"), enter)
	if got := answered(t, first); got != "Postgres" {
		t.Fatalf("first answer %q", got)
	}
	unanswered(t, second)
	if !strings.Contains(m.View(), "1 open") {
		t.Fatalf("view:\n%s", m.View())
	}

	m = typeKeys(m, text("1"), enter)
	if got := answered(t, second); got != "8080" {
		t.Fatalf("second answer %q", got)
	}
}

func TestAYesNoQuestionIsAConsentLineThatYAndNAnswer(t *testing.T) {
	m, reply := ask(newModel(nil), question("q1", 2, "implement", "watchdog proposes (retry): rerun — flaky", "yes", "no"))

	view := m.View()
	if !strings.Contains(view, "[y/n]") || strings.Contains(view, "1. yes") {
		t.Fatalf("view:\n%s", view)
	}
	m = typeKeys(m, text("y"))
	if got := answered(t, reply); got != "yes" {
		t.Fatalf("answer %q", got)
	}

	m, reply = ask(m, question("q2", 2, "implement", "watchdog proposes (provider): codex — limit", "yes", "no"))
	typeKeys(m, text("n"))
	if got := answered(t, reply); got != "no" {
		t.Fatalf("answer %q", got)
	}
}

func TestTheQuestionEventAndTheAskAreOneQuestion(t *testing.T) {
	m := newModel(recorded())
	m, reply := ask(m, question("q2", 2, "implement", "Which port?", "8080"))

	if strings.Count(m.View(), "Which port?") != 1 || !strings.Contains(m.View(), "1 open") {
		t.Fatalf("view:\n%s", m.View())
	}
	m = typeKeys(m, text("1"), enter)
	answered(t, reply)
	m = m.Apply(core.Event{At: at(41), Kind: "human", Phase: 2, Step: "implement", Fields: map[string]string{"what": "answer", "id": "q2"}})

	if strings.Count(m.View(), "q2  answered") != 1 {
		t.Fatalf("view:\n%s", m.View())
	}
}

func TestAQuestionAnsweredElsewhereReleasesTheAsk(t *testing.T) {
	m, reply := ask(newModel(nil), question("q1", 2, "plan", "Which database?"))

	m = m.Apply(core.Event{At: at(1), Kind: "human", Phase: 2, Step: "plan", Fields: map[string]string{"what": "answer", "id": "q1"}})

	if _, ok := <-reply; ok {
		t.Fatal("reply not closed")
	}
	if !strings.Contains(m.View(), "q1  answered by maintainer") {
		t.Fatalf("view:\n%s", m.View())
	}
	_, late := ask(m, question("q1", 2, "plan", "Which database?"))
	if _, ok := <-late; ok {
		t.Fatal("a late ask for an answered question was not released")
	}
}

func TestAResolveFirstQuestionShowsItsFullText(t *testing.T) {
	m, _ := ask(newModel(nil), core.Question{ID: "r1", Step: core.StepKey{Kind: "resolve first"}, Text: "Pick the database\n- [ ] **Pick the database** — Owner: me\n  Blocks: Phase 2"})

	view := m.View()
	for _, want := range []string{"?  r1  resolve first: Pick the database", "**Pick the database** — Owner: me", "Blocks: Phase 2"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
}

func TestFaceAskReturnsTheAnswerTypedIntoTheProgram(t *testing.T) {
	in, w := io.Pipe()
	var out syncBuffer
	f := &Face{In: in, Out: &out}
	f.Start(Header{RunID: "r1", Todo: "todo.md", Started: t0}, plan(), nil)
	defer f.Stop()

	got := make(chan string, 1)
	go func() {
		a, err := f.Ask(question("q1", 2, "plan", "Which database?", "Postgres", "SQLite"))
		if err != nil {
			a = err.Error()
		}
		got <- a
	}()
	for !strings.Contains(out.String(), "Which database?") {
		time.Sleep(time.Millisecond)
	}
	w.Write([]byte("2"))
	w.Write([]byte("\r"))

	select {
	case a := <-got:
		if a != "SQLite" {
			t.Fatalf("answer %q", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask did not return")
	}
}

func TestWithdrawEndsAWaitingFaceAsk(t *testing.T) {
	in, _ := io.Pipe()
	var out syncBuffer
	f := &Face{In: in, Out: &out}
	f.Start(Header{RunID: "r1", Todo: "todo.md", Started: t0}, plan(), nil)
	defer f.Stop()

	errs := make(chan error, 1)
	go func() {
		_, err := f.Ask(question("q1", 2, "plan", "Which database?"))
		errs <- err
	}()
	for !strings.Contains(out.String(), "Which database?") {
		time.Sleep(time.Millisecond)
	}
	f.Withdraw("q1")

	select {
	case err := <-errs:
		if err != core.ErrNoInput {
			t.Fatalf("err %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask did not return")
	}
}
