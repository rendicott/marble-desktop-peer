//go:build windows

package desktop

import (
	"context"
	"testing"
	"unsafe"
)

// SendInput rejects (returns 0) any cbSize that is not sizeof(INPUT); the mouse
// and keyboard variants must both match it: 40 bytes on 64-bit, 28 on 32-bit.
func TestInputStructSizes(t *testing.T) {
	want := uintptr(28)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		want = 40
	}
	if got := unsafe.Sizeof(inputMouseEvent{}); got != want {
		t.Fatalf("inputMouseEvent size = %d, want %d", got, want)
	}
	if got := unsafe.Sizeof(inputKeyEvent{}); got != want {
		t.Fatalf("inputKeyEvent size = %d, want %d", got, want)
	}
}

func TestScreenshotSmoke(t *testing.T) {
	if ok, note := Available(); !ok {
		t.Skipf("no interactive desktop: %s", note)
	}
	img, meta, err := Screenshot(context.Background())
	if err != nil {
		t.Skipf("capture unavailable in this session: %v", err)
	}
	if len(img) == 0 || meta.W <= 0 || meta.H <= 0 || meta.ScreenW <= 0 || meta.ScreenH <= 0 {
		t.Fatalf("bad screenshot: bytes=%d meta=%+v", len(img), meta)
	}
	if meta.W > MaxScreenshotEdge && meta.H > MaxScreenshotEdge {
		t.Fatalf("image not downscaled: %+v", meta)
	}
}

func TestProbeAndLockDoNotPanic(t *testing.T) {
	_ = ProbeClick(context.Background())
	ls := QueryLockState(context.Background())
	if ls.Source == "" {
		t.Fatalf("lock state has no source: %+v", ls)
	}
}

// Injection has to be accepted by the real kernel: SendInput returns the number
// of events it inserted and 0 for a bad cbSize/layout, which clickOS/typeOS/keyOS
// surface as errors. Also proves the click coordinate space: the cursor must end
// up exactly where we asked (DPI-aware, physical pixels).
func TestInputInjectionAccepted(t *testing.T) {
	if ok, note := Available(); !ok {
		t.Skipf("no interactive desktop: %s", note)
	}
	ctx := context.Background()
	w, h := screenSize()
	x, y := w/3, h/3
	if err := clickOS(ctx, x, y, "1"); err != nil {
		t.Fatalf("left click: %v", err)
	}
	var pt struct{ x, y int32 }
	if r, _, e := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); r == 0 {
		t.Fatalf("GetCursorPos: %v", e)
	}
	if int(pt.x) != x || int(pt.y) != y {
		t.Fatalf("cursor at (%d,%d) after click at (%d,%d) on %dx%d", pt.x, pt.y, x, y, w, h)
	}
	if err := clickOS(ctx, x, y, "3"); err != nil { // right click, then dismiss the menu
		t.Fatalf("right click: %v", err)
	}
	if err := keyOS(ctx, "Escape"); err != nil {
		t.Fatalf("Escape: %v", err)
	}
	if err := clickOS(ctx, x, y, "5"); err != nil {
		t.Fatalf("wheel: %v", err)
	}
	if err := typeOS(ctx, "marble-peer é\t\r\nok"); err != nil {
		t.Fatalf("type: %v", err)
	}
	// Chords that are harmless to whatever the runner has focused.
	for _, k := range []string{"shift", "ctrl+shift", "F13", "alt+F15", "ctrl+Home", "a"} {
		if err := keyOS(ctx, k); err != nil {
			t.Errorf("key %q: %v", k, err)
		}
	}
}
