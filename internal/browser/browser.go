package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rendicott/marble-desktop-peer/internal/config"
)

// Profile modes (ADR-0021 product intent: use the operator's real logins).
const (
	// ModeUser launches a *mirror* of the operator's Chrome profile under
	// ~/.marble-peer/chrome-user-mirror with CDP. Chrome ≥136 blocks remote debugging
	// on the default path (~/.config/google-chrome), so we cannot attach to the daily
	// browser process — we sync cookies/logins into a mirror that supports CDP.
	ModeUser = "user"
	// ModeMarble uses a blank ~/.marble-peer/chrome-profile (no daily logins).
	ModeMarble = "marble"
)

// Manager attaches to or launches Chromium for CDP control.
type Manager struct {
	mu        sync.Mutex
	cmd       *exec.Cmd // only set if we launched Chrome ourselves
	port      int
	profile   string // user-data-dir used for CDP Chrome
	sourceDir string // real Chrome dir we sync from (ModeUser)
	mode      string
	ready     bool
	owned     bool   // true if we spawned Chrome (safe to kill on --kill-browser-on-exit)
	lastURL   string // last navigated URL — preferred CDP target
}

// Options for New.
type Options struct {
	// Mode is "user" (default) or "marble".
	Mode string
	// CDPPort forces attach port (0 = auto-discover).
	CDPPort int
}

func New() *Manager {
	return NewWithOptions(Options{})
}

func NewWithOptions(opt Options) *Manager {
	mode := strings.ToLower(strings.TrimSpace(opt.Mode))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(os.Getenv("MARBLE_PEER_BROWSER_MODE")))
	}
	if mode == "" {
		mode = ModeUser // product default: real logins
	}
	if mode != ModeMarble {
		mode = ModeUser
	}
	m := &Manager{mode: mode}
	if opt.CDPPort > 0 {
		m.port = opt.CDPPort
	} else if p := os.Getenv("MARBLE_PEER_CDP_PORT"); p != "" {
		m.port, _ = strconv.Atoi(p)
	}
	if mode == ModeMarble {
		m.profile = filepath.Join(config.Home(), "chrome-profile")
	} else {
		// Mirror path (CDP-capable). Source is the daily Chrome data dir.
		m.sourceDir = userChromeDataDir()
		m.profile = filepath.Join(config.Home(), "chrome-user-mirror")
		if v := os.Getenv("MARBLE_PEER_CHROME_MIRROR_DIR"); v != "" {
			m.profile = v
		}
	}
	return m
}

// SourceDir is the daily Chrome profile we sync from (ModeUser).
func (m *Manager) SourceDir() string { return m.sourceDir }

func userChromeDataDir() string {
	if v := os.Getenv("MARBLE_PEER_CHROME_USER_DATA_DIR"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	for _, c := range chromeDataDirCandidates(runtime.GOOS, h) {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	cands := chromeDataDirCandidates(runtime.GOOS, h)
	if len(cands) > 0 {
		return cands[0]
	}
	return filepath.Join(h, ".config", "google-chrome")
}

// chromeDataDirCandidates is the ordered list of daily Chrome/Chromium profile dirs.
func chromeDataDirCandidates(goos, home string) []string {
	switch goos {
	case "darwin":
		return []string{
			filepath.Join(home, "Library", "Application Support", "Google", "Chrome"),
			filepath.Join(home, "Library", "Application Support", "Chromium"),
			filepath.Join(home, "Library", "Application Support", "Google", "Chrome Canary"),
			filepath.Join(home, "Library", "Application Support", "Microsoft Edge"),
		}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		return []string{
			filepath.Join(local, "Google", "Chrome", "User Data"),
			filepath.Join(local, "Chromium", "User Data"),
			filepath.Join(local, "Microsoft", "Edge", "User Data"),
		}
	default:
		return []string{
			filepath.Join(home, ".config", "google-chrome"),
			filepath.Join(home, ".config", "chromium"),
			filepath.Join(home, ".config", "google-chrome-beta"),
		}
	}
}

// UserChromeDir is the daily Chrome profile marble mirrors (ModeUser).
func UserChromeDir() string { return userChromeDataDir() }

// SyncUserProfile copies logins/cookies from the daily Chrome dir into the mirror.
// Safe while daily Chrome is running for most files; best after force (daily Chrome quit).
func SyncUserProfile(src, dst string) error {
	if src == "" || dst == "" {
		return fmt.Errorf("sync: empty path")
	}
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return fmt.Errorf("sync: source profile missing: %s", src)
	}
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	// Prefer rsync when available (fast incremental).
	if _, err := exec.LookPath("rsync"); err == nil {
		args := []string{
			"-a", "--delete",
			"--exclude=SingletonLock",
			"--exclude=SingletonCookie",
			"--exclude=SingletonSocket",
			"--exclude=DevToolsActivePort",
			"--exclude=Crash Reports",
			"--exclude=BrowserMetrics",
			"--exclude=*/Cache/**",
			"--exclude=*/Code Cache/**",
			"--exclude=*/GPUCache/**",
			"--exclude=*/Service Worker/CacheStorage/**",
			src + "/",
			dst + "/",
		}
		cmd := exec.Command("rsync", args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("rsync: %v: %s", err, string(out))
		}
		return nil
	}
	// Fallback: copy Local State + Default (cookies, local storage)
	_ = copyFile(filepath.Join(src, "Local State"), filepath.Join(dst, "Local State"))
	return copyDir(filepath.Join(src, "Default"), filepath.Join(dst, "Default"))
}

func copyFile(src, dst string) error {
	in, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, in, 0o600)
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable (locked) files
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		// skip heavy/irrelevant
		if strings.Contains(rel, "Cache") || strings.Contains(rel, "GPUCache") ||
			strings.Contains(rel, "Code Cache") || strings.HasPrefix(rel, "Crash Reports") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		_ = os.MkdirAll(filepath.Dir(target), 0o700)
		return os.WriteFile(target, data, 0o600)
	})
}

