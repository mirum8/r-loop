package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"r-loop/internal/core"
)

type intakeHost struct {
	mu            sync.Mutex
	args          []string
	prompt        string
	replies       []map[string]any
	closed        bool
	submit        [][]string
	errs          []error
	done          chan struct{}
	configAtStart bool
}

func (h *intakeHost) Reachable() error { return nil }
func (h *intakeHost) Open(spec core.OpenSpec) (core.Workspace, error) {
	return core.Workspace{ID: "ws-intake", RootPane: "pane-intake"}, nil
}
func (h *intakeHost) Start(pane, name, kind string, args []string) (core.Agent, error) {
	h.mu.Lock()
	h.args = args
	for i, arg := range args {
		if arg == "--mcp-config" && i+1 < len(args) {
			_, err := os.Stat(args[i+1])
			h.configAtStart = err == nil
		}
	}
	h.mu.Unlock()
	return core.Agent{Name: name, Pane: pane}, nil
}

func TestIntakeWritesItsMCPConfigWhenTheRepoRootAndTempDirHaveASpace(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, tmp := filepath.Join(base, "repo with space"), filepath.Join(base, "tmp with space")
	for _, dir := range []string{root, tmp} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f := newFixtureIn(t, root)
	f.commit()
	t.Setenv("TMPDIR", tmp)
	host := &intakeHost{done: make(chan struct{}), submit: [][]string{{"docs/topic/todo.md", "--phases", "2"}}}
	opts, err := f.intake(host, "the topic plan, only phase 2", "--plain")
	<-host.done
	if err != nil || len(host.errs) != 0 || !host.configAtStart || !reflect.DeepEqual(opts.Phases, []string{"2"}) {
		t.Fatalf("opts=%+v err=%v host errs=%v config at start=%v", opts, err, host.errs, host.configAtStart)
	}
	for i, arg := range host.args {
		if arg == "--mcp-config" && i+1 < len(host.args) {
			if !strings.HasPrefix(host.args[i+1], tmp) {
				t.Fatalf("config path %q does not start with %q", host.args[i+1], tmp)
			}
			return
		}
	}
	t.Fatal("missing --mcp-config")
}

func TestAFreeFormRunWithAMissingIntakeBinaryExits127BeforeStarting(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  ghost:\n    kind: rloop-no-such-binary\n    doneSignal: sentinel\n    ask: mcp\nintake:\n  provider: ghost\n")
	f.commit()
	code := f.main("the topic plan")
	if code != 127 || !strings.Contains(f.err.String(), "intake.provider: provider ghost binary rloop-no-such-binary not found on PATH") || f.herdrCalled() {
		t.Fatalf("code=%d stderr=%q herdr called=%v", code, f.err.String(), f.herdrCalled())
	}
}
func (h *intakeHost) Prompt(agent, text string, wait bool, timeout time.Duration) error {
	h.mu.Lock()
	h.prompt = text
	h.mu.Unlock()
	go h.converse()
	return nil
}
func (h *intakeHost) State(agent string) (core.AgentState, error)            { return core.AgentWorking, nil }
func (h *intakeHost) AgentPane(agent string) (string, error)                 { return "", nil }
func (h *intakeHost) Read(agent string, lines int) (string, error)           { return "", nil }
func (h *intakeHost) Screen(agent string) (string, error)                    { return "", nil }
func (h *intakeHost) SendKeys(agent string, keys ...string) error            { return nil }
func (h *intakeHost) SendText(agent, text string) error                      { return nil }
func (h *intakeHost) Interrupt(agent string) error                           { return nil }
func (h *intakeHost) Tag(workspaceID string, tokens map[string]string) error { return nil }
func (h *intakeHost) Split(pane, direction, cwd string, env map[string]string) (string, error) {
	return "", nil
}
func (h *intakeHost) ClosePane(pane string) error { return nil }
func (h *intakeHost) Close(workspaceID string) error {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	return nil
}

func (h *intakeHost) converse() {
	defer close(h.done)
	h.mu.Lock()
	args := h.args
	h.mu.Unlock()
	var url string
	for i, a := range args {
		if a == "--mcp-config" && i+1 < len(args) {
			var cfg struct {
				MCPServers map[string]struct{ URL string } `json:"mcpServers"`
			}
			data, err := os.ReadFile(args[i+1])
			if err == nil {
				err = json.Unmarshal(data, &cfg)
			}
			if err != nil {
				h.fail(err)
				return
			}
			url = cfg.MCPServers["r-loop"].URL
		}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: url, MaxRetries: -1}, nil)
	if err != nil {
		h.fail(err)
		return
	}
	defer cs.Close()
	for _, argv := range h.submit {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "submit_args", Arguments: map[string]any{"argv": argv}})
		if err != nil {
			h.fail(err)
			return
		}
		m, _ := res.StructuredContent.(map[string]any)
		h.mu.Lock()
		h.replies = append(h.replies, m)
		h.mu.Unlock()
	}
}

func (h *intakeHost) fail(err error) {
	h.mu.Lock()
	h.errs = append(h.errs, err)
	h.mu.Unlock()
}

func (f *fixture) intake(host *intakeHost, args ...string) (Options, error) {
	f.t.Helper()
	given, _, err := parseFlags(args)
	if err != nil {
		f.t.Fatal(err)
	}
	in, err := newIntake(args, given, f.env)
	if err != nil {
		return Options{}, err
	}
	in.host, in.poll = host, time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return in.run(ctx)
}

