package core

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

const planPath = ".task-plans/phase-3-plan-reader.md"

const goodPlan = `status: planned

## Summary
Reads the plan.

## Changes
- internal/plan/reader.go: create

## Tests
- TestReadsPhases

## Assumptions
- the file is UTF-8
- headings are ASCII
`

func planCtx(plan string, changed ...string) EvidenceContext {
	return EvidenceContext{
		Repo:      &fakeRepo{Tree: "tree-now", TreeChanges: changed},
		Worktree:  "/wt",
		StartTree: "tree-start",
		PlanPath:  planPath,
		FS:        fstest.MapFS{planPath: {Data: []byte(plan)}},
	}
}

func runCheck(t *testing.T, name string, ctx EvidenceContext) (bool, string) {
	t.Helper()
	check, ok := LookupCheck(name)
	if !ok {
		t.Fatalf("check %q not registered", name)
	}
	return check(ctx)
}

func TestPlanFileCheckPassesOnAFullPlanThatChangedOnlyItself(t *testing.T) {
	ok, missing := runCheck(t, "plan-file", planCtx(goodPlan, planPath))

	if !ok || missing != "" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestPlanFileCheckComparesTheTreeAtStartWithASnapshotOfTheWorktree(t *testing.T) {
	ctx := planCtx(goodPlan, planPath)

	runCheck(t, "plan-file", ctx)

	want := []string{"Repo.Snapshot /wt", "Repo.TreeDiff tree-start tree-now"}
	if got := ctx.Repo.(*fakeRepo).Calls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q", got)
	}
}

