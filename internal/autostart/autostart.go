// Package autostart installs user-level login start for marble-peer.
package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Install writes OS-specific autostart artifacts and optionally enables them.
// Never requires root. Linux: systemd --user unit + session wrapper.
func Install(exe string, enable bool) (paths []string, msg string, err error) {
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			return nil, "", err
		}
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return nil, "", err
	}
	// Prefer resolved binary (not /proc/self/exe ephemeral path after delete)
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}

	switch runtime.GOOS {
	case "linux":
		return installLinux(exe, enable)
	case "darwin":
		return installDarwin(exe, enable)
	case "windows":
		return installWindows(exe, enable)
	default:
		return nil, "", fmt.Errorf("autostart not supported on %s", runtime.GOOS)
	}
}

// Uninstall removes autostart artifacts.
func Uninstall() (paths []string, msg string, err error) {
	switch runtime.GOOS {
	case "linux":
		return uninstallLinux()
	case "darwin":
		return uninstallDarwin()
	case "windows":
		return uninstallWindows()
	default:
		return nil, "", fmt.Errorf("autostart not supported on %s", runtime.GOOS)
	}
}

func home() string {
	h, _ := os.UserHomeDir()
	return h
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
