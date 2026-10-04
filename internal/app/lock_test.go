package app

import (
	"strings"
	"testing"
	"time"

	"github.com/rendicott/marble-desktop-peer/internal/config"
)

func testApp(t *testing.T, urls ...string) (*App, []*link) {
	t.Helper()
	t.Setenv("MARBLE_PEER_HOME", t.TempDir())
	var cfg config.File
	toks := map[string]string{}
	for _, u := range urls {
		cfg.Harnesses = append(cfg.Harnesses, config.Harness{URL: u})
		toks[u] = "tok-" + u
	}
	a := New(cfg, toks)
	for _, l := range a.links {
		l.proto = 2
		l.instance = "boot-1"
	}
	return a, a.Links()
}

func TestLockSingleHolder(t *testing.T) {
	a, ls := testApp(t, "http://h1", "http://h2")
	h1, h2 := ls[0], ls[1]

	if err := a.authorizeAction(h1); err == nil {
		t.Fatal("v2 harness ran an action without the lock")
	}
	if err := a.acquireLock(h1, false); err != nil {
		t.Fatal(err)
	}
	if err := a.acquireLock(h1, false); err != nil {
		t.Fatalf("re-acquire by holder: %v", err)
	}
	if err := a.authorizeAction(h1); err != nil {
		t.Fatalf("holder refused: %v", err)
	}
	err := a.acquireLock(h2, false)
	if err == nil || !strings.Contains(err.Error(), "http://h1") {
		t.Fatalf("second harness acquire: %v", err)
	}
	if err := a.authorizeAction(h2); err == nil {
		t.Fatal("non-holder ran an action")
	}
	if a.lockHeldByOrFree(h2) {
		t.Fatal("non-holder may cancel")
	}

	a.releaseLock(h2, "not holder") // no-op
	if !a.LockInfo().Held {
		t.Fatal("non-holder released the lock")
	}
	a.releaseLock(h1, "done")
	if a.LockInfo().Held {
		t.Fatal("lock still held after release")
	}
	if err := a.acquireLock(h2, false); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

func TestClearLock(t *testing.T) {
	a, ls := testApp(t, "http://h1", "http://h2")
	if err := a.acquireLock(ls[0], false); err != nil {
		t.Fatal(err)
	}
	prev := a.ClearLock()
	if !prev.Held || prev.Holder != "http://h1" {
		t.Fatalf("ClearLock returned %+v", prev)
	}
	if a.LockInfo().Held {
		t.Fatal("lock held after clear")
	}
	if err := a.acquireLock(ls[1], false); err != nil {
		t.Fatalf("acquire after clear: %v", err)
	}
}

func TestLockSurvivesReconnectButNotRestart(t *testing.T) {
	a, ls := testApp(t, "http://h1")
	h1 := ls[0]
	if err := a.acquireLock(h1, false); err != nil {
		t.Fatal(err)
	}
	a.onHarnessDisconnect(h1)
	a.onHarnessHello(h1) // same instance: network blip
	if !a.LockInfo().Held {
		t.Fatal("lock dropped on reconnect from same harness process")
	}
	h1.instance = "boot-2"
	a.onHarnessHello(h1)
	if a.LockInfo().Held {
		t.Fatal("lock kept across harness restart")
	}
}

func TestLegacyHarnessImplicitLock(t *testing.T) {
	a, ls := testApp(t, "http://old", "http://new")
	old, nw := ls[0], ls[1]
	old.proto = 1

	if err := a.authorizeAction(old); err != nil {
		t.Fatalf("legacy action: %v", err)
	}
	if info := a.LockInfo(); !info.Held || !info.Implicit {
		t.Fatalf("legacy action did not take implicit lock: %+v", info)
	}
	if err := a.acquireLock(nw, false); err == nil {
		t.Fatal("v2 harness took a lock in active legacy use")
	}
	// Idle implicit lock is reclaimable.
	a.lockMu.Lock()
	a.lockLastUse = time.Now().Add(-implicitLockIdle - time.Second)
	a.lockMu.Unlock()
	if err := a.acquireLock(nw, false); err != nil {
		t.Fatalf("idle implicit lock not reclaimed: %v", err)
	}
	// And a legacy disconnect only drops its own implicit lock.
	a.onHarnessDisconnect(old)
	if !a.LockInfo().Held {
		t.Fatal("legacy disconnect released someone else's lock")
	}
}
