package rpc

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFirstAvailableBlockRequiresResult(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		want    uint64
		wantErr bool
	}{
		{name: "genesis", body: `{"jsonrpc":"2.0","id":1,"result":0}`},
		{name: "retained_floor", body: `{"jsonrpc":"2.0","id":1,"result":12345}`, want: 12345},
		{name: "null", body: `{"jsonrpc":"2.0","id":1,"result":null}`, wantErr: true},
		{name: "missing", body: `{"jsonrpc":"2.0","id":1}`, wantErr: true},
		{name: "rpc_error", body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"unavailable"}}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			client := NewSolRPC(server.URL, time.Second)
			floor, err := client.GetFirstAvailableBlock()
			if tc.wantErr {
				require.Error(t, err, "missing coverage must not become a successful genesis floor")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, floor)
		})
	}
}
