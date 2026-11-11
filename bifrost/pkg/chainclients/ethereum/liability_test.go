package ethereum

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	etypes "github.com/ethereum/go-ethereum/core/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	types2 "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func (s *EthereumSuite) TestSignTxRetriesWithLiability(c *C) {
	pubkeyMgr, err := pubkeymanager.NewPubKeyManager(s.bridge, s.m)
	c.Assert(err, IsNil)
	e, err := NewClient(s.thorKeys, config.BifrostChainConfiguration{
		ChainID: common.ETHChain,
		RPCHost: s.server.URL,
		BlockScanner: config.BifrostBlockScannerConfiguration{
			ChainID:            common.ETHChain,
			StartBlockHeight:   1,
			HTTPRequestTimeout: time.Second,
			MaxGasLimit:        80000,
		},
	}, nil, s.bridge, s.m, pubkeyMgr, thorclient.NewPoolMgr(s.bridge))
	c.Assert(err, IsNil)
	c.Assert(pubkeyMgr.Start(), IsNil)
	defer func() { c.Assert(pubkeyMgr.Stop(), IsNil) }()
	e.signingReady.Store(true)

	tx := stypes.TxOutItem{
		Chain:       common.ETHChain,
		ToAddress:   types2.GetRandomETHAddress(),
		VaultPubKey: e.localPubKey,
		Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One))),
		Memo:        "MIGRATE:100",
		GasRate:     5,
		MaxGas:      common.Gas(common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One)))),
	}
	addr, err := tx.VaultPubKey.GetAddress(tx.Chain)
	c.Assert(err, IsNil)
	raw, checkpoint, obs, err := e.SignTx(tx, 100)
	c.Assert(err, IsNil)
	c.Assert(raw, NotNil)
	c.Assert(obs, NotNil)
	original := new(big.Int).Set(e.pendingLiabilities[addr.String()][0])

	signed := &etypes.Transaction{}
	c.Assert(signed.UnmarshalJSON(raw), IsNil)
	c.Assert(signed.Nonce(), Equals, uint64(0))
	c.Assert(json.Unmarshal(checkpoint, new(uint64)), IsNil)
	tx.Checkpoint = checkpoint

	// Exercise the real expired-cache lookup and clearing path. The RPC mock
	// reports the old broadcast absent, but its balance reservation remains.
	s.mimirOverrides = map[string]string{
		fmt.Sprintf(constants.MimirTemplateSkipSignerCache, tx.Chain): "1",
	}
	c.Assert(e.signerCacheManager.SetSignedWithExpiry(tx.CacheHash(), tx.VaultPubKey.String(),
		signed.Hash().Hex(), time.Now().Add(-time.Minute).UnixMilli()), IsNil)
	c.Assert(e.signerCacheManager.HasSigned(tx.CacheHash()), Equals, true)
	raw, retryCheckpoint, obs, err := e.SignTx(tx, 101)
	c.Assert(err, IsNil, Commentf("same-nonce signing retry must not reserve the payment twice"))
	c.Assert(raw, NotNil)
	c.Assert(obs, NotNil)
	c.Assert(retryCheckpoint, DeepEquals, checkpoint)
	c.Assert(signed.UnmarshalJSON(raw), IsNil)
	c.Assert(signed.Nonce(), Equals, uint64(0))
	c.Assert(e.signerCacheManager.HasSigned(tx.CacheHash()), Equals, false)
	c.Assert(e.pendingLiabilities[addr.String()][0].Cmp(original), Equals, 0)

	// Reserve a different pending nonce. Replacements may reuse only their
	// own reservation, and must retain the largest signed cost at that nonce.
	other := new(big.Int).Mul(big.NewInt(2), big.NewInt(common.WeiPerOne))
	e.recordLiability(addr.String(), 1, other)
	largest := new(big.Int).Set(original)
	for _, tc := range []struct {
		amount       uint64
		insufficient bool
	}{
		{5, false}, // Same cost, with a distinct nonce still reserved.
		{4, false}, // A cheaper replacement cannot release the original cost.
		{6, false}, // A more expensive replacement must raise the reservation.
		{8, true},  // Fits alone, but cannot spend another nonce's reservation.
		{11, true}, // Genuinely exceeds the mock's confirmed balance of 10.
	} {
		tx.Coins = common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(tc.amount*common.One)))
		raw, retryCheckpoint, obs, err = e.SignTx(tx, 102)
		if tc.insufficient {
			c.Assert(err, ErrorMatches, "insufficient gas asset balance.*", Commentf("amount %d", tc.amount))
			c.Assert(raw, IsNil)
			c.Assert(obs, IsNil)
		} else {
			c.Assert(err, IsNil, Commentf("amount %d", tc.amount))
			c.Assert(raw, NotNil)
			c.Assert(obs, NotNil)
			c.Assert(retryCheckpoint, DeepEquals, checkpoint)
			c.Assert(signed.UnmarshalJSON(raw), IsNil)
			c.Assert(signed.Nonce(), Equals, uint64(0))
			if tc.amount == 6 {
				c.Assert(e.pendingLiabilities[addr.String()][0].Cmp(largest), Equals, 1)
				largest.Set(e.pendingLiabilities[addr.String()][0])
			}
		}
		c.Assert(e.pendingLiabilities[addr.String()][0].Cmp(largest), Equals, 0)
		c.Assert(e.pendingLiabilities[addr.String()][1].Cmp(other), Equals, 0)
		c.Assert(len(e.pendingLiabilities[addr.String()]), Equals, 2)
	}
}

