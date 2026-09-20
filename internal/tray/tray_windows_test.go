//go:build windows

package tray

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
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

// syncBuffer lets the test read helper output while the process is still writing it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Runs the real helper against a fake mini UI: it must create the NotifyIcon and
// menu, poll /status.json on its 2s timer (immediate paint + at least one tick),
// take the pending-confirmation branch, and not log any refresh error.
func TestTrayHelperPollsStatus(t *testing.T) {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		t.Skip("powershell.exe not available")
	}
	var polls int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status.json" {
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(&polls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"Online","computer_id":"ci-test","browser_ready":true,` +
			`"pending_confirms":[{"url":"` + srv.URL + `/confirm/x","prompt":"p","risk":"low"}],"confirm_count":1,` +
			`"miniui_addr":"` + srv.URL + `"}`))
	}))
	defer srv.Close()

	script := filepath.Join(t.TempDir(), "tray_windows.ps1")
	if err := os.WriteFile(script, trayPS, 0o600); err != nil {
		t.Fatal(err)
	}
	var out syncBuffer
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass", "-File", script)
	cmd.Env = append(os.Environ(),
		"MARBLE_PEER_PID="+strconv.Itoa(os.Getpid()),
		"MARBLE_PEER_STATUS_URL="+srv.URL+"/status.json",
		"MARBLE_PEER_MINIUI="+srv.URL,
	)
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer func() {
		_ = cmd.Process.Kill()
	}()

	deadline := time.After(40 * time.Second) // powershell + WinForms cold start can be slow on CI
	for atomic.LoadInt32(&polls) < 2 {
		select {
		case err := <-exited:
			t.Fatalf("tray helper exited early (polls=%d): %v\n%s", atomic.LoadInt32(&polls), err, out.String())
		case <-deadline:
			t.Fatalf("tray helper polled status %d times in 40s (want >=2)\n%s", atomic.LoadInt32(&polls), out.String())
		case <-time.After(250 * time.Millisecond):
		}
	}
	if s := out.String(); strings.Contains(s, "tray refresh") || strings.Contains(strings.ToLower(s), "exception") {
		t.Fatalf("tray helper reported errors:\n%s", s)
	}
	select {
	case err := <-exited:
		t.Fatalf("tray helper died while running: %v\n%s", err, out.String())
	default:
	}
}
