package sessionhost

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const appLs = `{"spaces":[{"id":"sp1","tabs":[{"terminals":[{"terminal":"t9","pane":"pn9"}]}]}]}`

func script(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func upHerdr(t *testing.T, dir string) string {
	return script(t, dir, "herdr", `echo "$*" >> "$0.calls"; echo '{"result":{"workspaces":[]}}'`)
}

func downHerdr(t *testing.T, dir string) string {
	return script(t, dir, "herdr", `echo "$*" >> "$0.calls"; echo 'connection refused' >&2; exit 1`)
}

func upApp(t *testing.T, dir string) (string, string) {
	hcBin := script(t, dir, "hc", `echo "$*" >> "$0.calls"; echo '{}'`)
	app := script(t, dir, "herdr-app", `echo "$*" >> "$0.calls"
case "$1" in
  ls) echo '`+appLs+`' ;;
  *) echo '{"space":"sp1","tab":"tb1","focused_pane":"pn9"}' ;;
esac`)
	return hcBin, app
}

func downApp(t *testing.T, dir string) (string, string) {
	hcBin := script(t, dir, "hc", `echo '{"error":{"kind":"unavailable","message":"no core"}}' >&2; exit 1`)
	app := script(t, dir, "herdr-app", `echo '{"error":"window not open"}'; exit 1`)
	return hcBin, app
}

func env(herdrBin, hcBin, appBin string) Env {
	return Env{Herdr: herdrBin, HC: hcBin, App: appBin, LookPath: exec.LookPath}
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	var e *ExitError
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an ExitError", err)
	}
	return e.Code
}

func calls(path string) string {
	data, _ := os.ReadFile(path + ".calls")
	return string(data)
}

func TestHerdrPaneIDPicksHerdrWithoutProbing(t *testing.T) {
	dir := t.TempDir()
	h := downHerdr(t, dir)
	hcBin, app := upApp(t, dir)
	e := env(h, hcBin, app)
	e.HerdrPane, e.HCTerminal = "w1:p1", "t9"

	conn, err := Connect(e, "auto")

	if err != nil || conn.Name != "herdr" || conn.Source != "auto: HERDR_PANE_ID" || conn.Self != "w1:p1" {
		t.Fatalf("conn=%+v err=%v", conn, err)
	}
	if calls(h) != "" || calls(app) != "" {
		t.Fatalf("probed: herdr=%q app=%q", calls(h), calls(app))
	}
}

func TestHCTerminalIDPicksTheAppAndItsPane(t *testing.T) {
	dir := t.TempDir()
	hcBin, app := upApp(t, dir)
	e := env(upHerdr(t, dir), hcBin, app)
	e.HCTerminal = "t9"

	conn, err := Connect(e, "auto")

	if err != nil || conn.Name != "app" || conn.Source != "auto: HC_TERMINAL_ID" || conn.Self != "pn9" {
		t.Fatalf("conn=%+v err=%v", conn, err)
	}
}

func TestAppSelfIsEmptyForATerminalOutsideTheWindow(t *testing.T) {
	dir := t.TempDir()
	hcBin, app := upApp(t, dir)
	e := env(upHerdr(t, dir), hcBin, app)
	e.HCTerminal = "t-elsewhere"

	conn, err := Connect(e, "auto")

	if err != nil || conn.Self != "" {
		t.Fatalf("conn=%+v err=%v", conn, err)
	}
}

func TestProbeUsesTheOneReachableHost(t *testing.T) {
	for _, tc := range []struct {
		name      string
		herdrUp   bool
		wantHost  string
		wantSelf  string
		wantProbe string
	}{
		{"herdr up", true, "herdr", "", "auto: probe"},
		{"app up", false, "app", "", "auto: probe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			h := downHerdr(t, dir)
			hcBin, app := upApp(t, dir)
			if tc.herdrUp {
				h = upHerdr(t, dir)
				hcBin, app = downApp(t, dir)
			}

			conn, err := Connect(env(h, hcBin, app), "auto")

			if err != nil || conn.Name != tc.wantHost || conn.Source != tc.wantProbe || conn.Self != tc.wantSelf {
				t.Fatalf("conn=%+v err=%v", conn, err)
			}
		})
	}
}

