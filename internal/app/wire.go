package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
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
	From          string
	Phases        []string
	Overrides     []config.Override
	Unattended    bool
	Yes           bool
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
	Findings core.PlanFindings
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
	Triaged  bool

	groups   []core.Group
	triageMu sync.Mutex
	triaging *triageRun
	lock     *store.Lock
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

type phaseList struct{ dst *[]string }

func (p phaseList) String() string { return "" }

func (p phaseList) Set(arg string) error {
	for _, s := range strings.Split(arg, ",") {
		id := strings.ToLower(strings.TrimSpace(s))
		if !core.ValidPhaseID(id) {
			return fmt.Errorf("--phases %q: want n,n", arg)
		}
		*p.dst = append(*p.dst, id)
	}
	return nil
}

type phaseFrom struct{ dst *string }

func (p phaseFrom) String() string { return "" }

func (p phaseFrom) Set(arg string) error {
	id := strings.ToLower(strings.TrimSpace(arg))
	if !core.ValidPhaseID(id) {
		return fmt.Errorf("--from %q: want a phase label such as 10 or 10a", arg)
	}
	*p.dst = id
	return nil
}

func flagSet(o *Options) *flag.FlagSet {
	fs := flag.NewFlagSet("r-loop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Var(phaseFrom{&o.From}, "from", "run the unticked phases from `N` on, in plan order; N is a heading label such as 10 or 10a")
	fs.Var(phaseList{&o.Phases}, "phases", "run only these unticked phases, comma-separated `n,n`; labels such as 10a work")
	for _, key := range []string{"provider", "model", "effort"} {
		fs.Var(overrides{key, &o.Overrides}, key, "set one row's "+key+" for this run, `<step>=<"+key+">`; repeatable")
	}
	fs.BoolVar(&o.Unattended, "unattended", false, "never ask the maintainer; the watchdog decides from the repository")
	fs.BoolVar(&o.Yes, "yes", false, "start after triage without asking the maintainer; verification still runs")
	fs.BoolVar(&o.Plain, "plain", false, "plain line output instead of the TUI")
	fs.BoolVar(&o.DryRun, "dry-run", false, "print the banner and the run list, start nothing")
	return fs
}

func parseFlags(args []string) (Options, []string, error) {
	var o Options
	fs := flagSet(&o)
	positional, err := parsePositional(fs, args)
	return o, positional, err
}

func parsePositional(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return positional, nil
}

func ParseArgs(args []string) (Options, error) {
	o, positional, err := parseFlags(args)
	if err != nil {
		return o, err
	}
	if len(positional) != 1 {
		return o, errors.New("want exactly one todo path")
	}
	o.Todo = positional[0]
	return o, nil
}

func usage() string {
	var b strings.Builder
	fs := flagSet(&Options{})
	fs.SetOutput(&b)
	fs.PrintDefaults()
	return strings.TrimRight(b.String(), "\n")
}

func freeForm(positional []string) bool {
	return len(positional) > 1 || len(positional) == 1 && !strings.HasSuffix(positional[0], ".md")
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
		case "--create-config":
			if len(args) == 1 {
				return CreateConfig(env)
			}
		}
	}
	opts, positional, err := parseFlags(args)
	if err == nil && freeForm(positional) {
		if opts, err = Intake(args, opts, env); err != nil {
			return fail(env, err)
		}
	} else if err == nil {
		opts, err = ParseArgs(args)
	}
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

func CreateConfig(env Env) int {
	path, err := config.Create(env.Home)
	if err != nil {
		return fail(env, err)
	}
	fmt.Fprintf(env.Stdout, "wrote %s\n", path)
	return 0
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
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	quit := make(chan struct{})
	defer func() {
		signal.Stop(sigs)
		close(quit)
	}()
	go func() {
		for {
			select {
			case sig := <-sigs:
				cancel(errors.New(signalName(sig)))
				if w.TUI != nil {
					w.TUI.Stop()
				}
			case <-quit:
				return
			}
		}
	}()
	if w.TUI != nil {
		w.TUI.OnExit = func(err error) { cancel(displayExit(err)) }
	}
	code := 2
	if _, err := w.Ask.Serve(ctx); err != nil {
		fmt.Fprintf(w.Env.Stderr, "r-loop: ask server: %v\n", err)
	} else if err := w.startWatchdog(ctx); err != nil {
		code = fail(w.Env, err)
	} else {
		w.startTUI()
		code = w.run(ctx, opts)
	}
	if w.TUI != nil && context.Cause(ctx) != nil {
		w.TUI.Stop()
	}
	if err := w.Dog.Stop(); err != nil {
		fmt.Fprintf(w.Env.Stderr, "r-loop: close watchdog: %v\n", err)
	}
	cancel(nil)
	w.Ask.Wait()
	w.release()
	w.Face.Close()
	return code
}