func TestPlanFileCheckFailsWhenThePlanIsAbsent(t *testing.T) {
	ctx := planCtx(goodPlan)
	ctx.FS = fstest.MapFS{}

	ok, missing := runCheck(t, "plan-file", ctx)

	if ok || missing != "no plan at "+planPath {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestPlanFileCheckFailsWithoutTheStatusHeaderInTheFirstFiveLines(t *testing.T) {
	late := "# Plan\n\n\n\n\nstatus: planned\n" + strings.TrimPrefix(goodPlan, "status: planned\n")

	for name, plan := range map[string]string{
		"absent": strings.TrimPrefix(goodPlan, "status: planned\n"),
		"late":   late,
	} {
		t.Run(name, func(t *testing.T) {
			ok, missing := runCheck(t, "plan-file", planCtx(plan, planPath))

			if ok || missing != "no status: planned header" {
				t.Fatalf("ok = %v, missing = %q", ok, missing)
			}
		})
	}
}

func TestPlanFileCheckAcceptsTheStatusHeaderOnTheFifthLine(t *testing.T) {
	plan := "# Plan\n\n\n\nstatus: planned\n" + strings.TrimPrefix(goodPlan, "status: planned\n")

	ok, missing := runCheck(t, "plan-file", planCtx(plan, planPath))

	if !ok {
		t.Fatalf("missing = %q", missing)
	}
}

func TestPlanFileCheckFailsOnEachMissingHeading(t *testing.T) {
	for _, heading := range []string{"## Summary", "## Changes", "## Tests", "## Assumptions"} {
		t.Run(heading, func(t *testing.T) {
			plan := strings.Replace(goodPlan, heading+"\n", "### Other\n", 1)

			ok, missing := runCheck(t, "plan-file", planCtx(plan, planPath))

			if ok || missing != "missing "+heading {
				t.Fatalf("ok = %v, missing = %q", ok, missing)
			}
		})
	}
}

func TestPlanFileCheckFailsOnAnEmptyTestsSection(t *testing.T) {
	plan := strings.Replace(goodPlan, "- TestReadsPhases\n", "we will test later\n", 1)

	ok, missing := runCheck(t, "plan-file", planCtx(plan, planPath))

	if ok || missing != "## Tests is empty" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestPlanFileCheckAcceptsNumberedAndStarAndPlusTestItems(t *testing.T) {
	sections := map[string]string{
		"numbered with prose": "Write these first in `greet/greet_test.go`. They must fail before `greet.go` exists:\n\n" +
			"1. `TestHello/named`: `Hello(\"Alice\")` returns exactly `Hello, Alice!`.\n" +
			"2. `TestHello/empty name`: `Hello(\"\")` returns exactly `Hello, world!`.\n\n" +
			"Verify with `go test ./greet/...`, which must be green.\n",
		"numbered with parenthesis": "2) TestReadsPhases\n",
		"star bullet":               "* TestReadsPhases\n",
		"plus bullet":               "+ TestReadsPhases\n",
	}
	for name, body := range sections {
		t.Run(name, func(t *testing.T) {
			plan := strings.Replace(goodPlan, "- TestReadsPhases\n", body, 1)

			ok, missing := runCheck(t, "plan-file", planCtx(plan, planPath))

			if !ok {
				t.Fatalf("missing = %q", missing)
			}
		})
	}
}

func TestPlanFileCheckAcceptsATestsTableWithDataRows(t *testing.T) {
	table := "| Test | Covers |\n|---|:---:|\n| `TestHello/named` | a name |\n| `TestHello/empty` | no name |\n"
	plan := strings.Replace(goodPlan, "- TestReadsPhases\n", table, 1)

	ok, missing := runCheck(t, "plan-file", planCtx(plan, planPath))

	if !ok {
		t.Fatalf("missing = %q", missing)
	}
}

func TestPlanFileCheckFailsOnATestsTableWithOnlyAHeader(t *testing.T) {
	plan := strings.Replace(goodPlan, "- TestReadsPhases\n", "| Test | Covers |\n| --- | --- |\n", 1)

	ok, missing := runCheck(t, "plan-file", planCtx(plan, planPath))

	if ok || missing != "## Tests is empty" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestPlanFileCheckFailsOnATestsSectionOfProseOnly(t *testing.T) {
	plan := strings.Replace(goodPlan, "- TestReadsPhases\n", "Write the tests in greet_test.go.\n\nVerify with `go test ./greet/...`, 2 of them.\n", 1)

	ok, missing := runCheck(t, "plan-file", planCtx(plan, planPath))

	if ok || missing != "## Tests is empty" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestPlanFileCheckFailsWhenThePlanStepChangedASecondFile(t *testing.T) {
	ok, missing := runCheck(t, "plan-file", planCtx(goodPlan, planPath, "internal/plan/reader.go"))

	if ok || missing != "plan step changed internal/plan/reader.go" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestPlanFileCheckFailsWhenThePlanStepChangedNothing(t *testing.T) {
	ok, missing := runCheck(t, "plan-file", planCtx(goodPlan))

	if ok || missing != "plan step did not change "+planPath {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestDiffCheckPassesWhenTheTreeChangedSinceTheStepStarted(t *testing.T) {
	repo := &fakeRepo{Tree: "tree-now", TreeChanges: []string{"main.go"}}

	ok, missing := runCheck(t, "diff", EvidenceContext{Repo: repo, Worktree: "/wt", StartTree: "tree-start"})

	if !ok || missing != "" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
	want := []string{"Repo.Snapshot /wt", "Repo.TreeDiff tree-start tree-now"}
	if !reflect.DeepEqual(repo.Calls(), want) {
		t.Fatalf("calls = %q", repo.Calls())
	}
}

func TestDiffCheckFailsWhenTheTreeIsDirtyFromAnEarlierStepButUnchangedSinceStart(t *testing.T) {
	repo := &fakeRepo{Tree: "tree-start", DirtyFiles: []string{"left-over.go"}, Changed: []string{"left-over.go"}}

	ok, missing := runCheck(t, "diff", EvidenceContext{Repo: repo, Worktree: "/wt", StartSHA: "base", StartTree: "tree-start"})

	if ok || missing != "no change since the step started" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestReportCheck(t *testing.T) {
	const report = "docs/topic/reports/milestone-1-core.md"
	for name, tc := range map[string]struct {
		fs      fstest.MapFS
		ok      bool
		missing string
	}{
		"written": {fstest.MapFS{report: {Data: []byte("# Milestone 1\n")}}, true, ""},
		"absent":  {fstest.MapFS{}, false, "no report at " + report},
		"empty":   {fstest.MapFS{report: {Data: nil}}, false, "report " + report + " is empty"},
	} {
		t.Run(name, func(t *testing.T) {
			ok, missing := runCheck(t, "report", EvidenceContext{ReportPath: report, FS: tc.fs})

			if ok != tc.ok || missing != tc.missing {
				t.Fatalf("ok = %v, missing = %q", ok, missing)
			}
		})
	}
}

const findingsPath = "phase-3/implement-findings-codex-r2.json"

func findingsCtx(body string) EvidenceContext {
	return EvidenceContext{
		FindingsFiles: []string{findingsPath},
		FS:            fstest.MapFS{findingsPath: {Data: []byte(body)}},
	}
}

func TestFindingsCheckRequiresTheNativeOutputWhenNamed(t *testing.T) {
	const native = "implement-rv-codex-r1/native-review.txt"
	const body = `{"reviewer":"codex","findings":[]}`
	for _, tc := range []struct {
		name, output  string
		named, wantOK bool
	}{
		{"absent", "", true, false},
		{"blank", "\n\t ", true, false},
		{"present", "P1: nil map", true, true},
		{"unnamed", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := findingsCtx(body)
			files := fstest.MapFS{findingsPath: {Data: []byte(body)}}
			if tc.name != "absent" && tc.name != "unnamed" {
				files[native] = &fstest.MapFile{Data: []byte(tc.output)}
			}
			ctx.FS = files
			if tc.named {
				ctx.NativeOutput, ctx.ReviewCommand = native, "codex exec review"
			}
			ok, missing := runCheck(t, "findings", ctx)
			want := ""
			if !tc.wantOK {
				want = "native review `codex exec review` produced no output"
			}
			if ok != tc.wantOK || missing != want {
				t.Fatalf("ok = %v, missing = %q", ok, missing)
			}
		})
	}
}

func TestFindingsCheckPassesOnAWellFormedFile(t *testing.T) {
	body := `{"reviewer":"codex","findings":[
		{"id":"codex-r2-1","title":"nil map","detail":"writes to a nil map","files":["a.go"]},
		{"id":"codex-r2-2","title":"leak","detail":"file not closed","files":["b.go","c.go"]}]}`

	ok, missing := runCheck(t, "findings", findingsCtx(body))

	if !ok || missing != "" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestFindingsCheckPassesOnAReviewerWithNoFindings(t *testing.T) {
	ok, missing := runCheck(t, "findings", findingsCtx(`{"reviewer":"codex","findings":[]}`))

	if !ok {
		t.Fatalf("missing = %q", missing)
	}
}

func TestFindingsCheckFails(t *testing.T) {
	for name, tc := range map[string]struct {
		body, missing string
	}{
		"unreadable json":  {`{"reviewer":`, findingsPath + ": unreadable"},
		"wrong type":       {`{"reviewer":"codex","findings":[{"id":"codex-r2-1","title":"t","detail":"d","files":"a.go"}]}`, findingsPath + ": unreadable"},
		"no findings list": {`{"reviewer":"codex"}`, findingsPath + ": no findings list"},
		"other reviewer":   {`{"reviewer":"claude","findings":[]}`, findingsPath + ": reviewer claude does not match codex"},
		"missing id":       {`{"reviewer":"codex","findings":[{"title":"t","detail":"d","files":[]}]}`, findingsPath + ": finding 1 has no id"},
		"missing title":    {`{"reviewer":"codex","findings":[{"id":"codex-r2-1","detail":"d","files":[]}]}`, findingsPath + ": finding codex-r2-1 has no title"},
		"missing detail":   {`{"reviewer":"codex","findings":[{"id":"codex-r2-1","title":"t","files":[]}]}`, findingsPath + ": finding codex-r2-1 has no detail"},
		"null files":       {`{"reviewer":"codex","findings":[{"id":"codex-r2-1","title":"t","detail":"d","files":null}]}`, findingsPath + ": finding codex-r2-1 has no files list"},
		"missing files":    {`{"reviewer":"codex","findings":[{"id":"codex-r2-1","title":"t","detail":"d"}]}`, findingsPath + ": finding codex-r2-1 has no files list"},
		"wrong prefix":     {`{"reviewer":"codex","findings":[{"id":"codex-r1-1","title":"t","detail":"d","files":[]}]}`, findingsPath + ": id codex-r1-1 does not start with codex-r2-"},
		"duplicate id": {`{"reviewer":"codex","findings":[
			{"id":"codex-r2-1","title":"t","detail":"d","files":[]},
			{"id":"codex-r2-1","title":"u","detail":"e","files":[]}]}`, findingsPath + ": duplicate id codex-r2-1"},
	} {
		t.Run(name, func(t *testing.T) {
			ok, missing := runCheck(t, "findings", findingsCtx(tc.body))

			if ok || missing != tc.missing {
				t.Fatalf("ok = %v, missing = %q", ok, missing)
			}
		})
	}
}

func TestFindingsCheckFailsOnAnAbsentFile(t *testing.T) {
	ctx := findingsCtx("")
	ctx.FS = fstest.MapFS{}

	ok, missing := runCheck(t, "findings", ctx)

	if ok || missing != "no findings at "+findingsPath {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestFindingsCheckFailsOnAFileNameOutsideTheScheme(t *testing.T) {
	ctx := EvidenceContext{
		FindingsFiles: []string{"phase-3/codex.json"},
		FS:            fstest.MapFS{"phase-3/codex.json": {Data: []byte(`{"reviewer":"codex","findings":[]}`)}},
	}

	ok, missing := runCheck(t, "findings", ctx)

	if ok || missing != "phase-3/codex.json: name is not <kind>-findings-<reviewer>-r<round>.json" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestFindingsCheckAcceptsAHyphenatedReviewer(t *testing.T) {
	const path = "phase-3/plan-findings-open-code-r1.json"
	ctx := EvidenceContext{
		FindingsFiles: []string{path},
		FS: fstest.MapFS{path: {Data: []byte(
			`{"reviewer":"open-code","findings":[{"id":"open-code-r1-1","title":"t","detail":"d","files":[]}]}`)}},
	}

	ok, missing := runCheck(t, "findings", ctx)

	if !ok {
		t.Fatalf("missing = %q", missing)
	}
}

func TestReadFindingsParsesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "implement-findings-codex-r1.json")
	os.WriteFile(path, []byte(`{"reviewer":"codex","findings":[{"id":"codex-r1-1","title":"t","detail":"d","files":["a.go"]}]}`), 0o644)

	got, err := ReadFindings(path)

	want := Findings{Reviewer: "codex", Findings: []Finding{{ID: "codex-r1-1", Title: "t", Detail: "d", Files: []string{"a.go"}}}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got = %+v, err = %v", got, err)
	}
}

func TestPlanAssumptions(t *testing.T) {
	for name, tc := range map[string]struct {
		plan string
		want []string
	}{
		"items": {goodPlan, []string{"the file is UTF-8", "headings are ASCII"}},
		"none":  {strings.Replace(goodPlan, "- the file is UTF-8\n- headings are ASCII\n", "none\n", 1), nil},
		"followed by another section": {
			goodPlan + "\n## Notes\n- not an assumption\n",
			[]string{"the file is UTF-8", "headings are ASCII"},
		},
		"no section": {"status: planned\n", nil},
		"table rows are not assumptions": {
			strings.Replace(goodPlan, "- headings are ASCII\n", "- headings are ASCII\n\n| Assumption | Why |\n|---|---|\n| ASCII only | tooling |\n", 1),
			[]string{"the file is UTF-8", "headings are ASCII"},
		},
		"numbered items": {
			strings.Replace(goodPlan, "- the file is UTF-8\n- headings are ASCII\n", "1. the file is UTF-8\n2) headings are ASCII\n", 1),
			[]string{"the file is UTF-8", "headings are ASCII"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := PlanAssumptions(planPath, fstest.MapFS{planPath: {Data: []byte(tc.plan)}})

			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got = %q", got)
			}
		})
	}
}

func TestPlanAssumptionsOfAnAbsentPlanIsNone(t *testing.T) {
	if got := PlanAssumptions(planPath, fstest.MapFS{}); got != nil {
		t.Fatalf("got = %q", got)
	}
}

func writeSentinel(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan-a1.sentinel")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadSentinel(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want Sentinel
	}{
		"ok":                         {`{"outcome":"ok","reason":""}`, Sentinel{Outcome: "ok"}},
		"failed":                     {`{"outcome":"failed","reason":"plan is wrong"}`, Sentinel{Outcome: "failed", Reason: "plan is wrong"}},
		"an old sentinel with at":    {`{"outcome":"ok","reason":"","at":"2026-09-18T10:00:00Z"}`, Sentinel{Outcome: "ok"}},
		"a mistyped at is no matter": {`{"outcome":"ok","reason":"","at":"2026-09-23T11:50:08:z"}`, Sentinel{Outcome: "ok"}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ReadSentinel(writeSentinel(t, tc.body))

			if err != nil || got != tc.want {
				t.Fatalf("got = %+v, err = %v", got, err)
			}
		})
	}
}

func TestReadSentinelOfAnAbsentFileIsErrNoSentinel(t *testing.T) {
	_, err := ReadSentinel(filepath.Join(t.TempDir(), "plan-a1.sentinel"))

	if !errors.Is(err, ErrNoSentinel) {
		t.Fatalf("err = %v", err)
	}
}

func TestReadSentinelRejectsMalformedContent(t *testing.T) {
	for name, body := range map[string]string{
		"other outcome": `{"outcome":"done","reason":"","at":"2026-09-18T10:00:00Z"}`,
		"no outcome":    `{"reason":"","at":"2026-09-18T10:00:00Z"}`,
		"not json":      `ok`,
		"empty":         ``,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ReadSentinel(writeSentinel(t, body))

			if !errors.Is(err, ErrSentinelMalformed) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestJudge(t *testing.T) {
	ok := Sentinel{Outcome: "ok"}
	failed := Sentinel{Outcome: "failed", Reason: "tests do not compile"}
	for name, tc := range map[string]struct {
		s          Sentinel
		sErr       error
		evidenceOK bool
		missing    string
		state      StepState
		reason     string
	}{
		"ok with evidence":        {ok, nil, true, "", StepOK, ""},
		"failed sentinel":         {failed, nil, true, "", StepFailed, "tests do not compile"},
		"failed without evidence": {failed, nil, false, "no change since the step started", StepFailed, "tests do not compile"},
		"ok without evidence":     {ok, nil, false, "missing ## Tests", StepFailed, "evidence missing: missing ## Tests"},
		"malformed":               {Sentinel{}, ErrSentinelMalformed, true, "", StepFailed, "sentinel unreadable"},
	} {
		t.Run(name, func(t *testing.T) {
			state, reason := Judge(tc.s, tc.sErr, tc.evidenceOK, tc.missing)

			if state != tc.state || reason != tc.reason {
				t.Fatalf("state = %s, reason = %q", state, reason)
			}
		})
	}
}

func TestPipelineBuildsTheOrderedKinds(t *testing.T) {
	rows := map[string]StepRow{
		"plan":      {Provider: "claude", Model: "opus", Effort: "high", Rounds: 2},
		"implement": {Provider: "codex", Fallback: Fallback{Provider: "claude"}, Reviewers: []Reviewer{{Provider: "claude"}}},
	}
	prompts := map[string]string{"plan": "plan", "implement": "implement"}
	checks := map[string]string{"plan": "plan-file", "implement": "diff"}

	kinds, err := Pipeline([]string{"plan", "implement"}, rows, prompts, checks)

	want := []StepKind{
		{Name: "plan", Prompt: "plan", Check: "plan-file", Row: rows["plan"]},
		{Name: "implement", Prompt: "implement", Check: "diff", Row: rows["implement"]},
	}
	if err != nil || !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds = %+v, err = %v", kinds, err)
	}
}

func TestPipelineRejectsAnEntryWithNoRow(t *testing.T) {
	_, err := Pipeline([]string{"plan", "docs"}, map[string]StepRow{"plan": {}},
		map[string]string{"plan": "plan"}, map[string]string{"plan": "plan-file"})

	if err == nil || !strings.Contains(err.Error(), `"docs"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestPipelineRejectsACheckThatIsNotRegistered(t *testing.T) {
	_, err := Pipeline([]string{"implement"}, map[string]StepRow{"implement": {}},
		map[string]string{"implement": "implement"}, map[string]string{"implement": "bogus"})

	if err == nil || !strings.Contains(err.Error(), `"implement"`) || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("err = %v", err)
	}
}

func TestPipelineRejectsTheReviewHalfChecksOnARow(t *testing.T) {
	for _, check := range []string{"verdict", "findings"} {
		t.Run(check, func(t *testing.T) {
			_, err := Pipeline([]string{"implement"}, map[string]StepRow{"implement": {}},
				map[string]string{"implement": "implement"}, map[string]string{"implement": check})

			if err == nil || !strings.Contains(err.Error(), `"implement"`) || !strings.Contains(err.Error(), check) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestRegisterCheckAddsAStepWithoutEditingTheLoop(t *testing.T) {
	RegisterCheck("always-ok", func(EvidenceContext) (bool, string) { return true, "" })

	kinds, err := Pipeline([]string{"docs"}, map[string]StepRow{"docs": {Provider: "claude"}},
		map[string]string{"docs": "docs"}, map[string]string{"docs": "always-ok"})

	if err != nil || len(kinds) != 1 || kinds[0].Check != "always-ok" {
		t.Fatalf("kinds = %+v, err = %v", kinds, err)
	}
	if ok, _ := runCheck(t, kinds[0].Check, EvidenceContext{}); !ok {
		t.Fatal("always-ok did not pass")
	}
}

func TestPlanFileCheckRequiresOneGateCommandWhenTheItemGateIsOn(t *testing.T) {
	cases := map[string]struct{ plan, missing string }{
		"absent":      {goodPlan, "missing ## Gate"},
		"no command":  {goodPlan + "\n## Gate\nrun the tests\n", "## Gate must hold exactly one command in backticks"},
		"two":         {goodPlan + "\n## Gate\n`go test ./a` or `go test ./b`\n", "## Gate must hold exactly one command in backticks"},
		"one command": {goodPlan + "\n## Gate\n`go test ./internal/plan -run TestReadsPhases`\n", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := planCtx(c.plan, planPath)
			ctx.NeedGate = true

			ok, missing := runCheck(t, "plan-file", ctx)

			if ok != (c.missing == "") || missing != c.missing {
				t.Fatalf("ok = %v, missing = %q", ok, missing)
			}
		})
	}
}

func TestPlanFileCheckIgnoresTheGateWhenTheItemGateIsOff(t *testing.T) {
	ok, missing := runCheck(t, "plan-file", planCtx(goodPlan, planPath))

	if !ok || missing != "" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestPlanFileCheckAcceptsAnItemSkipWithCitedEvidence(t *testing.T) {
	for _, status := range []string{"already-done", "not-work"} {
		ctx := planCtx("status: "+status+"\n\n## Evidence\n- rate shown: web/deal.html:40\n", planPath)
		ctx.NeedGate = true

		ok, missing := runCheck(t, "plan-file", ctx)

		if !ok || missing != "" {
			t.Errorf("%s: ok = %v, missing = %q", status, ok, missing)
		}
	}
}

func TestPlanFileCheckRefusesAnItemSkipWithoutACitation(t *testing.T) {
	ctx := planCtx("status: already-done\n\n## Evidence\n- it is built\n", planPath)
	ctx.NeedGate = true

	ok, missing := runCheck(t, "plan-file", ctx)

	if ok || missing != "## Evidence cites no path:line" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}

func TestPlanFileCheckRefusesAnItemSkipOutsideABacklog(t *testing.T) {
	ok, missing := runCheck(t, "plan-file", planCtx("status: already-done\n\n## Evidence\n- x: a/b.go:1\n", planPath))

	if ok || missing != "no status: planned header" {
		t.Fatalf("ok = %v, missing = %q", ok, missing)
	}
}
