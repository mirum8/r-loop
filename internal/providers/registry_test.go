package providers

import (
	"encoding/json"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"r-loop/internal/core"
)

func projectBlocks(t *testing.T, src string) map[string]yaml.Node {
	t.Helper()
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	out := map[string]yaml.Node{}
	m := doc.Content[0]
	for i := 0; i < len(m.Content); i += 2 {
		out[m.Content[i].Value] = *m.Content[i+1]
	}
	return out
}

func TestArgsKeepAPlaceholderValueWithSpacesAsOneArgument(t *testing.T) {
	claude, err := NewRegistry(nil, nil, t.TempDir()).Resolve("claude")
	if err != nil {
		t.Fatal(err)
	}
	path := "/x/repo with space/.r-loop/runs/r1/watchdog.mcp.json"
	if got, want := Args(claude, "opus", "", "", path, ""), []string{"--model", "opus", "--mcp-config", path}; !reflect.DeepEqual(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}
	blocks := projectBlocks(t, "mine:\n  kind: claude\n  askFlag: \"--cfg={mcpConfig}\"\n  doneSignal: sentinel\n  ask: mcp\n")
	mine, err := NewRegistry(blocks, nil, t.TempDir()).Resolve("mine")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Args(mine, "", "", "", "/x/a b/c.json", ""), []string{"--cfg=/x/a b/c.json"}; !reflect.DeepEqual(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}
}

func TestArgsSplitTemplatesWithoutPlaceholdersOnWhitespace(t *testing.T) {
	blocks := projectBlocks(t, "mine:\n  kind: codex\n  flags: \"  -c a=1\\t--no-alt-screen   -x \"\n  modelFlag: \"-c   model={model}\"\n  doneSignal: sentinel\n")
	mine, err := NewRegistry(blocks, nil, t.TempDir()).Resolve("mine")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := Args(mine, "gpt-5", "", "", "", ""), []string{"-c", "a=1", "--no-alt-screen", "-x", "-c", "model=gpt-5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}
}

func TestShippedClaudeBlock(t *testing.T) {
	r := NewRegistry(nil, nil, t.TempDir())

	p, err := r.Resolve("claude")

	if err != nil {
		t.Fatal(err)
	}
	want := Provider{Name: "claude", Kind: "claude", ModelFlag: "--model {model}", EffortFlag: "--effort {effort}",
		AskFlag: "--mcp-config {mcpConfig}", DirFlag: "--add-dir {dir}", DoneSignal: "sentinel", Ask: "mcp", Review: "/code-review", Source: "shipped"}
	if p != want {
		t.Errorf("got %+v\nwant %+v", p, want)
	}
}

func TestShippedCodexBlock(t *testing.T) {
	r := NewRegistry(nil, nil, t.TempDir())

	p, err := r.Resolve("codex")

	if err != nil {
		t.Fatal(err)
	}
	want := Provider{Name: "codex", Kind: "codex", Flags: "-c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true", ModelFlag: "-c model={model}", EffortFlag: "-c model_reasoning_effort={effort}",
		AskFlag: "-c mcp_servers.r-loop.url={url}", DirFlag: `-c sandbox_workspace_write.writable_roots=["{dir}"]`, DoneSignal: "sentinel", Ask: "mcp",
		Review:      "/review Review the current code changes (staged, unstaged, and untracked files) and provide prioritized findings.",
		ReviewStart: ">> Code review started", ReviewDone: "<< Code review finished", Source: "shipped"}
	if p != want {
		t.Errorf("got %+v\nwant %+v", p, want)
	}
}

func TestShippedCodexFlagsLetTheSandboxOpenLocalListeners(t *testing.T) {
	r := NewRegistry(nil, nil, t.TempDir())
	codex, err := r.Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	args := Args(codex, "", "", "http://127.0.0.1:9/ask", "", "")
	found := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-c" && args[i+1] == "sandbox_workspace_write.network_access=true" {
			found = true
		}
	}
	if !found {
		t.Errorf("step args %q lack the network access flag", args)
	}
}

