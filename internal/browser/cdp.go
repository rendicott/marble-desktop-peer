package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// cdpSession is a short-lived CDP WebSocket to one Chrome page target.
type cdpSession struct {
	ws   *websocket.Conn
	next int64
	mu   sync.Mutex
	wait map[int64]chan cdpResp
}

type cdpResp struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func dialCDP(ctx context.Context, wsURL string) (*cdpSession, error) {
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	ws, _, err := d.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, err
	}
	s := &cdpSession{
		ws:   ws,
		wait: make(map[int64]chan cdpResp),
	}
	go s.readLoop()
	return s, nil
}

func (s *cdpSession) readLoop() {
	for {
		_, data, err := s.ws.ReadMessage()
		if err != nil {
			s.mu.Lock()
			for id, ch := range s.wait {
				close(ch)
				delete(s.wait, id)
			}
			s.mu.Unlock()
			return
		}
		var resp cdpResp
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}
		if resp.ID == 0 {
			continue // event
		}
		s.mu.Lock()
		ch := s.wait[resp.ID]
		if ch != nil {
			delete(s.wait, resp.ID)
			select {
			case ch <- resp:
			default:
			}
		}
		s.mu.Unlock()
	}
}

func (s *cdpSession) call(ctx context.Context, method string, params interface{}) (json.RawMessage, error) {
	id := atomic.AddInt64(&s.next, 1)
	msg := map[string]interface{}{
		"id":     id,
		"method": method,
	}
	if params != nil {
		msg["params"] = params
	}
	ch := make(chan cdpResp, 1)
	s.mu.Lock()
	s.wait[id] = ch
	err := s.ws.WriteJSON(msg)
	s.mu.Unlock()
	if err != nil {
		s.mu.Lock()
		delete(s.wait, id)
		s.mu.Unlock()
		return nil, err
	}
	// Cap hangs: UPS/heavy SPAs often freeze Runtime.evaluate; don't burn 30s per call.
	timeout := 12 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if d := time.Until(deadline); d > 0 && d < timeout {
			timeout = d
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.wait, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("cdp connection closed")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("cdp %s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	case <-timer.C:
		s.mu.Lock()
		delete(s.wait, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("cdp timeout: %s (page may be stuck on a modal — try click_text Sign Digitally or open URL again)", method)
	}
}

func (s *cdpSession) close() {
	_ = s.ws.Close()
}

// activePageWS returns webSocketDebuggerUrl for the best page target.
// Preference: last navigated URL → ups/track → gmail → other https (not newtab) → chrome:// → blank.
func (m *Manager) activePageWS() (string, error) {
	tabs, err := m.listTabs()
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	last := m.lastURL
	m.mu.Unlock()

	var (
		lastHit string
		ups     string
		gmail   string
		https   string
		other   string
		blank   string
	)
	for i := len(tabs) - 1; i >= 0; i-- {
		t := tabs[i]
		if t.WSUrl == "" {
			continue
		}
		u := t.URL
		// Skip NTP chrome UI
		if strings.Contains(u, "chrome://newtab") || strings.Contains(u, "chrome://new-tab") {
			if blank == "" {
				blank = t.WSUrl
			}
			continue
		}
		switch {
		case u == "about:blank" || u == "":
			if blank == "" {
				blank = t.WSUrl
			}
		case last != "" && (u == last || strings.HasPrefix(u, last) || strings.Contains(u, last) || strings.Contains(last, u)):
			if lastHit == "" {
				lastHit = t.WSUrl
			}
		case strings.Contains(u, "ups.com") || strings.Contains(u, "/track"):
			if ups == "" {
				ups = t.WSUrl
			}
		case strings.Contains(u, "mail.google.com"):
			if gmail == "" {
				gmail = t.WSUrl
			}
		case strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://"):
			if https == "" {
				https = t.WSUrl
			}
		default:
			if other == "" {
				other = t.WSUrl
			}
		}
	}
	for _, cand := range []string{lastHit, ups, gmail, https, blank, other} {
		if cand != "" {
			return cand, nil
		}
	}
	if len(tabs) > 0 && tabs[0].WSUrl != "" {
		return tabs[0].WSUrl, nil
	}
	// create a page
	m.mu.Lock()
	port := m.port
	m.mu.Unlock()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/new?about:blank", port))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var t tabInfo
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	if t.WSUrl == "" {
		return "", fmt.Errorf("no page websocket")
	}
	return t.WSUrl, nil
}

func (m *Manager) withPage(ctx context.Context, fn func(*cdpSession) error) error {
	wsURL, err := m.activePageWS()
	if err != nil {
		return err
	}
	s, err := dialCDP(ctx, wsURL)
	if err != nil {
		return err
	}
	defer s.close()
	return fn(s)
}

// setLastURL remembers the last navigated/focused page for tab targeting.
func (m *Manager) setLastURL(u string) {
	if u == "" {
		return
	}
	m.mu.Lock()
	m.lastURL = u
	m.mu.Unlock()
}
