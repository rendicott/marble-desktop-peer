//go:build !darwin

package desktop

import "context"

// activateAppOS does not raise a window off macOS. expect_app still checks
// that the frontmost app already matches; it never activates a browser.
func activateAppOS(ctx context.Context, name string) error {
	_, _ = ctx, name
	return nil
}
