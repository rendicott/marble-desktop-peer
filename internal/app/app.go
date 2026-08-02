package app

import (
	"context"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rendicott/marble-desktop-peer/internal/browser"
	"github.com/rendicott/marble-desktop-peer/internal/config"
	"github.com/rendicott/marble-desktop-peer/internal/desktop"
	"github.com/rendicott/marble-desktop-peer/internal/keepalive"
	"github.com/rendicott/marble-desktop-peer/internal/protocol"
	"github.com/rendicott/marble-desktop-peer/internal/queue"
)

const PeerVersion = "0.1.0-dev"

// App is the long-running peer daemon.
type App struct {
	Cfg     config.File
	Token   string
	Browser *browser.Manager
	Q       *queue.Queue

	mu     sync.Mutex
	state  string
	ws     *websocket.Conn
	cancel context.CancelFunc

	// writeMu serializes all websocket writes. gorilla/websocket panics on concurrent writers
	// ("concurrent write to websocket connection") — ping/pong + action results race without this.
	writeMu sync.Mutex

	// confirm
	confirmMu   sync.Mutex
	confirmCh   map[string]chan bool
	confirmMeta map[string]ConfirmRequest // pending UI metadata
}

// ConfirmRequest is a high-risk action waiting for the human on the peer machine.
type ConfirmRequest struct {
	ID        string    `json:"id"`
	Prompt    string    `json:"prompt"`
	Risk      string    `json:"risk"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func New(cfg config.File, token string) *App {
	mode := cfg.BrowserMode
	if mode == "" {
		mode = browser.ModeUser
	}
	return &App{
		Cfg:   cfg,
		Token: token,
		Browser: browser.NewWithOptions(browser.Options{
			Mode:    mode,
			CDPPort: cfg.CDPPort,
		}),
		Q:           queue.New(),
		state:       "Offline",
		confirmCh:   make(map[string]chan bool),
		confirmMeta: make(map[string]ConfirmRequest),
	}
}

func (a *App) caps() protocol.Caps {
	desk, _ := desktop.Available()
	return protocol.Caps{
		Browser: a.Browser.Available(),
		Desktop: desk,
		Confirm: true,
	}
}

// Run connects WS and serves actions until ctx done.
func (a *App) Run(ctx context.Context) error {
	if a.Cfg.HarnessURL == "" || a.Token == "" || a.Cfg.DeviceID == "" {
		return fmt.Errorf("not paired — run: marble-peer pair")
	}
	// Keep display/session awake so XWayland and desktop input survive idle blanking.
	// Disable with MARBLE_PEER_KEEP_AWAKE=0.
	awake := keepalive.Start(ctx)
	defer awake.Stop()

	// start/attach browser best-effort (do not fail the peer if CDP is slow)
	go func() {
		bctx, bcancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer bcancel()
		log.Printf("browser: mode=%s profile=%s source=%s", a.Browser.Mode(), a.Browser.ProfileDir(), a.Browser.SourceDir())
		// Prefer Ensure (sync+launch mirror) over bare Start for user mode.
		if er, err := a.Browser.Ensure(bctx, false); err != nil {
			log.Printf("browser: %v (desktop still available)", err)
		} else {
			log.Printf("browser: CDP ready action=%s port=%d owned=%v — %s", er.Action, er.Port, er.Owned, er.Message)
		}
	}()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := a.session(ctx); err != nil {
			log.Printf("ws session: %v — retry in 3s", err)
			a.setState("Offline")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
			}
			continue
		}
		// clean session end (should be rare) — reconnect
		log.Printf("ws session ended cleanly; reconnecting")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (a *App) setState(s string) {
	a.mu.Lock()
	a.state = s
	a.mu.Unlock()
	log.Printf("state=%s", s)
}

func (a *App) State() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// wsWrite serializes all writes on the active peer↔harness connection.
// Must be used for every WriteJSON after the connection is stored on a.ws.
func (a *App) wsWrite(v interface{}) error {
	a.mu.Lock()
	w := a.ws
	a.mu.Unlock()
	if w == nil {
		return fmt.Errorf("websocket not connected")
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_ = w.SetWriteDeadline(time.Now().Add(30 * time.Second))
	err := w.WriteJSON(v)
	_ = w.SetWriteDeadline(time.Time{})
	return err
}

func (a *App) session(ctx context.Context) error {
	u, err := wsURL(a.Cfg.HarnessURL, a.Cfg.DeviceID, a.Token)
	if err != nil {
		return err
	}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	ws, _, err := dialer.DialContext(ctx, u, nil)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.ws = ws
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.ws = nil
		a.mu.Unlock()
		// Close under write lock so no writer races with Close.
		a.writeMu.Lock()
		_ = ws.Close()
		a.writeMu.Unlock()
	}()

	caps := a.caps()
	hello := protocol.Envelope{
		Type:            "hello",
		ProtocolVersion: protocol.Version,
		DeviceID:        a.Cfg.DeviceID,
		Token:           a.Token,
		OS:              runtime.GOOS,
		PeerVersion:     PeerVersion,
		Caps:            &caps,
	}
	if err := a.wsWrite(hello); err != nil {
		return err
	}
	_, data, err := ws.ReadMessage()
	if err != nil {
		return err
	}
	var ack protocol.Envelope
	if err := json.Unmarshal(data, &ack); err != nil || ack.Type != "hello_ack" {
		return fmt.Errorf("expected hello_ack")
	}
	if ack.ComputerID != "" {
		a.Cfg.ComputerID = ack.ComputerID
		_ = config.Save(a.Cfg)
	}
	a.setState("Online")
	log.Printf("connected as computer_id=%s", a.Cfg.ComputerID)

	// Client-side keepalive as backup if hub pings are missing.
	// Uses wsWrite so it never races pong/result writers.
	stopPing := make(chan struct{})
	defer close(stopPing)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopPing:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				if err := a.wsWrite(protocol.Envelope{Type: "ping"}); err != nil {
					return
				}
			}
		}
	}()

	for {
		_ = ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		_, data, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		var env protocol.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		switch env.Type {
		case "ping":
			if err := a.wsWrite(protocol.Envelope{Type: "pong"}); err != nil {
				return err
			}
		case "pong":
			// hub keepalive reply
		case "cancel":
			a.Q.Cancel()
		case "confirm_resolve":
			// Human accepted/denied from Marble harness UI (not only peer mini-UI).
			_ = a.ResolveConfirm(env.ID, env.OK)
			log.Printf("CONFIRM resolve from harness id=%s accept=%v", env.ID, env.OK)
		case "action":
			go a.handleAction(env)
		}
	}
}

func (a *App) handleAction(env protocol.Envelope) {
	deadline := time.Duration(env.DeadlineMS) * time.Millisecond
	if deadline <= 0 {
		deadline = 120 * time.Second
	}
	if deadline > 5*time.Minute {
		deadline = 5 * time.Minute
	}
	parent, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	err := a.Q.Run(parent, func(ctx context.Context) error {
		res := a.exec(ctx, env)
		res.Type = "result"
		res.ID = env.ID
		if werr := a.wsWrite(res); werr != nil {
			return werr
		}
		return nil
	})
	if err != nil {
		// Queue rejected (busy/cancel) or write failed — still try to surface error once.
		_ = a.wsWrite(protocol.Envelope{
			Type: "result", ID: env.ID, OK: false, Error: err.Error(),
		})
	}
}

func (a *App) exec(ctx context.Context, env protocol.Envelope) protocol.Envelope {
	var payload map[string]interface{}
	_ = json.Unmarshal(env.Payload, &payload)
	if payload == nil {
		payload = map[string]interface{}{}
	}
	switch env.Kind {
	case "screenshot":
		img, meta, err := desktop.Screenshot(ctx)
		if err != nil {
			return protocol.Envelope{OK: false, Error: err.Error()}
		}
		ls := desktop.QueryLockState(ctx)
		m := map[string]interface{}{
			"w": meta.W, "h": meta.H, "scale": meta.Scale,
			"locked": ls.Locked, "lock_source": ls.Source,
		}
		text := ""
		if ls.Locked {
			text = desktop.FormatLockWarning(ls)
		}
		return protocol.Envelope{
			OK:            true,
			ScreenshotB64: base64.StdEncoding.EncodeToString(img),
			Meta:          m,
			Text:          text,
		}
	case "desktop_click":
		x, _ := asInt(payload["x"])
		y, _ := asInt(payload["y"])
		btn := asButton(payload["button"])
		if err := desktop.Click(ctx, x, y, btn); err != nil {
			return protocol.Envelope{OK: false, Error: err.Error()}
		}
		return protocol.Envelope{OK: true, Text: fmt.Sprintf("clicked (%d,%d) button=%s", x, y, btn)}
	case "desktop_type":
		text, _ := payload["text"].(string)
		if err := desktop.Type(ctx, text); err != nil {
			return protocol.Envelope{OK: false, Error: err.Error()}
		}
		return protocol.Envelope{OK: true}
	case "desktop_key":
		key, _ := payload["key"].(string)
		if err := desktop.Key(ctx, key); err != nil {
			return protocol.Envelope{OK: false, Error: err.Error()}
		}
		return protocol.Envelope{OK: true}
	case "browser_ensure":
		force, _ := payload["force"].(bool)
		er, err := a.Browser.Ensure(ctx, force)
		b, _ := json.Marshal(er)
		if err != nil {
			return protocol.Envelope{OK: false, Error: err.Error(), Text: string(b)}
		}
		return protocol.Envelope{OK: true, Text: string(b), Meta: map[string]interface{}{
			"action": er.Action, "port": er.Port, "owned": er.Owned, "mode": er.Mode, "profile": er.Profile,
		}}
	case "browser_tabs":
		if err := a.ensureBrowser(ctx); err != nil {
			return protocol.Envelope{OK: false, Error: err.Error()}
		}
		t, err := a.Browser.TabsJSON(ctx)
		if err != nil {
			return protocol.Envelope{OK: false, Error: err.Error()}
		}
		return protocol.Envelope{OK: true, Text: t}
	case "browser_open":
		if err := a.ensureBrowser(ctx); err != nil {
			return protocol.Envelope{OK: false, Error: err.Error()}
		}
		u, _ := payload["url"].(string)
		nt, _ := payload["new_tab"].(bool)
		if err := a.Browser.Open(ctx, u, nt); err != nil {
			return protocol.Envelope{OK: false, Error: err.Error()}
		}
		return protocol.Envelope{OK: true}
	case "browser_snapshot":
		if err := a.ensureBrowser(ctx); err != nil {
			return protocol.Envelope{OK: false, Error: err.Error()}
		}
		t, err := a.Browser.Snapshot(ctx)
		if err != nil {
			return protocol.Envelope{OK: false, Error: err.Error()}
		}
		return protocol.Envelope{OK: true, Text: t}
	case "browser_act":
		if err := a.ensureBrowser(ctx); err != nil {
			return protocol.Envelope{OK: false, Error: err.Error()}
		}
		action, _ := payload["action"].(string)
		target, _ := payload["target"].(string)
		text, _ := payload["text"].(string)
		x, _ := asInt(payload["x"])
		y, _ := asInt(payload["y"])
		result, err := a.Browser.Act(ctx, action, target, text, x, y)
		if err != nil {
			return protocol.Envelope{OK: false, Error: err.Error(), Text: result}
		}
		if result == "" {
			result = "ok"
		}
		return protocol.Envelope{OK: true, Text: result}
	case "confirm":
		prompt, _ := payload["prompt"].(string)
		risk, _ := payload["risk"].(string)
		ok, detail := a.waitConfirm(ctx, env.ID, prompt, risk)
		return protocol.Envelope{Type: "confirm_result", ID: env.ID, OK: ok, Text: detail}
	default:
		return protocol.Envelope{OK: false, Error: "unknown kind " + env.Kind}
	}
}

// ensureBrowser makes CDP ready without force-killing the user's Chrome.
func (a *App) ensureBrowser(ctx context.Context) error {
	if a.Browser.Available() {
		return nil
	}
	er, err := a.Browser.Ensure(ctx, false)
	if err != nil {
		return fmt.Errorf("%v — try computer_browser_ensure with force=true (relaunches your Chrome with CDP; needs confirm for safety from agent), or run: marble-peer print-chrome-cmd", err)
	}
	if !er.OK {
		return fmt.Errorf("browser not ready: %s", er.Message)
	}
	return nil
}

func asInt(v interface{}) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case json.Number:
		i, err := t.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}

// asButton normalizes mouse button for xdotool. Empty/"0"/wrong types become "1".
// Passing "" to xdotool click causes BadValue (XTest) on this machine.
func asButton(v interface{}) string {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" || s == "0" {
			return "1"
		}
		if s == "left" {
			return "1"
		}
		if s == "middle" {
			return "2"
		}
		if s == "right" {
			return "3"
		}
		return s
	case float64:
		if t <= 0 {
			return "1"
		}
		return strconv.Itoa(int(t))
	case int:
		if t <= 0 {
			return "1"
		}
		return strconv.Itoa(t)
	default:
		return "1"
	}
}

func (a *App) waitConfirm(ctx context.Context, id, prompt, risk string) (accepted bool, detail string) {
	if id == "" {
		id = uuid.NewString()
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "Agent requests confirmation for a high-risk action"
	}
	if risk == "" {
		risk = "high"
	}
	base := a.miniUIBase()
	confirmURL := strings.TrimRight(base, "/") + "/confirm/" + id
	expires := time.Now().Add(120 * time.Second)

	ch := make(chan bool, 1)
	a.confirmMu.Lock()
	a.confirmCh[id] = ch
	a.confirmMeta[id] = ConfirmRequest{
		ID: id, Prompt: prompt, Risk: risk, URL: confirmURL,
		CreatedAt: time.Now(), ExpiresAt: expires,
	}
	a.confirmMu.Unlock()
	defer func() {
		a.confirmMu.Lock()
		delete(a.confirmCh, id)
		delete(a.confirmMeta, id)
		a.confirmMu.Unlock()
	}()

	log.Printf("CONFIRM required id=%s risk=%s url=%s prompt=%s", id, risk, confirmURL, prompt)
	fmt.Fprintf(os.Stderr, "\n*** CONFIRM (%s) — open this URL and click Accept or Deny (120s):\n    %s\n    prompt: %s\n\n", risk, confirmURL, prompt)

	// Surface on the operator's desktop (best-effort).
	go notifyConfirm(prompt, risk, confirmURL)

	select {
	case v := <-ch:
		if v {
			return true, "accepted by human (Marble harness UI or peer mini-UI/tray)"
		}
		return false, "denied by human (Marble harness UI or peer mini-UI/tray)"
	case <-ctx.Done():
		return false, "confirm canceled (context done); open " + confirmURL + " within 120s next time"
	case <-time.After(120 * time.Second):
		return false, "confirm timed out after 120s (default deny). Human must open " + confirmURL + " and click Accept"
	}
}

// miniUIBase returns the live mini UI origin from state.json (or default).
func (a *App) miniUIBase() string {
	if b, err := os.ReadFile(filepath.Join(config.Home(), "state.json")); err == nil {
		var st map[string]interface{}
		if json.Unmarshal(b, &st) == nil {
			if s, ok := st["miniui_addr"].(string); ok && s != "" {
				return s
			}
		}
	}
	port := 18765
	if a.Cfg.MiniUIPort > 0 {
		port = a.Cfg.MiniUIPort
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// PendingConfirms returns outstanding human confirmations for UI/tray.
func (a *App) PendingConfirms() []ConfirmRequest {
	a.confirmMu.Lock()
	defer a.confirmMu.Unlock()
	out := make([]ConfirmRequest, 0, len(a.confirmMeta))
	now := time.Now()
	for _, c := range a.confirmMeta {
		if now.After(c.ExpiresAt) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ResolveConfirm is called from mini UI.
func (a *App) ResolveConfirm(id string, accept bool) bool {
	a.confirmMu.Lock()
	ch := a.confirmCh[id]
	a.confirmMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- accept:
		log.Printf("CONFIRM resolved id=%s accept=%v", id, accept)
		return true
	default:
		return false
	}
}

// notifyConfirm shows a desktop notification and opens the confirm page in a browser.
func notifyConfirm(prompt, risk, confirmURL string) {
	// notify-send (GNOME)
	if _, err := exec.LookPath("notify-send"); err == nil {
		title := "Marble Peer — confirmation required"
		if risk != "" {
			title += " (" + risk + ")"
		}
		body := prompt
		if len(body) > 180 {
			body = body[:180] + "…"
		}
		body += "\nOpen: " + confirmURL
		_ = exec.Command("notify-send", "-u", "critical", "-t", "120000",
			"--app-name=marble-peer", title, body).Start()
	}
	// Open the Accept/Deny page
	if _, err := exec.LookPath("xdg-open"); err == nil {
		_ = exec.Command("xdg-open", confirmURL).Start()
	}
}

func wsURL(harness, deviceID, token string) (string, error) {
	u, err := url.Parse(strings.TrimRight(harness, "/"))
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("bad harness url scheme")
	}
	u.Path = "/api/computers/ws"
	q := u.Query()
	q.Set("device_id", deviceID)
	q.Set("token", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// EnsureDeviceID loads or creates device id.
func EnsureDeviceID(cfg *config.File) error {
	if cfg.DeviceID != "" {
		return nil
	}
	cfg.DeviceID = uuid.NewString()
	return config.Save(*cfg)
}

// statusProbeCache avoids hammering PATH lookups + CDP HTTP probes when the tray
// or mini-UI polls /status.json. Without this, a buggy idle-spin client can issue
// hundreds of Available() probes per second and pin Chrome DevTools + the peer.
type statusProbeCache struct {
	mu        sync.Mutex
	at        time.Time
	desk      bool
	deskNote  string
	browserOK bool
}

const statusProbeTTL = 2 * time.Second

var globalStatusProbe statusProbeCache

func cachedStatusProbes(a *App) (desk bool, deskNote string, browserOK bool) {
	globalStatusProbe.mu.Lock()
	defer globalStatusProbe.mu.Unlock()
	if time.Since(globalStatusProbe.at) < statusProbeTTL && !globalStatusProbe.at.IsZero() {
		return globalStatusProbe.desk, globalStatusProbe.deskNote, globalStatusProbe.browserOK
	}
	desk, deskNote = desktop.Available()
	browserOK = a.Browser.Available()
	globalStatusProbe.desk = desk
	globalStatusProbe.deskNote = deskNote
	globalStatusProbe.browserOK = browserOK
	globalStatusProbe.at = time.Now()
	return desk, deskNote, browserOK
}

// StatusJSON for mini UI / CLI / tray.
func (a *App) StatusJSON() map[string]interface{} {
	desk, deskNote, browserOK := cachedStatusProbes(a)
	mini := a.miniUIBase()
	pending := a.PendingConfirms()
	return map[string]interface{}{
		"state":            a.State(),
		"computer_id":      a.Cfg.ComputerID,
		"device_id":        a.Cfg.DeviceID,
		"harness_url":      a.Cfg.HarnessURL,
		"peer_version":     PeerVersion,
		"caps":             protocol.Caps{Browser: browserOK, Desktop: desk, Confirm: true},
		"desktop_note":     deskNote,
		"desktop_ok":       desk,
		"browser_ok":       browserOK,
		"browser_ready":    browserOK,
		"browser_mode":     a.Browser.Mode(),
		"browser_profile":  a.Browser.ProfileDir(),
		"browser_source":   a.Browser.SourceDir(),
		"browser_owned":    a.Browser.Owned(),
		"busy":             a.Q.Busy(),
		"miniui_addr":      mini,
		"pid":              os.Getpid(),
		"pending_confirms": pending,
		"confirm_count":    len(pending),
	}
}

// StartMiniUI serves localhost control UI.
func (a *App) StartMiniUI(ctx context.Context) (addr string, err error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		st := a.StatusJSON()
		b, _ := json.MarshalIndent(st, "", "  ")
		fmt.Fprintf(w, `<!doctype html><meta charset=utf-8><title>marble-peer</title>
<body style="font-family:system-ui;background:#111;color:#eee;padding:1.5rem">
<h1>marble-peer</h1>
<pre>%s</pre>
<p><a href="/pair" style="color:#7cf">Pair</a></p>
</body>`, string(b))
	})
	mux.HandleFunc("/pair", a.handlePairUI)
	mux.HandleFunc("/confirm/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/confirm/")
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			accept := r.FormValue("accept") == "1" || r.FormValue("accept") == "true"
			a.ResolveConfirm(id, accept)
			fmt.Fprintf(w, "ok accept=%v", accept)
			return
		}
		fmt.Fprintf(w, `<!doctype html><body style="font-family:system-ui;background:#111;color:#eee;padding:2rem">
<h2>Confirm action</h2>
<form method=post><button name=accept value=1>Accept</button>
<button name=accept value=0>Deny</button></form></body>`)
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		a.Q.Cancel()
		fmt.Fprint(w, "stopped")
	})

	ports := []int{18765, 18766, 18767, 18768, 18769, 18770, 18771, 18772, 18773, 18774, 18775}
	var lnAddr string
	var srv *http.Server
	for _, p := range ports {
		addr = fmt.Sprintf("127.0.0.1:%d", p)
		srv = &http.Server{Addr: addr, Handler: mux}
		go func() {
			<-ctx.Done()
			_ = srv.Close()
		}()
		// try listen
		errCh := make(chan error, 1)
		go func() { errCh <- srv.ListenAndServe() }()
		select {
		case err := <-errCh:
			if err != nil && err != http.ErrServerClosed {
				continue // try next port
			}
		case <-time.After(100 * time.Millisecond):
			// assume listening
			a.Cfg.MiniUIPort = p
			_ = config.WriteState(map[string]interface{}{"miniui_addr": addr})
			log.Printf("miniui http://%s", addr)
			lnAddr = addr
			// wait until closed
			go func() {
				<-ctx.Done()
			}()
			return lnAddr, nil
		}
	}
	return "", fmt.Errorf("no free miniui port")
}

func (a *App) handlePairUI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		harness := r.FormValue("harness_url")
		hCode := r.FormValue("h_code")
		if err := Pair(harness, hCode, r.FormValue("allow_http") == "1"); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		fmt.Fprint(w, "paired — restart marble-peer run")
		return
	}
	fmt.Fprint(w, `<!doctype html><body style="font-family:system-ui;background:#111;color:#eee;padding:2rem">
<h2>Pair with Marble</h2>
<form method=post>
<label>Harness URL <input name=harness_url style="width:100%" placeholder="http://127.0.0.1:8080"></label><br><br>
<label>H-code <input name=h_code></label><br><br>
<label><input type=checkbox name=allow_http value=1> Allow HTTP on private net</label><br><br>
<button type=submit>Pair</button>
</form></body>`)
}

// Pair performs mutual handshake join + poll until sealed.
func Pair(harnessURL, hCode string, allowHTTP bool) error {
	harnessURL = strings.TrimRight(strings.TrimSpace(harnessURL), "/")
	hCode = strings.ToUpper(strings.TrimSpace(hCode))
	if harnessURL == "" || hCode == "" {
		return fmt.Errorf("harness_url and h_code required")
	}
	if strings.HasPrefix(harnessURL, "http://") && !allowHTTP {
		// allow localhost always
		if !strings.Contains(harnessURL, "127.0.0.1") && !strings.Contains(harnessURL, "localhost") &&
			!strings.Contains(harnessURL, "100.") {
			return fmt.Errorf("HTTP harness requires --allow-http or private mesh confirm")
		}
	}
	if err := config.EnsureHome(); err != nil {
		return err
	}
	cfg, _ := config.Load()
	if err := EnsureDeviceID(&cfg); err != nil {
		return err
	}
	desk, _ := desktop.Available()
	body := map[string]interface{}{
		"h_code":    hCode,
		"device_id": cfg.DeviceID,
		"os":        runtime.GOOS,
		"caps": map[string]bool{
			"browser": findChrome() != "",
			"desktop": desk,
			"confirm": true,
		},
	}
	b, _ := json.Marshal(body)
	resp, err := http.Post(harnessURL+"/api/computers/pair/join", "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var join struct {
		PairingID string `json:"pairing_id"`
		PCode     string `json:"p_code"`
		Status    string `json:"status"`
		Error     string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&join)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("join %s: %s", resp.Status, join.Error)
	}
	fmt.Printf("\n*** Enter P-code in Marble Settings: %s\n\nWaiting for confirm…\n", join.PCode)
	// poll
	for i := 0; i < 120; i++ {
		time.Sleep(2 * time.Second)
		u := fmt.Sprintf("%s/api/computers/pair/status?pairing_id=%s&device_id=%s",
			harnessURL, url.QueryEscape(join.PairingID), url.QueryEscape(cfg.DeviceID))
		r, err := http.Get(u)
		if err != nil {
			continue
		}
		var st struct {
			Status      string `json:"status"`
			DeviceToken string `json:"device_token"`
			ComputerID  string `json:"computer_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&st)
		r.Body.Close()
		if st.Status == "sealed" && st.DeviceToken != "" {
			cfg.HarnessURL = harnessURL
			cfg.ComputerID = st.ComputerID
			if err := config.Save(cfg); err != nil {
				return err
			}
			if err := config.SaveToken(st.DeviceToken); err != nil {
				return err
			}
			fmt.Printf("Paired as computer_id=%s\n", st.ComputerID)
			return nil
		}
		if st.Status == "sealed" {
			// token already consumed? fail
			return fmt.Errorf("sealed but no device_token — re-pair")
		}
	}
	return fmt.Errorf("timeout waiting for operator confirm")
}

