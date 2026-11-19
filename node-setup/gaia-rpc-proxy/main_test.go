package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandaloneConfig(t *testing.T) {
	for _, bad := range []string{"", "http://archive.example", "https://user:secret@archive.example", "https://archive.example/?secret=1", "https://archive.example/#f"} {
		if _, err := endpoint(bad); err == nil {
			t.Fatalf("unsafe URL accepted: %q", bad)
		}
	}
	filename := filepath.Join(t.TempDir(), "config.json")
	cfg := map[string]any{"primary_rpc_file": "/run/primary", "archive_rpc": "https://archive.example:443", "timeout_seconds": 15}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(filename, b, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := readConfig(filename)
	if err != nil || c.TimeoutSeconds != 15 || c.Listen != "127.0.0.1:8080" {
		t.Fatal(c, err)
	}
	cfg["broadcast_enabled"] = true
	b, _ = json.Marshal(cfg)
	_ = os.WriteFile(filename, b, 0600)
	if _, err = readConfig(filename); err == nil {
		t.Fatal("unknown configuration accepted")
	}
}

func TestStandaloneProbesAndMetrics(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	g := gaiaTestProxy(t, p, a)
	a.server.Close()
	for _, route := range []string{"/healthz", "/readyz", "/metrics"} {
		w := httptest.NewRecorder()
		handler(g).ServeHTTP(w, httptest.NewRequest("GET", route, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", route, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "tron_proxy_") {
			t.Fatal("TRON state exposed by separate service")
		}
	}
	p.update(func() { p.network = "wrong-chain" })
	w := httptest.NewRecorder()
	handler(g).ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 {
		t.Fatal("wrong-chain primary ready", w.Code)
	}
}
