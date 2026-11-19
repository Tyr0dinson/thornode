package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

func TestArchivePrunedConnectionRetriesFresh(t *testing.T) {
	p, a := newGaiaTestNode(t, 50), newGaiaTestNode(t, 1)
	var mu sync.Mutex
	var firstConnection string
	pruned, successful := 0, 0
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var q gaiaRequest
		if json.Unmarshal(body, &q) != nil {
			t.Error("invalid request")
			return
		}
		mu.Lock()
		if firstConnection == "" {
			firstConnection = r.RemoteAddr
		}
		oldConnection := r.RemoteAddr == firstConnection
		historical := string(q.Params["height"]) == `"40"`
		if historical && oldConnection {
			pruned++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": q.ID,
				"error": map[string]any{"code": -32603, "message": "Internal error", "data": "height 40 is not available, lowest height is 60"}})
			return
		}
		if historical {
			successful++
		}
		mu.Unlock()
		a.server.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	g := gaiaTestProxy(t, p, a)
	g.archive, _ = url.Parse(front.URL)
	gaiaTestOK(t, gaiaTestRead(g, "block", 40))
	mu.Lock()
	defer mu.Unlock()
	if pruned != 1 || successful != 1 {
		t.Fatalf("wanted one pruned connection and one fresh success, got %d / %d", pruned, successful)
	}
}