func TestPendingLiabilityByNonce(t *testing.T) {
	client := &Client{pendingLiabilities: make(map[string]map[uint64]*big.Int)}
	client.recordLiability("vault", 0, big.NewInt(7))
	amount := big.NewInt(100)
	client.recordLiability("vault", 1, amount)
	amount.SetInt64(999) // Callers must not be able to mutate a signed reservation.
	client.recordLiability("vault", 1, big.NewInt(90))
	client.recordLiability("vault", 2, big.NewInt(20))
	client.recordLiability("other-vault", 1, big.NewInt(1000))

	// Prune nonce 0, exclude only the nonce being signed, and retain nonce 2.
	if got := client.getPendingLiability("vault", 1, 1); got.Cmp(big.NewInt(20)) != 0 {
		t.Fatalf("replacement liabilities: got %s, want 20", got)
	}
	if _, exists := client.pendingLiabilities["vault"][0]; exists {
		t.Fatal("confirmed nonce was not pruned")
	}
	if got := client.getPendingLiability("vault", 1, 3); got.Cmp(big.NewInt(120)) != 0 {
		t.Fatalf("new nonce liabilities: got %s, want 120", got)
	}
	client.recordLiability("vault", 1, big.NewInt(110))
	if got := client.getPendingLiability("vault", 1, 3); got.Cmp(big.NewInt(130)) != 0 {
		t.Fatalf("increased replacement liabilities: got %s, want 130", got)
	}
	if got := client.getPendingLiability("other-vault", 1, 2); got.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("other vault liabilities: got %s, want 1000", got)
	}
	if got := client.getPendingLiability("missing-vault", 1, 2); got.Sign() != 0 {
		t.Fatalf("missing vault liabilities: got %s, want 0", got)
	}
	if got := client.getPendingLiability("vault", 3, 3); got.Sign() != 0 {
		t.Fatalf("confirmed liabilities: got %s, want 0", got)
	}
}

func TestConcurrentReplacementLiability(t *testing.T) {
	client := &Client{pendingLiabilities: make(map[string]map[uint64]*big.Int)}
	var workers sync.WaitGroup
	for i := int64(1); i <= 100; i++ {
		workers.Add(1)
		go func(amount int64) {
			defer workers.Done()
			client.recordLiability("vault", 1, big.NewInt(amount))
			client.getPendingLiability("vault", 0, 2)
		}(i)
	}
	workers.Wait()
	if got := client.getPendingLiability("vault", 0, 2); got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("concurrent replacements: got %s, want 100", got)
	}
}
