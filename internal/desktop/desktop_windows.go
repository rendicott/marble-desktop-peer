//go:build windows

package desktop

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unicode"
	"unicode/utf16"
	"unsafe"
)

// Windows desktop control with no CGO and no helper binaries: GDI BitBlt for
// screenshots, SendInput for mouse/keyboard. The process is made DPI-aware at
// startup so screenshot pixels and click coordinates are both physical pixels
// (otherwise a 150% display reports a scaled-down virtual size and the click
// space no longer matches the captured image).

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")

	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	procGetSystemMetrics              = user32.NewProc("GetSystemMetrics")
	procGetDC                         = user32.NewProc("GetDC")
	procReleaseDC                     = user32.NewProc("ReleaseDC")
	procSetCursorPos                  = user32.NewProc("SetCursorPos")
	procGetCursorPos                  = user32.NewProc("GetCursorPos")
	procSendInput                     = user32.NewProc("SendInput")
	procVkKeyScanW                    = user32.NewProc("VkKeyScanW")
	procMapVirtualKeyW                = user32.NewProc("MapVirtualKeyW")
	procGetForegroundWindow           = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW                = user32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW          = user32.NewProc("GetWindowTextLengthW")
	procGetWindowThreadProcessId      = user32.NewProc("GetWindowThreadProcessId")

	procCreateCompatibleDC     = gdi32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBitmap = gdi32.NewProc("CreateCompatibleBitmap")
	procSelectObject           = gdi32.NewProc("SelectObject")
	procBitBlt                 = gdi32.NewProc("BitBlt")
	procGetDIBits              = gdi32.NewProc("GetDIBits")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procDeleteDC               = gdi32.NewProc("DeleteDC")

	// kernel32 itself is declared once in lock_windows.go (same package, same
	// build tag) — reused here for the new procs below.
	procOpenProcess                = kernel32.NewProc("OpenProcess")
	procCloseHandle                = kernel32.NewProc("CloseHandle")
	procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")
)

func init() {
	if procSetProcessDpiAwarenessContext.Find() == nil {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 is (HANDLE)-4 (Windows 10 1703+).
		if r, _, _ := procSetProcessDpiAwarenessContext.Call(^uintptr(3)); r != 0 {
			return
		}
	}
	_, _, _ = procSetProcessDPIAware.Call()
}

const (
	smCxScreen = 0
	smCyScreen = 1

	srcCopy    = 0x00CC0020
	captureBlt = 0x40000000 // include layered / topmost windows
	biRGB      = 0

	inputMouse    = 0
	inputKeyboard = 1

	mouseeventfLeftDown   = 0x0002
	mouseeventfLeftUp     = 0x0004
	mouseeventfRightDown  = 0x0008
	mouseeventfRightUp    = 0x0010
	mouseeventfMiddleDown = 0x0020
	mouseeventfMiddleUp   = 0x0040
	mouseeventfWheel      = 0x0800

	keyeventfExtended = 0x0001
	keyeventfKeyUp    = 0x0002
	keyeventfUnicode  = 0x0004

	wheelDelta = 120
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

// INPUT is a tagged union of MOUSEINPUT / KEYBDINPUT / HARDWAREINPUT. Go has no
// unions, so the mouse and keyboard variants are separate structs padded to the
// same size (40 bytes on 64-bit, 28 on 32-bit); SendInput is told that size.
type mouseInput struct {
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	extraInfo uintptr
}

type keybdInput struct {
	vk        uint16
	scan      uint16
	flags     uint32
	time      uint32
	extraInfo uintptr
}

type inputMouseEvent struct {
	typ uint32
	mi  mouseInput
}

type inputKeyEvent struct {
	typ uint32
	ki  keybdInput
	_   [8]byte // pad to sizeof(mouseInput)
}

func screenSize() (w, h int) {
	cx, _, _ := procGetSystemMetrics.Call(smCxScreen)
	cy, _, _ := procGetSystemMetrics.Call(smCyScreen)
	return int(int32(cx)), int(int32(cy))
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func screenshotOS(ctx context.Context, out string) (coordW, coordH int, err error) {
	_ = ctx
	w, h := screenSize()
	if w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("screenshot: no primary display (is peer running in an interactive desktop session?)")
	}
	img, err := captureScreen(w, h)
	if err != nil {
		return 0, 0, err
	}
	f, err := os.Create(out)
	if err != nil {
		return 0, 0, err
	}
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(f, img); err != nil {
		_ = f.Close()
		return 0, 0, err
	}
	if err := f.Close(); err != nil {
		return 0, 0, err
	}
	return w, h, nil
}

// captureScreen copies the primary display into an RGBA image via GDI.
func captureScreen(w, h int) (*image.RGBA, error) {
	screen, _, _ := procGetDC.Call(0)
	if screen == 0 {
		return nil, fmt.Errorf("screenshot: GetDC(NULL) failed")
	}
	defer procReleaseDC.Call(0, screen)

	mem, _, _ := procCreateCompatibleDC.Call(screen)
	if mem == 0 {
		return nil, fmt.Errorf("screenshot: CreateCompatibleDC failed")
	}
	defer procDeleteDC.Call(mem)

	bmp, _, _ := procCreateCompatibleBitmap.Call(screen, uintptr(w), uintptr(h))
	if bmp == 0 {
		return nil, fmt.Errorf("screenshot: CreateCompatibleBitmap %dx%d failed", w, h)
	}
	defer procDeleteObject.Call(bmp)

	old, _, _ := procSelectObject.Call(mem, bmp)
	r, _, e := procBitBlt.Call(mem, 0, 0, uintptr(w), uintptr(h), screen, 0, 0, srcCopy|captureBlt)
	// GetDIBits requires the bitmap not be selected into a DC.
	_, _, _ = procSelectObject.Call(mem, old)
	if r == 0 {
		return nil, fmt.Errorf("screenshot: BitBlt failed: %v (locked screen, disconnected remote session, or secure desktop?)", e)
	}

	pix := make([]byte, w*h*4)
	bi := bitmapInfo{Header: bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(w),
		Height:      -int32(h), // negative = top-down rows
		Planes:      1,
		BitCount:    32,
		Compression: biRGB,
	}}
	r, _, e = procGetDIBits.Call(screen, bmp, 0, uintptr(h),
		uintptr(unsafe.Pointer(&pix[0])), uintptr(unsafe.Pointer(&bi)), 0 /* DIB_RGB_COLORS */)
	if r == 0 {
		return nil, fmt.Errorf("screenshot: GetDIBits failed: %v", e)
	}
	for i := 0; i+3 < len(pix); i += 4 { // BGRA -> RGBA, force opaque
		pix[i], pix[i+2] = pix[i+2], pix[i]
		pix[i+3] = 0xFF
	}
	return &image.RGBA{Pix: pix, Stride: w * 4, Rect: image.Rect(0, 0, w, h)}, nil
}

