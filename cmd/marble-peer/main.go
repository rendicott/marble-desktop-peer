package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rendicott/marble-desktop-peer/internal/app"
	"github.com/rendicott/marble-desktop-peer/internal/autostart"
	"github.com/rendicott/marble-desktop-peer/internal/browser"
	"github.com/rendicott/marble-desktop-peer/internal/config"
	"github.com/rendicott/marble-desktop-peer/internal/desktop"
	"github.com/rendicott/marble-desktop-peer/internal/tray"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("marble-peer ")

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "version", "-version", "--version":
		fmt.Println("marble-peer", app.PeerVersion)
	case "pair":
		fs := flag.NewFlagSet("pair", flag.ExitOnError)
		harness := fs.String("harness", "", "Marble harness base URL")
		code := fs.String("code", "", "H-code from Settings")
		allowHTTP := fs.Bool("allow-http", false, "Allow cleartext HTTP harness URL")
		serve := fs.Bool("serve", false, "Open mini UI for pairing only")
		_ = fs.Parse(args)
		if *serve {
			runPairServe()
			return
		}
		if err := app.Pair(*harness, *code, *allowHTTP); err != nil {
			fmt.Fprintln(os.Stderr, "pair:", err)
			os.Exit(1)
		}
	case "status":
		cfg, _ := config.Load()
		tok, _ := config.LoadToken()
		a := app.New(cfg, tok)
		b, _ := json.MarshalIndent(a.StatusJSON(), "", "  ")
		fmt.Println(string(b))
		desk, note := desktop.Available()
		fmt.Printf("desktop: %v (%s)\n", desk, note)
	case "unpair":
		cfg, _ := config.Load()
		cfg.ComputerID = ""
		cfg.HarnessURL = ""
		_ = config.Save(cfg)
		_ = config.ClearToken()
		fmt.Println("unpaired local credentials")
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		killBrowser := fs.Bool("kill-browser-on-exit", false, "Kill Chrome only if marble-peer launched it")
		noMini := fs.Bool("no-miniui", false, "Disable localhost mini UI")
		noTray := fs.Bool("no-tray", false, "Disable system tray icon")
		browserMode := fs.String("browser-mode", "", "user (default, your Chrome profile) | marble (isolated)")
		cdpPort := fs.Int("cdp-port", 0, "Attach to this CDP port (default try 9222)")
		_ = fs.Parse(args)
		runDaemon(*killBrowser, !*noMini, !*noTray, *browserMode, *cdpPort)
	case "install-autostart":
		fs := flag.NewFlagSet("install-autostart", flag.ExitOnError)
		noEnable := fs.Bool("no-enable", false, "Only write unit files; do not enable/start")
		_ = fs.Parse(args)
		exe, err := os.Executable()
		if err != nil {
			log.Fatal(err)
		}
		if resolved, e := filepath.EvalSymlinks(exe); e == nil {
			exe = resolved
		}
		paths, msg, err := autostart.Install(exe, !*noEnable)
		for _, p := range paths {
			fmt.Println("wrote", p)
		}
		fmt.Println(msg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "note:", err)
			// non-zero if enable failed but files written is still useful
			os.Exit(1)
		}
	case "uninstall-autostart":
		paths, msg, err := autostart.Uninstall()
		for _, p := range paths {
			fmt.Println("removed", p)
		}
		fmt.Println(msg)
		if err != nil {
			log.Fatal(err)
		}
	case "print-chrome-cmd":
		fmt.Println(browser.PrintChromeCmd())
		fmt.Fprintln(os.Stderr, "\n# Quit all Chrome windows first, then run the command above.")
		fmt.Fprintln(os.Stderr, "# Leave that Chrome open; in another terminal: marble-peer run")
		fmt.Fprintln(os.Stderr, "# Peer attaches to port 9222 and uses YOUR profile (cookies/logins).")
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `marble-peer — desktop agent for Marble (ADR-0021)

Commands:
  pair --harness URL --code H-XXXXXX [--allow-http]
  pair --serve
  run [--kill-browser-on-exit] [--no-miniui] [--no-tray]
  status
  unpair
  install-autostart [--no-enable]   # systemd --user + desktop autostart (login)
  uninstall-autostart
  print-chrome-cmd
  version

Browser modes (config browser_mode or MARBLE_PEER_BROWSER_MODE):
  user   (default) — mirror of ~/.config/google-chrome (your logins) + CDP
  marble           — isolated ~/.marble-peer/chrome-profile

Autostart (Linux):
  marble-peer install-autostart
  systemctl --user status marble-peer
  journalctl --user -u marble-peer -f
  tail -f ~/.marble-peer/peer.log
`)
}

