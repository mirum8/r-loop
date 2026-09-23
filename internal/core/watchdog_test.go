package core

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type varsCapture struct {
	name string
	vars map[string]any
}

func (p *varsCapture) Render(name string, vars map[string]any) (string, string, error) {
	p.name, p.vars = name, vars
	return "watch the run", "embedded:" + name, nil
}

func newWatchdog(host SessionHost, store Store, provider ProviderArgs) *Watchdog {
	return &Watchdog{
		Host: host, Prompts: &varsCapture{}, Store: store, Provider: provider,
		RunID: "run-1", Root: "/repo", Pane: "driver-pane", TodoPath: "docs/x/todo.md", SpecDir: "docs/x", RunDir: "/repo/.r-loop/runs/run-1",
		Allow: []string{"deps", "ports"},
		Sleep: func(time.Duration) {},
	}
}

func TestWatchdogStartSplitsThenStartsThenPromptsWithoutWait(t *testing.T) {
	host := &fakeSessionHost{}
	dog := newWatchdog(host, &fakeStore{}, ProviderArgs{Kind: "claude", Args: []string{"--model", "sonnet", "--effort", "low", "--mcp-config", "/run/wd.json"}})

	if err := dog.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"SessionHost.AgentPane rloop-wd-run-1",
		"SessionHost.Split driver-pane right /repo",
		"SessionHost.Start pane-1 rloop-wd-run-1 claude [--model sonnet --effort low --mcp-config /run/wd.json]",
		`SessionHost.Prompt rloop-wd-run-1 "watch the run" false 0s`,
	}
	if got := host.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls\n got %q\nwant %q", got, want)
	}
	p := dog.Prompts.(*varsCapture)
	wantVars := map[string]any{"TodoPath": "docs/x/todo.md", "SpecDir": "docs/x", "RunDir": "/repo/.r-loop/runs/run-1", "Allow": []string{"deps", "ports"}, "Unattended": false}
	if p.name != "watchdog" || !reflect.DeepEqual(p.vars, wantVars) {
		t.Errorf("rendered %s %v", p.name, p.vars)
	}
}

func TestWatchdogWithoutModelAndEffortStartsWithNoSuchArgs(t *testing.T) {
	host := &fakeSessionHost{}
	dog := newWatchdog(host, &fakeStore{}, ProviderArgs{Kind: "codex"})

	if err := dog.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := host.Calls()[2]; got != "SessionHost.Start pane-1 rloop-wd-run-1 codex []" {
		t.Errorf("start %q", got)
	}
}

func TestWatchdogStartFailureNamesTheHostError(t *testing.T) {
	host := &fakeSessionHost{Err: errors.New("herdr: pane_not_found: gone")}
	dog := newWatchdog(host, &fakeStore{}, ProviderArgs{Kind: "claude"})

	err := dog.Start(context.Background())

	if err == nil || !strings.Contains(err.Error(), "pane_not_found") {
		t.Fatalf("err %v", err)
	}
}

func TestWatchdogStopClosesThePaneItOpened(t *testing.T) {
	host := &fakeSessionHost{}
	dog := newWatchdog(host, &fakeStore{}, ProviderArgs{Kind: "claude"})
	if err := dog.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	if err := dog.Stop(); err != nil {
		t.Fatal(err)
	}

	if got := host.Calls(); got[len(got)-1] != "SessionHost.ClosePane pane-1" {
		t.Errorf("calls %q", got)
	}
}

