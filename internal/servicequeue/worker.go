package servicequeue

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Handler func(context.Context, Item) error

type codedError interface{ Code() string }

type Worker struct {
	queue    *Store
	handlers map[string]Handler
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	err      error
}

func NewWorker(queue *Store, handlers map[string]Handler) (*Worker, error) {
	if queue == nil || len(handlers) != len(Lanes) {
		return nil, errors.New("worker handlers must cover every service work lane")
	}
	selected := make(map[string]Handler, len(handlers))
	for lane, handler := range handlers {
		if !validLane(lane) || handler == nil {
			return nil, errors.New("worker handlers must cover every service work lane")
		}
		selected[lane] = handler
	}
	return &Worker{queue: queue, handlers: selected}, nil
}

func handlerErrorCode(err error) string {
	if coded, ok := err.(codedError); ok {
		code := coded.Code()
		if len(code) > 0 && len(code) <= 64 {
			valid := true
			for _, char := range code {
				if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
					char >= '0' && char <= '9' || char == '_' || char == '-' || char == '.') {
					valid = false
					break
				}
			}
			if valid {
				return code
			}
		}
	}
	return "handler_failed"
}

// ProcessOne claims one durable item, then records exactly one attempt. A
// handler error is recorded without persisting its potentially sensitive text.
func (worker *Worker) ProcessOne(ctx context.Context, lane string) (*Item, error) {
	if !validLane(lane) {
		return nil, errors.New("unknown service work lane")
	}
	claimed, err := worker.queue.Claim(ctx, lane)
	if err != nil || claimed == nil {
		return claimed, err
	}
	var resolved Item
	if err := worker.handlers[lane](ctx, *claimed); err != nil {
		resolved, err = worker.queue.Fail(ctx, claimed.WorkID, handlerErrorCode(err))
	} else {
		resolved, err = worker.queue.Complete(ctx, claimed.WorkID)
	}
	if err != nil {
		return nil, err
	}
	return &resolved, nil
}

func (worker *Worker) setError(err error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.err == nil {
		worker.err = err
	}
}

func (worker *Worker) Err() error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.err
}

func (worker *Worker) Start(ctx context.Context) error {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.done != nil {
		return errors.New("service queue worker is already started")
	}
	ctx, worker.cancel = context.WithCancel(ctx)
	worker.done = make(chan struct{})
	var lanes sync.WaitGroup
	lanes.Add(len(Lanes))
	for _, lane := range Lanes {
		go func(lane string) {
			defer lanes.Done()
			for ctx.Err() == nil {
				item, err := worker.ProcessOne(ctx, lane)
				if err != nil {
					if ctx.Err() == nil {
						worker.setError(err)
					}
					return
				}
				if item == nil {
					select {
					case <-ctx.Done():
						return
					case <-time.After(50 * time.Millisecond):
					}
				}
			}
		}(lane)
	}
	go func() { lanes.Wait(); close(worker.done) }()
	return nil
}

func (worker *Worker) Close(ctx context.Context) error {
	worker.mu.Lock()
	cancel, done := worker.cancel, worker.done
	worker.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return worker.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}
