//go:build darwin

package desktop

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

//go:embed marble_desk.swift
var deskSwift []byte

const deskHelperVersion = "1"

var (
	helperOnce sync.Mutex
	helperPath string
	helperErr  error
)

func helperDir() string {
	if v := os.Getenv("MARBLE_PEER_HOME"); v != "" {
		return filepath.Join(v, "helpers")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".marble-peer", "helpers")
}

func ensureDeskHelper(ctx context.Context) (string, error) {
	helperOnce.Lock()
	defer helperOnce.Unlock()
	if helperPath != "" && helperErr == nil {
		return helperPath, nil
	}
	dir := helperDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		helperErr = err
		return "", err
	}
	bin := filepath.Join(dir, "marble-desk")
	if out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput(); err == nil && strings.TrimSpace(string(out)) == deskHelperVersion {
		helperPath = bin
		helperErr = nil
		return bin, nil
	}
	src := filepath.Join(dir, "marble_desk.swift")
	if err := os.WriteFile(src, deskSwift, 0o600); err != nil {
		helperErr = err
		return "", err
	}
	swiftc, err := exec.LookPath("swiftc")
	if err != nil {
		helperErr = fmt.Errorf("swiftc not found (install Xcode Command Line Tools) and no cached marble-desk helper")
		return "", helperErr
	}
	cmd := exec.CommandContext(ctx, swiftc, "-O", "-o", bin, src)
	if out, err := cmd.CombinedOutput(); err != nil {
		helperErr = fmt.Errorf("swiftc marble-desk: %v: %s", err, strings.TrimSpace(string(out)))
		return "", helperErr
	}
	_ = exec.Command("codesign", "-s", "-", "-f", bin).Run()
	if out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != deskHelperVersion {
		helperErr = fmt.Errorf("marble-desk helper failed after compile: %v %s", err, strings.TrimSpace(string(out)))
		return "", helperErr
	}
	helperPath = bin
	helperErr = nil
	return bin, nil
}

func runDesk(ctx context.Context, args ...string) (string, error) {
	bin, err := ensureDeskHelper(ctx)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		return s, fmt.Errorf("marble-desk %s: %v: %s", strings.Join(args, " "), err, s)
	}
	return s, nil
}

type darwinScreenSize struct {
	W     float64 `json:"w"`
	H     float64 `json:"h"`
	Scale float64 `json:"scale"`
	PxW   float64 `json:"px_w"`
	PxH   float64 `json:"px_h"`
}

func (s darwinScreenSize) points() (w, h int) {
	return int(s.W + 0.5), int(s.H + 0.5)
}

func darwinScreensize(ctx context.Context) (darwinScreenSize, error) {
	var sz darwinScreenSize
	// JXA first — no helper compile, TCC follows the parent process.
	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e",
		`ObjC.import("AppKit"); var s=$.NSScreen.mainScreen; JSON.stringify({w:s.frame.size.width,h:s.frame.size.height,scale:s.backingScaleFactor,px_w:s.frame.size.width*s.backingScaleFactor,px_h:s.frame.size.height*s.backingScaleFactor});`)
	if out, err := cmd.CombinedOutput(); err == nil {
		if json.Unmarshal(bytes.TrimSpace(out), &sz) == nil && sz.W > 0 && sz.H > 0 {
			return sz, nil
		}
	}
	if s, err := runDesk(ctx, "screensize"); err == nil {
		if json.Unmarshal([]byte(s), &sz) == nil && sz.W > 0 && sz.H > 0 {
			return sz, nil
		}
	}
	if sz.W <= 0 || sz.H <= 0 {
		return sz, fmt.Errorf("screensize empty")
	}
	return sz, nil
}

