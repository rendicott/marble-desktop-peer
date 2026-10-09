// Package desktop provides OS-level screenshot and input for marble-peer.
//
// Linux: GNOME/XWayland tools (grim, gnome-screenshot, xdotool).
// macOS: screencapture + a compiled Swift helper (CGEvent) for click/type/key.
// Windows: GDI screen capture + SendInput via user32/gdi32 (no CGO). Session 0
// as LocalSystem runs those calls in a helper process in the logged-on session.
package desktop

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxScreenshotEdge is the max JPEG edge sent to the harness/model (ADR-0020).
// Clicks use image pixel space; peer maps to screen via ScreenW/ScreenH.
const MaxScreenshotEdge = 1280

// MaxScreenshotEdgeUncapped bounds max_edge=0 ("uncapped") so one request can't
// produce a multi-hundred-MB frame on an 8K display.
const MaxScreenshotEdgeUncapped = 4096

// Rect is a rectangle in display (click-space) coordinates.
type Rect struct {
	X, Y, W, H int
}

// CaptureOpts selects what a screenshot returns (region capture, peer caps.region).
type CaptureOpts struct {
	// Region in display coordinates; nil = full screen.
	Region *Rect
	// Scale is image px per display px; 0 = all captured detail (physical pixels,
	// e.g. 2× on Retina). Never upscales beyond what was captured.
	Scale float64
	// MaxEdge caps the longest image edge: 0 = default (MaxScreenshotEdge),
	// <0 = uncapped (bounded by MaxScreenshotEdgeUncapped).
	MaxEdge int
}

// ScreenMeta describes a capture (possibly a region, possibly downscaled for transport).
type ScreenMeta struct {
	W       int     `json:"w"`                  // JPEG / image width (model click space)
	H       int     `json:"h"`                  // JPEG / image height
	Scale   float64 `json:"scale"`              // display px per image px (region.w / w; 1 if unscaled)
	ScreenW int     `json:"screen_w,omitempty"` // click-space width (physical or logical points)
	ScreenH int     `json:"screen_h,omitempty"` // click-space height
	// Region is the captured area in display coords; zero value = full screen.
	Region Rect `json:"-"`
	// Downscaled: the image has fewer pixels than the display area it shows.
	Downscaled bool `json:"-"`
}

// region returns the captured area, treating the zero value as the full screen.
func (m ScreenMeta) region() Rect {
	if m.Region.W > 0 && m.Region.H > 0 {
		return m.Region
	}
	sw, sh := m.ScreenW, m.ScreenH
	if sw <= 0 {
		sw = m.W
	}
	if sh <= 0 {
		sh = m.H
	}
	return Rect{0, 0, sw, sh}
}

// Perms is a best-effort macOS TCC snapshot. Other OSes report n/a.
type Perms struct {
	ScreenRecording string `json:"screen_recording"` // granted|denied|unknown|n/a
	Accessibility   string `json:"accessibility"`    // granted|denied|unknown|n/a
	Note            string `json:"note,omitempty"`
}

var (
	screenMu        sync.Mutex
	lastScreen      ScreenMeta
	stickyOpts      CaptureOpts // what post-action shots capture (last explicit request)
	lastClickScreen struct {
		X, Y int
		Set  bool
	} // display coords for the crosshair overlay (survives zoom changes)
)

// LastScreen returns dimensions from the most recent Screenshot (if any).
func LastScreen() ScreenMeta {
	screenMu.Lock()
	defer screenMu.Unlock()
	return lastScreen
}

