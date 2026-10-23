package evm

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/rs/zerolog"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	ttypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// nullTxProxy forwards every JSON-RPC call to target except eth_getTransactionByHash,
// which it answers with null: the behaviour of an endpoint that never saw a broadcast
// made through another endpoint.
func nullTxProxy(target string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var rpcReq struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &rpcReq)
		rw.Header().Set("Content-Type", "application/json")
		if rpcReq.Method == "eth_getTransactionByHash" {
			_, _ = rw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
			return
		}
		resp, err := http.Post(target+req.URL.Path, "application/json", bytes.NewReader(body))
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		rw.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(rw, resp.Body)
	}))
}

// A broadcast made through one endpoint can be unknown to the endpoint that is active
// when unstuck runs. The signer cache must not be cleared on that single "not found":
// every endpoint is asked, and while one still holds the transaction the item is left
// for a later round. Clearing it would re-sign the outbound at a new nonce while the
// original is still live.
func (s *UnstuckTestSuite) TestUnstuckKeepsCacheWhileAnotherEndpointHoldsTheTx(c *C) {
	config.Init()
	active := nullTxProxy(s.server.URL)
	defer active.Close()
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	poolMgr := thorclient.NewPoolMgr(s.bridge)
	e, err := NewEVMClient(s.thorKeys, config.BifrostChainConfiguration{
		ChainID:        common.AVAXChain,
		RPCHost:        active.URL,
		SolvencyBlocks: 10,
		Endpoints: []config.ChainEndpointConfig{
			{Name: "active", RPCHost: active.URL, Priority: 1},
			{Name: "holder", RPCHost: s.server.URL, Priority: 2},
		},
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:            common.AVAXChain,
			StartBlockHeight:   1,
			HTTPRequestTimeout: time.Second * 10,
			GasCacheBlocks:     40,
			Concurrency:        1,
		},
	}, nil, s.bridge, s.m, pubkeyMgr, poolMgr)
	c.Assert(err, IsNil)
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	pubkeyMgr.AddPubKey(e.kw.GetPubKey(), false, common.SigningAlgoSecp256k1)
	c.Assert(e.EndpointManager().Active().Name, Equals, "active")

	inHash := types2.GetRandomTxHash()
	pKey := types2.GetRandomPubKey()
	toAddr, _ := pKey.GetAddress(e.GetChain())
	toi := ttypes.TxOutItem{
		Chain:       e.GetChain(),
		ToAddress:   toAddr,
		VaultPubKey: e.kw.GetPubKey(),
		Coins:       common.Coins{common.NewCoin(e.GetChain().GetGasAsset(), cosmos.NewUint(100000000))},
		Memo:        "OUT:" + inHash.String(),
		MaxGas:      common.Gas(common.NewCoins(common.NewCoin(e.GetChain().GetGasAsset(), cosmos.NewUint(100000000)))),
		GasRate:     5,
		InHash:      inHash,
		Height:      800,
	}
	accessor := e.evmScanner.blockMetaAccessor
	stuckTxID := "0x88df016429689c079f3b2f6ad39fa052532c56795b733da78a91ebe6a713944b" // pending on "holder" only
	c.Assert(e.signerCacheManager.SetSigned(toi.CacheHash(), toi.CacheVault(e.GetChain()), stuckTxID), IsNil)
	item := types.SignedTxItem{Hash: stuckTxID, Height: 800, VaultPubKey: e.kw.GetPubKey().String(), TxOutItem: &toi}
	c.Assert(accessor.AddSignedTxItem(item), IsNil)

	err = e.unstuckTx(zerolog.Nop(), item)
	c.Assert(err, ErrorMatches, ".*alive on another endpoint.*")
	e.unstuckAction()
	items, err := accessor.GetSignedTxItems()
	c.Assert(err, IsNil)
	c.Assert(items, HasLen, 1) // left for a later round
	c.Assert(e.signerCacheManager.HasSigned(toi.CacheHash()), Equals, true)
}
