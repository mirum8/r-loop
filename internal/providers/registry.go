package providers

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"r-loop/internal/core"
)

//go:embed shipped/*.yaml
var shipped embed.FS

const shippedSource = "shipped"

type Provider struct {
	Name, Kind, ModelFlag, EffortFlag, AskFlag, DoneSignal, Ask, Review, Source string
}

type Registry struct {
	project    map[string]yaml.Node
	provenance map[string]string
	machineDir string
}

func NewRegistry(project map[string]yaml.Node, provenance map[string]string, machineDir string) *Registry {
	return &Registry{project: project, provenance: provenance, machineDir: machineDir}
}

func (r *Registry) Resolve(name string) (Provider, error) {
	if n, ok := r.project[name]; ok {
		source := r.provenance["providers."+name]
		if source == "" {
			source = "project config"
		}
		return decode(name, &n, source)
	}
	path := filepath.Join(r.machineDir, name+".yaml")
	data, err := os.ReadFile(path)
	if err == nil {
		return parse(name, data, path)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return Provider{}, fmt.Errorf("provider %s: %w", name, err)
	}
	if data, err := shipped.ReadFile("shipped/" + name + ".yaml"); err == nil {
		return parse(name, data, shippedSource)
	}
	return Provider{}, fmt.Errorf("provider %q: no block in the project config, %s or the shipped providers", name, path)
}

func parse(name string, data []byte, source string) (Provider, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Provider{}, fmt.Errorf("%s: provider %s: %w", source, name, err)
	}
	if len(doc.Content) == 0 {
		return Provider{}, fmt.Errorf("%s: provider %s: kind is required", source, name)
	}
	return decode(name, doc.Content[0], source)
}

func decode(name string, n *yaml.Node, source string) (Provider, error) {
	fail := func(field, format string, args ...any) (Provider, error) {
		return Provider{}, fmt.Errorf("%s: provider %s: %s %s", source, name, field, fmt.Sprintf(format, args...))
	}
	if n.Kind != yaml.MappingNode {
		return fail("block", "must be a mapping")
	}
	p := Provider{Name: name, Source: source}
	fields := map[string]*string{
		"kind": &p.Kind, "modelFlag": &p.ModelFlag, "effortFlag": &p.EffortFlag, "askFlag": &p.AskFlag,
		"doneSignal": &p.DoneSignal, "ask": &p.Ask, "review": &p.Review,
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, val := n.Content[i].Value, n.Content[i+1]
		field, ok := fields[key]
		if !ok {
			return fail(key, "is not a provider key")
		}
		if val.Kind != yaml.ScalarNode {
			return fail(key, "must be a string")
		}
		*field = val.Value
	}
	switch {
	case p.Kind == "":
		return fail("kind", "is required")
	case p.ModelFlag != "" && !strings.Contains(p.ModelFlag, "{model}"):
		return fail("modelFlag", "must contain {model} or be empty")
	case p.EffortFlag != "" && !strings.Contains(p.EffortFlag, "{effort}"):
		return fail("effortFlag", "must contain {effort} or be empty")
	case p.AskFlag != "" && !strings.Contains(p.AskFlag, "{url}") && !strings.Contains(p.AskFlag, "{mcpConfig}"):
		return fail("askFlag", "must contain {url} or {mcpConfig} or be empty")
	case p.DoneSignal != "sentinel":
		return fail("doneSignal", "must be sentinel, got %q", p.DoneSignal)
	case p.Ask != "" && p.Ask != "mcp" && p.Ask != "none":
		return fail("ask", "must be mcp or none, got %q", p.Ask)
	}
	if p.Ask == "" {
		p.Ask = "none"
	}
	return p, nil
}

func Args(p Provider, model, effort, askURL, mcpConfigPath string) []string {
	values := map[string]string{"{model}": model, "{effort}": effort, "{url}": askURL, "{mcpConfig}": mcpConfigPath}
	var args []string
	for _, tmpl := range []string{p.ModelFlag, p.EffortFlag, p.AskFlag} {
		if flag, ok := expand(tmpl, values); ok {
			args = append(args, strings.Fields(flag)...)
		}
	}
	return args
}

func expand(tmpl string, values map[string]string) (string, bool) {
	if tmpl == "" {
		return "", false
	}
	var pairs []string
	for placeholder, value := range values {
		if strings.Contains(tmpl, placeholder) && value == "" {
			return "", false
		}
		pairs = append(pairs, placeholder, value)
	}
	return strings.NewReplacer(pairs...).Replace(tmpl), true
}

func WriteMCPConfig(path, url string) error {
	data, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{"r-loop": map[string]any{"type": "http", "url": url}},
	})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func ToCore(p Provider, model, effort, askURL, mcpConfigPath string) core.ProviderArgs {
	return core.ProviderArgs{Kind: p.Kind, Args: Args(p, model, effort, askURL, mcpConfigPath), Ask: p.Ask == "mcp", Review: p.Review}
}
