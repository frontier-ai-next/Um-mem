package main

import (
	"context"
	"errors"
	"time"

	"github.com/frontier-ai-next/Um-mem/internal/engine"
	"github.com/frontier-ai-next/Um-mem/internal/scheduler"
	"github.com/frontier-ai-next/Um-mem/internal/servicepolicy"
	"github.com/frontier-ai-next/Um-mem/internal/servicequeue"
)

type backgroundRuntime struct {
	cancel   context.CancelFunc
	worker   *servicequeue.Worker
	loopDone <-chan error
}

func enqueueDueSchedulerWork(ctx context.Context, scheduled *scheduler.Store,
	work *servicequeue.Store, now time.Time) (int, error) {
	jobs, err := scheduled.RecoverDue(ctx, now)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, job := range jobs {
		lane := "prospective_scheduler"
		if job.Kind == "consolidation_assessment" {
			lane = "consolidation_transition"
		} else if job.Kind != "activation_opportunity" {
			return queued, errors.New("local service scheduler job kind is invalid")
		}
		receipt, err := work.Submit(ctx, lane, job.JobID, job.DecisionRevision, 3)
		if err != nil {
			return queued, err
		}
		if receipt.Status == "backpressured" {
			return queued, errors.New("local service scheduler work queue is backpressured")
		}
		if receipt.Status == "queued" {
			queued++
		}
	}
	return queued, nil
}

func startBackground(ctx context.Context, selected *engine.Engine, policy *servicepolicy.Store,
	scheduled *scheduler.Store, work *servicequeue.Store, onIndexed ...func(string)) (*backgroundRuntime, error) {
	// The caller must already own the published listener registry. Recovering a
	// running item before that point could steal it from the previous listener.
	if err := work.RecoverAbandoned(ctx); err != nil {
		return nil, err
	}
	handlers := map[string]servicequeue.Handler{
		"indexing": func(ctx context.Context, item servicequeue.Item) error {
			if err := selected.RefreshRecordIndexes(ctx, item.WorkRevision); err != nil {
				return err
			}
			if len(onIndexed) != 0 && onIndexed[0] != nil {
				onIndexed[0](item.WorkRevision)
			}
			return nil
		},
		"working_continuity": func(ctx context.Context, _ servicequeue.Item) error {
			health, err := selected.Health(ctx)
			if err != nil {
				return err
			}
			if health["native_ok"] != true {
				return errors.New("background native validation failed")
			}
			return nil
		},
		"prospective_scheduler": func(ctx context.Context, _ servicequeue.Item) error {
			_, err := scheduled.FireDue(ctx, time.Now())
			return err
		},
		"consolidation_transition": func(ctx context.Context, _ servicequeue.Item) error {
			_, err := scheduled.FireDue(ctx, time.Now())
			return err
		},
		"policy_shadow_qualification": func(_ context.Context, _ servicequeue.Item) error {
			bundle, err := policy.Active()
			if err != nil {
				return err
			}
			_, err = policy.SnapshotFor(bundle.RevisionID)
			return err
		},
	}
	worker, err := servicequeue.NewWorker(work, handlers)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	if err := worker.Start(runCtx); err != nil {
		cancel()
		return nil, err
	}
	if _, err := enqueueDueSchedulerWork(runCtx, scheduled, work, time.Now()); err != nil {
		cancel()
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		return nil, errors.Join(err, worker.Close(closeCtx))
	}
	done := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				done <- nil
				return
			case <-ticker.C:
				if err := worker.Err(); err != nil {
					done <- err
					return
				}
				if _, err := enqueueDueSchedulerWork(runCtx, scheduled, work, time.Now()); err != nil {
					done <- err
					return
				}
			}
		}
	}()
	return &backgroundRuntime{cancel: cancel, worker: worker, loopDone: done}, nil
}

func (runtime *backgroundRuntime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	workerErr := runtime.worker.Close(ctx)
	var loopErr error
	select {
	case loopErr = <-runtime.loopDone:
	case <-ctx.Done():
		loopErr = ctx.Err()
	}
	return errors.Join(workerErr, loopErr)
}
