package desktop

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Windows virtual-key codes used by the key-name parser. The parser is
// OS-independent (and unit-tested on every platform); only desktop_windows.go
// turns a keySpec into SendInput events.
const (
	vkBack    = 0x08
	vkTab     = 0x09
	vkReturn  = 0x0D
	vkShift   = 0x10
	vkControl = 0x11
	vkMenu    = 0x12 // Alt
	vkEscape  = 0x1B
	vkSpace   = 0x20
	vkPrior   = 0x21 // Page Up
	vkNext    = 0x22 // Page Down
	vkEnd     = 0x23
	vkHome    = 0x24
	vkLeft    = 0x25
	vkUp      = 0x26
	vkRight   = 0x27
	vkDown    = 0x28
	vkInsert  = 0x2D
	vkDelete  = 0x2E
	vkLWin    = 0x5B
	vkF1      = 0x70
)

// keySpec is a parsed "ctrl+shift+t" style key chord.
type keySpec struct {
	mods []uint16 // modifier virtual keys, pressed in order and released in reverse
	vk   uint16   // non-zero for named keys
	char rune     // non-zero for a single printable character (layout-resolved on Windows)
}

// isExtendedVK reports whether the key needs KEYEVENTF_EXTENDEDKEY so the
// navigation cluster is not mistaken for the numpad.
func isExtendedVK(vk uint16) bool {
	switch vk {
	case vkPrior, vkNext, vkEnd, vkHome, vkLeft, vkUp, vkRight, vkDown, vkInsert, vkDelete, vkLWin:
		return true
	}
	return false
}

var namedKeys = map[string]uint16{
	"return": vkReturn, "enter": vkReturn,
	"tab":    vkTab,
	"escape": vkEscape, "esc": vkEscape,
	"space":     vkSpace,
	"backspace": vkBack,
	"delete":    vkDelete, "del": vkDelete,
	"insert": vkInsert, "ins": vkInsert,
	"home": vkHome, "end": vkEnd,
	"pageup": vkPrior, "page_up": vkPrior, "prior": vkPrior,
	"pagedown": vkNext, "page_down": vkNext, "next": vkNext,
	"up": vkUp, "down": vkDown, "left": vkLeft, "right": vkRight,
}

// modifierKeys maps chord modifiers to virtual keys. "cmd" is treated as Ctrl
// because the harness models emit macOS-style chords (cmd+c) and on Windows the
// intent is nearly always copy/paste/select-all. Use "win"/"super"/"meta" for
// the actual Windows key.
var modifierKeys = map[string]uint16{
	"ctrl": vkControl, "control": vkControl, "cmd": vkControl, "command": vkControl,
	"shift": vkShift,
	"alt":   vkMenu, "option": vkMenu, "opt": vkMenu,
	"win": vkLWin, "super": vkLWin, "meta": vkLWin,
}

// parseKeySpec parses names like "Return", "ctrl+c", "ctrl+shift+Left", "F5", "alt+F4".
func parseKeySpec(key string) (keySpec, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return keySpec{}, fmt.Errorf("empty key")
	}
	// A bare "+" is the plus key, and "ctrl++" is ctrl and plus.
	var parts []string
	switch {
	case key == "+":
		parts = []string{"+"}
	case strings.HasSuffix(key, "++"):
		parts = append(strings.Split(strings.TrimSuffix(key, "++"), "+"), "+")
	default:
		parts = strings.Split(key, "+")
	}

	var spec keySpec
	for i, raw := range parts {
		name := strings.TrimSpace(raw)
		if name == "" {
			return keySpec{}, fmt.Errorf("invalid key %q", key)
		}
		last := i == len(parts)-1
		lower := strings.ToLower(name)
		if !last {
			vk, ok := modifierKeys[lower]
			if !ok {
				return keySpec{}, fmt.Errorf("unknown modifier %q in %q", name, key)
			}
			spec.mods = append(spec.mods, vk)
			continue
		}
		if vk, ok := namedKeys[lower]; ok {
			spec.vk = vk
			return spec, nil
		}
		if vk, ok := modifierKeys[lower]; ok { // a lone "shift" / "ctrl" / "win" tap
			spec.vk = vk
			return spec, nil
		}
		if len(lower) >= 2 && lower[0] == 'f' {
			var n int
			if _, err := fmt.Sscanf(lower[1:], "%d", &n); err == nil && n >= 1 && n <= 24 && fmt.Sprint(n) == lower[1:] {
				spec.vk = uint16(vkF1 + n - 1)
				return spec, nil
			}
		}
		if utf8.RuneCountInString(name) == 1 {
			r, _ := utf8.DecodeRuneInString(name)
			spec.char = r
			return spec, nil
		}
		return keySpec{}, fmt.Errorf("unknown key %q", name)
	}
	return spec, nil
}
