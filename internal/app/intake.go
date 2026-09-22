package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"r-loop/internal/askmcp"
	"r-loop/internal/config"
	"r-loop/internal/core"
	"r-loop/internal/gitrepo"
	"r-loop/internal/herdr"
	"r-loop/internal/plan"
	"r-loop/internal/prompts"
	"r-loop/internal/providers"
)

type intake struct {
	env      Env
	root     string
	text     string
	given    Options
	cfg      config.Intake
	provider providers.Provider
	host     core.SessionHost
	prompts  core.Prompts
	poll     time.Duration
	accepted chan []string

	mu   sync.Mutex
	done bool
}

func Intake(args []string, given Options, env Env) (Options, error) {
	in, err := newIntake(args, given, env)
	if err != nil {
		return Options{}, err
	}
	if err := in.host.Reachable(); err != nil {
		return Options{}, exit(4, "herdr server unreachable: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return in.run(ctx)
}

func newIntake(args []string, given Options, env Env) (*intake, error) {
	if _, err := exec.LookPath(env.Git); err != nil {
		return nil, exit(127, "git binary %s not found", env.Git)
	}
	repo, err := gitrepo.Open(env.Dir)
	if err != nil {
		return nil, exit(2, "%v", err)
	}
	root := repo.Root()
	cfg, err := config.Load(root, env.Home, given.Overrides)
	if err != nil {
		return nil, exit(2, "%v", err)
	}
	reg := providers.NewRegistry(cfg.Providers, cfg.Provenance, filepath.Join(env.Home, ".config", "r-loop", "providers"))
	if err := checkRole(reg, role{field: "intake.provider", provider: cfg.Intake.Provider}); err != nil {
		return nil, err
	}
	p, _ := reg.Resolve(cfg.Intake.Provider)
	if _, err := exec.LookPath(env.Herdr); err != nil {
		return nil, exit(127, "herdr binary %s not found", env.Herdr)
	}
	return &intake{
		env: env, root: root, text: strings.Join(args, " "), given: given, cfg: cfg.Intake, provider: p,
		host: herdr.Client{Bin: env.Herdr}, prompts: prompts.New(root), poll: time.Second, accepted: make(chan []string, 1),
	}, nil
}

func (in *intake) run(ctx context.Context) (Options, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dir, err := os.MkdirTemp("", "r-loop-intake-")
	if err != nil {
		return Options{}, exit(2, "%v", err)
	}
	defer os.RemoveAll(dir)
	url, err := (&askmcp.Intake{Submit: in.submit}).Serve(ctx)
	if err != nil {
		return Options{}, exit(2, "intake server: %v", err)
	}
	mcpPath := filepath.Join(dir, "intake.mcp.json")
	args := providers.ToCore(in.provider, in.cfg.Model, in.cfg.Effort, url, mcpPath)
	if slices.ContainsFunc(args.Args, func(a string) bool { return strings.Contains(a, mcpPath) }) {
		if err := providers.WriteMCPConfig(mcpPath, url); err != nil {
			return Options{}, exit(2, "%v", err)
		}
	}
	session := &core.Intake{
		Host: in.host, Prompts: in.prompts, Provider: args, Poll: in.poll,
		Name: "rloop-intake-" + strconv.Itoa(in.env.PID), Root: in.root, Pane: in.env.Pane,
		Vars: map[string]any{"Text": in.text, "Dir": in.env.Dir, "Root": in.root, "Usage": usage(), "Unattended": in.given.Unattended},
	}
	argv, err := session.Run(ctx, in.accepted)
	switch {
	case errors.Is(err, context.Canceled):
		return Options{}, exit(2, "intake cancelled")
	case errors.Is(err, core.ErrIntakeGone):
		return Options{}, exit(4, "%v", err)
	case err != nil:
		return Options{}, exit(4, "intake did not start: %v", err)
	}
	fmt.Fprintf(in.env.Stderr, "r-loop: resolved: r-loop %s\n", shellJoin(argv))
	return ParseArgs(argv)
}

func (in *intake) submit(argv []string) (bool, string) {
	opts, err := ParseArgs(argv)
	if err != nil {
		return false, err.Error()
	}
	if freeForm([]string{opts.Todo}) {
		return false, fmt.Sprintf("%s: the plan path must end in .md", opts.Todo)
	}
	todo := opts.Todo
	if !filepath.IsAbs(todo) {
		todo = filepath.Join(in.env.Dir, todo)
	}
	pl, err := plan.Reader{}.Read(todo)
	if err != nil {
		return false, err.Error()
	}
	if _, err := core.RunList(pl, todo, core.RunOptions{From: opts.From, Phases: opts.Phases}); err != nil {
		return false, err.Error()
	}
	if _, err := config.Load(in.root, in.env.Home, opts.Overrides); err != nil {
		return false, err.Error()
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.done {
		return false, "a command line was already accepted"
	}
	in.done = true
	in.accepted <- slices.Clone(argv)
	return true, ""
}

func shellJoin(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.IndexFunc(a, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=,:@+", r))
		}) < 0 {
			out[i] = a
			continue
		}
		out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(out, " ")
}
