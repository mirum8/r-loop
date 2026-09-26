package core

import (
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const paneReviewText = "/review Review the current code changes"

type screenHost struct {
	*scriptedHost
	mu      sync.Mutex
	screens map[string]int
	entered map[string]time.Time
	enters  map[string]int
	screen  func(h *screenHost, agent string, n int) string
}

func (h *screenHost) Screen(agent string) (string, error) {
	h.record("SessionHost.Screen %s", agent)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.screens[agent]++
	return h.screen(h, agent, h.screens[agent]), nil
}

func (h *screenHost) SendKeys(agent string, keys ...string) error {
	h.mu.Lock()
	h.entered[agent] = time.Now()
	h.enters[agent]++
	h.mu.Unlock()
	return h.scriptedHost.SendKeys(agent, keys...)
}

func paneReviewRig(t *testing.T, screen func(h *screenHost, agent string, n int) string, reviewers ...Reviewer) (*reviewRig, *screenHost) {
	t.Helper()
	r := newReviewRig(t, reviewers...)
	host := &screenHost{scriptedHost: r.host, screens: map[string]int{}, entered: map[string]time.Time{}, enters: map[string]int{}, screen: screen}
	r.sm.Host = host
	resolve := r.sm.Resolve
	r.sm.Resolve = func(provider, model, effort, askURL, mcpConfigPath, dir string) (ProviderArgs, error) {
		a, err := resolve(provider, model, effort, askURL, mcpConfigPath, dir)
		if provider == "codex" {
			a.Review, a.ReviewStart, a.ReviewDone = paneReviewText, ">> Code review started", "<< Code review finished"
		}
		return a, err
	}
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }
	return r, host
}

func fastPaneReview(t *testing.T) {
	t.Helper()
	start, typed, submit := paneReviewStartWait, paneReviewTypedWait, paneReviewSubmitWait
	t.Cleanup(func() { paneReviewStartWait, paneReviewTypedWait, paneReviewSubmitWait = start, typed, submit })
	paneReviewStartWait, paneReviewTypedWait, paneReviewSubmitWait = 20*time.Millisecond, 20*time.Millisecond, 20*time.Millisecond
}

func scripted(screens ...string) func(h *screenHost, agent string, n int) string {
	return func(h *screenHost, agent string, n int) string {
		return screens[min(n, len(screens))-1]
	}
}

func TestPaneReviewRunsBeforeTheReviewerIsPrompted(t *testing.T) {
	r, _ := paneReviewRig(t, scripted(
		"› "+paneReviewText,
		">> Code review started: Review the current code changes <<\n• Working",
		">> Code review started: Review the current code changes <<\n\n<< Code review finished >>",
	), Reviewer{Provider: "codex"})

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	agent := "rloop-2kuxv-p3-implemen-8lgad-r1"
	want := []string{
		"SessionHost.Start pane-2 " + agent + " codex []",
		"SessionHost.SendText " + agent + " " + paneReviewText,
		"SessionHost.Screen " + agent,
		"SessionHost.SendKeys " + agent + " enter",
		"SessionHost.Screen " + agent,
		"SessionHost.Screen " + agent,
		"SessionHost.Screen " + agent,
		`SessionHost.Prompt ` + agent + ` "review r1" false 0s`,
	}
	if got := r.callsFrom("SessionHost.Start pane-2", "SessionHost.SendText", "SessionHost.SendKeys", "SessionHost.Screen", "SessionHost.Prompt "+agent); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls =\n%s", strings.Join(got, "\n"))
	}
	if r.reviews[0]["ReviewRan"] != true || r.reviews[0]["ReviewCommand"] != paneReviewText {
		t.Fatalf("vars ReviewRan=%v ReviewCommand=%v", r.reviews[0]["ReviewRan"], r.reviews[0]["ReviewCommand"])
	}
	for _, kind := range []string{"review-running", "review-ran"} {
		if evs := r.events(kind); len(evs) != 1 || evs[0].Fields["reviewer"] != "codex" {
			t.Errorf("%s events = %+v", kind, evs)
		}
	}
	if f := r.events("review-find"); len(f) != 1 || f[0].Fields["command"] != paneReviewText {
		t.Errorf("review-find = %+v", f)
	}
}

