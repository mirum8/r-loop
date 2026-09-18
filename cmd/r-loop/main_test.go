package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionPrintsVersion(t *testing.T) {
	var out, errOut bytes.Buffer

	code := run([]string{"--version"}, &out, &errOut)

	if code != 0 || out.String() != "r-loop dev\n" {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
}

func TestAnythingElseExitsTwoWithUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"todo.md"}, {"--help"}, {"--version", "x"}} {
		var out, errOut bytes.Buffer

		code := run(args, &out, &errOut)

		if code != 2 || !strings.HasPrefix(errOut.String(), "usage: r-loop") {
			t.Fatalf("args=%v code=%d stderr=%q", args, code, errOut.String())
		}
	}
}
