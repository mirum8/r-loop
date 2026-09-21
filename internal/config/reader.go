package config

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed defaults.yaml
var defaultsYAML []byte

var ErrConfig = errors.New("config error")

type configError string

func (e configError) Error() string        { return string(e) }
func (e configError) Is(target error) bool { return target == ErrConfig }

type Override struct {
	Key, Step, Value string
}

type Reviewer struct {
	Provider, Model, Effort string
}

type Fallback struct {
	Provider, Model, Effort string
}

type GateFix struct {
	Provider, Model, Effort string
}

type StepRow struct {
	Prompt, Check, Provider, Model, Effort string
	Fallback                               Fallback
	Timeout                                time.Duration
	Reviewers                              []Reviewer
	Rounds                                 int
	ReviewTimeout                          time.Duration
}

type Watchdog struct {
	Provider, Model, Effort                              string
	Fallback                                             Fallback
	Allow                                                []string
	RemedyWindow, AnswerWindow, CheckTimeout, StallGrace time.Duration
	UnblockTimeout                                       time.Duration
	OvertimeFactor, DiffFactor                           float64
	MaxRestarts                                          int
}

type Land struct {
	FixRounds   int
	GateTimeout time.Duration
	Fix         GateFix
}

type Unattended struct {
	Allow           []string
	QuestionTimeout time.Duration
}

type Notify struct {
	OnHalt, OnWarn, OnDone string
}

type LoopConfig struct {
	Pipeline   []string
	Steps      map[string]StepRow
	Providers  map[string]yaml.Node
	Watchdog   Watchdog
	Land       Land
	Unattended Unattended
	Notify     Notify
	Provenance map[string]string

	overrides []appliedOverride
	swaps     []fallbackSwap
}

type fallbackSwap struct {
	step      string
	to, old   Fallback
	oldSource string
}

type appliedOverride struct {
	Override
	old, oldSource string
}

var remedyClasses = []string{"deps", "ports", "containers", "locks", "restart", "retry", "provider"}

var checks = []string{"plan-file", "diff", "report"}

type schema map[string]schema

var roleSchema = schema{"provider": nil, "model": nil, "effort": nil}

var topSchema = schema{
	"pipeline": nil,
	"steps": schema{"*": schema{
		"prompt": nil, "check": nil, "provider": nil, "model": nil, "effort": nil, "timeout": nil,
		"fallback": nil, "reviewers": nil, "rounds": nil, "reviewTimeout": nil,
	}},
	"providers":  schema{"*": nil},
	"land":       schema{"fixRounds": nil, "gateTimeout": nil, "fix": roleSchema},
	"unattended": schema{"allow": nil, "questionTimeout": nil},
	"notify":     schema{"onHalt": nil, "onWarn": nil, "onDone": nil},
	"watchdog": schema{
		"provider": nil, "model": nil, "effort": nil, "fallback": nil, "allow": nil, "maxRestarts": nil,
		"remedyWindow": nil, "answerWindow": nil, "checkTimeout": nil, "stallGrace": nil, "unblockTimeout": nil,
		"overtimeFactor": nil, "diffFactor": nil,
	},
}

const defaultSource = "default"

type layer struct {
	file  string
	nodes map[string]*yaml.Node
}

func errAt(file string, n *yaml.Node, format string, args ...any) error {
	return configError(fmt.Sprintf("%s:%d: ", file, n.Line) + fmt.Sprintf(format, args...))
}

func isNull(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

func parseLayer(file string, data []byte) (*layer, error) {
	l := &layer{file: file, nodes: map[string]*yaml.Node{}}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, configError(fmt.Sprintf("%s: %v", file, err))
	}
	if len(doc.Content) == 0 {
		return l, nil
	}
	if err := rejectFlow(file, &doc); err != nil {
		return nil, err
	}
	return l, l.walk("", doc.Content[0], topSchema)
}

func rejectFlow(file string, n *yaml.Node) error {
	if n.Style&yaml.FlowStyle != 0 {
		return errAt(file, n, "flow style is not accepted, write it block style")
	}
	for _, c := range n.Content {
		if err := rejectFlow(file, c); err != nil {
			return err
		}
	}
	return nil
}

func (l *layer) walk(prefix string, n *yaml.Node, s schema) error {
	if isNull(n) {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return errAt(l.file, n, "%s must be a mapping", strings.TrimSuffix(prefix, "."))
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		path := prefix + k.Value
		sub, ok := s[k.Value]
		if !ok {
			sub, ok = s["*"]
		}
		if !ok {
			return errAt(l.file, k, "unknown key %q", path)
		}
		l.nodes[path] = v
		if sub != nil {
			if err := l.walk(path+".", v, sub); err != nil {
				return err
			}
		}
	}
	return nil
}