func TestShippedCodexFlagsLetACodexSandboxOpenALocalListener(t *testing.T) {
	if os.Getenv("R_LOOP_LISTEN_HELPER") == "1" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if os.Getenv("CODEX_SANDBOX") != "" {
		t.Skip("cannot start a nested codex sandbox")
	}
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex not on PATH")
	}
	r := NewRegistry(nil, nil, t.TempDir())
	codex, err := r.Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"sandbox", "-c", "sandbox_mode=workspace-write"}, strings.Fields(codex.Flags)...)
	args = append(args, "--", os.Args[0], "-test.run=^TestShippedCodexFlagsLetACodexSandboxOpenALocalListener$", "-test.count=1")
	cmd := exec.Command(codexBin, args...)
	cmd.Env = append(os.Environ(), "R_LOOP_LISTEN_HELPER=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("codex sandbox with the shipped flags could not open a local listener: %v\n%s", err, out)
	}
}

func TestProjectBlockReplacesShippedCodexWhole(t *testing.T) {
	blocks := projectBlocks(t, "codex:\n  kind: codex\n  modelFlag: \"--m {model}\"\n  doneSignal: sentinel\n")
	r := NewRegistry(blocks, map[string]string{"providers.codex": ".r-loop/config.yaml:providers.codex"}, t.TempDir())

	p, err := r.Resolve("codex")

	if err != nil {
		t.Fatal(err)
	}
	want := Provider{Name: "codex", Kind: "codex", ModelFlag: "--m {model}", DoneSignal: "sentinel", Ask: "none",
		Source: ".r-loop/config.yaml:providers.codex"}
	if p != want {
		t.Errorf("got %+v\nwant %+v", p, want)
	}
}

func TestProjectBlockWinsOverMachineFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "claude.yaml"), []byte("kind: machine\ndoneSignal: sentinel\n"), 0o644)
	blocks := projectBlocks(t, "claude:\n  kind: project\n  doneSignal: sentinel\n")
	r := NewRegistry(blocks, nil, dir)

	p, err := r.Resolve("claude")

	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "project" || p.Source != "project config" {
		t.Errorf("got kind %q source %q", p.Kind, p.Source)
	}
}

func TestMachineFileAddsProviderWithOnlyKindAndDoneSignal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pdev.yaml")
	os.WriteFile(path, []byte("kind: pdev\ndoneSignal: sentinel\n"), 0o644)
	r := NewRegistry(nil, nil, dir)

	p, err := r.Resolve("pdev")

	if err != nil {
		t.Fatal(err)
	}
	want := Provider{Name: "pdev", Kind: "pdev", DoneSignal: "sentinel", Ask: "none", Source: path}
	if p != want {
		t.Errorf("got %+v\nwant %+v", p, want)
	}
	if got := Args(p, "opus", "high", "http://x", "/tmp/m.json", ""); len(got) != 0 {
		t.Errorf("args %q, want none", got)
	}
}

func TestMachineFileReplacesShippedBlockWhole(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "claude.yaml"), []byte("kind: claude\ndoneSignal: sentinel\n"), 0o644)
	r := NewRegistry(nil, nil, dir)

	p, err := r.Resolve("claude")

	if err != nil {
		t.Fatal(err)
	}
	if p.ModelFlag != "" || p.Review != "" || p.Ask != "none" {
		t.Errorf("shipped keys leaked into the machine block: %+v", p)
	}
}

func TestUnknownProvider(t *testing.T) {
	r := NewRegistry(nil, nil, t.TempDir())

	_, err := r.Resolve("nope")

	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("got %v", err)
	}
}

