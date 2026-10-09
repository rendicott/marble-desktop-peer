package queue

import (
	"context"
	"log"
	"sync"
	"time"
)

// abandonAfter is how long Run waits, after cancel, for the handler to
// return before releasing the slot anyway. A handler blocked in a call that
// ignores context would otherwise leave busy true until the process restarts.
// Longer than cmdx.WaitDelay so a command that does honor cancel finishes
// first and is not overlapped with the next action.
var abandonAfter = 5 * time.Second

// Queue serializes actions (depth 1).
type Queue struct {
	mu     sync.Mutex
	busy   bool
	cancel context.CancelFunc
	gen    uint64
}

func New() *Queue { return &Queue{} }

// Run runs fn exclusively. A second call while one is in progress returns
// errBusy. When parent is cancelled, Run waits abandonAfter for fn to return,
// then releases the slot even if fn is still blocked.
func (q *Queue) Run(parent context.Context, fn func(ctx context.Context) error) error {
	q.mu.Lock()
	if q.busy {
		q.mu.Unlock()
		return errBusy
	}
	ctx, cancel := context.WithCancel(parent)
	q.gen++
	gen := q.gen
	q.busy = true
	q.cancel = cancel
	q.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- fn(ctx)
	}()

	release := func() {
		cancel()
		q.mu.Lock()
		// A newer Run owns the slot if this action was already abandoned.
		if q.gen == gen {
			q.busy = false
			q.cancel = nil
		}
		q.mu.Unlock()
	}

	select {
	case err := <-done:
		release()
		return err
	case <-ctx.Done():
		select {
		case err := <-done:
			release()
			return err
		case <-time.After(abandonAfter):
			log.Printf("queue: action did not return after cancel; releasing slot")
			release()
			return ctx.Err()
		}
	}
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
