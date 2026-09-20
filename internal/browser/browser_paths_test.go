package browser

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestChromeDataDirCandidatesDarwin(t *testing.T) {
	dirs := chromeDataDirCandidates("darwin", "/Users/ada")
	if len(dirs) == 0 {
		t.Fatal("empty")
	}
	if !strings.Contains(dirs[0], "Library/Application Support/Google/Chrome") {
		t.Fatalf("first darwin dir = %s", dirs[0])
	}
}

func TestChromeDataDirCandidatesLinux(t *testing.T) {
	dirs := chromeDataDirCandidates("linux", "/home/ada")
	if dirs[0] != filepath.Join("/home/ada", ".config", "google-chrome") {
		t.Fatalf("got %s", dirs[0])
	}
}

func TestChromeBinaryCandidatesDarwin(t *testing.T) {
	bins := chromeBinaryCandidates("darwin")
	found := false
	for _, b := range bins {
		if strings.Contains(b, "Google Chrome.app") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing Chrome.app in %v", bins)
	}
}

func TestChromeDataDirCandidatesWindows(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\ada\AppData\Local`)
	dirs := chromeDataDirCandidates("windows", `C:\Users\ada`)
	want := filepath.Join(`C:\Users\ada\AppData\Local`, "Google", "Chrome", "User Data")
	if dirs[0] != want {
		t.Fatalf("first windows dir = %s, want %s", dirs[0], want)
	}
}

func TestChromeBinaryCandidatesWindows(t *testing.T) {
	t.Setenv("ProgramFiles", `C:\Program Files`)
	t.Setenv("ProgramFiles(x86)", `C:\Program Files (x86)`)
	t.Setenv("LOCALAPPDATA", `C:\Users\ada\AppData\Local`)
	bins := chromeBinaryCandidates("windows")
	has := func(sub string) bool {
		for _, b := range bins {
			if strings.Contains(b, sub) {
				return true
			}
		}
		return false
	}
	// per-user installs (LOCALAPPDATA) are the Chrome default for non-admin installs
	for _, sub := range []string{`Users`, "chrome.exe", "msedge.exe", "Edge"} {
		if !has(sub) {
			t.Fatalf("missing %q in %v", sub, bins)
		}
	}
	// Chrome must be preferred over Edge.
	firstEdge, lastChrome := -1, -1
	for i, b := range bins {
		if strings.Contains(strings.ToLower(b), "edge") && firstEdge < 0 {
			firstEdge = i
		}
		if strings.HasSuffix(strings.ToLower(b), "chrome.exe") {
			lastChrome = i
		}
	}
	if firstEdge < lastChrome {
		t.Fatalf("Edge listed before a Chrome candidate: %v", bins)
	}
}
