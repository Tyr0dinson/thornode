package mock

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func getCov(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url) // nolint:gosec,noctx
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, body
}

func TestServerRestCov(t *testing.T) {
	srv := NewServer()
	defer srv.Close()

	// path is lowercased, slashes become dashes, trailing dash trimmed
	code, body := getCov(t, srv.URL+"/Binance/api/v3/ticker/price/")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	want, err := responses.ReadFile("data/binance-api-v3-ticker-price.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("unexpected body")
	}

	// lbank ticker appends the symbol query param
	code, body = getCov(t, srv.URL+"/lbank/v2/ticker/24hr.do?symbol=btc_usdt")
	if code != http.StatusOK {
		t.Fatalf("lbank status %d", code)
	}
	want, err = responses.ReadFile("data/lbank-v2-ticker-24hr.do-btc_usdt.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("unexpected lbank body")
	}

	// unknown path -> 500 with no body
	code, body = getCov(t, srv.URL+"/does/not/exist")
	if code != http.StatusInternalServerError || len(body) != 0 {
		t.Fatalf("expected 500 empty, got %d %q", code, body)
	}
}

func dialCov(t *testing.T, srvURL, provider string) *websocket.Conn {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srvURL, "http") + "/" + provider + "/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	return conn
}

func TestServerWsTextCov(t *testing.T) {
	srv := NewServer()
	defer srv.Close()

	conn := dialCov(t, srv.URL, "okx")
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte("sub")); err != nil {
		t.Fatal(err)
	}
	mt, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if mt != websocket.TextMessage || string(msg) != "bogus" {
		t.Fatalf("expected bogus text, got %d %q", mt, msg)
	}
	// okx has three ws fixture files (sub, ticker btc, ticker eth), sorted
	expected := []string{"okx-ws-sub-msg.json", "okx-ws-ticker-msg-btc.json", "okx-ws-ticker-msg-eth.json"}
	for _, name := range expected {
		mt, msg, err = conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		want, _ := responses.ReadFile("data/" + name)
		if mt != websocket.TextMessage || !bytes.Equal(msg, want) {
			t.Fatalf("unexpected message for %s", name)
		}
	}
}

func readCompressedCov(t *testing.T, conn *websocket.Conn, decompress func(io.Reader) (io.Reader, error)) []byte {
	t.Helper()
	mt, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if mt != websocket.BinaryMessage {
		t.Fatalf("expected binary message, got %d", mt)
	}
	r, err := decompress(bytes.NewReader(msg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestServerWsCompressedCov(t *testing.T) {
	srv := NewServer()
	defer srv.Close()

	cases := []struct {
		provider   string
		first      string
		decompress func(io.Reader) (io.Reader, error)
	}{
		{"digifinex", "digifinex-ws-pong-msg.json", func(r io.Reader) (io.Reader, error) { return zlib.NewReader(r) }},
		{"htx", "htx-ws-sub-msg.json", func(r io.Reader) (io.Reader, error) { return gzip.NewReader(r) }},
	}
	for _, tc := range cases {
		conn := dialCov(t, srv.URL, tc.provider)
		if err := conn.WriteMessage(websocket.TextMessage, []byte("sub")); err != nil {
			t.Fatal(err)
		}
		_, msg, err := conn.ReadMessage()
		if err != nil || string(msg) != "bogus" {
			t.Fatalf("%s: expected bogus, got %q %v", tc.provider, msg, err)
		}
		got := readCompressedCov(t, conn, tc.decompress)
		want, _ := responses.ReadFile("data/" + tc.first)
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: unexpected decompressed payload", tc.provider)
		}
		conn.Close()
	}
}

func TestServerWsBadPathCov(t *testing.T) {
	srv := NewServer()
	defer srv.Close()

	// path has too many segments: handler returns without upgrading
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/a/b/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err == nil {
		conn.Close()
		t.Fatal("expected dial failure")
	}
	if resp == nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("expected handshake rejected with 200, got %+v", resp)
	}
	_ = resp.Body.Close()

	// plain http request to a ws path is rejected by the upgrader
	code, _ := getCov(t, srv.URL+"/okx/ws")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", code)
	}
}
