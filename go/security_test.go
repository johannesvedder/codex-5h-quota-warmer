package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSecureStatePath(t *testing.T) {
	for _, path := range []string{defaultStateFile(), "plugins/state/test.json"} {
		if _, err := secureStatePath(path); err != nil {
			t.Fatalf("valid state path rejected: %v", err)
		}
	}
	for _, path := range []string{"plugins/state/../outside.json", "plugins/state/nested/test.json", "plugins/state/test.txt", "plugins/state/.hidden.json", filepath.Join(t.TempDir(), "outside.json")} {
		if _, err := secureStatePath(path); err == nil {
			t.Fatalf("unsafe path accepted: %s", path)
		}
	}
}

func TestStateSanitization(t *testing.T) {
	old := runState{LastSource: "schedule:private", LastResults: []warmResult{{AuthID: "alice@example.org", Label: "private", Error: "Bearer sensitive"}}, Logs: []logEntry{{Fields: map[string]any{"error": "secret value", "auth_id": "alice@example.org", "credential_count": 1}}}}
	sanitized := sanitizedState(old)
	raw, err := json.Marshal(sanitized)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"alice@example.org", "Bearer sensitive", "secret value", "private"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("sensitive value leaked: %s", secret)
		}
	}
	if sanitized.LastResults[0].CredentialNumber != 1 || sanitized.Logs[0].Fields["credential_count"] != 1 {
		t.Fatal("safe diagnostics lost")
	}
	if old.LastResults[0].AuthID == "" {
		t.Fatal("original live result was mutated")
	}
}

func TestStateIO(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join("plugins", "state", "history.json")
	original := runState{LastRun: time.Now().UTC(), LastResults: []warmResult{{AuthID: "private", Success: true}}}
	if err := writeRunState(path, original); err != nil {
		t.Fatal(err)
	}
	got, ok, err := loadRunState(path)
	if err != nil || !ok || got.LastResults[0].AuthID != "" || !got.LastResults[0].Success {
		t.Fatalf("state roundtrip failed: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.json")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := writeRunState(path, original); err == nil {
		t.Fatal("symlink accepted")
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "unchanged" {
		t.Fatal("external target modified")
	}
}

func TestConfigBoundsAndCanonicalTime(t *testing.T) {
	for _, cfg := range []string{
		"prompt: " + strings.Repeat("a", 4097),
		"state_file: ../outside.json",
		"schedules: [{id: a, at: '5:00'}, {id: b, at: '05:00'}]",
	} {
		if _, err := parsePluginConfig([]byte(cfg)); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	cfg, err := parsePluginConfig([]byte("schedules: [{id: a, at: '5:00'}]"))
	if err != nil || cfg.Schedules[0].At != "05:00" {
		t.Fatalf("time not normalized: %v", err)
	}
}

func TestManagementRoutes(t *testing.T) {
	const base = "/v0/management/plugins/" + pluginID
	for _, tc := range []struct {
		method, path string
		code int
	}{
		{http.MethodGet, base + "/run", http.StatusMethodNotAllowed},
		{http.MethodPost, base + "/status", http.StatusMethodNotAllowed},
		{http.MethodGet, "/somewhere/status", http.StatusNotFound},
		{http.MethodGet, "/v0/resource/plugins/" + pluginID + "/status", http.StatusOK},
		{http.MethodGet, base + "/status", http.StatusOK},
	} {
		req, _ := json.Marshal(managementRequest{Method: tc.method, Path: tc.path})
		raw, err := handleManagement(req)
		if err != nil {
			t.Fatal(err)
		}
		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		var response managementResponse
		if err := json.Unmarshal(env.Result, &response); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != tc.code {
			t.Fatalf("%s %s: expected %d got %d", tc.method, tc.path, tc.code, response.StatusCode)
		}
		if response.Headers.Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store")
		}
	}
}

func TestBrowserDoesNotPersistCredentials(t *testing.T) {
	for _, forbidden := range []string{"localStorage.setItem", "sessionStorage.setItem", "localStorage.getItem", "sessionStorage.getItem"} {
		if strings.Contains(statusPageHTML, forbidden) {
			t.Fatalf("browser storage access: %s", forbidden)
		}
	}
}

func TestManualRunRateLimit(t *testing.T) {
	w := newWarmer()
	w.config.Enabled = true
	w.lastManualStart = time.Now()
	if _, err := w.Run("manual"); err == nil {
		t.Fatal("rate limit was not enforced")
	} else if e, ok := err.(*hostCallError); !ok || e.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("unexpected error: %v", err)
	}
}
