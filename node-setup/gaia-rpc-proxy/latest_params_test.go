package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLatestBlockPreservesEmptyParams(t *testing.T) {
	upstream := newGaiaTestNode(t, 1)
	strict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			t.Error("invalid upstream request")
			return
		}
		if _, ok := q["params"]; !ok {
			http.Error(w, "params required", http.StatusBadRequest)
			return
		}
		body, _ := json.Marshal(q)
		forward := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
		forward.Header = r.Header.Clone()
		upstream.server.Config.Handler.ServeHTTP(w, forward)
	}))
	defer strict.Close()
	u, _ := url.Parse(strict.URL)
	g := newGaiaProxy(u, u, time.Second)
	defer g.client.CloseIdleConnections()
	for _, params := range []string{`null`, `{}`, `{"height":null}`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/gaia", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"block","params":`+params+`}`))
		g.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("params=%s status=%d: %s", params, w.Code, w.Body.String())
		}
	}
}
