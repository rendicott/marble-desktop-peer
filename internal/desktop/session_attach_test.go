package desktop

import (
	"errors"
	"testing"
)

func TestInteractiveSessionNeverAttaches(t *testing.T) {
	if got := desktopRoute(false, true); got != routeLocal {
		t.Fatalf("opt-in must not reroute an interactive session: got %d", got)
	}
	if got := desktopRoute(false, false); got != routeLocal {
		t.Fatalf("interactive session without opt-in: got %d", got)
	}
	if got := desktopRoute(true, false); got != routeRefuse {
		t.Fatalf("session 0 with attach off: got %d want refuse", got)
	}
	if got := desktopRoute(true, true); got != routeAttach {
		t.Fatalf("session 0 with attach on: got %d want attach", got)
	}
}

func TestSession0AttachDecision(t *testing.T) {
	on := true
	off := false
	cases := []struct {
		env    string
		cfg    *bool
		system bool
		want   bool
	}{
		{"", nil, false, false},
		{"", nil, true, true},
		{"", &off, true, false},
		{"", &on, false, true},
		{"0", &on, true, false},
		{"false", nil, true, false},
		{"off", nil, true, false},
		{"1", &off, false, true},
		{"on", nil, false, true},
		{" yes ", nil, false, true},
		{"no", &on, true, false},
	}
	for _, c := range cases {
		if got := session0AttachDecision(c.env, c.cfg, c.system); got != c.want {
			t.Errorf("env=%q cfg=%v system=%v: got %v want %v", c.env, c.cfg, c.system, got, c.want)
		}
	}
}

func TestPickAttachSession(t *testing.T) {
	admin := attachSession{ID: 2, State: wtsDisconnected, User: "Administrator"}
	consoleUser := attachSession{ID: 1, State: wtsConnected, User: "Ada"}
	active := attachSession{ID: 3, State: wtsActive, User: "Bea"}
	connected := attachSession{ID: 4, State: wtsConnected, User: "Cy"}

	if _, ok := pickAttachSession(0xFFFFFFFF, nil); ok {
		t.Fatal("empty list should not attach")
	}
	if _, ok := pickAttachSession(1, []attachSession{{ID: 0, State: wtsActive, User: "SYSTEM"}}); ok {
		t.Fatal("session 0 must never be chosen")
	}

	got, ok := pickAttachSession(1, []attachSession{admin, consoleUser})
	if !ok || got.ID != 1 || got.User != "Ada" {
		t.Fatalf("console with a user should win: %+v ok=%v", got, ok)
	}

	// Reproduction: console session has nobody logged on; Administrator is
	// disconnected on session 2. Attach there.
	got, ok = pickAttachSession(1, []attachSession{admin})
	if !ok || got.ID != 2 || got.User != "Administrator" {
		t.Fatalf("disconnected logged-on user: %+v ok=%v", got, ok)
	}

	got, ok = pickAttachSession(0xFFFFFFFF, []attachSession{admin, active})
	if !ok || got.ID != 3 {
		t.Fatalf("active should beat disconnected: %+v", got)
	}
	got, ok = pickAttachSession(0xFFFFFFFF, []attachSession{admin, connected})
	if !ok || got.ID != 4 {
		t.Fatalf("connected should beat disconnected: %+v", got)
	}
	// A token with no readable username is still a logged-on session. Active
	// beats a disconnected named session.
	got, ok = pickAttachSession(1, []attachSession{admin, {ID: 5, State: wtsActive, User: ""}})
	if !ok || got.ID != 5 {
		t.Fatalf("active session with an empty username: %+v", got)
	}
}

func TestAttachNotes(t *testing.T) {
	if got := attachOKNote(2, "Administrator"); got != "attached to session 2 (Administrator)" {
		t.Fatalf("ok note: %q", got)
	}
	if got := attachOKNote(2, ""); got != "attached to session 2" {
		t.Fatalf("ok note without user: %q", got)
	}
	if got := attachDeniedNote(2, errors.New("Access is denied.")); got != "attach to session 2 denied: Access is denied." {
		t.Fatalf("denied note: %q", got)
	}
	if noInteractiveSessionNote != "no interactive session (session 0, no logged-on user)" {
		t.Fatalf("no-session note changed: %q", noInteractiveSessionNote)
	}
	if secureDesktopNote != "secure desktop (locked or UAC prompt)" {
		t.Fatalf("secure note changed: %q", secureDesktopNote)
	}
	if session0AttachOffNote != "session 0 (non-interactive); desktop attach is off" {
		t.Fatalf("attach-off note changed: %q", session0AttachOffNote)
	}
}

func TestQuoteWinArg(t *testing.T) {
	if got := quoteWinArg(`C:\Program Files\marble-peer.exe`); got != `"C:\Program Files\marble-peer.exe"` {
		t.Fatalf("space path: %q", got)
	}
	if got := quoteWinArg(`marble-peer.exe`); got != `"marble-peer.exe"` {
		t.Fatalf("plain: %q", got)
	}
	if got := quoteWinArg(`a"b`); got != `"a\"b"` {
		t.Fatalf("quote: %q", got)
	}
}