func screenshotOS(ctx context.Context, out string) (coordW, coordH int, err error) {
	sc, err := exec.LookPath("screencapture")
	if err != nil {
		sc = "/usr/sbin/screencapture"
	}
	try := func() error {
		cmd := exec.CommandContext(ctx, sc, "-x", "-m", "-t", "png", out)
		b, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("screencapture: %v: %s", err, strings.TrimSpace(string(b)))
		}
		st, err := os.Stat(out)
		if err != nil || st.Size() == 0 {
			msg := strings.TrimSpace(string(b))
			if msg == "" {
				msg = "could not create image from display"
			}
			return fmt.Errorf("screencapture: %s", msg)
		}
		return nil
	}
	if err := try(); err != nil {
		if isScreenCaptureDenied(err) {
			setScreenPerm("denied")
			_ = requestPermsOS()
			_ = openPrivacySettingsOS("screen")
			if err2 := try(); err2 != nil {
				setScreenPerm("denied")
				return 0, 0, fmt.Errorf("%v\n%s", err2, permsHelpOS())
			}
		} else {
			return 0, 0, err
		}
	}
	setScreenPerm("granted")
	sz, szErr := darwinScreensize(ctx)
	if szErr == nil {
		if w, h := sz.points(); w > 0 && h > 0 {
			return w, h, nil
		}
	}
	return 0, 0, nil
}

func isScreenCaptureDenied(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "could not create image") ||
		strings.Contains(s, "not authorized") ||
		strings.Contains(s, "permission") ||
		strings.Contains(s, "denied")
}

func clickOS(ctx context.Context, sx, sy int, button string) error {
	var errs []string
	if err := jxaClick(ctx, sx, sy, button); err == nil {
		logFrontmost(ctx)
		return nil
	} else {
		errs = append(errs, "jxa: "+err.Error())
	}
	if _, err := runDesk(ctx, "click", strconv.Itoa(sx), strconv.Itoa(sy), button); err == nil {
		logFrontmost(ctx)
		return nil
	} else {
		errs = append(errs, "marble-desk: "+err.Error())
	}
	if cliclick, e := exec.LookPath("cliclick"); e == nil {
		spec := fmt.Sprintf("c:%d,%d", sx, sy)
		if button == "3" {
			spec = fmt.Sprintf("rc:%d,%d", sx, sy)
		}
		cmd := exec.CommandContext(ctx, cliclick, spec)
		if out, err2 := cmd.CombinedOutput(); err2 == nil {
			logFrontmost(ctx)
			return nil
		} else {
			errs = append(errs, "cliclick: "+err2.Error()+" "+strings.TrimSpace(string(out)))
		}
	}
	_ = requestPermsOS()
	_ = openPrivacySettingsOS("accessibility")
	return fmt.Errorf("desktop click failed (screen=%d,%d): %s\n%s", sx, sy, strings.Join(errs, " | "), permsHelpOS())
}

func jxaClick(ctx context.Context, x, y int, button string) error {
	down, up, btn := 1, 2, 0 // left
	switch button {
	case "2":
		down, up, btn = 25, 26, 2 // other/middle
	case "3":
		down, up, btn = 3, 4, 1 // right
	}
	script := fmt.Sprintf(`
ObjC.import("CoreGraphics");
var point = $.CGPointMake(%d, %d);
var mv = $.CGEventCreateMouseEvent(null, 5, point, 0);
if (!mv) throw new Error("CGEventCreateMouseEvent move failed");
$.CGEventPost(0, mv);
var dn = $.CGEventCreateMouseEvent(null, %d, point, %d);
if (!dn) throw new Error("CGEventCreateMouseEvent down failed");
$.CGEventPost(0, dn);
var up = $.CGEventCreateMouseEvent(null, %d, point, %d);
if (!up) throw new Error("CGEventCreateMouseEvent up failed");
$.CGEventPost(0, up);
"ok";
`, x, y, down, btn, up, btn)
	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func logFrontmost(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "osascript", "-e",
		`tell application "System Events" to get name of first application process whose frontmost is true`)
	out, err := cmd.CombinedOutput()
	name := strings.TrimSpace(string(out))
	if err != nil || name == "" {
		return
	}
	fmt.Fprintf(os.Stderr, "marble-peer desktop: frontmost after click: %s\n", name)
}

