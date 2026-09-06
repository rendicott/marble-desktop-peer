//go:build darwin

package desktop

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDarwinScreensizeJXA(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sz, err := darwinScreensize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w, h := sz.points()
	if w < 800 || h < 600 {
		t.Fatalf("implausible screen %dx%d scale=%v", w, h, sz.Scale)
	}
}

func TestDarwinLockState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ls := QueryLockState(ctx)
	if ls.Source == "unknown" {
		t.Fatalf("lock source unknown: %+v", ls)
	}
	t.Logf("lock=%v source=%s %s", ls.Locked, ls.Source, ls.Detail)
}

func TestDarwinAvailable(t *testing.T) {
	ok, note := Available()
	if !ok {
		t.Fatalf("available=false note=%s", note)
	}
	if !strings.Contains(note, "screencapture") {
		t.Fatalf("note=%s", note)
	}
}

func TestDarwinPermsJSONShape(t *testing.T) {
	p := QueryPerms()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "screen_recording") {
		t.Fatalf("%s", b)
	}
}

func TestScreencapturePresent(t *testing.T) {
	if _, err := exec.LookPath("screencapture"); err != nil {
		if _, err2 := exec.LookPath("/usr/sbin/screencapture"); err2 != nil {
			t.Fatal("screencapture missing")
		}
	}
}
