package sessionhost

import (
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"r-loop/internal/core"
	"r-loop/internal/hc"
	"r-loop/internal/herdr"
)

const (
	Herdr = "herdr"
	App   = "app"
	Auto  = "auto"
)

type Env struct {
	HerdrPane, HCTerminal string
	Herdr, HC, App        string
	LookPath              func(string) (string, error)
}

type Conn struct {
	core.SessionHost
	Self      string
	Name      string
	Source    string
	Preflight func(kinds []string) error
}

type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

func exit(code int, format string, args ...any) *ExitError {
	return &ExitError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

type candidate struct {
	name string
	host core.SessionHost
	bins []string
}

func (env Env) candidates() map[string]candidate {
	return map[string]candidate{
		Herdr: {Herdr, herdr.Client{Bin: env.Herdr}, []string{env.Herdr}},
		App:   {App, hc.Client{HC: env.HC, App: env.App}, []string{env.HC, env.App}},
	}
}

func (env Env) missing(c candidate) error {
	for _, bin := range c.bins {
		if bin == "" {
			return fmt.Errorf("%s binary not set", label(c.name))
		}
		if _, err := env.LookPath(bin); err != nil {
			return fmt.Errorf("%s binary %s not found", label(c.name), bin)
		}
	}
	return nil
}

func label(name string) string {
	if name == App {
		return "herdr-app"
	}
	return name
}

func other(name string) string {
	if name == Herdr {
		return App
	}
	return Herdr
}

func Connect(env Env, choice string) (Conn, error) {
	hosts := env.candidates()
	var chosen candidate
	source := ""
	switch choice {
	case Herdr, App:
		chosen = hosts[choice]
		if err := env.missing(chosen); err != nil {
			return Conn{}, exit(127, "%v", err)
		}
		if err := chosen.host.Reachable(); err != nil {
			return Conn{}, exit(4, "%s unreachable: %v", label(choice), err)
		}
	case Auto, "":
		var err error
		if chosen, source, err = env.detect(hosts); err != nil {
			return Conn{}, err
		}
	default:
		return Conn{}, exit(2, "host: want auto, herdr or app, got %q", choice)
	}
	self, err := env.self(chosen)
	if err != nil {
		return Conn{}, err
	}
	alt := hosts[other(chosen.name)]
	r := &router{primary: chosen.host, probe: func() (core.SessionHost, bool) {
		if env.missing(alt) != nil || alt.host.Reachable() != nil {
			return nil, false
		}
		return alt.host, true
	}}
	return Conn{SessionHost: r, Self: self, Name: chosen.name, Source: source, Preflight: env.preflight(chosen.name)}, nil
}

func (env Env) detect(hosts map[string]candidate) (candidate, string, error) {
	pick := func(name, source string) (candidate, string, error) {
		c := hosts[name]
		if err := env.missing(c); err != nil {
			return candidate{}, "", exit(127, "%v", err)
		}
		return c, source, nil
	}
	if env.HerdrPane != "" {
		return pick(Herdr, "auto: HERDR_PANE_ID")
	}
	if env.HCTerminal != "" {
		return pick(App, "auto: HC_TERMINAL_ID")
	}
	var reachable []candidate
	var errs, absent []string
	for _, name := range []string{Herdr, App} {
		c := hosts[name]
		if err := env.missing(c); err != nil {
			absent = append(absent, err.Error())
			errs = append(errs, fmt.Sprintf("%s: %v", label(name), err))
			continue
		}
		if err := c.host.Reachable(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", label(name), err))
			continue
		}
		reachable = append(reachable, c)
	}
	switch {
	case len(reachable) == 2:
		return candidate{}, "", exit(2, "both herdr and herdr-app are running; set host: herdr or host: app")
	case len(reachable) == 1:
		return reachable[0], "auto: probe", nil
	case len(absent) == 2:
		return candidate{}, "", exit(127, "no session host on PATH (%s)", strings.Join(absent, "; "))
	}
	return candidate{}, "", exit(4, "no session host reachable (%s)", strings.Join(errs, "; "))
}

func (env Env) self(c candidate) (string, error) {
	if c.name == Herdr {
		return env.HerdrPane, nil
	}
	if env.HCTerminal == "" {
		return "", nil
	}
	pane, err := c.host.(hc.Client).TerminalPane(env.HCTerminal)
	if err != nil {
		return "", exit(4, "herdr-app unreachable: %v", err)
	}
	return pane, nil
}

var codexVersion = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

var minCodex = [3]int{0, 166, 0}

func (env Env) preflight(name string) func([]string) error {
	return func(kinds []string) error {
		if name != App || !slices.Contains(kinds, "codex") {
			return nil
		}
		bin, err := env.LookPath("codex")
		if err != nil {
			return exit(127, "codex binary not found on PATH")
		}
		out, err := exec.Command(bin, "--version").Output()
		if err != nil {
			return exit(2, "codex --version: %v", err)
		}
		m := codexVersion.FindStringSubmatch(string(out))
		if m == nil {
			return exit(2, "codex --version: no version in %q", strings.TrimSpace(string(out)))
		}
		var got [3]int
		for i := range got {
			got[i], _ = strconv.Atoi(m[i+1])
		}
		if slices.Compare(got[:], minCodex[:]) < 0 {
			return exit(2, "codex %s reports no status to hc; 0.166 or later is needed — run codex update", m[0])
		}
		return nil
	}
}
