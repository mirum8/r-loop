package providers

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

type pluginRun struct {
	calls  [][]string
	lists  []string
	addErr error
}

func (r *pluginRun) run(name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	if args[1] != "list" {
		return nil, r.addErr
	}
	out := r.lists[0]
	if len(r.lists) > 1 {
		r.lists = r.lists[1:]
	}
	return []byte(out), nil
}

var codexSecurity = Provider{Name: "codex", Kind: "codex", SecurityReview: "$codex-security:security-diff-scan", SecurityPlugin: "codex-security@openai-curated"}

const (
	codexWithoutSecurity = `{"installed":[{"pluginId":"r@personal","enabled":true}]}`
	codexWithSecurity    = `{"installed":[{"pluginId":"r@personal","enabled":true},{"pluginId":"codex-security@openai-curated","enabled":true}]}`
)

func TestEnsurePluginLeavesAnEnabledPluginAlone(t *testing.T) {
	r := &pluginRun{lists: []string{codexWithSecurity}}

	installed, err := EnsurePlugin(codexSecurity, r.run)

	if err != nil || installed {
		t.Fatalf("installed=%v err=%v", installed, err)
	}
	if want := [][]string{{"codex", "plugin", "list", "--json"}}; !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q, want %q", r.calls, want)
	}
}

func TestEnsurePluginAddsAMissingCodexPlugin(t *testing.T) {
	r := &pluginRun{lists: []string{codexWithoutSecurity, codexWithSecurity}}

	installed, err := EnsurePlugin(codexSecurity, r.run)

	if err != nil || !installed {
		t.Fatalf("installed=%v err=%v", installed, err)
	}
	want := [][]string{{"codex", "plugin", "list", "--json"}, {"codex", "plugin", "add", "codex-security@openai-curated"}, {"codex", "plugin", "list", "--json"}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Errorf("calls = %q, want %q", r.calls, want)
	}
}

func TestEnsurePluginEnablesAnInstalledButDisabledClaudePlugin(t *testing.T) {
	p := Provider{Name: "claude", Kind: "claude", SecurityReview: "/scan", SecurityPlugin: "sec@official"}
	r := &pluginRun{lists: []string{`[{"id":"sec@official","enabled":false}]`, `[{"id":"sec@official","enabled":true}]`}}

	installed, err := EnsurePlugin(p, r.run)

	if err != nil || !installed {
		t.Fatalf("installed=%v err=%v", installed, err)
	}
	if want := []string{"claude", "plugin", "enable", "sec@official"}; !reflect.DeepEqual(r.calls[1], want) {
		t.Errorf("enable call = %q, want %q", r.calls[1], want)
	}
}

func TestEnsurePluginFailsWhenTheAddFails(t *testing.T) {
	r := &pluginRun{lists: []string{codexWithoutSecurity}, addErr: errors.New("exit status 1")}

	_, err := EnsurePlugin(codexSecurity, r.run)

	if err == nil || !strings.Contains(err.Error(), "codex plugin add codex-security@openai-curated: exit status 1") {
		t.Errorf("err = %v", err)
	}
}

func TestEnsurePluginFailsWhenThePluginIsStillMissingAfterTheAdd(t *testing.T) {
	r := &pluginRun{lists: []string{codexWithoutSecurity}}

	_, err := EnsurePlugin(codexSecurity, r.run)

	if err == nil || !strings.Contains(err.Error(), "codex-security@openai-curated is still not installed and enabled") {
		t.Errorf("err = %v", err)
	}
}

func TestEnsurePluginRefusesAKindWithoutAnInstaller(t *testing.T) {
	p := Provider{Name: "pdev", Kind: "pdev", SecurityReview: "/scan", SecurityPlugin: "sec@m"}
	r := &pluginRun{}

	_, err := EnsurePlugin(p, r.run)

	if err == nil || !strings.Contains(err.Error(), "kind pdev has no plugin installer") || len(r.calls) != 0 {
		t.Errorf("err = %v calls = %q", err, r.calls)
	}
}

func TestEnsurePluginInstallsAMissingClaudePlugin(t *testing.T) {
	p := Provider{Name: "claude", Kind: "claude", SecurityReview: "/scan", SecurityPlugin: "sec@official"}
	r := &pluginRun{lists: []string{`[]`, `[{"id":"sec@official","enabled":true}]`}}

	installed, err := EnsurePlugin(p, r.run)

	if err != nil || !installed {
		t.Fatalf("installed=%v err=%v", installed, err)
	}
	if want := []string{"claude", "plugin", "install", "sec@official"}; !reflect.DeepEqual(r.calls[1], want) {
		t.Errorf("install call = %q, want %q", r.calls[1], want)
	}
}