func TestWatchdogStartClosesTheStaleWatchdogOfADeadDriverFirst(t *testing.T) {
	host := &fakeSessionHost{Panes: map[string]string{"rloop-wd-run-1": "old-pane"}}
	store := &fakeStore{}
	dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})

	if err := dog.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"SessionHost.AgentPane rloop-wd-run-1",
		"SessionHost.ClosePane old-pane",
		"SessionHost.Split driver-pane right /repo",
		"SessionHost.Start pane-1 rloop-wd-run-1 claude []",
		`SessionHost.Prompt rloop-wd-run-1 "watch the run" false 0s`,
	}
	if got := host.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls\n got %q\nwant %q", got, want)
	}
	var stale *Event
	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == "watchdog-stale-closed" {
			stale = rec.Event
		}
	}
	if stale == nil || stale.Fields["pane"] != "old-pane" {
		t.Errorf("records %+v", store.Records["run-1"])
	}
}

func TestWatchdogStartRecordsItselfBeforeItOpensAPane(t *testing.T) {
	shared := &callLog{}
	host := &fakeSessionHost{callLog: callLog{Shared: shared}}
	store := &fakeStore{callLog: callLog{Shared: shared}}
	dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})

	if err := dog.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := shared.Calls()
	if len(calls) < 3 || calls[1] != "Store.Append run-1 event" || calls[2] != "SessionHost.Split driver-pane right /repo" {
		t.Fatalf("calls %q", calls)
	}
	if rec := store.Records["run-1"][0]; rec.Event.Kind != "watchdog-start" {
		t.Errorf("recorded %+v", rec.Event)
	}
}

func TestWatchdogWithNoDriverPaneOpensItsOwnWorkspaceAndStopClosesIt(t *testing.T) {
	host := &fakeSessionHost{}
	dog := newWatchdog(host, &fakeStore{}, ProviderArgs{Kind: "claude"})
	dog.Pane = ""

	if err := dog.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := dog.Stop(); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"SessionHost.AgentPane rloop-wd-run-1",
		"SessionHost.Open /repo ◆ watchdog map[]",
		"SessionHost.Tag ws-1 map[rloop:◆ run run-1]",
		"SessionHost.Start pane-1 rloop-wd-run-1 claude []",
		`SessionHost.Prompt rloop-wd-run-1 "watch the run" false 0s`,
		"SessionHost.Close ws-1",
	}
	if got := host.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls\n got %q\nwant %q", got, want)
	}
}

func TestWatchdogStopBeforeStartTouchesNothing(t *testing.T) {
	host := &fakeSessionHost{}
	dog := newWatchdog(host, &fakeStore{}, ProviderArgs{Kind: "claude"})

	if err := dog.Stop(); err != nil {
		t.Fatal(err)
	}

	if got := host.Calls(); len(got) != 0 {
		t.Errorf("calls %q", got)
	}
}

func TestUnreachableIsRecordedBeforeTheWatchdogIsDropped(t *testing.T) {
	host := &blockedHost{}
	host.States = map[string]AgentState{"rloop-wd-run-1": AgentGone}
	store := &fakeStore{Err: errors.New("disk full")}
	face := &fakeFace{}
	dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})
	dog.Face = face

	err := dog.Notify("step started phase-2/implement", false, 0)
	dog.Notify("step ended phase-2/implement ok ", false, 0)

	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("err %v", err)
	}
	if host.prompts != 2 {
		t.Errorf("prompts %d, want the watchdog kept after a failed record", host.prompts)
	}
	if len(face.Events) != 0 {
		t.Errorf("emitted %+v", face.Events)
	}
}

type blockedHost struct {
	fakeSessionHost
	mu      sync.Mutex
	prompts int
}

func (h *blockedHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	h.mu.Lock()
	h.prompts++
	h.mu.Unlock()
	return errors.New("herdr agent prompt: herdr: agent_blocked: waiting on a permission")
}

