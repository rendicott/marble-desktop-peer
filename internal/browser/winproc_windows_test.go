//go:build windows

package browser

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Real robocopy against a synthetic profile. The source path has a space (like
// "User Data") because argument quoting is where robocopy invocations break.
func TestSyncProfileRobocopy(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "User Data")
	dst := filepath.Join(root, "mirror")

	writeFile(t, filepath.Join(src, "Local State"), "state")
	writeFile(t, filepath.Join(src, "Default", "Network", "Cookies"), "cookies")
	writeFile(t, filepath.Join(src, "Default", "Preferences"), "prefs")
	writeFile(t, filepath.Join(src, "Default", "Cache", "junk"), "x")
	writeFile(t, filepath.Join(src, "Default", "Code Cache", "js", "junk"), "x")
	writeFile(t, filepath.Join(src, "Default", "GPUCache", "junk"), "x")
	writeFile(t, filepath.Join(src, "Default", "Service Worker", "CacheStorage", "junk"), "x")
	writeFile(t, filepath.Join(src, "SingletonLock"), "x")
	writeFile(t, filepath.Join(src, "lockfile"), "x")
	writeFile(t, filepath.Join(dst, "stale.txt"), "left over from an older sync")

	if err := SyncUserProfile(src, dst); err != nil {
		t.Fatalf("SyncUserProfile: %v", err)
	}
	for _, want := range []string{"Local State", `Default\Network\Cookies`, `Default\Preferences`} {
		if !exists(filepath.Join(dst, want)) {
			t.Errorf("mirror missing %s", want)
		}
	}
	for _, unwanted := range []string{`Default\Cache`, `Default\Code Cache`, `Default\GPUCache`,
		`Default\Service Worker\CacheStorage`, "SingletonLock", "lockfile", "stale.txt"} {
		if exists(filepath.Join(dst, unwanted)) {
			t.Errorf("mirror should not contain %s", unwanted)
		}
	}
	// A second sync is incremental and idempotent.
	if err := SyncUserProfile(src, dst); err != nil {
		t.Fatalf("second sync: %v", err)
	}
}

// Launches a real, isolated Chrome with CDP and drives the Windows-specific
// process helpers against it. Opt-in (opens a browser window): CI sets
// MARBLE_PEER_TEST_CHROME=1 in an informational step.
func TestWindowsChromeLifecycle(t *testing.T) {
	if os.Getenv("MARBLE_PEER_TEST_CHROME") != "1" {
		t.Skip("set MARBLE_PEER_TEST_CHROME=1 to launch a real Chrome")
	}
	if findChrome() == "" {
		t.Skip("no Chrome/Edge installed")
	}
	t.Setenv("MARBLE_PEER_HOME", t.TempDir())
	t.Setenv("MARBLE_PEER_CDP_PORT", "")

	m := NewWithOptions(Options{Mode: ModeMarble})
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	defer func() { m.Stop(true); killProfileChromeWindows(m.profile) }()

	res, err := m.Ensure(ctx, false)
	if err != nil || !res.OK || res.Port == 0 {
		t.Fatalf("Ensure: err=%v res=%+v", err, res)
	}
	t.Logf("launched: %+v", res)
	if !probeCDP(res.Port) {
		t.Fatalf("CDP not answering on port %d", res.Port)
	}

	found := false
	for _, p := range scanChromeDebugPortsWindows() {
		if p == res.Port {
			found = true
		}
	}
	if !found {
		t.Errorf("PowerShell process scan did not report debug port %d", res.Port)
	}

	killProfileChrome(m.profile)
	deadline := time.Now().Add(20 * time.Second)
	for probeCDP(res.Port) && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	if probeCDP(res.Port) {
		t.Fatalf("Chrome still answering on port %d after killProfileChrome", res.Port)
	}
}
