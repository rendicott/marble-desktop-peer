//go:build linux

package autostart

import "fmt"

func installDarwin(exe string, enable bool) ([]string, string, error) {
	return nil, "", fmt.Errorf("not darwin")
}
func uninstallDarwin() ([]string, string, error) {
	return nil, "", fmt.Errorf("not darwin")
}
func installWindows(exe string, enable bool) ([]string, string, error) {
	return nil, "", fmt.Errorf("not windows")
}
func uninstallWindows() ([]string, string, error) {
	return nil, "", fmt.Errorf("not windows")
}
