# Idle CPU / laptop fan storm (status.json spin)

**Date:** 2026-08-02  
**Host:** rinux (Skylake dual-core laptop)  
**Symptom:** Fans always on while peer is “idle”; load ~2.9 on 4 threads; package ~73°C.

## What we saw

While `busy: false` and no agent actions were running:

| Process | ~CPU | Notes |
|--------|------|--------|
| `marble-peer` | ~75% | Serving `/status.json` ~900×/s |
| Chrome CDP mirror (`:9222`) | ~48% | DevTools probed on every status |
| `tray_linux.py` | ~35% | Tight GLib idle loop |
| TIME_WAIT on mini-UI port | ~12k | Client open/close storm |

RAM/disk were fine. This was pure control-plane burn, not screenshots or real browser work.

## Root cause

In `internal/tray/tray_linux.py` the tray registered:

```python
GLib.timeout_add_seconds(2, refresh)
GLib.idle_add(refresh)   # BUG
```

`refresh()` returns `True` so the **2s timer** keeps repeating (correct).

`GLib.idle_add` treats a `True` return as **“schedule me again immediately”**.  
So the idle source never drained: a tight loop that:

1. Spun one core in the tray process
2. HTTP GETs `http://127.0.0.1:<miniui>/status.json` hundreds–thousands of times per second
3. Each `StatusJSON()` called `desktop.Available()` and `Browser.Available()`
4. Browser probe hits Chrome CDP (`/json/version`), pinning the mirror Chrome

Net effect on a thin laptop: multi-core load, heat, constant fans — even with no Marble session activity.

## Fixes

### 1. Tray: one-shot idle refresh (primary)

```python
GLib.timeout_add_seconds(2, refresh)
# Return False from the idle callback so it does not re-arm.
GLib.idle_add(lambda: refresh() and False)
```

Idle still paints status once at startup; only the 2s timeout repeats.

### 2. StatusJSON: short TTL cache on expensive probes (defense in depth)

`internal/app/app.go` caches `desktop.Available()` + `Browser.Available()` for **2 seconds**.

Even if another client (or a future tray bug) polls `/status.json` too hard, we do not re-probe PATH/xdotool/CDP on every request.

## Verification (post-restart)

After rebuild + `systemctl --user restart marble-peer`:

- peer / tray / chrome mirror ≈ idle (~0–1% when not acting)
- CPU idle ~90%
- package temp dropped ~15–20°C on the same machine
- TIME_WAIT storm gone

## Operator notes

- Peer is designed to stay up (user systemd). It also keeps the session awake (`MARBLE_PEER_KEEP_AWAKE`, default on) so computer-use survives idle blanking — that can prevent deep sleep by design.
- When computer-use is not needed: `systemctl --user stop marble-peer` (or disable autostart).
- Real agent work (screenshots, CDP, desktop input) will still warm the chassis; that is expected load, not this bug.

## Files touched

- `internal/tray/tray_linux.py` — idle_add return value
- `internal/app/app.go` — `cachedStatusProbes` / `statusProbeTTL`
