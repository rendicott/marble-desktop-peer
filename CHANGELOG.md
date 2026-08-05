# Changelog

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

[v0.1.0]: https://github.com/rendicott/marble-desktop-peer/releases/tag/v0.1.0
