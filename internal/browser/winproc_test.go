package browser

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDebugPortsFromCommandLines(t *testing.T) {
	lines := []string{
		`"C:\Program Files\Google\Chrome\Application\chrome.exe" --remote-debugging-port=9222 --user-data-dir="C:\Users\ada\.marble-peer\chrome-user-mirror" --no-first-run`,
		`"C:\Program Files\Google\Chrome\Application\chrome.exe" --type=renderer --remote-debugging-port=9333`,
		`"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe" --remote-debugging-port=9444`,
		`"C:\Program Files\Google\Chrome\Application\chrome.exe" "--remote-debugging-port=9555"`,
		`"C:\Program Files\Google\Chrome\Application\chrome.exe" --remote-debugging-port=9222`, // dup
		`notepad.exe --remote-debugging-port=1111`,
		"",
	}
	got := debugPortsFromCommandLines(lines, "chrome", "msedge")
	want := []int{9222, 9444, 9555}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// The Unix ps path only names chrome/chromium: Edge lines must not match there.
	if got := debugPortsFromCommandLines(lines[2:3], "chrome", "chromium"); len(got) != 0 {
		t.Fatalf("edge matched unix names: %v", got)
	}
}

func TestRobocopyArgs(t *testing.T) {
	sep := string(filepath.Separator) // platform separator so Clean has something to strip on any OS
	args := robocopyArgs(filepath.Join("C:", "Users", "ada", "User Data")+sep, filepath.Join("C:", "Users", "ada", "mirror")+sep)
	joined := strings.Join(args, "|")
	for _, must := range []string{"/MIR", "/R:0", "/W:0", "Code Cache", "GPUCache", "SingletonLock", "lockfile", "/XD", "/XF"} {
		if !strings.Contains(joined, must) {
			t.Errorf("robocopy args missing %q: %v", must, args)
		}
	}
	// A trailing backslash before the closing quote breaks robocopy's argument parsing.
	for _, a := range args[:2] {
		if strings.HasSuffix(a, sep) {
			t.Errorf("path arg keeps trailing separator: %q", a)
		}
	}
}
