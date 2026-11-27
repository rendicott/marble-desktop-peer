package desktop

import (
	"os"
	"strings"
	"testing"
)

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
