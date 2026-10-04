//go:build darwin

package autostart

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// macOS autostart + GUI-session launch.
//
// Why this is not a plain LaunchAgent pointing at the binary:
//
// screencapture (and, less reliably, CGEventPost-based input) only works when
// the calling process has a LaunchServices-registered identity. A process
// spawned directly by launchd — even inside a correct Aqua session with
// LimitLoadToSessionType=Aqua and full TCC grants — is NOT registered with
// LaunchServices, so screencapture fails with:
//
//	could not create image from display
//
// Verified on macOS 27: a minimal LaunchAgent whose only job is to run
// screencapture fails, while the same script started via `open -a Terminal`
// succeeds. The fix is to launch through Launch Services.
//
// We therefore install a tiny .app bundle (MarblePeer.app) that wraps the
// binary, and point the LaunchAgent at `open -a MarblePeer.app`. Launch
// Services registers the bundle, the peer inherits a real GUI identity, and
// screenshots/input work. The bundle also gives macOS a stable code identity
// for TCC, so permission grants survive binary upgrades.

const (
	darwinLabel    = "com.rendicott.marble-peer"
	darwinBundleID = "com.rendicott.marble-peer"
	darwinAppName  = "MarblePeer.app"
	// darwinSelfSignedCN is the common name of the self-signed codesigning
	// certificate marble-peer creates on first install. A stable signing
	// identity is what lets TCC grants survive peer upgrades.
	darwinSelfSignedCN = "Marble Peer Self-Signed"
)

func darwinAppDir() string {
	return filepath.Join(home(), "Applications", darwinAppName)
}

func darwinPlistPath() string {
	return filepath.Join(home(), "Library/LaunchAgents", darwinLabel+".plist")
}

func darwinLogPath() string {
	return filepath.Join(home(), "Library/Logs/marble-peer.log")
}

// writeAppBundle creates ~/Applications/MarblePeer.app wrapping exe.
//
// The peer binary is copied INTO the bundle as Contents/MacOS/MarblePeer and
// is the bundle's CFBundleExecutable. This matters: macOS attributes
// screencapture to the *responsible* process, and only a process that is
// itself a LaunchServices-registered app (or a direct child of one) gets a
// usable GUI identity. A shell-script launcher that spawns the binary as a
// child does NOT work — verified on macOS 27.
//
// Because the binary is copied, install-autostart must be re-run after
// upgrading marble-peer. `marble-peer doctor` detects a stale bundle and says
// so.
func writeAppBundle(exe string) ([]string, error) {
	app := darwinAppDir()
	macOSDir := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macOSDir, 0o755); err != nil {
		return nil, err
	}

	infoPlist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>MarblePeer</string>
  <key>CFBundleIdentifier</key><string>%s</string>
  <key>CFBundleName</key><string>MarblePeer</string>
  <key>CFBundleDisplayName</key><string>Marble Peer</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
`, darwinBundleID)
	infoPath := filepath.Join(app, "Contents", "Info.plist")
	if err := os.WriteFile(infoPath, []byte(infoPlist), 0o644); err != nil {
		return nil, err
	}

	// Copy the real binary in as the bundle executable.
	//
	// Skip the copy when the running binary already IS the bundle executable
	// (the normal case when installing from a released .app.zip, or when
	// re-running install-autostart on an already-installed peer). Copying a
	// file onto itself would truncate it.
	binPath := filepath.Join(macOSDir, "MarblePeer")
	if !sameFile(exe, binPath) {
		if err := copyFile(exe, binPath, 0o755); err != nil {
			return nil, fmt.Errorf("copy binary into bundle: %w", err)
		}
	}

	// Sign the bundle so TCC has a stable identity to key grants to.
	//
	// Ad-hoc signing ("-") is NOT enough: macOS stores a code-signing
	// requirement (csreq) that pins the grant to the exact binary hash, so
	// every rebuild invalidates the user's Screen Recording / Accessibility
	// grants and they must re-grant. That is the single worst part of the
	// macOS experience.
	//
	// Instead we sign with a persistent self-signed certificate created once
	// in the user's login keychain. The certificate identity is stable across
	// rebuilds, so TCC grants survive upgrades. A real Developer ID is used
	// automatically if one is present.
	signBundle(app)

	return []string{infoPath, binPath}, nil
}

// signBundle signs the app bundle with the best available identity.
//
// Why this matters: macOS TCC stores a code-signing requirement (csreq) that
// pins a permission grant to the exact signing identity. With ad-hoc signing
// ("-") the identity is the binary hash, so EVERY rebuild invalidates the
// user's Screen Recording / Accessibility grants and they must re-grant. That
// is the single worst part of the macOS peer experience.
//
// Signing with a stable certificate fixes it permanently. Preference order:
//  1. Developer ID Application certificate (best; also notarizable)
//  2. a persistent self-signed certificate in the login keychain
//  3. ad-hoc (grants must be re-granted after each upgrade)
func signBundle(app string) {
	if id := findSigningIdentity(); id != "" {
		if exec.Command("codesign", "--force", "--deep", "--sign", id,
			"--identifier", darwinBundleID, app).Run() == nil {
			return
		}
	}
	// Ad-hoc fallback.
	_ = exec.Command("codesign", "--force", "--deep", "--sign", "-",
		"--identifier", darwinBundleID, app).Run()
}

// findSigningIdentity returns a codesigning identity to use, preferring a
// Developer ID and falling back to a self-signed cert we manage.
func findSigningIdentity() string {
	// Prefer a real Developer ID if the user has one.
	out, err := exec.Command("security", "find-identity", "-v", "-p", "codesigning").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "Developer ID Application") {
				if id := extractIdentityHash(line); id != "" {
					return id
				}
			}
		}
	}
	// Fall back to our own persistent self-signed certificate, if it is
	// already trusted (see SelfSignedCertStatus for why trust matters).
	if selfSignedCertUsable() {
		return darwinSelfSignedCN
	}
	return ""
}

// extractIdentityHash pulls the 40-hex-char SHA-1 from a `security
// find-identity` line like:
//
//  1. ABCDEF... "Developer ID Application: Foo (TEAMID)"
func extractIdentityHash(line string) string {
	for _, f := range strings.Fields(line) {
		if len(f) == 40 && isHex(f) {
			return f
		}
	}
	return ""
}

