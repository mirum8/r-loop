package core

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func newIntake(host *fakeSessionHost, pane string) *Intake {
	return &Intake{
		Host: host, Prompts: &fakePrompts{Texts: map[string]string{"intake": "parse this"}},
		Provider: ProviderArgs{Kind: "codex", Args: []string{"-c", "model=gpt-5.6-mini"}},
		Name:     "rloop-intake-42", Root: "/repo", Pane: pane, Poll: time.Millisecond,
	}
}

func TestIntakeReturnsTheAcceptedArgvAndClosesItsWorkspace(t *testing.T) {
	host := &fakeSessionHost{}
	accepted := make(chan []string, 1)
	accepted <- []string{"docs/x/todo.md", "--phases", "3"}

	argv, err := newIntake(host, "").Run(context.Background(), accepted)

	if err != nil || !reflect.DeepEqual(argv, []string{"docs/x/todo.md", "--phases", "3"}) {
		t.Fatalf("argv %q err %v", argv, err)
	}
	want := []string{
		"SessionHost.Open /repo ◆ intake map[]",
		"SessionHost.Start pane-1 rloop-intake-42 codex [-c model=gpt-5.6-mini]",
		`SessionHost.Prompt rloop-intake-42 "parse this" false 0s`,
		"SessionHost.Close ws-1",
	}
	if got := host.Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls:\n%q\nwant:\n%q", got, want)
	}
}

func TestIntakeInsideHerdrSplitsBesideTheDriverAndClosesThePane(t *testing.T) {
	host := &fakeSessionHost{}
	accepted := make(chan []string, 1)
	accepted <- []string{"todo.md"}

	if _, err := newIntake(host, "driver-pane").Run(context.Background(), accepted); err != nil {
		t.Fatal(err)
	}

	calls := host.Calls()
	if calls[0] != "SessionHost.Split driver-pane right /repo" || calls[len(calls)-1] != "SessionHost.ClosePane pane-1" {
		t.Fatalf("calls %q", calls)
	}
}

func TestIntakeWhoseAgentIsGoneFailsAndClosesItsWorkspace(t *testing.T) {
	host := &fakeSessionHost{States: map[string]AgentState{"rloop-intake-42": AgentGone}}

	_, err := newIntake(host, "").Run(context.Background(), make(chan []string))

	calls := host.Calls()
	if !errors.Is(err, ErrIntakeGone) || calls[len(calls)-1] != "SessionHost.Close ws-1" {
		t.Fatalf("err %v calls %q", err, calls)
	}
}

func TestIntakeCancelledReturnsTheContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newIntake(&fakeSessionHost{}, "").Run(ctx, make(chan []string))

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestIntakeThatCannotStartClosesWhatItOpened(t *testing.T) {
	host := &fakeSessionHost{}
	in := newIntake(host, "")
	in.Prompts = &fakePrompts{Err: errors.New("bad template")}

	_, err := in.Run(context.Background(), make(chan []string))

	calls := host.Calls()
	if err == nil || calls[len(calls)-1] != "SessionHost.Close ws-1" {
		t.Fatalf("err %v calls %q", err, calls)
	}
}
