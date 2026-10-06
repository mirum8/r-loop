package analyze

import (
	"reflect"
	"strings"
	"testing"

	"r-loop/internal/core"
)

func hit(tool, rule, level, path string, line int) Hit {
	return Hit{Tool: tool, Rule: rule, Level: level, Message: "m", Path: path, Line: line}
}

func manyHits(tool, rule, level, path string, n int) []Hit {
	hits := make([]Hit, 0, n)
	for i := 1; i <= n; i++ {
		hits = append(hits, hit(tool, rule, level, path, i))
	}
	return hits
}

func parseSARIF(t *testing.T, tool, base, data string) []Hit {
	t.Helper()
	hits, err := ParseSARIF(tool, base, []byte(data))
	if err != nil {
		t.Fatalf("ParseSARIF: %v", err)
	}
	return hits
}

func sarifWithLocation(location string) string {
	return `{"runs":[{"originalUriBaseIds":{"SRCROOT":{"uri":"file:///repo/src/main/java/"}},"tool":{"driver":{"name":"x"}},"results":[{"ruleId":"R","level":"note","message":{"text":"m"},"locations":[{"physicalLocation":{"artifactLocation":` + location + `,"region":{"startLine":1}}}]}]}]}`
}

func TestParseSARIFReadsEachResult(t *testing.T) {
	// given
	data := `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"golangci-lint"}},"results":[
		{"ruleId":"errcheck","level":"error","message":{"text":"unchecked"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"a.go"},"region":{"startLine":3,"startColumn":2}}}]},
		{"ruleId":"unused","level":"warning","message":{"text":"unused x"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"pkg/b.go"},"region":{"startLine":9}}}]}
	]}]}`

	// when
	actual := parseSARIF(t, "golangci-lint", "/repo/mod", data)

	// then
	expected := []Hit{
		{Tool: "golangci-lint", Rule: "errcheck", Level: "error", Message: "unchecked", Path: "/repo/mod/a.go", Line: 3},
		{Tool: "golangci-lint", Rule: "unused", Level: "warning", Message: "unused x", Path: "/repo/mod/pkg/b.go", Line: 9},
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}

func TestParseSARIFResolvesTheResultsPath(t *testing.T) {
	for _, tc := range []struct {
		name, location, expected string
	}{
		{"file URI", `{"uri":"file:///abs/x.go"}`, "/abs/x.go"},
		{"uriBaseId", `{"uri":"com/a/B.java","uriBaseId":"SRCROOT"}`, "/repo/src/main/java/com/a/B.java"},
		{"percent-encoded", `{"uri":"sp%20ace.go"}`, "/repo/sp ace.go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// given
			data := sarifWithLocation(tc.location)

			// when
			actual := parseSARIF(t, "x", "/repo", data)

			// then
			if len(actual) != 1 || actual[0].Path != tc.expected {
				t.Fatalf("actual %+v, expected path %q", actual, tc.expected)
			}
		})
	}
}

func TestParseSARIFTakesTheLevelFromTheResultTheRuleOrTheDefault(t *testing.T) {
	for _, tc := range []struct {
		name, rules, level, expected string
	}{
		{"result", `[{"id":"R","defaultConfiguration":{"level":"note"}}]`, `"level":"error",`, "error"},
		{"rule default", `[{"id":"R","defaultConfiguration":{"level":"note"}}]`, ``, "note"},
		{"sarif default", `[{"id":"R"}]`, ``, "warning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// given
			data := `{"runs":[{"tool":{"driver":{"name":"x","rules":` + tc.rules + `}},"results":[{"ruleId":"R",` + tc.level + `"message":{"text":"m"}}]}]}`

			// when
			actual := parseSARIF(t, "x", "/repo", data)

			// then
			if len(actual) != 1 || actual[0].Level != tc.expected {
				t.Fatalf("actual %+v, expected level %q", actual, tc.expected)
			}
		})
	}
}

