package desktop

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// LockState reports whether the session appears locked / on greeter.
type LockState struct {
	Locked  bool   `json:"locked"`
	Source  string `json:"source,omitempty"`  // screensaver|loginctl|unknown
	Detail  string `json:"detail,omitempty"`
}

// QueryLockState best-effort detects GNOME/session lock screens.
func QueryLockState(ctx context.Context) LockState {
	// org.gnome.ScreenSaver.GetActive
	for _, dest := range []struct {
		dest, path, method string
	}{
		{"org.gnome.ScreenSaver", "/org/gnome/ScreenSaver", "org.gnome.ScreenSaver.GetActive"},
		{"org.freedesktop.ScreenSaver", "/org/freedesktop/ScreenSaver", "org.freedesktop.ScreenSaver.GetActive"},
		{"org.freedesktop.ScreenSaver", "/ScreenSaver", "org.freedesktop.ScreenSaver.GetActive"},
	} {
		cmd := exec.CommandContext(ctx, "gdbus", "call", "--session",
			"--dest", dest.dest,
			"--object-path", dest.path,
			"--method", dest.method,
		)
		ensureDisplay(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			continue
		}
		s := strings.ToLower(string(out))
		if strings.Contains(s, "true") {
			return LockState{Locked: true, Source: "screensaver", Detail: strings.TrimSpace(string(out))}
		}
		if strings.Contains(s, "false") {
			return LockState{Locked: false, Source: "screensaver", Detail: strings.TrimSpace(string(out))}
		}
	}
	// loginctl show-session
	if path, err := exec.LookPath("loginctl"); err == nil {
		cmd := exec.CommandContext(ctx, path, "show-session", "self", "-p", "LockedHint", "--value")
		ensureDisplay(cmd)
		out, err := cmd.CombinedOutput()
		if err == nil {
			v := strings.TrimSpace(string(out))
			if strings.EqualFold(v, "yes") {
				return LockState{Locked: true, Source: "loginctl", Detail: "LockedHint=yes"}
			}
			if strings.EqualFold(v, "no") {
				return LockState{Locked: false, Source: "loginctl", Detail: "LockedHint=no"}
			}
		}
	}
	return LockState{Locked: false, Source: "unknown", Detail: "could not query"}
}

// FormatLockWarning returns a short agent-facing string when locked.
func FormatLockWarning(ls LockState) string {
	if !ls.Locked {
		return ""
	}
	return fmt.Sprintf(
		"DESKTOP LOCKED (%s). Peer sees the login/lock screen — not the user's apps. "+
			"Do not claim the image is unclear: report LOCK SCREEN. Ask the human to unlock, "+
			"or ensure marble-peer keep-awake is running before idle. Automation cannot type the user password without explicit policy.",
		ls.Source,
	)
}
