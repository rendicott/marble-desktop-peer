// Package desktop provides OS-level screenshot and input for marble-peer.
//
// Linux: GNOME/XWayland tools (grim, gnome-screenshot, xdotool).
// macOS: screencapture + a compiled Swift helper (CGEvent) for click/type/key.
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

// ScreenMeta describes the primary display capture (possibly downscaled for transport).
type ScreenMeta struct {
	W       int     `json:"w"`                  // JPEG / image width (model click space)
	H       int     `json:"h"`                  // JPEG / image height
	Scale   float64 `json:"scale"`              // screenW / imageW (1 if unscaled)
	ScreenW int     `json:"screen_w,omitempty"` // click-space width (physical or logical points)
	ScreenH int     `json:"screen_h,omitempty"` // click-space height
}

// Perms is a best-effort macOS TCC snapshot. Other OSes report n/a.
type Perms struct {
	ScreenRecording string `json:"screen_recording"` // granted|denied|unknown|n/a
	Accessibility   string `json:"accessibility"`    // granted|denied|unknown|n/a
	Note            string `json:"note,omitempty"`
}

var (
	screenMu     sync.Mutex
	lastScreen   ScreenMeta
	lastClickImg struct {
		X, Y int
		Set  bool
	} // image-space coords for overlay
)

// LastScreen returns dimensions from the most recent Screenshot (if any).
func LastScreen() ScreenMeta {
	screenMu.Lock()
	defer screenMu.Unlock()
	return lastScreen
}

// MetaMap returns JSON-friendly screenshot metadata for the protocol envelope.
func MetaMap(meta ScreenMeta, extra map[string]interface{}) map[string]interface{} {
	m := map[string]interface{}{
		"w": meta.W, "h": meta.H, "scale": meta.Scale,
		"screen_w": meta.ScreenW, "screen_h": meta.ScreenH,
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// SetLastClickImage records the last click in image pixel space (for crosshair overlay).
func SetLastClickImage(x, y int) {
	screenMu.Lock()
	lastClickImg.X, lastClickImg.Y, lastClickImg.Set = x, y, true
	screenMu.Unlock()
}

// ImageToScreen maps image-space click coords to physical/logical screen pixels.
func ImageToScreen(meta ScreenMeta, ix, iy int) (sx, sy int) {
	sw, sh := meta.ScreenW, meta.ScreenH
	if sw <= 0 {
		sw = meta.W
	}
	if sh <= 0 {
		sh = meta.H
	}
	if meta.W <= 0 || meta.H <= 0 {
		return ix, iy
	}
	sx = ix * sw / meta.W
	sy = iy * sh / meta.H
	return sx, sy
}

func enabled() bool {
	v := strings.TrimSpace(os.Getenv("MARBLE_PEER_DESKTOP"))
	if v == "0" || strings.EqualFold(v, "false") {
		return false
	}
	return true
}

// Screenshot captures the primary display as JPEG bytes.
func Screenshot(ctx context.Context) ([]byte, ScreenMeta, error) {
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
	return encodeShotMapped(out, coordW, coordH)
}

func encodeShot(path string) ([]byte, ScreenMeta, error) {
	return encodeShotMapped(path, 0, 0)
}

// encodeShotMapped JPEG-encodes path. If coordW/coordH > 0 they are the click
// coordinate space (e.g. macOS logical points on a Retina display); otherwise
// the decoded image pixel size is used (Linux).
func encodeShotMapped(path string, coordW, coordH int) ([]byte, ScreenMeta, error) {
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

	scaled := img
	imgW, imgH := imgW0, imgH0
	scale := float64(screenW) / float64(imgW)
	if max(imgW, imgH) > MaxScreenshotEdge {
		if imgW >= imgH {
			imgW = MaxScreenshotEdge
			imgH = imgH0 * MaxScreenshotEdge / imgW0
		} else {
			imgH = MaxScreenshotEdge
			imgW = imgW0 * MaxScreenshotEdge / imgH0
		}
		if imgW < 1 {
			imgW = 1
		}
		if imgH < 1 {
			imgH = 1
		}
		scale = float64(screenW) / float64(imgW)
		scaled = resizeNearest(img, imgW, imgH)
	}

	// Draw last-click crosshair in image space (helps vision see misses).
	screenMu.Lock()
	lcX, lcY, lcSet := lastClickImg.X, lastClickImg.Y, lastClickImg.Set
	screenMu.Unlock()
	if lcSet {
		scaled = drawCrosshair(scaled, lcX, lcY)
	}

	meta := ScreenMeta{
		W: imgW, H: imgH, Scale: scale,
		ScreenW: screenW, ScreenH: screenH,
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
	SetLastClickImage(x, y)
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