func isHex(s string) bool {
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// SigningStatus describes whether a stable signing identity is available, and
// if not, what the user must do to get one.
//
// A stable identity is what keeps macOS TCC grants (Screen Recording,
// Accessibility) alive across peer upgrades. Ad-hoc signing pins the grant to
// the binary hash, so every upgrade forces the user to re-grant.
//
// Preference order matches signBundle: Developer ID, then our self-signed
// certificate. Creating the self-signed cert is scriptable, but macOS requires
// an interactive authorization to trust it, so we surface the exact command
// rather than failing silently.
func SigningStatus() (usable bool, hint string) {
	// A Developer ID is the best case and needs no extra steps.
	if hasDeveloperID() {
		return true, ""
	}
	if selfSignedCertUsable() {
		return true, ""
	}
	// Self-signed cert present but not trusted?
	if _, err := exec.Command("security", "find-certificate", "-c",
		darwinSelfSignedCN).Output(); err == nil {
		return false, "certificate exists but is not trusted for code signing — " +
			"run `marble-peer install-autostart --trust-cert` from a Terminal window on the Mac"
	}
	return false, "no stable signing identity — run `marble-peer install-autostart --trust-cert` " +
		"from a Terminal window on the Mac to create one (keeps permissions across upgrades)"
}

// hasDeveloperID reports whether a Developer ID Application certificate is
// available for code signing.
func hasDeveloperID() bool {
	out, err := exec.Command("security", "find-identity", "-v", "-p", "codesigning").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "Developer ID Application")
}

// SelfSignedCertStatus is kept for callers that specifically want the
// self-signed path; prefer SigningStatus.
func SelfSignedCertStatus() (bool, string) { return SigningStatus() }

// selfSignedCertUsable reports whether our self-signed cert is present AND
// trusted for code signing (i.e. codesign can actually use it).
func selfSignedCertUsable() bool {
	out, err := exec.Command("security", "find-identity", "-v", "-p", "codesigning").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), darwinSelfSignedCN)
}

