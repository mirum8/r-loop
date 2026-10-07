package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func (h ReviewHalf) nativeReviews(ctx context.Context, worker *Session, runs []*reviewerRun, failed func(*reviewerRun, *reviewerFail) Outcome) Outcome {
	sm := h.Sessions
	var native []int
	for i, r := range runs {
		if r.fail == nil && execReview(r) {
			native = append(native, i)
		}
	}
	for _, i := range native {
		if err := h.event(worker, "review-running", map[string]string{"reviewer": runs[i].s.Reviewer}); err != nil {
			return sm.fail(worker, "record: "+err.Error())
		}
	}
	errs := make([]error, len(runs))
	recorded := make([]error, len(runs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, i := range native {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if errs[i] = h.runNativeReview(ctx, worker, runs[i].s); errs[i] != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			recorded[i] = h.event(worker, "review-ran", map[string]string{"reviewer": runs[i].s.Reviewer})
		}()
	}
	wg.Wait()
	for _, i := range native {
		r := runs[i]
		if errs[i] != nil {
			if out := failed(r, &reviewerFail{reason: "reviewer " + r.s.Reviewer + ": " + errs[i].Error(), excerpt: execFailedExcerpt(r.s)}); out.State == StepFailed {
				return out
			}
			continue
		}
		if recorded[i] != nil {
			return sm.fail(worker, "record: "+recorded[i].Error())
		}
		r.s.Ref.Vars["ReviewRan"] = true
	}
	return Outcome{}
}

func execReview(r *reviewerRun) bool {
	return r.args.ReviewExec != "" && nativeReview(r.s.Ref.Kind.Prompt)
}

func (h ReviewHalf) runNativeReview(ctx context.Context, worker *Session, s *Session) error {
	command := s.Ref.Vars["ReviewCommand"].(string)
	prefix := "native review `" + command + "`"
	runner := h.Sessions.ReviewRunner
	if runner == nil {
		return errors.New(prefix + ": no review runner")
	}
	out, err := runner.RunReview(ctx, worker.Dir, command, s.Ref.Kind.Row.Timeout)
	if err != nil {
		return errors.New(prefix + ": " + err.Error())
	}
	if strings.TrimSpace(out) == "" {
		return errors.New(prefix + " produced no output")
	}
	if err := os.WriteFile(filepath.Join(s.Ref.Vars["ArtifactsDir"].(string), "native-review.txt"), []byte(out), 0o644); err != nil {
		return errors.New(prefix + ": " + err.Error())
	}
	return nil
}

func execFailedExcerpt(s *Session) string {
	return "The driver ran `" + s.Ref.Vars["ReviewCommand"].(string) + "` itself and it failed before the reviewer pane was prompted: the pane has done nothing, and an addendum never reaches it. Retry runs the same command again."
}
