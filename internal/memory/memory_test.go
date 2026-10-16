package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"r-loop/internal/core"
)

var at = time.Date(2026, 10, 16, 10, 31, 2, 0, time.UTC)

func TestWriteThenListRoundTrips(t *testing.T) {
	// given
	d := New(t.TempDir())

	// when
	id, err := d.Write(core.MemoryFact{Scope: "step", Description: "Port 5432 must be free!", Body: "stop postgres", Run: "run-1", At: at})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := d.List()

	// then
	if err != nil {
		t.Fatal(err)
	}
	if id != "20261016-port-5432-must-be-free" {
		t.Fatalf("id = %q", id)
	}
	want := core.MemoryFact{ID: id, Scope: "step", Description: "Port 5432 must be free!", Body: "stop postgres\n", Run: "run-1", At: at, Path: filepath.Join(d.Path, id+".md")}
	if len(facts) != 1 || facts[0] != want {
		t.Fatalf("facts = %+v\nwant %+v", facts, want)
	}
}

func TestListOfAMissingFolderIsEmpty(t *testing.T) {
	facts, err := New(t.TempDir()).List()
	if err != nil || len(facts) != 0 {
		t.Fatalf("facts = %v, err = %v", facts, err)
	}
}

func TestACollidingIdGetsASuffix(t *testing.T) {
	// given
	d := New(t.TempDir())
	fact := core.MemoryFact{Scope: "all", Description: "same", At: at}
	if _, err := d.Write(fact); err != nil {
		t.Fatal(err)
	}

	// when
	second, _ := d.Write(fact)
	third, _ := d.Write(fact)

	// then
	if second != "20261016-same-2" || third != "20261016-same-3" {
		t.Fatalf("ids = %q, %q", second, third)
	}
}

func TestALongDescriptionIsCutInTheId(t *testing.T) {
	d := New(t.TempDir())

	id, err := d.Write(core.MemoryFact{Scope: "all", Description: strings.Repeat("abc ", 40), At: at})

	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimPrefix(id, "20261016-"); len(got) > 48 || strings.HasSuffix(got, "-") {
		t.Fatalf("id = %q", id)
	}
}

func TestAnUnreadableFileIsSkippedWithAWarning(t *testing.T) {
	// given
	d := New(t.TempDir())
	var warnings []string
	d.Warn = func(msg string) { warnings = append(warnings, msg) }
	if _, err := d.Write(core.MemoryFact{Scope: "all", Description: "kept", At: at}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.Path, "broken.md"), []byte("no front matter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.Path, "notes.txt"), []byte("ignored\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// when
	facts, err := d.List()

	// then
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].Description != "kept" {
		t.Fatalf("facts = %+v", facts)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "broken.md") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestDeleteRemovesTheFact(t *testing.T) {
	d := New(t.TempDir())
	id, _ := d.Write(core.MemoryFact{Scope: "all", Description: "gone soon", At: at})

	if err := d.Delete(id); err != nil {
		t.Fatal(err)
	}

	if facts, _ := d.List(); len(facts) != 0 {
		t.Fatalf("facts = %+v", facts)
	}
}

func TestDeleteRefusesAnIdOutsideTheFolder(t *testing.T) {
	// given
	root := t.TempDir()
	d := New(root)
	outside := filepath.Join(root, ".r-loop", "x.md")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	// when
	errs := []error{d.Delete("../x"), d.Delete("a/b"), d.Delete("")}

	// then
	for i, err := range errs {
		if err == nil {
			t.Fatalf("delete %d: want a refusal", i)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file: %v", err)
	}
}

func TestDeleteOfAMissingFactNamesIt(t *testing.T) {
	err := New(t.TempDir()).Delete("20261016-none")

	if err == nil || err.Error() != "fact 20261016-none not found" {
		t.Fatalf("err = %v", err)
	}
}

func TestWriteFailsWhenTheCollisionCheckCannotStatTheFolder(t *testing.T) {
	// given
	d := New(t.TempDir())
	if err := os.MkdirAll(d.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d.Path, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(d.Path, 0o755) })

	// when
	_, err := d.Write(core.MemoryFact{Scope: "all", Description: "d", At: at})

	// then
	if err == nil {
		t.Fatal("want an error")
	}
}
