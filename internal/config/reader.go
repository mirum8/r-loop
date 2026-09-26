package config

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
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
	Name, Prompt, Requires  string
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
	Provider, Model, Effort                  string
	Allow, Dialogs                           []string
	BlockerTimeout, CheckTimeout, StallGrace time.Duration
	UnblockTimeout, TriageTimeout            time.Duration
	OvertimeFactor, DiffFactor               float64
	MaxRestarts                              int
}

type Intake struct {
	Provider, Model, Effort string
}

type Land struct {
	FixRounds   int
	GateTimeout time.Duration
	Fix         GateFix
}

type Unattended struct {
	Allow []string
}

type Notify struct {
	OnHalt, OnWarn, OnDone string
}

type LoopConfig struct {
	Label      string
	Pipeline   []string
	Steps      map[string]StepRow
	Providers  map[string]yaml.Node
	Watchdog   Watchdog
	Intake     Intake
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
	path, old, oldSource string
}

var remedyClasses = []string{"deps", "ports", "containers", "locks", "restart", "retry", "provider"}

var reviewerName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

var checks = []string{"plan-file", "diff", "report"}

type schema map[string]schema

var roleSchema = schema{"provider": nil, "model": nil, "effort": nil}

var topSchema = schema{
	"label":    nil,
	"pipeline": nil,
	"steps": schema{"*": schema{
		"prompt": nil, "check": nil, "provider": nil, "model": nil, "effort": nil, "timeout": nil,
		"fallback": nil, "reviewers": nil, "rounds": nil, "reviewTimeout": nil,
	}},
	"providers":  schema{"*": nil},
	"intake":     roleSchema,
	"land":       schema{"fixRounds": nil, "gateTimeout": nil, "fix": roleSchema},
	"unattended": schema{"allow": nil},
	"notify":     schema{"onHalt": nil, "onWarn": nil, "onDone": nil},
	"watchdog": schema{
		"provider": nil, "model": nil, "effort": nil, "allow": nil, "dialogs": nil, "maxRestarts": nil,
		"blockerTimeout": nil, "remedyWindow": nil, "checkTimeout": nil, "stallGrace": nil, "unblockTimeout": nil, "triageTimeout": nil,
		"overtimeFactor": nil, "diffFactor": nil,
	},
}

const defaultSource = "default"

const IntakeRow = "intake"

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

func (r *resolver) numeric(path string) (string, *yaml.Node, *layer, error) {
	for _, l := range r.layers {
		n := l.nodes[path]
		if n == nil || isNull(n) {
			continue
		}
		r.prov[path] = source(l, path)
		if n.Kind != yaml.ScalarNode {
			return "", n, l, errAt(l.file, n, "%s must be a single value", path)
		}
		return n.Value, n, l, nil
	}
	r.prov[path] = defaultSource
	return "", nil, nil, nil
}

func (r *resolver) str(path string) (string, error) {
	v, _, _, err := r.scalar(path)
	return v, err
}

func (r *resolver) duration(path string) (time.Duration, error) {
	v, n, l, err := r.numeric(path)
	if err != nil || n == nil {
		return 0, err
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, errAt(l.file, n, "%s: %q is not a duration", path, v)
	}
	if d <= 0 {
		return 0, errAt(l.file, n, "%s: %q is not a positive duration", path, v)
	}
	return d, nil
}

func (r *resolver) aliased(path, old string) (time.Duration, error) {
	for _, l := range r.layers {
		n, o := l.nodes[path], l.nodes[old]
		if n != nil && !isNull(n) && o != nil && !isNull(o) {
			return 0, errAt(l.file, o, "%s is the old name of %s: set one of them", old, path)
		}
		if n == nil || isNull(n) {
			if o == nil || isNull(o) {
				continue
			}
			d, err := r.duration(old)
			r.prov[path] = r.prov[old]
			delete(r.prov, old)
			return d, err
		}
		break
	}
	return r.duration(path)
}

func (r *resolver) count(path string) (int, error) {
	v, n, l, err := r.numeric(path)
	if err != nil || n == nil {
		return 0, err
	}
	i, err := strconv.Atoi(v)
	if err != nil || i < 0 {
		return 0, errAt(l.file, n, "%s: %q is not an integer ≥ 0", path, v)
	}
	return i, nil
}

