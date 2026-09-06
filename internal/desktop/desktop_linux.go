//go:build linux

package desktop

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func screenshotOS(ctx context.Context, out string) (coordW, coordH int, err error) {
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
		return 0, 0, nil
	}
	if lastErr != nil {
		return 0, 0, fmt.Errorf("screenshot failed: %v", lastErr)
	}
	return 0, 0, fmt.Errorf("no screenshot tool (install gnome-screenshot, grim, or imagemagick)")
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

func clickOS(ctx context.Context, sx, sy int, button string) error {
	xd, err := findXdotool()
	if err != nil {
		if err2 := clickYdotool(ctx, sx, sy, button); err2 == nil {
			logActiveWindow(ctx, "")
			return nil
		}
		return fmt.Errorf("%v (and ydotool unavailable)", err)
	}

	var errs []string
	if err := xdotoolMoveClick(ctx, xd, sx, sy, button, false); err == nil {
		logActiveWindow(ctx, xd)
		return nil
	} else {
		errs = append(errs, "mousemove+click: "+err.Error())
	}
	if err := xdotoolMoveDownUp(ctx, xd, sx, sy, button); err == nil {
		logActiveWindow(ctx, xd)
		return nil
	} else {
		errs = append(errs, "mousedown/up: "+err.Error())
	}
	if err := xdotoolMoveClick(ctx, xd, sx, sy, button, true); err == nil {
		logActiveWindow(ctx, xd)
		return nil
	} else {
		errs = append(errs, "clearmod click: "+err.Error())
	}
	if err := clickYdotool(ctx, sx, sy, button); err == nil {
		logActiveWindow(ctx, xd)
		return nil
	} else if !strings.Contains(err.Error(), "not found") {
		errs = append(errs, "ydotool: "+err.Error())
	}

	return fmt.Errorf("desktop click failed after %d strategies (screen=%d,%d): %s",
		len(errs), sx, sy, strings.Join(errs, " | "))
}

func logActiveWindow(ctx context.Context, xd string) {
	if xd == "" {
		var err error
		xd, err = findXdotool()
		if err != nil {
			return
		}
	}
	cmd := exec.CommandContext(ctx, xd, "getactivewindow", "getwindowname")
	ensureDisplay(cmd)
	out, err := cmd.CombinedOutput()
	name := strings.TrimSpace(string(out))
	if err != nil || name == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "marble-peer desktop: active window after click: %s\n", name)
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
	cmd := exec.CommandContext(ctx, yd, "mousemove", "--absolute", "-x", strconv.Itoa(x), "-y", strconv.Itoa(y))
	ensureDisplay(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ydotool mousemove: %v: %s", err, strings.TrimSpace(string(out)))
	}
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

func typeOS(ctx context.Context, text string) error {
	xd, err := findXdotool()
	if err == nil {
		_ = raiseUsefulWindow(ctx, xd)
		cmd := exec.CommandContext(ctx, xd, "type", "--clearmodifiers", "--delay", "12", "--", text)
		ensureDisplay(cmd)
		if out, err := cmd.CombinedOutput(); err == nil {
			return nil
		} else {
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

func keyOS(ctx context.Context, key string) error {
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
	if wtype, err := exec.LookPath("wtype"); err == nil {
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

func raiseUsefulWindow(ctx context.Context, xd string) error {
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

func availableOS() (bool, string) {
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

func probeClickOS(ctx context.Context) error {
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

func queryPermsOS() Perms {
	return Perms{ScreenRecording: "n/a", Accessibility: "n/a", Note: "linux"}
}

func requestPermsOS() Perms { return queryPermsOS() }

func openPrivacySettingsOS(section string) error {
	_ = section
	return fmt.Errorf("privacy settings UI is macOS-only")
}

func permsHelpOS() string { return "" }