func sendMouse(flags uint32, data int32) error {
	in := inputMouseEvent{typ: inputMouse, mi: mouseInput{flags: flags, mouseData: uint32(data)}}
	return sendInput(unsafe.Pointer(&in), 1, unsafe.Sizeof(in))
}

func sendKeys(evs []inputKeyEvent) error {
	if len(evs) == 0 {
		return nil
	}
	return sendInput(unsafe.Pointer(&evs[0]), len(evs), unsafe.Sizeof(evs[0]))
}

func sendInput(p unsafe.Pointer, n int, size uintptr) error {
	r, _, e := procSendInput.Call(uintptr(n), uintptr(p), size)
	if int(r) != n {
		return fmt.Errorf("SendInput injected %d/%d events: %v (target window may be elevated; UIPI blocks input to higher-integrity apps)", r, n, e)
	}
	return nil
}

func clickOS(ctx context.Context, sx, sy int, button string) error {
	var down, up uint32
	var wheel int32
	switch button {
	case "1":
		down, up = mouseeventfLeftDown, mouseeventfLeftUp
	case "2":
		down, up = mouseeventfMiddleDown, mouseeventfMiddleUp
	case "3":
		down, up = mouseeventfRightDown, mouseeventfRightUp
	case "4": // X11 convention (xdotool click 4/5): scroll up / down
		wheel = wheelDelta
	case "5":
		wheel = -wheelDelta
	default:
		return fmt.Errorf("desktop click: unsupported mouse button %q", button)
	}
	if r, _, e := procSetCursorPos.Call(uintptr(sx), uintptr(sy)); r == 0 {
		return fmt.Errorf("desktop click: SetCursorPos(%d,%d): %v", sx, sy, e)
	}
	sleepCtx(ctx, 40*time.Millisecond) // let hover/mouse-move handlers see the pointer first
	if err := ctx.Err(); err != nil {
		return err
	}
	if wheel != 0 {
		return sendMouse(mouseeventfWheel, wheel)
	}
	if err := sendMouse(down, 0); err != nil {
		return fmt.Errorf("desktop click (screen=%d,%d): %w", sx, sy, err)
	}
	sleepCtx(ctx, 25*time.Millisecond)
	if err := sendMouse(up, 0); err != nil {
		return fmt.Errorf("desktop click (screen=%d,%d): %w", sx, sy, err)
	}
	return nil
}

func keyEvent(vk uint16, up bool) inputKeyEvent {
	scan, _, _ := procMapVirtualKeyW.Call(uintptr(vk), 0 /* MAPVK_VK_TO_VSC */)
	var flags uint32
	if isExtendedVK(vk) {
		flags |= keyeventfExtended
	}
	if up {
		flags |= keyeventfKeyUp
	}
	return inputKeyEvent{typ: inputKeyboard, ki: keybdInput{vk: vk, scan: uint16(scan), flags: flags}}
}

func unicodeEvent(u uint16, up bool) inputKeyEvent {
	flags := uint32(keyeventfUnicode)
	if up {
		flags |= keyeventfKeyUp
	}
	return inputKeyEvent{typ: inputKeyboard, ki: keybdInput{scan: u, flags: flags}}
}

