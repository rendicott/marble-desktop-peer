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
	plist := filepath.Join(plistDir, "com.rendicott.marble-peer.plist")
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.rendicott.marble-peer</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><false/>
  <key>StandardOutPath</key><string>%s/Library/Logs/marble-peer.log</string>
  <key>StandardErrorPath</key><string>%s/Library/Logs/marble-peer.log</string>
</dict>
</plist>
`, exe, homeDir, homeDir)
	if err := os.WriteFile(plist, []byte(body), 0o644); err != nil {
		return nil, "", err
	}
	msg := "wrote " + plist
	if enable {
		if out, err := run("launchctl", "load", plist); err != nil {
			return []string{plist}, msg, fmt.Errorf("launchctl load: %v (%s)", err, out)
		}
		msg += "\nloaded with launchctl"
	} else {
		msg += "\nload with: launchctl load " + plist
	}
	return []string{plist}, msg, nil
}

func uninstallDarwin() ([]string, string, error) {
	plist := filepath.Join(home(), "Library/LaunchAgents/com.rendicott.marble-peer.plist")
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