func TestNotifyRecordsWatchdogUnreachableWhenABlockedWatchdogIsGone(t *testing.T) {
	host := &blockedHost{}
	host.States = map[string]AgentState{"rloop-wd-run-1": AgentGone}
	store := &fakeStore{}
	face := &fakeFace{}
	dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})
	dog.Face = face
	var slept []time.Duration
	dog.Sleep = func(d time.Duration) { slept = append(slept, d) }

	err := dog.Notify("step started phase-2/implement", false, 0)
	dog.Notify("step ended phase-2/implement ok ", false, 0)

	if err == nil || !strings.Contains(err.Error(), "agent_blocked") {
		t.Errorf("err %v", err)
	}
	if host.prompts != 1 {
		t.Errorf("prompts %d, want 1 then none once unreachable", host.prompts)
	}
	if len(slept) != 0 {
		t.Errorf("slept %v", slept)
	}
	var kinds []string
	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordEvent {
			kinds = append(kinds, rec.Event.Kind)
		}
	}
	if !reflect.DeepEqual(kinds, []string{"watchdog-unreachable"}) {
		t.Errorf("recorded %v", kinds)
	}
	if len(face.Events) != 1 || face.Events[0].Kind != "watchdog-unreachable" {
		t.Errorf("emitted %+v", face.Events)
	}
}

func TestNotifyReturnsAStateErrorWithoutDroppingTheWatchdog(t *testing.T) {
	host := &blockedHost{}
	host.Err = errors.New("herdr: connection refused")
	store := &fakeStore{}
	dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})

	err := dog.Notify("step started phase-2/implement", false, 0)

	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err %v", err)
	}
	if !dog.live() {
		t.Error("watchdog dropped on a State error")
	}
	if len(store.Records["run-1"]) != 0 {
		t.Errorf("recorded %+v", store.Records["run-1"])
	}
}

func TestNotifyIsOnePromptWhenTheWatchdogAccepts(t *testing.T) {
	host := &fakeSessionHost{}
	dog := newWatchdog(host, &fakeStore{}, ProviderArgs{Kind: "claude"})

	if err := dog.Notify("hello", true, time.Minute); err != nil {
		t.Fatal(err)
	}

	if got := host.Calls(); !reflect.DeepEqual(got, []string{`SessionHost.Prompt rloop-wd-run-1 "hello" true 1m0s`}) {
		t.Errorf("calls %q", got)
	}
}

func TestWatchSendsStepStartedAndStepEndedToTheWatchdog(t *testing.T) {
	host := &fakeSessionHost{}
	store := &fakeStore{}
	w := newWatch(store)
	w.Dog = newWatchdog(host, store, ProviderArgs{Kind: "claude"})
	ref := implementRef(2, 1)
	ref.Base = "abc123"

	w.StepStarted(ref, &Session{Agent: "rloop-p2-implement", Dir: "/repo/.r-loop/wt/phase-2"})
	w.StepEnded(ref, Outcome{State: StepFailed, Reason: "tests red"})
	if err := w.Dog.Stop(); err != nil {
		t.Fatal(err)
	}

	want := []string{
		`SessionHost.Prompt rloop-wd-run-1 "step started phase-2/implement agent rloop-p2-implement worktree /repo/.r-loop/wt/phase-2 base abc123" false 0s`,
		`SessionHost.Prompt rloop-wd-run-1 "step ended phase-2/implement failed tests red" false 0s`,
	}
	if got := host.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls\n got %q\nwant %q", got, want)
	}
}

type mcpHaltWatch struct {
	*Watch
	handler func(Signal) (bool, string)
	result  chan string
}

func (m *mcpHaltWatch) StepStarted(ref StepRef, s *Session) {
	m.Watch.StepStarted(ref, s)
	if ref.Key.Phase == "1" && ref.Key.Kind == "implement" {
		go func() {
			ok, reason := m.handler(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: "1", Kind: "implement"}, Reason: "off the plan"})
			if ok {
				reason = "accepted"
			}
			m.result <- reason
		}()
	}
}

