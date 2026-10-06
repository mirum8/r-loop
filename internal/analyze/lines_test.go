package analyze

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeText(t *testing.T, dir, rel, content string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	for rel, content := range files {
		writeText(t, dir, rel, content)
	}
	run(t, dir, "add", "-A")
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func changed(t *testing.T, dir string) Changes {
	t.Helper()
	ch, err := Changed(context.Background(), dir)
	if err != nil {
		t.Fatalf("Changed: %v", err)
	}
	return ch
}

func TestChangedListsTheAddedAndModifiedLines(t *testing.T) {
	// given
	dir := newGitRepo(t, map[string]string{"a.go": "1\n2\n3\n4\n5\n"})
	writeText(t, dir, "a.go", "1\nB\n3\n5\n6\n7\n")

	// when
	actual := changed(t, dir)

	// then
	expected := map[int]bool{2: true, 5: true, 6: true}
	if !reflect.DeepEqual(actual.Lines["a.go"], expected) {
		t.Fatalf("actual %v, expected %v", actual.Lines["a.go"], expected)
	}
}

func TestChangedListsAFileWithOnlyDeletedLinesWithNoLines(t *testing.T) {
	// given
	dir := newGitRepo(t, map[string]string{"d.go": "x\ny\n"})
	writeText(t, dir, "d.go", "x\n")

	// when
	actual := changed(t, dir)

	// then
	lines, ok := actual.Lines["d.go"]
	if !ok || len(lines) != 0 || !reflect.DeepEqual(actual.Files(), []string{"d.go"}) {
		t.Fatalf("actual lines %v (present %v), files %v", lines, ok, actual.Files())
	}
}

func TestChangedTakesANewUntrackedFileWhole(t *testing.T) {
	// given
	dir := newGitRepo(t, map[string]string{"a.go": "1\n"})
	writeText(t, dir, "new/b.go", "package b\n")

	// when
	actual := changed(t, dir)

	// then
	if !reflect.DeepEqual(actual.Untracked, map[string]bool{"new/b.go": true}) || !actual.Has("new/b.go", 99) {
		t.Fatalf("actual untracked %v", actual.Untracked)
	}
}

func TestChangedTakesAMovedFileWholeUnderItsNewPath(t *testing.T) {
	// given
	dir := newGitRepo(t, map[string]string{"old/a.go": "1\n2\n"})
	if err := os.Mkdir(filepath.Join(dir, "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "mv", "old/a.go", "new/a.go")

	// when
	actual := changed(t, dir)

	// then
	if !reflect.DeepEqual(actual.Files(), []string{"new/a.go"}) || !reflect.DeepEqual(actual.Lines["new/a.go"], map[int]bool{1: true, 2: true}) {
		t.Fatalf("actual files %v, lines %v", actual.Files(), actual.Lines)
	}
}

func TestChangedLeavesOutADeletedFile(t *testing.T) {
	// given
	dir := newGitRepo(t, map[string]string{"old.go": "1\n", "keep.txt": "k\n"})
	if err := os.Remove(filepath.Join(dir, "old.go")); err != nil {
		t.Fatal(err)
	}

	// when
	actual := changed(t, dir)

	// then
	if len(actual.Files()) != 0 {
		t.Fatalf("actual files %v, expected none", actual.Files())
	}
}

func TestChangedReadsPathsWithASpaceAndANonASCIIName(t *testing.T) {
	// given
	dir := newGitRepo(t, map[string]string{"sp ace.go": "1\n", "é.go": "1\n"})
	writeText(t, dir, "sp ace.go", "one\n")
	writeText(t, dir, "é.go", "one\n")

	// when
	actual := changed(t, dir)

	// then
	expected := map[string]map[int]bool{"sp ace.go": {1: true}, "é.go": {1: true}}
	if !reflect.DeepEqual(actual.Lines, expected) {
		t.Fatalf("actual %v, expected %v", actual.Lines, expected)
	}
}

func TestChangedDoesNotReadAnAddedPlusPlusLineAsAHeader(t *testing.T) {
	// given
	dir := newGitRepo(t, map[string]string{"a.go": "1\n"})
	writeText(t, dir, "a.go", "1\n++ x\n")

	// when
	actual := changed(t, dir)

	// then
	if !reflect.DeepEqual(actual.Files(), []string{"a.go"}) || !reflect.DeepEqual(actual.Lines["a.go"], map[int]bool{2: true}) {
		t.Fatalf("actual files %v, lines %v", actual.Files(), actual.Lines)
	}
}

func TestChangedIgnoresTheUsersDiffConfig(t *testing.T) {
	// given
	dir := newGitRepo(t, map[string]string{"a.go": "1\n"})
	global := os.Getenv("GIT_CONFIG_GLOBAL")
	run(t, dir, "config", "--file", global, "diff.noprefix", "true")
	run(t, dir, "config", "--file", global, "diff.mnemonicPrefix", "true")
	run(t, dir, "config", "--file", global, "color.diff", "always")
	run(t, dir, "config", "--file", global, "diff.relative", "false")
	writeText(t, dir, "a.go", "one\n")

	// when
	actual := changed(t, dir)

	// then
	if !reflect.DeepEqual(actual.Lines["a.go"], map[int]bool{1: true}) {
		t.Fatalf("actual lines %v", actual.Lines)
	}
}

func TestChangedIgnoresTheUsersInterHunkContext(t *testing.T) {
	// given
	dir := newGitRepo(t, map[string]string{"a.go": "1\n2\n3\n4\n5\n"})
	run(t, dir, "config", "--file", os.Getenv("GIT_CONFIG_GLOBAL"), "diff.interHunkContext", "5")
	writeText(t, dir, "a.go", "one\n2\nthree\n4\n5\n")

	// when
	actual := changed(t, dir)

	// then
	expected := map[int]bool{1: true, 3: true}
	if !reflect.DeepEqual(actual.Lines["a.go"], expected) {
		t.Fatalf("actual %v, expected %v", actual.Lines["a.go"], expected)
	}
}

func TestChangedFilesIsTheSortedUnionOfTrackedAndUntracked(t *testing.T) {
	// given
	dir := newGitRepo(t, map[string]string{"b.go": "1\n"})
	writeText(t, dir, "b.go", "2\n")
	writeText(t, dir, "a.go", "1\n")

	// when
	actual := changed(t, dir).Files()

	// then
	if !reflect.DeepEqual(actual, []string{"a.go", "b.go"}) {
		t.Fatalf("actual %v", actual)
	}
}

func TestChangedFailsOutsideARepository(t *testing.T) {
	// given
	t.Setenv("GIT_CEILING_DIRECTORIES", os.TempDir())
	dir := t.TempDir()

	// when
	_, err := Changed(context.Background(), dir)

	// then
	if err == nil || !strings.HasPrefix(err.Error(), "git diff:") {
		t.Fatalf("err %v, expected one starting git diff:", err)
	}
}
