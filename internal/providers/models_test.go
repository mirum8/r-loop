package providers

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const catalog = `{"models":[
{"slug":"gpt-6-astra","visibility":"list"},
{"slug":"gpt-6-sol","visibility":"list"},
{"slug":"gpt-6.1-sol","visibility":"list"},
{"slug":"gpt-7-sol","visibility":"hide"},
{"slug":"gpt-5.6-sol","visibility":"list"},
{"slug":"gpt-6.10-luna","visibility":"list"},
{"slug":"gpt-6.9-luna","visibility":"list"},
{"slug":"gpt-5.5","visibility":"list"}]}`

func codexWith(out string, err error) (Provider, *[][]string, func(string, ...string) ([]byte, error)) {
	var calls [][]string
	run := func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return []byte(out), err
	}
	return Provider{Name: "codex", Kind: "codex", Models: "debug models"}, &calls, run
}

func TestResolveModelPicksTheNewestListedVersionOfAnAlias(t *testing.T) {
	for alias, want := range map[string]string{"sol": "gpt-6.1-sol", "luna": "gpt-6.10-luna", "astra": "gpt-6-astra"} {
		p, calls, run := codexWith(catalog, nil)

		got, err := ResolveModel(p, alias, run)

		if err != nil {
			t.Fatalf("%s: %v", alias, err)
		}
		if got != want {
			t.Errorf("%s resolved to %q, want %q", alias, got, want)
		}
		if want := [][]string{{"codex", "debug", "models"}}; !reflect.DeepEqual(*calls, want) {
			t.Errorf("%s: calls %q, want %q", alias, *calls, want)
		}
	}
}

func TestResolveModelPassesAPinnedModelThroughWithoutTheCatalog(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-5.5", "gpt-x", "o3", ""} {
		p, calls, run := codexWith("", errors.New("must not run"))

		got, err := ResolveModel(p, model, run)

		if err != nil || got != model {
			t.Errorf("%q resolved to %q, %v", model, got, err)
		}
		if len(*calls) != 0 {
			t.Errorf("%q ran the catalog: %q", model, *calls)
		}
	}
}

func TestResolveModelPassesAnAliasThroughForAProviderWithoutACatalog(t *testing.T) {
	_, calls, run := codexWith("", errors.New("must not run"))

	got, err := ResolveModel(Provider{Name: "claude", Kind: "claude"}, "opus", run)

	if err != nil || got != "opus" {
		t.Errorf("opus resolved to %q, %v", got, err)
	}
	if len(*calls) != 0 {
		t.Errorf("ran the catalog: %q", *calls)
	}
}

func TestResolveModelFailsWithoutAMatch(t *testing.T) {
	cases := map[string]struct {
		alias, out string
		err        error
		want       string
	}{
		"no listed match": {"terra", catalog, nil, `codex has no model matching "terra"`},
		"catalog fails":   {"sol", "", errors.New("exit status 1"), "exit status 1"},
		"bad json":        {"sol", "not json", nil, "codex debug models"},
	}
	for name, c := range cases {
		p, _, run := codexWith(c.out, c.err)

		got, err := ResolveModel(p, c.alias, run)

		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %q, %v; want error containing %q", name, got, err, c.want)
		}
	}
}