func typeOS(ctx context.Context, text string) error {
	var evs []inputKeyEvent
	flush := func() error {
		err := sendKeys(evs)
		evs = evs[:0]
		sleepCtx(ctx, 8*time.Millisecond) // don't flood the target's input queue
		return err
	}
	for _, u := range utf16.Encode([]rune(text)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch u {
		case '\r':
			continue // "\r\n" is sent as one Return via the '\n' case
		case '\n':
			evs = append(evs, keyEvent(vkReturn, false), keyEvent(vkReturn, true))
		case '\t':
			evs = append(evs, keyEvent(vkTab, false), keyEvent(vkTab, true))
		default:
			evs = append(evs, unicodeEvent(u, false), unicodeEvent(u, true))
		}
		if len(evs) >= 64 {
			if err := flush(); err != nil {
				return fmt.Errorf("type: %w", err)
			}
		}
	}
	if err := flush(); err != nil {
		return fmt.Errorf("type: %w", err)
	}
	return nil
}

func keyOS(ctx context.Context, key string) error {
	spec, err := parseKeySpec(key)
	if err != nil {
		return fmt.Errorf("key: %w", err)
	}
	vk, mods := spec.vk, append([]uint16(nil), spec.mods...)
	if spec.char != 0 {
		ch := spec.char
		if len(mods) > 0 && unicode.IsUpper(ch) { // "ctrl+C" means ctrl+c, not ctrl+shift+c
			ch = unicode.ToLower(ch)
		}
		r, _, _ := procVkKeyScanW.Call(uintptr(ch))
		scan := int16(r)
		if scan == -1 { // no key produces this character on the active layout
			if len(mods) > 0 {
				return fmt.Errorf("key: %q has no key on the current keyboard layout", key)
			}
			return typeOS(ctx, string(ch))
		}
		vk = uint16(scan) & 0xff
		state := (scan >> 8) & 0xff
		addMod := func(m uint16) {
			for _, have := range mods {
				if have == m {
					return
				}
			}
			mods = append(mods, m)
		}
		if state&1 != 0 {
			addMod(vkShift)
		}
		if state&2 != 0 {
			addMod(vkControl)
		}
		if state&4 != 0 {
			addMod(vkMenu)
		}
	}

	evs := make([]inputKeyEvent, 0, 2*len(mods)+2)
	for _, m := range mods {
		evs = append(evs, keyEvent(m, false))
	}
	evs = append(evs, keyEvent(vk, false), keyEvent(vk, true))
	for i := len(mods) - 1; i >= 0; i-- {
		evs = append(evs, keyEvent(mods[i], true))
	}
	if err := sendKeys(evs); err != nil {
		return fmt.Errorf("key %q: %w", key, err)
	}
	return nil
}

const processQueryLimitedInformation = 0x1000

// activeWindowOS returns the foreground window's title and its owning
// process's executable name (closest Windows analog to an "app name").
func activeWindowOS(ctx context.Context) (title, app string, err error) {
	_ = ctx
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return "", "", fmt.Errorf("GetForegroundWindow: no foreground window")
	}
	if ln, _, _ := procGetWindowTextLengthW.Call(hwnd); ln > 0 {
		buf := make([]uint16, ln+1)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		title = syscall.UTF16ToString(buf)
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != 0 {
		app = processNameByPID(pid)
	}
	return title, app, nil
}

// processNameByPID resolves a PID to its executable base name via
// QueryFullProcessImageNameW (Vista+). Returns "" on any failure — this is
// advisory metadata, never worth failing the caller over.
func processNameByPID(pid uint32) string {
	h, _, _ := procOpenProcess.Call(uintptr(processQueryLimitedInformation), 0, uintptr(pid))
	if h == 0 {
		return ""
	}
	defer procCloseHandle.Call(h)
	buf := make([]uint16, 260)
	size := uint32(len(buf))
	r, _, _ := procQueryFullProcessImageNameW.Call(
		h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return ""
	}
	return filepath.Base(syscall.UTF16ToString(buf[:size]))
}

func availableOS() (bool, string) {
	w, h := screenSize()
	if w <= 0 || h <= 0 {
		return false, "no primary display (not an interactive desktop session?)"
	}
	return true, fmt.Sprintf("Win32 GDI capture + SendInput, primary display %dx%d px", w, h)
}

func probeClickOS(ctx context.Context) error {
	_ = ctx
	var pt struct{ x, y int32 }
	if r, _, e := procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt))); r == 0 {
		return fmt.Errorf("GetCursorPos: %v", e)
	}
	return nil
}

func queryPermsOS() Perms {
	return Perms{ScreenRecording: "n/a", Accessibility: "n/a", Note: "windows"}
}

func requestPermsOS() Perms { return queryPermsOS() }

func openPrivacySettingsOS(section string) error {
	_ = section
	return fmt.Errorf("privacy settings UI is macOS-only")
}

func permsHelpOS() string { return "" }
