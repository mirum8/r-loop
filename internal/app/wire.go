package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"r-loop/internal/config"
	"r-loop/internal/core"
	"r-loop/internal/face/plain"
	"r-loop/internal/gitrepo"
	"r-loop/internal/herdr"
	"r-loop/internal/notify"
	"r-loop/internal/plan"
	"r-loop/internal/prompts"
	"r-loop/internal/providers"
	"r-loop/internal/store"
)

type Options struct {
	Todo                   string
	From                   int
	Phases                 []int
	Overrides              []config.Override
	NoWatchdog, Unattended bool
	Plain, DryRun          bool
}

type Env struct {
	Dir, Home, Herdr, Git string
	PID                   int
	Stdout, Stderr        io.Writer
	Now                   func() time.Time
}

type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string { return e.Msg }

func exit(code int, format string, args ...any) *ExitError {
	return &ExitError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

type Wiring struct {
	Opts     Options
	Env      Env
	Todo     string
	Plan     core.Plan
	Config   config.LoopConfig
	Registry *providers.Registry
	Store    *store.Store
	Prompts  *prompts.Renderer
	Host     herdr.Client
	Repo     *gitrepo.Repo
	Face     *plain.Face
	Notify   *notify.Shell
	Gate     *core.LandGate
	Loop     *core.RunLoop
}

type overrides struct {
	key string
	dst *[]config.Override
}

func (o overrides) String() string { return "" }

func (o overrides) Set(arg string) error {
	ov, err := config.ParseOverride(o.key, arg)
	if err != nil {
		return err
	}
	*o.dst = append(*o.dst, ov)
	return nil
}

type phaseList struct{ dst *[]int }

func (p phaseList) String() string { return "" }

func (p phaseList) Set(arg string) error {
	for _, s := range strings.Split(arg, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return fmt.Errorf("--phases %q: want n,n", arg)
		}
		*p.dst = append(*p.dst, n)
	}
	return nil
}

