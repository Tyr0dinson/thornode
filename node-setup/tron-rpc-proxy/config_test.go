package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfig(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "config.json")
	c := Config{Genesis: testGenesis, Database: filepath.Join(dir, "state.db"), AdminSocket: filepath.Join(dir, "admin.sock"), Listen: "127.0.0.1:0", LocalAPI: "http://localhost:8090", LocalRPC: "http://localhost:8555/jsonrpc", TimeoutSeconds: 4, PollSeconds: 10}
	b, _ := json.Marshal(c)
	if err := os.WriteFile(f, b, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readConfig(f)
	if err != nil || got.BroadcastEnabled || got.AutoFailbackEnabled || got.timeout() != 4*time.Second {
		t.Fatal("invalid defaults", err)
	}
	c.AutoFailbackEnabled = true
	b, _ = json.Marshal(c)
	if err := os.WriteFile(f, b, 0600); err != nil {
		t.Fatal(err)
	}
	got, err = readConfig(f)
	if err != nil || !got.AutoFailbackEnabled {
		t.Fatal("automatic failback opt-in not parsed", err)
	}
	for _, b := range []string{`{}`, `{"genesis":"bad"}`, `{"extra":true}`, `{"listen":"a","listen":"b"}`} {
		if err := os.WriteFile(f, []byte(b), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = readConfig(f); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	if _, err = readConfig(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("missing config accepted")
	}
}

func TestCredentialFiles(t *testing.T) {
	for _, raw := range []string{"http://example.com/token", "https://name:secret@example.com", "https://example.com/?token=secret", "https://example.com/#secret", "file:///etc/passwd", "https:///missing"} {
		if _, err := endpoint(raw, true); err == nil {
			t.Fatal("unsafe external URL accepted", raw)
		}
	}
	u, err := endpoint("http://127.0.0.1:8090", false)
	if err != nil || u.Host != "127.0.0.1:8090" {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "external-api")
	if err = os.WriteFile(f, []byte("https://example.com/private-path/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	u, err = endpointFile(f)
	if err != nil || u.Path != "/private-path/" {
		t.Fatal(err)
	}
	_, err = endpointFile(f + "missing")
	if err == nil || strings.Contains(err.Error(), f) {
		t.Fatal("credential path leaked")
	}
}

func TestConfigReceiptFloor(t *testing.T) {
	f := filepath.Join(t.TempDir(), "config.json")
	for _, tc := range []struct {
		name, extra string
		wantFloor   int64
		valid       bool
	}{
		{"omitted", "", 0, true},
		{"zero", `,"receipt_floor":0`, 0, true},
		{"separate-floor", `,"receipt_floor":95000`, 95000, true},
		{"lower-than-block-floor", `,"receipt_floor":80000`, 80000, true},
		{"negative", `,"receipt_floor":-1`, 0, false},
		{"fraction", `,"receipt_floor":1.5`, 0, false},
		{"string", `,"receipt_floor":"95000"`, 0, false},
		{"overflow", `,"receipt_floor":9223372036854775808`, 0, false},
		{"duplicate", `,"receipt_floor":95000,"receipt_floor":0`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"genesis":"` + testGenesis + `","database":"state.db","admin_socket":"admin.sock","block_floor":90000` + tc.extra + `}`
			if err := os.WriteFile(f, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			got, err := readConfig(f)
			if (err == nil) != tc.valid {
				t.Fatalf("config error=%v, valid=%t", err, tc.valid)
			}
			if tc.valid && (got.ReceiptFloor != tc.wantFloor || got.BlockFloor != 90000) {
				t.Fatal("receipt and block floors were conflated")
			}
		})
	}
}
