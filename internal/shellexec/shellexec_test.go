package shellexec

import (
	"context"
	"strings"
	"testing"
)

func TestRunEcho(t *testing.T) {
	res, err := Run(context.Background(), "echo hello-marble-peer", "", 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "hello-marble-peer") {
		t.Fatalf("stdout = %q, want it to contain hello-marble-peer", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0", res.ExitCode)
	}
}

func TestRunNonZeroExitIsNotATransportError(t *testing.T) {
	res, err := Run(context.Background(), "exit 7", "", 0)
	if err != nil {
		t.Fatalf("Run returned transport error for a plain non-zero exit: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("exit_code = %d, want 7", res.ExitCode)
	}
}

func TestRunEmptyCommand(t *testing.T) {
	if _, err := Run(context.Background(), "   ", "", 0); err == nil {
		t.Fatal("expected error for empty/whitespace-only command")
	}
}

func TestRunTimeout(t *testing.T) {
	res, err := Run(context.Background(), "sleep 5", "", 1)
	if err == nil {
		t.Fatal("expected a timeout error for a 1s timeout against a 5s sleep")
	}
	if !res.TimedOut {
		t.Fatalf("Result.TimedOut = false, want true (err=%v)", err)
	}
}
