// Package desktop provides OS-level screenshot and input for marble-peer.
// On Ubuntu GNOME (often Wayland), X11 tools talk to XWayland (DISPLAY=:0).
// We harden that path rather than telling agents to avoid desktop entirely.
package desktop

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ScreenMeta describes the primary display capture.
type ScreenMeta struct {
	W     int     `json:"w"`
	H     int     `json:"h"`
	Scale float64 `json:"scale"`
}

var (
	screenMu   sync.Mutex
	lastScreen ScreenMeta
)

// LastScreen returns dimensions from the most recent Screenshot (if any).
func LastScreen() ScreenMeta {
	screenMu.Lock()
	defer screenMu.Unlock()
	return lastScreen
}

// Screenshot captures the primary display as JPEG bytes.
func Screenshot(ctx context.Context) ([]byte, ScreenMeta, error) {
	dir, err := os.MkdirTemp("", "marble-peer-shot-*")
	if err != nil {
		return nil, ScreenMeta{}, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "s.png")

	var lastErr error
	// Order: grim (native Wayland), gnome-screenshot, ImageMagick import, scrot.
	try := []struct {
		name string
		args []string
	}{
		{"grim", []string{out}},
		{"gnome-screenshot", []string{"-f", out}},
		{"import", []string{"-window", "root", out}},
		{"scrot", []string{"-o", out}},
	}
	for _, t := range try {
		if _, err := exec.LookPath(t.name); err != nil {
			continue
		}
		cmd := exec.CommandContext(ctx, t.name, t.args...)
		ensureDisplay(cmd)
		if outb, err := cmd.CombinedOutput(); err != nil {
			lastErr = fmt.Errorf("%s: %v: %s", t.name, err, strings.TrimSpace(string(outb)))
			continue
		}
		if st, err := os.Stat(out); err != nil || st.Size() == 0 {
			lastErr = fmt.Errorf("%s wrote empty file", t.name)
			continue
		}
		return encodeShot(out)
	}
	if lastErr != nil {
		return nil, ScreenMeta{}, fmt.Errorf("screenshot failed: %v", lastErr)
	}
	return nil, ScreenMeta{}, fmt.Errorf("no screenshot tool (install gnome-screenshot, grim, or imagemagick)")
}

func encodeShot(path string) ([]byte, ScreenMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ScreenMeta{}, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		raw, rerr := os.ReadFile(path)
		return raw, ScreenMeta{}, rerr
	}
	b := img.Bounds()
	meta := ScreenMeta{W: b.Dx(), H: b.Dy(), Scale: 1}
	if meta.W <= 0 || meta.H <= 0 {
		return nil, meta, fmt.Errorf("screenshot decoded with empty bounds")
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, meta, err
	}
	screenMu.Lock()
	lastScreen = meta
	screenMu.Unlock()
	return buf.Bytes(), meta, nil
}

