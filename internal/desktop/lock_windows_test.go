//go:build windows

package desktop

import (
	"context"
	"strings"
	"testing"
)

// nonInteractiveSession must never panic and, whichever branch fires, has to
// agree with queryLockWindows: a non-interactive session is always reported
// locked with source "session" and an actionable message (not the generic
// "secure desktop" wording), since screenshot/input cannot work there either way.
func TestNonInteractiveSessionAgreesWithLockState(t *testing.T) {
	yes, why := nonInteractiveSession()
	ls := queryLockWindows(context.Background())
	if yes {
		if why == "" {
			t.Error("nonInteractiveSession reported true with no explanation")
		}
		if ls.Source != "session" {
			t.Errorf("queryLockWindows source = %q, want %q when non-interactive", ls.Source, "session")
		}
		if !ls.Locked {
			t.Error("queryLockWindows should report Locked=true when non-interactive")
		}
		for _, want := range []string{"interactive", "schtasks"} {
			if !strings.Contains(ls.Detail, want) {
				t.Errorf("queryLockWindows detail missing actionable hint %q: %s", want, ls.Detail)
			}
		}
	}
}
