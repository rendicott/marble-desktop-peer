//go:build !linux && !darwin

package desktop

import (
	"context"
	"fmt"
	"runtime"
)

func screenshotOS(ctx context.Context, out string) (coordW, coordH int, err error) {
	_ = ctx
	_ = out
	return 0, 0, fmt.Errorf("desktop screenshot not implemented on %s", runtime.GOOS)
}

func clickOS(ctx context.Context, sx, sy int, button string) error {
	_ = ctx
	_ = sx
	_ = sy
	_ = button
	return fmt.Errorf("desktop click not implemented on %s", runtime.GOOS)
}

func typeOS(ctx context.Context, text string) error {
	_ = ctx
	_ = text
	return fmt.Errorf("desktop type not implemented on %s", runtime.GOOS)
}

func keyOS(ctx context.Context, key string) error {
	_ = ctx
	_ = key
	return fmt.Errorf("desktop key not implemented on %s", runtime.GOOS)
}

func availableOS() (bool, string) {
	return false, "desktop not implemented on " + runtime.GOOS
}

func probeClickOS(ctx context.Context) error {
	_ = ctx
	return fmt.Errorf("desktop not implemented on %s", runtime.GOOS)
}

func queryPermsOS() Perms {
	return Perms{ScreenRecording: "n/a", Accessibility: "n/a", Note: runtime.GOOS}
}

func requestPermsOS() Perms { return queryPermsOS() }

func openPrivacySettingsOS(section string) error {
	_ = section
	return fmt.Errorf("privacy settings UI is macOS-only")
}

func permsHelpOS() string { return "" }
