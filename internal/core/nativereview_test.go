package core

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const execReviewCommand = "codex review --uncommitted"

func execReviewRig(t *testing.T, runner ReviewRunner, reviewers ...Reviewer) *reviewRig {
	t.Helper()
	r := newReviewRig(t, reviewers...)
	if f, ok := runner.(*fakeReviewRunner); ok {
		f.Shared = r.shared
	}
	if runner != nil {
		r.sm.ReviewRunner = runner
	}
	resolve := r.sm.Resolve
	r.sm.Resolve = func(provider, model, effort, askURL, mcpConfigPath, dir string) (ProviderArgs, error) {
		a, err := resolve(provider, model, effort, askURL, mcpConfigPath, dir)
		if provider == "codex" {
			a.Review, a.ReviewExec = "", execReviewCommand
		}
		return a, err
	}
	r.behave = func(vars map[string]any) { writeFindings(t, vars, "ok", 0) }
	return r
}

func TestANativeExecReviewRunsInTheWorktreeAndItsStdoutBecomesTheNativeReport(t *testing.T) {
	runner := &fakeReviewRunner{Outputs: []string{"- [P1] Include the first element — main.go:7-7\n"}}
	r := execReviewRig(t, runner, Reviewer{Provider: "codex"})

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	agent := "rloop-2kuxv-p3-implemen-8lgad-r1"
	want := []string{
		"SessionHost.Start pane-2 " + agent + " codex []",
		"ReviewRunner.RunReview " + r.worker.Dir + " " + execReviewCommand + " 1h0m0s",
		`SessionHost.Prompt ` + agent + ` "review r1" false 0s`,
	}
	if got := r.callsFrom("SessionHost.Start pane-2", "ReviewRunner.", "SessionHost.Prompt "+agent); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls =\n%s", strings.Join(got, "\n"))
	}
	data, err := os.ReadFile(filepath.Join(r.reviews[0]["ArtifactsDir"].(string), "native-review.txt"))
	if err != nil || string(data) != "- [P1] Include the first element — main.go:7-7\n" {
		t.Fatalf("native-review.txt = %q, %v", data, err)
	}
	if r.reviews[0]["ReviewRan"] != true || r.reviews[0]["ReviewCommand"] != execReviewCommand {
		t.Fatalf("vars ReviewRan=%v ReviewCommand=%v", r.reviews[0]["ReviewRan"], r.reviews[0]["ReviewCommand"])
	}
	for _, kind := range []string{"review-running", "review-ran"} {
		if evs := r.events(kind); len(evs) != 1 || !reflect.DeepEqual(evs[0].Fields, map[string]string{"reviewer": "codex", "step": "implement"}) {
			t.Errorf("%s events = %+v", kind, evs)
		}
	}
	if f := r.events("review-find"); len(f) != 1 || f[0].Fields["command"] != execReviewCommand || f[0].Fields["state"] != "ok" {
		t.Errorf("review-find = %+v", f)
	}
}

func TestOtherReviewersArePromptedBeforeTheExecReviewRuns(t *testing.T) {
	r := execReviewRig(t, &fakeReviewRunner{Outputs: []string{"(none)\n"}}, Reviewer{Provider: "codex"}, Reviewer{Provider: "codex", Name: "ui", Prompt: "review-ui"})

	out := r.run()

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	got := r.callsFrom("SessionHost.Prompt rloop-2kuxv-p3-implemen-", "ReviewRunner.")
	if len(got) != 3 || !strings.Contains(got[0], "review r1") || !strings.HasPrefix(got[1], "ReviewRunner.RunReview") || !strings.HasPrefix(got[2], "SessionHost.Prompt rloop-2kuxv-p3-implemen-8lgad-r1") {
		t.Fatalf("calls =\n%s", strings.Join(got, "\n"))
	}
	var order []string
	for _, v := range r.reviews {
		order = append(order, v["prompt"].(string))
	}
	if !reflect.DeepEqual(order, []string{"review-ui", "review"}) {
		t.Fatalf("prompt order = %v", order)
	}
}

func TestAFailedExecReviewRaisesARetryableBlockerNamingTheCommandAndError(t *testing.T) {
	runner := &fakeReviewRunner{Errs: []error{errors.New("exit 1: stream disconnected")}}
	r := execReviewRig(t, runner, Reviewer{Provider: "codex"})

	out, obs := r.runRaising(then(Resolution{Action: "block", By: "watchdog"}))

	reason := "reviewer codex: native review `" + execReviewCommand + "`: exit 1: stream disconnected"
	if out.State != StepFailed || out.Reason != reason {
		t.Fatalf("outcome = %+v", out)
	}
	if len(obs.blockers) != 1 || obs.blockers[0].Reason != reason || !reflect.DeepEqual(obs.blockers[0].Actions, []string{"retry", "switch", "skip", "block", "stop"}) {
		t.Fatalf("blockers = %+v", obs.blockers)
	}
	if len(r.reviews) != 0 {
		t.Fatalf("reviewer was prompted: %+v", r.reviews)
	}
	if f := r.events("review-find"); len(f) != 1 || f[0].Fields["state"] != "failed" {
		t.Errorf("review-find = %+v", f)
	}
}