func ParseArgs(args []string) (Options, error) {
	var o Options
	fs := flag.NewFlagSet("r-loop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.IntVar(&o.From, "from", 0, "")
	fs.Var(phaseList{&o.Phases}, "phases", "")
	for _, key := range []string{"provider", "model", "effort"} {
		fs.Var(overrides{key, &o.Overrides}, key, "")
	}
	fs.BoolVar(&o.NoWatchdog, "no-watchdog", false, "")
	fs.BoolVar(&o.Unattended, "unattended", false, "")
	fs.BoolVar(&o.Plain, "plain", false, "")
	fs.BoolVar(&o.DryRun, "dry-run", false, "")
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return o, err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	if len(positional) != 1 {
		return o, errors.New("want exactly one todo path")
	}
	o.Todo = positional[0]
	return o, nil
}

func Main(args []string, env Env) int {
	if len(args) > 0 {
		switch args[0] {
		case "status":
			return Status(args[1:], env)
		case "resume":
			return Resume(args[1:], env)
		case "abort":
			return Abort(args[1:], env)
		}
	}
	opts, err := ParseArgs(args)
	if err != nil {
		fmt.Fprintf(env.Stderr, "usage: r-loop <todo.md> [flags]: %v\n", err)
		return 2
	}
	w, err := Wire(opts, env)
	if err == nil {
		err = Preflight(w)
	}
	if err != nil {
		return fail(env, err)
	}
	if opts.DryRun {
		return 0
	}
	return w.Execute(core.RunOptions{From: opts.From, Phases: opts.Phases})
}

func fail(env Env, err error) int {
	code := 2
	var e *ExitError
	if errors.As(err, &e) {
		code = e.Code
	}
	fmt.Fprintf(env.Stderr, "r-loop: %s\n", strings.ReplaceAll(err.Error(), "\n", " "))
	return code
}

func (w *Wiring) Execute(opts core.RunOptions) int {
	code := w.Loop.Run(context.Background(), opts)
	w.Face.Close()
	if err := w.Store.ClearCurrent(); err != nil {
		fmt.Fprintf(w.Env.Stderr, "r-loop: clear current: %v\n", err)
	}
	return code
}

func Wire(opts Options, env Env) (*Wiring, error) {
	if _, err := exec.LookPath(env.Git); err != nil {
		return nil, exit(127, "git binary %s not found", env.Git)
	}
	repo, err := gitrepo.Open(env.Dir)
	if err != nil {
		return nil, exit(2, "%v", err)
	}
	root := repo.Root()
	todo := opts.Todo
	if !filepath.IsAbs(todo) {
		todo = filepath.Join(env.Dir, todo)
	}
	pl, err := plan.Reader{}.Read(todo)
	if err != nil {
		return nil, exit(2, "%v", err)
	}
	cfg, err := config.Load(root, env.Home, opts.Overrides)
	if err != nil {
		return nil, exit(2, "%v", err)
	}
	w := &Wiring{
		Opts:     opts,
		Env:      env,
		Todo:     todo,
		Plan:     pl,
		Config:   cfg,
		Registry: providers.NewRegistry(cfg.Providers, cfg.Provenance, filepath.Join(env.Home, ".config", "r-loop", "providers")),
		Store:    store.New(root),
		Prompts:  prompts.New(root),
		Host:     herdr.Client{Bin: env.Herdr},
		Repo:     repo,
		Face:     &plain.Face{Out: env.Stdout},
	}
	w.Notify = &notify.Shell{Emit: w.Face.Emit}
	rows := map[string]core.StepRow{}
	promptNames := map[string]string{}
	checks := map[string]string{}
	for name, row := range cfg.Steps {
		rows[name], promptNames[name], checks[name] = coreRow(row), row.Prompt, row.Check
	}
	kinds, err := core.Pipeline(cfg.Pipeline, rows, promptNames, checks)
	if err != nil {
		return nil, exit(2, "%v", err)
	}
	sm := &core.SessionManager{
		Host:       w.Host,
		Repo:       repo,
		Prompts:    w.Prompts,
		Store:      w.Store,
		Resolve:    w.resolve,
		Now:        time.Now,
		StallGrace: cfg.Watchdog.StallGrace,
	}
	runners := core.DefaultRunners(sm, kinds)
	impl := rows["implement"]
	fix := cfg.Land.Fix
	fixKind := core.StepKind{Name: "gatefix", Prompt: "gatefix", Check: "diff", Row: core.StepRow{
		Provider: fix.Provider, Model: fix.Model, Effort: fix.Effort,
		Timeout: impl.Timeout, Reviewers: impl.Reviewers, Rounds: 1, ReviewTimeout: impl.ReviewTimeout,
	}}
	ms := cfg.Steps["milestone"]
	w.Gate = &core.LandGate{
		Repo:        repo,
		Plan:        plan.Reader{},
		Store:       w.Store,
		Face:        w.Face,
		TodoPath:    todo,
		GateTimeout: cfg.Land.GateTimeout,
		Boundary: &core.MilestoneBoundary{
			Plan:     pl,
			Sessions: sm,
			Repo:     repo,
			Kind:     core.StepKind{Name: "milestone", Prompt: ms.Prompt, Check: ms.Check, Row: rows["milestone"]},
			Topic:    pl.Topic,
			Face:     w.Face,
		},
		FixRounds: cfg.Land.FixRounds,
		FixKind:   fixKind,
		Runner:    core.DefaultRunners(sm, []core.StepKind{fixKind})["diff"],
	}
	w.Loop = &core.RunLoop{
		Plan:        pl,
		TodoPath:    todo,
		Kinds:       kinds,
		Sessions:    sm,
		Store:       w.Store,
		Face:        w.Face,
		Notifier:    w.Notify,
		Hooks:       core.Hooks(cfg.Notify),
		Lander:      w.Gate,
		Runners:     runners,
		MaxRestarts: cfg.Watchdog.MaxRestarts,
	}
	return w, nil
}

func (w *Wiring) bind(runID string) {
	dir := w.Store.Dir(runID)
	w.Loop.RunID = runID
	w.Gate.RunID = runID
	w.Gate.Boundary.RunID = runID
	w.Gate.Boundary.RunDir = dir
	w.Face.Report = filepath.Join(dir, "report.md")
	w.Notify.Log = filepath.Join(dir, "notify.log")
}

func (w *Wiring) resolve(provider, model, effort, askURL, mcpConfigPath string) (core.ProviderArgs, error) {
	p, err := w.Registry.Resolve(provider)
	if err != nil {
		return core.ProviderArgs{}, err
	}
	return providers.ToCore(p, model, effort, askURL, mcpConfigPath), nil
}

func coreRow(r config.StepRow) core.StepRow {
	row := core.StepRow{
		Provider: r.Provider, Model: r.Model, Effort: r.Effort,
		Fallback: core.Fallback(r.Fallback),
		Timeout:  r.Timeout, Rounds: r.Rounds, ReviewTimeout: r.ReviewTimeout,
	}
	for _, rv := range r.Reviewers {
		row.Reviewers = append(row.Reviewers, core.Reviewer(rv))
	}
	return row
}
