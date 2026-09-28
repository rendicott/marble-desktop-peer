//go:build darwin

package autostart

import "fmt"

// Stubs so autostart.go's runtime.GOOS switch links on darwin.
func installLinux(exe string, enable bool) ([]string, string, error) {
	return nil, "", fmt.Errorf("not linux")
}
func uninstallLinux() ([]string, string, error) {
	return nil, "", fmt.Errorf("not linux")
}
func installWindows(exe string, enable bool) ([]string, string, error) {
	return nil, "", fmt.Errorf("not windows")
}
func uninstallWindows() ([]string, string, error) {
	return nil, "", fmt.Errorf("not windows")
}
