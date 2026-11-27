package app

import (
	"context"
	"strings"
	"testing"
)

func TestGuardAllowsInputWhenNoClickRemembered(t *testing.T) {
	a := &App{}
	for _, kind := range []string{"desktop_type", "desktop_key"} {
		if env := a.guardDesktopFocus(context.Background(), kind, nil); env != nil {
			t.Fatalf("%s: no remembered app should allow input, got %+v", kind, env)
		}
	}
}

func TestGuardDoesNotSampleOnClick(t *testing.T) {
	a := &App{}
	a.setExpectedFocus("TextEdit")
	// desktop_click establishes focus; it must not refuse based on the previous app.
	// This returns before any frontmost-app sample, so it stays OS-independent.
	if env := a.guardDesktopFocus(context.Background(), "desktop_click", nil); env != nil {
		t.Fatalf("click should not be focus-guarded, got %+v", env)
	}
}

func TestGuardRejectsUnsafeExpectApp(t *testing.T) {
	a := &App{}
	bad := []string{
		`Mail"`,
		"Mail; beep",
		"Mail\nbeep",
		"Mail\rbeep",
		"Mail'beep",
		"Mail\\beep",
	}
	for _, kind := range []string{"desktop_type", "desktop_key", "desktop_click"} {
		for _, name := range bad {
			env := a.guardDesktopFocus(context.Background(), kind, map[string]interface{}{
				"expect_app": name,
			})
			if env == nil || env.OK {
				t.Fatalf("%s %q: expected refusal, got %+v", kind, name, env)
			}
			if !strings.Contains(env.Error, "invalid expect_app") {
				t.Fatalf("%s %q: %s", kind, name, env.Error)
			}
			if env.Meta["expected_app"] != name {
				t.Fatalf("%s %q: expected_app=%v", kind, name, env.Meta["expected_app"])
			}
		}
	}
	// Blank is the same as omitting the field: no remembered app, so type proceeds.
	if env := a.guardDesktopFocus(context.Background(), "desktop_type", map[string]interface{}{
		"expect_app": "  ",
	}); env != nil {
		t.Fatalf("blank expect_app should be absent, got %+v", env)
	}
	env := a.guardDesktopFocus(context.Background(), "desktop_click", map[string]interface{}{
		"expect_app": 1,
	})
	if env == nil || env.OK || !strings.Contains(env.Error, "string") {
		t.Fatalf("non-string expect_app: %+v", env)
	}
}
