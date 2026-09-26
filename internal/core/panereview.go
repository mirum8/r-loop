package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	paneReviewStartWait  = 2 * time.Minute
	paneReviewTypedWait  = 5 * time.Second
	paneReviewSubmitWait = 3 * time.Second
)

const paneReviewPresses = 5

var paneReviewFailures = []string{"Review was interrupted", "Reviewer failed to output a response"}

func (h ReviewHalf) paneReviews(ctx context.Context, worker *Session, runs []*reviewerRun, failed func(*reviewerRun, *reviewerFail) Outcome) Outcome {
	sm := h.Sessions
	var pane []int
	for i, r := range runs {
		if r.fail == nil && r.args.ReviewDone != "" && r.s.Ref.Kind.Prompt == "review" {
			pane = append(pane, i)
		}
	}
	for _, i := range pane {
		if err := h.event(worker, "review-running", map[string]string{"reviewer": runs[i].s.Reviewer}); err != nil {
			return sm.fail(worker, "record: "+err.Error())
		}
	}
	errs := make([]error, len(runs))
	recorded := make([]error, len(runs))
	presses := make([]int, len(runs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, i := range pane {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if presses[i], errs[i] = h.runPaneReview(ctx, runs[i].s, runs[i].args); errs[i] != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			recorded[i] = h.event(worker, "review-ran", map[string]string{"reviewer": runs[i].s.Reviewer, "presses": strconv.Itoa(presses[i])})
		}()
	}
	wg.Wait()
	for _, i := range pane {
		r := runs[i]
		if errs[i] != nil {
			if out := failed(r, paneFail(r.s, errs[i])); out.State == StepFailed {
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

type paneStuck struct{ msg string }

func (e *paneStuck) Error() string { return e.msg }

func paneFail(s *Session, err error) *reviewerFail {
	var stuck *paneStuck
	return &reviewerFail{reason: "reviewer " + s.Reviewer + ": " + err.Error(), pane: errors.As(err, &stuck)}
}

func (h ReviewHalf) runPaneReview(ctx context.Context, s *Session, a ProviderArgs) (int, error) {
	if err := h.Sessions.Host.SendText(s.Agent, a.Review); err != nil {
		return 0, err
	}
	presses, err := h.submit(ctx, s.Agent, a)
	if err != nil {
		return presses, err
	}
	return presses, h.awaitPane(ctx, s, a)
}

func (h ReviewHalf) submit(ctx context.Context, agent string, a ProviderArgs) (int, error) {
	text := strings.Join(strings.Fields(a.Review), "")
	typed := func(screen string) bool { return strings.Contains(strings.Join(strings.Fields(screen), ""), text) }
	started := func(screen string) bool { return strings.Contains(screen, a.ReviewStart) }
	screen, shown, err := h.pollScreen(ctx, agent, paneReviewTypedWait, func(screen string) bool { return typed(screen) || started(screen) })
	if err != nil {
		return 0, err
	}
	if !shown {
		return 0, &paneStuck{fmt.Sprintf("review text did not reach the composer within %s", paneReviewTypedWait)}
	}
	n := 0
	for n < paneReviewPresses && !started(screen) {
		if err := h.Sessions.Host.SendKeys(agent, "enter"); err != nil {
			return n, err
		}
		n++
		var taken bool
		if screen, taken, err = h.pollScreen(ctx, agent, paneReviewSubmitWait, func(screen string) bool { return started(screen) || !typed(screen) }); err != nil || taken {
			return n, err
		}
	}
	return n, nil
}

func (h ReviewHalf) awaitPane(ctx context.Context, s *Session, a ProviderArgs) error {
	if err := h.awaitScreen(ctx, s.Agent, a.ReviewStart, paneReviewStartWait, "start"); err != nil {
		return err
	}
	return h.awaitScreen(ctx, s.Agent, a.ReviewDone, s.Ref.Kind.Row.Timeout, "finish")
}

func (h ReviewHalf) awaitScreen(ctx context.Context, agent, marker string, limit time.Duration, what string) error {
	_, seen, err := h.pollScreen(ctx, agent, limit, func(screen string) bool { return strings.Contains(screen, marker) })
	if err != nil {
		return err
	}
	if !seen {
		return &paneStuck{fmt.Sprintf("review did not %s within %s", what, limit)}
	}
	return nil
}

func (h ReviewHalf) pollScreen(ctx context.Context, agent string, limit time.Duration, done func(string) bool) (string, bool, error) {
	poll := h.Sessions.Poll
	if poll <= 0 {
		poll = defaultPoll
	}
	deadline := time.Now().Add(limit)
	for {
		screen, err := h.Sessions.Host.Screen(agent)
		if err != nil {
			return "", false, fmt.Errorf("review: agent gone: %w", err)
		}
		for _, f := range paneReviewFailures {
			if strings.Contains(screen, f) {
				return screen, false, &paneStuck{"review failed: " + f}
			}
		}
		if done(screen) {
			return screen, true, nil
		}
		if limit > 0 && !time.Now().Before(deadline) {
			return screen, false, nil
		}
		select {
		case <-ctx.Done():
			return screen, false, ctx.Err()
		case <-time.After(poll):
		}
	}
}