func TestAFailedExecReviewsBlockerExplainsThePaneWasNeverPromptedInsteadOfShowingIt(t *testing.T) {
	runner := &fakeReviewRunner{Errs: []error{errors.New("exit 7: review refused")}}
	r := execReviewRig(t, runner, Reviewer{Provider: "codex"})

	_, obs := r.runRaising(then(Resolution{Action: "block", By: "watchdog"}))

	if len(obs.blockers) != 1 {
		t.Fatalf("blockers = %+v", obs.blockers)
	}
	if expected := "The driver ran `" + execReviewCommand + "` itself and it failed before the reviewer pane was prompted: the pane has done nothing, and an addendum never reaches it. Retry runs the same command again."; obs.blockers[0].Excerpt != expected {
		t.Fatalf("excerpt = %q, expected %q", obs.blockers[0].Excerpt, expected)
	}
}

func TestAFailedExecReviewWithoutARaiserFailsTheStep(t *testing.T) {
	runner := &fakeReviewRunner{Errs: []error{errors.New("timed out after 1h0m0s")}}
	r := execReviewRig(t, runner, Reviewer{Provider: "codex"})

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer codex: native review `"+execReviewCommand+"`: timed out after 1h0m0s" {
		t.Fatalf("outcome = %+v", out)
	}
}

func TestAnExecReviewWithBlankStdoutFailsAsProducedNoOutput(t *testing.T) {
	r := execReviewRig(t, &fakeReviewRunner{Outputs: []string{" \n\t\n"}}, Reviewer{Provider: "codex"})

	out := r.run()

	if out.State != StepFailed || out.Reason != "reviewer codex: native review `"+execReviewCommand+"` produced no output" {
		t.Fatalf("outcome = %+v", out)
	}
	if len(r.reviews) != 0 {
		t.Fatalf("reviewer was prompted: %+v", r.reviews)
	}
}

func TestRetryingAFailedExecReviewRunsTheCommandAgain(t *testing.T) {
	runner := &fakeReviewRunner{Outputs: []string{"", "(none)\n"}, Errs: []error{errors.New("exit 1: boom"), nil}}
	r := execReviewRig(t, runner, Reviewer{Provider: "codex"})

	out, obs := r.runRaising(then(Resolution{Action: "retry", By: "maintainer"}))

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	if len(obs.blockers) != 1 {
		t.Fatalf("blockers = %+v", obs.blockers)
	}
	if got := r.callsFrom("ReviewRunner."); len(got) != 2 {
		t.Fatalf("runs = %q", got)
	}
	if len(r.reviews) != 1 || r.reviews[0]["ReviewRan"] != true {
		t.Fatalf("reviews = %+v", r.reviews)
	}
	data, err := os.ReadFile(filepath.Join(r.reviews[0]["ArtifactsDir"].(string), "native-review.txt"))
	if err != nil || string(data) != "(none)\n" {
		t.Fatalf("native-review.txt = %q, %v", data, err)
	}
}

func TestSwitchingAnExecReviewerToClaudeUsesTheInSessionReview(t *testing.T) {
	runner := &fakeReviewRunner{Errs: []error{errors.New("exit 1: boom")}}
	r := execReviewRig(t, runner, Reviewer{Provider: "codex"})
	r.behave = func(vars map[string]any) { writeReview(t, vars, "ok", 0) }

	out, _ := r.runRaising(then(Resolution{Action: "switch", By: "maintainer", Provider: "claude"}))

	if out.State != StepOK {
		t.Fatalf("outcome = %+v", out)
	}
	if got := r.callsFrom("ReviewRunner."); len(got) != 1 {
		t.Fatalf("runs = %q", got)
	}
	if len(r.reviews) != 1 || r.reviews[0]["ReviewRan"] != false || r.reviews[0]["ReviewCommand"] != "/claude-review" {
		t.Fatalf("reviews = %+v", r.reviews)
	}
}

func TestAnExecReviewerWithoutARunnerFails(t *testing.T) {
	r := execReviewRig(t, nil, Reviewer{Provider: "codex"})

	out := r.run()

	if out.State != StepFailed || !strings.Contains(out.Reason, "reviewer codex: native review `"+execReviewCommand+"`: no review runner") {
		t.Fatalf("outcome = %+v", out)
	}
}