func TestAHaltFromTheMCPHandlerStopsTheLoopWithExit5(t *testing.T) {
	r := newLoopRig(t)
	r.host.behaviour["rloop-p1-implement"] = "hold"
	w := &Watch{Store: r.store, Face: r.face}
	m := &mcpHaltWatch{Watch: w, handler: w.Handle, result: make(chan string, 1)}
	r.loop.Watcher = m

	code := r.run(RunOptions{})

	if code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	if got := <-m.result; got != "accepted" {
		t.Errorf("handler %q", got)
	}
	blocked := r.events("phase-blocked")
	if len(blocked) != 1 || blocked[0].Fields["reason"] != "watchdog: off the plan" {
		t.Errorf("blocked %+v", blocked)
	}
}

func TestWatchdogNameIsPerRunAndFitsHerdrsNameRule(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	cases := map[string]string{
		"20260919-082451":   "rloop-wd-20260919-082451",
		"20260919-082451-2": "rloop-wd-20260919-082451-2",
		"run-1":             "rloop-wd-run-1",
	}
	for runID, want := range cases {
		if got := WatchdogName(runID); got != want {
			t.Errorf("WatchdogName(%q) = %q, want %q", runID, got, want)
		}
	}
	for _, runID := range []string{"20260919-082451-12345678901234567890", "Run.ID/With Odd:Chars", ""} {
		if got := WatchdogName(runID); !valid.MatchString(got) {
			t.Errorf("WatchdogName(%q) = %q breaks herdr's name rule", runID, got)
		}
	}
	if WatchdogName("20260919-082451") == WatchdogName("20260919-082451-2") {
		t.Error("two runs share a watchdog name")
	}
	if WatchdogName("20260919-082451-123456789012345678901") == WatchdogName("20260919-082451-123456789012345678902") {
		t.Error("long run ids that differ at the end share a watchdog name")
	}
}

func TestWatchdogStartNeverTouchesAnotherRunsLiveWatchdog(t *testing.T) {
	host := &fakeSessionHost{Panes: map[string]string{
		"rloop-wd-20260919-082451": "other-run-pane",
		"rloop-watchdog":           "legacy-pane",
	}}
	store := &fakeStore{}
	dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})

	if err := dog.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, c := range host.Calls() {
		if strings.HasPrefix(c, "SessionHost.ClosePane") || strings.HasPrefix(c, "SessionHost.AgentPane rloop-wd-20260919") {
			t.Errorf("touched another run's watchdog: %q", c)
		}
	}
	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordEvent && rec.Event.Kind == "watchdog-stale-closed" {
			t.Errorf("recorded %+v", rec.Event)
		}
	}
}

type askingHost struct {
	fakeSessionHost
	mu         sync.Mutex
	blockedFor int
	prompts    int
}

func (h *askingHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.prompts++
	if h.prompts <= h.blockedFor {
		return errors.New("herdr agent prompt: herdr: agent_blocked: agent rloop-wd-run-1 is blocked and requires interactive input")
	}
	return nil
}

func TestNotifyWaitsOutAWatchdogAskingTheMaintainer(t *testing.T) {
	host := &askingHost{blockedFor: 5}
	host.States = map[string]AgentState{"rloop-wd-run-1": AgentBlocked}
	store := &fakeStore{}
	face := &fakeFace{}
	dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})
	dog.Face = face
	var slept []time.Duration
	dog.Sleep = func(d time.Duration) { slept = append(slept, d) }

	err := dog.Notify("question q2 from phase-10c/plan: which?", false, 0)

	if err != nil {
		t.Fatalf("err %v", err)
	}
	if host.prompts != 6 {
		t.Errorf("prompts %d, want 6", host.prompts)
	}
	if !reflect.DeepEqual(slept, []time.Duration{30 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}) {
		t.Errorf("slept %v", slept)
	}
	if !dog.live() {
		t.Error("watchdog dropped while it was asking the maintainer")
	}
	want := []string{"watchdog-waiting", "watchdog-resumed"}
	if got := recordedKinds(store); !reflect.DeepEqual(got, want) {
		t.Errorf("recorded %v, want %v", got, want)
	}
	if got := emittedKinds(face); !reflect.DeepEqual(got, want) {
		t.Errorf("emitted %v, want %v", got, want)
	}
}

