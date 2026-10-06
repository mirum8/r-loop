package analyze

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"r-loop/internal/core"
)

func pmdReport(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<pmd xmlns="http://pmd.sourceforge.net/report/2.0.0" version="7.0.0">
` + body + `
</pmd>`
}

func pmdViolation(file string, line, priority int, rule string) string {
	return `<file name="` + file + `"><violation beginline="` + strconv.Itoa(line) + `" endline="` + strconv.Itoa(line) + `" rule="` + rule + `" ruleset="bestpractices" priority="` + strconv.Itoa(priority) + `">
Avoid unused local variables such as 'x'.
</violation></file>`
}

func TestParsePMDReadsEachViolation(t *testing.T) {
	// given
	data := pmdReport(pmdViolation("/abs/A.java", 7, 3, "UnusedLocalVariable") + pmdViolation("src/B.java", 12, 1, "EmptyCatchBlock"))

	// when
	actual, err := ParsePMD("/repo/api", []byte(data))

	// then
	expected := []Hit{
		{Tool: "pmd", Rule: "UnusedLocalVariable", Level: "warning", Message: "Avoid unused local variables such as 'x'.", Path: "/abs/A.java", Line: 7},
		{Tool: "pmd", Rule: "EmptyCatchBlock", Level: "error", Message: "Avoid unused local variables such as 'x'.", Path: "/repo/api/src/B.java", Line: 12},
	}
	if err != nil || !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, err %v, expected %+v", actual, err, expected)
	}
}

func TestParsePMDMapsPriorityToLevel(t *testing.T) {
	for _, tc := range []struct {
		priority int
		expected string
	}{
		{1, "error"}, {2, "error"}, {3, "warning"}, {4, "note"}, {5, "note"},
	} {
		t.Run("priority "+strconv.Itoa(tc.priority), func(t *testing.T) {
			// given
			data := pmdReport(pmdViolation("A.java", 1, tc.priority, "R"))

			// when
			actual, err := ParsePMD("/repo", []byte(data))

			// then
			if err != nil || len(actual) != 1 || actual[0].Level != tc.expected {
				t.Fatalf("actual %+v, err %v, expected level %q", actual, err, tc.expected)
			}
		})
	}
}

func TestParsePMDIgnoresProcessingErrors(t *testing.T) {
	// given
	data := pmdReport(`<error filename="x" msg="boom"/>`)

	// when
	actual, err := ParsePMD("/repo", []byte(data))

	// then
	if err != nil || len(actual) != 0 {
		t.Fatalf("actual %+v, err %v; expected no hits", actual, err)
	}
}

func TestParsePMDRejectsMalformedXML(t *testing.T) {
	// given
	data := []byte(`<pmd xmlns="http://pmd.sourceforge.net/report/2.0.0"><file name="A.java">`)

	// when
	_, err := ParsePMD("/repo", data)

	// then
	if err == nil || !strings.HasPrefix(err.Error(), "pmd: xml:") {
		t.Fatalf("err %v, expected one starting pmd: xml:", err)
	}
}

func TestParsePMDRejectsAnotherRootElement(t *testing.T) {
	// given
	data := []byte(`<checkstyle version="10.0"><file name="A.java"/></checkstyle>`)

	// when
	_, err := ParsePMD("/repo", data)

	// then
	if err == nil || !strings.HasPrefix(err.Error(), "pmd: xml:") {
		t.Fatalf("err %v, expected one starting pmd: xml:", err)
	}
}

func TestPMDViolationsPassTheChangedLineFilter(t *testing.T) {
	// given
	data := pmdReport(pmdViolation("A.java", 7, 3, "UnusedLocalVariable") + pmdViolation("A.java", 8, 3, "UnusedLocalVariable"))
	hits, err := ParsePMD("/repo", []byte(data))
	if err != nil {
		t.Fatal(err)
	}

	// when
	actual := Findings("/repo", Changes{Lines: map[string]map[int]bool{"A.java": {7: true}}}, hits)

	// then
	expected := []core.Finding{{
		ID:     "s1",
		Title:  "pmd/UnusedLocalVariable: Avoid unused local variables such as 'x'.",
		Detail: "warning\nAvoid unused local variables such as 'x'.\nA.java:7",
		Files:  []string{"A.java"},
	}}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("actual %+v, expected %+v", actual, expected)
	}
}
