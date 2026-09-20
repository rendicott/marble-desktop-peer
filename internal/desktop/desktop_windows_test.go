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
