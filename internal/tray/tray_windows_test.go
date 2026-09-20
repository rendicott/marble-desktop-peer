//go:build windows

package tray

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The tray helper is a PowerShell script that only runs inside an interactive
// desktop session, so at minimum prove it parses under the real PowerShell parser.
func TestTrayScriptParses(t *testing.T) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("powershell.exe not available")
	}
	script := filepath.Join(t.TempDir(), "tray_windows.ps1")
	if err := os.WriteFile(script, trayPS, 0o600); err != nil {
		t.Fatal(err)
	}
	const check = `$errs = $null; $null = [System.Management.Automation.Language.Parser]::ParseFile($env:TRAY_PS1, [ref]$null, [ref]$errs); if ($errs -and $errs.Count -gt 0) { $errs | ForEach-Object { Write-Output ($_.Extent.StartLineNumber.ToString() + ': ' + $_.Message) }; exit 1 }`
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", check)
	cmd.Env = append(os.Environ(), "TRAY_PS1="+script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tray_windows.ps1 has parse errors: %v\n%s", err, out)
	}
}

func TestTrayScriptIsASCII(t *testing.T) {
	for i, b := range trayPS {
		if b > 0x7f {
			t.Fatalf("non-ASCII byte at offset %d: Windows PowerShell 5.1 reads BOM-less files as ANSI", i)
		}
	}
}
