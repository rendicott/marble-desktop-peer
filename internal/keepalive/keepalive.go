// Package keepalive keeps the graphical session awake while marble-peer runs
// so XWayland/DISPLAY and desktop input do not go away after idle blanking.
package keepalive

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rendicott/marble-desktop-peer/internal/desktop"
)

// Guard holds background inhibitors until Stop or context cancel.
type Guard struct {
	mu       sync.Mutex
	cmds     []*exec.Cmd
	cookie   uint32 // GNOME SessionManager inhibit cookie (0 = none)
	ssCookie uint32 // freedesktop/GNOME ScreenSaver inhibit
	// gsettings restore
	prevIdleDelay    string
	prevLockEnabled  string
	prevLockDelay    string
	gsettingsTouched bool
	cancel           context.CancelFunc
	done             chan struct{}
}

// Start begins idle/suspend inhibition for the lifetime of ctx (or until Stop).
// Safe to call when headless — returns a no-op Guard.
// Disable with MARBLE_PEER_KEEP_AWAKE=0.
func Start(parent context.Context) *Guard {
	if os.Getenv("MARBLE_PEER_KEEP_AWAKE") == "0" ||
		strings.EqualFold(os.Getenv("MARBLE_PEER_KEEP_AWAKE"), "false") {
		log.Printf("keepalive: disabled (MARBLE_PEER_KEEP_AWAKE=0)")
		return &Guard{done: make(chan struct{})}
	}

	if runtime.GOOS == "darwin" {
		return startCaffeinate(parent)
	}

	ctx, cancel := context.WithCancel(parent)
	g := &Guard{cancel: cancel, done: make(chan struct{})}

	// 1) gnome-session-inhibit (works on Ubuntu GNOME user sessions)
	if path, err := exec.LookPath("gnome-session-inhibit"); err == nil {
		cmd := exec.CommandContext(ctx, path,
			"--app-id", "marble-peer",
			"--reason", "Marble peer: keep display/session awake for computer use",
			"--inhibit", "idle:suspend",
			"--inhibit-only",
		)
		// Inherit graphical session env (DISPLAY, DBUS, …)
		cmd.Env = os.Environ()
		desktop.EnsureEnv(&cmd.Env)
		if err := cmd.Start(); err != nil {
			log.Printf("keepalive: gnome-session-inhibit start: %v", err)
		} else {
			g.cmds = append(g.cmds, cmd)
			go func() { _ = cmd.Wait() }()
			log.Printf("keepalive: gnome-session-inhibit idle:suspend (pid=%d)", cmd.Process.Pid)
		}
	} else {
		log.Printf("keepalive: gnome-session-inhibit not found")
	}

	// 2) Direct GNOME SessionManager.Inhibit (idle+suspend)
	if c, err := gnomeInhibit(); err != nil {
		log.Printf("keepalive: SessionManager.Inhibit: %v", err)
	} else {
		g.cookie = c
		log.Printf("keepalive: GNOME SessionManager inhibit cookie=%d", c)
	}

	// 3) ScreenSaver inhibit (prevents lock-on-idle on many GNOME setups)
	if c, err := screenSaverInhibit(); err != nil {
		log.Printf("keepalive: ScreenSaver.Inhibit: %v", err)
	} else {
		g.ssCookie = c
		log.Printf("keepalive: ScreenSaver inhibit cookie=%d", c)
	}

	// 4) Soften GNOME lock settings for peer lifetime (restored on Stop)
	g.applyGSettingsNoLock()

	// 5) X11 screensaver off (XWayland) — reapplied periodically
	applyX11NoBlank()
	go g.loop(ctx)

	return g
}

