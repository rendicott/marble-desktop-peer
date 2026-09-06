//go:build darwin

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

//go:embed tray_darwin.swift
var traySwift []byte

const trayHelperVersion = "1"

func startPlatform(ctx context.Context, h Hooks) error {
	if _, err := exec.LookPath("swiftc"); err != nil {
		log.Printf("tray: swiftc not found — skipping macOS menu bar extra (mini UI still works)")
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
			if b, err := os.ReadFile(filepath.Join(config.Home(), "state.json")); err == nil {
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
	src := filepath.Join(dir, "tray_darwin.swift")
	if err := os.WriteFile(src, traySwift, 0o600); err != nil {
		return err
	}
	bin := filepath.Join(dir, "marble-tray")
	needBuild := true
	if out, err := exec.CommandContext(ctx, bin, "version").CombinedOutput(); err == nil && stringsTrim(out) == trayHelperVersion {
		needBuild = false
	}
	if needBuild {
		cmd := exec.CommandContext(ctx, "swiftc", "-O", "-o", bin, src)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("swiftc tray: %v: %s", err, stringsTrim(out))
		}
		_ = exec.Command("codesign", "-s", "-", "-f", bin).Run()
	}

	statusURL := ""
	if addr != "" {
		statusURL = addr + "/status.json"
	}
	cmd := exec.CommandContext(ctx, bin)
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
	log.Printf("tray: macOS menu bar helper pid=%d miniui=%s", cmd.Process.Pid, addr)

	go func() {
		err := cmd.Wait()
		if ctx.Err() == nil && err != nil {
			log.Printf("tray: helper exited: %v", err)
		}
	}()

	<-ctx.Done()
	if cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
	}
	return nil
}

func stringsTrim(b []byte) string {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\r' || b[j-1] == '\t') {
		j--
	}
	return string(b[i:j])
}

func indexStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func extractJSONString(s string) string {
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