func TestParseSARIFResolvesAnArtifactLocationByIndex(t *testing.T) {
	// given
	data := `{"runs":[{"tool":{"driver":{"name":"SpotBugs"}},"artifacts":[{"location":{"uri":"src/A.java"}}],"results":[
		{"ruleId":"DLS","level":"warning","message":{"text":"dead store"},"locations":[{"physicalLocation":{"artifactLocation":{"index":0},"region":{"startLine":7}}}]}
	]}]}`

	// when
	hits := parseSARIF(t, "spotbugs", "/repo", data)
	actual := Findings("/repo", Changes{Lines: map[string]map[int]bool{"src/A.java": {7: true}}}, hits)

	// then
	if hits[0].Path != "/repo/src/A.java" || hits[0].Line != 7 {
		t.Fatalf("actual hit %+v", hits[0])
	}
	if len(actual) != 1 || !reflect.DeepEqual(actual[0].Files, []string{"src/A.java"}) {
		t.Fatalf("actual findings %+v", actual)
	}
}

func TestParseSARIFResolvesTheRuleFromRuleIndex(t *testing.T) {
	// given
	data := `{"runs":[{"tool":{"driver":{"name":"x","rules":[{"id":"A"},{"id":"B","defaultConfiguration":{"level":"error"}}]}},"results":[{"ruleIndex":1,"message":{"text":"m"}}]}]}`

	// when
	actual := parseSARIF(t, "x", "/repo", data)

	// then
	if len(actual) != 1 || actual[0].Rule != "B" || actual[0].Level != "error" {
		t.Fatalf("actual %+v, expected rule B at level error", actual)
	}
}

func TestParseSARIFBuildsTheMessageFromItsIdAndArguments(t *testing.T) {
	for _, tc := range []struct {
		name, driver, arguments, expected string
	}{
		{"rule messageStrings", `"rules":[{"id":"R","messageStrings":{"default":{"text":"Dead store to {0} in {1}"}}}]`, `["x","Foo.bar()"]`, "Dead store to x in Foo.bar()"},
		{"globalMessageStrings", `"rules":[{"id":"R"}],"globalMessageStrings":{"default":{"text":"{{0}} is {0}"}}`, `["x"]`, "{0} is x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// given
			data := `{"runs":[{"tool":{"driver":{"name":"x",` + tc.driver + `}},"results":[{"ruleId":"R","message":{"id":"default","arguments":` + tc.arguments + `}}]}]}`

			// when
			actual := parseSARIF(t, "spotbugs", "/repo", data)

			// then
			if len(actual) != 1 || actual[0].Message != tc.expected {
				t.Fatalf("actual %+v, expected message %q", actual, tc.expected)
			}
		})
	}
}

func TestParseSARIFKeepsAResultWithoutALocation(t *testing.T) {
	// given
	data := `{"runs":[{"tool":{"driver":{"name":"govulncheck"}},"results":[{"ruleId":"GO-2026-0001","level":"error","message":{"text":"vuln"}}]}]}`

	// when
	actual := parseSARIF(t, "govulncheck", "/repo", data)

	// then
	expected := []Hit{{Tool: "govulncheck", Rule: "GO-2026-0001", Level: "error", Message: "vuln"}}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}

func TestParseSARIFRejectsInvalidJSON(t *testing.T) {
	// given
	data := []byte(`{"runs":[`)

	// when
	_, err := ParseSARIF("semgrep", "/repo", data)

	// then
	if err == nil || !strings.HasPrefix(err.Error(), "semgrep: sarif:") {
		t.Fatalf("err %v, expected one starting semgrep: sarif:", err)
	}
}

func TestFindingsKeepOnlyHitsOnChangedLinesOrInUntrackedFiles(t *testing.T) {
	ch := Changes{Lines: map[string]map[int]bool{"a.go": {3: true}}, Untracked: map[string]bool{"n.go": true}}
	for _, tc := range []struct {
		name     string
		hit      Hit
		expected int
	}{
		{"changed line", hit("golangci-lint", "r", "warning", "/repo/a.go", 3), 1},
		{"unchanged line", hit("golangci-lint", "r", "warning", "/repo/a.go", 4), 0},
		{"untracked file", hit("golangci-lint", "r", "warning", "/repo/n.go", 40), 1},
		{"unchanged file", hit("golangci-lint", "r", "warning", "/repo/b.go", 1), 0},
		{"pathless", hit("golangci-lint", "r", "warning", "", 0), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// given
			hits := []Hit{tc.hit}

			// when
			actual := Findings("/repo", ch, hits)

			// then
			if len(actual) != tc.expected {
				t.Fatalf("actual %+v, expected %d findings", actual, tc.expected)
			}
		})
	}
}

