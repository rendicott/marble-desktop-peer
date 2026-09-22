package autostart

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func TestWindowsLauncherVBS(t *testing.T) {
	got := windowsLauncherVBS(`C:\Users\Zoë Ada\bin\marble-peer.exe`)
	// VBScript builds the quoted command line from concatenated string literals.
	want := `CreateObject("WScript.Shell").Run """" & "C:\Users\Zoë Ada\bin\marble-peer.exe" & """ run", 0, False`
	if !strings.Contains(got, want) {
		t.Fatalf("launcher:\n%s\nwant line:\n%s", got, want)
	}
	if !strings.Contains(got, "\r\n") {
		t.Fatal("VBS needs CRLF line endings")
	}
}

func TestUTF16LEWithBOMRoundTrip(t *testing.T) {
	in := "Zoë \U0001F600 run"
	b := utf16LEWithBOM(in)
	if b[0] != 0xFF || b[1] != 0xFE {
		t.Fatalf("missing BOM: % x", b[:2])
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 2; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	if got := string(utf16.Decode(u)); got != in {
		t.Fatalf("round trip = %q", got)
	}
}
