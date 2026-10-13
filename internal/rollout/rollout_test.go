package rollout

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const reviewerMarker = "/runs/r/phase-3/implement-rv-codex-r1-a1.sentinel"

var since = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func writeRollout(t *testing.T, home, day, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func metaWithSource(id, start, cwd, source string) string {
	return `{"timestamp":"2026-10-13T13:00:00.000Z","type":"session_meta","payload":{"id":"` + id + `","timestamp":"` + start + `","cwd":"` + cwd + `","source":` + source + `}}`
}

func metaLine(id, start, cwd string) string {
	return metaWithSource(id, start, cwd, `"cli"`)
}

func subagentMetaLine(id, start, cwd string) string {
	return metaWithSource(id, start, cwd, `{"subagent":{"thread_spawn":{"parent_thread_id":"root","depth":1}}}`)
}

func itemLine(role, payloadType, text string) string {
	encoded, _ := json.Marshal(text)
	return `{"timestamp":"2026-10-13T13:00:00.000Z","type":"response_item","payload":{"type":"` + payloadType + `","role":"` + role + `","content":[{"type":"input_text","text":` + string(encoded) + `}]}}`
}

func userLine(text string) string {
	return itemLine("user", "message", text)
}

func assertFound(t *testing.T, actualPath, actualSession, expectedPath, expectedSession string) {
	t.Helper()
	if actualPath != expectedPath || actualSession != expectedSession {
		t.Fatalf("found (%q, %q), want (%q, %q)", actualPath, actualSession, expectedPath, expectedSession)
	}
}

func TestFindReturnsTheRolloutWhoseUserMessageNamesTheMarker(t *testing.T) {
	// given
	home := t.TempDir()
	expected := writeRollout(t, home, "2026/10/13", "rollout-1.jsonl", metaLine("019a", "2026-10-13T12:00:01.000Z", "/wt/phase-3"), userLine("write the sentinel to `"+reviewerMarker+"`"))

	// when
	actualPath, actualSession := Finder{Home: home}.Find("/wt/phase-3", reviewerMarker, since)

	// then
	assertFound(t, actualPath, actualSession, expected, "019a")
}

func TestFindTellsApartReviewersStartedSecondsApartInOneWorktree(t *testing.T) {
	// given
	home := t.TempDir()
	writeRollout(t, home, "2026/10/13", "rollout-2026-10-13T12-00-01-a.jsonl", metaLine("a", "2026-10-13T12:00:01.000Z", "/wt/phase-3"), userLine("write /runs/r/phase-3/implement-rv-codex-a-r1-a1.sentinel"))
	expected := writeRollout(t, home, "2026/10/13", "rollout-2026-10-13T12-00-02-b.jsonl", metaLine("b", "2026-10-13T12:00:02.000Z", "/wt/phase-3"), userLine("write /runs/r/phase-3/implement-rv-codex-b-r1-a1.sentinel"))
	writeRollout(t, home, "2026/10/13", "rollout-2026-10-13T12-00-03-c.jsonl", metaLine("c", "2026-10-13T12:00:03.000Z", "/wt/phase-3"), userLine("write /runs/r/phase-3/implement-rv-codex-c-r1-a1.sentinel"))
	writeRollout(t, home, "2026/10/13", "rollout-2026-10-13T12-00-04-native.jsonl", metaLine("native", "2026-10-13T12:00:04.000Z", "/wt/phase-3"), userLine("review the uncommitted changes"))

	// when
	actualPath, actualSession := Finder{Home: home}.Find("/wt/phase-3", "/runs/r/phase-3/implement-rv-codex-b-r1-a1.sentinel", since)

	// then
	assertFound(t, actualPath, actualSession, expected, "b")
}

func TestFindSkipsARolloutInAnotherWorktree(t *testing.T) {
	// given
	home := t.TempDir()
	writeRollout(t, home, "2026/10/13", "rollout-1.jsonl", metaLine("019a", "2026-10-13T12:00:01.000Z", "/wt/phase-4"), userLine(reviewerMarker))

	// when
	actualPath, actualSession := Finder{Home: home}.Find("/wt/phase-3", reviewerMarker, since)

	// then
	assertFound(t, actualPath, actualSession, "", "")
}

func TestFindJudgesTheStartByThePayloadTimestamp(t *testing.T) {
	tests := []struct {
		name    string
		start   string
		since   time.Time
		matches bool
	}{
		{"started a second before, written after", "2026-10-13T11:59:59.000Z", since, false},
		{"started in the same millisecond as since", "2026-10-13T12:00:00.000Z", time.Date(2026, 10, 13, 12, 0, 0, 400000, time.UTC), true},
		{"started after since", "2026-10-13T12:00:02.000Z", since, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			home := t.TempDir()
			path := writeRollout(t, home, "2026/10/13", "rollout-1.jsonl", metaLine("019a", tt.start, "/wt/phase-3"), userLine(reviewerMarker))

			// when
			actualPath, _ := Finder{Home: home}.Find("/wt/phase-3", reviewerMarker, tt.since)

			// then
			if actualMatched := actualPath == path; actualMatched != tt.matches {
				t.Fatalf("matched = %t (path %q), want %t", actualMatched, actualPath, tt.matches)
			}
		})
	}
}