func findChrome() string {
	for _, c := range chromeBinaryCandidates(runtime.GOOS) {
		if !strings.Contains(c, string(os.PathSeparator)) {
			if p, err := exec.LookPath(c); err == nil {
				return p
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func chromeBinaryCandidates(goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"google-chrome", "chromium",
		}
	case "windows":
		return []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			"chrome.exe", "msedge.exe",
		}
	default:
		return []string{
			"google-chrome-stable", "google-chrome", "chromium-browser", "chromium",
			"/usr/bin/google-chrome-stable", "/usr/bin/google-chrome", "/usr/bin/chromium-browser",
		}
	}
}

// ChromeBinary is the system Chrome/Chromium/Edge executable, or "".
func ChromeBinary() string { return findChrome() }

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Mode returns user|marble.
func (m *Manager) Mode() string { return m.mode }

// ProfileDir is the Chrome user-data-dir in use.
func (m *Manager) ProfileDir() string { return m.profile }

// Owned reports whether this process launched Chrome.
func (m *Manager) Owned() bool { return m.owned }

// EnsureResult describes browser readiness after Ensure.
type EnsureResult struct {
	OK      bool   `json:"ok"`
	Mode    string `json:"mode"`
	Profile string `json:"profile"`
	Port    int    `json:"port"`
	Owned   bool   `json:"owned"`
	Action  string `json:"action"` // attached | launched | already_ready | relaunched
	Message string `json:"message,omitempty"`
}

// Ensure makes CDP available: attach to existing debug Chrome, or launch with the
// configured profile. If force is true, quits Chrome holding the user profile and
// relaunches with remote debugging (cookies/logins stay on disk).
func (m *Manager) Ensure(ctx context.Context, force bool) (EnsureResult, error) {
	res := EnsureResult{Mode: m.mode, Profile: m.profile}
	if m.Available() {
		m.mu.Lock()
		res.Port = m.port
		res.Owned = m.owned
		m.mu.Unlock()
		res.OK = true
		res.Action = "already_ready"
		res.Message = "CDP already available (using existing debug Chrome / session)"
		return res, nil
	}

	// User mode: sync logins into CDP-capable mirror, then launch/attach mirror Chrome.
	// force=true: recycle mirror Chrome and re-sync from daily profile first.
	if m.mode == ModeUser {
		if force {
			killProfileChrome(m.profile) // only mirror, NOT daily Chrome
			_ = os.Remove(filepath.Join(config.Home(), "cdp_port"))
			m.mu.Lock()
			m.port = 0
			m.ready = false
			m.cmd = nil
			m.owned = false
			m.mu.Unlock()
		}
		if err := SyncUserProfile(m.sourceDir, m.profile); err != nil {
			// soft: continue with existing mirror if any
			res.Message = "profile sync warning: " + err.Error()
		} else {
			res.Message = "synced logins from " + m.sourceDir
		}
		if err := m.launch(ctx); err != nil {
			// if launch fails because mirror locked, force-kill mirror and retry once
			if profileLocked(m.profile) {
				killProfileChrome(m.profile)
				if err2 := m.launch(ctx); err2 != nil {
					return res, fmt.Errorf("launch mirror chrome failed: %v (after sync: %s)", err2, res.Message)
				}
			} else {
				return res, fmt.Errorf("launch mirror chrome failed: %v", err)
			}
		}
		m.mu.Lock()
		res.Port = m.port
		res.Owned = m.owned
		m.mu.Unlock()
		res.OK = true
		if force {
			res.Action = "relaunched"
		} else {
			res.Action = "launched"
		}
		res.Message = fmt.Sprintf(
			"%s; mirror=%s port=%d (daily Chrome at %s is left alone; logins copied into mirror for CDP)",
			res.Message, m.profile, res.Port, m.sourceDir,
		)
		return res, nil
	}

	err := m.Start(ctx)
	if err == nil {
		m.mu.Lock()
		res.Port = m.port
		res.Owned = m.owned
		m.mu.Unlock()
		res.OK = true
		if res.Owned {
			res.Action = "launched"
			res.Message = "launched Chrome with profile + remote debugging"
		} else {
			res.Action = "attached"
			res.Message = "attached to existing Chrome CDP"
		}
		return res, nil
	}
	res.Message = err.Error()
	return res, err
}

// Start attaches to an existing debug Chrome or launches one with the configured profile.
func (m *Manager) Start(ctx context.Context) error {
	// 1) Attach if we already know a live CDP port
	m.mu.Lock()
	if m.port > 0 && probeCDP(m.port) {
		m.ready = true
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	// 2) Discover any live CDP (fixed ports + Chrome cmdline + DevToolsActivePort file)
	for _, p := range discoverCDPPorts(m.port, m.profile) {
		if probeCDP(p) {
			m.mu.Lock()
			m.port = p
			m.ready = true
			m.owned = false
			m.mu.Unlock()
			savePort(p)
			return nil
		}
	}

	// 3) Launch Chrome ourselves with the right profile
	return m.launch(ctx)
}

func (m *Manager) launch(ctx context.Context) error {
	bin := findChrome()
	if bin == "" {
		return fmt.Errorf("chrome/chromium not found")
	}

	// Never kill the operator's *daily* Chrome. Only recycle the mirror / marble profile we own.
	if m.mode == ModeUser {
		// Ensure mirror has been synced at least once
		if _, err := os.Stat(filepath.Join(m.profile, "Default")); err != nil {
			if err := SyncUserProfile(m.sourceDir, m.profile); err != nil {
				return fmt.Errorf("initial profile sync from %s: %w", m.sourceDir, err)
			}
		}
		if profileLocked(m.profile) {
			// Our mirror is stuck — recycle only the mirror
			killProfileChrome(m.profile)
		}
	} else {
		killProfileChrome(m.profile)
	}

	if err := os.MkdirAll(m.profile, 0o700); err != nil {
		return err
	}

	// Prefer fixed 9222 so attach after restart is reliable (not a random freePort).
	port := 9222
	if !portFree(port) {
		// something else on 9222 — only then pick ephemeral
		if p, err := freePort(); err == nil {
			port = p
		}
	}
	args := []string{
		"--remote-debugging-port=" + strconv.Itoa(port),
		"--remote-debugging-address=127.0.0.1",
		"--remote-allow-origins=*",
		"--user-data-dir=" + m.profile,
		"--no-first-run",
		"--no-default-browser-check",
	}
	if runtime.GOOS == "linux" {
		// Prefer X11 so desktop xdotool can interact if needed; Wayland breaks XTEST.
		args = append(args, "--ozone-platform=x11")
	}
	// User mode: restore session in THIS instance (do not open a second "about:blank" window later).
	if m.mode == ModeUser {
		args = append(args, "--restore-last-session")
	} else {
		args = append(args, "about:blank")
	}

	cmd := exec.Command(bin, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Env = chromeEnv()
	if err := cmd.Start(); err != nil {
		return err
	}

	m.mu.Lock()
	m.cmd = cmd
	m.port = port
	m.owned = true
	m.mu.Unlock()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return fmt.Errorf("context canceled waiting for CDP on port %d", port)
		}
		// Also re-scan: Chrome sometimes hands off and another process owns CDP.
		for _, p := range discoverCDPPorts(port, m.profile) {
			if probeCDP(p) {
				m.mu.Lock()
				m.port = p
				m.ready = true
				// if we attached to handed-off instance, we may not own it
				if p != port {
					m.owned = false
				}
				m.mu.Unlock()
				savePort(p)
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("chrome CDP not ready on port %d (mode=%s profile=%s). Chrome may have started without a live debug port — try computer_browser_ensure force=true", port, m.mode, m.profile)
}

func portFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func chromeEnv() []string {
	env := os.Environ()
	if runtime.GOOS != "linux" {
		return env
	}
	if os.Getenv("DISPLAY") == "" {
		env = append(env, "DISPLAY=:0")
	}
	if os.Getenv("XAUTHORITY") == "" {
		if h, err := os.UserHomeDir(); err == nil {
			xa := filepath.Join(h, ".Xauthority")
			if _, err := os.Stat(xa); err == nil {
				env = append(env, "XAUTHORITY="+xa)
			}
		}
	}
	return env
}

func profileLocked(userDataDir string) bool {
	// Chrome creates SingletonLock when a live instance holds the profile.
	for _, name := range []string{"SingletonLock", "SingletonCookie", "lockfile"} {
		if _, err := os.Stat(filepath.Join(userDataDir, name)); err == nil {
			return true
		}
	}
	return false
}

// Stop marks not ready; only kills Chrome if we own the process and killChrome is true.
func (m *Manager) Stop(killChrome bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ready = false
	if killChrome && m.owned && m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		_, _ = m.cmd.Process.Wait()
		m.cmd = nil
		m.owned = false
	}
}

// Available is true when CDP is up.
func (m *Manager) Available() bool {
	m.mu.Lock()
	port := m.port
	ready := m.ready
	m.mu.Unlock()
	if !ready || port == 0 || !probeCDP(port) {
		// recover attach if Chrome is already debug-enabled somewhere
		for _, p := range discoverCDPPorts(port, m.profile) {
			if probeCDP(p) {
				m.mu.Lock()
				m.port = p
				m.ready = true
				m.owned = false
				m.mu.Unlock()
				savePort(p)
				return true
			}
		}
		return false
	}
	return true
}

func discoverCDPPorts(preferred int, profile string) []int {
	seen := map[int]bool{}
	var out []int
	add := func(p int) {
		if p > 0 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	add(preferred)
	add(9222)
	add(9229)
	add(9333)
	if st := readSavedPort(); st > 0 {
		add(st)
	}
	// Chrome writes DevToolsActivePort: line1=port line2=browser WS path
	if profile != "" {
		if b, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort")); err == nil {
			lines := strings.Split(string(b), "\n")
			if len(lines) > 0 {
				if p, err := strconv.Atoi(strings.TrimSpace(lines[0])); err == nil {
					add(p)
				}
			}
		}
	}
	// Scan running Chrome main processes for --remote-debugging-port=N
	for _, p := range scanChromeDebugPortsFromProc() {
		add(p)
	}
	return out
}

// scanChromeDebugPortsFromProc finds --remote-debugging-port values on live Chrome processes.
func scanChromeDebugPortsFromProc() []int {
	if runtime.GOOS != "linux" {
		return scanChromeDebugPortsFromPS()
	}
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return scanChromeDebugPortsFromPS()
	}
	var ports []int
	seen := map[int]bool{}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		pid := e.Name()
		if pid[0] < '1' || pid[0] > '9' {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", pid, "cmdline"))
		if err != nil || len(b) == 0 {
			continue
		}
		cmd := string(b)
		// main chrome only (skip --type=renderer etc.)
		if !strings.Contains(cmd, "chrome") && !strings.Contains(cmd, "chromium") {
			continue
		}
		if strings.Contains(cmd, "--type=") {
			continue
		}
		// parse --remote-debugging-port=1234
		const key = "--remote-debugging-port="
		idx := strings.Index(cmd, key)
		if idx < 0 {
			continue
		}
		rest := cmd[idx+len(key):]
		// cmdline is null-separated; stop at null or space
		end := 0
		for end < len(rest) && rest[end] != 0 && rest[end] != ' ' {
			end++
		}
		p, err := strconv.Atoi(rest[:end])
		if err != nil || p <= 0 || seen[p] {
			continue
		}
		seen[p] = true
		ports = append(ports, p)
	}
	return ports
}

func scanChromeDebugPortsFromPS() []int {
	cmd := exec.Command("ps", "ax", "-o", "command=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}
	var ports []int
	seen := map[int]bool{}
	const key = "--remote-debugging-port="
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(strings.ToLower(line), "chrome") && !strings.Contains(strings.ToLower(line), "chromium") {
			continue
		}
		if strings.Contains(line, "--type=") {
			continue
		}
		idx := strings.Index(line, key)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(key):]
		end := 0
		for end < len(rest) && rest[end] != 0 && rest[end] != ' ' && rest[end] != '\t' {
			end++
		}
		p, err := strconv.Atoi(rest[:end])
		if err != nil || p <= 0 || seen[p] {
			continue
		}
		seen[p] = true
		ports = append(ports, p)
	}
	return ports
}

func probeCDP(port int) bool {
	client := &http.Client{Timeout: 400 * time.Millisecond}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

type tabInfo struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
	WSUrl string `json:"webSocketDebuggerUrl"`
}

func (m *Manager) listTabs() ([]tabInfo, error) {
	m.mu.Lock()
	port := m.port
	m.mu.Unlock()
	if port == 0 || !probeCDP(port) {
		return nil, fmt.Errorf("browser not ready (no CDP) — start Chrome with remote debugging or run marble-peer print-chrome-cmd")
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tabs []tabInfo
	if err := json.NewDecoder(resp.Body).Decode(&tabs); err != nil {
		return nil, err
	}
	var pages []tabInfo
	for _, t := range tabs {
		if t.Type == "page" || t.Type == "" {
			pages = append(pages, t)
		}
	}
	return pages, nil
}

// TabsJSON returns tab list as JSON text.
func (m *Manager) TabsJSON(ctx context.Context) (string, error) {
	tabs, err := m.listTabs()
	if err != nil {
		return "", err
	}
	type row struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	var rows []row
	for _, t := range tabs {
		rows = append(rows, row{ID: t.ID, Title: t.Title, URL: t.URL})
	}
	b, _ := json.MarshalIndent(rows, "", "  ")
	return string(b), nil
}

// Open navigates the active page to urlStr via CDP Page.navigate.
// newTab=true opens one extra tab in the SAME browser (CDP Target.createTarget), never a second Chrome process.
// If the only targets are chrome:// pages (bookmarks, NTP chrome UI), opens a fresh http tab via /json/new.
func (m *Manager) Open(ctx context.Context, urlStr string, newTab bool) error {
	if !strings.HasPrefix(urlStr, "http://") && !strings.HasPrefix(urlStr, "https://") {
		urlStr = "https://" + urlStr
	}
	if !m.Available() {
		if err := m.Start(ctx); err != nil {
			return err
		}
	}
	if newTab {
		return m.openNewTab(ctx, urlStr)
	}
	// If no suitable http(s) page exists, create one — Page.navigate on chrome:// often hangs.
	tabs, _ := m.listTabs()
	hasHTTP := false
	for _, t := range tabs {
		if strings.HasPrefix(t.URL, "http://") || strings.HasPrefix(t.URL, "https://") {
			hasHTTP = true
			break
		}
	}
	if !hasHTTP {
		return m.openNewTab(ctx, urlStr)
	}
	// Prefer navigating an existing http(s) tab (lastURL / ups / gmail preferred by activePageWS).
	err := m.withPage(ctx, func(s *cdpSession) error {
		// shorter timeouts for Page.* so we fail fast on hung chrome:// targets
		cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		_, err := s.call(cctx, "Page.enable", nil)
		if err != nil {
			return err
		}
		_, err = s.call(cctx, "Page.navigate", map[string]interface{}{"url": urlStr})
		if err != nil {
			return err
		}
		// Base settle; heavy bot/WAF pages (UPS Akamai) need longer — waitForContent below.
		time.Sleep(1500 * time.Millisecond)
		return nil
	})
	if err == nil {
		m.setLastURL(urlStr)
		// Best-effort wait for real content (not chrome NTP / bot interstitial).
		_ = m.waitForUsefulContent(ctx, 12*time.Second)
		// ADR-0022: dismiss host chrome noise once per open (Restore pages / Not now).
		_ = m.dismissHostOverlaysOnce(ctx)
	}
	return err
}

// dismissHostOverlaysOnce best-effort clicks common Chrome restore / "Not now" buttons once.
// No loop — returns labels dismissed for meta/logging.
func (m *Manager) dismissHostOverlaysOnce(ctx context.Context) []string {
	sctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var dismissed []string
	_ = m.withPage(sctx, func(s *cdpSession) error {
		_, _ = s.call(sctx, "Runtime.enable", nil)
		expr := `(function(){
  const want = ['Restore', 'Restore pages', "Don't restore", 'Not now', 'Close', 'Dismiss', 'OK'];
  const hit = [];
  const els = document.querySelectorAll('button, [role="button"], a, input[type="button"], input[type="submit"]');
  for (const el of els) {
    if (hit.length >= 3) break;
    const t = ((el.getAttribute('aria-label')||'') + ' ' + (el.innerText||el.value||'')).replace(/\s+/g,' ').trim();
    if (!t || t.length > 40) continue;
    for (const w of want) {
      if (t === w || t.indexOf(w) === 0) {
        try { el.click(); hit.push(t.slice(0,40)); } catch(e) {}
        break;
      }
    }
  }
  return JSON.stringify(hit);
})()`
		raw, err := s.call(sctx, "Runtime.evaluate", map[string]interface{}{
			"expression":    expr,
			"returnByValue": true,
		})
		if err != nil {
			return nil
		}
		var res struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if json.Unmarshal(raw, &res) == nil && res.Result.Value != "" {
			_ = json.Unmarshal([]byte(res.Result.Value), &dismissed)
		}
		return nil
	})
	return dismissed
}

// waitForUsefulContent polls a light evaluate until body text looks real or deadline.
func (m *Manager) waitForUsefulContent(ctx context.Context, max time.Duration) error {
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ok bool
		err := m.withPage(ctx, func(s *cdpSession) error {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			_, _ = s.call(cctx, "Runtime.enable", nil)
			raw, err := s.call(cctx, "Runtime.evaluate", map[string]interface{}{
				"expression": `(function(){
  const u = location.href || '';
  const t = (document.body && (document.body.innerText || '')) || '';
  if (u.indexOf('chrome://') === 0) return 'chrome';
  if (t.indexOf('akam-sw') >= 0 || t.indexOf('akamServiceWorker') >= 0) return 'bot_wall';
  if (t.length < 40) return 'short';
  return 'ok';
})()`,
				"returnByValue": true,
			})
			if err != nil {
				return err
			}
			var res struct {
				Result struct {
					Value string `json:"value"`
				} `json:"result"`
			}
			_ = json.Unmarshal(raw, &res)
			switch res.Result.Value {
			case "ok":
				ok = true
			case "bot_wall":
				// keep waiting — challenge sometimes finishes
			}
			return nil
		})
		if err == nil && ok {
			return nil
		}
		time.Sleep(800 * time.Millisecond)
	}
	return fmt.Errorf("page content not ready (bot wall or still loading)")
}

func (m *Manager) openNewTab(ctx context.Context, urlStr string) error {
	m.mu.Lock()
	port := m.port
	m.mu.Unlock()
	if port == 0 {
		return fmt.Errorf("no cdp port")
	}
	// PUT /json/new?<url> is the most reliable way to open a tab without attaching first.
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/new?%s", port, url.QueryEscape(urlStr))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// fallback: navigate via Target.createTarget on any attached session
		return m.withPage(ctx, func(s *cdpSession) error {
			_, err := s.call(ctx, "Target.createTarget", map[string]interface{}{"url": urlStr})
			time.Sleep(1200 * time.Millisecond)
			return err
		})
	}
	defer resp.Body.Close()
	time.Sleep(1500 * time.Millisecond)
	m.setLastURL(urlStr)
	_ = m.dismissHostOverlaysOnce(ctx)
	return nil
}

