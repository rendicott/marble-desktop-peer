//go:build !darwin

package autostart

import "fmt"

// RelaunchViaLaunchServices is macOS-only; other platforms have no
// LaunchServices requirement for screen capture.
func RelaunchViaLaunchServices(args []string) error {
	return fmt.Errorf("--gui is only supported on macOS")
}

// RunningViaLaunchServices is always true off macOS.
func RunningViaLaunchServices() bool { return true }

// BundleStale is macOS-only.
func BundleStale() (bool, string) { return false, "" }

// IsPeerBinary is always true off macOS (no bundle indirection).
func IsPeerBinary() bool { return true }

// CreateSelfSignedCert is macOS-only.
func CreateSelfSignedCert() error {
	return fmt.Errorf("--trust-cert is only supported on macOS")
}

// SelfSignedCertStatus is macOS-only.
func SelfSignedCertStatus() (bool, string) { return true, "" }

// SigningStatus is macOS-only; other platforms have no signing requirement.
func SigningStatus() (bool, string) { return true, "" }
