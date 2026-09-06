// Package tray provides a desktop system-tray control surface for marble-peer.
package tray

import (
	"context"
	"os"
)

// Hooks are callbacks from tray menu actions into the daemon.
type Hooks struct {
	// Status returns a short label, e.g. "Online" / "Offline".
	Status func() string
	// MiniUIAddr returns the mini UI base URL (may be empty until bound).
	MiniUIAddr func() string
	// StopAction cancels the current peer action queue item.
	StopAction func()
	// Quit requests a graceful process exit (exit 0 so systemd does not restart).
	Quit func()
	// ComputerID optional label for tooltip.
	ComputerID func() string
}

// Start runs the platform tray until ctx is cancelled.
// Linux: Python AppIndicator helper. macOS: Swift menu bar extra. Other OS: wait/no-op.
// Safe to call when HEADLESS / no DISPLAY — returns immediately.
func Start(ctx context.Context, h Hooks) error {
	if os.Getenv("MARBLE_PEER_NO_TRAY") == "1" {
		return nil
	}
	return startPlatform(ctx, h)
}
