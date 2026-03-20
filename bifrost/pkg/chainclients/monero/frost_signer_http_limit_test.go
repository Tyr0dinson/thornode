package monero

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/config"
)

func responseAtLimit(height int64) string {
	prefix := fmt.Sprintf(`{"height":%d,"pad":"`, height)
	suffix := `"}` // closes "pad" and object
	padLen := maxFrostResponseBytes - len(prefix) - len(suffix)
	if padLen < 0 {
		panic("invalid response size fixture")
	}
	return prefix + strings.Repeat("a", padLen) + suffix
}

func TestFrostSignerGetOversizedResponseReturnsSizeError(t *testing.T) {
	t.Parallel()

	payload := responseAtLimit(123) + "x"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/chain/height" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(
		config.BifrostChainConfiguration{SignerStoreSecret: testSignerStoreSecret},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	_, err = signer.GetChainHeight()
	if err == nil {
		t.Fatal("expected GetChainHeight size-limit error")
	}
	if !strings.Contains(err.Error(), "frost response exceeded 10485760 byte limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFrostSignerPostOversizedResponseReturnsSizeError(t *testing.T) {
	t.Parallel()

	prefix := `{"session_id":"sess-1","preprocess":"aa","pad":"`
	suffix := `"}`
	padLen := (maxFrostResponseBytes - len(prefix) - len(suffix)) + 1
	payload := prefix + strings.Repeat("b", padLen) + suffix

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/sign/prepare" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(
		config.BifrostChainConfiguration{SignerStoreSecret: testSignerStoreSecret},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	_, err = signer.FrostSignPrepare(xmrFrostSignPrepareRequest{
		MsgID:        "msg-1",
		VaultPubKey:  "thorpub1test",
		ToAddress:    "48address",
		AmountAtomic: 1,
		DecoyContext: "ctx",
	})
	if err == nil {
		t.Fatal("expected FrostSignPrepare size-limit error")
	}
	if !strings.Contains(err.Error(), "frost response exceeded 10485760 byte limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFrostSignerResponseAtLimitStillParses(t *testing.T) {
	t.Parallel()

	payload := responseAtLimit(456)
	if len(payload) != maxFrostResponseBytes {
		t.Fatalf("invalid fixture size: got %d want %d", len(payload), maxFrostResponseBytes)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/monero/chain/height" {
			http.Error(w, "unexpected path: "+r.URL.Path, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(payload))
	}))
	defer server.Close()

	signer, err := NewFrostSignerWithEndpoint(
		config.BifrostChainConfiguration{SignerStoreSecret: testSignerStoreSecret},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	height, err := signer.GetChainHeight()
	if err != nil {
		t.Fatalf("GetChainHeight failed: %v", err)
	}
	if height != 456 {
		t.Fatalf("expected height 456, got %d", height)
	}
}