func typeOS(ctx context.Context, text string) error {
	_ = raiseChrome(ctx)
	if _, err := runDesk(ctx, "type", text); err == nil {
		return nil
	} else {
		// osascript System Events fallback (also needs Accessibility).
		script := `tell application "System Events" to keystroke ` + applescriptString(text)
		cmd := exec.CommandContext(ctx, "osascript", "-e", script)
		if out, err2 := cmd.CombinedOutput(); err2 == nil {
			return nil
		} else {
			_ = requestPermsOS()
			return fmt.Errorf("type: %v; osascript: %v: %s\n%s", err, err2, strings.TrimSpace(string(out)), permsHelpOS())
		}
	}
}

func keyOS(ctx context.Context, key string) error {
	_ = raiseChrome(ctx)
	if _, err := runDesk(ctx, "key", key); err == nil {
		return nil
	} else {
		script, ok := applescriptKey(key)
		if !ok {
			return fmt.Errorf("key: %v\n%s", err, permsHelpOS())
		}
		cmd := exec.CommandContext(ctx, "osascript", "-e", script)
		if out, err2 := cmd.CombinedOutput(); err2 != nil {
			return fmt.Errorf("key: %v; osascript: %v: %s\n%s", err, err2, strings.TrimSpace(string(out)), permsHelpOS())
		}
		return nil
	}
}

func raiseChrome(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "osascript", "-e", `tell application "Google Chrome" to activate`)
	return cmd.Run()
}

func applescriptString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func applescriptKey(key string) (string, bool) {
	parts := strings.Split(key, "+")
	name := parts[len(parts)-1]
	mods := parts[:len(parts)-1]
	keyCode := ""
	switch strings.ToLower(name) {
	case "return", "enter":
		keyCode = "return"
	case "tab":
		keyCode = "tab"
	case "escape", "esc":
		keyCode = "escape"
	case "space":
		keyCode = "space"
	case "backspace", "delete":
		keyCode = "delete"
	case "up":
		keyCode = "up arrow"
	case "down":
		keyCode = "down arrow"
	case "left":
		keyCode = "left arrow"
	case "right":
		keyCode = "right arrow"
	default:
		if len(name) == 1 {
			using := applescriptUsing(mods)
			return `tell application "System Events" to keystroke ` + applescriptString(name) + using, true
		}
		return "", false
	}
	using := applescriptUsing(mods)
	return `tell application "System Events" to key code (` + applescriptKeyCode(keyCode) + `)` + using, true
}

func applescriptUsing(mods []string) string {
	if len(mods) == 0 {
		return ""
	}
	var as []string
	for _, m := range mods {
		switch strings.ToLower(m) {
		case "ctrl", "control":
			as = append(as, "control down")
		case "alt", "option", "opt":
			as = append(as, "option down")
		case "shift":
			as = append(as, "shift down")
		case "cmd", "command", "super", "meta":
			as = append(as, "command down")
		}
	}
	if len(as) == 0 {
		return ""
	}
	return " using {" + strings.Join(as, ", ") + "}"
}

func applescriptKeyCode(name string) string {
	// Use keystroke of named keys via System Events key code numbers.
	switch name {
	case "return":
		return "36"
	case "tab":
		return "48"
	case "escape":
		return "53"
	case "space":
		return "49"
	case "delete":
		return "51"
	case "up arrow":
		return "126"
	case "down arrow":
		return "125"
	case "left arrow":
		return "123"
	case "right arrow":
		return "124"
	default:
		return "36"
	}
}

func availableOS() (bool, string) {
	if _, err := exec.LookPath("screencapture"); err != nil {
		if _, err2 := os.Stat("/usr/sbin/screencapture"); err2 != nil {
			return false, "screencapture not found"
		}
	}
	parts := []string{"screencapture"}
	if _, err := exec.LookPath("swiftc"); err == nil {
		parts = append(parts, "swiftc")
	}
	if _, err := exec.LookPath("osascript"); err == nil {
		parts = append(parts, "osascript")
	}
	if _, err := exec.LookPath("cliclick"); err == nil {
		parts = append(parts, "cliclick")
	}
	return true, strings.Join(parts, "; ")
}

