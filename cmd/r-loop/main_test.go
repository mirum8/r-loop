package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestVersionPrintsVersion(t *testing.T) {
	var out, errOut bytes.Buffer

	code := run([]string{"--version"}, &out, &errOut, os.Getwd)

	if code != 0 || out.String() != "r-loop dev\n" {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
}

func TestBadUsageExitsTwoWithOneLine(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"--version", "x"}, {"no-such-todo.md", "--dry-run"}, {"statusline-tap"}, {"statusline-tap", "a", "b"}} {
		var out, errOut bytes.Buffer

		code := run(args, &out, &errOut, os.Getwd)

		if code != 2 || strings.Count(errOut.String(), "\n") != 1 {
			t.Fatalf("args=%v code=%d stderr=%q", args, code, errOut.String())
		}
	}
}

func TestAGetwdErrorWithControlBytesPrintsACleanLine(t *testing.T) {
	// given
	var actualStderr bytes.Buffer

	// when
	actualCode := run([]string{"todo.md"}, &bytes.Buffer{}, &actualStderr, func() (string, error) { return "", errors.New("getwd: \x1b[2Jgone\x07") })

	// then
	if expected := "r-loop: getwd: [2Jgone\n"; actualCode != 2 || actualStderr.String() != expected {
		t.Fatalf("code=%d stderr=%q, expected 2 and %q", actualCode, actualStderr.String(), expected)
	}
}
