package desktop

import (
	"image"
	"testing"
)

func TestImageToScreenUnscaled(t *testing.T) {
	m := ScreenMeta{W: 100, H: 50, Scale: 1, ScreenW: 100, ScreenH: 50}
	sx, sy := ImageToScreen(m, 40, 20)
	if sx != 40 || sy != 20 {
		t.Fatalf("got %d,%d", sx, sy)
	}
}

func TestImageToScreenScaled(t *testing.T) {
	// 2560x1440 → 1280x720, scale=2
	m := ScreenMeta{W: 1280, H: 720, Scale: 2, ScreenW: 2560, ScreenH: 1440}
	sx, sy := ImageToScreen(m, 640, 360)
	if sx != 1280 || sy != 720 {
		t.Fatalf("got %d,%d want 1280,720", sx, sy)
	}
}

func TestResizeNearest(t *testing.T) {
	src := imageNewRGBA(100, 50)
	dst := resizeNearest(src, 50, 25)
	b := dst.Bounds()
	if b.Dx() != 50 || b.Dy() != 25 {
		t.Fatalf("size %dx%d", b.Dx(), b.Dy())
	}
}

func imageNewRGBA(w, h int) *image.RGBA {
	return image.NewRGBA(image.Rect(0, 0, w, h))
}
