package desktop

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// expectAppRe is the only set of characters interpolated into AppleScript for
// expect_app. Quotes, semicolons, and newlines are rejected so a payload cannot
// break out of `tell application "…"`.
var expectAppRe = regexp.MustCompile(`^[A-Za-z0-9 ._-]{1,64}$`)

// FocusVerdict is the result of comparing an expected frontmost app with the
// one just sampled. Empty on either side is FocusUnknown: there is no
// expectation, or the frontmost app could not be read. Callers refuse to
// inject input only on FocusMismatch.
type FocusVerdict string

const (
	FocusMatch    FocusVerdict = "match"
	FocusMismatch FocusVerdict = "mismatch"
	FocusUnknown  FocusVerdict = "unknown"
)

// CompareFocus classifies expected against the sampled frontmost app.
// Comparison is exact after trimming space. A case difference is a mismatch.
func CompareFocus(expected, focused string) FocusVerdict {
	expected = strings.TrimSpace(expected)
	focused = strings.TrimSpace(focused)
	if expected == "" || focused == "" {
		return FocusUnknown
	}
	if expected == focused {
		return FocusMatch
	}
	return FocusMismatch
}

// FocusMismatchError names both apps. It is the action error when input
// would land somewhere other than the app focused after the last click.
func FocusMismatchError(expected, focused string) string {
	return fmt.Sprintf("refusing input: expected app %q, focused app %q",
		strings.TrimSpace(expected), strings.TrimSpace(focused))
}

// ValidExpectApp reports whether name is safe to interpolate into AppleScript.
// The pattern is ^[A-Za-z0-9 ._-]{1,64}$. Newlines are rejected explicitly:
// Go's `$` can succeed before a trailing newline, and a newline must never
// reach the script.
func ValidExpectApp(name string) bool {
	if strings.ContainsAny(name, "\n\r") {
		return false
	}
	return expectAppRe.MatchString(name)
}

// ActivateExpectApp makes name frontmost on macOS, then re-samples the
// frontmost app. It returns an error — and callers must not inject input —
// when name is invalid, activation fails, or the frontmost app is not name.
// Other platforms do not activate anything; they only check the app that is
// already frontmost.
func ActivateExpectApp(ctx context.Context, name string) (focused string, err error) {
	if !ValidExpectApp(name) {
		return "", fmt.Errorf("invalid expect_app %q", name)
	}
	if err := activateAppOS(ctx, name); err != nil {
		return "", err
	}
	_, focused, err = ActiveWindow(ctx)
	if err != nil {
		return "", fmt.Errorf("expect_app %q: could not read frontmost app: %v", name, err)
	}
	switch CompareFocus(name, focused) {
	case FocusMatch:
		return focused, nil
	case FocusUnknown:
		return focused, fmt.Errorf("expect_app %q: frontmost app unknown", name)
	default:
		return focused, errors.New(FocusMismatchError(name, focused))
	}
}
