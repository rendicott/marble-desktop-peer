//go:build !linux

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

func installDarwin(exe string, enable bool) ([]string, string, error) {
	homeDir := home()
	plistDir := filepath.Join(homeDir, "Library/LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		return nil, "", err
	}
	_ = os.MkdirAll(filepath.Join(homeDir, "Library/Logs"), 0o755)
	plist := filepath.Join(plistDir, "com.rendicott.marble-peer.plist")
	label := "com.rendicott.marble-peer"
	logPath := filepath.Join(homeDir, "Library/Logs/marble-peer.log")
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>run</string>
  </array>
  <key>WorkingDirectory</key><string>%s</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><false/>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
  <key>ProcessType</key><string>Interactive</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>MARBLE_PEER_KEEP_AWAKE</key><string>1</string>
  </dict>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, label, exe, homeDir, logPath, logPath)
	if err := os.WriteFile(plist, []byte(body), 0o644); err != nil {
		return nil, "", err
	}
	msg := "LaunchAgent " + plist
	if enable {
		uid := os.Getuid()
		domain := fmt.Sprintf("gui/%d", uid)
		target := domain + "/" + label
		_, _ = run("launchctl", "bootout", target)
		if out, err := run("launchctl", "bootstrap", domain, plist); err != nil {
			// Older launchctl: load
			if out2, err2 := run("launchctl", "load", "-w", plist); err2 != nil {
				return []string{plist}, msg, fmt.Errorf("launchctl bootstrap: %v (%s); load: %v (%s)", err, out, err2, out2)
			}
			msg += "\nloaded with launchctl load -w"
		} else {
			_, _ = run("launchctl", "enable", target)
			_, _ = run("launchctl", "kickstart", "-k", target)
			msg += "\nbootstrapped " + target
		}
		msg += "\nlogs: tail -f " + logPath
		msg += "\nGrant Screen Recording + Accessibility to this binary (or Terminal if you start it from a terminal)."
		msg += "\nRun: marble-peer doctor"
	} else {
		msg += "\nload with: launchctl bootstrap gui/$(id -u) " + plist
	}
	return []string{plist}, msg, nil
}

func uninstallDarwin() ([]string, string, error) {
	plist := filepath.Join(home(), "Library/LaunchAgents/com.rendicott.marble-peer.plist")
	label := "com.rendicott.marble-peer"
	uid := os.Getuid()
	target := fmt.Sprintf("gui/%d/%s", uid, label)
	_, _ = run("launchctl", "bootout", target)
	_, _ = run("launchctl", "unload", plist)
	if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
		return nil, "", err
	}
	return []string{plist}, "unloaded and removed LaunchAgent", nil
}

func installWindows(exe string, enable bool) ([]string, string, error) {
	// Startup folder .cmd is simple and needs no admin.
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return nil, "", fmt.Errorf("APPDATA not set")
	}
	dir := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	cmdPath := filepath.Join(dir, "marble-peer.cmd")
	body := fmt.Sprintf("@echo off\r\nstart \"\" %q run\r\n", exe)
	if err := os.WriteFile(cmdPath, []byte(body), 0o644); err != nil {
		return nil, "", err
	}
	_ = enable
	return []string{cmdPath}, "wrote Startup shortcut " + cmdPath, nil
}

func uninstallWindows() ([]string, string, error) {
	appData := os.Getenv("APPDATA")
	cmdPath := filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", "marble-peer.cmd")
	if err := os.Remove(cmdPath); err != nil && !os.IsNotExist(err) {
		return nil, "", err
	}
	return []string{cmdPath}, "removed Startup entry", nil
}

// Stubs so autostart.go's runtime.GOOS switch links on non-linux targets.
func installLinux(exe string, enable bool) ([]string, string, error) {
	return nil, "", fmt.Errorf("not linux")
}
func uninstallLinux() ([]string, string, error) {
	return nil, "", fmt.Errorf("not linux")
}

// silence unused
var _ = runtime.GOOS
