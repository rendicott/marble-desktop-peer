# Changelog

## Unreleased

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

[v0.1.1]: https://github.com/rendicott/marble-desktop-peer/releases/tag/v0.1.1
[v0.1.0]: https://github.com/rendicott/marble-desktop-peer/releases/tag/v0.1.0
