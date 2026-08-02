#!/usr/bin/env python3
"""Marble peer system tray (AppIndicator). Spawned by marble-peer run.

Env:
  MARBLE_PEER_PID       parent Go process to signal on Quit
  MARBLE_PEER_STATUS_URL  http://127.0.0.1:PORT/status.json
  MARBLE_PEER_MINIUI      http://127.0.0.1:PORT/
"""
from __future__ import annotations

import json
import os
import signal
import sys
import threading
import time
import urllib.error
import urllib.request
import webbrowser

import gi

gi.require_version("Gtk", "3.0")
from gi.repository import GLib, Gtk  # noqa: E402

# Prefer Ayatana (Ubuntu 22+), fall back to legacy AppIndicator3.
Indicator = None
for mod, ver in (("AyatanaAppIndicator3", "0.1"), ("AppIndicator3", "0.1")):
    try:
        gi.require_version(mod, ver)
        Indicator = __import__("gi.repository", fromlist=[mod]).__dict__[mod]
        break
    except Exception:
        continue

if Indicator is None:
    sys.stderr.write("marble-peer tray: no AppIndicator library (install gir1.2-ayatanaappindicator3-0.1)\n")
    sys.exit(0)


def fetch_status(url: str) -> dict:
    if not url:
        return {}
    try:
        req = urllib.request.Request(url, headers={"Accept": "application/json"})
        with urllib.request.urlopen(req, timeout=2) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except Exception:
        return {}


def main() -> None:
    parent = int(os.environ.get("MARBLE_PEER_PID", "0") or "0")
    status_url = os.environ.get("MARBLE_PEER_STATUS_URL", "")
    miniui = os.environ.get("MARBLE_PEER_MINIUI", "")

    # Wait briefly for mini UI to bind
    for _ in range(40):
        st = fetch_status(status_url)
        if st or not status_url:
            break
        time.sleep(0.25)
        # re-read env-less: parent may write state; status_url is fixed at start

    indicator = Indicator.Indicator.new(
        "marble-peer",
        "network-workgroup",
        Indicator.IndicatorCategory.APPLICATION_STATUS,
    )
    indicator.set_status(Indicator.IndicatorStatus.ACTIVE)
    indicator.set_title("Marble Peer")

    menu = Gtk.Menu()

    item_status = Gtk.MenuItem(label="Status: starting…")
    item_status.set_sensitive(False)
    menu.append(item_status)

    item_id = Gtk.MenuItem(label="Computer: —")
    item_id.set_sensitive(False)
    menu.append(item_id)

    menu.append(Gtk.SeparatorMenuItem())

    item_confirm = Gtk.MenuItem(label="No pending confirmations")
    item_confirm.set_sensitive(False)

    def on_confirm(_w):
        st = fetch_status(status_url)
        pending = st.get("pending_confirms") or []
        if pending:
            url = pending[0].get("url") or ""
            if url:
                webbrowser.open(url)
                return
        base = st.get("miniui_addr") or miniui
        if base:
            webbrowser.open(base)

    item_confirm.connect("activate", on_confirm)
    menu.append(item_confirm)

    item_open = Gtk.MenuItem(label="Open mini UI")
    def on_open(_w):
        url = miniui
        st = fetch_status(status_url)
        if st.get("miniui_addr"):
            url = st["miniui_addr"]
        if url:
            webbrowser.open(url)
    item_open.connect("activate", on_open)
    menu.append(item_open)

    item_stop = Gtk.MenuItem(label="Stop current action")
    def on_stop(_w):
        st = fetch_status(status_url)
        base = st.get("miniui_addr") or miniui
        if not base:
            return
        try:
            req = urllib.request.Request(
                base.rstrip("/") + "/stop",
                data=b"",
                method="POST",
            )
            urllib.request.urlopen(req, timeout=3).read()
        except Exception as e:
            sys.stderr.write(f"tray stop: {e}\n")
    item_stop.connect("activate", on_stop)
    menu.append(item_stop)

    menu.append(Gtk.SeparatorMenuItem())

    item_quit = Gtk.MenuItem(label="Quit marble-peer")
    def on_quit(_w):
        # Prefer HTTP quit so Go flushes logs; then signal parent.
        st = fetch_status(status_url)
        base = st.get("miniui_addr") or miniui
        if base:
            try:
                req = urllib.request.Request(
                    base.rstrip("/") + "/quit",
                    data=b"",
                    method="POST",
                )
                urllib.request.urlopen(req, timeout=3).read()
            except Exception:
                pass
        if parent > 0:
            try:
                os.kill(parent, signal.SIGTERM)
            except ProcessLookupError:
                pass
        Gtk.main_quit()
    item_quit.connect("activate", on_quit)
    menu.append(item_quit)

    menu.show_all()
    indicator.set_menu(menu)
    # Some shells assert if set_title is called before the indicator is realized.
    GLib.idle_add(lambda: indicator.set_status(Indicator.IndicatorStatus.ACTIVE) or False)

    def refresh():
        try:
            st = fetch_status(status_url)
            state = st.get("state") or ("unknown" if not st else "…")
            cid = st.get("computer_id") or "—"
            browser = "browser" if st.get("browser_ready") else "no-browser"
            pending = st.get("pending_confirms") or []
            nconf = st.get("confirm_count") or len(pending)
            item_status.set_label(f"Status: {state} ({browser})")
            item_id.set_label(f"Computer: {cid}")
            if nconf:
                item_confirm.set_label(f"⚠️ Confirm action ({nconf}) — click")
                item_confirm.set_sensitive(True)
                tip = f"Marble Peer — CONFIRM needed ({nconf})"
            else:
                item_confirm.set_label("No pending confirmations")
                item_confirm.set_sensitive(False)
                tip = f"Marble Peer — {state}"
                if cid and cid != "—":
                    tip += f" ({cid})"
            try:
                indicator.set_title(tip)
            except Exception:
                pass
        except Exception as e:
            sys.stderr.write(f"tray refresh: {e}\n")
        # Exit tray if parent is gone
        if parent > 0:
            try:
                os.kill(parent, 0)
            except ProcessLookupError:
                Gtk.main_quit()
                return False
        return True

    # Poll status every 2s on GTK main loop.
    # IMPORTANT: idle callbacks must return False (or not True). refresh() returns
    # True so the 2s timeout keeps firing; if we idle_add(refresh) directly, GLib
    # treats True as "run again" and spins a tight idle loop (~100% of a core,
    # thousands of status.json hits/sec, laptop fans).
    GLib.timeout_add_seconds(2, refresh)
    GLib.idle_add(lambda: refresh() and False)

    def watch_parent():
        while True:
            time.sleep(2)
            if parent <= 0:
                continue
            try:
                os.kill(parent, 0)
            except ProcessLookupError:
                GLib.idle_add(Gtk.main_quit)
                return

    threading.Thread(target=watch_parent, daemon=True).start()
    Gtk.main()


if __name__ == "__main__":
    main()
