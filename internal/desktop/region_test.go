package desktop

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// retinaShot writes a 3840×2160 PNG (physical pixels of a 1920×1080 logical display,
// the field report's machine) with a 20×20 physical green marker at logical (840, 450).
func retinaShot(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	green := color.RGBA{G: 255, A: 255}
	for y := 900; y < 920; y++ {
		for x := 1680; x < 1700; x++ {
			img.Set(x, y, green)
		}
	}
	p := filepath.Join(t.TempDir(), "s.png")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func resetShotState() {
	screenMu.Lock()
	lastScreen = ScreenMeta{}
	stickyOpts = CaptureOpts{}
	lastClickScreen.Set = false
	screenMu.Unlock()
}

func decodeJPEG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestFullShotUnchangedAndReportsDownscale(t *testing.T) {
	resetShotState()
	_, meta, err := encodeShotMapped(retinaShot(t), 1920, 1080, CaptureOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if meta.W != 1280 || meta.H != 720 || meta.Scale != 1.5 || !meta.Downscaled {
		t.Fatalf("default full shot must stay 1280×720, 1.5× downscaled: %+v", meta)
	}
	m := MetaMap(meta, nil)
	if r := m["region"].(map[string]int); r["w"] != 1920 || r["h"] != 1080 || r["x"] != 0 {
		t.Fatalf("full-screen region: %v", r)
	}
	if sx, sy := ImageToScreen(meta, 640, 360); sx != 960 || sy != 540 {
		t.Fatalf("full-shot mapping (%d,%d)", sx, sy)
	}
}

// The report's ask: a 400×500 region at native detail. On Retina that is 800×1000 image
// px (2 per display px), so a 44pt target is ~88 image px instead of ~5.
func TestRegionNativeDetailAndClickMapping(t *testing.T) {
	resetShotState()
	o := CaptureOpts{Region: &Rect{640, 200, 400, 500}}
	b, meta, err := encodeShotMapped(retinaShot(t), 1920, 1080, o)
	if err != nil {
		t.Fatal(err)
	}
	if meta.W != 800 || meta.H != 1000 || meta.Downscaled || meta.Scale != 0.5 {
		t.Fatalf("region should be native physical detail: %+v", meta)
	}
	if zoom := MetaMap(meta, nil)["zoom"].(float64); zoom != 2 {
		t.Fatalf("zoom %v", zoom)
	}
	// Marker at logical (840,450) → region-relative (200,250) → image (400,500).
	if c := color.RGBAModel.Convert(decodeJPEG(t, b).At(405, 505)).(color.RGBA); c.G < 200 || c.R > 60 {
		t.Fatalf("marker not where expected in the crop: %+v", c)
	}
	// Clicks in this image map back through the region.
	if sx, sy := ImageToScreen(meta, 400, 500); sx != 840 || sy != 450 {
		t.Fatalf("crop click maps to (%d,%d), want (840,450)", sx, sy)
	}

	// scale=1.0 → logical resolution.
	_, meta, _ = encodeShotMapped(retinaShot(t), 1920, 1080, CaptureOpts{Region: &Rect{640, 200, 400, 500}, Scale: 1})
	if meta.W != 400 || meta.H != 500 || meta.Downscaled {
		t.Fatalf("scale=1: %+v", meta)
	}
	// max_edge still applies to regions: 500 → 400×500 is exactly 1:1 with the display
	// (not downscaled); 250 → 200×250 is.
	_, meta, _ = encodeShotMapped(retinaShot(t), 1920, 1080, CaptureOpts{Region: &Rect{640, 200, 400, 500}, MaxEdge: 500})
	if meta.H != 500 || meta.W != 400 || meta.Downscaled {
		t.Fatalf("max_edge 500 on region: %+v", meta)
	}
	_, meta, _ = encodeShotMapped(retinaShot(t), 1920, 1080, CaptureOpts{Region: &Rect{640, 200, 400, 500}, MaxEdge: 250})
	if meta.H != 250 || meta.W != 200 || !meta.Downscaled || meta.Scale != 2 {
		t.Fatalf("max_edge 250 on region: %+v", meta)
	}
}

func TestUncappedAndClampedRegion(t *testing.T) {
	resetShotState()
	_, meta, _ := encodeShotMapped(retinaShot(t), 1920, 1080, CaptureOpts{MaxEdge: -1})
	if meta.W != 3840 || meta.H != 2160 || meta.Downscaled {
		t.Fatalf("uncapped full: %+v", meta)
	}
	// Region hanging off the bottom-right is clamped to the display.
	_, meta, err := encodeShotMapped(retinaShot(t), 1920, 1080, CaptureOpts{Region: &Rect{1800, 1000, 400, 400}})
	if err != nil || meta.Region != (Rect{1800, 1000, 120, 80}) {
		t.Fatalf("clamp: %+v %v", meta.Region, err)
	}
	if _, _, err := encodeShotMapped(retinaShot(t), 1920, 1080, CaptureOpts{Region: &Rect{2000, 0, 50, 50}}); err == nil {
		t.Fatal("off-screen region should error")
	}
}

// Region in image space = pixels of the last screenshot, the same space clicks use.
func TestResolveRegionImageSpace(t *testing.T) {
	resetShotState()
	if _, err := ResolveRegion(Rect{0, 0, 10, 10}, "image"); err == nil {
		t.Fatal("image space without a prior screenshot should error")
	}
	if _, _, err := encodeShotMapped(retinaShot(t), 1920, 1080, CaptureOpts{}); err != nil {
		t.Fatal(err)
	}
	// The agent saw the full shot at 1280×720 and outlines a pane there.
	r, err := ResolveRegion(Rect{426, 133, 268, 334}, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.X != 639 || r.Y != 199 || r.W != 402 || r.H != 501 {
		t.Fatalf("image→display %+v", r)
	}
	if r, _ := ResolveRegion(Rect{640, 200, 400, 500}, "screen"); r != (Rect{640, 200, 400, 500}) {
		t.Fatalf("screen space passthrough %+v", r)
	}
	if _, err := ResolveRegion(Rect{0, 0, 0, 5}, "screen"); err == nil {
		t.Fatal("zero-size region should error")
	}
}

func TestCrosshairFollowsClickIntoCrop(t *testing.T) {
	resetShotState()
	setLastClickScreen(840, 450) // display coords
	b, _, err := encodeShotMapped(retinaShot(t), 1920, 1080, CaptureOpts{Region: &Rect{640, 200, 400, 500}})
	if err != nil {
		t.Fatal(err)
	}
	// Crosshair arm 10 px right of the click, at image (410, 500) — red.
	if c := color.RGBAModel.Convert(decodeJPEG(t, b).At(410, 500)).(color.RGBA); c.R < 180 || c.G > 120 {
		t.Fatalf("crosshair not drawn at the click's crop position: %+v", c)
	}
}

func TestZoomAroundStaysOnScreen(t *testing.T) {
	resetShotState()
	screenMu.Lock()
	lastScreen = ScreenMeta{W: 1280, H: 720, ScreenW: 1920, ScreenH: 1080}
	screenMu.Unlock()
	if r := ZoomAround(960, 540, 400); r != (Rect{760, 340, 400, 400}) {
		t.Fatalf("centred %+v", r)
	}
	if r := ZoomAround(10, 1075, 400); r != (Rect{0, 680, 400, 400}) {
		t.Fatalf("corner %+v", r)
	}
}
