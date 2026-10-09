package queue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunRejectsWhenBusy(t *testing.T) {
	q := New()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- q.Run(context.Background(), func(ctx context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	err := q.Run(context.Background(), func(ctx context.Context) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "peer busy") {
		t.Fatalf("second Run err = %v, want peer busy", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if q.Busy() {
		t.Fatal("slot still busy after the action returned")
	}
}

func TestCancelUnblocksHandler(t *testing.T) {
	prev := abandonAfter
	abandonAfter = time.Second
	t.Cleanup(func() { abandonAfter = prev })

	q := New()
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	got := make(chan error, 1)
	go func() {
		got <- q.Run(ctx, func(ctx context.Context) error {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	<-entered
	start := time.Now()
	cancel()
	err := <-got
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("handler that honors cancel took %s", time.Since(start))
	}
	if q.Busy() {
		t.Fatal("slot still busy")
	}
}

func TestAbandonReleasesStuckHandler(t *testing.T) {
	prev := abandonAfter
	abandonAfter = 40 * time.Millisecond
	t.Cleanup(func() { abandonAfter = prev })

	q := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	got := make(chan error, 1)
	go func() {
		got <- q.Run(ctx, func(ctx context.Context) error {
			close(entered)
			select {} // ignores cancel, as a blocked syscall would
		})
	}()
	<-entered
	if !q.Busy() {
		t.Fatal("expected busy while the handler is stuck")
	}
	cancel()
	select {
	case err := <-got:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stuck handler kept the slot")
	}
	if q.Busy() {
		t.Fatal("slot still busy after abandon")
	}
	if err := q.Run(context.Background(), func(ctx context.Context) error { return nil }); err != nil {
		t.Fatalf("next action: %v", err)
	}
}
