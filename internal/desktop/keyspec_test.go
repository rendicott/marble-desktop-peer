package desktop

import "testing"

func TestParseKeySpec(t *testing.T) {
	cases := []struct {
		in   string
		mods []uint16
		vk   uint16
		char rune
	}{
		{"Return", nil, vkReturn, 0},
		{"enter", nil, vkReturn, 0},
		{"ESC", nil, vkEscape, 0},
		{"ctrl+c", []uint16{vkControl}, 0, 'c'},
		{"cmd+v", []uint16{vkControl}, 0, 'v'}, // macOS-style chord means Ctrl on Windows
		{"ctrl+shift+Left", []uint16{vkControl, vkShift}, vkLeft, 0},
		{"alt+F4", []uint16{vkMenu}, vkF1 + 3, 0},
		{"F12", nil, vkF1 + 11, 0},
		{"win+r", []uint16{vkLWin}, 0, 'r'},
		{"super+d", []uint16{vkLWin}, 0, 'd'},
		{"PageDown", nil, vkNext, 0},
		{"shift", nil, vkShift, 0},
		{"+", nil, 0, '+'},
		{"ctrl++", []uint16{vkControl}, 0, '+'},
		{"ctrl+/", []uint16{vkControl}, 0, '/'},
		{"é", nil, 0, 'é'},
	}
	for _, c := range cases {
		got, err := parseKeySpec(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.vk != c.vk || got.char != c.char || len(got.mods) != len(c.mods) {
			t.Errorf("%q: got %+v want mods=%v vk=%#x char=%q", c.in, got, c.mods, c.vk, c.char)
			continue
		}
		for i := range c.mods {
			if got.mods[i] != c.mods[i] {
				t.Errorf("%q: mods %v want %v", c.in, got.mods, c.mods)
			}
		}
	}
}

func TestParseKeySpecErrors(t *testing.T) {
	for _, in := range []string{"", "  ", "ctrl+", "+c+", "bogus+c", "ctrl+notakey", "F0", "F25", "F1x"} {
		if _, err := parseKeySpec(in); err == nil {
			t.Errorf("%q: expected error", in)
		}
	}
}

func TestIsExtendedVK(t *testing.T) {
	if !isExtendedVK(vkLeft) || !isExtendedVK(vkDelete) {
		t.Fatal("nav keys must be extended")
	}
	if isExtendedVK(vkReturn) || isExtendedVK(vkControl) {
		t.Fatal("return/ctrl are not extended")
	}
}