func TestWaitingIsMarkedOnceAcrossDeliveriesUntilAPromptIsAccepted(t *testing.T) {
	host := &askingHost{blockedFor: 3}
	host.States = map[string]AgentState{"rloop-wd-run-1": AgentBlocked}
	store := &fakeStore{}
	face := &fakeFace{}
	dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})
	dog.Face = face
	dog.Sleep = func(time.Duration) { host.Err = errors.New("herdr: connection refused") }

	first := dog.Notify("step started phase-2/implement", false, 0)
	host.Err = nil
	dog.Sleep = func(time.Duration) {}
	second := dog.Notify("step ended phase-2/implement ok ", false, 0)

	if first == nil || second != nil {
		t.Errorf("first %v second %v", first, second)
	}
	want := []string{"watchdog-waiting", "watchdog-resumed"}
	if got := recordedKinds(store); !reflect.DeepEqual(got, want) {
		t.Errorf("recorded %v, want %v", got, want)
	}
	if got := emittedKinds(face); !reflect.DeepEqual(got, want) {
		t.Errorf("emitted %v, want %v", got, want)
	}
}

func TestStopGivesUpOnANoticeTheWatchdogIsBlockedOn(t *testing.T) {
	host := &askingHost{blockedFor: 100}
	host.States = map[string]AgentState{"rloop-wd-run-1": AgentBlocked}
	dog := newWatchdog(host, &fakeStore{}, ProviderArgs{Kind: "claude"})
	dog.Sleep = nil
	dog.Post("step started phase-2/implement")
	dog.Post("step ended phase-2/implement ok ")

	stopped := make(chan error)
	go func() { stopped <- dog.Stop() }()

	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop waited for the maintainer to answer the watchdog")
	}
	if dog.live() {
		t.Error("watchdog live after Stop")
	}
}

func recordedKinds(store *fakeStore) []string {
	var kinds []string
	for _, rec := range store.Records["run-1"] {
		if rec.Kind == RecordEvent {
			kinds = append(kinds, rec.Event.Kind)
		}
	}
	return kinds
}

func emittedKinds(face *fakeFace) []string {
	var kinds []string
	for _, ev := range face.Events {
		kinds = append(kinds, ev.Kind)
	}
	return kinds
}

type goneHost struct {
	fakeSessionHost
	gone atomic.Bool
}

func (h *goneHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	if h.gone.Load() {
		return errors.New("herdr agent prompt: herdr: agent_blocked: waiting on a permission")
	}
	return h.fakeSessionHost.Prompt(agent, text, wait, timeout)
}

func (h *goneHost) State(agent string) (AgentState, error) {
	if h.gone.Load() {
		return AgentGone, nil
	}
	return h.fakeSessionHost.State(agent)
}

func goneRig(t *testing.T) (*loopRig, *goneHost, *Watchdog) {
	r := newLoopRig(t)
	host := &goneHost{}
	dog := newWatchdog(host, &r.store.fakeStore, ProviderArgs{Kind: "claude"})
	w := &Watch{Store: r.store, Face: r.face, Dog: dog}
	dog.OnGone = w.WatchdogGone
	r.loop.Watcher = w
	return r, host, dog
}

type goneLander struct {
	*fakeLander
	host *goneHost
	dog  *Watchdog
}

func (g *goneLander) Land(ctx context.Context, ph Phase) (Landing, error) {
	l, err := g.fakeLander.Land(ctx, ph)
	g.host.gone.Store(true)
	g.dog.Notify("phase landed", false, 0)
	return l, err
}

