//go:build !linux && !darwin && !windows

package tray

import (
	"context"
	"log"
)

func startPlatform(ctx context.Context, h Hooks) error {
	log.Printf("tray: system tray not yet implemented on this OS (peer still runs)")
	<-ctx.Done()
	return ctx.Err()
}
