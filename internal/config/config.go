package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type File struct {
	HarnessURL  string `json:"harness_url"`
	DeviceID    string `json:"device_id"`
	ComputerID  string `json:"computer_id"`
	LogLevel    string `json:"log_level"`
	MiniUIPort  int    `json:"miniui_port"`
	KillBrowser bool   `json:"kill_browser_on_exit"`
	// BrowserMode: "user" (default) = real Chrome profile / attach CDP;
	// "marble" = isolated ~/.marble-peer/chrome-profile (no daily logins).
	BrowserMode string `json:"browser_mode,omitempty"`
	// CDPPort: prefer attach to this port (0 = auto 9222/…); env MARBLE_PEER_CDP_PORT overrides.
	CDPPort int `json:"cdp_port,omitempty"`
}

func Home() string {
	if v := os.Getenv("MARBLE_PEER_HOME"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".marble-peer")
}

func EnsureHome() error {
	return os.MkdirAll(Home(), 0o700)
}

func Path() string { return filepath.Join(Home(), "config.json") }

func Load() (File, error) {
	var f File
	b, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return f, nil
		}
		return f, err
	}
	err = json.Unmarshal(b, &f)
	return f, err
}

func Save(f File) error {
	if err := EnsureHome(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Path(), b, 0o600)
}

func CredsPath() string { return filepath.Join(Home(), "credentials") }

func LoadToken() (string, error) {
	b, err := os.ReadFile(CredsPath())
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func SaveToken(tok string) error {
	if err := EnsureHome(); err != nil {
		return err
	}
	return os.WriteFile(CredsPath(), []byte(tok), 0o600)
}

func ClearToken() error {
	_ = os.Remove(CredsPath())
	return nil
}

// LockPath is the single-instance flock for `marble-peer run`.
func LockPath() string { return filepath.Join(Home(), "marble-peer.lock") }

// AcquireRunLock prevents two peers from thrashing the same computer_id on the harness.
// Returns an open locked file that must stay open for the process lifetime.
func AcquireRunLock() (*os.File, error) {
	if err := EnsureHome(); err != nil {
		return nil, err
	}
	f, err := tryFlock()
	if err == nil {
		return f, nil
	}
	// flock busy — find who holds it (never include our own pid)
	self := os.Getpid()
	holders := findPeerHolders()
	// Drop dead PIDs and ourselves from lock file / pgrep noise
	var live []int
	for _, p := range holders {
		if p == self {
			continue
		}
		if pidAlive(p) {
			live = append(live, p)
		}
	}
	if len(live) == 0 {
		// Stale lock (empty/corrupt file, owner dead). Remove and retry once.
		_ = os.Remove(LockPath())
		f, err = tryFlock()
		if err == nil {
			return f, nil
		}
		return nil, fmt.Errorf(
			"could not acquire lock %s even after clearing stale file.\n  try:  rm -f %s && pkill -x marble-peer; marble-peer run",
			LockPath(), LockPath(),
		)
	}
	// Format kill help with full PIDs
	parts := make([]string, len(live))
	for i, p := range live {
		parts[i] = strconv.Itoa(p)
	}
	list := strings.Join(parts, " ")
	return nil, fmt.Errorf(
		"another marble-peer is already running (pid %s).\n  stop it:  kill %s\n  or:       pkill -x marble-peer\n  force:    kill -9 %s; rm -f %s",
		list, list, list, LockPath(),
	)
}

func tryFlock() (*os.File, error) {
	f, err := os.OpenFile(LockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, err
	}
	// Write PID only after we own the lock (do not truncate until then).
	_ = f.Truncate(0)
	_, _ = f.Seek(0, 0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Sync()
	return f, nil
}

// findPeerHolders collects candidate PIDs: lock file contents, pgrep marble-peer, lock file openers.
func findPeerHolders() []int {
	seen := map[int]bool{}
	var out []int
	add := func(p int) {
		if p > 0 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	// 1) PID written in lock file
	if b, err := os.ReadFile(LockPath()); err == nil {
		for _, tok := range strings.Fields(string(b)) {
			if p, err := strconv.Atoi(tok); err == nil {
				add(p)
			}
		}
	}
	// 2) processes named marble-peer
	if matches, err := filepath.Glob("/proc/[0-9]*/comm"); err == nil {
		for _, path := range matches {
			b, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			if strings.TrimSpace(string(b)) != "marble-peer" {
				continue
			}
			// /proc/<pid>/comm
			dir := filepath.Dir(path)
			pidStr := filepath.Base(dir)
			if p, err := strconv.Atoi(pidStr); err == nil {
				add(p)
			}
		}
	}
	// 3) anyone with the lock path open (fd scan is expensive; skip if we already have holders)
	if len(out) == 0 {
		for _, p := range lockFileOpeners() {
			add(p)
		}
	}
	return out
}

func lockFileOpeners() []int {
	lock := LockPath()
	var pids []int
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join("/proc", e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if target == lock {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	// Signal 0 checks existence without killing.
	err := syscall.Kill(pid, 0)
	return err == nil
}

func StatePath() string { return filepath.Join(Home(), "state.json") }

func WriteState(v map[string]interface{}) error {
	if err := EnsureHome(); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return os.WriteFile(StatePath(), b, 0o600)
}