func TestAWatchdogGoneBeforeTheFirstPhaseHaltsTheRunWithoutStartingIt(t *testing.T) {
	r, host, dog := goneRig(t)
	host.gone.Store(true)
	dog.Notify("run started", false, 0)

	code := r.run(RunOptions{})

	if code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	if got := r.events("phase-start"); len(got) != 0 {
		t.Errorf("started %+v", got)
	}
	st, _ := r.store.Load("run-1")
	if len(st.Steps) != 0 || len(st.Signals) != 0 {
		t.Errorf("steps %+v signals %+v", st.Steps, st.Signals)
	}
	if halts := r.events("halt"); len(halts) != 1 {
		t.Errorf("halts %+v", halts)
	}
}

func TestAWatchdogGoneBetweenPhasesHaltsTheRunBeforeTheNextPhase(t *testing.T) {
	r, host, dog := goneRig(t)
	r.loop.Lander = &goneLander{fakeLander: r.lander, host: host, dog: dog}

	code := r.run(RunOptions{})

	if code != 5 {
		t.Fatalf("exit %d, want 5", code)
	}
	var started []string
	for _, ev := range r.events("phase-start") {
		started = append(started, ev.Phase)
	}
	if !reflect.DeepEqual(started, []string{"1"}) {
		t.Errorf("started phases %v, want only 1", started)
	}
	if got := r.calls("Land "); !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("landed %v", got)
	}
}

func TestAStoppedWatchdogDoesNotHaltTheRun(t *testing.T) {
	r, _, dog := goneRig(t)
	if err := dog.Stop(); err != nil {
		t.Fatal(err)
	}

	code := r.run(RunOptions{})

	if code != 0 {
		t.Errorf("exit %d, want 0", code)
	}
}

func TestAskingTheMaintainerShowsTheQuestionUntilTheWatchdogsNextCall(t *testing.T) {
	host := &askingHost{}
	store := &fakeStore{}
	face := &fakeFace{}
	dog := newWatchdog(host, store, ProviderArgs{Kind: "codex"})
	dog.Face = face

	if err := dog.AskMaintainer("retry phase-2/implement with the helper renamed?", []string{"yes", "no"}, "yes"); err != nil {
		t.Fatal(err)
	}
	if err := dog.Notify("step ended phase-3/plan ok ", false, 0); err != nil {
		t.Fatal(err)
	}
	if err := dog.AskMaintainer("again?", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := dog.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := dog.Resume(); err != nil {
		t.Fatal(err)
	}

	want := []string{"watchdog-waiting", "watchdog-resumed"}
	if got := recordedKinds(store); !reflect.DeepEqual(got, want) {
		t.Errorf("recorded %v, want %v", got, want)
	}
	if got := emittedKinds(face); !reflect.DeepEqual(got, want) {
		t.Fatalf("emitted %v, want %v", got, want)
	}
	wantFields := map[string]string{"question": "retry phase-2/implement with the helper renamed?", "options": "yes; no", "recommended": "yes"}
	if got := face.Events[0].Fields; !reflect.DeepEqual(got, wantFields) {
		t.Errorf("fields %v, want %v", got, wantFields)
	}
}

func TestAWatchdogAskingThenBlockedOnTheMaintainerIsShownWaitingOnce(t *testing.T) {
	host := &askingHost{blockedFor: 2}
	host.States = map[string]AgentState{"rloop-wd-run-1": AgentBlocked}
	store := &fakeStore{}
	face := &fakeFace{}
	dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})
	dog.Face = face

	if err := dog.AskMaintainer("which store?", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := dog.Notify("step started phase-2/implement", false, 0); err != nil {
		t.Fatal(err)
	}
	if err := dog.Resume(); err != nil {
		t.Fatal(err)
	}

	want := []string{"watchdog-waiting", "watchdog-resumed"}
	if got := emittedKinds(face); !reflect.DeepEqual(got, want) {
		t.Errorf("emitted %v, want %v", got, want)
	}
	if got := recordedKinds(store); !reflect.DeepEqual(got, want) {
		t.Errorf("recorded %v, want %v", got, want)
	}
}
