//go:build !linux

package autostart

import (
	"fmt"
	"os"
	"os/exec"
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

func windowsStartupDir() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", fmt.Errorf("APPDATA not set")
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup"), nil
}

func installWindows(exe string, enable bool) ([]string, string, error) {
	// Startup folder needs no admin and runs in the interactive session, which
	// the desktop (screenshot/input) tools require; a Windows service would not.
	dir, err := windowsStartupDir()
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", err
	}
	// An earlier build wrote marble-peer.cmd, which flashes a console window.
	_ = os.Remove(filepath.Join(dir, "marble-peer.cmd"))
	vbs := filepath.Join(dir, "marble-peer.vbs")
	if err := os.WriteFile(vbs, utf16LEWithBOM(windowsLauncherVBS(exe)), 0o644); err != nil {
		return nil, "", err
	}
	msg := "wrote Startup launcher " + vbs
	if enable {
		if err := exec.Command("wscript.exe", "//B", vbs).Start(); err != nil {
			return []string{vbs}, msg, fmt.Errorf("start now: %v", err)
		}
		msg += "\nstarted marble-peer in the background (a second copy exits on its own: single-instance lock)"
	}
	msg += "\nlog: " + filepath.Join(home(), ".marble-peer", "peer.log")
	msg += "\nstop: right-click the Marble tray icon > Quit, or: taskkill /IM marble-peer.exe"
	return []string{vbs}, msg, nil
}

func uninstallWindows() ([]string, string, error) {
	dir, err := windowsStartupDir()
	if err != nil {
		return nil, "", err
	}
	var removed []string
	for _, name := range []string{"marble-peer.vbs", "marble-peer.cmd"} {
		p := filepath.Join(dir, name)
		if err := os.Remove(p); err == nil {
			removed = append(removed, p)
		} else if !os.IsNotExist(err) {
			return removed, "", err
		}
	}
	return removed, "removed Startup entry (a running peer keeps running; quit it from the tray icon)", nil
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
