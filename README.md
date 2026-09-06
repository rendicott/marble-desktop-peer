# marble-desktop-peer

Desktop agent for **[Marble](https://github.com/rendicott/marble)** remote computer-use ([ADR-0020](https://github.com/rendicott/marble/blob/main/adr/0020-marble-peer.md) / [ADR-0021](https://github.com/rendicott/marble/blob/main/adr/0021-marble-desktop-peer.md)).

Gives the Marble harness **remote hands and eyes** on your personal machine: full-desktop screenshots and input, plus a managed Chrome profile over CDP. The harness is the brain (`computer_*` tools); this binary is the hands.

| | |
|--|--|
| **Binary** | `marble-peer` |
| **Module** | `github.com/rendicott/marble-desktop-peer` |
| **Latest release** | **[v0.1.1](https://github.com/rendicott/marble-desktop-peer/releases/tag/v0.1.1)** |
| **Data dir** | `~/.marble-peer` (`MARBLE_PEER_HOME`) |
| **Harness** | Marble ≥ **v0.4.1** (computers registry + peer hub) |

## What's new in v0.1.1

- **macOS desktop control** (screenshot, click, type, key), Chrome profile paths, LaunchAgent, menu bar tray  
- `marble-peer doctor` for local permissions / Chrome / screenshot probe  
- Darwin release binaries built on **GitHub-hosted macOS** (not Linux cross-compile)  

See [CHANGELOG.md](CHANGELOG.md).

## Install (prebuilt)

There is **no `.dmg`**, Homebrew formula, or App Store build. GitHub Actions publishes **unsigned portable binaries** on version tags (`v*`). Download from **[Releases](https://github.com/rendicott/marble-desktop-peer/releases)**.

| Asset | Platform |
|-------|----------|
| `marble-peer-linux-amd64` | Linux x86_64 |
| `marble-peer-linux-arm64` | Linux aarch64 |
| `marble-peer-darwin-arm64` | macOS Apple Silicon |
| `marble-peer-darwin-amd64` | macOS Intel |

`v0.1.0`’s `darwin-arm64` asset was a Linux cross-compile and did **not** implement Mac desktop/Chrome. **v0.1.1+** is the first functional macOS peer.

### macOS

Needs **Google Chrome** in `/Applications`. No other packages. Optional: [Xcode Command Line Tools](https://developer.apple.com/download/all/) (`xcode-select --install`) so the first click/type can compile a small Swift helper (`swiftc`). Built-in `screencapture`, `osascript`, and `caffeinate` are enough for doctor + screenshots.

**1. Download, verify, and install** (Apple Silicon). Paste this whole block — it has no `#` comments (zsh treats those as commands unless `interactivecomments` is on). `SHA256SUMS` is a **separate** release asset from the binary. LaunchAgent records the binary’s real path, so do **not** leave it in `~/Downloads`.

```bash
cd ~/Downloads
curl -fsSL -O https://github.com/rendicott/marble-desktop-peer/releases/latest/download/marble-peer-darwin-arm64
curl -fsSL -O https://github.com/rendicott/marble-desktop-peer/releases/latest/download/SHA256SUMS
grep marble-peer-darwin-arm64 SHA256SUMS | shasum -a 256 -c -
chmod +x marble-peer-darwin-arm64
xattr -d com.apple.quarantine marble-peer-darwin-arm64 2>/dev/null || true
mkdir -p ~/.local/bin
mv -f marble-peer-darwin-arm64 ~/.local/bin/marble-peer
export PATH="$HOME/.local/bin:$PATH"
```

Add `export PATH="$HOME/.local/bin:$PATH"` to `~/.zshrc` so it survives new terminals. If Gatekeeper still blocks the binary: Finder → right-click `~/.local/bin/marble-peer` → Open.

Intel: replace `darwin-arm64` with `darwin-amd64` in the `curl` and `grep` lines. With GitHub CLI: `gh release download --repo rendicott/marble-desktop-peer --pattern 'marble-peer-darwin-arm64' --pattern SHA256SUMS`.

**2. Probe the machine** (tools, Chrome, screenshot, TCC):

```bash
marble-peer version
marble-peer doctor --open-settings
```

**3. Grant permissions** to the app that *launches* marble-peer:

| Setting | Why |
|---------|-----|
| **System Settings → Privacy & Security → Screen Recording** | Full-desktop screenshots |
| **System Settings → Privacy & Security → Accessibility** | Synthesized click / type / key |

- Run from **Terminal / iTerm**: enable that terminal app.  
- Started as a **Login Item** (`install-autostart`): enable **marble-peer**.  
- You may also see **osascript** / **marble-desk** — enable those if listed.  

Unsigned rebuilds can drop off the list; re-grant if `doctor` reports screen/accessibility denied. Mini UI (`http://127.0.0.1:18765`) also has a permission banner.

**4. Pair with Marble, then run** (see [Pair](#pair-mutual-handshake)):

```bash
marble-peer pair --harness https://YOUR-HARNESS --code HXXXXX
marble-peer run
```

**5. Optional — start at login** (LaunchAgent `~/Library/LaunchAgents/com.rendicott.marble-peer.plist`):

```bash
marble-peer install-autostart
launchctl print gui/$(id -u)/com.rendicott.marble-peer
tail -f ~/Library/Logs/marble-peer.log
```

`KeepAlive` is false, so tray **Quit** is a clean exit. Data dir: `~/.marble-peer`. Uninstall:

```bash
marble-peer uninstall-autostart
```

### Linux

```bash
curl -fsSL -O https://github.com/rendicott/marble-desktop-peer/releases/latest/download/marble-peer-linux-amd64
curl -fsSL -O https://github.com/rendicott/marble-desktop-peer/releases/latest/download/SHA256SUMS
grep marble-peer-linux-amd64 SHA256SUMS | sha256sum -c -
chmod +x marble-peer-linux-amd64
mkdir -p ~/.local/bin
mv marble-peer-linux-amd64 ~/.local/bin/marble-peer
```

| Tool | Purpose |
|------|---------|
| **Google Chrome** (or Chromium) | Browser automation via CDP mirror |
| `xdotool` | Desktop click / type / key |
| `gnome-screenshot` or ImageMagick `import` | Screenshots |

```bash
# Debian/Ubuntu examples
sudo apt install xdotool gnome-screenshot
# AppIndicator tray (Ubuntu often has this already):
# sudo apt install gir1.2-ayatanaappindicator3-0.1
```

## Build from source

```bash
git clone https://github.com/rendicott/marble-desktop-peer.git
cd marble-desktop-peer
go build -o bin/marble-peer ./cmd/marble-peer
./bin/marble-peer version
```

Requires **Go 1.18+** (CI release builds use **1.25.x**). Release binaries use `CGO_ENABLED=0` (no CGO; Linux uses shell tools, macOS compiles a Swift helper at first use).

Optional ldflags (same as CI):

```bash
go build -ldflags "-s -w \
  -X github.com/rendicott/marble-desktop-peer/internal/app.PeerVersion=v0.1.1 \
  -X github.com/rendicott/marble-desktop-peer/internal/app.Commit=$(git rev-parse --short HEAD) \
  -X github.com/rendicott/marble-desktop-peer/internal/app.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o bin/marble-peer ./cmd/marble-peer
```

## Your logged-in Chrome (default)

**Chrome 136+ blocks remote debugging on the default profile path**
(Linux `~/.config/google-chrome`, macOS `~/Library/Application Support/Google/Chrome`).
Passing `--remote-debugging-port` there will start Chrome but **never open a CDP port**.

Marble’s default **`browser_mode=user`** therefore:

1. **Copies** your profile (cookies/logins) into  
   `~/.marble-peer/chrome-user-mirror`
2. Launches **that mirror** with CDP on port **9222**
3. Leaves your **daily Chrome** alone

```bash
marble-peer run
# or from the harness agent: computer_browser_ensure  (force=true re-syncs + restarts mirror)
```

Notes:
- Logins are as of the last sync (not a live attach to the daily window).
- Re-run `computer_browser_ensure` / `force=true` after you log into new sites in daily Chrome.
- Blank isolated profile: `marble-peer run --browser-mode marble`
- Daily Chrome data dir: Linux `~/.config/google-chrome`, macOS `~/Library/Application Support/Google/Chrome`

## Pair (mutual handshake)

1. In Marble: **Settings → Computers → Pair** (or `POST /api/computers/pair/start`) → copy **H-code**.
2. On this machine:

```bash
marble-peer pair \
  --harness http://127.0.0.1:8080 \
  --code HXXXXX \
  --allow-http   # only on private nets / localhost
```

3. Enter the printed **P-code** in Marble Settings to confirm.
4. Run:

```bash
# Linux only, if needed (headless SSH without a seat will not work):
# export DISPLAY=:0
marble-peer run
```

Mini UI (status / confirm): `http://127.0.0.1:18765` (or next free port; see `~/.marble-peer/state.json`).

## Autostart + system tray

### Linux

Starts at login via **systemd --user** (and a GNOME autostart desktop file as backup).

```bash
go build -o bin/marble-peer ./cmd/marble-peer
ln -sfn "$PWD/bin/marble-peer" ~/.local/bin/marble-peer

marble-peer install-autostart   # writes unit + wrapper + enables now
systemctl --user status marble-peer
journalctl --user -u marble-peer -f
# or:
tail -f ~/.marble-peer/peer.log
```

Tray menu:

- Status (Online / Offline)
- Open mini UI
- Stop current action
- Quit marble-peer (clean exit — systemd will **not** restart)

Tray polls `/status.json` every **2s** (plus one immediate paint). Do **not**
`GLib.idle_add` a callback that returns `True` — that re-arms forever and will
pin CPU + Chrome CDP (see [docs/idle-cpu-status-storm.md](docs/idle-cpu-status-storm.md)).

### Keep display awake (and unlocked)

While `marble-peer run` is active it:

- inhibits **idle + suspend** (`gnome-session-inhibit`, GNOME SessionManager)
- inhibits **ScreenSaver** (lock-on-idle)
- softens GNOME settings for the peer lifetime: `idle-delay=0`, `lock-enabled=false` (restored on stop)
- re-applies `xset s off` / `s noblank` on a timer

```bash
# default on
MARBLE_PEER_KEEP_AWAKE=1 marble-peer run
# disable
MARBLE_PEER_KEEP_AWAKE=0 marble-peer run
```

**Note:** closing the laptop **lid** may still suspend depending on GNOME power settings. Prefer “blank” not “suspend”, or leave the lid open. If the session is **already** locked when peer starts, unlock once manually.

```bash
marble-peer uninstall-autostart   # disable + remove unit/desktop entry
marble-peer run --no-tray         # foreground without tray
```

### macOS

Starts at login via a **LaunchAgent** (`~/Library/LaunchAgents/com.rendicott.marble-peer.plist`). Keep-awake uses `caffeinate`. Tray is a Swift menu bar extra (compiled on first `run`).

```bash
# Binary must already live at a stable path (see macOS install above).
marble-peer doctor
marble-peer install-autostart
launchctl print gui/$(id -u)/com.rendicott.marble-peer
tail -f ~/Library/Logs/marble-peer.log
```

After the first LaunchAgent start, grant **Screen Recording** and **Accessibility** to `marble-peer` (System Settings). `KeepAlive` is false so tray **Quit** is a clean exit.

```bash
marble-peer uninstall-autostart
marble-peer run --no-tray
```

## CLI overview

| Command | Purpose |
|---------|---------|
| `marble-peer pair` | Complete mutual pairing with a harness H-code |
| `marble-peer run` | Long-running daemon (WS + actions + optional tray) |
| `marble-peer status` | Local JSON status + desktop availability |
| `marble-peer unpair` | Clear local computer id + device token |
| `marble-peer install-autostart` / `uninstall-autostart` | Login start (Linux systemd --user / macOS LaunchAgent) |
| `marble-peer doctor` | Local probe: Chrome, desktop tools, macOS TCC, screenshot |
| `marble-peer version` | Print version (release builds inject tag/commit/date) |

## Design & protocol

| Doc | Location |
|-----|----------|
| Wire protocol | [Marble `docs/peer-protocol.md`](https://github.com/rendicott/marble/blob/main/docs/peer-protocol.md) |
| System ADR | [Marble ADR-0020](https://github.com/rendicott/marble/blob/main/adr/0020-marble-peer.md) |
| Peer implementation ADR | [`adr/0001-desktop-agent.md`](adr/0001-desktop-agent.md) (canonical copy of marble ADR-0021) |
| Idle CPU incident | [docs/idle-cpu-status-storm.md](docs/idle-cpu-status-storm.md) |
| Changelog | [CHANGELOG.md](CHANGELOG.md) |

## Security

- Mini UI binds **127.0.0.1** only  
- Device token stored mode **0600** in `~/.marble-peer/credentials`  
- No cookie export; high-risk actions should use harness `computer_confirm`  
- Do not commit `~/.marble-peer` or machine-specific harness URLs with secrets  

## Releases (maintainers)

GitHub Actions builds on tags `v*` (and `workflow_dispatch` re-run). Same pattern as [marble-harness](https://github.com/rendicott/marble).

```bash
git tag -a v0.1.1 -m "v0.1.1"
git push origin v0.1.1
# Workflow "Release" tests on Linux + macOS, attaches assets
```

Linux binaries build on `ubuntu-latest`. Darwin binaries build on `macos-latest` (not cross-compiled from Linux). Unsigned portable files only — no `.dmg` or notarization.

If a tag exists but the workflow failed: **Actions → Release → Run workflow** → enter tag (e.g. `v0.1.1`).

Workflow: [`.github/workflows/release.yml`](.github/workflows/release.yml).

## Related

- Harness repo: https://github.com/rendicott/marble  
- Issues / computer-use tools live primarily in the harness; peer bugs and OS packaging belong here.
