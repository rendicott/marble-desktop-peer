package queue

import (
	"context"
	"sync"
)

// Queue serializes actions (depth 1).
type Queue struct {
	mu     sync.Mutex
	busy   bool
	cancel context.CancelFunc
}

func New() *Queue { return &Queue{} }

// Run runs fn exclusively. If already busy, returns false.
func (q *Queue) Run(parent context.Context, fn func(ctx context.Context) error) error {
	q.mu.Lock()
	if q.busy {
		q.mu.Unlock()
		return errBusy
	}
	ctx, cancel := context.WithCancel(parent)
	q.busy = true
	q.cancel = cancel
	q.mu.Unlock()

	defer func() {
		cancel()
		q.mu.Lock()
		q.busy = false
		q.cancel = nil
		q.mu.Unlock()
	}()
	return fn(ctx)
}

func (q *Queue) Cancel() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.cancel != nil {
		q.cancel()
	}
}

func (q *Queue) Busy() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.busy
}

type busyErr struct{}

func (busyErr) Error() string { return "peer busy (action queue depth 1)" }

var errBusy = busyErr{}