type resolver struct {
	layers []*layer
	prov   map[string]string
}

func (r *resolver) lookup(path string) (*yaml.Node, *layer) {
	for _, l := range r.layers {
		if n, ok := l.nodes[path]; ok {
			return n, l
		}
	}
	return nil, nil
}

func source(l *layer, path string) string {
	if l.file == defaultSource {
		return defaultSource
	}
	return l.file + ":" + path
}

func (r *resolver) scalar(path string) (string, *yaml.Node, *layer, error) {
	n, l := r.lookup(path)
	if n == nil {
		r.prov[path] = defaultSource
		return "", nil, nil, nil
	}
	r.prov[path] = source(l, path)
	if isNull(n) {
		return "", n, l, nil
	}
	if n.Kind != yaml.ScalarNode {
		return "", n, l, errAt(l.file, n, "%s must be a single value", path)
	}
	return n.Value, n, l, nil
}

func (r *resolver) str(path string) (string, error) {
	v, _, _, err := r.scalar(path)
	return v, err
}

func (r *resolver) duration(path string) (time.Duration, error) {
	v, n, l, err := r.scalar(path)
	if err != nil || v == "" {
		return 0, err
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, errAt(l.file, n, "%s: %q is not a duration", path, v)
	}
	return d, nil
}

func (r *resolver) count(path string) (int, error) {
	v, n, l, err := r.scalar(path)
	if err != nil || v == "" {
		return 0, err
	}
	i, err := strconv.Atoi(v)
	if err != nil || i < 0 {
		return 0, errAt(l.file, n, "%s: %q is not an integer ≥ 0", path, v)
	}
	return i, nil
}

func (r *resolver) factor(path string) (float64, error) {
	v, n, l, err := r.scalar(path)
	if err != nil {
		return 0, err
	}
	f, perr := strconv.ParseFloat(v, 64)
	if perr != nil || f < 1 {
		if n == nil {
			return 0, configError(fmt.Sprintf("%s must be a number ≥ 1", path))
		}
		return 0, errAt(l.file, n, "%s: %q is not a number ≥ 1", path, v)
	}
	return f, nil
}

func (r *resolver) list(path string) ([]*yaml.Node, *layer, error) {
	n, l := r.lookup(path)
	if n == nil {
		r.prov[path] = defaultSource
		return nil, nil, nil
	}
	r.prov[path] = source(l, path)
	if isNull(n) {
		return nil, l, nil
	}
	if n.Kind != yaml.SequenceNode {
		return nil, l, errAt(l.file, n, "%s must be a list", path)
	}
	return n.Content, l, nil
}

func (r *resolver) names(path string) ([]string, error) {
	items, l, err := r.list(path)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, it := range items {
		if it.Kind != yaml.ScalarNode {
			return nil, errAt(l.file, it, "%s: each entry must be a single value", path)
		}
		out = append(out, it.Value)
	}
	return out, nil
}

func (r *resolver) classes(path string) ([]string, error) {
	items, l, err := r.list(path)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, it := range items {
		if it.Kind != yaml.ScalarNode || !slices.Contains(remedyClasses, it.Value) {
			return nil, errAt(l.file, it, "%s: %q is not a remedy class (%s)", path, it.Value, strings.Join(remedyClasses, ", "))
		}
		out = append(out, it.Value)
	}
	return out, nil
}

func role(file, path string, n *yaml.Node) (provider, model, effort string, err error) {
	if n.Kind == yaml.ScalarNode && !isNull(n) {
		if n.Value == "" {
			return "", "", "", errAt(file, n, "%s needs a provider name", path)
		}
		return n.Value, "", "", nil
	}
	if n.Kind != yaml.MappingNode {
		return "", "", "", errAt(file, n, "%s must be a provider name or a block with provider, model and effort", path)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if v.Kind != yaml.ScalarNode {
			return "", "", "", errAt(file, v, "%s.%s must be a single value", path, k.Value)
		}
		switch k.Value {
		case "provider":
			provider = v.Value
		case "model":
			model = v.Value
		case "effort":
			effort = v.Value
		default:
			return "", "", "", errAt(file, k, "unknown key %q", path+"."+k.Value)
		}
	}
	if provider == "" {
		return "", "", "", errAt(file, n, "%s needs a provider", path)
	}
	return provider, model, effort, nil
}

