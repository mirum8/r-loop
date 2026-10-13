package hooktap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"r-loop/internal/core"
)

func env(agent string) func(string) string {
	return func(key string) string {
		if key == "R_LOOP_AGENT" {
			return agent
		}
		return ""
	}
}

func TestTapWritesNothingOutsideAnR_LoopSession(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hooks.jsonl")

	Tap(strings.NewReader(`{"hook_event_name":"Stop"}`), out, env(""))

	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("hooks file written: %v", err)
	}
}

func TestTapIgnoresInputThatIsNotAHookEvent(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hooks.jsonl")

	Tap(strings.NewReader(`not json`), out, env("rloop-a"))

	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("hooks file written: %v", err)
	}
}

func TestFeedReadsWhatTheTapAppends(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hooks.jsonl")
	feed := NewFeed(out)

	Tap(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Edit","tool_input":{"file_path":"/wt/a.go"}}`), out, env("rloop-a"))
	Tap(strings.NewReader(`{"hook_event_name":"Notification","message":"Claude needs your permission to use Bash","notification_type":"permission_prompt"}`), out, env("rloop-b"))
	events, err := feed.Read()

	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	if e := events[0]; e.Agent != "rloop-a" || e.Event != "PreToolUse" || e.Tool != "Edit" || e.Target != "/wt/a.go" {
		t.Errorf("first = %+v", e)
	}
	if e := events[1]; e.Agent != "rloop-b" || e.Event != "Notification" || e.Notice != "Claude needs your permission to use Bash" || e.Type != "permission_prompt" {
		t.Errorf("second = %+v", e)
	}
}

func TestTapUsesTheBashCommandAsTheTarget(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hooks.jsonl")
	feed := NewFeed(out)

	Tap(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test ./...","description":"run tests"}}`), out, env("rloop-a"))
	events, _ := feed.Read()

	if len(events) != 1 || events[0].Target != "go test ./..." {
		t.Fatalf("events = %+v", events)
	}
}

func TestTapCapsALongCommand(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hooks.jsonl")
	feed := NewFeed(out)

	Tap(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"`+strings.Repeat("x", 5000)+`"}}`), out, env("rloop-a"))
	events, _ := feed.Read()

	if len(events) != 1 || len(events[0].Target) != 512 {
		t.Fatalf("events = %d, target %d bytes", len(events), len(events[0].Target))
	}
}

func TestFeedSkipsLinesWrittenBeforeItOpened(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hooks.jsonl")
	Tap(strings.NewReader(`{"hook_event_name":"Stop"}`), out, env("rloop-old"))
	feed := NewFeed(out)

	Tap(strings.NewReader(`{"hook_event_name":"Stop"}`), out, env("rloop-new"))
	events, _ := feed.Read()

	if len(events) != 1 || events[0].Agent != "rloop-new" {
		t.Fatalf("events = %+v", events)
	}
}

func TestFeedHoldsAPartialLineUntilItIsComplete(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hooks.jsonl")
	feed := NewFeed(out)
	line := `{"at":"2026-10-13T10:00:00Z","agent":"rloop-a","event":"Stop"}`

	os.WriteFile(out, []byte(line[:20]), 0o600)
	first, _ := feed.Read()
	f, _ := os.OpenFile(out, os.O_WRONLY|os.O_APPEND, 0o600)
	f.WriteString(line[20:] + "\n")
	f.Close()
	second, _ := feed.Read()

	if len(first) != 0 {
		t.Fatalf("partial line read: %+v", first)
	}
	if len(second) != 1 || second[0] != (core.HookEvent{At: second[0].At, Agent: "rloop-a", Event: "Stop"}) {
		t.Fatalf("second = %+v", second)
	}
}

func TestFeedSkipsAMalformedLine(t *testing.T) {
	out := filepath.Join(t.TempDir(), "hooks.jsonl")
	feed := NewFeed(out)

	os.WriteFile(out, []byte("garbage\n"+`{"agent":"rloop-a","event":"Stop"}`+"\n"), 0o600)
	events, err := feed.Read()

	if err != nil || len(events) != 1 || events[0].Agent != "rloop-a" {
		t.Fatalf("events = %+v, err %v", events, err)
	}
}
