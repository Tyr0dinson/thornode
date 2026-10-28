package solana

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/reporter"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type solReporterBridge struct {
	thorclient.ThorchainBridge
	vaults ttypes.Vaults
}

func (b *solReporterBridge) GetAsgards() (ttypes.Vaults, error) {
	return b.vaults, nil
}

type solReporterAttester struct {
	reports chan common.Solvency
}

func (a *solReporterAttester) AttestSolvency(ctx context.Context, msg common.Solvency) error {
	select {
	case a.reports <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// A nonce-account RPC failure must not remove the vault from solvency reporting.
// Exercise the real account reader through the asynchronous dispatcher, rather
// than replacing GetVaultAccount with a reporter mock.
func (s *ClientBackfillSuite) TestReporterSolvencyIgnoresNonceAccount(c *C) {
	var nonceReads atomic.Int64
	balanceAddresses := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var rpcReq struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(req.Body).Decode(&rpcReq); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch rpcReq.Method {
		case "getSlot":
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","result":111,"id":1}`)
		case "getBalance":
			var address string
			if len(rpcReq.Params) == 0 || json.Unmarshal(rpcReq.Params[0], &address) != nil {
				http.Error(w, "missing balance address", http.StatusBadRequest)
				return
			}
			balanceAddresses <- address
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","result":{"context":{"slot":111},"value":10000000000},"id":1}`)
		case "getAccountInfo":
			nonceReads.Add(1)
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","error":{"code":-32005,"message":"nonce account unavailable"},"id":1}`)
		default:
			_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","result":null,"id":1}`)
		}
	}))
	defer server.Close()
	client := s.makeClient(c, server)
	client.cfg.SolvencyBlocks = 50
	client.cfg.BlockScanner.ObservationFlexibilityBlocks = 20
	client.solScanner.healthy.Store(true)
	client.solScanner.lastFeeRate.Store(1_000)
	vault := ttypes.Vault{
		PubKey:      ttypes.GetRandomPubKey(),
		PubKeyEddsa: ttypes.GetRandomEd25519PubKey(),
		Coins:       common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.NewUint(2_000_000_000))),
	}
	address, err := vault.PubKeyEddsa.GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	bridge := &solReporterBridge{ThorchainBridge: s.bridge, vaults: ttypes.Vaults{vault}}
	attester := &solReporterAttester{reports: make(chan common.Solvency, 1)}
	stopper := make(chan struct{})
	var wg sync.WaitGroup
	dispatcher := reporter.NewDispatcher(map[common.Chain]reporter.ChainClient{common.SOLChain: client}, nil, bridge, attester, nil, stopper, &wg)
	defer func() {
		close(stopper)
		wg.Wait()
	}()

	dispatcher.Process(common.SOLChain, 111)
	select {
	case msg := <-attester.reports:
		c.Assert(msg.PubKey, Equals, vault.PubKey)
		c.Assert(msg.Chain, Equals, common.SOLChain)
		c.Assert(msg.Height, Equals, int64(100))
		c.Assert(msg.Coins.GetCoin(common.SOLAsset).Amount.Equal(cosmos.NewUint(1_000_000_000)), Equals, true)
	case <-time.After(2 * time.Second):
		c.Fatal("expected solvency attestation despite unavailable nonce account")
	}
	select {
	case got := <-balanceAddresses:
		c.Assert(got, Equals, address.String())
	default:
		c.Fatal("report did not query the vault's actual SOL address")
	}
	c.Assert(nonceReads.Load(), Equals, int64(0))
}
