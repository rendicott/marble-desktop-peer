package config

import (
	"os"
	"testing"
)

func TestLoadMigratesSingleHarness(t *testing.T) {
	t.Setenv("MARBLE_PEER_HOME", t.TempDir())
	if err := EnsureHome(); err != nil {
		t.Fatal(err)
	}
	legacy := `{"harness_url":"http://h1:8080/","device_id":"dev","computer_id":"laptop"}`
	if err := os.WriteFile(Path(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyCredsPath(), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	h, ok := f.Harness("http://h1:8080")
	if !ok || h.ComputerID != "laptop" || len(f.Harnesses) != 1 {
		t.Fatalf("harnesses after migrate: %+v", f.Harnesses)
	}
	if f.HarnessURL != "" || f.ComputerID != "" || f.DeviceID != "dev" {
		t.Fatalf("legacy fields not cleared / device lost: %+v", f)
	}
	toks, err := LoadTokens()
	if err != nil || toks["http://h1:8080"] != "secret" {
		t.Fatalf("tokens after migrate: %v %v", toks, err)
	}
	if _, err := os.Stat(legacyCredsPath()); !os.IsNotExist(err) {
		t.Fatal("legacy credentials file left behind")
	}

	// Second harness is added alongside; re-pairing the first replaces it.
	f.UpsertHarness(Harness{URL: "http://h2/", ComputerID: "laptop-2"})
	f.UpsertHarness(Harness{URL: "http://h1:8080", ComputerID: "laptop-new"})
	if err := Save(f); err != nil {
		t.Fatal(err)
	}
	if err := SaveToken("http://h2", "s2"); err != nil {
		t.Fatal(err)
	}
	f, _ = Load()
	if len(f.Harnesses) != 2 {
		t.Fatalf("want 2 harnesses, got %+v", f.Harnesses)
	}
	if h, _ := f.Harness("http://h1:8080"); h.ComputerID != "laptop-new" {
		t.Fatalf("re-pair did not replace: %+v", h)
	}

	if err := ClearToken("http://h1:8080"); err != nil {
		t.Fatal(err)
	}
	toks, _ = LoadTokens()
	if _, ok := toks["http://h1:8080"]; ok || toks["http://h2"] != "s2" {
		t.Fatalf("ClearToken(one): %v", toks)
	}
}