// MetaMap returns JSON-friendly screenshot metadata for the protocol envelope.
func MetaMap(meta ScreenMeta, extra map[string]interface{}) map[string]interface{} {
	r := meta.region()
	zoom := 0.0
	if r.W > 0 {
		zoom = float64(meta.W) / float64(r.W)
	}
	m := map[string]interface{}{
		"w": meta.W, "h": meta.H, "scale": meta.Scale,
		"screen_w": meta.ScreenW, "screen_h": meta.ScreenH,
		"region":     map[string]int{"x": r.X, "y": r.Y, "w": r.W, "h": r.H},
		"zoom":       zoom, // image px per display px
		"downscaled": meta.Downscaled,
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// setLastClickScreen records the last click in display coords (for crosshair overlay).
func setLastClickScreen(x, y int) {
	screenMu.Lock()
	lastClickScreen.X, lastClickScreen.Y, lastClickScreen.Set = x, y, true
	screenMu.Unlock()
}

// LastClickScreen returns the last click in display coords.
func LastClickScreen() (x, y int, ok bool) {
	screenMu.Lock()
	defer screenMu.Unlock()
	return lastClickScreen.X, lastClickScreen.Y, lastClickScreen.Set
}

// ImageToScreen maps image-space click coords to physical/logical screen pixels,
// through the captured region when the image is a crop.
func ImageToScreen(meta ScreenMeta, ix, iy int) (sx, sy int) {
	if meta.W <= 0 || meta.H <= 0 {
		return ix, iy
	}
	r := meta.region()
	return r.X + ix*r.W/meta.W, r.Y + iy*r.H/meta.H
}

// ResolveRegion converts a requested region to display coords. space "image" means
// pixels of the last screenshot (the same space clicks use); "screen" is display coords.
func ResolveRegion(r Rect, space string) (Rect, error) {
	if r.W <= 0 || r.H <= 0 {
		return Rect{}, fmt.Errorf("region needs positive w and h (got %dx%d)", r.W, r.H)
	}
	switch strings.ToLower(strings.TrimSpace(space)) {
	case "", "image":
		meta := LastScreen()
		if meta.W <= 0 || meta.H <= 0 {
			return Rect{}, fmt.Errorf("region in image space needs a previous screenshot; take one first or pass space=screen")
		}
		x0, y0 := ImageToScreen(meta, r.X, r.Y)
		x1, y1 := ImageToScreen(meta, r.X+r.W, r.Y+r.H)
		return Rect{x0, y0, x1 - x0, y1 - y0}, nil
	case "screen", "display":
		return r, nil
	default:
		return Rect{}, fmt.Errorf("unknown region space %q (image|screen)", space)
	}
}

// ZoomAround returns a size×size display-coord region centred on (x, y), shifted
// to stay on screen.
func ZoomAround(x, y, size int) Rect {
	meta := LastScreen()
	sw, sh := meta.ScreenW, meta.ScreenH
	r := Rect{x - size/2, y - size/2, size, size}
	if sw > 0 && sh > 0 {
		if r.W > sw {
			r.W = sw
		}
		if r.H > sh {
			r.H = sh
		}
		if r.X+r.W > sw {
			r.X = sw - r.W
		}
		if r.Y+r.H > sh {
			r.Y = sh - r.H
		}
	}
	if r.X < 0 {
		r.X = 0
	}
	if r.Y < 0 {
		r.Y = 0
	}
	return r
}

func enabled() bool {
	v := strings.TrimSpace(os.Getenv("MARBLE_PEER_DESKTOP"))
	if v == "0" || strings.EqualFold(v, "false") {
		return false
	}
	return true
}

// Screenshot captures the full primary display as JPEG bytes (default size cap).
func Screenshot(ctx context.Context) ([]byte, ScreenMeta, error) {
	return ScreenshotWith(ctx, CaptureOpts{})
}

// ScreenshotWith captures per opts and makes them sticky for post-action shots.
func ScreenshotWith(ctx context.Context, o CaptureOpts) ([]byte, ScreenMeta, error) {
	img, meta, err := capture(ctx, o)
	if err == nil {
		screenMu.Lock()
		stickyOpts = o
		screenMu.Unlock()
	}
	return img, meta, err
}

// ScreenshotSticky repeats the last explicit capture request (region, scale, cap), so a
// post-click shot of a zoomed region stays zoomed: verification at the same detail.
func ScreenshotSticky(ctx context.Context) ([]byte, ScreenMeta, error) {
	screenMu.Lock()
	o := stickyOpts
	screenMu.Unlock()
	return capture(ctx, o)
}

func capture(ctx context.Context, o CaptureOpts) ([]byte, ScreenMeta, error) {
	if !enabled() {
		return nil, ScreenMeta{}, fmt.Errorf("desktop disabled (MARBLE_PEER_DESKTOP=0)")
	}
	dir, err := os.MkdirTemp("", "marble-peer-shot-*")
	if err != nil {
		return nil, ScreenMeta{}, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "s.png")
	coordW, coordH, err := screenshotOS(ctx, out)
	if err != nil {
		return nil, ScreenMeta{}, err
	}
	return encodeShotMapped(out, coordW, coordH, o)
}

// encodeShotMapped JPEG-encodes path, cropped and scaled per o. If coordW/coordH > 0
// they are the click coordinate space (e.g. macOS logical points on a Retina display);
// otherwise the decoded image pixel size is used (Linux).
func encodeShotMapped(path string, coordW, coordH int, o CaptureOpts) ([]byte, ScreenMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ScreenMeta{}, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		raw, rerr := os.ReadFile(path)
		return raw, ScreenMeta{}, rerr
	}
	b := img.Bounds()
	imgW0, imgH0 := b.Dx(), b.Dy()
	if imgW0 <= 0 || imgH0 <= 0 {
		return nil, ScreenMeta{}, fmt.Errorf("screenshot decoded with empty bounds")
	}

	screenW, screenH := imgW0, imgH0
	if coordW > 0 && coordH > 0 {
		screenW, screenH = coordW, coordH
	}

	// Region in display coords, clamped to the screen.
	reg := Rect{0, 0, screenW, screenH}
	if o.Region != nil {
		reg = clampRect(*o.Region, screenW, screenH)
		if reg.W < 1 || reg.H < 1 {
			return nil, ScreenMeta{}, fmt.Errorf("region %+v is outside the %dx%d display", *o.Region, screenW, screenH)
		}
	}
	// Crop in captured (physical) pixels.
	px0, py0 := reg.X*imgW0/screenW, reg.Y*imgH0/screenH
	px1, py1 := (reg.X+reg.W)*imgW0/screenW, (reg.Y+reg.H)*imgH0/screenH
	if px1 <= px0 {
		px1 = px0 + 1
	}
	if py1 <= py0 {
		py1 = py0 + 1
	}
	var src image.Image = img
	if o.Region != nil {
		src = cropImage(img, image.Rect(b.Min.X+px0, b.Min.Y+py0, b.Min.X+px1, b.Min.Y+py1))
	}
	cw, ch := px1-px0, py1-py0

	imgW, imgH := cw, ch
	if o.Scale > 0 {
		tw := int(float64(reg.W)*o.Scale + 0.5)
		th := int(float64(reg.H)*o.Scale + 0.5)
		if tw >= 1 && th >= 1 && tw < cw {
			imgW, imgH = tw, th
		}
	}
	maxEdge := MaxScreenshotEdge
	if o.MaxEdge > 0 {
		maxEdge = o.MaxEdge
	} else if o.MaxEdge < 0 {
		maxEdge = MaxScreenshotEdgeUncapped
	}
	if maxEdge > MaxScreenshotEdgeUncapped {
		maxEdge = MaxScreenshotEdgeUncapped
	}
	if max(imgW, imgH) > maxEdge {
		if imgW >= imgH {
			imgH = imgH * maxEdge / imgW
			imgW = maxEdge
		} else {
			imgW = imgW * maxEdge / imgH
			imgH = maxEdge
		}
		if imgW < 1 {
			imgW = 1
		}
		if imgH < 1 {
			imgH = 1
		}
	}
	scaled := src
	if imgW != cw || imgH != ch {
		scaled = resizeNearest(src, imgW, imgH)
	}

	// Draw last-click crosshair, mapped from display coords into this image.
	if cx, cy, ok := LastClickScreen(); ok && cx >= reg.X && cy >= reg.Y && cx < reg.X+reg.W && cy < reg.Y+reg.H {
		scaled = drawCrosshair(scaled, (cx-reg.X)*imgW/reg.W, (cy-reg.Y)*imgH/reg.H)
	}

	meta := ScreenMeta{
		W: imgW, H: imgH, Scale: float64(reg.W) / float64(imgW),
		ScreenW: screenW, ScreenH: screenH,
		Downscaled: float64(imgW) < float64(reg.W)*0.99,
	}
	if o.Region != nil {
		meta.Region = reg
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: 80}); err != nil {
		return nil, meta, err
	}
	screenMu.Lock()
	lastScreen = meta
	screenMu.Unlock()
	return buf.Bytes(), meta, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clampRect(r Rect, sw, sh int) Rect {
	x0, y0, x1, y1 := r.X, r.Y, r.X+r.W, r.Y+r.H
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	if x1 > sw {
		x1 = sw
	}
	if y1 > sh {
		y1 = sh
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}
}

func cropImage(src image.Image, r image.Rectangle) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), src, r.Min, draw.Src)
	return dst
}