func TestPaneReviewThatNeverStartsFailsNamingTheReviewer(t *testing.T) {
	fastPaneReview(t)
	r, _ := paneReviewRig(t, scripted("› ", "› /review Review the current code changes"), Reviewer{Provider: "codex"})

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer codex: review did not start within 20ms" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(r.reviews) != 0 {
		t.Fatalf("reviewer was prompted: %+v", r.reviews)
	}
}

func TestPaneReviewThatNeverFinishesFailsWithinTheReviewTimeout(t *testing.T) {
	r, _ := paneReviewRig(t, scripted(">> Code review started: x <<"), Reviewer{Provider: "codex"})
	r.worker.Ref.Kind.Row.ReviewTimeout = 20 * time.Millisecond

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer codex: review did not finish within 20ms" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(r.reviews) != 0 {
		t.Fatalf("reviewer was prompted: %+v", r.reviews)
	}
}

func TestInterruptedPaneReviewFails(t *testing.T) {
	for _, screen := range [][]string{
		{"› ", ">> Code review started: x <<\n■ Review was interrupted. Please re-run /review."},
		{"■ Review was interrupted."},
		{">> Code review started: x <<", "Reviewer failed to output a response."},
	} {
		r, _ := paneReviewRig(t, scripted(screen...), Reviewer{Provider: "codex"})

		out := r.run()

		if out.State != StepFailed || !strings.HasPrefix(out.Reason, "reviewer codex: review failed: ") || strings.Contains(out.Reason, "screen") {
			t.Fatalf("screens %q: outcome = %+v", screen, out)
		}
		if len(r.reviews) != 0 || len(r.events("review-ran")) != 0 {
			t.Fatalf("screens %q: review went on", screen)
		}
	}
}

func TestReviewerWithoutMarkersIsPromptedAsBefore(t *testing.T) {
	r, _ := paneReviewRig(t, scripted("› "+paneReviewText, ">> Code review started <<\n<< Code review finished >>"), Reviewer{Provider: "claude"}, Reviewer{Provider: "codex"})

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	claude := "rloop-2kuxv-p3-implemen-q1s68-r1"
	if got := r.callsFrom("SessionHost.SendText "+claude, "SessionHost.SendKeys "+claude, "SessionHost.Screen "+claude); len(got) != 0 {
		t.Fatalf("claude reviewer driven: %q", got)
	}
	byPrompt := map[string]any{}
	for _, v := range r.reviews {
		byPrompt[v["ReviewCommand"].(string)] = v["ReviewRan"]
	}
	if !reflect.DeepEqual(byPrompt, map[string]any{"/claude-review": false, paneReviewText: true}) {
		t.Fatalf("ReviewRan by command = %v", byPrompt)
	}
}

func TestPaneReviewersRunTheirReviewsAtTheSameTime(t *testing.T) {
	r, _ := paneReviewRig(t, func(h *screenHost, agent string, n int) string {
		if h.enters[agent] == 0 {
			return "› " + paneReviewText
		}
		if len(h.entered) < 2 {
			return ">> Code review started <<"
		}
		return ">> Code review started <<\n<< Code review finished >>"
	}, Reviewer{Provider: "codex"}, Reviewer{Provider: "codex", Name: "second"})
	r.worker.Ref.Kind.Row.ReviewTimeout = 2 * time.Second

	begin := time.Now()
	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	if took := time.Since(begin); took > time.Second {
		t.Fatalf("took %s", took)
	}
	if n := len(r.events("review-ran")); n != 2 {
		t.Fatalf("review-ran events = %d", n)
	}
}

