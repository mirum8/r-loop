package askmcp

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func serveIntake(t *testing.T, submit func([]string) (bool, string)) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	url, err := (&Intake{Submit: submit}).Serve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return url
}

func TestSubmitArgsHandsTheArgvToSubmitAndReturnsItsVerdict(t *testing.T) {
	var got []string
	url := serveIntake(t, func(argv []string) (bool, string) {
		got = argv
		return false, "phase 9 is ticked or absent"
	})
	cs := connect(t, url)

	out := call(t, cs, "submit_args", map[string]any{"argv": []string{"docs/x/todo.md", "--phases", "9"}})

	if !reflect.DeepEqual(got, []string{"docs/x/todo.md", "--phases", "9"}) {
		t.Fatalf("submit got %q", got)
	}
	if out["accepted"] != false || out["reason"] != "phase 9 is ticked or absent" {
		t.Fatalf("reply %v", out)
	}
}

func TestSubmitArgsAcceptedCarriesNoReason(t *testing.T) {
	cs := connect(t, serveIntake(t, func([]string) (bool, string) { return true, "" }))

	out := call(t, cs, "submit_args", map[string]any{"argv": []string{"todo.md"}})

	if out["accepted"] != true || out["reason"] != nil {
		t.Fatalf("reply %v", out)
	}
}

func TestIntakeURLWithoutItsTokenIsNotFound(t *testing.T) {
	url := serveIntake(t, func([]string) (bool, string) { return true, "" })
	base := url[:strings.LastIndex(url, "/")+1] + "wrong"

	resp, err := http.Post(base, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}