func TestValidationErrorsNameFieldAndSource(t *testing.T) {
	cases := []struct {
		name, block, field string
	}{
		{"kind missing", "doneSignal: sentinel\n", "kind"},
		{"kind empty", "kind: \"\"\ndoneSignal: sentinel\n", "kind"},
		{"modelFlag without placeholder", "kind: x\ndoneSignal: sentinel\nmodelFlag: --model\n", "modelFlag"},
		{"effortFlag without placeholder", "kind: x\ndoneSignal: sentinel\neffortFlag: --effort\n", "effortFlag"},
		{"askFlag without placeholder", "kind: x\ndoneSignal: sentinel\naskFlag: --mcp\n", "askFlag"},
		{"dirFlag without placeholder", "kind: x\ndoneSignal: sentinel\ndirFlag: --add-dir\n", "dirFlag"},
		{"doneSignal missing", "kind: x\n", "doneSignal"},
		{"doneSignal other", "kind: x\ndoneSignal: exit\n", "doneSignal"},
		{"ask other", "kind: x\ndoneSignal: sentinel\nask: stdin\n", "ask"},
		{"unknown key", "kind: x\ndoneSignal: sentinel\nbogus: 1\n", "bogus"},
		{"non-scalar value", "kind:\n  - x\ndoneSignal: sentinel\n", "kind"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "pdev.yaml")
			os.WriteFile(path, []byte(c.block), 0o644)
			r := NewRegistry(nil, nil, dir)

			_, err := r.Resolve("pdev")

			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), c.field) || !strings.Contains(err.Error(), path) {
				t.Errorf("error %q should name field %q and source %q", err, c.field, path)
			}
		})
	}
}

func TestValidationErrorNamesProjectSource(t *testing.T) {
	blocks := projectBlocks(t, "codex:\n  kind: codex\n  doneSignal: sentinel\n  ask: always\n")
	r := NewRegistry(blocks, map[string]string{"providers.codex": ".r-loop/config.yaml:providers.codex"}, t.TempDir())

	_, err := r.Resolve("codex")

	if err == nil || !strings.Contains(err.Error(), "ask") || !strings.Contains(err.Error(), ".r-loop/config.yaml:providers.codex") {
		t.Errorf("got %v", err)
	}
}

func TestAskFlagAcceptsUrlOrMcpConfig(t *testing.T) {
	for _, flag := range []string{"--url {url}", "--cfg {mcpConfig}", ""} {
		blocks := projectBlocks(t, "x:\n  kind: x\n  doneSignal: sentinel\n  askFlag: \""+flag+"\"\n")
		r := NewRegistry(blocks, nil, t.TempDir())

		if _, err := r.Resolve("x"); err != nil {
			t.Errorf("askFlag %q: %v", flag, err)
		}
	}
}