func (r *resolver) factor(path string) (float64, error) {
	v, n, l, err := r.numeric(path)
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

func (r *resolver) rules(path string) ([]string, error) {
	items, l, err := r.list(path)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, it := range items {
		if it.Kind != yaml.ScalarNode {
			return nil, errAt(l.file, it, "%s: each entry must be a single value", path)
		}
		if strings.TrimSpace(it.Value) == "" {
			return nil, errAt(l.file, it, "%s: a rule is empty", path)
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
	rv, err := reviewer(file, path, n, false)
	return rv.Provider, rv.Model, rv.Effort, err
}

func reviewer(file, path string, n *yaml.Node, named bool) (rv Reviewer, err error) {
	if n.Kind != yaml.MappingNode {
		return rv, errAt(file, n, "%s must be a block with provider, model and effort (run r-loop --migrate-config)", path)
	}
	fields := map[string]*string{"provider": &rv.Provider, "model": &rv.Model, "effort": &rv.Effort}
	if named {
		fields["name"], fields["prompt"], fields["requires"] = &rv.Name, &rv.Prompt, &rv.Requires
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if v.Kind != yaml.ScalarNode {
			return rv, errAt(file, v, "%s.%s must be a single value", path, k.Value)
		}
		dst, ok := fields[k.Value]
		if !ok {
			return rv, errAt(file, k, "unknown key %q", path+"."+k.Value)
		}
		*dst = v.Value
	}
	for _, f := range []struct{ need, v string }{{"a provider", rv.Provider}, {"a model", rv.Model}, {"an effort", rv.Effort}} {
		if f.v == "" {
			return rv, errAt(file, n, "%s needs %s", path, f.need)
		}
	}
	if rv.Name != "" && !reviewerName.MatchString(rv.Name) {
		return rv, errAt(file, n, "%s.name must be lowercase letters, digits and dashes, got %q", path, rv.Name)
	}
	if r := rv.Requires; r != "" && (filepath.IsAbs(r) || !filepath.IsLocal(r)) {
		return rv, errAt(file, n, "%s.requires must be a path inside the repository, got %q", path, r)
	}
	return rv, nil
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
	return cfg, cfg.requireRoles()
}

func (cfg *LoopConfig) requireRoles() error {
	roles := map[string][3]string{
		"watchdog": {cfg.Watchdog.Provider, cfg.Watchdog.Model, cfg.Watchdog.Effort},
		IntakeRow:  {cfg.Intake.Provider, cfg.Intake.Model, cfg.Intake.Effort},
		"land.fix": {cfg.Land.Fix.Provider, cfg.Land.Fix.Model, cfg.Land.Fix.Effort},
	}
	for name, row := range cfg.Steps {
		roles["steps."+name] = [3]string{row.Provider, row.Model, row.Effort}
	}
	for _, path := range slices.Sorted(maps.Keys(roles)) {
		for i, key := range []string{"provider", "model", "effort"} {
			if roles[path][i] == "" {
				return configError(fmt.Sprintf("%s.%s: not set, every session names its provider, model and effort", path, key))
			}
		}
	}
	return nil
}

func (r *resolver) resolve() (LoopConfig, error) {
	cfg := LoopConfig{Steps: map[string]StepRow{}, Providers: map[string]yaml.Node{}, Provenance: r.prov}
	label, n, l, err := r.scalar("label")
	if err != nil {
		return cfg, err
	}
	if label != "" && !reviewerName.MatchString(label) {
		return cfg, errAt(l.file, n, "label must be lowercase letters, digits and dashes, got %q", label)
	}
	cfg.Label = label
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
	ids := map[string]bool{}
	for _, it := range items {
		rv, err := reviewer(l.file, p+"reviewers", it, true)
		if err != nil {
			return row, err
		}
		id := rv.Name
		if id == "" {
			id = rv.Provider
		}
		if ids[id] {
			return row, errAt(l.file, it, "%sreviewers: two reviewers named %q, give one a name", p, id)
		}
		ids[id] = true
		row.Reviewers = append(row.Reviewers, rv)
	}
	if row.Timeout == 0 {
		n, l := r.lookup("steps." + name)
		return row, errAt(l.file, n, "%stimeout: not set, want a positive duration", p)
	}
	if row.ReviewTimeout == 0 && row.Rounds > 0 && len(row.Reviewers) > 0 {
		n, l := r.lookup("steps." + name)
		return row, errAt(l.file, n, "%sreviewTimeout: not set, want a positive duration for the review half", p)
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
		{"intake.provider", &cfg.Intake.Provider}, {"intake.model", &cfg.Intake.Model}, {"intake.effort", &cfg.Intake.Effort},
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
		{"watchdog.checkTimeout", &w.CheckTimeout}, {"watchdog.stallGrace", &w.StallGrace},
		{"watchdog.unblockTimeout", &w.UnblockTimeout}, {"watchdog.triageTimeout", &w.TriageTimeout},
		{"land.gateTimeout", &cfg.Land.GateTimeout},
	} {
		if *f.dst, err = r.duration(f.path); err != nil {
			return err
		}
	}
	if w.BlockerTimeout, err = r.aliased("watchdog.blockerTimeout", "watchdog.remedyWindow"); err != nil {
		return err
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
	if w.Dialogs, err = r.rules("watchdog.dialogs"); err != nil {
		return err
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
		if !ok && o.Step != IntakeRow {
			return configError(fmt.Sprintf("%s: unknown step %q", flag, o.Step))
		}
		k := Override{Key: o.Key, Step: o.Step}
		if seen[k] {
			return configError(fmt.Sprintf("%s: step %q given twice", flag, o.Step))
		}
		seen[k] = true
		fields := map[string]*string{"provider": &row.Provider, "model": &row.Model, "effort": &row.Effort}
		path := "steps." + o.Step + "." + o.Key
		if o.Step == IntakeRow {
			fields = map[string]*string{"provider": &cfg.Intake.Provider, "model": &cfg.Intake.Model, "effort": &cfg.Intake.Effort}
			path = IntakeRow + "." + o.Key
		}
		field, ok := fields[o.Key]
		if !ok {
			return configError(fmt.Sprintf("unknown override --%s", o.Key))
		}
		cfg.overrides = append(cfg.overrides, appliedOverride{o, path, *field, cfg.Provenance[path]})
		*field = o.Value
		cfg.Provenance[path] = "flag:" + flag
		if o.Step == IntakeRow {
			continue
		}
		if o.Key == "provider" && row.Fallback.Provider == o.Value {
			orig := configured[o.Step]
			fb := "steps." + o.Step + ".fallback"
			swap := fallbackSwap{o.Step, Fallback{orig.Provider, orig.Model, orig.Effort}, row.Fallback, cfg.Provenance[fb]}
			row.Fallback = swap.to
			cfg.swaps = append(cfg.swaps, swap)
			cfg.Provenance[fb] = "flag:" + flag
		}
		cfg.Steps[o.Step] = row
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

func Create(homeDir string) (string, error) {
	path := filepath.Join(homeDir, ".config", "r-loop", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("%s already exists", path)
	}
	if err != nil {
		return "", err
	}
	if _, err := f.Write(defaultsYAML); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}
