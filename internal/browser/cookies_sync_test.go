package browser

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeSyncTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A real profile with logins has a non-empty Default/Network/Cookies. If a
// mirror sync raced running Chrome and the file was skipped (missing or left
// zero-length), the mirror would launch with the Local State encryption key
// but no logins at all — verifyCookiesSynced must catch that.
func TestVerifyCookiesSyncedMissingInMirror(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSyncTestFile(t, filepath.Join(src, "Default", "Network", "Cookies"), "real cookie data")
	// dst has no Cookies file at all (Chrome held it locked during copy).

	err := verifyCookiesSynced(src, dst)
	if err == nil {
		t.Fatal("expected error when mirror is missing a non-empty source Cookies DB")
	}
	if !errors.Is(err, ErrCookiesLocked) {
		t.Errorf("error should wrap ErrCookiesLocked, got: %v", err)
	}
}

func TestVerifyCookiesSyncedZeroLengthInMirror(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSyncTestFile(t, filepath.Join(src, "Default", "Network", "Cookies"), "real cookie data")
	writeSyncTestFile(t, filepath.Join(dst, "Default", "Network", "Cookies"), "")

	err := verifyCookiesSynced(src, dst)
	if !errors.Is(err, ErrCookiesLocked) {
		t.Fatalf("expected ErrCookiesLocked for a zero-length mirror copy, got: %v", err)
	}
}

func TestVerifyCookiesSyncedOK(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSyncTestFile(t, filepath.Join(src, "Default", "Network", "Cookies"), "real cookie data")
	writeSyncTestFile(t, filepath.Join(dst, "Default", "Network", "Cookies"), "real cookie data (copied)")

	if err := verifyCookiesSynced(src, dst); err != nil {
		t.Fatalf("expected no error when mirror has a usable cookie DB, got: %v", err)
	}
}

// A fresh source profile (never logged into) has no cookie DB at all — that
// is not a sync failure and must not be flagged.
func TestVerifyCookiesSyncedNoSourceCookies(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSyncTestFile(t, filepath.Join(src, "Local State"), "state")

	if err := verifyCookiesSynced(src, dst); err != nil {
		t.Fatalf("expected no error when source has no cookie DB, got: %v", err)
	}
}

// SyncUserProfile must surface the same failure end-to-end on the non-Windows
// copyDir fallback path (no rsync binary available).
func TestSyncUserProfileCopyDirFallbackDetectsLockedCookies(t *testing.T) {
	if _, err := os.Stat("/usr/bin/true"); err != nil {
		t.Skip("requires a POSIX-like environment")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	dst := filepath.Join(root, "dst")
	writeSyncTestFile(t, filepath.Join(src, "Local State"), "state")
	writeSyncTestFile(t, filepath.Join(src, "Default", "Network", "Cookies"), "real cookie data")
	// Simulate Chrome holding Cookies open: make it unreadable so copyDir skips it,
	// the same way a Windows sharing violation or EBUSY would be skipped.
	cookiesPath := filepath.Join(src, "Default", "Network", "Cookies")
	if err := os.Chmod(cookiesPath, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(cookiesPath, 0o644)
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 0 does not block reads")
	}

	t.Setenv("PATH", "") // force the rsync-unavailable fallback
	err := SyncUserProfile(src, dst)
	if !errors.Is(err, ErrCookiesLocked) {
		t.Fatalf("expected ErrCookiesLocked from the copyDir fallback path, got: %v", err)
	}
}