// CreateSelfSignedCert generates and imports a self-signed codesigning
// certificate into the login keychain, then trusts it.
//
// The trust step needs interactive authorization on modern macOS, so this must
// be run from a Terminal window on the Mac (not over SSH). Returns an error
// explaining that if the trust step is refused.
func CreateSelfSignedCert() error {
	if selfSignedCertUsable() {
		return nil
	}
	tmp, err := os.MkdirTemp("", "marble-cert")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	keyPath := filepath.Join(tmp, "key.pem")
	crtPath := filepath.Join(tmp, "cert.pem")
	p12Path := filepath.Join(tmp, "cert.p12")
	const p12pass = "marble"

	// 10-year self-signed cert with the codeSigning extended key usage.
	// macOS ships LibreSSL, which lacks -legacy; -des3 is the compatible
	// choice for a PKCS#12 that `security import` accepts.
	req := exec.Command("openssl", "req", "-x509", "-newkey", "rsa:2048",
		"-keyout", keyPath, "-out", crtPath, "-days", "3650", "-nodes",
		"-subj", "/CN="+darwinSelfSignedCN,
		"-addext", "extendedKeyUsage=codeSigning",
		"-addext", "basicConstraints=critical,CA:false")
	if out, err := req.CombinedOutput(); err != nil {
		return fmt.Errorf("openssl req: %v: %s", err, strings.TrimSpace(string(out)))
	}
	p12 := exec.Command("openssl", "pkcs12", "-export", "-out", p12Path,
		"-inkey", keyPath, "-in", crtPath, "-passout", "pass:"+p12pass, "-des3")
	if out, err := p12.CombinedOutput(); err != nil {
		return fmt.Errorf("openssl pkcs12: %v: %s", err, strings.TrimSpace(string(out)))
	}

	kc := loginKeychain()
	// Unlock so the import can proceed non-interactively when possible.
	_ = exec.Command("security", "unlock-keychain", kc).Run()

	imp := exec.Command("security", "import", p12Path,
		"-k", kc, "-P", p12pass,
		"-T", "/usr/bin/codesign", "-T", "/usr/bin/security", "-A")
	if out, err := imp.CombinedOutput(); err != nil {
		return fmt.Errorf("security import: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// Trust it for code signing. This is the step that needs interactive
	// authorization; over SSH it fails with "no user interaction was possible".
	trust := exec.Command("security", "add-trusted-cert", "-d", "-r", "trustRoot",
		"-k", kc, crtPath)
	if out, err := trust.CombinedOutput(); err != nil {
		return fmt.Errorf("security add-trusted-cert: %v: %s\n"+
			"run this from a Terminal window on the Mac (not over SSH):\n"+
			"  marble-peer install-autostart --trust-cert",
			err, strings.TrimSpace(string(out)))
	}
	if !selfSignedCertUsable() {
		return fmt.Errorf("certificate created but codesign still cannot use it; "+
			"open Keychain Access → login → \"%s\" → Get Info → Trust → "+
			"set \"Code Signing\" to Always Trust", darwinSelfSignedCN)
	}
	return nil
}

func loginKeychain() string {
	out, err := exec.Command("security", "default-keychain", "-d", "user").Output()
	if err == nil {
		s := strings.Trim(strings.TrimSpace(string(out)), `"`)
		if s != "" {
			return s
		}
	}
	return filepath.Join(home(), "Library/Keychains/login.keychain-db")
}

// sameFile reports whether two paths refer to the same file on disk.
func sameFile(a, b string) bool {
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return os.SameFile(ai, bi)
}

// copyFile copies src to dst with the given mode, replacing dst atomically.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func installDarwin(exe string, enable bool) ([]string, string, error) {
	plistDir := filepath.Join(home(), "Library/LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		return nil, "", err
	}
	_ = os.MkdirAll(filepath.Join(home(), "Library/Logs"), 0o755)

	paths, err := writeAppBundle(exe)
	if err != nil {
		return nil, "", err
	}

	app := darwinAppDir()
	plist := darwinPlistPath()
	logPath := darwinLogPath()

	// Launch the bundle's executable directly.
	//
	// We deliberately do NOT use `open -a` here: `open` hands off to
	// LaunchServices and returns immediately, so launchd loses track of the
	// process (no restart, no exit status). Running the bundle executable
	// directly still gives the process its bundle identity — macOS resolves
	// the enclosing .app from the executable path — while keeping launchd as
	// the parent so KeepAlive and logging work.
	binPath := filepath.Join(app, "Contents", "MacOS", "MarblePeer")
	body := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>run</string>
  </array>
  <key>WorkingDirectory</key><string>%s</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><false/>
  <key>LimitLoadToSessionType</key><string>Aqua</string>
  <key>ProcessType</key><string>Interactive</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>MARBLE_PEER_KEEP_AWAKE</key><string>1</string>
  </dict>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, darwinLabel, binPath, home(), logPath, logPath)
	if err := os.WriteFile(plist, []byte(body), 0o644); err != nil {
		return nil, "", err
	}
	paths = append(paths, plist)

	msg := "installed app bundle " + app
	msg += "\ninstalled LaunchAgent " + plist

	if enable {
		uid := os.Getuid()
		domain := fmt.Sprintf("gui/%d", uid)
		target := domain + "/" + darwinLabel
		// Clear any previous registration first. A job left behind by an
		// earlier `launchctl load -w` makes `bootstrap` fail with
		// "Input/output error", so unload both ways before bootstrapping.
		_, _ = run("launchctl", "bootout", target)
		_, _ = run("launchctl", "unload", plist)
		if out, err := run("launchctl", "bootstrap", domain, plist); err != nil {
			return paths, msg, fmt.Errorf("launchctl bootstrap %s: %v (%s)\n"+
				"try: launchctl bootout %s && launchctl bootstrap %s %s",
				domain, err, out, target, domain, plist)
		}
		_, _ = run("launchctl", "enable", target)
		_, _ = run("launchctl", "kickstart", "-k", target)
		msg += "\nbootstrapped " + target
		msg += "\nlogs: tail -f " + logPath
		msg += "\n\nGrant Screen Recording + Accessibility to \"Marble Peer\" in"
		msg += "\nSystem Settings → Privacy & Security, then run: marble-peer doctor"
		if hasDeveloperID() {
			msg += "\nsigned with your Developer ID — permissions will survive upgrades"
		} else if usable, hint := SigningStatus(); !usable {
			msg += "\n\nNOTE: " + hint
			msg += "\nWithout a stable signing identity, macOS drops the permission"
			msg += "\ngrants every time marble-peer is upgraded."
		} else {
			msg += "\nsigned with a stable self-signed identity — permissions will survive upgrades"
		}
	} else {
		msg += "\nload with: launchctl bootstrap gui/$(id -u) " + plist
	}
	return paths, msg, nil
}

