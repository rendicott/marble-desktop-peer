package desktop

import (
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

// funcBody returns the source of a top-level function, or "" if it is absent.
func funcBody(src, name string) string {
	sig := "func " + name + "("
	i := strings.Index(src, sig)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	if j := strings.Index(rest[len(sig):], "\nfunc "); j >= 0 {
		return rest[:len(sig)+j]
	}
	return rest
}
