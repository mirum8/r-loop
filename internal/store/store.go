package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"r-loop/internal/core"
)

const (
	eventsFile    = "events.jsonl"
	questionsFile = "questions.jsonl"
	signalsFile   = "signals.jsonl"
	remediesFile  = "remedies.jsonl"
)

var legacyPhaseRe = regexp.MustCompile(`"Phase":(\d+)`)

var recordFiles = []string{eventsFile, questionsFile, signalsFile, remediesFile}

var fileForKind = map[string]string{
	core.RecordStep:     eventsFile,
	core.RecordRun:      eventsFile,
	core.RecordLanding:  eventsFile,
	core.RecordEvent:    eventsFile,
	core.RecordQuestion: questionsFile,
	core.RecordSignal:   signalsFile,
	core.RecordRemedy:   remediesFile,
}

type Store struct {
	runs string
	now  func() time.Time
}

type meta struct {
	Todo    string    `json:"todo"`
	Started time.Time `json:"started"`
}

func New(repoRoot string) *Store {
	return &Store{runs: filepath.Join(repoRoot, ".r-loop", "runs"), now: time.Now}
}

func (s *Store) Dir(runID string) string {
	return filepath.Join(s.runs, runID)
}

func (s *Store) Create(m core.RunMeta) (string, error) {
	if err := os.MkdirAll(s.runs, 0o755); err != nil {
		return "", err
	}
	base := s.now().Format("20060102-150405")
	id := base
	for n := 2; ; n++ {
		err := os.Mkdir(s.Dir(id), 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
	dir := s.Dir(id)
	if err := os.WriteFile(filepath.Join(dir, "config.resolved.yaml"), m.ResolvedConfig, 0o644); err != nil {
		return "", err
	}
	mb, err := json.Marshal(meta{Todo: m.Todo, Started: m.Started})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), mb, 0o644); err != nil {
		return "", err
	}
	for _, f := range recordFiles {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			return "", err
		}
	}
	return id, nil
}

func (s *Store) Append(runID string, rec core.Record) error {
	name, ok := fileForKind[rec.Kind]
	if !ok {
		return fmt.Errorf("unknown record kind %q", rec.Kind)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir(runID), name), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s *Store) Load(runID string) (core.RunState, error) {
	st := core.RunState{ID: runID, Status: core.RunCreated, Steps: map[core.StepKey]core.StepState{}}
	mb, err := os.ReadFile(filepath.Join(s.Dir(runID), "meta.json"))
	if err != nil {
		return st, err
	}
	var m meta
	if err := json.Unmarshal(mb, &m); err != nil {
		return st, fmt.Errorf("meta.json: %w", err)
	}
	st.Todo, st.Started = m.Todo, m.Started
	var order []core.StepKey
	for _, name := range recordFiles {
		recs, warning, err := s.readRecords(runID, name)
		if err != nil {
			return st, err
		}
		if warning != "" {
			st.Warnings = append(st.Warnings, warning)
		}
		for _, rec := range recs {
			if err := apply(&st, &order, rec); err != nil {
				return st, fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	st.LastStep = lastStep(st.Steps, order)
	return st, nil
}

func (s *Store) readRecords(runID, name string) ([]core.Record, string, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir(runID), name))
	if err != nil {
		return nil, "", err
	}
	lines := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
	var recs []core.Record
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		var rec core.Record
		if err := json.Unmarshal(legacyPhase(line), &rec); err != nil {
			if i == len(lines)-1 && !bytes.HasSuffix(b, []byte("\n")) {
				return recs, fmt.Sprintf("%s: skipped truncated last line %d", name, i+1), nil
			}
			return nil, "", fmt.Errorf("%s line %d: %w", name, i+1, err)
		}
		recs = append(recs, rec)
	}
	return recs, "", nil
}

func legacyPhase(line []byte) []byte {
	return legacyPhaseRe.ReplaceAllFunc(line, func(m []byte) []byte {
		n := m[len(`"Phase":`):]
		if string(n) == "0" {
			n = nil
		}
		return []byte(`"Phase":"` + string(n) + `"`)
	})
}

func apply(st *core.RunState, order *[]core.StepKey, rec core.Record) error {
	switch rec.Kind {
	case core.RecordStep:
		if rec.Step == nil {
			return errors.New("step record without a step")
		}
		st.Steps[*rec.Step] = rec.State
		st.Span(*rec.Step, rec.State, rec.At)
		*order = append(*order, *rec.Step)
	case core.RecordRun:
		st.Status = rec.Run
	case core.RecordLanding:
		if rec.Landing != nil {
			st.Landed = append(st.Landed, *rec.Landing)
		}
	case core.RecordEvent:
		if rec.Event != nil {
			st.Events = append(st.Events, *rec.Event)
		}
	case core.RecordQuestion:
		if rec.Question != nil {
			st.Questions = upsertQuestion(st.Questions, *rec.Question)
		}
	case core.RecordSignal:
		if rec.Signal != nil {
			st.Signals = append(st.Signals, *rec.Signal)
		}
	case core.RecordRemedy:
		if rec.Remedy != nil {
			st.Remedies = append(st.Remedies, *rec.Remedy)
		}
	default:
		return fmt.Errorf("unknown record kind %q", rec.Kind)
	}
	return nil
}

func upsertQuestion(qs []core.Question, q core.Question) []core.Question {
	for i := range qs {
		if qs[i].ID == q.ID {
			qs[i] = q
			return qs
		}
	}
	return append(qs, q)
}

func lastStep(steps map[core.StepKey]core.StepState, order []core.StepKey) *core.StepKey {
	if len(order) == 0 {
		return nil
	}
	for i := len(order) - 1; i >= 0; i-- {
		if st := steps[order[i]]; st != core.StepOK && st != core.StepFailed {
			key := order[i]
			return &key
		}
	}
	key := order[len(order)-1]
	return &key
}

func (s *Store) currentPath() string {
	return filepath.Join(s.runs, "current")
}

func (s *Store) Current() (string, int, bool) {
	b, err := os.ReadFile(s.currentPath())
	if err != nil {
		return "", 0, false
	}
	fields := strings.Fields(string(b))
	if len(fields) != 2 {
		return "", 0, false
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil {
		return "", 0, false
	}
	return fields[0], pid, true
}

func (s *Store) SetCurrent(runID string, pid int) error {
	if err := os.MkdirAll(s.runs, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.runs, ".current-*")
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(tmp, "%s %d\n", runID, pid)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), s.currentPath())
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

func (s *Store) ClearCurrent() error {
	err := os.Remove(s.currentPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) MarkAbort(runID string) error {
	return os.WriteFile(filepath.Join(s.Dir(runID), "abort"), nil, 0o644)
}

func (s *Store) ClearAbort(runID string) error {
	err := os.Remove(filepath.Join(s.Dir(runID), "abort"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) Aborted(runID string) bool {
	_, err := os.Stat(filepath.Join(s.Dir(runID), "abort"))
	return err == nil
}

func EnsureExcluded(repoRoot string) error {
	out, err := exec.Command("git", "-C", repoRoot, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return fmt.Errorf("git rev-parse --git-common-dir: %w", err)
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(repoRoot, common)
	}
	path := filepath.Join(common, "info", "exclude")
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	present := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		present[strings.TrimSpace(line)] = true
	}
	var add string
	for _, line := range []string{".r-loop/runs/", ".r-loop/wt/"} {
		if !present[line] {
			add += line + "\n"
		}
	}
	if add == "" {
		return nil
	}
	if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
		add = "\n" + add
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(add); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