func findXdotool() (string, error) {
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		filepath.Join(home, ".local/bin/xdotool"),
		"xdotool",
	} {
		if p == "xdotool" {
			return exec.LookPath("xdotool")
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", fmt.Errorf("xdotool not found")
}

func normalizeButton(button string) string {
	b := strings.TrimSpace(strings.ToLower(button))
	switch b {
	case "", "0", "left", "l":
		return "1"
	case "middle", "m":
		return "2"
	case "right", "r":
		return "3"
	case "1", "2", "3", "4", "5":
		return b
	default:
		if _, err := strconv.Atoi(b); err == nil && b != "0" {
			return b
		}
		return "1"
	}
}

// Click moves and clicks at screen coordinates using several strategies.
// Empty button is forced to "1" (empty string causes XTest BadValue).
func Click(ctx context.Context, x, y int, button string) error {
	button = normalizeButton(button)
	if x < 0 || y < 0 {
		return fmt.Errorf("invalid click coordinates (%d,%d)", x, y)
	}
	// (0,0) is often a mapping bug; allow only if screenshot says 0 is valid (never)
	if x == 0 && y == 0 {
		return fmt.Errorf("invalid click coordinates (0,0) — take computer_screenshot first and click from visible UI coords")
	}
	if meta := LastScreen(); meta.W > 0 && meta.H > 0 {
		if x >= meta.W || y >= meta.H {
			return fmt.Errorf("click (%d,%d) outside last screenshot %dx%d — re-screenshot and remap", x, y, meta.W, meta.H)
		}
	}

	xd, err := findXdotool()
	if err != nil {
		// Try ydotool without xdotool
		if err2 := clickYdotool(ctx, x, y, button); err2 == nil {
			return nil
		}
		return fmt.Errorf("%v (and ydotool unavailable)", err)
	}

	_ = raiseUsefulWindow(ctx, xd)

	var errs []string
	// Strategy 1: absolute mousemove + click
	if err := xdotoolMoveClick(ctx, xd, x, y, button, false); err == nil {
		return nil
	} else {
		errs = append(errs, "mousemove+click: "+err.Error())
	}
	// Strategy 2: mousemove + mousedown/mouseup
	if err := xdotoolMoveDownUp(ctx, xd, x, y, button); err == nil {
		return nil
	} else {
		errs = append(errs, "mousedown/up: "+err.Error())
	}
	// Strategy 3: click on active window using absolute coords after activate
	if err := xdotoolMoveClick(ctx, xd, x, y, button, true); err == nil {
		return nil
	} else {
		errs = append(errs, "clearmod click: "+err.Error())
	}
	// Strategy 4: ydotool (uinput) if present
	if err := clickYdotool(ctx, x, y, button); err == nil {
		return nil
	} else if !strings.Contains(err.Error(), "not found") {
		errs = append(errs, "ydotool: "+err.Error())
	}

	return fmt.Errorf("desktop click failed after %d strategies: %s", len(errs), strings.Join(errs, " | "))
}

func xdotoolMoveClick(ctx context.Context, xd string, x, y int, button string, clear bool) error {
	cmd := exec.CommandContext(ctx, xd, "mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y))
	ensureDisplay(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return xdotoolErr("mousemove", err, out)
	}
	args := []string{"click"}
	if clear {
		args = append(args, "--clearmodifiers")
	}
	args = append(args, button)
	cmd = exec.CommandContext(ctx, xd, args...)
	ensureDisplay(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return xdotoolErr("click", err, out)
	}
	return nil
}

func xdotoolMoveDownUp(ctx context.Context, xd string, x, y int, button string) error {
	cmd := exec.CommandContext(ctx, xd, "mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y))
	ensureDisplay(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return xdotoolErr("mousemove", err, out)
	}
	for _, op := range []string{"mousedown", "mouseup"} {
		cmd = exec.CommandContext(ctx, xd, op, button)
		ensureDisplay(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return xdotoolErr(op, err, out)
		}
	}
	return nil
}

func clickYdotool(ctx context.Context, x, y int, button string) error {
	yd, err := exec.LookPath("ydotool")
	if err != nil {
		return fmt.Errorf("ydotool not found")
	}
	// ydotool mousemove absolute requires ydotoold; try anyway.
	cmd := exec.CommandContext(ctx, yd, "mousemove", "--absolute", "-x", strconv.Itoa(x), "-y", strconv.Itoa(y))
	ensureDisplay(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ydotool mousemove: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// 0xC0 = left click in some ydotool versions; also try "click 0"
	for _, args := range [][]string{
		{"click", "0"},
		{"click", "0xC0"},
		{"click", button},
	} {
		cmd = exec.CommandContext(ctx, yd, args...)
		ensureDisplay(cmd)
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			_ = out
		}
	}
	return fmt.Errorf("ydotool click failed")
}

// Type types text via xdotool, with wtype fallback on Wayland.
func Type(ctx context.Context, text string) error {
	if text == "" {
		return fmt.Errorf("empty text")
	}
	xd, err := findXdotool()
	if err == nil {
		_ = raiseUsefulWindow(ctx, xd)
		cmd := exec.CommandContext(ctx, xd, "type", "--clearmodifiers", "--delay", "12", "--", text)
		ensureDisplay(cmd)
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			// fall through to wtype
			_ = out
		}
	}
	if wtype, err := exec.LookPath("wtype"); err == nil {
		cmd := exec.CommandContext(ctx, wtype, "--", text)
		ensureDisplay(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("wtype: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("type failed (xdotool and wtype)")
}

// Key sends a key name (e.g. Return, ctrl+c).
func Key(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("empty key")
	}
	xd, err := findXdotool()
	if err == nil {
		_ = raiseUsefulWindow(ctx, xd)
		cmd := exec.CommandContext(ctx, xd, "key", "--clearmodifiers", key)
		ensureDisplay(cmd)
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
			_ = out
		}
	}
	// wtype -k for single keys
	if wtype, err := exec.LookPath("wtype"); err == nil {
		// Map a few common names
		k := key
		switch strings.ToLower(key) {
		case "return", "enter":
			k = "Return"
		case "esc", "escape":
			k = "Escape"
		case "tab":
			k = "Tab"
		case "backspace":
			k = "BackSpace"
		}
		cmd := exec.CommandContext(ctx, wtype, "-k", k)
		ensureDisplay(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("wtype key: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("key failed")
}

// raiseUsefulWindow focuses Chrome / GNOME terminal so clicks land somewhere useful.
func raiseUsefulWindow(ctx context.Context, xd string) error {
	// Class matches first (more stable than title).
	classQueries := []string{"Google-chrome", "google-chrome", "Chromium", "chromium", "Firefox", "firefox"}
	for _, class := range classQueries {
		cmd := exec.CommandContext(ctx, xd, "search", "--onlyvisible", "--class", class)
		ensureDisplay(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			continue
		}
		ids := strings.Fields(string(out))
		if len(ids) == 0 {
			continue
		}
		wid := ids[len(ids)-1]
		act := exec.CommandContext(ctx, xd, "windowactivate", "--sync", wid)
		ensureDisplay(act)
		if err := act.Run(); err == nil {
			return nil
		}
	}
	for _, name := range []string{"Chrome", "Chromium", "Gmail", "UPS", "Firefox"} {
		cmd := exec.CommandContext(ctx, xd, "search", "--onlyvisible", "--name", name)
		ensureDisplay(cmd)
		out, err := cmd.CombinedOutput()
		if err != nil {
			continue
		}
		ids := strings.Fields(string(out))
		if len(ids) == 0 {
			continue
		}
		wid := ids[len(ids)-1]
		act := exec.CommandContext(ctx, xd, "windowactivate", "--sync", wid)
		ensureDisplay(act)
		if err := act.Run(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no focusable window")
}

func xdotoolErr(op string, err error, out []byte) error {
	msg := strings.TrimSpace(string(out))
	if strings.Contains(msg, "BadValue") || strings.Contains(msg, "XTest") || strings.Contains(msg, "X Error") {
		return fmt.Errorf("xdotool %s XTest error (%v: %s) — ensure DISPLAY/XAUTHORITY (peer session wrapper); retry after computer_screenshot", op, err, msg)
	}
	return fmt.Errorf("xdotool %s: %v: %s", op, err, msg)
}

// Available reports whether desktop ops can work.
func Available() (bool, string) {
	hasShot := false
	for _, n := range []string{"grim", "gnome-screenshot", "import", "scrot"} {
		if _, err := exec.LookPath(n); err == nil {
			hasShot = true
			break
		}
	}
	if !hasShot {
		return false, "no screenshot tool"
	}
	_, xdErr := findXdotool()
	_, ydErr := exec.LookPath("ydotool")
	_, wtErr := exec.LookPath("wtype")
	if xdErr != nil && ydErr != nil {
		return true, "screenshot ok; no xdotool/ydotool for click — install xdotool"
	}
	parts := []string{"screenshot ok"}
	if xdErr == nil {
		parts = append(parts, "xdotool")
	}
	if ydErr == nil {
		parts = append(parts, "ydotool")
	}
	if wtErr == nil {
		parts = append(parts, "wtype")
	}
	if os.Getenv("XDG_SESSION_TYPE") == "wayland" || os.Getenv("WAYLAND_DISPLAY") != "" {
		parts = append(parts, "wayland/XWayland")
	}
	return true, strings.Join(parts, "; ")
}

// EnsureEnv mutates env (os.Environ-style KEY=VAL slice) with graphical session vars.
// Used by desktop tools and keepalive inhibitors under systemd --user.
func EnsureEnv(env *[]string) {
	if env == nil {
		return
	}
	set := func(key, val string) {
		prefix := key + "="
		for i, e := range *env {
			if strings.HasPrefix(e, prefix) {
				if len(e) > len(prefix) {
					return
				}
				(*env)[i] = prefix + val
				return
			}
		}
		*env = append(*env, prefix+val)
	}
	get := func(key string) string {
		prefix := key + "="
		for _, e := range *env {
			if strings.HasPrefix(e, prefix) {
				return strings.TrimPrefix(e, prefix)
			}
		}
		return os.Getenv(key)
	}

	hasDisplay := get("DISPLAY") != ""
	if !hasDisplay {
		for _, d := range []string{":0", ":1"} {
			if _, err := os.Stat("/tmp/.X11-unix/X" + strings.TrimPrefix(d, ":")); err == nil {
				set("DISPLAY", d)
				hasDisplay = true
				break
			}
		}
		if !hasDisplay {
			set("DISPLAY", ":0")
		}
	}

	if get("XAUTHORITY") == "" {
		runtimeDir := get("XDG_RUNTIME_DIR")
		if runtimeDir == "" {
			runtimeDir = os.Getenv("XDG_RUNTIME_DIR")
		}
		if runtimeDir != "" {
			if matches, _ := filepath.Glob(filepath.Join(runtimeDir, ".mutter-Xwaylandauth.*")); len(matches) > 0 {
				set("XAUTHORITY", matches[0])
			}
		}
		if get("XAUTHORITY") == "" {
			if h, err := os.UserHomeDir(); err == nil {
				xa := filepath.Join(h, ".Xauthority")
				if _, err := os.Stat(xa); err == nil {
					set("XAUTHORITY", xa)
				}
			}
		}
	}

	if get("XDG_RUNTIME_DIR") == "" {
		if uid := os.Getuid(); uid > 0 {
			rd := fmt.Sprintf("/run/user/%d", uid)
			if st, err := os.Stat(rd); err == nil && st.IsDir() {
				set("XDG_RUNTIME_DIR", rd)
			}
		}
	}
	rd := get("XDG_RUNTIME_DIR")
	if rd == "" {
		rd = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	if get("WAYLAND_DISPLAY") == "" {
		if _, err := os.Stat(filepath.Join(rd, "wayland-0")); err == nil {
			set("WAYLAND_DISPLAY", "wayland-0")
		}
	}
	if get("DBUS_SESSION_BUS_ADDRESS") == "" {
		if _, err := os.Stat(filepath.Join(rd, "bus")); err == nil {
			set("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(rd, "bus"))
		}
	}

	home, _ := os.UserHomeDir()
	if home != "" {
		pathHasLocal := false
		for _, e := range *env {
			if strings.HasPrefix(e, "PATH=") && strings.Contains(e, home+"/.local/bin") {
				pathHasLocal = true
				break
			}
		}
		if !pathHasLocal {
			found := false
			for i, e := range *env {
				if strings.HasPrefix(e, "PATH=") {
					(*env)[i] = "PATH=" + filepath.Join(home, ".local/bin") + ":" + strings.TrimPrefix(e, "PATH=")
					found = true
					break
				}
			}
			if !found {
				set("PATH", filepath.Join(home, ".local/bin")+":/usr/local/bin:/usr/bin:/bin")
			}
		}
	}
}

// ensureDisplay injects a usable graphical session environment for child tools.
// Critical when marble-peer runs under systemd --user.
func ensureDisplay(cmd *exec.Cmd) {
	env := os.Environ()
	EnsureEnv(&env)
	cmd.Env = env
}

// Sleep helper for tests.
func Sleep(d time.Duration) { time.Sleep(d) }

// ProbeClick runs a no-op-ish mousemove to current location (health).
func ProbeClick(ctx context.Context) error {
	xd, err := findXdotool()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, xd, "getmouselocation")
	ensureDisplay(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return xdotoolErr("getmouselocation", err, out)
	}
	return nil
}
