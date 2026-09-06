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
