package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

var excludeTimeout = time.Minute

var appendMu sync.Mutex

var ErrMeta = errors.New("meta.json")

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
	Branch  string    `json:"branch,omitempty"`
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
	mb, err := json.Marshal(meta{Todo: m.Todo, Branch: m.Branch, Started: m.Started})
	if err != nil {
		return "", err
	}
	for _, f := range recordFiles {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
			return "", err
		}
	}
	if err := writeAtomic(dir, "meta.json", mb); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) Append(runID string, rec core.Record) error {
	appendMu.Lock()
	defer appendMu.Unlock()
	name, ok := fileForKind[rec.Kind]
	if !ok {
		return fmt.Errorf("unknown record kind %q", rec.Kind)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir(runID), name)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_RDWR, 0)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if info.Size() > 0 {
		var last [1]byte
		if _, err := f.ReadAt(last[:], info.Size()-1); err != nil {
			f.Close()
			return err
		}
		if last[0] != '\n' {
			b, err := os.ReadFile(path)
			if err != nil {
				f.Close()
				return err
			}
			i := bytes.LastIndexByte(b, '\n')
			var tail core.Record
			if json.Unmarshal(legacyPhase(b[i+1:]), &tail) == nil {
				line = append([]byte{'\n'}, line...)
			} else if err := f.Truncate(int64(i + 1)); err != nil {
				f.Close()
				return err
			}
		}
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
		return st, fmt.Errorf("%w: %w", ErrMeta, err)
	}
	var m meta
	if err := json.Unmarshal(mb, &m); err != nil {
		return st, fmt.Errorf("%w: %w", ErrMeta, err)
	}
	st.Todo, st.Branch, st.Started = m.Todo, m.Branch, m.Started
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
	if err := st.Apply(rec); err != nil {
		return err
	}
	if rec.Kind == core.RecordStep {
		*order = append(*order, *rec.Step)
	}
	return nil
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
	return writeAtomic(s.runs, "current", fmt.Appendf(nil, "%s %d\n", runID, pid))
}

func writeAtomic(dir, name string, data []byte) error {
	tmp, err := os.CreateTemp(dir, "."+name+"-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	if werr == nil {
		werr = tmp.Sync()
	}
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), filepath.Join(dir, name))
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

func (s *Store) ClearCurrent(runID string, pid int) error {
	id, currentPID, ok := s.Current()
	if !ok || id != runID || currentPID != pid {
		return nil
	}
	err := os.Remove(s.currentPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

type LockedError struct {
	RunID string
	PID   int
}

func (e *LockedError) Error() string {
	if e.RunID != "" {
		return fmt.Sprintf("run %s is live in pid %d", e.RunID, e.PID)
	}
	return "another r-loop run is live in this repository"
}

type Lock struct {
	s    *Store
	gate *os.File
	f    *os.File
}

func (s *Store) flockFile(name string, how int) (*os.File, error) {
	if err := os.MkdirAll(s.runs, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.runs, name), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func (s *Store) Lock() (*Lock, error) {
	gate, err := s.flockFile("gate", syscall.LOCK_EX)
	if err != nil {
		return nil, fmt.Errorf("run lock: %w", err)
	}
	f, err := s.flockFile("lock", syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		id, pid, ok := s.Current()
		gate.Close()
		if ok {
			return nil, &LockedError{RunID: id, PID: pid}
		}
		return nil, &LockedError{}
	}
	if err != nil {
		gate.Close()
		return nil, fmt.Errorf("run lock: %w", err)
	}
	return &Lock{s: s, gate: gate, f: f}, nil
}

func (l *Lock) Publish() {
	if l == nil || l.gate == nil {
		return
	}
	l.gate.Close()
	l.gate = nil
}

func (l *Lock) Release(runID string, pid int) error {
	if l == nil || l.f == nil {
		return nil
	}
	if l.gate == nil {
		gate, err := l.s.flockFile("gate", syscall.LOCK_EX)
		if err != nil {
			return fmt.Errorf("run lock: %w", err)
		}
		l.gate = gate
	}
	err := l.s.ClearCurrent(runID, pid)
	if closeErr := l.f.Close(); err == nil {
		err = closeErr
	}
	if closeErr := l.gate.Close(); err == nil {
		err = closeErr
	}
	l.f = nil
	l.gate = nil
	return err
}

func (s *Store) Live() (runID string, pid int, ok bool) {
	gate, err := os.Open(filepath.Join(s.runs, "gate"))
	if err != nil {
		return "", 0, false
	}
	defer gate.Close()
	if err := syscall.Flock(int(gate.Fd()), syscall.LOCK_SH); err != nil {
		return "", 0, false
	}
	f, err := os.Open(filepath.Join(s.runs, "lock"))
	if err != nil {
		return "", 0, false
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
	if err == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return "", 0, false
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		return "", 0, false
	}
	return s.Current()
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
	ctx, cancel := context.WithTimeout(context.Background(), excludeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoRoot, "rev-parse", "--git-common-dir")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("git rev-parse --git-common-dir: timed out after %s: %w", excludeTimeout, context.DeadlineExceeded)
		}
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
