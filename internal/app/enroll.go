package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rendicott/marble-desktop-peer/internal/config"
)

// Grant enrollment (harness ADR-0036): the harness mints a single-use grant for a machine
// it expects; the machine redeems it with one POST /api/computers/enroll. No H-code, no
// polling, no operator confirm — built for cloud-init / user-data provisioning.

// GrantFilePath is where provisioning can drop a grant for `run` to pick up.
func GrantFilePath() string { return filepath.Join(config.Home(), "grant") }

// Grant is a secret plus the harness it is for, and where it came from.
type Grant struct {
	Harness string
	Secret  string
	Source  string // flag | env | file
}

// ResolveGrant finds a grant: --grant, then MARBLE_GRANT, then ~/.marble-peer/grant. The
// file holds either the bare secret or {"harness": "...", "grant": "..."}. The harness
// comes from --harness, then the file, then MARBLE_HARNESS. ok=false means no grant.
func ResolveGrant(flagHarness, flagGrant string) (g Grant, ok bool, err error) {
	var fileHarness string
	switch {
	case strings.TrimSpace(flagGrant) != "":
		g.Secret, g.Source = strings.TrimSpace(flagGrant), "flag"
	case strings.TrimSpace(os.Getenv("MARBLE_GRANT")) != "":
		g.Secret, g.Source = strings.TrimSpace(os.Getenv("MARBLE_GRANT")), "env"
	default:
		b, rerr := os.ReadFile(GrantFilePath())
		if rerr != nil {
			if os.IsNotExist(rerr) {
				return g, false, nil
			}
			return g, false, rerr
		}
		txt := strings.TrimSpace(string(b))
		if strings.HasPrefix(txt, "{") {
			var f struct {
				Harness string `json:"harness"`
				Grant   string `json:"grant"`
			}
			if jerr := json.Unmarshal([]byte(txt), &f); jerr != nil {
				return g, false, fmt.Errorf("%s: %w", GrantFilePath(), jerr)
			}
			txt, fileHarness = strings.TrimSpace(f.Grant), f.Harness
		}
		if txt == "" {
			return g, false, nil
		}
		g.Secret, g.Source = txt, "file"
	}
	g.Harness = firstNonEmpty(flagHarness, fileHarness, os.Getenv("MARBLE_HARNESS"))
	g.Harness = config.NormalizeURL(g.Harness)
	if g.Harness == "" {
		return g, true, fmt.Errorf("grant found (%s) but no harness URL: pass --harness or set MARBLE_HARNESS", g.Source)
	}
	return g, true, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// EnrollOpts tunes Enroll.
type EnrollOpts struct {
	DeviceName string // default: hostname
	AllowHTTP  bool
	Force      bool          // enroll even if this harness already has a token
	Wait       time.Duration // keep retrying unreachable / 5xx harness this long (boot ordering)
}

// EnrollResult reports the outcome.
type EnrollResult struct {
	ComputerID string
	Harness    string
	Skipped    bool // already enrolled; grant left unspent
}

// ErrEnrollRejected wraps a definitive harness refusal (bad, used, expired grant).
var ErrEnrollRejected = errors.New("enroll rejected")

var enrollRetryEvery = 5 * time.Second

// Enroll redeems a grant. Idempotent: if this peer already holds a token for the harness
// it does nothing (a reboot mid-provisioning must not burn a second grant). On success a
// grant file is removed, since the grant is spent.
func Enroll(g Grant, o EnrollOpts) (EnrollResult, error) {
	res := EnrollResult{Harness: g.Harness}
	if err := checkHarnessURL(g.Harness, o.AllowHTTP || os.Getenv("MARBLE_ALLOW_HTTP") == "1"); err != nil {
		return res, err
	}
	if err := config.EnsureHome(); err != nil {
		return res, err
	}
	cfg, _ := config.Load()
	toks, _ := config.LoadTokens()
	if h, ok := cfg.Harness(g.Harness); ok && toks[g.Harness] != "" && !o.Force {
		res.ComputerID, res.Skipped = h.ComputerID, true
		return res, nil
	}
	if err := EnsureDeviceID(&cfg); err != nil {
		return res, err
	}
	name := strings.TrimSpace(o.DeviceName)
	if name == "" {
		name, _ = os.Hostname()
	}
	body, _ := json.Marshal(map[string]interface{}{
		"grant_secret": g.Secret,
		"device_name":  name,
		"device_id":    cfg.DeviceID,
		"os":           runtime.GOOS,
		"peer_version": PeerVersion,
		"caps":         pairCaps(),
	})

	var out enrollResponse
	deadline := time.Now().Add(o.Wait)
	client := &http.Client{Timeout: 30 * time.Second}
	for {
		out = enrollResponse{}
		status, err := postEnroll(client, g.Harness, body, &out)
		if err == nil && status == http.StatusOK && out.DeviceToken != "" {
			break
		}
		// Retry only what a later attempt can fix: network errors, 5xx, 429.
		transient := err != nil || status >= 500 || status == http.StatusTooManyRequests
		if !transient {
			msg := out.Error
			if msg == "" && status == http.StatusNotFound {
				msg = "harness has no open grants (or predates enrollment)"
			}
			if msg == "" {
				msg = http.StatusText(status)
			}
			return res, fmt.Errorf("%w: HTTP %d: %s", ErrEnrollRejected, status, msg)
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("HTTP %d: %s", status, out.Error)
			}
			return res, fmt.Errorf("enroll %s: %w", g.Harness, err)
		}
		time.Sleep(enrollRetryEvery)
	}

	if err := config.SaveToken(g.Harness, out.DeviceToken); err != nil {
		return res, err
	}
	// Reload: the token save must not race a stale cfg copy.
	cfg, _ = config.Load()
	if out.DeviceID != "" && out.DeviceID != cfg.DeviceID {
		cfg.DeviceID = out.DeviceID
	}
	cfg.UpsertHarness(config.Harness{URL: g.Harness, ComputerID: out.ComputerID})
	if err := config.Save(cfg); err != nil {
		return res, err
	}
	if g.Source == "file" {
		_ = os.Remove(GrantFilePath())
	}
	res.ComputerID = out.ComputerID
	return res, nil
}

type enrollResponse struct {
	ComputerID  string `json:"computer_id"`
	DeviceID    string `json:"device_id"`
	DeviceToken string `json:"device_token"`
	Error       string `json:"error"`
}

func postEnroll(c *http.Client, harness string, body []byte, out interface{}) (int, error) {
	resp, err := c.Post(harness+"/api/computers/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_ = json.NewDecoder(resp.Body).Decode(out)
	return resp.StatusCode, nil
}
