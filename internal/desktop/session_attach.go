package desktop

import (
	"fmt"
	"strings"
)

// How a desktop operation is routed. The interactive session always stays on
// routeLocal — session-0 attach is not a new default and is not a prerequisite
// for a normal user install.
const (
	routeLocal = iota
	routeRefuse
	routeAttach
)

const (
	noInteractiveSessionNote = "no interactive session (session 0, no logged-on user)"
	secureDesktopNote        = "secure desktop (locked or UAC prompt)"
	// session0AttachOffNote is the user-process session-0 case. Attach stays
	// off, so this must not claim we looked for a logged-on user and found none.
	session0AttachOffNote = "session 0 (non-interactive); desktop attach is off"
)

// WTS_CONNECTSTATE_CLASS values we care about when choosing a session to attach to.
const (
	wtsActive       = 0
	wtsConnected    = 1
	wtsDisconnected = 4
)

// attachSession is a logged-on Windows session the peer can attach to.
type attachSession struct {
	ID    uint32
	State uint32
	User  string
}

// desktopRoute decides where a desktop operation goes.
// nonInteractive is true only when this process is in session 0 or on a
// non-interactive window station. attach is the session-0 opt-in, which is
// ignored when nonInteractive is false so a user session never changes path.
func desktopRoute(nonInteractive, attach bool) int {
	if !nonInteractive {
		return routeLocal
	}
	if !attach {
		return routeRefuse
	}
	return routeAttach
}

// session0AttachDecision is the opt-in for session-0 desktop attach.
// env is MARBLE_PEER_SESSION0_DESKTOP (empty if unset) and wins over cfg.
// cfg nil means "not set". localSystem is NT AUTHORITY\SYSTEM.
// Default: on for LocalSystem, off for everyone else — a normal user install
// never acquires service-style behavior, including one that landed in session 0.
func session0AttachDecision(env string, cfg *bool, localSystem bool) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "0", "false", "off", "no":
		return false
	case "1", "true", "on", "yes":
		return true
	}
	if cfg != nil {
		return *cfg
	}
	return localSystem
}

// pickAttachSession chooses the session to attach to.
// consoleID is WTSGetActiveConsoleSessionId (0xFFFFFFFF if there is none).
// sessions contains only sessions that already have a user token. Session 0
// is skipped if it is present. A blank User still counts: the token succeeded
// and the name query did not.
// Preference: the console session, then an active session, then a connected
// one, then a disconnected logged-on session (a VM whose user disconnected).
func pickAttachSession(consoleID uint32, sessions []attachSession) (attachSession, bool) {
	var active, connected, disconnected []attachSession
	for _, s := range sessions {
		if s.ID == 0 {
			continue
		}
		if consoleID != 0 && consoleID != 0xFFFFFFFF && s.ID == consoleID {
			return s, true
		}
		switch s.State {
		case wtsActive:
			active = append(active, s)
		case wtsConnected:
			connected = append(connected, s)
		default:
			disconnected = append(disconnected, s)
		}
	}
	if len(active) > 0 {
		return active[0], true
	}
	if len(connected) > 0 {
		return connected[0], true
	}
	if len(disconnected) > 0 {
		return disconnected[0], true
	}
	return attachSession{}, false
}

func attachOKNote(id uint32, user string) string {
	if strings.TrimSpace(user) == "" {
		return fmt.Sprintf("attached to session %d", id)
	}
	return fmt.Sprintf("attached to session %d (%s)", id, user)
}

func attachDeniedNote(id uint32, err error) string {
	return fmt.Sprintf("attach to session %d denied: %v", id, err)
}

// quoteWinArg quotes one Windows command-line argument (CommandLineToArgvW rules).
func quoteWinArg(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			slashes++
		case '"':
			for ; slashes > 0; slashes-- {
				b.WriteByte('\\')
			}
			b.WriteString(`\"`)
		default:
			for ; slashes > 0; slashes-- {
				b.WriteByte('\\')
			}
			b.WriteByte(s[i])
		}
	}
	for ; slashes > 0; slashes-- {
		b.WriteByte('\\')
	}
	b.WriteByte('"')
	return b.String()
}