// Stop ends inhibitors. Safe to call multiple times.
func (g *Guard) Stop() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.cancel != nil {
		g.cancel()
		g.cancel = nil
	}
	cookie := g.cookie
	g.cookie = 0
	ssCookie := g.ssCookie
	g.ssCookie = 0
	cmds := g.cmds
	g.cmds = nil
	g.mu.Unlock()

	if cookie != 0 {
		if err := gnomeUninhibit(cookie); err != nil {
			log.Printf("keepalive: Uninhibit: %v", err)
		}
	}
	if ssCookie != 0 {
		if err := screenSaverUninhibit(ssCookie); err != nil {
			log.Printf("keepalive: ScreenSaver.UnInhibit: %v", err)
		}
	}
	g.restoreGSettings()
	for _, cmd := range cmds {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(os.Interrupt)
			p := cmd.Process
			time.AfterFunc(2*time.Second, func() {
				_ = p.Kill()
			})
		}
	}
	// Wait briefly for loop to exit (it owns closing g.done)
	select {
	case <-g.done:
	case <-time.After(3 * time.Second):
	}
	log.Printf("keepalive: stopped")
}

func (g *Guard) loop(ctx context.Context) {
	t := time.NewTicker(45 * time.Second)
	defer t.Stop()
	defer func() {
		select {
		case <-g.done:
		default:
			close(g.done)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			applyX11NoBlank()
		}
	}
}

func startCaffeinate(parent context.Context) *Guard {
	ctx, cancel := context.WithCancel(parent)
	g := &Guard{cancel: cancel, done: make(chan struct{})}
	path, err := exec.LookPath("caffeinate")
	if err != nil {
		log.Printf("keepalive: caffeinate not found")
		close(g.done)
		return g
	}
	// -d display, -i idle, -m disk, -s system (AC), -u user activity assertion
	cmd := exec.CommandContext(ctx, path, "-dimsu")
	if err := cmd.Start(); err != nil {
		log.Printf("keepalive: caffeinate start: %v", err)
		close(g.done)
		return g
	}
	g.cmds = append(g.cmds, cmd)
	go func() { _ = cmd.Wait() }()
	log.Printf("keepalive: caffeinate -dimsu (pid=%d)", cmd.Process.Pid)
	go g.loop(ctx)
	return g
}

func applyX11NoBlank() {
	xset, err := exec.LookPath("xset")
	if err != nil {
		return
	}
	// Screensaver off / no blank. DPMS may be missing on pure Wayland XWayland.
	for _, args := range [][]string{
		{"s", "off"},
		{"s", "noblank"},
		{"s", "0", "0"},
		{"-dpms"},
	} {
		cmd := exec.Command(xset, args...)
		env := os.Environ()
		desktop.EnsureEnv(&env)
		cmd.Env = env
		_ = cmd.Run()
	}
}

func gnomeInhibit() (uint32, error) {
	// gdbus call --session --dest org.gnome.SessionManager \
	//   --object-path /org/gnome/SessionManager \
	//   --method org.gnome.SessionManager.Inhibit "marble-peer" 0 "why" 12
	// flags: 4=suspend, 8=idle → 12
	cmd := exec.Command("gdbus", "call", "--session",
		"--dest", "org.gnome.SessionManager",
		"--object-path", "/org/gnome/SessionManager",
		"--method", "org.gnome.SessionManager.Inhibit",
		"marble-peer", "0", "Marble peer: keep display awake for computer use", "12",
	)
	env := os.Environ()
	desktop.EnsureEnv(&env)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return parseUint32Cookie(string(out))
}

func gnomeUninhibit(cookie uint32) error {
	cmd := exec.Command("gdbus", "call", "--session",
		"--dest", "org.gnome.SessionManager",
		"--object-path", "/org/gnome/SessionManager",
		"--method", "org.gnome.SessionManager.Uninhibit",
		fmt.Sprintf("%d", cookie),
	)
	env := os.Environ()
	desktop.EnsureEnv(&env)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func screenSaverInhibit() (uint32, error) {
	// Try freedesktop + GNOME ScreenSaver interfaces.
	type target struct{ dest, path, method string }
	for _, t := range []target{
		{"org.freedesktop.ScreenSaver", "/org/freedesktop/ScreenSaver", "org.freedesktop.ScreenSaver.Inhibit"},
		{"org.freedesktop.ScreenSaver", "/ScreenSaver", "org.freedesktop.ScreenSaver.Inhibit"},
		{"org.gnome.ScreenSaver", "/org/gnome/ScreenSaver", "org.gnome.ScreenSaver.Inhibit"},
	} {
		cmd := exec.Command("gdbus", "call", "--session",
			"--dest", t.dest,
			"--object-path", t.path,
			"--method", t.method,
			"marble-peer", "Marble peer: prevent lock screen during computer use",
		)
		env := os.Environ()
		desktop.EnsureEnv(&env)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			continue
		}
		if c, err := parseUint32Cookie(string(out)); err == nil {
			return c, nil
		}
	}
	return 0, fmt.Errorf("no ScreenSaver inhibit interface responded")
}

