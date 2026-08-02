//go:build linux

package tray

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rendicott/marble-desktop-peer/internal/config"
)

//go:embed tray_linux.py
var trayPy []byte

func startPlatform(ctx context.Context, h Hooks) error {
	// Need a display for GTK tray
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		log.Printf("tray: no DISPLAY/WAYLAND_DISPLAY — skipping system tray")
		return nil
	}
	if _, err := exec.LookPath("python3"); err != nil {
		log.Printf("tray: python3 not found — skipping system tray")
		return nil
	}

	// Wait for mini UI address (up to ~10s)
	addr := ""
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if h.MiniUIAddr != nil {
			addr = h.MiniUIAddr()
		}
		if addr == "" {
			// state.json fallback
			if b, err := os.ReadFile(filepath.Join(config.Home(), "state.json")); err == nil {
				// tiny parse without importing encoding/json cycle issues — use simple scan
				s := string(b)
				if i := indexStr(s, `"miniui_addr"`); i >= 0 {
					addr = extractJSONString(s[i:])
				}
			}
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
	script := filepath.Join(dir, "tray_linux.py")
	if err := os.WriteFile(script, trayPy, 0o700); err != nil {
		return err
	}

	statusURL := ""
	if addr != "" {
		statusURL = addr + "/status.json"
	}

	cmd := exec.CommandContext(ctx, "python3", script)
	cmd.Env = append(os.Environ(),
		"MARBLE_PEER_PID="+strconv.Itoa(os.Getpid()),
		"MARBLE_PEER_STATUS_URL="+statusURL,
		"MARBLE_PEER_MINIUI="+addr,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start tray helper: %w", err)
	}
	log.Printf("tray: AppIndicator helper pid=%d miniui=%s", cmd.Process.Pid, addr)

	go func() {
		err := cmd.Wait()
		if ctx.Err() == nil && err != nil {
			log.Printf("tray: helper exited: %v", err)
		}
	}()

	// When ctx done, helper is killed via CommandContext
	<-ctx.Done()
	if cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
	}
	return nil
}

func indexStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// extractJSONString finds the first "..." value after a key fragment.
func extractJSONString(s string) string {
	// look for :"value"
	colon := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			colon = i
			break
		}
	}
	if colon < 0 {
		return ""
	}
	i := colon + 1
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	if i >= len(s) || s[i] != '"' {
		return ""
	}
	i++
	start := i
	for i < len(s) && s[i] != '"' {
		if s[i] == '\\' && i+1 < len(s) {
			i += 2
			continue
		}
		i++
	}
	if i > start {
		return s[start:i]
	}
	return ""
}