// Snapshot returns page URL/title + truncated body text via Runtime.evaluate.
// For Gmail: list_rows (inbox), and when a thread is open: message{subject,from,body}.
// If evaluate hangs (UPS modals, heavy SPAs), still returns tabs + a short tip.
func (m *Manager) Snapshot(ctx context.Context) (string, error) {
	tabs, err := m.TabsJSON(ctx)
	if err != nil {
		return "", err
	}
	var bodyText string
	// Bound evaluate so one stuck page doesn't burn the whole tool deadline.
	sctx, scancel := context.WithTimeout(ctx, 14*time.Second)
	defer scancel()
	err = m.withPage(sctx, func(s *cdpSession) error {
		_, _ = s.call(sctx, "Runtime.enable", nil)
		// Prefer a light extract first (avoids multi-second walks on huge DOMs).
		expr := `(function(){
  const t = document.title || '';
  const u = location.href || '';
  let text = '';
  try {
    text = document.body ? (document.body.innerText || document.body.textContent || '') : '';
  } catch(e) { text = String(e); }
  text = text.replace(/\s+\n/g, '\n').replace(/\n{3,}/g, '\n\n').trim();
  if (text.length > 10000) text = text.slice(0, 10000) + '\n…[truncated]';

  // Gmail / list UIs: collect visible rows for click_text / open_gmail
  const rows = [];
  const sels = [
    'tr.zA', 'tr.zE',
    'div[role="row"]',
    'div[role="option"]',
    'div.xS', 'span.bog'
  ];
  const seen = new Set();
  for (const sel of sels) {
    document.querySelectorAll(sel).forEach((el) => {
      if (rows.length >= 40) return;
      const label = (el.getAttribute('aria-label') || el.innerText || '').replace(/\s+/g, ' ').trim();
      if (!label || label.length < 8) return;
      const key = label.slice(0, 120);
      if (seen.has(key)) return;
      seen.add(key);
      let tid = el.getAttribute('data-legacy-thread-id')
        || el.getAttribute('data-thread-id')
        || (el.querySelector && el.querySelector('[data-legacy-thread-id]')
            ? el.querySelector('[data-legacy-thread-id]').getAttribute('data-legacy-thread-id')
            : null);
      rows.push({i: rows.length, sel: sel, preview: label.slice(0, 240), thread_id: tid || ''});
    });
    if (rows.length >= 40) break;
  }

  // Open thread: extract subject / from / body (Gmail classic DOM)
  let message = null;
  const subjectEl = document.querySelector('h2.hP, h2[data-thread-perm-id], div[role="main"] h2');
  const bodyEls = Array.from(document.querySelectorAll('.a3s.aiL, .a3s, div[data-message-id] .a3s'));
  const fromEl = document.querySelector('span.gD, span[email]');
  if (subjectEl || bodyEls.length) {
    const bodies = bodyEls.map(el => (el.innerText || '').trim()).filter(Boolean);
    const body = bodies.join('\n\n---\n\n');
    message = {
      subject: subjectEl ? (subjectEl.innerText || '').trim() : '',
      from: fromEl ? ((fromEl.getAttribute('email') || '') + ' ' + (fromEl.innerText || '')).trim() : '',
      body: body.length > 12000 ? body.slice(0, 12000) + '\n…[truncated]' : body,
      body_len: body.length,
      open: /#(inbox|all|search|label)\/.+/.test(u) && !/#(inbox|all|search|label)\/?$/.test(u.split('?')[0])
    };
  }
  // Collect clickable-looking text for buttons (UPS Sign Digitally, etc.)
  const buttons = [];
  document.querySelectorAll('button, a, [role="button"], input[type="button"], input[type="submit"]').forEach((el) => {
    if (buttons.length >= 30) return;
    const label = ((el.getAttribute('aria-label') || '') + ' ' + (el.innerText || el.value || '')).replace(/\s+/g, ' ').trim();
    if (!label || label.length < 2 || label.length > 80) return;
    buttons.push(label.slice(0, 80));
  });
  return JSON.stringify({title:t, url:u, text:text, list_rows: rows, message: message, buttons: buttons});
})()`
		raw, err := s.call(sctx, "Runtime.evaluate", map[string]interface{}{
			"expression":    expr,
			"returnByValue": true,
			"awaitPromise":  false,
		})
		if err != nil {
			return err
		}
		var res struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			bodyText = string(raw)
			return nil
		}
		bodyText = res.Result.Value
		// remember URL from snapshot for next targeting
		var pretty map[string]interface{}
		if json.Unmarshal([]byte(bodyText), &pretty) == nil {
			if u, ok := pretty["url"].(string); ok && u != "" {
				m.setLastURL(u)
			}
		}
		return nil
	})
	if err != nil {
		return "tabs:\n" + tabs + "\n\nsnapshot error: " + err.Error() +
			"\n// tip: page may be stuck (modal or bot wall). Re-open URL, wait, click_text \"Sign Digitally\". UPS often needs a human to pass Akamai once in the mirror Chrome.\n", nil
	}
	// Detect bot interstitials in successful snapshots
	if strings.Contains(bodyText, "akam-sw") || strings.Contains(bodyText, "akamServiceWorker") || strings.Contains(bodyText, "Powered and protected by") {
		bodyText += "\n\n// WARNING: bot/WAF interstitial detected (e.g. UPS Akamai). Automation cannot click Sign Digitally until a human loads this page in the mirror Chrome and completes any challenge. Prefer computer_confirm + manual sign, or open UPS in daily Chrome.\n"
	}
	out := "tabs:\n" + tabs + "\n\nactive_page:\n"
	if bodyText != "" {
		var pretty map[string]interface{}
		if json.Unmarshal([]byte(bodyText), &pretty) == nil {
			b, _ := json.MarshalIndent(pretty, "", "  ")
			out += string(b)
		} else {
			out += bodyText
		}
	}
	out += "\n\n// tip: open email with browser_act action=open_gmail text=\"560049223\" (best)\n"
	out += "// or search_gmail then click_text; then snapshot and read active_page.message.body\n"
	return out, nil
}

