//go:build !linux && !darwin

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

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
