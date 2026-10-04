package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type File struct {
	// HarnessURL / ComputerID are the pre-multi-harness single pairing. Load
	// migrates them into Harnesses; they are never written back.
	HarnessURL string `json:"harness_url,omitempty"`
	ComputerID string `json:"computer_id,omitempty"`
	// Harnesses is every Marble harness this peer is paired with. The peer
	// connects to all of them; a harness must hold the peer lock to drive it.
	Harnesses   []Harness `json:"harnesses,omitempty"`
	DeviceID    string    `json:"device_id"`
	LogLevel    string    `json:"log_level"`
	MiniUIPort  int       `json:"miniui_port"`
	KillBrowser bool      `json:"kill_browser_on_exit"`
	// BrowserMode: "user" (default) = real Chrome profile / attach CDP;
	// "marble" = isolated ~/.marble-peer/chrome-profile (no daily logins).
	BrowserMode string `json:"browser_mode,omitempty"`
	// CDPPort: prefer attach to this port (0 = auto 9222/…); env MARBLE_PEER_CDP_PORT overrides.
	CDPPort int `json:"cdp_port,omitempty"`
}

// Harness is one paired Marble harness. Its device token lives in credentials.json.
type Harness struct {
	URL        string `json:"url"`
	ComputerID string `json:"computer_id"`
}

// NormalizeURL is the key used for a harness in config and credentials.
func NormalizeURL(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

// Harness returns the pairing for url, if any.
func (f File) Harness(url string) (Harness, bool) {
	url = NormalizeURL(url)
	for _, h := range f.Harnesses {
		if h.URL == url {
			return h, true
		}
	}
	return Harness{}, false
}

// UpsertHarness adds or replaces the pairing for h.URL.
func (f *File) UpsertHarness(h Harness) {
	h.URL = NormalizeURL(h.URL)
	for i := range f.Harnesses {
		if f.Harnesses[i].URL == h.URL {
			f.Harnesses[i] = h
			return
		}
	}
	f.Harnesses = append(f.Harnesses, h)
}

// RemoveHarness drops the pairing for url; reports whether one existed.
func (f *File) RemoveHarness(url string) bool {
	url = NormalizeURL(url)
	for i, h := range f.Harnesses {
		if h.URL == url {
			f.Harnesses = append(f.Harnesses[:i], f.Harnesses[i+1:]...)
			return true
		}
	}
	return false
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
	if err := json.Unmarshal(b, &f); err != nil {
		return f, err
	}
	if f.HarnessURL != "" {
		if err := migrateLegacy(&f); err != nil {
			return f, err
		}
	}
	return f, nil
}

// migrateLegacy moves the single-harness pairing (harness_url + computer_id in
// config.json, raw token in credentials) into Harnesses + credentials.json.
func migrateLegacy(f *File) error {
	url := NormalizeURL(f.HarnessURL)
	if _, ok := f.Harness(url); !ok {
		f.UpsertHarness(Harness{URL: url, ComputerID: f.ComputerID})
	}
	if b, err := os.ReadFile(legacyCredsPath()); err == nil {
		if tok := strings.TrimSpace(string(b)); tok != "" {
			toks, _ := LoadTokens()
			if toks[url] == "" {
				if err := SaveToken(url, tok); err != nil {
					return err
				}
			}
		}
	}
	f.HarnessURL = ""
	f.ComputerID = ""
	if err := Save(*f); err != nil {
		return err
	}
	_ = os.Remove(legacyCredsPath())
	return nil
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

// CredsPath holds device tokens keyed by harness URL (mode 0600).
func CredsPath() string { return filepath.Join(Home(), "credentials.json") }

// legacyCredsPath is the pre-multi-harness raw single token.
func legacyCredsPath() string { return filepath.Join(Home(), "credentials") }

// LoadTokens returns device tokens keyed by normalized harness URL.
func LoadTokens() (map[string]string, error) {
	out := map[string]string{}
	b, err := os.ReadFile(CredsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]string{}, err
	}
	return out, nil
}

func writeTokens(m map[string]string) error {
	if err := EnsureHome(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(CredsPath(), b, 0o600)
}

// SaveToken stores the device token for one harness.
func SaveToken(harnessURL, tok string) error {
	m, _ := LoadTokens()
	m[NormalizeURL(harnessURL)] = tok
	return writeTokens(m)
}

// ClearToken removes one harness token, or all of them when harnessURL is "".
func ClearToken(harnessURL string) error {
	if harnessURL == "" {
		_ = os.Remove(CredsPath())
		_ = os.Remove(legacyCredsPath())
		return nil
	}
	m, _ := LoadTokens()
	delete(m, NormalizeURL(harnessURL))
	return writeTokens(m)
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
			"could not acquire lock %s even after clearing stale file.\n  try:  %s",
			LockPath(), staleLockHint(),
		)
	}
	parts := make([]string, len(live))
	for i, p := range live {
		parts[i] = strconv.Itoa(p)
	}
	list := strings.Join(parts, " ")
	return nil, fmt.Errorf(
		"another marble-peer is already running (pid %s).\n%s",
		list, runningHint(live),
	)
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

func StatePath() string { return filepath.Join(Home(), "state.json") }

func WriteState(v map[string]interface{}) error {
	if err := EnsureHome(); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return os.WriteFile(StatePath(), b, 0o600)
}
