//go:build windows

package desktop

import (
	"context"
	"strings"
	"testing"
)

// nonInteractiveSession must never panic. When attach is off, a non-interactive
// session is reported locked with source "session" and the schtasks hint. When
// attach is on, the lock state is whatever the logged-on desktop reports.
func TestNonInteractiveSessionAgreesWithLockState(t *testing.T) {
	yes, why := nonInteractiveSession()
	ls := queryLockWindows(context.Background())
	if !yes {
		return
	}
	if why == "" {
		t.Error("nonInteractiveSession reported true with no explanation")
	}
	if ls.Source == "" {
		t.Fatal("lock state has no source")
	}
	if ls.Source != "session" {
		return
	}
	if !ls.Locked {
		t.Error("queryLockWindows should report Locked=true when source is session")
	}
	if !strings.Contains(ls.Detail, "schtasks") && !strings.Contains(ls.Detail, "no interactive session") && !strings.Contains(ls.Detail, "attach to session") {
		t.Errorf("queryLockWindows detail is not actionable: %s", ls.Detail)
	}
}
