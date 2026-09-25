package solana

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/common"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gopkg.in/check.v1"
)

// nonceSolvencyHandler serves getAccountInfo (a nonce account with the given
// authority, or a null value) and getBalance (the given lamports) so
// vaultNonceAccountLamports can be exercised end to end.
func nonceSolvencyHandler(authority string, lamports int64, exists bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "getAccountInfo":
			if !exists {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":null}}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":{"owner":"11111111111111111111111111111111","data":{"program":"nonce","parsed":{"type":"initialized","info":{"authority":%q,"blockhash":"9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo"}}}}}}`, authority)
		case "getBalance":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":%d}}`, lamports)
		default:
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
		}
	}
}

func (s *SolTestSuite) TestVaultNonceAccountLamports(c *C) {
	vault := types2.GetRandomEd25519PubKey()
	vaultAddr, err := vault.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)

	newClient := func(h http.HandlerFunc) (*httptest.Server, *SOLClient) {
		srv := httptest.NewServer(h)
		return srv, &SOLClient{rpcClient: rpc.NewSolRPC(srv.URL, 5*time.Second), logger: log.Logger}
	}

	// Authority == vault -> its rent counts.
	srv, cl := newClient(nonceSolvencyHandler(vaultAddr.String(), 1447680, true))
	c.Assert(cl.vaultNonceAccountLamports(vault).Int64(), Equals, int64(1447680))
	srv.Close()

	// Foreign authority -> not counted.
	foreign := types2.GetRandomEd25519PubKey()
	foreignAddr, _ := foreign.GetAddress(common.SOLChain)
	srv2, cl2 := newClient(nonceSolvencyHandler(foreignAddr.String(), 1447680, true))
	c.Assert(cl2.vaultNonceAccountLamports(vault).Int64(), Equals, int64(0))
	srv2.Close()

	// No nonce account (feature off) -> zero, unchanged behaviour.
	srv3, cl3 := newClient(nonceSolvencyHandler("", 0, false))
	c.Assert(cl3.vaultNonceAccountLamports(vault).Int64(), Equals, int64(0))
	srv3.Close()

	// Created-but-uninitialised account -> not counted (existence alone is insufficient).
	uninitHandler := func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "getAccountInfo" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":{"owner":"11111111111111111111111111111111","data":{"program":"nonce","parsed":{"type":"uninitialized","info":{}}}}}}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"context":{"slot":1},"value":1447680}}`)
	}
	srv4, cl4 := newClient(uninitHandler)
	c.Assert(cl4.vaultNonceAccountLamports(vault).Int64(), Equals, int64(0))
	srv4.Close()
}
