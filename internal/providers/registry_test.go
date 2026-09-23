package providers

import (
	"encoding/json"
	"io/fs"
	"os"
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

func TestShippedClaudeBlock(t *testing.T) {
	r := NewRegistry(nil, nil, t.TempDir())

	p, err := r.Resolve("claude")

	if err != nil {
		t.Fatal(err)
	}
	want := Provider{Name: "claude", Kind: "claude", ModelFlag: "--model {model}", EffortFlag: "--effort {effort}",
		AskFlag: "--mcp-config {mcpConfig}", DoneSignal: "sentinel", Ask: "mcp", Review: "/code-review", Source: "shipped"}
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
	want := Provider{Name: "codex", Kind: "codex", ModelFlag: "-c model={model}", EffortFlag: "-c model_reasoning_effort={effort}",
		AskFlag: "-c mcp_servers.r-loop.url={url}", DoneSignal: "sentinel", Ask: "mcp", Review: "/review", Source: "shipped"}
	if p != want {
		t.Errorf("got %+v\nwant %+v", p, want)
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
	if got := Args(p, "opus", "high", "http://x", "/tmp/m.json"); len(got) != 0 {
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
			[]string{"-c", "model=gpt-5", "-c", "model_reasoning_effort=high", "-c", "mcp_servers.r-loop.url=http://127.0.0.1:9/ask"}},
		{"codex without model and effort", codex, "", "",
			[]string{"-c", "mcp_servers.r-loop.url=http://127.0.0.1:9/ask"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Args(c.p, c.model, c.effort, "http://127.0.0.1:9/ask", "/run/mcp.json")

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

	if got := Args(claude, "opus", "", "http://x", ""); !reflect.DeepEqual(got, []string{"--model", "opus"}) {
		t.Errorf("claude: %q", got)
	}
	if got := Args(codex, "", "low", "", "/run/mcp.json"); !reflect.DeepEqual(got, []string{"-c", "model_reasoning_effort=low"}) {
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

	got := ToCore(claude, "opus", "", "http://x", "/run/mcp.json")
	plain := ToCore(pdev, "m", "e", "http://x", "/run/mcp.json")

	want := core.ProviderArgs{Kind: "claude", Args: []string{"--model", "opus", "--mcp-config", "/run/mcp.json"}, Ask: true, Review: "/code-review"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if plain.Ask || plain.Kind != "pdev" || len(plain.Args) != 0 || plain.Review != "" {
		t.Errorf("pdev: %+v", plain)
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
		got := Args(claude, "{effort}", "", "", "/tmp/{model}/mcp.json")

		want := []string{"--model", "{effort}", "--mcp-config", "/tmp/{model}/mcp.json"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %q\nwant %q", got, want)
		}
	}
}
