package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"

	"r-loop/internal/askmcp"
	"r-loop/internal/config"
	"r-loop/internal/core"
	"r-loop/internal/face/plain"
	"r-loop/internal/face/tui"
	"r-loop/internal/gitrepo"
	"r-loop/internal/herdr"
	"r-loop/internal/notify"
	"r-loop/internal/plan"
	"r-loop/internal/prompts"
	"r-loop/internal/providers"
	"r-loop/internal/store"
)

type Options struct {
	Todo          string
	From          int
	Phases        []int
	Overrides     []config.Override
	Unattended    bool
	Plain, DryRun bool
}

type Env struct {
	Dir, Home, Herdr, Git string
	Pane                  string
	PID                   int
	Stdin                 io.Reader
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
	Face     core.Face
	Plain    *plain.Face
	TUI      *tui.Face
	Notify   *notify.Shell
	Gate     *core.LandGate
	Probe    *core.GateProbe
	Loop     *core.RunLoop
	Ask      *askmcp.Server
	Watch    *core.Watch
	Dog      *core.Watchdog
	Remedies *core.Remedies
	Router   *core.QuestionRouter
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
		case "answer":
			return Answer(args[1:], env)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code := 2
	if _, err := w.Ask.Serve(ctx); err != nil {
		fmt.Fprintf(w.Env.Stderr, "r-loop: ask server: %v\n", err)
	} else if err := w.startWatchdog(ctx); err != nil {
		code = fail(w.Env, err)
	} else {
		go w.pollAnswers(ctx)
		w.startTUI()
		w.Loop.ServeQuestions(ctx)
		var err error
		var empty bool
		if opts, empty, err = w.unblock(ctx, opts); err != nil {
			code = fail(w.Env, err)
		} else if empty {
			code = 0
		} else {
			code = w.Loop.Run(ctx, opts)
		}
	}
	if err := w.Dog.Stop(); err != nil {
		fmt.Fprintf(w.Env.Stderr, "r-loop: close watchdog: %v\n", err)
	}
	cancel()
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
		Plain:    &plain.Face{Out: env.Stdout, In: env.Stdin, TTY: terminal(env.Stdin)},
	}
	w.Face = w.Plain
	if useTUI(opts.Plain, terminal(env.Stdin), terminal(env.Stdout)) {
		_, noColor := os.LookupEnv("NO_COLOR")
		w.TUI = &tui.Face{In: env.Stdin, Out: env.Stdout, NoColor: noColor}
		w.TUI.Abort = func() error { return w.Store.MarkAbort(w.Loop.RunID) }
		w.Face = w.TUI
	}
	w.Ask = &askmcp.Server{Store: w.Store}
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
		Ask:        w.Ask,
		Resolve:    w.resolve,
		Now:        time.Now,
		StallGrace: cfg.Watchdog.StallGrace,
		ItemGates:  pl.Backlog,
	}
	runners := core.DefaultRunners(sm, kinds)
	impl := rows["implement"]
	fix := cfg.Land.Fix
	fixKind := core.StepKind{Name: "gatefix", Prompt: "gatefix", Check: "diff", Row: core.StepRow{
		Provider: fix.Provider, Model: fix.Model, Effort: fix.Effort,
		Timeout: impl.Timeout, Reviewers: impl.Reviewers, Rounds: 1, ReviewTimeout: impl.ReviewTimeout,
	}}
	ms := cfg.Steps["milestone"]
	gs := cfg.Steps["gate"]
	w.Probe = &core.GateProbe{
		Sessions: sm,
		Repo:     repo,
		Kind:     core.StepKind{Name: "gate", Prompt: gs.Prompt, Check: gs.Check, Row: rows["gate"]},
		Plan:     pl,
		Face:     w.Face,
		Timeout:  cfg.Land.GateTimeout,
	}
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
	if pl.Backlog {
		w.Gate.Suite = w.Probe
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
		Ask:         w.Ask,
		MaxRestarts: cfg.Watchdog.MaxRestarts,
	}
	w.Watch = &core.Watch{Store: w.Store, Face: w.Face, Checks: core.ShippedChecks(cfg.Watchdog.OvertimeFactor, cfg.Watchdog.DiffFactor), Repo: repo, Plan: pl}
	w.Loop.Watcher = w.Watch
	allow := cfg.Watchdog.Allow
	if opts.Unattended {
		allow = append(slices.Clone(allow), addedClasses(cfg)...)
		w.Loop.QuestionTimeout = cfg.Unattended.QuestionTimeout
	}
	fallbacks := map[string]core.Fallback{}
	for _, k := range kinds {
		fallbacks[k.Name] = k.Row.Fallback
	}
	w.Remedies = &core.Remedies{Allow: allow, Face: w.Face, Store: w.Store, Window: cfg.Watchdog.RemedyWindow, Now: time.Now, Watch: w.Watch, MaxRestarts: cfg.Watchdog.MaxRestarts, Fallbacks: fallbacks}
	w.Dog = &core.Watchdog{Host: w.Host, Prompts: w.Prompts, Store: w.Store, Face: w.Face, Root: root, Pane: env.Pane, TodoPath: todo, SpecDir: filepath.Dir(todo), Allow: allow}
	w.Router = &core.QuestionRouter{Deliver: w.Loop.Deliver, Repo: repo, AnswerWindow: cfg.Watchdog.AnswerWindow}
	w.Loop.RemedyWindow = cfg.Watchdog.RemedyWindow
	return w, nil
}