func TestFindIgnoresTheMarkerOutsideAUserMessage(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"developer message", itemLine("developer", "message", reviewerMarker)},
		{"assistant message", itemLine("assistant", "message", reviewerMarker)},
		{"tool output", `{"type":"response_item","payload":{"type":"function_call_output","output":"` + reviewerMarker + `"}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			home := t.TempDir()
			writeRollout(t, home, "2026/10/13", "rollout-1.jsonl", metaLine("019a", "2026-10-13T12:00:01.000Z", "/wt/phase-3"), userLine("review the uncommitted changes"), tt.line)

			// when
			actualPath, actualSession := Finder{Home: home}.Find("/wt/phase-3", reviewerMarker, since)

			// then
			assertFound(t, actualPath, actualSession, "", "")
		})
	}
}

func TestFindNeverReturnsASubagentRolloutNamingTheMarker(t *testing.T) {
	t.Run("child readable, parent not yet written", func(t *testing.T) {
		// given
		home := t.TempDir()
		writeRollout(t, home, "2026/10/13", "rollout-2026-10-13T12-00-01-child.jsonl", subagentMetaLine("child", "2026-10-13T12:00:01.000Z", "/wt/phase-3"), userLine(reviewerMarker))

		// when
		actualPath, actualSession := Finder{Home: home}.Find("/wt/phase-3", reviewerMarker, since)

		// then
		assertFound(t, actualPath, actualSession, "", "")
	})
	t.Run("child sorts before its parent", func(t *testing.T) {
		// given
		home := t.TempDir()
		writeRollout(t, home, "2026/10/13", "rollout-2026-10-13T12-00-01-child.jsonl", subagentMetaLine("child", "2026-10-13T12:00:01.000Z", "/wt/phase-3"), userLine(reviewerMarker))
		expected := writeRollout(t, home, "2026/10/13", "rollout-2026-10-13T12-00-02-root.jsonl", metaLine("root", "2026-10-13T12:00:02.000Z", "/wt/phase-3"), userLine(reviewerMarker))

		// when
		actualPath, actualSession := Finder{Home: home}.Find("/wt/phase-3", reviewerMarker, since)

		// then
		assertFound(t, actualPath, actualSession, expected, "root")
	})
}

func TestFindMatchesAMarkerThatIsEscapedInTheRolloutJSON(t *testing.T) {
	tests := []struct {
		name   string
		marker string
	}{
		{"quote in the repository path", `/repo"demo/.r-loop/runs/r/phase-3/implement-a1.sentinel`},
		{"backslash in the repository path", `/repo\demo/.r-loop/runs/r/phase-3/implement-a1.sentinel`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// given
			home := t.TempDir()
			expected := writeRollout(t, home, "2026/10/13", "rollout-1.jsonl", metaLine("019a", "2026-10-13T12:00:01.000Z", "/wt/phase-3"), userLine("write the sentinel to "+tt.marker))

			// when
			actualPath, actualSession := Finder{Home: home}.Find("/wt/phase-3", tt.marker, since)

			// then
			assertFound(t, actualPath, actualSession, expected, "019a")
		})
	}
}

func TestFindLooksInTheDayBeforeSinceForALocalDateBehindUTC(t *testing.T) {
	// given
	home := t.TempDir()
	expected := writeRollout(t, home, "2026/10/12", "rollout-1.jsonl", metaLine("019a", "2026-10-13T00:30:05.000Z", "/wt/phase-3"), userLine(reviewerMarker))

	// when
	actualPath, actualSession := Finder{Home: home}.Find("/wt/phase-3", reviewerMarker, time.Date(2026, 10, 13, 0, 30, 0, 0, time.UTC))

	// then
	assertFound(t, actualPath, actualSession, expected, "019a")
}

func TestFindSkipsAMalformedRollout(t *testing.T) {
	// given
	home := t.TempDir()
	writeRollout(t, home, "2026/10/13", "rollout-0.jsonl", "not json", userLine(reviewerMarker))
	expected := writeRollout(t, home, "2026/10/13", "rollout-1.jsonl", metaLine("019a", "2026-10-13T12:00:01.000Z", "/wt/phase-3"), userLine(reviewerMarker))

	// when
	actualPath, actualSession := Finder{Home: home}.Find("/wt/phase-3", reviewerMarker, since)

	// then
	assertFound(t, actualPath, actualSession, expected, "019a")
}

func TestFindReturnsNothingWithoutASessionsDirectory(t *testing.T) {
	// given
	home := t.TempDir()

	// when
	actualPath, actualSession := Finder{Home: home}.Find("/wt/phase-3", reviewerMarker, since)

	// then
	assertFound(t, actualPath, actualSession, "", "")
}

func TestFindReadsAHomeWhoseNameHoldsGlobCharacters(t *testing.T) {
	// given
	home := filepath.Join(t.TempDir(), "codex[local]*")
	expected := writeRollout(t, home, "2026/10/13", "rollout-1.jsonl", metaLine("019a", "2026-10-13T12:00:01.000Z", "/wt/phase-3"), userLine(reviewerMarker))

	// when
	actualPath, actualSession := Finder{Home: home}.Find("/wt/phase-3", reviewerMarker, since)

	// then
	assertFound(t, actualPath, actualSession, expected, "019a")
}
