//go:build windows

package autostart

import "fmt"

// Stubs so autostart.go's runtime.GOOS switch links on windows.
// installLinux/uninstallLinux already live in other.go for this build.
func installDarwin(exe string, enable bool) ([]string, string, error) {
	return nil, "", fmt.Errorf("not darwin")
}
func uninstallDarwin() ([]string, string, error) {
	return nil, "", fmt.Errorf("not darwin")
}
