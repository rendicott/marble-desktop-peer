package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rendicott/marble-desktop-peer/internal/config"
)

func enrollHome(t *testing.T) {
	t.Helper()
	t.Setenv("MARBLE_PEER_HOME", t.TempDir())
	t.Setenv("MARBLE_GRANT", "")
	t.Setenv("MARBLE_HARNESS", "")
	enrollRetryEvery = 10 * time.Millisecond
}

func TestResolveGrantPrecedence(t *testing.T) {
	enrollHome(t)
	if _, ok, err := ResolveGrant("", ""); ok || err != nil {
		t.Fatalf("no grant anywhere: ok=%v err=%v", ok, err)
	}
	// File as JSON carries its own harness.
	if err := os.WriteFile(GrantFilePath(), []byte(`{"harness":"http://h:8080/","grant":"mgrant_file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	g, ok, err := ResolveGrant("", "")
	if !ok || err != nil || g.Secret != "mgrant_file" || g.Harness != "http://h:8080" || g.Source != "file" {
		t.Fatalf("file grant %+v %v %v", g, ok, err)
	}
	// Env beats file; MARBLE_HARNESS fills in the harness.
	t.Setenv("MARBLE_GRANT", "mgrant_env")
	t.Setenv("MARBLE_HARNESS", "http://env:8080")
	if g, _, _ := ResolveGrant("", ""); g.Secret != "mgrant_env" || g.Harness != "http://env:8080" || g.Source != "env" {
		t.Fatalf("env grant %+v", g)
	}
	// Flags beat both.
	if g, _, _ := ResolveGrant("http://flag:1", "mgrant_flag"); g.Secret != "mgrant_flag" || g.Harness != "http://flag:1" {
		t.Fatalf("flag grant %+v", g)
	}
	// A bare secret file with no harness anywhere is an error, not silence.
	t.Setenv("MARBLE_GRANT", "")
	t.Setenv("MARBLE_HARNESS", "")
	_ = os.WriteFile(GrantFilePath(), []byte("mgrant_bare\n"), 0o600)
	if _, ok, err := ResolveGrant("", ""); !ok || err == nil {
		t.Fatalf("bare file without harness: ok=%v err=%v", ok, err)
	}
}

func TestEnrollStoresTokenAndIsIdempotent(t *testing.T) {
	enrollHome(t)
	var calls, fails int32
	atomic.StoreInt32(&fails, 2) // harness still booting for the first two attempts
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/api/computers/enroll" {
			http.NotFound(w, r)
			return
		}
		if atomic.AddInt32(&fails, -1) >= 0 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["grant_secret"] != "mgrant_ok" || body["device_id"] == "" || body["device_name"] != "orb-win-test" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"computer_id": "orb-win-test", "device_id": body["device_id"].(string), "device_token": "tok123",
		})
	}))
	defer srv.Close()

	_ = os.WriteFile(GrantFilePath(), []byte(`{"harness":"`+srv.URL+`","grant":"mgrant_ok"}`), 0o600)
	g, _, err := ResolveGrant("", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := Enroll(g, EnrollOpts{DeviceName: "orb-win-test", AllowHTTP: true, Wait: 5 * time.Second})
	if err != nil || res.ComputerID != "orb-win-test" || res.Skipped {
		t.Fatalf("enroll: %+v %v", res, err)
	}
	toks, _ := config.LoadTokens()
	cfg, _ := config.Load()
	if toks[srv.URL] != "tok123" {
		t.Fatalf("token not stored: %v", toks)
	}
	if h, ok := cfg.Harness(srv.URL); !ok || h.ComputerID != "orb-win-test" || cfg.DeviceID == "" {
		t.Fatalf("config %+v", cfg)
	}
	if _, err := os.Stat(GrantFilePath()); !os.IsNotExist(err) {
		t.Fatal("spent grant file should be removed")
	}

	// Reboot mid-provisioning re-runs the bootstrap: no second claim.
	before := atomic.LoadInt32(&calls)
	res, err = Enroll(Grant{Harness: srv.URL, Secret: "mgrant_other", Source: "env"}, EnrollOpts{AllowHTTP: true})
	if err != nil || !res.Skipped || atomic.LoadInt32(&calls) != before {
		t.Fatalf("second enroll should skip without calling the harness: %+v %v", res, err)
	}
}

func TestEnrollRejectionIsNotRetried(t *testing.T) {
	enrollHome(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusGone)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "grant expired"})
	}))
	defer srv.Close()
	_, err := Enroll(Grant{Harness: srv.URL, Secret: "mgrant_x", Source: "flag"}, EnrollOpts{AllowHTTP: true, Wait: time.Minute})
	if !errors.Is(err, ErrEnrollRejected) || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("410 must fail fast: %v (calls=%d)", err, calls)
	}
	if toks, _ := config.LoadTokens(); len(toks) != 0 {
		t.Fatalf("no token on rejection: %v", toks)
	}
}

func TestEnrollRefusesPublicHTTP(t *testing.T) {
	enrollHome(t)
	t.Setenv("MARBLE_ALLOW_HTTP", "")
	if _, err := Enroll(Grant{Harness: "http://example.com:8080", Secret: "mgrant_x"}, EnrollOpts{}); err == nil {
		t.Fatal("cleartext public harness must need --allow-http")
	}
}
