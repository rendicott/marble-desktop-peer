package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rendicott/marble-desktop-peer/internal/config"
	"github.com/rendicott/marble-desktop-peer/internal/protocol"
)

// link is the connection to one paired harness. Each link reconnects on its
// own; all links share the App's action queue, browser and peer lock.
type link struct {
	a   *App
	url string

	mu         sync.Mutex
	token      string
	computerID string
	state      string
	ws         *websocket.Conn
	instance   string // hello_ack instance_id
	name       string // hello_ack harness_name
	proto      int    // hello_ack protocol_version
	cancel     context.CancelFunc

	// writeMu serializes all websocket writes. gorilla/websocket panics on concurrent writers
	// ("concurrent write to websocket connection") — ping/pong + action results race without this.
	writeMu sync.Mutex
}

// HarnessStatus is one link as shown in status.json.
type HarnessStatus struct {
	URL        string `json:"url"`
	Name       string `json:"name,omitempty"`
	ComputerID string `json:"computer_id"`
	State      string `json:"state"`
	Protocol   int    `json:"protocol_version,omitempty"`
}

func newLink(a *App, h config.Harness, token string) *link {
	return &link{a: a, url: h.URL, computerID: h.ComputerID, token: token, state: "Offline"}
}

func (l *link) status() HarnessStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return HarnessStatus{URL: l.url, Name: l.name, ComputerID: l.computerID, State: l.state, Protocol: l.proto}
}

func (l *link) setState(s string) {
	l.mu.Lock()
	l.state = s
	l.mu.Unlock()
	log.Printf("harness %s state=%s", l.url, s)
}

func (l *link) instanceID() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.instance
}

func (l *link) harnessName() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.name
}

// legacy is true for a connected harness that predates the peer lock.
func (l *link) legacy() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.proto < 2
}

// write serializes all writes on this link's active connection.
func (l *link) write(v interface{}) error {
	l.mu.Lock()
	w := l.ws
	l.mu.Unlock()
	if w == nil {
		return fmt.Errorf("websocket not connected")
	}
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	_ = w.SetWriteDeadline(time.Now().Add(30 * time.Second))
	err := w.WriteJSON(v)
	_ = w.SetWriteDeadline(time.Time{})
	return err
}

func (l *link) sendLockState(info LockInfo) {
	if l.legacy() {
		return
	}
	meta := map[string]interface{}{
		"held":        info.Held,
		"mine":        info.Held && info.Holder == l.url,
		"holder":      info.Holder,
		"holder_name": info.HolderName,
	}
	if info.Since != nil {
		meta["since"] = info.Since.UTC().Format(time.RFC3339)
	}
	_ = l.write(protocol.Envelope{Type: "lock_state", Meta: meta})
}

// run keeps the link connected until ctx is done or the link is stopped.
func (l *link) run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	l.mu.Lock()
	l.cancel = cancel
	l.mu.Unlock()
	defer cancel()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := l.session(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("harness %s ws session: %v — retry in 3s", l.url, err)
			l.setState("Offline")
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		// clean session end (should be rare) — reconnect
		log.Printf("harness %s ws session ended cleanly; reconnecting", l.url)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// stop disconnects the link for good (unpair / re-pair).
func (l *link) stop() {
	l.mu.Lock()
	cancel, ws := l.cancel, l.ws
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if ws != nil {
		_ = ws.Close()
	}
}

func (l *link) session(ctx context.Context) error {
	l.mu.Lock()
	token := l.token
	l.mu.Unlock()
	u, err := wsURL(l.url, l.a.Cfg.DeviceID, token)
	if err != nil {
		return err
	}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	ws, _, err := dialer.DialContext(ctx, u, nil)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.ws = ws
	l.mu.Unlock()
	// Unblock ReadMessage when the link is stopped.
	sessDone := make(chan struct{})
	defer close(sessDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = ws.Close()
		case <-sessDone:
		}
	}()
	defer func() {
		l.mu.Lock()
		l.ws = nil
		l.mu.Unlock()
		// Close under write lock so no writer races with Close.
		l.writeMu.Lock()
		_ = ws.Close()
		l.writeMu.Unlock()
		l.a.onHarnessDisconnect(l)
	}()

	caps := l.a.caps()
	hello := protocol.Envelope{
		Type:            "hello",
		ProtocolVersion: protocol.Version,
		DeviceID:        l.a.Cfg.DeviceID,
		Token:           token,
		OS:              runtime.GOOS,
		PeerVersion:     PeerVersion,
		Caps:            &caps,
	}
	if err := l.write(hello); err != nil {
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
	l.mu.Lock()
	l.instance = ack.InstanceID
	l.name = ack.HarnessName
	l.proto = ack.ProtocolVersion
	changedID := ack.ComputerID != "" && ack.ComputerID != l.computerID
	if changedID {
		l.computerID = ack.ComputerID
	}
	cid := l.computerID
	l.mu.Unlock()
	if changedID {
		l.a.saveComputerID(l.url, cid)
	}
	l.setState("Online")
	log.Printf("harness %s connected as computer_id=%s protocol=%d", l.url, cid, ack.ProtocolVersion)
	l.a.onHarnessHello(l)

	// Client-side keepalive as backup if hub pings are missing.
	// Uses write so it never races pong/result writers.
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
				if err := l.write(protocol.Envelope{Type: "ping"}); err != nil {
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
			if err := l.write(protocol.Envelope{Type: "pong"}); err != nil {
				return err
			}
		case "pong":
			// hub keepalive reply
		case "cancel":
			// Only the lock holder may stop the running action.
			if l.a.lockHeldByOrFree(l) {
				l.a.Q.Cancel()
			}
		case "lock":
			l.handleLock(env)
		case "confirm_resolve":
			// Human accepted/denied from Marble harness UI (not only peer mini-UI).
			_ = l.a.ResolveConfirm(env.ID, env.OK)
			log.Printf("CONFIRM resolve from harness %s id=%s accept=%v", l.url, env.ID, env.OK)
		case "action":
			go l.handleAction(env)
		}
	}
}