func probeClickOS(ctx context.Context) error {
	// Move to the current cursor (no-op). JXA so TCC follows the parent process.
	script := `
ObjC.import("CoreGraphics");
ObjC.import("Cocoa");
var p = $.NSEvent.mouseLocation;
var h = $.NSScreen.mainScreen.frame.size.height;
var point = $.CGPointMake(p.x, h - p.y);
var ev = $.CGEventCreateMouseEvent(null, 5, point, 0);
if (!ev) throw new Error("CGEventCreateMouseEvent failed");
$.CGEventPost(0, ev);
"ok";
`
	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mouse move: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

var (
	screenPermMu sync.Mutex
	screenPerm   string // granted|denied|""
)

func setScreenPerm(v string) {
	screenPermMu.Lock()
	screenPerm = v
	screenPermMu.Unlock()
}

func queryPermsOS() Perms {
	p := Perms{ScreenRecording: "unknown", Accessibility: "unknown", Note: "darwin"}
	screenPermMu.Lock()
	if screenPerm != "" {
		p.ScreenRecording = screenPerm
	}
	screenPermMu.Unlock()
	cmd := exec.Command("osascript", "-l", "JavaScript", "-e",
		`ObjC.import("ApplicationServices"); JSON.stringify({accessibility: $.AXIsProcessTrusted()});`)
	if out, err := cmd.CombinedOutput(); err == nil {
		var j struct {
			Accessibility bool `json:"accessibility"`
		}
		if json.Unmarshal(bytes.TrimSpace(out), &j) == nil {
			p.Accessibility = boolStatus(j.Accessibility)
		}
	}
	return p
}

func boolStatus(v bool) string {
	if v {
		return "granted"
	}
	return "denied"
}

func requestPermsOS() Perms {
	// Prompt the *parent* process (Terminal or marble-peer) via osascript, so
	// TCC grants attach to the same identity that runs screencapture / JXA clicks.
	cmd := exec.Command("osascript", "-l", "JavaScript", "-e", `
ObjC.import("ApplicationServices");
ObjC.import("Foundation");
var prompt = $.kAXTrustedCheckOptionPrompt;
var dict = $.NSDictionary.dictionaryWithObjectForKey(true, prompt);
$.AXIsProcessTrustedWithOptions(dict);
JSON.stringify({accessibility: $.AXIsProcessTrusted()});
`)
	_, _ = cmd.CombinedOutput()
	return queryPermsOS()
}

func openPrivacySettingsOS(section string) error {
	// Old System Preferences URLs still work on recent macOS; also try Settings pane IDs.
	var urls []string
	switch strings.ToLower(section) {
	case "accessibility", "ax":
		urls = []string{
			"x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
			"x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_Accessibility",
		}
	case "screen", "screencapture", "screenrecording":
		urls = []string{
			"x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
			"x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_ScreenCapture",
		}
	default:
		urls = []string{
			"x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
			"x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
		}
	}
	var last error
	for _, u := range urls {
		if err := exec.Command("open", u).Start(); err != nil {
			last = err
		}
	}
	return last
}

func permsHelpOS() string {
	return strings.TrimSpace(`
macOS permissions required for marble-peer desktop control:
  1. System Settings → Privacy & Security → Screen Recording
     Enable the app that launched marble-peer (Terminal / iTerm from a shell, or marble-peer if started as a Login Item).
  2. System Settings → Privacy & Security → Accessibility
     Enable the same app so clicks and keystrokes can be synthesized.
     If you also see marble-desk / osascript listed, enable those too.

Then re-run: marble-peer doctor
Or open the panes: marble-peer doctor --open-settings`)
}
