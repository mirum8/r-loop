package core

import "context"

type singleRunner struct {
	sm     *SessionManager
	review func(ctx context.Context, ref StepRef, worker *Session, obs Observer) Outcome
}

func (r singleRunner) Run(ctx context.Context, ref StepRef, obs Observer) Outcome {
	s, err := r.sm.Spawn(ctx, ref)
	if err != nil {
		return r.sm.Finish(s, Outcome{State: StepFailed, Reason: err.Error(), Session: s})
	}
	out := r.sm.Wait(ctx, s, obs)
	row := ref.Kind.Row
	if out.State == StepOK && len(row.Reviewers) > 0 && row.Rounds > 0 && r.review != nil {
		out = r.review(ctx, ref, s, obs)
	}
	return r.sm.Finish(s, out)
}

func DefaultRunners(sm *SessionManager, kinds []StepKind) map[string]StepRunner {
	runners := make(map[string]StepRunner, len(kinds))
	for _, k := range kinds {
		runners[k.Check] = singleRunner{sm: sm, review: ReviewHalf{Sessions: sm, Store: sm.Store}.Run}
	}
	return runners
}