func TestArgsOfShippedBlocks(t *testing.T) {
	r := NewRegistry(nil, nil, t.TempDir())
	claude, _ := r.Resolve("claude")
	codex, _ := r.Resolve("codex")
	cases := []struct {
		name          string
		p             Provider
		model, effort string
		want          []string
	}{
		{"claude with model and effort", claude, "opus", "high",
			[]string{"--model", "opus", "--effort", "high", "--mcp-config", "/run/mcp.json"}},
		{"claude without model and effort", claude, "", "",
			[]string{"--mcp-config", "/run/mcp.json"}},
		{"codex with model and effort", codex, "gpt-5", "high",
			[]string{"-c", "check_for_update_on_startup=false", "-c", "sandbox_workspace_write.network_access=true", "-c", "model=gpt-5", "-c", "model_reasoning_effort=high", "-c", "mcp_servers.r-loop.url=http://127.0.0.1:9/ask"}},
		{"codex without model and effort", codex, "", "",
			[]string{"-c", "check_for_update_on_startup=false", "-c", "sandbox_workspace_write.network_access=true", "-c", "mcp_servers.r-loop.url=http://127.0.0.1:9/ask"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Args(c.p, c.model, c.effort, "http://127.0.0.1:9/ask", "/run/mcp.json", "")

			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestArgsOmitAskFlagWithoutItsValue(t *testing.T) {
	r := NewRegistry(nil, nil, t.TempDir())
	claude, _ := r.Resolve("claude")
	codex, _ := r.Resolve("codex")

	if got := Args(claude, "opus", "", "http://x", "", ""); !reflect.DeepEqual(got, []string{"--model", "opus"}) {
		t.Errorf("claude: %q", got)
	}
	if got := Args(codex, "", "low", "", "/run/mcp.json", ""); !reflect.DeepEqual(got, []string{"-c", "check_for_update_on_startup=false", "-c", "sandbox_workspace_write.network_access=true", "-c", "model_reasoning_effort=low"}) {
		t.Errorf("codex: %q", got)
	}
}

func TestWriteMCPConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")

	if err := WriteMCPConfig(path, "http://127.0.0.1:4711/ask/1/implement"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"mcpServers": map[string]any{"r-loop": map[string]any{"type": "http", "url": "http://127.0.0.1:4711/ask/1/implement"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %s", data)
	}
}

func TestToCore(t *testing.T) {
	r := NewRegistry(nil, nil, t.TempDir())
	claude, _ := r.Resolve("claude")
	pdev := Provider{Name: "pdev", Kind: "pdev", DoneSignal: "sentinel", Ask: "none"}

	got := ToCore(claude, "opus", "", "http://x", "/run/mcp.json", "")
	plain := ToCore(pdev, "m", "e", "http://x", "/run/mcp.json", "")

	want := core.ProviderArgs{Kind: "claude", Args: []string{"--model", "opus", "--mcp-config", "/run/mcp.json"}, Ask: true, Review: "/code-review"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if plain.Ask || plain.Kind != "pdev" || len(plain.Args) != 0 || plain.Review != "" {
		t.Errorf("pdev: %+v", plain)
	}
}

func TestToCoreFillsTheReviewArgsWithFlagsModelAndEffort(t *testing.T) {
	r := NewRegistry(nil, nil, t.TempDir())
	codex, err := r.Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	codex.Review, codex.ReviewStart, codex.ReviewDone = "codex exec review --uncommitted {args} -o {output}", "", ""
	for _, tc := range []struct{ model, effort, want string }{
		{"gpt-x", "high", "codex exec review --uncommitted -c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true -c model=gpt-x -c model_reasoning_effort=high -o {output}"},
		{"", "", "codex exec review --uncommitted -c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true -o {output}"},
	} {
		got := ToCore(codex, tc.model, tc.effort, "http://x", "", "").Review
		if got != tc.want {
			t.Errorf("review = %q, want %q", got, tc.want)
		}
		if strings.Contains(got, "mcp_servers") {
			t.Errorf("ask flag in review: %q", got)
		}
	}
	claude, err := r.Resolve("claude")
	if err != nil {
		t.Fatal(err)
	}
	if got := ToCore(claude, "", "", "http://x", "", "").Review; got != "/code-review" {
		t.Fatalf("claude review = %q", got)
	}
}

func TestToCoreKeepsReviewConfigAsOneShellArgument(t *testing.T) {
	p := Provider{Kind: "codex", Flags: `-c sandbox_permissions=["disk-full-read-access"]`, Review: "codex exec review --uncommitted {args} -o {output}"}
	got := ToCore(p, "", "", "", "", "").Review
	cmd := strings.ReplaceAll(got, "codex exec review --uncommitted ", "set -- ")
	cmd = strings.ReplaceAll(cmd, " -o {output}", `; printf '%s\n' "$@"`)
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil {
		t.Fatal(err)
	}
	want := "-c\nsandbox_permissions=[\"disk-full-read-access\"]\n"
	if string(out) != want {
		t.Fatalf("args = %q, want %q (command %q)", out, want, got)
	}
}

func TestCoreNamesNoProvider(t *testing.T) {
	err := filepath.WalkDir("../core", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := strings.ToLower(string(data))
		for _, name := range []string{"claude", "codex", "opencode"} {
			if strings.Contains(text, name) {
				t.Errorf("%s mentions %q", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArgsDoNotReExpandPlaceholdersInValues(t *testing.T) {
	r := NewRegistry(nil, nil, t.TempDir())
	claude, _ := r.Resolve("claude")

	for i := 0; i < 50; i++ {
		got := Args(claude, "{effort}", "", "", "/tmp/{model}/mcp.json", "")

		want := []string{"--model", "{effort}", "--mcp-config", "/tmp/{model}/mcp.json"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q\nwant %q", got, want)
		}
	}
}

func TestProjectBlockFlagsArePassedFirstOnEveryStart(t *testing.T) {
	r := NewRegistry(projectBlocks(t, "mine:\n  kind: codex\n  flags: \"-c check_for_update_on_startup=false --no-alt-screen\"\n  modelFlag: \"-c model={model}\"\n  doneSignal: sentinel\n"), nil, t.TempDir())

	p, err := r.Resolve("mine")

	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-c", "check_for_update_on_startup=false", "--no-alt-screen", "-c", "model=gpt-5"}
	if got := Args(p, "gpt-5", "", "", "", ""); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if got := Args(p, "", "", "", "", ""); !reflect.DeepEqual(got, want[:3]) {
		t.Errorf("without model: %q", got)
	}
}

func TestFlagsWithAPlaceholderAreRefused(t *testing.T) {
	r := NewRegistry(projectBlocks(t, "mine:\n  kind: codex\n  flags: \"-c model={model}\"\n  doneSignal: sentinel\n"), nil, t.TempDir())

	_, err := r.Resolve("mine")

	if err == nil || !strings.Contains(err.Error(), "flags") {
		t.Fatalf("err %v", err)
	}
}

func TestDirFlagMustContainDir(t *testing.T) {
	blocks := projectBlocks(t, "mine:\n  kind: claude\n  dirFlag: \"--add-dir {model}\"\n  doneSignal: sentinel\n")
	r := NewRegistry(blocks, map[string]string{"providers.mine": ".r-loop/config.yaml:providers.mine"}, t.TempDir())

	_, err := r.Resolve("mine")

	if err == nil || !strings.Contains(err.Error(), "dirFlag") || !strings.Contains(err.Error(), ".r-loop/config.yaml:providers.mine") {
		t.Fatalf("err %v", err)
	}
	for _, flag := range []string{"--add-dir {dir}", ""} {
		blocks := projectBlocks(t, "x:\n  kind: x\n  doneSignal: sentinel\n  dirFlag: \""+flag+"\"\n")
		if _, err := NewRegistry(blocks, nil, t.TempDir()).Resolve("x"); err != nil {
			t.Errorf("dirFlag %q: %v", flag, err)
		}
	}
}

func TestFlagsMustNotContainDir(t *testing.T) {
	r := NewRegistry(projectBlocks(t, "mine:\n  kind: claude\n  flags: \"--add-dir {dir}\"\n  doneSignal: sentinel\n"), nil, t.TempDir())

	_, err := r.Resolve("mine")

	if err == nil || !strings.Contains(err.Error(), "flags") {
		t.Fatalf("err %v", err)
	}
}

func TestArgsAppendDirFlagOnlyWithADir(t *testing.T) {
	claude, err := NewRegistry(nil, nil, t.TempDir()).Resolve("claude")
	if err != nil {
		t.Fatal(err)
	}
	dir := "/x/repo with space/.r-loop/runs/r1/phase-3"

	withDir := Args(claude, "opus", "high", "", "/run/mcp.json", dir)
	withoutDir := Args(claude, "opus", "high", "", "/run/mcp.json", "")

	if want := []string{"--model", "opus", "--effort", "high", "--mcp-config", "/run/mcp.json", "--add-dir", dir}; !reflect.DeepEqual(withDir, want) {
		t.Errorf("with a dir %q\nwant %q", withDir, want)
	}
	if want := []string{"--model", "opus", "--effort", "high", "--mcp-config", "/run/mcp.json"}; !reflect.DeepEqual(withoutDir, want) {
		t.Errorf("without a dir %q\nwant %q", withoutDir, want)
	}
}

func TestShippedClaudeDirFlagAddsTheDir(t *testing.T) {
	claude, err := NewRegistry(nil, nil, t.TempDir()).Resolve("claude")
	if err != nil {
		t.Fatal(err)
	}

	got := Args(claude, "", "", "", "", "/run/r1/phase-3")

	if want := []string{"--add-dir", "/run/r1/phase-3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestShippedCodexDirFlagNamesTheWritableRoot(t *testing.T) {
	codex, err := NewRegistry(nil, nil, t.TempDir()).Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}

	got := Args(codex, "gpt-5", "", "http://127.0.0.1:9/ask", "", "/run/r1/phase-3")

	want := []string{"-c", "check_for_update_on_startup=false", "-c", "sandbox_workspace_write.network_access=true", "-c", "model=gpt-5",
		"-c", "mcp_servers.r-loop.url=http://127.0.0.1:9/ask", "-c", `sandbox_workspace_write.writable_roots=["/run/r1/phase-3"]`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestToCoreReviewArgsCarryNoDir(t *testing.T) {
	r := NewRegistry(nil, nil, t.TempDir())
	codex, err := r.Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}
	codex.Review, codex.ReviewStart, codex.ReviewDone = "codex exec review --uncommitted {args} -o {output}", "", ""

	got := ToCore(codex, "", "", "http://x", "", "/run/r1/phase-3")

	if last := got.Args[len(got.Args)-1]; last != `sandbox_workspace_write.writable_roots=["/run/r1/phase-3"]` {
		t.Errorf("step args %q lack the dir", got.Args)
	}
	if want := "codex exec review --uncommitted -c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true -o {output}"; got.Review != want {
		t.Errorf("review = %q, want %q", got.Review, want)
	}
}

func TestShippedCodexRunsReviewInItsPane(t *testing.T) {
	codex, err := NewRegistry(nil, nil, t.TempDir()).Resolve("codex")
	if err != nil {
		t.Fatal(err)
	}

	got := ToCore(codex, "gpt-x", "high", "http://x", "", "")

	if got.Review != "/review Review the current code changes (staged, unstaged, and untracked files) and provide prioritized findings." ||
		got.ReviewStart != ">> Code review started" || got.ReviewDone != "<< Code review finished" {
		t.Errorf("got review %q, start %q, done %q", got.Review, got.ReviewStart, got.ReviewDone)
	}
}

func TestReviewMarkersDecodeFromAProjectBlock(t *testing.T) {
	blocks := projectBlocks(t, "pdev:\n  kind: pdev\n  doneSignal: sentinel\n  review: /review all\n  reviewStart: \">> go\"\n  reviewDone: \"<< done\"\n")
	r := NewRegistry(blocks, nil, t.TempDir())

	p, err := r.Resolve("pdev")

	if err != nil {
		t.Fatal(err)
	}
	if p.ReviewStart != ">> go" || p.ReviewDone != "<< done" {
		t.Errorf("got %+v", p)
	}
	if got := ToCore(p, "", "", "", "", ""); got.Review != "/review all" || got.ReviewStart != ">> go" || got.ReviewDone != "<< done" {
		t.Errorf("core args %+v", got)
	}
}

func TestReviewMarkersComeInPairsAfterASlashReview(t *testing.T) {
	cases := []struct {
		name, block, field string
	}{
		{"start without done", "kind: x\ndoneSignal: sentinel\nreview: /review\nreviewStart: go\n", "reviewDone"},
		{"done without start", "kind: x\ndoneSignal: sentinel\nreview: /review\nreviewDone: done\n", "reviewStart"},
		{"shell review", "kind: x\ndoneSignal: sentinel\nreview: x review\nreviewStart: go\nreviewDone: done\n", "reviewStart"},
		{"no review", "kind: x\ndoneSignal: sentinel\nreviewStart: go\nreviewDone: done\n", "reviewStart"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "pdev.yaml")
			os.WriteFile(path, []byte(c.block), 0o644)

			_, err := NewRegistry(nil, nil, dir).Resolve("pdev")

			if err == nil || !strings.Contains(err.Error(), c.field) || !strings.Contains(err.Error(), path) {
				t.Errorf("error %v should name field %q and source %q", err, c.field, path)
			}
		})
	}
}