func resizeNearest(src image.Image, tw, th int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	for y := 0; y < th; y++ {
		sy := sb.Min.Y + y*sh/th
		for x := 0; x < tw; x++ {
			sx := sb.Min.X + x*sw/tw
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

func drawCrosshair(src image.Image, cx, cy int) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	if cx < b.Min.X || cy < b.Min.Y || cx >= b.Max.X || cy >= b.Max.Y {
		return dst
	}
	col := color.RGBA{R: 255, G: 40, B: 40, A: 255}
	const arm = 14
	const thick = 2
	for dx := -arm; dx <= arm; dx++ {
		for t := -thick / 2; t <= thick/2; t++ {
			x, y := cx+dx, cy+t
			if x >= b.Min.X && x < b.Max.X && y >= b.Min.Y && y < b.Max.Y {
				dst.Set(x, y, col)
			}
			x, y = cx+t, cy+dx
			if x >= b.Min.X && x < b.Max.X && y >= b.Min.Y && y < b.Max.Y {
				dst.Set(x, y, col)
			}
		}
	}
	// small ring
	for _, d := range []int{-6, -5, 5, 6} {
		for _, e := range []int{-6, -5, 5, 6} {
			x, y := cx+d, cy+e
			if x >= b.Min.X && x < b.Max.X && y >= b.Min.Y && y < b.Max.Y {
				dst.Set(x, y, col)
			}
		}
	}
	return dst
}

func normalizeButton(button string) string {
	b := strings.TrimSpace(strings.ToLower(button))
	switch b {
	case "", "0", "left", "l":
		return "1"
	case "middle", "m":
		return "2"
	case "right", "r":
		return "3"
	case "1", "2", "3", "4", "5":
		return b
	default:
		if _, err := strconv.Atoi(b); err == nil && b != "0" {
			return b
		}
		return "1"
	}
}

// Click moves and clicks. Coordinates are in **screenshot image space** (meta.w×meta.h).
// Peer maps to physical/logical screen pixels. Empty button is forced to "1".
func Click(ctx context.Context, x, y int, button string) error {
	if !enabled() {
		return fmt.Errorf("desktop disabled (MARBLE_PEER_DESKTOP=0)")
	}
	button = normalizeButton(button)
	if x < 0 || y < 0 {
		return fmt.Errorf("invalid click coordinates (%d,%d)", x, y)
	}
	if x == 0 && y == 0 {
		return fmt.Errorf("invalid click coordinates (0,0) — take computer_screenshot first and click from visible UI coords")
	}
	meta := LastScreen()
	if meta.W > 0 && meta.H > 0 {
		if x >= meta.W || y >= meta.H {
			return fmt.Errorf("click (%d,%d) outside last screenshot image %dx%d (screen %dx%d scale=%.2f) — re-screenshot and remap in image space",
				x, y, meta.W, meta.H, meta.ScreenW, meta.ScreenH, meta.Scale)
		}
	}
	sx, sy := ImageToScreen(meta, x, y)
	setLastClickScreen(sx, sy)
	return clickOS(ctx, sx, sy, button)
}

// Type types text into the focused window.
func Type(ctx context.Context, text string) error {
	if !enabled() {
		return fmt.Errorf("desktop disabled (MARBLE_PEER_DESKTOP=0)")
	}
	if text == "" {
		return fmt.Errorf("empty text")
	}
	return typeOS(ctx, text)
}

// Key sends a key name (e.g. Return, ctrl+c, cmd+c).
func Key(ctx context.Context, key string) error {
	if !enabled() {
		return fmt.Errorf("desktop disabled (MARBLE_PEER_DESKTOP=0)")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("empty key")
	}
	return keyOS(ctx, key)
}

// ActiveWindow returns the foreground window's title and owning app/process
// name, best-effort (either may be empty if the platform can't determine it).
// Lets a caller answer "did my keystrokes land where I think?" from the same
// screenshot/action call instead of a second round trip — see field report
// peer-gui-loop-report (2026-09-23).
func ActiveWindow(ctx context.Context) (title, app string, err error) {
	if !enabled() {
		return "", "", fmt.Errorf("desktop disabled (MARBLE_PEER_DESKTOP=0)")
	}
	return activeWindowOS(ctx)
}

// Available reports whether desktop ops can work (tools present; permissions may still be needed).
func Available() (bool, string) {
	if !enabled() {
		return false, "disabled (MARBLE_PEER_DESKTOP=0)"
	}
	return availableOS()
}

// ProbeClick runs a no-op-ish mouse query (health).
func ProbeClick(ctx context.Context) error {
	if !enabled() {
		return fmt.Errorf("desktop disabled (MARBLE_PEER_DESKTOP=0)")
	}
	return probeClickOS(ctx)
}

// QueryPerms returns OS permission state needed for desktop capture/input.
func QueryPerms() Perms { return queryPermsOS() }

// RequestPerms triggers OS permission prompts when possible (macOS TCC).
func RequestPerms() Perms { return requestPermsOS() }

// OpenPrivacySettings opens the OS UI for Screen Recording / Accessibility.
func OpenPrivacySettings(section string) error { return openPrivacySettingsOS(section) }

// PermsHelp is operator-facing text for granting macOS TCC (empty on other OS).
func PermsHelp() string { return permsHelpOS() }

// EnsureEnv mutates env (os.Environ-style KEY=VAL slice) with graphical session vars.
// Used by desktop tools and keepalive inhibitors under systemd --user.
func EnsureEnv(env *[]string) {
	if env == nil {
		return
	}
	set := func(key, val string) {
		prefix := key + "="
		for i, e := range *env {
			if strings.HasPrefix(e, prefix) {
				if len(e) > len(prefix) {
					return
				}
				(*env)[i] = prefix + val
				return
			}
		}
		*env = append(*env, prefix+val)
	}
	get := func(key string) string {
		prefix := key + "="
		for _, e := range *env {
			if strings.HasPrefix(e, prefix) {
				return strings.TrimPrefix(e, prefix)
			}
		}
		return os.Getenv(key)
	}

	hasDisplay := get("DISPLAY") != ""
	if !hasDisplay {
		for _, d := range []string{":0", ":1"} {
			if _, err := os.Stat("/tmp/.X11-unix/X" + strings.TrimPrefix(d, ":")); err == nil {
				set("DISPLAY", d)
				hasDisplay = true
				break
			}
		}
		if !hasDisplay {
			set("DISPLAY", ":0")
		}
	}

	if get("XAUTHORITY") == "" {
		runtimeDir := get("XDG_RUNTIME_DIR")
		if runtimeDir == "" {
			runtimeDir = os.Getenv("XDG_RUNTIME_DIR")
		}
		if runtimeDir != "" {
			if matches, _ := filepath.Glob(filepath.Join(runtimeDir, ".mutter-Xwaylandauth.*")); len(matches) > 0 {
				set("XAUTHORITY", matches[0])
			}
		}
		if get("XAUTHORITY") == "" {
			if h, err := os.UserHomeDir(); err == nil {
				xa := filepath.Join(h, ".Xauthority")
				if _, err := os.Stat(xa); err == nil {
					set("XAUTHORITY", xa)
				}
			}
		}
	}

	if get("XDG_RUNTIME_DIR") == "" {
		if uid := os.Getuid(); uid > 0 {
			rd := fmt.Sprintf("/run/user/%d", uid)
			if st, err := os.Stat(rd); err == nil && st.IsDir() {
				set("XDG_RUNTIME_DIR", rd)
			}
		}
	}
	rd := get("XDG_RUNTIME_DIR")
	if rd == "" {
		rd = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	if get("WAYLAND_DISPLAY") == "" {
		if _, err := os.Stat(filepath.Join(rd, "wayland-0")); err == nil {
			set("WAYLAND_DISPLAY", "wayland-0")
		}
	}
	if get("DBUS_SESSION_BUS_ADDRESS") == "" {
		if _, err := os.Stat(filepath.Join(rd, "bus")); err == nil {
			set("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(rd, "bus"))
		}
	}

	home, _ := os.UserHomeDir()
	if home != "" {
		pathHasLocal := false
		for _, e := range *env {
			if strings.HasPrefix(e, "PATH=") && strings.Contains(e, home+"/.local/bin") {
				pathHasLocal = true
				break
			}
		}
		if !pathHasLocal {
			found := false
			for i, e := range *env {
				if strings.HasPrefix(e, "PATH=") {
					(*env)[i] = "PATH=" + filepath.Join(home, ".local/bin") + ":" + strings.TrimPrefix(e, "PATH=")
					found = true
					break
				}
			}
			if !found {
				set("PATH", filepath.Join(home, ".local/bin")+":/usr/local/bin:/usr/bin:/bin")
			}
		}
	}
}

// ensureDisplay injects a usable graphical session environment for child tools.
// Critical when marble-peer runs under systemd --user.
func ensureDisplay(cmd *exec.Cmd) {
	env := os.Environ()
	EnsureEnv(&env)
	cmd.Env = env
}

// Sleep helper for tests.
func Sleep(d time.Duration) { time.Sleep(d) }