func TestFindingsKeepEveryGovulncheckHit(t *testing.T) {
	// given
	hits := []Hit{hit("govulncheck", "GO-1", "error", "/repo/go.mod", 5), hit("govulncheck", "GO-2", "error", "", 0)}

	// when
	actual := Findings("/repo", Changes{}, hits)

	// then
	if len(actual) != 2 {
		t.Fatalf("actual %+v, expected 2 findings", actual)
	}
}

func TestFindingsNumberTitleAndDescribeEachHit(t *testing.T) {
	// given
	h := Hit{Tool: "golangci-lint", Rule: "errcheck", Level: "warning", Message: "Error return value is not checked\nsecond line", Path: "/repo/internal/a.go", Line: 12}
	ch := Changes{Lines: map[string]map[int]bool{"internal/a.go": {12: true}}}

	// when
	actual := Findings("/repo", ch, []Hit{h})

	// then
	expected := []core.Finding{{
		ID:     "s1",
		Title:  "golangci-lint/errcheck: Error return value is not checked",
		Detail: "warning\nError return value is not checked\nsecond line\ninternal/a.go:12",
		Files:  []string{"internal/a.go"},
	}}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}

func TestFindingsDescribeAPathlessHitWithoutALocation(t *testing.T) {
	// given
	h := Hit{Tool: "govulncheck", Rule: "GO-2026-0001", Level: "error", Message: "GO-2026-0001 in x/net"}

	// when
	actual := Findings("/repo", Changes{}, []Hit{h})

	// then
	if len(actual) != 1 || actual[0].Detail != "error\nGO-2026-0001 in x/net" || actual[0].Files == nil || len(actual[0].Files) != 0 {
		t.Fatalf("actual %#v", actual)
	}
}

func TestFindingsListTheMostSevereFirst(t *testing.T) {
	// given
	hits := []Hit{hit("golangci-lint", "r", "warning", "/repo/a.go", 1), hit("golangci-lint", "r", "error", "/repo/b.go", 9)}
	ch := Changes{Untracked: map[string]bool{"a.go": true, "b.go": true}}

	// when
	actual := Findings("/repo", ch, hits)

	// then
	if len(actual) != 2 || actual[0].ID != "s1" || actual[0].Files[0] != "b.go" || actual[1].ID != "s2" || actual[1].Files[0] != "a.go" {
		t.Fatalf("actual %+v", actual)
	}
}

func TestFindingsCapAtFiftyAndCountTheRestByToolAndRule(t *testing.T) {
	// given
	hits := append(manyHits("golangci-lint", "errcheck", "error", "/repo/e.go", 50), manyHits("gosec", "G104", "warning", "/repo/w.go", 2)...)
	hits = append(hits, manyHits("pmd", "UnusedLocalVariable", "note", "/repo/n.java", 1)...)
	ch := Changes{Untracked: map[string]bool{"e.go": true, "w.go": true, "n.java": true}}

	// when
	actual := Findings("/repo", ch, hits)

	// then
	if len(actual) != 51 || actual[49].ID != "s50" || !strings.HasPrefix(actual[49].Title, "golangci-lint/errcheck:") {
		t.Fatalf("actual has %d findings, s50 %+v", len(actual), actual[min(49, len(actual)-1)])
	}
	expected := core.Finding{ID: "s51", Title: "static: 3 more findings", Detail: "gosec/G104: 2\npmd/UnusedLocalVariable: 1", Files: []string{"n.java", "w.go"}}
	if !reflect.DeepEqual(actual[50], expected) {
		t.Fatalf("actual %+v, expected %+v", actual[50], expected)
	}
}

func TestFindingsAtExactlyFiftyAddNoSummary(t *testing.T) {
	// given
	hits := manyHits("golangci-lint", "errcheck", "error", "/repo/e.go", 50)
	ch := Changes{Untracked: map[string]bool{"e.go": true}}

	// when
	actual := Findings("/repo", ch, hits)

	// then
	if len(actual) != 50 || actual[49].ID != "s50" {
		t.Fatalf("actual has %d findings", len(actual))
	}
}

func TestFindingsOfNoHitsIsAnEmptyList(t *testing.T) {
	// given
	var hits []Hit

	// when
	actual := Findings("/repo", Changes{}, hits)

	// then
	if actual == nil || len(actual) != 0 {
		t.Fatalf("actual %#v, expected an empty list", actual)
	}
}
