//go:build windows

package tray

import (
	"context"
	_ "embed"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/rendicott/marble-desktop-peer/internal/config"
)

//go:embed tray_windows.ps1
var trayPS []byte

// createNoWindow gives the helper its own hidden console. Without it the child
// would share the peer's console (and a hidden-window PowerShell would hide the
// operator's terminal).
const createNoWindow = 0x08000000

func startPlatform(ctx context.Context, h Hooks) error {
	ps, err := exec.LookPath("powershell.exe")
	if err != nil {
		log.Printf("tray: powershell.exe not found — skipping notification-area icon (mini UI still works)")
		<-ctx.Done()
		return ctx.Err()
	}

	addr := ""
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if h.MiniUIAddr != nil {
			addr = h.MiniUIAddr()
		}
		if addr == "" {
			addr = miniUIFromState()
		}
		if addr != "" {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}

	dir := filepath.Join(config.Home(), "tray")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	script := filepath.Join(dir, "tray_windows.ps1")
	if err := os.WriteFile(script, trayPS, 0o600); err != nil {
		return err
	}

	statusURL := ""
	if addr != "" {
		statusURL = addr + "/status.json"
	}
	cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass", "-File", script)
	cmd.Env = append(os.Environ(),
		"MARBLE_PEER_PID="+strconv.Itoa(os.Getpid()),
		"MARBLE_PEER_STATUS_URL="+statusURL,
		"MARBLE_PEER_MINIUI="+addr,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		return err
	}
	log.Printf("tray: Windows notification-area helper pid=%d miniui=%s", cmd.Process.Pid, addr)

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if ctx.Err() == nil && err != nil {
			log.Printf("tray: helper exited: %v (blocked by PowerShell execution policy? set MARBLE_PEER_NO_TRAY=1 to silence)", err)
		}
	case <-ctx.Done():
		// Do not kill: a killed NotifyIcon leaves a ghost icon in the tray until hovered.
		// The helper watches MARBLE_PEER_PID, removes its icon and exits within ~2s.
	}
	return nil
}

func miniUIFromState() string {
	b, err := os.ReadFile(filepath.Join(config.Home(), "state.json"))
	if err != nil {
		return ""
	}
	var st struct {
		MiniUIAddr string `json:"miniui_addr"`
	}
	if json.Unmarshal(b, &st) != nil {
		return ""
	}
	return st.MiniUIAddr
}
