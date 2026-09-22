package browser

// Windows process/profile helpers. Everything here is plain os/exec so it
// compiles (and its parsers are unit-tested) on every OS; it is only *called*
// when runtime.GOOS == "windows". Windows ships neither ps, pkill nor rsync, so
// PowerShell (CIM process table) and robocopy stand in for them.

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const chromeProcFilter = `Name='chrome.exe' OR Name='msedge.exe'`

// runPowerShell runs a script with Windows PowerShell 5.1 (present on every
// supported Windows). Values that come from the environment (profile paths) are
// passed via env vars, never spliced into the script text.
func runPowerShell(timeout time.Duration, script string, env ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

var (
	winScanMu   sync.Mutex
	winScanAt   time.Time
	winScanPort []int
)

// scanChromeDebugPortsWindows lists --remote-debugging-port values from live
// Chrome/Edge main processes. Spawning PowerShell costs hundreds of ms and
// discoverCDPPorts runs inside a 200ms poll loop, so results are cached briefly.
func scanChromeDebugPortsWindows() []int {
	winScanMu.Lock()
	defer winScanMu.Unlock()
	if time.Since(winScanAt) < 3*time.Second {
		return winScanPort
	}
	out, err := runPowerShell(15*time.Second,
		`Get-CimInstance Win32_Process -Filter "`+chromeProcFilter+`" | ForEach-Object { $_.CommandLine }`)
	winScanAt = time.Now()
	if err != nil {
		winScanPort = nil
		return nil
	}
	winScanPort = debugPortsFromCommandLines(strings.Split(out, "\n"), "chrome", "msedge")
	return winScanPort
}

// debugPortsFromCommandLines extracts --remote-debugging-port=N from browser main
// process command lines (renderer/utility children carry --type= and are skipped).
func debugPortsFromCommandLines(lines []string, names ...string) []int {
	const key = "--remote-debugging-port="
	var ports []int
	seen := map[int]bool{}
	for _, line := range lines {
		lower := strings.ToLower(line)
		named := false
		for _, n := range names {
			if strings.Contains(lower, n) {
				named = true
				break
			}
		}
		if !named || strings.Contains(line, "--type=") {
			continue
		}
		idx := strings.Index(line, key)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(key):]
		end := 0
		for end < len(rest) && rest[end] != 0 && rest[end] != ' ' && rest[end] != '\t' && rest[end] != '"' && rest[end] != '\r' {
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

// killProfileChromeWindows stops Chrome/Edge processes whose command line names
// this exact user-data-dir (the mirror), never the operator's daily browser.
func killProfileChromeWindows(profile string) {
	const script = `$p = $env:MARBLE_PEER_KILL_PROFILE
Get-CimInstance Win32_Process -Filter "` + chromeProcFilter + `" |
  Where-Object { $_.CommandLine -and $_.CommandLine.IndexOf('user-data-dir=' + $p, [StringComparison]::OrdinalIgnoreCase) -ge 0 } |
  ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }`
	if out, err := runPowerShell(20*time.Second, script, "MARBLE_PEER_KILL_PROFILE="+profile); err != nil {
		log.Printf("browser: stop mirror chrome: %v: %s", err, strings.TrimSpace(out))
	}
}

// robocopyArgs mirrors src into dst like the rsync path: delete extras, skip
// caches and the profile's singleton/lock files, no retries on locked files.
func robocopyArgs(src, dst string) []string {
	return []string{
		filepath.Clean(src), filepath.Clean(dst),
		"/MIR", "/XJ", "/R:0", "/W:0", "/NFL", "/NDL", "/NJH", "/NJS", "/NP",
		"/XD", "Cache", "Code Cache", "GPUCache", "DawnCache", "GrShaderCache", "ShaderCache",
		"Crash Reports", "BrowserMetrics", "CacheStorage",
		"/XF", "SingletonLock", "SingletonCookie", "SingletonSocket", "DevToolsActivePort", "lockfile",
	}
}

// syncProfileRobocopy copies the daily profile into the mirror. robocopy exit
// codes are a bit set: <8 is success, 8-15 means some files could not be copied,
// >=16 is a fatal error. Chrome keeps the Cookies database exclusively locked
// while running, so a partial copy is expected when daily Chrome is open; it is
// tolerated (like the copyDir fallback) but reported.
func syncProfileRobocopy(src, dst string) error {
	cmd := exec.Command("robocopy", robocopyArgs(src, dst)...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			return fmt.Errorf("robocopy: %v", err)
		}
		code = ee.ExitCode()
	}
	switch {
	case code >= 16:
		return fmt.Errorf("robocopy exit %d: %s", code, strings.TrimSpace(string(out)))
	case code >= 8:
		log.Printf("browser: profile sync: robocopy exit %d — some files were skipped (likely locked by running Chrome; "+
			"logins may be stale). Quit Chrome completely and run computer_browser_ensure force=true for a full sync.", code)
	}
	return nil
}
