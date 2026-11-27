package desktop

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestCompareFocus(t *testing.T) {
	cases := []struct {
		expected, focused string
		want              FocusVerdict
	}{
		{expected: "TextEdit", focused: "TextEdit", want: FocusMatch},
		{expected: "  TextEdit  ", focused: "TextEdit", want: FocusMatch},
		{expected: "TextEdit", focused: " TextEdit\n", want: FocusMatch},
		{expected: "Google Chrome", focused: "TextEdit", want: FocusMismatch},
		{expected: "TextEdit", focused: "textedit", want: FocusMismatch},
		{expected: "", focused: "TextEdit", want: FocusUnknown},
		{expected: "TextEdit", focused: "", want: FocusUnknown},
		{expected: "", focused: "", want: FocusUnknown},
		{expected: "   ", focused: "TextEdit", want: FocusUnknown},
		{expected: "TextEdit", focused: "  \t", want: FocusUnknown},
	}
	for _, tc := range cases {
		got := CompareFocus(tc.expected, tc.focused)
		if got != tc.want {
			t.Errorf("CompareFocus(%q, %q) = %q, want %q", tc.expected, tc.focused, got, tc.want)
		}
	}
}

func TestValidExpectApp(t *testing.T) {
	ok := []string{
		"TextEdit",
		"Google Chrome",
		"Code-Insiders",
		"App_1",
		"Notes 2.0",
		"A",
		strings.Repeat("a", 64),
	}
	bad := []string{
		"",
		`Chrome"`,
		`tell application "Google Chrome" to activate`,
		"Chrome; beep",
		"Chrome\nbeep",
		"Chrome\n",
		"\nChrome",
		"Chrome\rbeep",
		"app'name",
		"app`name",
		`app\name`,
		"a/b",
		"app\tname",
		"Mail & Calendar",
		"café",
		strings.Repeat("a", 65),
	}
	for _, name := range ok {
		if !ValidExpectApp(name) {
			t.Errorf("ValidExpectApp(%q) = false, want true", name)
		}
	}
	for _, name := range bad {
		if ValidExpectApp(name) {
			t.Errorf("ValidExpectApp(%q) = true, want false", name)
		}
	}
}

func TestActivateExpectAppRejectsUnsafeName(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"", `Chrome"`, "Chrome; beep", "Chrome\nbeep", "Chrome\rbeep"} {
		focused, err := ActivateExpectApp(ctx, name)
		if err == nil {
			t.Fatalf("ActivateExpectApp(%q) accepted the name", name)
		}
		if focused != "" {
			t.Fatalf("ActivateExpectApp(%q) focused=%q, want empty (no sample)", name, focused)
		}
		if !strings.Contains(err.Error(), "invalid expect_app") {
			t.Fatalf("ActivateExpectApp(%q) error = %v", name, err)
		}
	}
}

func TestExpectAppActivateValidatesBeforeAppleScript(t *testing.T) {
	b, err := os.ReadFile("desktop_darwin.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if n := strings.Count(text, "to activate"); n != 1 {
		t.Fatalf("darwin sources should activate only for expect_app, count=%d", n)
	}
	body := funcBody(text, "activateAppOS")
	if body == "" {
		t.Fatal("activateAppOS missing from desktop_darwin.go")
	}
	validated := strings.Index(body, "ValidExpectApp")
	script := strings.Index(body, "to activate")
	if validated < 0 || script < 0 || validated > script {
		t.Fatalf("activateAppOS must validate the name before building AppleScript:\n%s", body)
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "desktop_darwin.go" || e.IsDir() || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".swift") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "to activate") || strings.Contains(string(src), "windowactivate") {
			t.Errorf("%s activates an app", name)
		}
	}
}

func TestFocusMismatchErrorNamesBoth(t *testing.T) {
	got := FocusMismatchError(" TextEdit ", "Google Chrome")
	for _, name := range []string{"TextEdit", "Google Chrome"} {
		if !strings.Contains(got, name) {
			t.Errorf("error %q does not name %s", got, name)
		}
	}
}

// TestNoActivateBeforeInput fails if type/key/click raise an application.
// macOS used to osascript-activate Google Chrome, and Linux used to
// windowactivate a browser, before injecting keystrokes. Those events then
// landed in whichever app came forward — often the operator's logged-in browser.
func TestNoActivateBeforeInput(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	banned := []string{
		"raiseChrome",
		"raiseUsefulWindow",
		"windowactivate",
		`tell application "Google Chrome" to activate`,
	}
	inputFuncs := []string{"typeOS", "keyOS", "clickOS"}
	var sawSource bool
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".swift") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sawSource = true
		text := string(b)
		for _, s := range banned {
			if strings.Contains(text, s) {
				t.Errorf("%s contains %q", name, s)
			}
		}
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		for _, fn := range inputFuncs {
			body := funcBody(text, fn)
			if body == "" {
				continue
			}
			if strings.Contains(body, "activate") {
				t.Errorf("%s: %s raises an app before input:\n%s", name, fn, body)
			}
		}
	}
	if !sawSource {
		t.Fatal("no desktop sources found")
	}
}

// funcBody returns the source of a top-level function through its closing
// brace, or "" if it is absent. Braces inside strings and line comments
// are ignored so a following function's doc comment is not included.
func funcBody(src, name string) string {
	sig := "func " + name + "("
	i := strings.Index(src, sig)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	start := strings.Index(rest, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inLine, inRaw, inStr, escape := false, false, false, false
	for j := start; j < len(rest); j++ {
		c := rest[j]
		switch {
		case inLine:
			if c == '\n' {
				inLine = false
			}
		case inRaw:
			if c == '`' {
				inRaw = false
			}
		case inStr:
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inStr = false
			}
		case c == '/' && j+1 < len(rest) && rest[j+1] == '/':
			inLine = true
			j++
		case c == '`':
			inRaw = true
		case c == '"':
			inStr = true
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return rest[:j+1]
			}
		}
	}
	return rest
}