func Load(projectDir, homeDir string, overrides []Override) (LoopConfig, error) {
	files := []struct{ label, path string }{
		{".r-loop/config.yaml", filepath.Join(projectDir, ".r-loop", "config.yaml")},
		{"~/.config/r-loop/config.yaml", filepath.Join(homeDir, ".config", "r-loop", "config.yaml")},
	}
	var layers []*layer
	for _, f := range files {
		data, err := os.ReadFile(f.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return LoopConfig{}, err
		}
		l, err := parseLayer(f.label, data)
		if err != nil {
			return LoopConfig{}, err
		}
		layers = append(layers, l)
	}
	defaults, err := parseLayer(defaultSource, defaultsYAML)
	if err != nil {
		return LoopConfig{}, err
	}
	r := &resolver{layers: append(layers, defaults), prov: map[string]string{}}
	cfg, err := r.resolve()
	if err != nil {
		return LoopConfig{}, err
	}
	if err := cfg.applyOverrides(overrides); err != nil {
		return LoopConfig{}, err
	}
	if err := r.resolveFix(&cfg); err != nil {
		return LoopConfig{}, err
	}
	return cfg, nil
}

func (r *resolver) resolve() (LoopConfig, error) {
	cfg := LoopConfig{Steps: map[string]StepRow{}, Providers: map[string]yaml.Node{}, Provenance: r.prov}
	var err error
	if cfg.Pipeline, err = r.names("pipeline"); err != nil {
		return cfg, err
	}
	for _, name := range r.children("steps") {
		row, err := r.row(name)
		if err != nil {
			return cfg, err
		}
		cfg.Steps[name] = row
	}
	pipeline, pl := r.lookup("pipeline")
	for i, name := range cfg.Pipeline {
		if _, ok := cfg.Steps[name]; !ok {
			return cfg, errAt(pl.file, pipeline.Content[i], "pipeline entry %q has no steps row", name)
		}
	}
	for _, name := range r.children("providers") {
		n, l := r.lookup("providers." + name)
		cfg.Providers[name] = *n
		r.prov["providers."+name] = source(l, "providers."+name)
	}
	return cfg, r.sections(&cfg)
}

func (r *resolver) children(section string) []string {
	var names []string
	prefix := section + "."
	for _, l := range r.layers {
		for path := range l.nodes {
			rest, ok := strings.CutPrefix(path, prefix)
			if ok && !strings.Contains(rest, ".") && !slices.Contains(names, rest) {
				names = append(names, rest)
			}
		}
	}
	sort.Strings(names)
	return names
}

func (r *resolver) row(name string) (StepRow, error) {
	p := "steps." + name + "."
	var row StepRow
	var err error
	for _, f := range []struct {
		key string
		dst *string
	}{{"prompt", &row.Prompt}, {"provider", &row.Provider}, {"model", &row.Model}, {"effort", &row.Effort}} {
		if *f.dst, err = r.str(p + f.key); err != nil {
			return row, err
		}
	}
	check, n, l, err := r.scalar(p + "check")
	if err != nil {
		return row, err
	}
	if !slices.Contains(checks, check) {
		if n == nil {
			n, l = r.lookup("steps." + name)
		}
		return row, errAt(l.file, n, "%scheck: %q is not one of %s", p, check, strings.Join(checks, ", "))
	}
	row.Check = check
	if row.Timeout, err = r.duration(p + "timeout"); err != nil {
		return row, err
	}
	if row.ReviewTimeout, err = r.duration(p + "reviewTimeout"); err != nil {
		return row, err
	}
	if row.Rounds, err = r.count(p + "rounds"); err != nil {
		return row, err
	}
	items, l, err := r.list(p + "reviewers")
	if err != nil {
		return row, err
	}
	for _, it := range items {
		pr, m, e, err := role(l.file, p+"reviewers", it)
		if err != nil {
			return row, err
		}
		row.Reviewers = append(row.Reviewers, Reviewer{pr, m, e})
	}
	fb, fl := r.lookup(p + "fallback")
	if fb == nil || isNull(fb) {
		r.prov[p+"fallback"] = defaultSource
		return row, nil
	}
	r.prov[p+"fallback"] = source(fl, p+"fallback")
	pr, m, e, err := role(fl.file, p+"fallback", fb)
	if err != nil {
		return row, err
	}
	if pr == row.Provider {
		return row, errAt(fl.file, fb, "%sfallback names the step's own provider %q", p, pr)
	}
	row.Fallback = Fallback{pr, m, e}
	return row, nil
}

