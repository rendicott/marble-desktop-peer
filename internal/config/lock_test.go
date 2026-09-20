package config

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// The run lock is what stops two peers thrashing one computer_id; it must hold
// across separate handles, release on close, and leave the PID readable so the
// loser can name the winner (on Windows the lock is a mandatory byte range, so
// that last part is not automatic).
func TestTryFlockExclusiveAndReadablePID(t *testing.T) {
	t.Setenv("MARBLE_PEER_HOME", t.TempDir())
	if err := EnsureHome(); err != nil {
		t.Fatal(err)
	}

	first, err := tryFlock()
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if second, err := tryFlock(); err == nil {
		second.Close()
		t.Fatal("second lock succeeded while the first is held")
	}

	b, err := os.ReadFile(LockPath())
	if err != nil {
		t.Fatalf("lock file unreadable while held: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != strconv.Itoa(os.Getpid()) {
		t.Fatalf("lock file pid = %q, want %d", got, os.Getpid())
	}

	first.Close()
	third, err := tryFlock()
	if err != nil {
		t.Fatalf("lock not released on close: %v", err)
	}
	third.Close()
}

func TestPidAlive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Fatal("own pid reported dead")
	}
	if pidAlive(0) || pidAlive(-1) {
		t.Fatal("non-positive pid reported alive")
	}
}

func TestLockHintsMentionPIDs(t *testing.T) {
	t.Setenv("MARBLE_PEER_HOME", t.TempDir())
	h := runningHint([]int{4242, 77})
	if !strings.Contains(h, "4242") || !strings.Contains(h, "77") {
		t.Fatalf("hint missing pids: %s", h)
	}
	if staleLockHint() == "" {
		t.Fatal("empty stale-lock hint")
	}
}