func TestProbeWithBothReachableExits2(t *testing.T) {
	dir := t.TempDir()
	hcBin, app := upApp(t, dir)

	_, err := Connect(env(upHerdr(t, dir), hcBin, app), "auto")

	if exitCode(t, err) != 2 || err.Error() != "both herdr and herdr-app are running; set host: herdr or host: app" {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeWithNeitherReachableExits4NamingEachError(t *testing.T) {
	dir := t.TempDir()
	hcBin, app := downApp(t, dir)

	_, err := Connect(env(downHerdr(t, dir), hcBin, app), "auto")

	if exitCode(t, err) != 4 || !strings.HasPrefix(err.Error(), "no session host reachable (herdr: ") ||
		!strings.Contains(err.Error(), "connection refused") || !strings.Contains(err.Error(), "; herdr-app: hc: unavailable: no core") {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeWithOnlyOneBinaryOnThePathUsesIt(t *testing.T) {
	dir := t.TempDir()

	conn, err := Connect(env(upHerdr(t, dir), filepath.Join(dir, "no-hc"), filepath.Join(dir, "no-app")), "auto")

	if err != nil || conn.Name != "herdr" {
		t.Fatalf("conn=%+v err=%v", conn, err)
	}
}

func TestProbeWithNoBinaryExits127(t *testing.T) {
	dir := t.TempDir()

	_, err := Connect(env(filepath.Join(dir, "no-herdr"), filepath.Join(dir, "no-hc"), filepath.Join(dir, "no-app")), "auto")

	if exitCode(t, err) != 127 {
		t.Fatalf("err = %v", err)
	}
}

func TestEnvPickedHostWithoutItsBinaryExits127(t *testing.T) {
	dir := t.TempDir()
	e := env(upHerdr(t, dir), filepath.Join(dir, "no-hc"), filepath.Join(dir, "no-app"))
	e.HCTerminal = "t9"

	_, err := Connect(e, "auto")

	if exitCode(t, err) != 127 || !strings.Contains(err.Error(), "no-hc") {
		t.Fatalf("err = %v", err)
	}
}

func TestExplicitChoiceSkipsDetection(t *testing.T) {
	dir := t.TempDir()
	hcBin, app := upApp(t, dir)
	e := env(upHerdr(t, dir), hcBin, app)
	e.HerdrPane = "w1:p1"

	conn, err := Connect(e, "app")

	if err != nil || conn.Name != "app" || conn.Source != "" {
		t.Fatalf("conn=%+v err=%v", conn, err)
	}
	if !strings.Contains(calls(hcBin), "server status --json") {
		t.Fatalf("hc calls = %q, want a Reachable probe", calls(hcBin))
	}
}

func TestExplicitChoiceExits4WhenUnreachableAnd127WhenMissing(t *testing.T) {
	dir := t.TempDir()

	_, err := Connect(env(downHerdr(t, dir), "", ""), "herdr")
	if exitCode(t, err) != 4 || !strings.HasPrefix(err.Error(), "herdr unreachable: ") {
		t.Fatalf("unreachable: err = %v", err)
	}

	_, err = Connect(env(filepath.Join(dir, "no-herdr"), "", ""), "herdr")
	if exitCode(t, err) != 127 {
		t.Fatalf("missing: err = %v", err)
	}
}

func TestUnknownChoiceExits2(t *testing.T) {
	_, err := Connect(env("", "", ""), "tmux")

	if exitCode(t, err) != 2 {
		t.Fatalf("err = %v", err)
	}
}

func codexOnPath(t *testing.T, version string) Env {
	dir := t.TempDir()
	script(t, dir, "codex", `echo "codex-cli `+version+`"`)
	return Env{LookPath: func(name string) (string, error) { return exec.LookPath(filepath.Join(dir, name)) }}
}

func TestPreflightGatesCodexBelow0166OnTheApp(t *testing.T) {
	err := codexOnPath(t, "0.165.9").preflight("app")([]string{"claude", "codex"})
	if exitCode(t, err) != 2 || err.Error() != "codex 0.165.9 reports no status to hc; 0.166 or later is needed — run codex update" {
		t.Fatalf("0.165.9: err = %v", err)
	}

	if err := codexOnPath(t, "0.166.0").preflight("app")([]string{"codex"}); err != nil {
		t.Fatalf("0.166.0: err = %v", err)
	}
}

func TestPreflightIsANoOpOnHerdrAndWithoutCodex(t *testing.T) {
	e := codexOnPath(t, "0.1.0")

	if err := e.preflight("herdr")([]string{"codex"}); err != nil {
		t.Fatalf("herdr: err = %v", err)
	}
	if err := e.preflight("app")([]string{"claude"}); err != nil {
		t.Fatalf("no codex: err = %v", err)
	}
}