// Act performs browser actions via CDP.
// actions: open|navigate|click|click_text|type|press|key|eval|search_gmail|open_gmail|wait|set_input_files
// Returns a short result string (URL, hit preview, message summary) so the harness agent can verify success.
// For wait: x = timeout_ms (default 10000). For set_input_files: text = path(s), target = optional CSS.
func (m *Manager) Act(ctx context.Context, action, target, text string, x, y int) (string, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "open", "navigate":
		u := target
		if u == "" {
			u = text
		}
		if err := m.Open(ctx, u, false); err != nil {
			return "", err
		}
		cur, _ := m.pageURL(ctx)
		return "opened " + cur, nil
	case "search_gmail":
		// Navigate Gmail search hash — more reliable than clicking compose UI.
		q := text
		if q == "" {
			q = target
		}
		q = strings.TrimSpace(q)
		if q == "" {
			return "", fmt.Errorf("search_gmail needs text=query")
		}
		u := "https://mail.google.com/mail/u/0/#search/" + url.PathEscape(q)
		// PathEscape turns spaces into %20; Gmail wants + for spaces in hash sometimes
		u = strings.ReplaceAll(u, "%20", "+")
		if err := m.Open(ctx, u, false); err != nil {
			return "", err
		}
		time.Sleep(800 * time.Millisecond)
		cur, _ := m.pageURL(ctx)
		return "search_gmail query=" + q + " url=" + cur, nil
	case "open_gmail":
		// One-shot: search + open matching thread by data-legacy-thread-id (most reliable for reading mail).
		q := text
		if q == "" {
			q = target
		}
		q = strings.TrimSpace(q)
		if q == "" {
			return "", fmt.Errorf("open_gmail needs text=query (subject fragment, order #, sender…)")
		}
		return m.openGmail(ctx, q)
	case "wait":
		// Wait for selector (target), substring (text), and/or a fixed delay (x=timeout_ms).
		return m.waitFor(ctx, target, text, x)
	case "set_input_files", "upload_file", "set_files":
		// Play Console / store listing: CDP DOM.setFileInputFiles (OS file dialogs are not automatable).
		paths := splitFilePaths(text)
		if len(paths) == 0 {
			paths = splitFilePaths(target)
			target = ""
		}
		if len(paths) == 0 {
			return "", fmt.Errorf("set_input_files needs text=absolute path(s) on the peer (comma-separated)")
		}
		sel := strings.TrimSpace(target)
		if err := m.setInputFiles(ctx, sel, paths); err != nil {
			return "", err
		}
		time.Sleep(400 * time.Millisecond)
		mini := m.miniSnapshot(ctx)
		return fmt.Sprintf("set_input_files ok files=%v selector=%q\npost:\n%s", paths, sel, mini), nil
	case "click_text", "click_button":
		needle := text
		if needle == "" {
			needle = target
		}
		if strings.TrimSpace(needle) == "" {
			return "", fmt.Errorf("%s needs text=substring (button label)", action)
		}
		buttonsOnly := action == "click_button"
		var hit string
		err := m.withPage(ctx, func(s *cdpSession) error {
			var e error
			hit, e = cdpClickText(ctx, s, needle, buttonsOnly)
			return e
		})
		if err != nil {
			return "", err
		}
		time.Sleep(700 * time.Millisecond)
		cur, _ := m.pageURL(ctx)
		mini := m.miniSnapshot(ctx)
		return action + " hit=" + hit + " url=" + cur + "\npost:\n" + mini, nil
	case "click":
		err := m.withPage(ctx, func(s *cdpSession) error {
			_, _ = s.call(ctx, "Runtime.enable", nil)
			_, _ = s.call(ctx, "Input.enable", map[string]interface{}{})
			if strings.TrimSpace(target) != "" {
				// If target doesn't look like CSS, treat as click_text
				if !strings.ContainsAny(target, ".#[]>:=()") && !strings.HasPrefix(target, "text=") {
					_, e := cdpClickText(ctx, s, target, false)
					return e
				}
				if strings.HasPrefix(target, "text=") {
					_, e := cdpClickText(ctx, s, strings.TrimPrefix(target, "text="), false)
					return e
				}
				if err := validateCSSSelector(target); err != nil {
					return err
				}
				expr := fmt.Sprintf(`(function(){
  try {
    const el = document.querySelector(%q);
    if (!el) return JSON.stringify({ok:false, reason:'not_found'});
    el.scrollIntoView({block:'center', inline:'center'});
    el.click();
    return JSON.stringify({ok:true});
  } catch (e) {
    return JSON.stringify({ok:false, reason:String(e && e.message ? e.message : e)});
  }
})()`, target)
				raw, err := s.call(ctx, "Runtime.evaluate", map[string]interface{}{
					"expression":    expr,
					"returnByValue": true,
				})
				if err != nil {
					return err
				}
				var res struct {
					Result struct {
						Value string `json:"value"`
					} `json:"result"`
					ExceptionDetails json.RawMessage `json:"exceptionDetails"`
				}
				_ = json.Unmarshal(raw, &res)
				if len(res.ExceptionDetails) > 0 && string(res.ExceptionDetails) != "null" {
					return fmt.Errorf("invalid CSS selector %q (querySelector threw)", target)
				}
				var parsed struct {
					OK     bool   `json:"ok"`
					Reason string `json:"reason"`
				}
				if err := json.Unmarshal([]byte(res.Result.Value), &parsed); err != nil || res.Result.Value == "" {
					return fmt.Errorf("CSS click failed for %q (empty/invalid evaluate result) — use click_text/click_button or a valid CSS selector", target)
				}
				if !parsed.OK {
					if parsed.Reason == "not_found" {
						if _, err := cdpClickText(ctx, s, target, true); err == nil {
							return nil
						}
						return fmt.Errorf("selector not found: %s (also tried as click_button text)", target)
					}
					return fmt.Errorf("CSS click failed for %q: %s — jQuery pseudos like :contains are not supported; use click_button/click_text", target, parsed.Reason)
				}
				return nil
			}
			if x == 0 && y == 0 {
				return fmt.Errorf("click needs target selector, click_text, click_button, or x,y")
			}
			return cdpClick(ctx, s, float64(x), float64(y))
		})
		if err != nil {
			return "", err
		}
		time.Sleep(500 * time.Millisecond)
		cur, _ := m.pageURL(ctx)
		mini := m.miniSnapshot(ctx)
		return "click ok url=" + cur + "\npost:\n" + mini, nil
	case "type":
		err := m.withPage(ctx, func(s *cdpSession) error {
			_, _ = s.call(ctx, "Runtime.enable", nil)
			_, _ = s.call(ctx, "Input.enable", map[string]interface{}{})
			if strings.TrimSpace(target) != "" {
				expr := fmt.Sprintf(`(function(){
  const el = document.querySelector(%q);
  if (!el) return 'not_found';
  el.focus();
  return 'ok';
})()`, target)
				raw, err := s.call(ctx, "Runtime.evaluate", map[string]interface{}{
					"expression":    expr,
					"returnByValue": true,
				})
				if err != nil {
					return err
				}
				var res struct {
					Result struct {
						Value string `json:"value"`
					} `json:"result"`
				}
				_ = json.Unmarshal(raw, &res)
				if res.Result.Value == "not_found" {
					return fmt.Errorf("selector not found: %s", target)
				}
			}
			_, err := s.call(ctx, "Input.insertText", map[string]interface{}{"text": text})
			return err
		})
		if err != nil {
			return "", err
		}
		return "typed", nil
	case "press", "key":
		key := text
		if key == "" {
			key = target
		}
		err := m.withPage(ctx, func(s *cdpSession) error {
			_, _ = s.call(ctx, "Input.enable", map[string]interface{}{})
			// Map common keys for Gmail (o=open, j/k=nav, / =search)
			keyName, code := normalizeKey(key)
			_, err := s.call(ctx, "Input.dispatchKeyEvent", map[string]interface{}{
				"type":                  "keyDown",
				"key":                   keyName,
				"code":                  code,
				"windowsVirtualKeyCode": keyVK(keyName),
				"nativeVirtualKeyCode":  keyVK(keyName),
			})
			if err != nil {
				return err
			}
			_, err = s.call(ctx, "Input.dispatchKeyEvent", map[string]interface{}{
				"type":                  "keyUp",
				"key":                   keyName,
				"code":                  code,
				"windowsVirtualKeyCode": keyVK(keyName),
				"nativeVirtualKeyCode":  keyVK(keyName),
			})
			return err
		})
		if err != nil {
			return "", err
		}
		cur, _ := m.pageURL(ctx)
		return "pressed " + key + " url=" + cur, nil
	case "eval", "evaluate":
		expr := text
		if expr == "" {
			expr = target
		}
		var out string
		err := m.withPage(ctx, func(s *cdpSession) error {
			raw, err := s.call(ctx, "Runtime.evaluate", map[string]interface{}{
				"expression":    expr,
				"returnByValue": true,
			})
			if err != nil {
				return err
			}
			out = string(raw)
			if len(out) > 4000 {
				out = out[:4000] + "…"
			}
			return nil
		})
		return out, err
	default:
		return "", fmt.Errorf("unknown browser action %q (open|click|click_text|click_button|type|press|search_gmail|open_gmail|eval|wait|set_input_files)", action)
	}
}

