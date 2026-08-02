# marble-desktop-peer

Desktop agent for [Marble](https://github.com/rendicott/marble) (ADR-0020 / ADR-0021).

Gives the Marble harness **remote hands and eyes** on your personal machine: desktop screenshots/input and a managed Chrome profile over CDP.

| | |
|--|--|
| **Binary** | `marble-peer` |
| **Module** | `github.com/rendicott/marble-desktop-peer` |
| **Data dir** | `~/.marble-peer` (`MARBLE_PEER_HOME`) |

## Build

```bash
go build -o bin/marble-peer ./cmd/marble-peer
```

Requires Go 1.18+ (matches Marble). Linux screenshot uses `gnome-screenshot` (or ImageMagick `import`). Desktop click/type needs `xdotool` when available.

## Your logged-in Chrome (default)

**Chrome 136+ blocks remote debugging on the default profile path**
(`~/.config/google-chrome`). Passing `--remote-debugging-port` there will start
Chrome but **never open a CDP port** — that was the failure mode you hit.

Marble’s default **`browser_mode=user`** therefore:

1. **Copies** your profile (cookies/logins) into  
   `~/.marble-peer/chrome-user-mirror`
2. Launches **that mirror** with CDP on port **9222**
3. Leaves your **daily Chrome** alone

Sync + launch:

```bash
marble-peer run
# or from the agent: computer_browser_ensure  (force=true re-syncs + restarts mirror)
```

Notes:
- Logins are as of the last sync (not a live attach to the daily window).
- Re-run `computer_browser_ensure` / `force=true` after you log into new sites in daily Chrome.
- Blank isolated profile: `marble-peer run --browser-mode marble`

## Pair (mutual handshake)

1. In Marble: **Settings → Computers → Pair** (or `POST /api/computers/pair/start`) → copy **H-code**.
2. On this machine:

```bash
./bin/marble-peer pair \
  --harness http://127.0.0.1:8080 \
  --code HXXXXX \
  --allow-http   # only on private nets / localhost
```

3. Enter the printed **P-code** in Marble Settings to confirm.
4. Run:

```bash
export DISPLAY=:0   # if needed
./bin/marble-peer run
```

Mini UI (status / confirm): `http://127.0.0.1:18765` (or next free port; see `~/.marble-peer/state.json`).

## Autostart + system tray (Linux)

Starts at login via **systemd --user** (and a GNOME autostart desktop file as backup). Tray menu uses AppIndicator (needs `gir1.2-ayatanaappindicator3-0.1`, usually already on Ubuntu).

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

## Design

- Protocol: Marble `docs/peer-protocol.md`
- System ADR: marble `adr/0020-marble-peer.md`
- Peer ADR: marble `adr/0021-marble-desktop-peer.md` (copy to `adr/0001` when publishing this repo)
- Ops / incident notes: [docs/idle-cpu-status-storm.md](docs/idle-cpu-status-storm.md)
- Changelog: [CHANGELOG.md](CHANGELOG.md)

## Security

- Mini UI binds **127.0.0.1** only
- Device token stored mode **0600** in `~/.marble-peer/credentials`
- No cookie export; high-risk actions should use `computer_confirm`
