package app

import (
	"bytes"
	"testing"
)

func TestFailWritesTheErrorWithoutControlBytesAndKeepsItsExitCode(t *testing.T) {
	// given
	var actualStderr bytes.Buffer

	// when
	actualCode := fail(Env{Stderr: &actualStderr}, exit(3, "a\x1b[2Jb\x07c\nd"))

	// then
	if expected := "r-loop: a[2Jbc d\n"; actualCode != 3 || actualStderr.String() != expected {
		t.Fatalf("code=%d stderr=%q, expected 3 and %q", actualCode, actualStderr.String(), expected)
	}
}

func TestAPlanPathWithControlBytesExits2WithACleanStderrLine(t *testing.T) {
	// given
	f := newFixture(t)

	// when
	actualCode := f.main("x\x1b[2Jy\x1b]0;pwn\a.md", "--dry-run", "--plain")

	// then
	if expected := "r-loop: open " + f.root + "/x[2Jy]0;pwn.md: no such file or directory\n"; actualCode != 2 || f.err.String() != expected {
		t.Fatalf("code=%d stderr=%q, expected 2 and %q", actualCode, f.err.String(), expected)
	}
}

func TestAPhaseTitleWithControlBytesAndNoChecklistExits2WithTheTitleCleaned(t *testing.T) {
	// given
	f := newFixture(t)
	f.write("docs/topic/todo.md", "# Plan\n\n### Phase 1 — One\n- [ ] a\n\n### Phase 2 — x\x1b[2Jy\x1b]0;pwn\x07z\n**Done when:** `go test ./...`\n")
	f.commit()

	// when
	actualCode := f.main(f.todo, "--dry-run", "--plain")

	// then
	if expected := "r-loop: Phase 2 — x[2Jy]0;pwnz: no checklist items\n"; actualCode != 2 || f.err.String() != expected {
		t.Fatalf("code=%d stderr=%q, expected 2 and %q", actualCode, f.err.String(), expected)
	}
}

func TestAFlagWithControlBytesPrintsACleanUsageLine(t *testing.T) {
	// given
	f := newFixture(t)

	// when
	actualCode := f.main(f.todo, "-x\x1b[2J\x07")

	// then
	if expected := "usage: r-loop <todo.md> [flags]: flag provided but not defined: -x[2J\n"; actualCode != 2 || f.err.String() != expected {
		t.Fatalf("code=%d stderr=%q, expected 2 and %q", actualCode, f.err.String(), expected)
	}
}

func TestFailWritesTheErrorWithoutC1ControlCharacters(t *testing.T) {
	// given
	var actualStderr bytes.Buffer

	// when
	actualCode := fail(Env{Stderr: &actualStderr}, exit(2, "a\u009b2Jb\u0085c\u009dd"))

	// then
	if expected := "r-loop: a2Jbcd\n"; actualCode != 2 || actualStderr.String() != expected {
		t.Fatalf("code=%d stderr=%q, expected 2 and %q", actualCode, actualStderr.String(), expected)
	}
}