func (l *link) handleLock(env protocol.Envelope) {
	res := protocol.Envelope{Type: "result", ID: env.ID, OK: true}
	switch env.Kind {
	case "acquire":
		if err := l.a.acquireLock(l, false); err != nil {
			res.OK = false
			res.Error = err.Error()
		}
	case "release":
		l.a.releaseLock(l, "harness released")
	default:
		res.OK = false
		res.Error = "unknown lock kind " + env.Kind
	}
	info := l.a.LockInfo()
	res.Meta = map[string]interface{}{
		"held":        info.Held,
		"mine":        info.Held && info.Holder == l.url,
		"holder":      info.Holder,
		"holder_name": info.HolderName,
	}
	_ = l.write(res)
}

func (l *link) handleAction(env protocol.Envelope) {
	if err := l.a.authorizeAction(l); err != nil {
		_ = l.write(protocol.Envelope{
			Type: "result", ID: env.ID, OK: false, Error: err.Error(),
			Meta: map[string]interface{}{"lock_required": true},
		})
		return
	}
	deadline := time.Duration(env.DeadlineMS) * time.Millisecond
	if deadline <= 0 {
		deadline = 120 * time.Second
	}
	if deadline > 5*time.Minute {
		deadline = 5 * time.Minute
	}
	parent, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	summary := actionSummary(env)
	log.Printf("action start id=%s %s", env.ID, summary)
	started := time.Now()
	err := l.a.Q.Run(parent, func(ctx context.Context) error {
		res := l.a.exec(ctx, env)
		res.Type = "result"
		res.ID = env.ID
		if werr := l.write(res); werr != nil {
			return werr
		}
		return nil
	})
	log.Printf("action end id=%s %s dur=%s err=%v", env.ID, env.Kind, time.Since(started).Round(time.Millisecond), err)
	if err != nil {
		// Queue rejected (busy/cancel) or write failed — still try to surface error once.
		_ = l.write(protocol.Envelope{
			Type: "result", ID: env.ID, OK: false, Error: err.Error(),
		})
	}
}

// actionSummary is the start-log text. computer_exec includes a short prefix
// of the command so a wedge can be tied to security/osascript/curl; the rest
// of the command and all output stay out of the log.
func actionSummary(env protocol.Envelope) string {
	if env.Kind != "computer_exec" {
		if env.Kind == "" {
			return "action"
		}
		return env.Kind
	}
	var payload map[string]interface{}
	_ = json.Unmarshal(env.Payload, &payload)
	cmd, _ := payload["command"].(string)
	cmd = strings.Join(strings.Fields(cmd), " ")
	const max = 80
	if len(cmd) > max {
		cmd = cmd[:max] + "…"
	}
	if cmd == "" {
		return "computer_exec"
	}
	return "computer_exec " + cmd
}
