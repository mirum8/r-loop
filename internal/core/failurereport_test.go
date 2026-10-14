package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type reportingHost struct {
	fakeSessionHost
	write string
}

func (h *reportingHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	if h.write != "" && strings.HasPrefix(text, "failure report ") {
		path := strings.Fields(strings.TrimPrefix(text, "failure report "))[0]
		if err := os.WriteFile(path, []byte(h.write), 0o644); err != nil {
			return err
		}
	}
	return h.fakeSessionHost.Prompt(agent, text, wait, timeout)
}

func haltedDog(t *testing.T, status RunStatus) (*Watchdog, *reportingHost, *fakeStore, *fakeFace) {
	t.Helper()
	host := &reportingHost{}
	store := &fakeStore{}
	face := &fakeFace{}
	dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})
	dog.Face = face
	dog.RunDir = t.TempDir()
	if err := store.Append(dog.RunID, Record{Kind: RecordRun, Run: status, Reason: "watchdog: off the plan"}); err != nil {
		t.Fatal(err)
	}
	return dog, host, store, face
}

func failurePrompts(host *reportingHost) []string {
	var out []string
	for _, c := range host.Calls() {
		if strings.HasPrefix(c, "SessionHost.Prompt") && strings.Contains(c, "failure report ") {
			out = append(out, c)
		}
	}
	return out
}

func lastEvent(face *fakeFace) Event {
	evs := face.events()
	if len(evs) == 0 {
		return Event{}
	}
	return evs[len(evs)-1]
}

func TestFailureReportTextNamesThePathAndTheReason(t *testing.T) {
	got := FailureReportText("/repo/.r-loop/runs/run-1/failure-report.md", "watchdog: off the plan")

	for _, want := range []string{"failure report /repo/.r-loop/runs/run-1/failure-report.md", "watchdog: off the plan", "events.jsonl", "signals.jsonl", "remedies.jsonl", "report.md", "transcripts", "r-loop resume"} {
		if !strings.Contains(got, want) {
			t.Errorf("failure report text missing %q:\n%s", want, got)
		}
	}
}

func TestAHaltedRunAsksTheWatchdogForAFailureReportAndRecordsIt(t *testing.T) {
	dog, host, store, face := haltedDog(t, RunHalted)
	host.write = "# Verdict\n\ndriver bug\n"
	path := filepath.Join(dog.RunDir, "failure-report.md")

	dog.FailureReport(context.Background(), 7*time.Minute)

	prompts := failurePrompts(host)
	if len(prompts) != 1 || !strings.Contains(prompts[0], path) || !strings.Contains(prompts[0], "watchdog: off the plan") || !strings.HasSuffix(prompts[0], " true 7m0s") {
		t.Fatalf("failure report prompts %q", prompts)
	}
	if ev := lastEvent(face); ev.Kind != "failure-report" || ev.Fields["path"] != path {
		t.Errorf("last event %+v", ev)
	}
	st, _ := store.Load(dog.RunID)
	if ev := st.Events[len(st.Events)-1]; ev.Kind != "failure-report" || ev.Fields["path"] != path {
		t.Errorf("recorded event %+v", ev)
	}
}

func TestAWatchdogThatWritesNoFailureReportIsRecordedAsMissing(t *testing.T) {
	dog, host, store, face := haltedDog(t, RunHalted)

	dog.FailureReport(context.Background(), time.Minute)

	if len(failurePrompts(host)) != 1 {
		t.Fatalf("calls %q", host.Calls())
	}
	if ev := lastEvent(face); ev.Kind != "failure-report-missing" || ev.Fields["reason"] == "" {
		t.Errorf("last event %+v", ev)
	}
	st, _ := store.Load(dog.RunID)
	if ev := st.Events[len(st.Events)-1]; ev.Kind != "failure-report-missing" {
		t.Errorf("recorded event %+v", ev)
	}
}

func TestAFailureReportLeftFromAnEarlierHaltIsNotReportedAgain(t *testing.T) {
	dog, _, _, face := haltedDog(t, RunHalted)
	path := filepath.Join(dog.RunDir, "failure-report.md")
	if err := os.WriteFile(path, []byte("# Verdict\n\nthe earlier halt\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dog.FailureReport(context.Background(), time.Minute)

	if ev := lastEvent(face); ev.Kind != "failure-report-missing" {
		t.Errorf("last event %+v", ev)
	}
}

func TestAnEmptyFailureReportIsRecordedAsMissing(t *testing.T) {
	dog, host, _, face := haltedDog(t, RunHalted)
	host.write = ""
	if err := os.WriteFile(filepath.Join(dog.RunDir, "failure-report.md"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	dog.FailureReport(context.Background(), time.Minute)

	if ev := lastEvent(face); ev.Kind != "failure-report-missing" {
		t.Errorf("last event %+v", ev)
	}
}

func TestAFinishedRunAsksForNoFailureReport(t *testing.T) {
	dog, host, _, face := haltedDog(t, RunFinished)
	host.write = "x"

	dog.FailureReport(context.Background(), time.Minute)

	if p := failurePrompts(host); len(p) != 0 || len(face.events()) != 0 {
		t.Errorf("prompts %q, events %+v", p, face.events())
	}
}

func TestAnInterruptedRunAsksForNoFailureReport(t *testing.T) {
	dog, host, _, face := haltedDog(t, RunHalted)
	host.write = "x"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dog.FailureReport(ctx, time.Minute)

	if p := failurePrompts(host); len(p) != 0 || len(face.events()) != 0 {
		t.Errorf("prompts %q, events %+v", p, face.events())
	}
}

func TestAGoneWatchdogIsAskedForNoFailureReport(t *testing.T) {
	dog, host, _, face := haltedDog(t, RunHalted)
	host.write = "x"
	dog.gone = true

	dog.FailureReport(context.Background(), time.Minute)

	if p := failurePrompts(host); len(p) != 0 || len(face.events()) != 0 {
		t.Errorf("prompts %q, events %+v", p, face.events())
	}
}

func TestReportNamesTheDiagnosisUnderHalt(t *testing.T) {
	st := RunState{ID: "run-1", Status: RunHalted, Events: []Event{
		{Kind: "halt", Fields: map[string]string{"reason": "watchdog: off the plan"}},
		{Kind: "failure-report", Fields: map[string]string{"path": "/r/runs/run-1/failure-report.md"}},
	}}

	got := Report(st, threePhasePlan())

	if !strings.Contains(got, "## Halt\n\n- watchdog: off the plan\n- diagnosis: /r/runs/run-1/failure-report.md\n") {
		t.Fatalf("report:\n%s", got)
	}
}

func TestReportDropsADiagnosisFromBeforeAResume(t *testing.T) {
	st := RunState{ID: "run-1", Status: RunHalted, Events: []Event{
		{Kind: "failure-report", Fields: map[string]string{"path": "/r/old.md"}},
		{Kind: "human", Fields: map[string]string{"what": "resume"}},
		{Kind: "halt", Fields: map[string]string{"reason": "again"}},
	}}

	if got := Report(st, threePhasePlan()); strings.Contains(got, "diagnosis:") {
		t.Fatalf("report:\n%s", got)
	}
}