func screenSaverUninhibit(cookie uint32) error {
	for _, t := range []struct{ dest, path, method string }{
		{"org.freedesktop.ScreenSaver", "/org/freedesktop/ScreenSaver", "org.freedesktop.ScreenSaver.UnInhibit"},
		{"org.freedesktop.ScreenSaver", "/ScreenSaver", "org.freedesktop.ScreenSaver.UnInhibit"},
		{"org.gnome.ScreenSaver", "/org/gnome/ScreenSaver", "org.gnome.ScreenSaver.UnInhibit"},
	} {
		cmd := exec.Command("gdbus", "call", "--session",
			"--dest", t.dest,
			"--object-path", t.path,
			"--method", t.method,
			fmt.Sprintf("%d", cookie),
		)
		env := os.Environ()
		desktop.EnsureEnv(&env)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err == nil {
			_ = out
			return nil
		}
	}
	return fmt.Errorf("UnInhibit failed")
}

func parseUint32Cookie(s string) (uint32, error) {
	i := strings.Index(s, "uint32")
	if i < 0 {
		// sometimes just (123,)
		s = strings.TrimSpace(s)
	} else {
		s = strings.TrimSpace(s[i+6:])
	}
	num := ""
	for _, r := range s {
		if r >= '0' && r <= '9' {
			num += string(r)
		} else if num != "" {
			break
		}
	}
	if num == "" {
		return 0, fmt.Errorf("no cookie in %q", s)
	}
	v, err := strconv.ParseUint(num, 10, 32)
	return uint32(v), err
}

func (g *Guard) applyGSettingsNoLock() {
	if _, err := exec.LookPath("gsettings"); err != nil {
		return
	}
	// Save previous values then disable idle lock while peer runs.
	get := func(schema, key string) string {
		cmd := exec.Command("gsettings", "get", schema, key)
		env := os.Environ()
		desktop.EnsureEnv(&env)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	set := func(schema, key, val string) {
		cmd := exec.Command("gsettings", "set", schema, key, val)
		env := os.Environ()
		desktop.EnsureEnv(&env)
		cmd.Env = env
		_ = cmd.Run()
	}
	g.prevIdleDelay = get("org.gnome.desktop.session", "idle-delay")
	g.prevLockEnabled = get("org.gnome.desktop.screensaver", "lock-enabled")
	g.prevLockDelay = get("org.gnome.desktop.screensaver", "lock-delay")
	if g.prevIdleDelay == "" && g.prevLockEnabled == "" {
		return
	}
	// idle-delay 0 = never idle; lock-enabled false; lock-delay 0
	set("org.gnome.desktop.session", "idle-delay", "uint32 0")
	set("org.gnome.desktop.screensaver", "lock-enabled", "false")
	set("org.gnome.desktop.screensaver", "lock-delay", "uint32 0")
	g.gsettingsTouched = true
	log.Printf("keepalive: gsettings lock softened (idle-delay=0, lock-enabled=false); will restore on stop")
}

func (g *Guard) restoreGSettings() {
	if !g.gsettingsTouched {
		return
	}
	set := func(schema, key, val string) {
		if val == "" {
			return
		}
		cmd := exec.Command("gsettings", "set", schema, key, val)
		env := os.Environ()
		desktop.EnsureEnv(&env)
		cmd.Env = env
		if err := cmd.Run(); err != nil {
			log.Printf("keepalive: restore gsettings %s %s: %v", schema, key, err)
		}
	}
	set("org.gnome.desktop.session", "idle-delay", g.prevIdleDelay)
	set("org.gnome.desktop.screensaver", "lock-enabled", g.prevLockEnabled)
	set("org.gnome.desktop.screensaver", "lock-delay", g.prevLockDelay)
	log.Printf("keepalive: gsettings lock settings restored")
	g.gsettingsTouched = false
}
