package app

import (
	"context"
	"testing"
)

func TestGuardAllowsInputWhenNoClickRemembered(t *testing.T) {
	a := &App{}
	for _, kind := range []string{"desktop_type", "desktop_key"} {
		if env := a.guardDesktopFocus(context.Background(), kind); env != nil {
			t.Fatalf("%s: no remembered app should allow input, got %+v", kind, env)
		}
	}
}

func TestGuardDoesNotSampleOnClick(t *testing.T) {
	a := &App{}
	a.setExpectedFocus("TextEdit")
	// desktop_click establishes focus; it must not refuse based on the previous app.
	// This returns before any frontmost-app sample, so it stays OS-independent.
	if env := a.guardDesktopFocus(context.Background(), "desktop_click"); env != nil {
		t.Fatalf("click should not be focus-guarded, got %+v", env)
	}
}