func TestFreeFormIsAnythingButOneMarkdownPath(t *testing.T) {
	for positional, want := range map[string]bool{
		"":                                false,
		"docs/topic/todo.md":              false,
		"missing.md":                      false,
		"the loop plan, phases 3 and 4":   true,
		"docs/topic/todo.md|only phase 3": true,
		"a.md|b.md":                       true,
		"todo":                            true,
	} {
		var words []string
		if positional != "" {
			words = strings.Split(positional, "|")
		}
		if got := freeForm(words); got != want {
			t.Errorf("freeForm(%q) = %v, want %v", words, got, want)
		}
	}
}

func TestFreeTextGoesToTheIntakeWhichNeedsHerdr(t *testing.T) {
	f := newFixture(t)
	f.commit()
	f.fakeHerdr(1)

	code := f.main("the topic plan, only phase 2", "--plain")

	if code != 4 || strings.Count(f.err.String(), "\n") != 1 || !strings.Contains(f.err.String(), "herdr server unreachable") {
		t.Fatalf("code=%d stderr=%q", code, f.err.String())
	}
}

func TestIntakeRefusesAnInvalidArgvThenReturnsTheConfirmedOne(t *testing.T) {
	f := newFixture(t)
	f.commit()
	host := &intakeHost{done: make(chan struct{}), submit: [][]string{
		{"docs/topic/todo.md", "--phases", "1"},
		{"docs/topic/todo", "--phases", "2"},
		{"docs/topic/todo.md", "--provider", "nope=codex"},
		{"docs/topic/todo.md", "--phases", "2", "--dry-run"},
	}}

	opts, err := f.intake(host, "the topic plan, only phase 2", "--plain")
	<-host.done

	if err != nil {
		t.Fatalf("err %v host errs %v", err, host.errs)
	}
	if opts.Todo != "docs/topic/todo.md" || !reflect.DeepEqual(opts.Phases, []string{"2"}) || !opts.DryRun || opts.Plain {
		t.Fatalf("opts %+v", opts)
	}
	for i, want := range []string{"phase 1 is ticked or absent", "must end in .md", `unknown step "nope"`} {
		if r := host.replies[i]; r["accepted"] != false || !strings.Contains(r["reason"].(string), want) {
			t.Errorf("reply %d = %v, want a refusal with %q", i, r, want)
		}
	}
	if r := host.replies[3]; r["accepted"] != true {
		t.Errorf("reply 3 = %v", r)
	}
	if got := f.err.String(); got != "r-loop: resolved: r-loop docs/topic/todo.md --phases 2 --dry-run\n" {
		t.Errorf("stderr %q", got)
	}
	if !host.closed {
		t.Error("intake workspace left open")
	}
	for _, want := range []string{"the topic plan, only phase 2 --plain", "-phases n,n", f.root} {
		if !strings.Contains(host.prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, host.prompt)
		}
	}
}

func TestIntakeRunsTheIntakeRowsProviderModelAndEffort(t *testing.T) {
	f := newFixture(t)
	f.commit()
	given, _, _ := parseFlags([]string{"phase 2", "--provider", "intake=codex", "--model", "intake=gpt-5.6-mini"})

	in, err := newIntake([]string{"phase 2"}, given, f.env)
	if err != nil {
		t.Fatal(err)
	}

	if in.provider.Kind != "codex" || in.cfg.Model != "gpt-5.6-mini" || in.cfg.Effort != "medium" {
		t.Fatalf("provider %+v cfg %+v", in.provider, in.cfg)
	}
}

func TestAnIntakeProviderWithoutMCPExits2BeforeStarting(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  plainbot:\n    kind: codex\n    doneSignal: sentinel\n    ask: none\nintake:\n  provider: plainbot\n")
	f.commit()

	code := f.main("the topic plan")

	if code != 2 || !strings.Contains(f.err.String(), "intake.provider: provider plainbot has no MCP ask channel") || f.herdrCalled() {
		t.Fatalf("code=%d stderr=%q herdr called %v", code, f.err.String(), f.herdrCalled())
	}
}

func TestAnIntakeProviderWithoutMCPIsRefusedInPreflight(t *testing.T) {
	f := newFixture(t)
	f.write(".r-loop/config.yaml", "providers:\n  plainbot:\n    kind: codex\n    doneSignal: sentinel\n    ask: none\nintake:\n  provider: plainbot\n")
	f.commit()

	_, err := f.preflight(f.todo, "--plain", "--dry-run")

	if code := exitCode(t, err); code != 2 || !strings.Contains(err.Error(), "intake.provider: provider plainbot has no MCP ask channel") {
		t.Fatalf("code=%d err=%v", code, err)
	}
}

func TestShellJoinQuotesOnlyWhatNeedsIt(t *testing.T) {
	got := shellJoin([]string{"docs/my plan/todo.md", "--phases", "3,4", "--model", "plan=it's", ""})

	if want := `'docs/my plan/todo.md' --phases 3,4 --model 'plan=it'\''s' ''`; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestASecondSubmitIsRefusedEvenAfterTheFirstWasTaken(t *testing.T) {
	f := newFixture(t)
	f.commit()
	in, err := newIntake([]string{"phase 2"}, Options{}, f.env)
	if err != nil {
		t.Fatal(err)
	}

	first, _ := in.submit([]string{"docs/topic/todo.md", "--phases", "2"})
	<-in.accepted
	second, reason := in.submit([]string{"docs/topic/todo.md", "--phases", "3"})

	if !first || second || reason != "a command line was already accepted" {
		t.Fatalf("first %v second %v reason %q", first, second, reason)
	}
}
