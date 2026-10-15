package core

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type configRig struct {
	shared *callLog
	store  *fakeStore
	face   *fakeFace
	repo   *fakeRepo
	writes []string
	set    *ConfigSetter
}

func newConfigRig(writeErr error) *configRig {
	shared := &callLog{}
	r := &configRig{shared: shared}
	r.store = &fakeStore{callLog: callLog{Shared: shared}}
	r.face = &fakeFace{callLog: callLog{Shared: shared}}
	r.repo = &fakeRepo{callLog: callLog{Shared: shared}, SHA: "sha"}
	r.set = &ConfigSetter{Store: r.store, Face: r.face, Repo: r.repo, RunID: "run-1", Write: func(key, value string) error {
		shared.record("Write %s=%s", key, value)
		r.writes = append(r.writes, key+"="+value)
		return writeErr
	}}
	return r
}

func (r *configRig) events(kind string) []Event {
	var out []Event
	for _, rec := range r.store.Records["run-1"] {
		if rec.Event != nil && rec.Event.Kind == kind {
			out = append(out, *rec.Event)
		}
	}
	return out
}

func TestSetConfigRefusesTheWatchdogsOwnPowers(t *testing.T) {
	for _, key := range []string{"watchdog.allow", "watchdog", "unattended.allow", "watchdog.maxRestarts"} {
		t.Run(key, func(t *testing.T) {
			r := newConfigRig(nil)

			decision, reason := r.set.Set(key, "x", "because")

			if decision != "refused" || !strings.Contains(reason, "not yours to set") {
				t.Errorf("decision %q reason %q", decision, reason)
			}
			if len(r.writes) != 0 || len(r.store.Records["run-1"]) != 0 || len(r.repo.CommitPaths) != 0 {
				t.Errorf("writes %v records %v commits %v", r.writes, r.store.Records, r.repo.CommitPaths)
			}
		})
	}
}

func TestSetConfigRefusesAnEmptyKeyOrReason(t *testing.T) {
	for name, in := range map[string][2]string{"key": {"", "why"}, "why": {"steps.plan.model", " "}} {
		t.Run(name, func(t *testing.T) {
			r := newConfigRig(nil)

			decision, _ := r.set.Set(in[0], "x", in[1])

			if decision != "refused" || len(r.writes) != 0 {
				t.Errorf("decision %q writes %v", decision, r.writes)
			}
		})
	}
}

func TestSetConfigRecordsTheChangeBeforeWritingItAndCommitsTheProjectConfig(t *testing.T) {
	r := newConfigRig(nil)
	r.repo.DirtyFiles = []string{".r-loop/config.yaml"}

	decision, reason := r.set.Set("steps.implement.model", "gpt-5.5", "gpt-5 is retired")

	if decision != "authorised" || reason != "" {
		t.Fatalf("decision %q reason %q", decision, reason)
	}
	calls := r.shared.Calls()
	appended := slices.Index(calls, "Store.Append run-1 event")
	wrote := slices.Index(calls, "Write steps.implement.model=gpt-5.5")
	if appended < 0 || wrote < 0 || appended > wrote {
		t.Errorf("calls %v: want the event appended before the write", calls)
	}
	got := r.events(EventConfigSet)
	want := map[string]string{"key": "steps.implement.model", "value": "gpt-5.5", "why": "gpt-5 is retired"}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Fields, want) {
		t.Errorf("events %+v", got)
	}
	if !slices.Contains(calls, `Repo.Commit "chore(r-loop): set steps.implement.model"`) || !reflect.DeepEqual(r.repo.CommitPaths, [][]string{{".r-loop/config.yaml"}}) {
		t.Errorf("calls %v commits %v", calls, r.repo.CommitPaths)
	}
	if !slices.ContainsFunc(r.face.Events, func(ev Event) bool { return ev.Kind == EventConfigSet }) {
		t.Errorf("face events %+v", r.face.Events)
	}
}

func TestSetConfigCommitsNothingWhenTheProjectConfigIsClean(t *testing.T) {
	r := newConfigRig(nil)
	r.repo.DirtyFiles = []string{"src/a.go"}

	decision, _ := r.set.Set("land.gateTimeout", "30m", "the suite is slow")

	if decision != "authorised" || len(r.repo.CommitPaths) != 0 {
		t.Errorf("decision %q commits %v", decision, r.repo.CommitPaths)
	}
}

func TestSetConfigRefusesAValueTheDriverCouldNotApply(t *testing.T) {
	r := newConfigRig(errors.New(".r-loop/config.yaml:3: unknown key steps.implement.modle"))

	decision, reason := r.set.Set("steps.implement.modle", "x", "typo")

	if decision != "refused" || !strings.Contains(reason, "unknown key") {
		t.Errorf("decision %q reason %q", decision, reason)
	}
	if failed := r.events(EventConfigSetFailed); len(failed) != 1 || failed[0].Fields["key"] != "steps.implement.modle" {
		t.Errorf("failed events %+v", failed)
	}
	if len(r.repo.CommitPaths) != 0 {
		t.Errorf("commits %v", r.repo.CommitPaths)
	}
}

func TestSetConfigRunsOneSetAtATime(t *testing.T) {
	r := newConfigRig(nil)
	var inFlight, overlapped atomic.Int32
	r.set.Write = func(key, value string) error {
		if inFlight.Add(1) > 1 {
			overlapped.Add(1)
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return nil
	}
	r.repo.DirtyFiles = []string{ProjectConfig}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.set.Set(fmt.Sprintf("steps.s%d.model", i), "opus", "because")
		}()
	}
	wg.Wait()

	if n := overlapped.Load(); n != 0 {
		t.Errorf("%d sets overlapped", n)
	}
	if got := len(r.events(EventConfigSet)); got != 8 {
		t.Errorf("%d config-set events", got)
	}
}
