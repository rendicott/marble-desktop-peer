package desktop

import (
	"fmt"
	"strings"
)

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