func (w *Wiring) release() {
	if err := w.lock.Release(w.Loop.RunID, w.Env.PID); err != nil {
		fmt.Fprintf(w.Env.Stderr, "r-loop: release run lock: %v\n", err)
	}
	w.lock = nil
}

func signalName(sig os.Signal) string {
	switch sig {
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGHUP:
		return "SIGHUP"
	default:
		return sig.String()
	}
}

func displayExit(err error) error {
	if err != nil {
		return fmt.Errorf("display exited: %w", err)
	}
	return errors.New("display closed")
}

func (w *Wiring) run(ctx context.Context, opts core.RunOptions) int {
	kept, deferrals, finished, err := w.unblock(ctx, opts)
	if err == nil && !finished {
		opts, finished, err = w.triage(ctx, opts, kept, deferrals)
	}
	switch {
	case err != nil:
		return fail(w.Env, err)
	case finished:
		return 0
	}
	return w.Loop.Run(ctx, opts)
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
		Plain:    &plain.Face{Out: env.Stdout},
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
		Label:      cfg.Label,
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
	}
	fallbacks := map[string]core.Fallback{}
	for _, k := range kinds {
		fallbacks[k.Name] = k.Row.Fallback
	}
	w.Remedies = &core.Remedies{Allow: allow, Store: w.Store, Face: w.Face, Now: time.Now, Watch: w.Watch, MaxRestarts: cfg.Watchdog.MaxRestarts, Fallbacks: fallbacks, Asks: w.asks}
	w.Dog = &core.Watchdog{Host: w.Host, Prompts: w.Prompts, Store: w.Store, Face: w.Face, Root: root, Pane: env.Pane, Label: cfg.Label, TodoPath: todo, SpecDir: filepath.Dir(todo), Allow: allow, Unattended: opts.Unattended}
	w.Remedies.Dog = w.Dog
	w.Router = &core.QuestionRouter{Deliver: w.Loop.Deliver, Repo: repo}
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
	if err := w.startDog(ctx, wd.Provider, wd.Model, wd.Effort); err != nil {
		return err
	}
	w.Watch.Dog = w.Dog
	w.Dog.OnGone = w.Watch.WatchdogGone
	w.Watch.PhaseCheck = &core.PhaseCheck{Dog: w.Dog, Repo: w.Loop.Sessions.Repo, Timeout: wd.CheckTimeout, Backlog: w.Plan.Backlog}
	w.Router.Dog = w.Dog
	w.Watch.Router = w.Router
	w.Ask.Handle(askmcp.WatchdogHandlers{Signal: w.Watch.Handle, Propose: w.Remedies.Propose, Restart: w.Remedies.Restart, Answer: w.Router.Answer, AskMaintainer: w.Dog.AskMaintainer, Resume: w.Dog.Resume, SubmitTriage: w.submitTriage, SubmitGate: w.submitGate})
	return nil
}

func (w *Wiring) startDog(ctx context.Context, provider, model, effort string) error {
	url := w.Ask.WatchdogURL()
	mcpPath := filepath.Join(w.Dog.RunDir, "watchdog.mcp.json")
	args, err := w.resolve(provider, model, effort, url, mcpPath)
	if err != nil {
		return exit(2, "watchdog.provider: %v", err)
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
	steps := make([]string, len(w.Loop.Kinds))
	for i, k := range w.Loop.Kinds {
		steps[i] = k.Name
	}
	w.TUI.Start(tui.Header{
		RunID:   w.Loop.RunID,
		Todo:    w.Opts.Todo,
		Report:  w.Plain.Report,
		Started: started,
		Steps:   steps,
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

func (w *Wiring) asks(provider string) bool {
	p, err := w.Registry.Resolve(provider)
	return err == nil && p.Ask == "mcp"
}