func (r *resolver) sections(cfg *LoopConfig) error {
	var err error
	w := &cfg.Watchdog
	for _, f := range []struct {
		path string
		dst  *string
	}{
		{"watchdog.provider", &w.Provider}, {"watchdog.model", &w.Model}, {"watchdog.effort", &w.Effort},
		{"notify.onHalt", &cfg.Notify.OnHalt}, {"notify.onWarn", &cfg.Notify.OnWarn}, {"notify.onDone", &cfg.Notify.OnDone},
	} {
		if *f.dst, err = r.str(f.path); err != nil {
			return err
		}
	}
	for _, f := range []struct {
		path string
		dst  *time.Duration
	}{
		{"watchdog.remedyWindow", &w.RemedyWindow}, {"watchdog.answerWindow", &w.AnswerWindow},
		{"watchdog.checkTimeout", &w.CheckTimeout}, {"watchdog.stallGrace", &w.StallGrace},
		{"watchdog.unblockTimeout", &w.UnblockTimeout},
		{"land.gateTimeout", &cfg.Land.GateTimeout}, {"unattended.questionTimeout", &cfg.Unattended.QuestionTimeout},
	} {
		if *f.dst, err = r.duration(f.path); err != nil {
			return err
		}
	}
	if w.MaxRestarts, err = r.count("watchdog.maxRestarts"); err != nil {
		return err
	}
	if cfg.Land.FixRounds, err = r.count("land.fixRounds"); err != nil {
		return err
	}
	if w.OvertimeFactor, err = r.factor("watchdog.overtimeFactor"); err != nil {
		return err
	}
	if w.DiffFactor, err = r.factor("watchdog.diffFactor"); err != nil {
		return err
	}
	if w.Allow, err = r.classes("watchdog.allow"); err != nil {
		return err
	}
	if fb, fl := r.lookup("watchdog.fallback"); fb == nil || isNull(fb) {
		r.prov["watchdog.fallback"] = defaultSource
	} else {
		r.prov["watchdog.fallback"] = source(fl, "watchdog.fallback")
		pr, m, e, err := role(fl.file, "watchdog.fallback", fb)
		if err != nil {
			return err
		}
		if pr == w.Provider {
			return errAt(fl.file, fb, "watchdog.fallback names the watchdog's own provider %q", pr)
		}
		w.Fallback = Fallback{pr, m, e}
	}
	cfg.Unattended.Allow, err = r.classes("unattended.allow")
	return err
}

func ParseOverride(key, arg string) (Override, error) {
	step, value, ok := strings.Cut(arg, "=")
	if !ok || step == "" {
		return Override{}, configError(fmt.Sprintf("--%s %q: want <step>=<%s>", key, arg, key))
	}
	return Override{Key: key, Step: step, Value: value}, nil
}

func (cfg *LoopConfig) applyOverrides(overrides []Override) error {
	configured := maps.Clone(cfg.Steps)
	seen := map[Override]bool{}
	for _, o := range overrides {
		flag := "--" + o.Key
		row, ok := cfg.Steps[o.Step]
		if !ok {
			return configError(fmt.Sprintf("%s: unknown step %q", flag, o.Step))
		}
		k := Override{Key: o.Key, Step: o.Step}
		if seen[k] {
			return configError(fmt.Sprintf("%s: step %q given twice", flag, o.Step))
		}
		seen[k] = true
		var field *string
		switch o.Key {
		case "provider":
			field = &row.Provider
		case "model":
			field = &row.Model
		case "effort":
			field = &row.Effort
		default:
			return configError(fmt.Sprintf("unknown override --%s", o.Key))
		}
		path := "steps." + o.Step + "." + o.Key
		cfg.overrides = append(cfg.overrides, appliedOverride{o, *field, cfg.Provenance[path]})
		*field = o.Value
		if o.Key == "provider" && row.Fallback.Provider == o.Value {
			orig := configured[o.Step]
			fb := "steps." + o.Step + ".fallback"
			swap := fallbackSwap{o.Step, Fallback{orig.Provider, orig.Model, orig.Effort}, row.Fallback, cfg.Provenance[fb]}
			row.Fallback = swap.to
			cfg.swaps = append(cfg.swaps, swap)
			cfg.Provenance[fb] = "flag:" + flag
		}
		cfg.Steps[o.Step] = row
		cfg.Provenance[path] = "flag:" + flag
	}
	return nil
}

func (r *resolver) resolveFix(cfg *LoopConfig) error {
	impl := cfg.Steps["implement"]
	inherited := map[string]string{"provider": impl.Provider, "model": impl.Model, "effort": impl.Effort}
	got := map[string]string{}
	src := map[string]string{}
	for _, key := range []string{"provider", "model", "effort"} {
		path := "land.fix." + key
		v, n, l, err := r.scalar(path)
		if err != nil {
			return err
		}
		if n != nil {
			got[key], src[key] = v, source(l, path)
		}
	}
	other := got["provider"] != "" && got["provider"] != impl.Provider
	resolved := map[string]string{}
	for _, key := range []string{"provider", "model", "effort"} {
		path := "land.fix." + key
		switch {
		case src[key] != "" && (key != "provider" || got[key] != ""):
			resolved[key], r.prov[path] = got[key], src[key]
		case other:
			resolved[key], r.prov[path] = "", src["provider"]
		default:
			resolved[key], r.prov[path] = inherited[key], r.prov["steps.implement."+key]
		}
	}
	cfg.Land.Fix = GateFix{resolved["provider"], resolved["model"], resolved["effort"]}
	return nil
}