func runDaemon(killBrowser, miniui, useTray bool, browserMode string, cdpPort int) {
	// Also log to ~/.marble-peer/peer.log when not already redirected by systemd
	setupFileLog()

	if err := config.EnsureHome(); err != nil {
		log.Fatal(err)
	}
	// Single instance: two peers for the same device thrash the harness WS (1006 loop).
	lock, err := config.AcquireRunLock()
	if err != nil {
		log.Fatal(err)
	}
	defer lock.Close()

	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	cfg.KillBrowser = killBrowser
	if browserMode != "" {
		cfg.BrowserMode = browserMode
	}
	if cdpPort > 0 {
		cfg.CDPPort = cdpPort
	}
	if cfg.BrowserMode == "" {
		cfg.BrowserMode = browser.ModeUser
	}
	_ = config.Save(cfg)

	tok, err := config.LoadToken()
	if err != nil || tok == "" {
		log.Fatal("not paired — run: marble-peer pair --harness … --code …")
	}
	if err := app.EnsureDeviceID(&cfg); err != nil {
		log.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	a := app.New(cfg, tok)

	var miniMu sync.Mutex
	miniAddr := ""
	setMini := func(addr string) {
		miniMu.Lock()
		miniAddr = addr
		miniMu.Unlock()
	}
	getMini := func() string {
		miniMu.Lock()
		defer miniMu.Unlock()
		return miniAddr
	}

	if miniui {
		go func() {
			addr, err := serveMiniUI(ctx, a, cancel)
			if err != nil && ctx.Err() == nil {
				log.Printf("miniui: %v", err)
				return
			}
			if addr != "" {
				setMini(addr)
			}
		}()
	}

	if useTray {
		go func() {
			err := tray.Start(ctx, tray.Hooks{
				Status: func() string { return a.State() },
				MiniUIAddr: func() string {
					if u := getMini(); u != "" {
						return u
					}
					// brief wait for bind
					deadline := time.Now().Add(8 * time.Second)
					for time.Now().Before(deadline) {
						if u := getMini(); u != "" {
							return u
						}
						time.Sleep(150 * time.Millisecond)
					}
					return getMini()
				},
				StopAction: func() { a.Q.Cancel() },
				Quit:       cancel,
				ComputerID: func() string { return a.Cfg.ComputerID },
			})
			if err != nil && ctx.Err() == nil {
				log.Printf("tray: %v", err)
			}
		}()
	}

	if err := a.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
	a.Browser.Stop(killBrowser && a.Browser.Owned())
	log.Printf("exit")
}

func setupFileLog() {
	// Under systemd, StandardOutput/Error already append to peer.log — avoid double lines.
	if os.Getenv("INVOCATION_ID") != "" || os.Getenv("JOURNAL_STREAM") != "" {
		return
	}
	_ = config.EnsureHome()
	path := filepath.Join(config.Home(), "peer.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	// Terminal start: tee to peer.log for later inspection.
	log.SetOutput(&teeWriter{a: os.Stderr, b: f})
}

type teeWriter struct {
	a, b *os.File
}

func (t *teeWriter) Write(p []byte) (int, error) {
	n, err := t.a.Write(p)
	_, _ = t.b.Write(p)
	return n, err
}

// serveMiniUI binds localhost control UI. cancel is invoked by POST /quit (tray).
func serveMiniUI(ctx context.Context, a *app.App, cancel context.CancelFunc) (string, error) {
	mux := http.NewServeMux()
	ports := []int{18765, 18766, 18767, 18768, 18769, 18770, 18771, 18772, 18773, 18774, 18775, 0}
	var ln net.Listener
	var addr string
	var err error
	for _, p := range ports {
		if p == 0 {
			ln, err = net.Listen("tcp", "127.0.0.1:0")
		} else {
			ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		}
		if err == nil {
			addr = ln.Addr().String()
			break
		}
	}
	if ln == nil {
		return "", fmt.Errorf("bind miniui: %v", err)
	}
	base := "http://" + addr
	_ = config.WriteState(map[string]interface{}{"miniui_addr": base})
	log.Printf("miniui %s", base)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		st := a.StatusJSON()
		st["miniui_addr"] = base
		b, _ := json.MarshalIndent(st, "", "  ")
		// Pending confirms banner — primary human path for computer_confirm
		banner := ""
		if pending, ok := st["pending_confirms"].([]app.ConfirmRequest); ok && len(pending) > 0 {
			banner = `<div style="background:#422006;border:1px solid #f59e0b;border-radius:10px;padding:1rem 1.25rem;margin-bottom:1.25rem">
<h2 style="margin:0 0 0.5rem;color:#fbbf24">⚠️ Action waiting for your confirmation</h2>`
			for _, c := range pending {
				banner += fmt.Sprintf(`<p style="margin:0.5rem 0;white-space:pre-wrap">%s</p>
<p><a href="%s" style="display:inline-block;padding:0.6rem 1rem;background:#16a34a;color:#fff;border-radius:8px;text-decoration:none;font-weight:600">Review Accept / Deny</a>
 <span style="color:#9aa3b5;font-size:0.85rem"> risk=%s · expires ~2 min</span></p>`,
					htmlEsc(c.Prompt), htmlEsc(c.URL), htmlEsc(c.Risk))
			}
			banner += `</div>`
		}
		fmt.Fprintf(w, `<!doctype html><meta charset=utf-8><title>marble-peer</title>
<meta http-equiv="refresh" content="5">
<body style="font-family:system-ui;background:#0f1115;color:#e8ecf4;padding:1.5rem;max-width:52rem">
<h1 style="margin-top:0">marble-peer</h1>
%s
<pre style="background:#171a21;padding:1rem;border-radius:8px;overflow:auto">%s</pre>
<p>
  <a style="color:#7c9cff" href="/pair">Pair</a> ·
  <a style="color:#7c9cff" href="/status.json">status.json</a> ·
  <form style="display:inline" method=post action="/stop"><button>Stop action</button></form>
  <form style="display:inline" method=post action="/quit"><button>Quit</button></form>
</p>
<p style="color:#6b7280;font-size:0.85rem">When the agent calls <code>computer_confirm</code>, a notification opens and this page shows Accept/Deny. Default is deny after 120s.</p>
</body>`, banner, string(b))
	})
	mux.HandleFunc("/status.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		st := a.StatusJSON()
		st["miniui_addr"] = base
		_ = json.NewEncoder(w).Encode(st)
	})
	mux.HandleFunc("/pair", a.HandlePairHTTP)
	mux.HandleFunc("/confirm/", a.HandleConfirmHTTP)
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		a.Q.Cancel()
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		fmt.Fprint(w, "bye")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		log.Printf("quit requested via miniui/tray")
		cancel()
	})

	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		return base, nil
	}
	return base, err
}

func runPairServe() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	cfg, _ := config.Load()
	a := app.New(cfg, "")
	if _, err := serveMiniUI(ctx, a, cancel); err != nil {
		log.Fatal(err)
	}
}

func htmlEsc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}
