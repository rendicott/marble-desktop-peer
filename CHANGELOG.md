# Changelog

## [v0.1.2] — 2026-09-22

First release with **Windows** assets. The Windows peer needs an **interactive desktop session** — see the callout below and the README Windows section.

> **Windows: plain SSH is not enough.** OpenSSH on Windows lands your shell in **session 0**, the non-interactive "Services" session, which has no desktop at all. Screenshot and input are structurally impossible there (not merely blocked by a lock). Install over SSH if you like, but *run* the peer from an interactive session — a console/RDP session, `install-autostart`, or a scheduled task created with `schtasks ... /it`. `doctor` detects this and says so (`lock: locked=true source=session`).

### Added
- **Windows peer** (`windows/amd64`, `windows/arm64`), pure Go with no CGO and no helper tools:
  - screenshot via GDI `BitBlt`; click / type / key (chords, named keys, Unicode text) via `SendInput`; per-monitor DPI-aware so screenshot and click coordinates match
  - lock / UAC / secure-desktop detection (`doctor`, status, screenshot lock warning)
  - keep-awake via `SetThreadExecutionState`
  - notification-area tray icon (PowerShell + WinForms) with confirm balloon
  - `install-autostart` writes a hidden Startup-folder launcher (replaces the visible-console `.cmd` stub)
  - Chrome/Edge discovery under Program Files and `%LOCALAPPDATA%`; profile mirror via `robocopy`; CDP port scan and mirror-only kill via PowerShell (no `ps`/`pkill`/`rsync`)
  - single-instance lock (`LockFileEx`) and PID liveness check
- CI runs `go test ./...` on `windows-latest` and cross-compiles Windows targets; the release workflow builds Windows assets on `windows-latest`.

### Fixed
- **Windows Chrome profile mirror:** `SyncUserProfile` (robocopy on Windows, rsync/`copyDir` elsewhere) now verifies the mirror actually has a usable `Default/Network/Cookies` after syncing. Previously, if Chrome was still running and held the cookie DB locked, the sync tolerated the skipped file and launched the mirror anyway — `Local State` (the cookie encryption key) copied fine, but with no cookie database the mirror had **zero logins**, silently. The sync now fails clearly (`ErrCookiesLocked`) instead of proceeding, telling the operator to quit Chrome completely and retry with `computer_browser_ensure force=true`. Other locked-but-optional files (Sessions, `Cookies-journal`, etc.) are still tolerated.
- **Windows `doctor` / lock detection:** distinguish a non-interactive session (e.g. a plain SSH shell, which lands in Windows session 0 and has no desktop at all — `OpenInputDesktop`/`BitBlt`/`SendInput` all fail there) from a genuinely locked/secure desktop. `doctor` and lock status now report `source=session` with an actionable message (run from an interactive session, or use `schtasks ... /it`) instead of the generic secure-desktop wording.

### Docs
- **README Windows section:** documented that the peer needs an interactive session and plain SSH alone will not work, with a `schtasks /it` recipe for remote/headless setup.

### Known limitations (Windows)
- Input into elevated windows requires an elevated peer (UIPI); the lock screen / UAC prompt cannot be captured or driven.
- SSH alone lands in session 0 (no interactive desktop); run from an interactive session (console/RDP) or a scheduled task with `/it` — see the README Windows section.
- Primary display only (same as macOS).
- Unsigned binary (SmartScreen prompt).
- Windows on ARM is cross-compiled only (not run on ARM hardware).

## [v0.1.1] — 2026-09-05

First **functional macOS** peer. Darwin release assets are built on `macos-latest` (v0.1.0’s `darwin-arm64` was a Linux cross-compile without Mac desktop/Chrome paths). Still a portable unsigned binary — no `.dmg` or notarization.

### Added
- **macOS desktop support:** screenshot (`screencapture`), click/type/key (JXA CGEvent + Swift helper), lock-screen detection, `caffeinate` keep-awake, LaunchAgent autostart, menu-bar tray
- **macOS Chrome:** `/Applications/Google Chrome.app` + `~/Library/Application Support/Google/Chrome` user-profile mirror (no Linux `--ozone-platform=x11`)
- **`marble-peer doctor`:** local OS/Chrome/desktop/permission probe; `--open-settings` for TCC panes
- Mini UI macOS permission banner + `POST /macos-perms`
- CI on `macos-latest`; release builds `darwin/arm64` and `darwin/amd64` on a macOS runner (Go **1.25.x** so Darwin test binaries include `LC_UUID` for current macOS dyld)

### Fixed / Improved (computer-use reliability)
- **`click_text` / `click_button`:** prefer real buttons/links; reject huge nav-shell DIVs; return `ambiguous` instead of clicking the wrong container
- **CSS `click`:** reject jQuery `:contains` and other invalid selectors; never silent-success on empty evaluate
- **Screenshots:** max edge 1280 JPEG; meta includes `screen_w`/`screen_h`/`scale`; last-click crosshair overlay
- **`desktop_click`:** image-space coords mapped to screen; atomic post-click screenshot in the same queue slot (no `peer busy`); no Chrome raise-on-click; log active window title

## [v0.1.0] — 2026-08-05


First public release of **marble-peer** (desktop agent for [Marble](https://github.com/rendicott/marble)).

### Added
- Pair / unpair with Marble harness (mutual H-code / P-code handshake)
- Long-running `marble-peer run` daemon: WebSocket dial-out, action queue, mini UI
- Browser path: Chrome user-profile **mirror** + CDP (`browser_mode=user` default); isolated `marble` mode
- Desktop path: screenshot + click/type/key via `xdotool` / GNOME tools
- Linux **systemd --user** autostart + AppIndicator tray
- Display keep-awake / idle inhibit while running (`MARBLE_PEER_KEEP_AWAKE`)
- GitHub Actions multi-arch release binaries (`linux/amd64`, `linux/arm64`, `darwin/arm64`)

### Fixed
- **Idle CPU / laptop fan storm from tray status polling**  
  The Linux AppIndicator tray used `GLib.idle_add(refresh)` where `refresh()`
  returns `True`. GLib re-arms idle sources that return true, which created a
  tight loop: ~35% tray CPU, hundreds–thousands of `/status.json` requests per
  second, CDP probes pinning the Chrome mirror, and multi-core load while the
  peer reported `busy: false`.  
  **Fix:** one-shot idle refresh (`idle_add(lambda: refresh() and False)`) plus
  a 2s cache on desktop/browser probes inside `StatusJSON()`.  
  Details: [docs/idle-cpu-status-storm.md](docs/idle-cpu-status-storm.md).

[v0.1.2]: https://github.com/rendicott/marble-desktop-peer/releases/tag/v0.1.2
[v0.1.1]: https://github.com/rendicott/marble-desktop-peer/releases/tag/v0.1.1
[v0.1.0]: https://github.com/rendicott/marble-desktop-peer/releases/tag/v0.1.0