// waitFor sleeps and/or polls until CSS selector or text substring appears.
// x is timeout_ms (default 10000, max 60000). Empty target+text → pure sleep.
func (m *Manager) waitFor(ctx context.Context, selector, needle string, timeoutMS int) (string, error) {
	if timeoutMS <= 0 {
		timeoutMS = 10000
	}
	if timeoutMS > 60000 {
		timeoutMS = 60000
	}
	selector = strings.TrimSpace(selector)
	needle = strings.TrimSpace(needle)
	deadline := time.Now().Add(time.Duration(timeoutMS) * time.Millisecond)

	if selector == "" && needle == "" {
		// Pure delay — useful after open/navigation on SPAs.
		d := time.Duration(timeoutMS) * time.Millisecond
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(d):
			return fmt.Sprintf("wait slept %dms", timeoutMS), nil
		}
	}

	poll := 250 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		found, detail, err := m.pollCondition(ctx, selector, needle)
		if err == nil && found {
			mini := m.miniSnapshot(ctx)
			return fmt.Sprintf("wait ok %s\npost:\n%s", detail, mini), nil
		}
		if time.Now().After(deadline) {
			mini := m.miniSnapshot(ctx)
			why := "timeout"
			if err != nil {
				why = err.Error()
			}
			return "", fmt.Errorf("wait timeout after %dms (selector=%q text=%q last=%s): %s\npost:\n%s",
				timeoutMS, selector, needle, why, detail, mini)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (m *Manager) pollCondition(ctx context.Context, selector, needle string) (bool, string, error) {
	var detail string
	var found bool
	err := m.withPage(ctx, func(s *cdpSession) error {
		_, _ = s.call(ctx, "Runtime.enable", nil)
		expr := fmt.Sprintf(`(function(){
  const sel = %q;
  const needle = %q;
  let selOk = true;
  let textOk = true;
  let info = {};
  if (sel) {
    const el = document.querySelector(sel);
    selOk = !!el;
    info.selector_found = selOk;
    if (el) {
      const r = el.getBoundingClientRect();
      info.selector_visible = r.width > 0 && r.height > 0;
      selOk = info.selector_visible || selOk;
    }
  }
  if (needle) {
    const body = (document.body && (document.body.innerText || document.body.textContent) || '');
    const buttons = [];
    document.querySelectorAll('button, a, [role="button"], input[type="submit"], input[type="button"]').forEach(el => {
      const t = ((el.getAttribute('aria-label')||'') + ' ' + (el.innerText||el.value||'')).replace(/\s+/g,' ').trim();
      if (t) buttons.push(t.slice(0,80));
    });
    const hay = body + '\n' + buttons.join('\n');
    textOk = hay.toLowerCase().indexOf(needle.toLowerCase()) >= 0;
    info.text_found = textOk;
    info.url = location.href;
    info.title = document.title;
  } else {
    info.url = location.href;
    info.title = document.title;
  }
  info.ok = selOk && textOk;
  return JSON.stringify(info);
})()`, selector, needle)
		raw, err := s.call(ctx, "Runtime.evaluate", map[string]interface{}{
			"expression":    expr,
			"returnByValue": true,
		})
		if err != nil {
			return err
		}
		var res struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		_ = json.Unmarshal(raw, &res)
		detail = res.Result.Value
		if detail == "" {
			detail = string(raw)
		}
		var info struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal([]byte(detail), &info) == nil {
			found = info.OK
		}
		return nil
	})
	return found, detail, err
}

// setInputFiles uses CDP DOM.setFileInputFiles — the only reliable way to upload
// for Play Console / SPA file pickers (native OS dialogs cannot be driven by xdotool).
func (m *Manager) setInputFiles(ctx context.Context, selector string, paths []string) error {
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("file not found on peer: %s (%v)", p, err)
		}
	}
	return m.withPage(ctx, func(s *cdpSession) error {
		_, _ = s.call(ctx, "DOM.enable", nil)
		_, _ = s.call(ctx, "Runtime.enable", nil)
		// Resolve objectId for the file input.
		selExpr := `(function(){
  const all = Array.from(document.querySelectorAll('input[type=file]'));
  if (!all.length) return null;
  const vis = all.find(el => {
    const r = el.getBoundingClientRect();
    const st = getComputedStyle(el);
    return (r.width > 0 || r.height > 0 || st.display !== 'none') && st.visibility !== 'hidden';
  });
  return vis || all[0];
})()`
		if strings.TrimSpace(selector) != "" {
			selExpr = fmt.Sprintf("document.querySelector(%q)", selector)
		}
		raw, err := s.call(ctx, "Runtime.evaluate", map[string]interface{}{
			"expression":    selExpr,
			"returnByValue": false,
			"objectGroup":   "marble-file",
		})
		if err != nil {
			return err
		}
		var res struct {
			Result struct {
				ObjectID string `json:"objectId"`
				Type     string `json:"type"`
				Subtype  string `json:"subtype"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return fmt.Errorf("parse file input object: %w", err)
		}
		if res.Result.ObjectID == "" || res.Result.Type == "undefined" || res.Result.Subtype == "null" {
			return fmt.Errorf("no file input found (selector=%q) — open the upload control or pass target=CSS for input[type=file]", selector)
		}
		_, err = s.call(ctx, "DOM.setFileInputFiles", map[string]interface{}{
			"objectId": res.Result.ObjectID,
			"files":    paths,
		})
		if err != nil {
			return fmt.Errorf("DOM.setFileInputFiles: %w", err)
		}
		// Dispatch change/input so React/Angular pick up the files.
		changeExpr := selExpr
		if strings.TrimSpace(selector) != "" {
			changeExpr = fmt.Sprintf("document.querySelector(%q)", selector)
		}
		_, _ = s.call(ctx, "Runtime.evaluate", map[string]interface{}{
			"expression": fmt.Sprintf(`(function(){
  const el = %s;
  if (!el) return;
  el.dispatchEvent(new Event('input', {bubbles:true}));
  el.dispatchEvent(new Event('change', {bubbles:true}));
})()`, changeExpr),
			"returnByValue": true,
		})
		return nil
	})
}

func splitFilePaths(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// Support comma or newline separated paths.
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == ';'
	})
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// miniSnapshot is a cheap post-action page summary (title/url/buttons) so the model
// can verify UI changes without a full browser_snapshot round-trip.
func (m *Manager) miniSnapshot(ctx context.Context) string {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var body string
	err := m.withPage(sctx, func(s *cdpSession) error {
		_, _ = s.call(sctx, "Runtime.enable", nil)
		expr := `(function(){
  const buttons = [];
  document.querySelectorAll('button, a[role="button"], [role="button"], input[type="submit"], input[type="button"]').forEach(el => {
    if (buttons.length >= 20) return;
    const t = ((el.getAttribute('aria-label')||'') + ' ' + (el.innerText||el.value||'')).replace(/\s+/g,' ').trim();
    if (t && t.length >= 2 && t.length <= 80) buttons.push(t.slice(0,80));
  });
  const inputs = [];
  document.querySelectorAll('input:not([type=hidden]), textarea').forEach(el => {
    if (inputs.length >= 12) return;
    inputs.push({
      type: el.type || el.tagName.toLowerCase(),
      name: el.name || '',
      aria: el.getAttribute('aria-label') || '',
      placeholder: el.placeholder || '',
      value: (el.value || '').slice(0, 80)
    });
  });
  const files = document.querySelectorAll('input[type=file]').length;
  return JSON.stringify({
    title: document.title || '',
    url: location.href || '',
    buttons: buttons,
    inputs: inputs,
    file_inputs: files
  });
})()`
		raw, err := s.call(sctx, "Runtime.evaluate", map[string]interface{}{
			"expression":    expr,
			"returnByValue": true,
		})
		if err != nil {
			return err
		}
		var res struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			body = string(raw)
			return nil
		}
		body = res.Result.Value
		return nil
	})
	if err != nil {
		return "mini_snapshot error: " + err.Error()
	}
	if body == "" {
		return "(empty mini snapshot)"
	}
	var pretty map[string]interface{}
	if json.Unmarshal([]byte(body), &pretty) == nil {
		b, _ := json.MarshalIndent(pretty, "", "  ")
		return string(b)
	}
	return body
}

// ensureGmailTab creates or focuses a mail.google.com page if none exists.
func (m *Manager) ensureGmailTab(ctx context.Context) error {
	tabs, err := m.listTabs()
	if err != nil {
		return err
	}
	for _, t := range tabs {
		if strings.Contains(t.URL, "mail.google.com") && t.WSUrl != "" {
			return nil
		}
	}
	// No Gmail tab — open one via Target.createTarget from any page, or /json/new.
	// Prefer /json/new with the Gmail URL so we don't Page.navigate chrome:// UI.
	m.mu.Lock()
	port := m.port
	m.mu.Unlock()
	if port == 0 {
		return fmt.Errorf("no cdp port")
	}
	// Chrome accepts PUT/GET to /json/new?url
	u := fmt.Sprintf("http://127.0.0.1:%d/json/new?%s", port, url.QueryEscape("https://mail.google.com/mail/u/0/#inbox"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// fallback GET
		resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/new?https://mail.google.com/mail/u/0/#inbox", port))
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	// give Gmail SPA a moment
	time.Sleep(2 * time.Second)
	return nil
}

// pageURL returns the active page URL.
func (m *Manager) pageURL(ctx context.Context) (string, error) {
	var u string
	err := m.withPage(ctx, func(s *cdpSession) error {
		_, _ = s.call(ctx, "Runtime.enable", nil)
		raw, err := s.call(ctx, "Runtime.evaluate", map[string]interface{}{
			"expression":    "location.href",
			"returnByValue": true,
		})
		if err != nil {
			return err
		}
		var res struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		_ = json.Unmarshal(raw, &res)
		u = res.Result.Value
		return nil
	})
	return u, err
}

// openGmail searches and opens the first matching thread by data-legacy-thread-id, then returns body summary.
func (m *Manager) openGmail(ctx context.Context, query string) (string, error) {
	// Ensure we have a real Gmail page target (not chrome://bookmarks which hangs Page.navigate).
	if err := m.ensureGmailTab(ctx); err != nil {
		return "", fmt.Errorf("ensure gmail tab: %w", err)
	}
	searchURL := "https://mail.google.com/mail/u/0/#search/" + strings.ReplaceAll(url.PathEscape(query), "%20", "+")
	if err := m.Open(ctx, searchURL, false); err != nil {
		return "", err
	}
	// Wait for virtual list rows
	var threadID, preview string
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		err := m.withPage(ctx, func(s *cdpSession) error {
			_, _ = s.call(ctx, "Runtime.enable", nil)
			b, _ := json.Marshal(query)
			expr := fmt.Sprintf(`(function(){
  const needle = %s.toLowerCase();
  const rows = Array.from(document.querySelectorAll('tr.zA, tr.zE, div[role="row"][data-legacy-thread-id], div[data-legacy-thread-id]'));
  for (const el of rows) {
    const label = ((el.getAttribute('aria-label') || '') + ' ' + (el.innerText || '')).replace(/\s+/g, ' ').trim();
    if (!label || label.toLowerCase().indexOf(needle) < 0) continue;
    let tid = el.getAttribute('data-legacy-thread-id') || el.getAttribute('data-thread-id') || '';
    if (!tid) {
      const child = el.querySelector('[data-legacy-thread-id]');
      if (child) tid = child.getAttribute('data-legacy-thread-id') || '';
    }
    if (!tid) {
      // try href hash
      const a = el.querySelector('a[href*="#"]');
      if (a) {
        const m = (a.getAttribute('href')||'').match(/#(inbox|all|search)\/([A-Za-z0-9]+)/);
        if (m) tid = m[2];
      }
    }
    if (tid) return JSON.stringify({thread_id: tid, preview: label.slice(0, 200)});
  }
  return JSON.stringify({thread_id:'', preview:'', n: rows.length});
})()`, string(b))
			raw, err := s.call(ctx, "Runtime.evaluate", map[string]interface{}{
				"expression":    expr,
				"returnByValue": true,
			})
			if err != nil {
				return err
			}
			var res struct {
				Result struct {
					Value string `json:"value"`
				} `json:"result"`
			}
			_ = json.Unmarshal(raw, &res)
			var hit struct {
				ThreadID string `json:"thread_id"`
				Preview  string `json:"preview"`
			}
			_ = json.Unmarshal([]byte(res.Result.Value), &hit)
			threadID = hit.ThreadID
			preview = hit.Preview
			return nil
		})
		if err != nil {
			return "", err
		}
		if threadID != "" {
			break
		}
		time.Sleep(400 * time.Millisecond)
	}
	if threadID == "" {
		// Fallback: click_text then read URL
		if _, err := m.Act(ctx, "click_text", "", query, 0, 0); err != nil {
			return "", fmt.Errorf("open_gmail: no thread_id for %q and click_text failed: %v", query, err)
		}
		time.Sleep(1200 * time.Millisecond)
		cur, _ := m.pageURL(ctx)
		body, _ := m.readOpenMessage(ctx)
		return fmt.Sprintf("open_gmail via click_text query=%q url=%s message=%s", query, cur, body), nil
	}
	// Open thread by hash (search context keeps query)
	openURL := "https://mail.google.com/mail/u/0/#search/" + strings.ReplaceAll(url.PathEscape(query), "%20", "+") + "/" + threadID
	if err := m.Open(ctx, openURL, false); err != nil {
		// try #all/
		if err2 := m.Open(ctx, "https://mail.google.com/mail/u/0/#all/"+threadID, false); err2 != nil {
			return "", fmt.Errorf("open thread: %v / %v", err, err2)
		}
	}
	time.Sleep(1500 * time.Millisecond)
	// Sometimes hash navigate needs a second tick on SPA
	cur, _ := m.pageURL(ctx)
	if !strings.Contains(cur, threadID) && !strings.Contains(cur, "#inbox/") && !strings.Contains(cur, "#all/") {
		// force click_text as backup
		_, _ = m.Act(ctx, "click_text", "", query, 0, 0)
		time.Sleep(1200 * time.Millisecond)
		cur, _ = m.pageURL(ctx)
	}
	body, _ := m.readOpenMessage(ctx)
	return fmt.Sprintf("open_gmail query=%q thread_id=%s preview=%q url=%s\nmessage:\n%s", query, threadID, preview, cur, body), nil
}

// readOpenMessage pulls subject/from/body of the currently open Gmail thread.
func (m *Manager) readOpenMessage(ctx context.Context) (string, error) {
	var out string
	err := m.withPage(ctx, func(s *cdpSession) error {
		_, _ = s.call(ctx, "Runtime.enable", nil)
		expr := `(function(){
  const subjectEl = document.querySelector('h2.hP, h2[data-thread-perm-id], div[role="main"] h2');
  const fromEl = document.querySelector('span.gD, span[email]');
  const bodies = Array.from(document.querySelectorAll('.a3s.aiL, .a3s'))
    .map(el => (el.innerText||'').trim()).filter(Boolean);
  const body = bodies.join('\n\n---\n\n');
  return JSON.stringify({
    url: location.href,
    subject: subjectEl ? (subjectEl.innerText||'').trim() : '',
    from: fromEl ? ((fromEl.getAttribute('email')||'') + ' ' + (fromEl.innerText||'')).trim() : '',
    body: body.length > 10000 ? body.slice(0,10000)+'\n…[truncated]' : body
  });
})()`
		raw, err := s.call(ctx, "Runtime.evaluate", map[string]interface{}{
			"expression":    expr,
			"returnByValue": true,
		})
		if err != nil {
			return err
		}
		var res struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		_ = json.Unmarshal(raw, &res)
		out = res.Result.Value
		if out == "" {
			out = string(raw)
		}
		return nil
	})
	return out, err
}

// cdpClickText finds a visible element containing needle and clicks it (Gmail rows, etc.).
// Prefers the row element (tr.zA) over nested spans so Gmail opens the thread.
// validateCSSSelector rejects jQuery-style and other non-CSS selector syntax.
func validateCSSSelector(sel string) error {
	low := strings.ToLower(sel)
	if strings.Contains(low, ":contains") || strings.Contains(low, ":has(") ||
		strings.Contains(low, ":eq(") || strings.Contains(low, ":gt(") ||
		strings.Contains(low, ":lt(") || strings.Contains(low, ":first") ||
		strings.Contains(low, ":last") || strings.Contains(sel, "$(") {
		return fmt.Errorf("invalid CSS selector %q — jQuery pseudos (:contains, :has, …) are not supported; use action=click_button text=\"Label\" or a real CSS selector", sel)
	}
	return nil
}

// cdpClickText finds a visible control by label substring.
// buttonsOnly=true restricts to button-like nodes (click_button); false allows Gmail rows + tight links.
func cdpClickText(ctx context.Context, s *cdpSession, needle string, buttonsOnly bool) (string, error) {
	_, _ = s.call(ctx, "Runtime.enable", nil)
	_, _ = s.call(ctx, "Input.enable", map[string]interface{}{})
	b, _ := json.Marshal(needle)
	mode, _ := json.Marshal(map[string]bool{"buttons_only": buttonsOnly})
	expr := fmt.Sprintf(`(function(){
  const needle = %s.toLowerCase().trim();
  const mode = %s;
  if (!needle) return JSON.stringify({ok:false, reason:'empty'});
  const MAX_LABEL = 80;
  const MAX_AREA = 120000;
  const candidates = [];
  function pushCand(el, label, kind, score) {
    const r = el.getBoundingClientRect();
    if (r.width < 4 || r.height < 4) return;
    if (r.bottom < 0 || r.right < 0 || r.top > (window.innerHeight||0) || r.left > (window.innerWidth||0)) return;
    const area = r.width * r.height;
    if (area > MAX_AREA) return;
    if (label.length > MAX_LABEL) return;
    // Reject nav shells: many children + long label
    if (el.children && el.children.length > 8 && label.length > 40) return;
    candidates.push({el, label, kind, score, area, r});
  }
  function labelOf(el) {
    return ((el.getAttribute('aria-label')||'') + ' ' + (el.innerText||el.value||'')).replace(/\s+/g,' ').trim();
  }
  // Pass 1: button-like controls
  document.querySelectorAll('button, [role="button"], input[type="submit"], input[type="button"], a').forEach(el => {
    const label = labelOf(el);
    if (!label || label.length < needle.length) return;
    const low = label.toLowerCase();
    const idx = low.indexOf(needle);
    if (idx < 0) return;
    let score = label.length + idx + (el.tagName === 'A' ? 30 : 0);
    if (low === needle) score -= 1000;
    else if (low.indexOf(needle) === 0) score -= 400;
    pushCand(el, label, 'buttonish', score);
  });
  if (!mode.buttons_only) {
    // Pass 2: Gmail / list rows
    document.querySelectorAll('tr.zA, tr.zE, div[role="row"], div[data-legacy-thread-id], div[role="option"], div[role="link"], span.bog, div.xS').forEach(el => {
      const label = labelOf(el);
      if (!label) return;
      const idx = label.toLowerCase().indexOf(needle);
      if (idx < 0) return;
      if (label.length > 240) return;
      let score = Math.min(label.length, 200) + idx - 500;
      pushCand(el, label.slice(0,160), 'row', score);
    });
  }
  if (!candidates.length) {
    return JSON.stringify({ok:false, reason:'not_found', needle:needle});
  }
  candidates.sort((a,b) => a.score - b.score || a.area - b.area);
  // Ambiguous: two close top scores with different labels
  if (candidates.length >= 2) {
    const a = candidates[0], b = candidates[1];
    if (Math.abs(a.score - b.score) < 40 && a.label.toLowerCase() !== b.label.toLowerCase()) {
      return JSON.stringify({
        ok:false, reason:'ambiguous',
        candidates: candidates.slice(0,5).map(c => ({preview:c.label.slice(0,80), tag:c.el.tagName, kind:c.kind}))
      });
    }
  }
  let best = candidates[0].el;
  const row = best.closest && best.closest('tr.zA, tr.zE, div[role="row"], div[data-legacy-thread-id]');
  if (row && !mode.buttons_only) best = row;
  best.scrollIntoView({block:'center', inline:'center'});
  const r = best.getBoundingClientRect();
  const cx = r.left + Math.min(Math.max(r.width * 0.5, 8), r.width - 8);
  const cy = r.top + r.height/2;
  best.click();
  for (const type of ['mousedown','mouseup','click']) {
    best.dispatchEvent(new MouseEvent(type, {bubbles:true, cancelable:true, clientX:cx, clientY:cy, view:window}));
  }
  const tid = best.getAttribute('data-legacy-thread-id') || '';
  return JSON.stringify({
    ok:true, cx:cx, cy:cy, tag:best.tagName, thread_id:tid,
    preview:(best.innerText||best.value||'').replace(/\s+/g,' ').trim().slice(0,160)
  });
})()`, string(b), string(mode))
	raw, err := s.call(ctx, "Runtime.evaluate", map[string]interface{}{
		"expression":    expr,
		"returnByValue": true,
	})
	if err != nil {
		return "", err
	}
	var res struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &res)
	if res.Result.Value == "" {
		return "", fmt.Errorf("no visible element containing %q", needle)
	}
	var parsed struct {
		OK         bool    `json:"ok"`
		Reason     string  `json:"reason"`
		CX         float64 `json:"cx"`
		CY         float64 `json:"cy"`
		Candidates []struct {
			Preview string `json:"preview"`
			Tag     string `json:"tag"`
			Kind    string `json:"kind"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(res.Result.Value), &parsed); err != nil {
		if res.Result.Value == "not_found" {
			return "", fmt.Errorf("no visible element containing %q", needle)
		}
		return "", fmt.Errorf("click_text parse failed for %q", needle)
	}
	if !parsed.OK {
		if parsed.Reason == "ambiguous" {
			var bits []string
			for _, c := range parsed.Candidates {
				bits = append(bits, fmt.Sprintf("%s[%s]", c.Tag, c.Preview))
			}
			return "", fmt.Errorf("ambiguous label %q — candidates: %s; use a more specific text or click_button", needle, strings.Join(bits, " | "))
		}
		kind := "element"
		if buttonsOnly {
			kind = "button"
		}
		return "", fmt.Errorf("no visible %s containing %q", kind, needle)
	}
	if parsed.CX > 0 {
		_ = cdpClick(ctx, s, parsed.CX, parsed.CY)
	}
	time.Sleep(600 * time.Millisecond)
	return res.Result.Value, nil
}

func normalizeKey(key string) (name, code string) {
	k := strings.TrimSpace(key)
	switch strings.ToLower(k) {
	case "enter", "return":
		return "Enter", "Enter"
	case "tab":
		return "Tab", "Tab"
	case "escape", "esc":
		return "Escape", "Escape"
	case "backspace":
		return "Backspace", "Backspace"
	case "arrowdown", "down":
		return "ArrowDown", "ArrowDown"
	case "arrowup", "up":
		return "ArrowUp", "ArrowUp"
	default:
		if len(k) == 1 {
			return k, "Key" + strings.ToUpper(k)
		}
		return k, k
	}
}

func keyVK(key string) int {
	switch key {
	case "Enter":
		return 13
	case "Tab":
		return 9
	case "Escape":
		return 27
	case "Backspace":
		return 8
	case "ArrowDown":
		return 40
	case "ArrowUp":
		return 38
	default:
		if len(key) == 1 {
			return int(key[0])
		}
		return 0
	}
}

func cdpClick(ctx context.Context, s *cdpSession, x, y float64) error {
	for _, typ := range []string{"mousePressed", "mouseReleased"} {
		_, err := s.call(ctx, "Input.dispatchMouseEvent", map[string]interface{}{
			"type":       typ,
			"x":          x,
			"y":          y,
			"button":     "left",
			"clickCount": 1,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func keyToCode(key string) string {
	switch strings.ToLower(key) {
	case "enter", "return":
		return "Enter"
	case "tab":
		return "Tab"
	case "escape", "esc":
		return "Escape"
	case "backspace":
		return "Backspace"
	default:
		if len(key) == 1 {
			return "Key" + strings.ToUpper(key)
		}
		return key
	}
}

func readSavedPort() int {
	b, err := os.ReadFile(filepath.Join(config.Home(), "cdp_port"))
	if err != nil {
		return 0
	}
	p, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return p
}

func savePort(port int) {
	_ = os.MkdirAll(config.Home(), 0o700)
	_ = os.WriteFile(filepath.Join(config.Home(), "cdp_port"), []byte(strconv.Itoa(port)), 0o600)
}

func killProfileChrome(profile string) {
	if profile == "" {
		return
	}
	// Match main Chrome only when possible; fall back to user-data-dir match.
	// GNU pkill accepts `--`; BSD (macOS) pkill does not.
	if runtime.GOOS == "darwin" {
		_ = exec.Command("pkill", "-f", "user-data-dir="+profile).Run()
	} else {
		_ = exec.Command("pkill", "-f", "--", "user-data-dir="+profile).Run()
	}
	// Give SingletonLock time to clear
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !profileLocked(profile) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Stale lock file after hard kill
	for _, name := range []string{"SingletonLock", "SingletonCookie", "SingletonSocket"} {
		_ = os.Remove(filepath.Join(profile, name))
	}
	time.Sleep(200 * time.Millisecond)
}

// PrintChromeCmd explains the Chrome ≥136 mirror approach (CDP blocked on default profile path).
func PrintChromeCmd() string {
	bin := findChrome()
	if bin == "" {
		cands := chromeBinaryCandidates(runtime.GOOS)
		if len(cands) > 0 {
			bin = cands[0]
		} else {
			bin = "google-chrome"
		}
	}
	src := userChromeDataDir()
	mirror := filepath.Join(config.Home(), "chrome-user-mirror")
	return fmt.Sprintf(
		`# Chrome blocks remote debugging on the default profile path.
# Marble uses a *mirror* of your profile (cookies/logins) that supports CDP.
# Your daily Chrome at %s is left alone.
#
# Peer does this automatically via computer_browser_ensure.
# Manual equivalent:
rsync -a --delete --exclude=Singleton* --exclude='*/Cache/**' %q/ %q/
%s --remote-debugging-port=9222 --remote-debugging-address=127.0.0.1 --remote-allow-origins=* --user-data-dir=%q
`,
		src, src, mirror, bin, mirror,
	)
}