func TestAPaneReviewStuckOnADialogRaisesABlockerWithKeysAndResumesAfterThem(t *testing.T) {
	fastPaneReview(t)
	var answered atomic.Bool
	r, _ := paneReviewRig(t, func(h *screenHost, agent string, n int) string {
		if !answered.Load() {
			return "Trust this folder?   \n› 1. Yes\n  2. No\n\n"
		}
		return ">> Code review started: x <<\n<< Code review finished >>"
	}, Reviewer{Provider: "codex"})

	out, obs := r.runRaising(func(n int, b Blocker) Resolution {
		answered.Store(true)
		return Resolution{Action: "keys", By: "watchdog", Citation: "trust", Keys: []string{"enter"}}
	})

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	if len(obs.blockers) != 1 {
		t.Fatalf("blockers = %+v", obs.blockers)
	}
	b := obs.blockers[0]
	if b.Reason != "reviewer codex: review text did not reach the composer within 20ms" || b.Excerpt != "Trust this folder?\n› 1. Yes\n  2. No" || !reflect.DeepEqual(b.Actions, []string{"retry", "keys", "switch", "skip", "block", "stop"}) {
		t.Fatalf("blocker = %+v", b)
	}
	if len(r.host.Splits) != 1 || len(r.reviews) != 1 || r.reviews[0]["ReviewRan"] != true {
		t.Fatalf("splits %d, reviews %+v", len(r.host.Splits), r.reviews)
	}
	if got := r.callsFrom("SessionHost.SendText"); len(got) != 1 {
		t.Errorf("the review was sent again: %q", got)
	}
}

func TestAPaneReviewWhoseFirstEnterIsSwallowedPressesEnterAgain(t *testing.T) {
	fastPaneReview(t)
	r, _ := paneReviewRig(t, func(h *screenHost, agent string, n int) string {
		if h.enters[agent] < 2 {
			return "› /review Review the current\n  code changes"
		}
		return ">> Code review started: Review the current code changes <<\n<< Code review finished >>"
	}, Reviewer{Provider: "codex"})

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	agent := "rloop-2kuxv-p3-implemen-8lgad-r1"
	if got := r.callsFrom("SessionHost.SendKeys " + agent); !reflect.DeepEqual(got, []string{"SessionHost.SendKeys " + agent + " enter", "SessionHost.SendKeys " + agent + " enter"}) {
		t.Fatalf("keys = %q", got)
	}
	if len(r.reviews) != 1 || r.reviews[0]["ReviewRan"] != true {
		t.Fatalf("reviews %+v", r.reviews)
	}
	if ran := r.events("review-ran"); len(ran) != 1 || ran[0].Fields["presses"] != "2" {
		t.Fatalf("review-ran = %+v", ran)
	}
}

func TestAPaneReviewPressesEnterAtMostFiveTimes(t *testing.T) {
	fastPaneReview(t)
	r, _ := paneReviewRig(t, scripted("› "+paneReviewText), Reviewer{Provider: "codex"})

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer codex: review did not start within 20ms" {
		t.Fatalf("outcome = %+v", out)
	}
	if got := r.callsFrom("SessionHost.SendKeys"); len(got) != 5 {
		t.Fatalf("keys = %q", got)
	}
}

func TestAPaneReviewWhoseTextNeverReachesTheComposerFails(t *testing.T) {
	fastPaneReview(t)
	r, _ := paneReviewRig(t, scripted("› "), Reviewer{Provider: "codex"})

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer codex: review text did not reach the composer within 20ms" {
		t.Fatalf("outcome = %+v", out)
	}
	if got := r.callsFrom("SessionHost.SendKeys"); len(got) != 0 {
		t.Fatalf("enter pressed on an empty composer: %q", got)
	}
	if len(r.reviews) != 0 {
		t.Fatalf("reviewer was prompted: %+v", r.reviews)
	}
}

func TestAPaneReviewBlockerKeepsItsReasonToOneLineAndTheScreenInTheExcerpt(t *testing.T) {
	fastPaneReview(t)
	screen := "› " + paneReviewText + "\n\n  ? for shortcuts   100% context left"
	r, _ := paneReviewRig(t, scripted(screen), Reviewer{Provider: "codex"})

	_, obs := r.runRaising(func(n int, b Blocker) Resolution {
		return Resolution{Action: "block", By: "watchdog"}
	})

	if len(obs.blockers) != 1 {
		t.Fatalf("blockers = %+v", obs.blockers)
	}
	b := obs.blockers[0]
	if b.Reason != "reviewer codex: review did not start within 20ms" {
		t.Fatalf("reason = %q", b.Reason)
	}
	if !strings.Contains(b.Excerpt, "? for shortcuts") {
		t.Fatalf("excerpt = %q", b.Excerpt)
	}
	text := blockerText("b1", b)
	if first, rest, _ := strings.Cut(text, "\n"); strings.Contains(first, "shortcuts") || !strings.Contains(rest, "› "+paneReviewText+"\n\n  ? for shortcuts") {
		t.Fatalf("routed text = %q", text)
	}
}