func addedClasses(cfg config.LoopConfig) []string {
	var added []string
	for _, c := range cfg.Unattended.Allow {
		if !slices.Contains(cfg.Watchdog.Allow, c) && !slices.Contains(added, c) {
			added = append(added, c)
		}
	}
	return added
}

func (w *Wiring) startWatchdog(ctx context.Context) error {
	wd := w.Config.Watchdog
	err := w.startDog(ctx, "watchdog.provider", wd.Provider, wd.Model, wd.Effort)
	if fb := wd.Fallback; err != nil && fb.Provider != "" {
		first := err
		if err = w.startDog(ctx, "watchdog.fallback", fb.Provider, fb.Model, fb.Effort); err != nil {
			err = exit(4, "watchdog did not start: %v; fallback: %v", first, err)
		}
	}
	if err != nil {
		return err
	}
	w.Watch.Dog = w.Dog
	w.Watch.PhaseCheck = &core.PhaseCheck{Dog: w.Dog, Repo: w.Loop.Sessions.Repo, Timeout: wd.CheckTimeout}
	w.Router.Dog = w.Dog
	w.Watch.Router = w.Router
	w.Ask.Handle(askmcp.WatchdogHandlers{Signal: w.Watch.Handle, Propose: w.Remedies.Propose, Restart: w.Remedies.Restart, Answer: w.Router.Answer})
	return nil
}

func (w *Wiring) startDog(ctx context.Context, field, provider, model, effort string) error {
	url := w.Ask.WatchdogURL()
	mcpPath := filepath.Join(w.Dog.RunDir, "watchdog.mcp.json")
	args, err := w.resolve(provider, model, effort, url, mcpPath)
	if err != nil {
		return exit(2, "%s: %v", field, err)
	}
	if slices.ContainsFunc(args.Args, func(a string) bool { return strings.Contains(a, mcpPath) }) {
		if err := providers.WriteMCPConfig(mcpPath, url); err != nil {
			return exit(2, "%v", err)
		}
	}
	w.Dog.Provider = args
	if err := w.Dog.Start(ctx); err != nil {
		return exit(4, "watchdog did not start: %v", err)
	}
	return nil
}

func (w *Wiring) startTUI() {
	if w.TUI == nil {
		return
	}
	run, _ := w.Store.Load(w.Loop.RunID)
	started := run.Started
	if started.IsZero() {
		started = time.Now()
	}
	w.TUI.Start(tui.Header{
		RunID:   w.Loop.RunID,
		Todo:    w.Opts.Todo,
		Report:  w.Plain.Report,
		Started: started,
	}, w.Plan.Phases, run.Events)
}

func (w *Wiring) faceName() string {
	if w.TUI != nil {
		return "tui"
	}
	return "plain"
}

func (w *Wiring) bind(runID string) {
	dir := w.Store.Dir(runID)
	w.Loop.RunID = runID
	w.Gate.RunID = runID
	w.Gate.Boundary.RunID = runID
	w.Gate.Boundary.RunDir = dir
	w.Probe.RunID, w.Probe.RunDir = runID, dir
	w.Plain.Report = filepath.Join(dir, "report.md")
	w.Notify.Log = filepath.Join(dir, "notify.log")
	w.Ask.RunDir, w.Ask.RunID = dir, runID
	w.Dog.RunID, w.Dog.RunDir = runID, dir
	if run, err := w.Store.Load(runID); err == nil {
		w.Ask.Seq = len(run.Questions)
	}
}

func useTUI(plain, stdinTTY, stdoutTTY bool) bool {
	return !plain && stdinTTY && stdoutTTY
}

func terminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && term.IsTerminal(f.Fd())
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