func findChrome() string {
	for _, c := range []string{"google-chrome-stable", "google-chrome", "chromium-browser", "chromium"} {
		if p, err := execLook(c); err == nil {
			return p
		}
	}
	return ""
}

func execLook(name string) (string, error) {
	return exec.LookPath(name)
}

// HandlePairHTTP is used by cmd mini UI.
func (a *App) HandlePairHTTP(w http.ResponseWriter, r *http.Request) {
	a.handlePairUI(w, r)
}

// HandleConfirmHTTP handles /confirm/{id}.
func (a *App) HandleConfirmHTTP(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/confirm/")
	id = strings.Trim(id, "/")
	if id == "" {
		http.Error(w, "missing confirm id", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		accept := r.FormValue("accept") == "1" || r.FormValue("accept") == "true" || r.FormValue("accept") == "Accept"
		ok := a.ResolveConfirm(id, accept)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if !ok {
			fmt.Fprintf(w, `<!doctype html><meta charset=utf-8>
<body style="font-family:system-ui;background:#0f1115;color:#e8ecf4;padding:2rem">
<h2>Too late or unknown request</h2>
<p>This confirmation expired, was already answered, or the peer restarted.</p>
<p><a style="color:#7c9cff" href="/">Back to status</a></p></body>`)
			return
		}
		label := "Denied"
		color := "#f87171"
		if accept {
			label = "Accepted"
			color = "#4ade80"
		}
		fmt.Fprintf(w, `<!doctype html><meta charset=utf-8>
<body style="font-family:system-ui;background:#0f1115;color:#e8ecf4;padding:2rem">
<h2 style="color:%s">%s</h2>
<p>You can close this tab. The agent will continue with this answer.</p>
<p><a style="color:#7c9cff" href="/">Back to status</a></p></body>`, color, label)
		return
	}

	// Lookup prompt for display
	prompt, risk := "(no details — request may have expired)", ""
	a.confirmMu.Lock()
	if m, ok := a.confirmMeta[id]; ok {
		prompt = m.Prompt
		risk = m.Risk
	}
	a.confirmMu.Unlock()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset=utf-8>
<title>Confirm — marble-peer</title>
<body style="font-family:system-ui;background:#0f1115;color:#e8ecf4;padding:2rem;max-width:40rem">
<h1 style="margin-top:0">⚠️ Confirmation required</h1>
<p style="color:#fbbf24">Risk: <strong>%s</strong></p>
<div style="background:#171a21;padding:1rem 1.25rem;border-radius:10px;border:1px solid #2a3140;margin:1rem 0;white-space:pre-wrap">%s</div>
<p style="color:#9aa3b5;font-size:0.9rem">This is the human gate for high-risk agent actions on this machine. Default is <strong>deny</strong> after 120 seconds if you do nothing.</p>
<form method=post style="display:flex;gap:0.75rem;margin-top:1.5rem">
  <button name=accept value=1 style="flex:1;padding:0.9rem 1rem;font-size:1.1rem;background:#16a34a;color:#fff;border:0;border-radius:8px;cursor:pointer">Accept</button>
  <button name=accept value=0 style="flex:1;padding:0.9rem 1rem;font-size:1.1rem;background:#dc2626;color:#fff;border:0;border-radius:8px;cursor:pointer">Deny</button>
</form>
<p style="margin-top:2rem;font-size:0.85rem;color:#6b7280">id=%s</p>
</body>`, htmlEscape(risk), htmlEscape(prompt), htmlEscape(id))
}

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}