func uninstallDarwin() ([]string, string, error) {
	var removed []string
	uid := os.Getuid()
	target := fmt.Sprintf("gui/%d/%s", uid, darwinLabel)
	_, _ = run("launchctl", "bootout", target)

	plist := darwinPlistPath()
	_, _ = run("launchctl", "unload", plist)
	if err := os.Remove(plist); err == nil {
		removed = append(removed, plist)
	} else if !os.IsNotExist(err) {
		return removed, "", err
	}

	app := darwinAppDir()
	if err := os.RemoveAll(app); err == nil {
		removed = append(removed, app)
	}
	// Drop the LaunchServices registration so a reinstall re-registers cleanly.
	_, _ = run("/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister",
		"-u", app)

	return removed, "unloaded LaunchAgent and removed app bundle", nil
}

// RelaunchViaLaunchServices starts the peer through its app bundle so it
// acquires a GUI identity, then exits the current (non-GUI) process.
//
// This is the escape hatch for users who started the peer from an SSH shell
// or a bare LaunchAgent: `marble-peer run --gui` re-execs itself the right way
// without requiring install-autostart.
func RelaunchViaLaunchServices(args []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	if _, err := writeAppBundle(exe); err != nil {
		return fmt.Errorf("write app bundle: %w", err)
	}
	// `open -g` keeps focus; -n forces a new instance even if one is running.
	cmd := exec.Command("/usr/bin/open", "-g", "-n", "-a", darwinAppDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("open -a %s: %v: %s", darwinAppDir(), err, strings.TrimSpace(string(out)))
	}
	fmt.Println("relaunched marble-peer via its app bundle (GUI identity acquired)")
	fmt.Println("the previous process will exit; check status with: marble-peer status")
	return nil
}

// RunningViaLaunchServices reports whether the current process is running from
// inside the MarblePeer.app bundle (i.e. has a GUI identity). On macOS this is
// the difference between screencapture working and failing with
// "could not create image from display".
func RunningViaLaunchServices() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	// The binary must live inside a .app bundle to carry a bundle identity.
	return strings.Contains(exe, ".app/Contents/MacOS/")
}

// IsPeerBinary reports whether the running executable is the marble-peer
// binary itself (as opposed to a shell, or a copy used only for diagnostics).
// doctor uses this to decide whether the bundle check is meaningful.
func IsPeerBinary() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	base := filepath.Base(exe)
	return base == "marble-peer" || base == "MarblePeer"
}

// BundleStale reports whether the installed app bundle holds an older copy of
// the peer binary than the one currently running. The bundle embeds a copy of
// the binary, so upgrading marble-peer requires re-running install-autostart.
func BundleStale() (bool, string) {
	exe, err := os.Executable()
	if err != nil {
		return false, ""
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	bundled := filepath.Join(darwinAppDir(), "Contents", "MacOS", "MarblePeer")
	if exe == bundled {
		return false, "" // running from the bundle; nothing to compare
	}
	// Only meaningful when the running binary is the peer itself.
	if !IsPeerBinary() {
		return false, ""
	}
	cur, err1 := os.Stat(exe)
	bun, err2 := os.Stat(bundled)
	if err2 != nil || err1 != nil {
		return false, "" // no bundle installed yet
	}
	if cur.Size() != bun.Size() || !cur.ModTime().Equal(bun.ModTime()) {
		return true, bundled
	}
	return false, ""
}
