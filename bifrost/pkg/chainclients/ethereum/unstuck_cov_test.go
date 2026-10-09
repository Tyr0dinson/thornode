package ethereum

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"

	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/rs/zerolog"

	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

var covConfigInitOnce sync.Once

// covInitConfig loads the default bifrost config (reschedule buffer of 150 blocks).
func covInitConfig() {
	covConfigInitOnce.Do(config.Init)
}

// covTxByHash renders a tx for eth_getTransactionByHash; mined txs carry a block number.
func covTxByHash(t *testing.T, tx *etypes.Transaction, mined bool) json.RawMessage {
	t.Helper()
	b, err := tx.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !mined {
		return b
	}
	var m map[string]json.RawMessage
	if err = json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["blockNumber"] = json.RawMessage(`"0x10"`)
	m["blockHash"] = json.RawMessage(`"0x0000000000000000000000000000000000000000000000000000000000000010"`)
	m["from"] = json.RawMessage(`"0x0000000000000000000000000000000000000001"`)
	out, _ := json.Marshal(m)
	return out
}

// covSentTx decodes the last raw tx passed to eth_sendRawTransaction.
func covSentTx(t *testing.T, rpc *covRPCServer) *etypes.Transaction {
	t.Helper()
	params := rpc.lastParams("eth_sendRawTransaction")
	if len(params) == 0 {
		t.Fatal("no tx was broadcast")
	}
	var hexTx string
	if err := json.Unmarshal(params[0], &hexTx); err != nil {
		t.Fatal(err)
	}
	raw, err := hexutil.Decode(hexTx)
	if err != nil {
		t.Fatal(err)
	}
	tx := &etypes.Transaction{}
	if err = tx.UnmarshalBinary(raw); err != nil {
		t.Fatal(err)
	}
	return tx
}

func (e *covEnv) stuckItem(t *testing.T, hash string, height int64) evmtypes.SignedTxItem {
	t.Helper()
	toi := e.outbound(ethCoin(common.One), "OUT:"+thortypes.GetRandomTxHash().String())
	toi.Height = height
	if err := e.client.signerCacheManager.SetSigned(toi.CacheHash(), toi.CacheVault(common.ETHChain), hash); err != nil {
		t.Fatal(err)
	}
	return evmtypes.SignedTxItem{Hash: hash, Height: height, VaultPubKey: e.pubKey.String(), TxOutItem: &toi}
}

func TestCovUnstuckLoopStops(t *testing.T) {
	env := newCovEnv(t, nil)
	close(env.client.stopchan)
	env.client.wg.Add(1)
	done := make(chan struct{})
	go func() {
		env.client.unstuck()
		close(done)
	}()
	<-done
	env.client.wg.Wait()
}

func TestCovUnstuckActionGates(t *testing.T) {
	covInitConfig()
	env := newCovEnv(t, nil)
	c := env.client
	hash := "0x1111111111111111111111111111111111111111111111111111111111111111"
	if err := c.AddSignedTxItem(hash, 800, env.pubKey.String(), nil); err != nil {
		t.Fatal(err)
	}
	env.rpc.set("eth_getTransactionByHash", covResult(nil))
	remaining := func() int {
		items, err := c.ethScanner.blockMetaAccessor.GetSignedTxItems()
		if err != nil {
			t.Fatal(err)
		}
		return len(items)
	}

	// halted signing: with a valid signing period and an eligible item, the
	// halt is the only thing stopping the pass
	env.bridge.constants = map[string]int64{constants.SigningTransactionPeriod.String(): 300}
	env.bridge.height = 1000
	env.bridge.setMimir("HALTSIGNING", 1)
	c.unstuckAction()
	if remaining() != 1 || env.rpc.count("eth_getTransactionByHash") != 0 {
		t.Fatal("halted signing must not unstuck")
	}
	// clearing the halt lets the same item through to the tx lookup (not found
	// on chain, so it is dropped from the store)
	env.bridge.setMimir("HALTSIGNING", 0)
	c.unstuckAction()
	if remaining() != 0 || env.rpc.count("eth_getTransactionByHash") != 1 {
		t.Fatalf("unhalted pass: remaining=%d lookups=%d", remaining(), env.rpc.count("eth_getTransactionByHash"))
	}
	if err := c.AddSignedTxItem(hash, 800, env.pubKey.String(), nil); err != nil {
		t.Fatal(err)
	}
	lookups := env.rpc.count("eth_getTransactionByHash")

	// constants unavailable
	env.bridge.constErr = errors.New("down")
	c.unstuckAction()
	if env.rpc.count("eth_getTransactionByHash") != lookups || remaining() != 1 {
		t.Fatal("constants error must abort")
	}
	env.bridge.constErr = nil

	// invalid signing period
	env.bridge.constants = map[string]int64{constants.SigningTransactionPeriod.String(): 0}
	c.unstuckAction()
	if env.rpc.count("eth_getTransactionByHash") != lookups || remaining() != 1 {
		t.Fatal("invalid signing period must abort")
	}
}

func TestCovUnstuckActionSelection(t *testing.T) {
	covInitConfig()
	env := newCovEnv(t, nil)
	c := env.client
	env.bridge.constants = map[string]int64{constants.SigningTransactionPeriod.String(): 300}
	env.bridge.height = 1000

	future := env.stuckItem(t, "0x01", 1001)
	recent := env.stuckItem(t, "0x02", 950)
	ready := env.stuckItem(t, "0x03", 800)
	early := env.stuckItem(t, "0x04", 800)
	early.TxOutItem.Height = 950 // not yet in the reschedule buffer
	for _, it := range []evmtypes.SignedTxItem{future, recent, ready, early} {
		if err := c.ethScanner.blockMetaAccessor.AddSignedTxItem(it); err != nil {
			t.Fatal(err)
		}
	}
	// the ready tx is gone from chain: removed and signer cache cleared for re-sign
	env.rpc.set("eth_getTransactionByHash", covResult(nil))
	c.unstuckAction()

	items, err := c.ethScanner.blockMetaAccessor.GetSignedTxItems()
	if err != nil {
		t.Fatal(err)
	}
	left := map[string]bool{}
	for _, it := range items {
		left[it.Hash] = true
	}
	if len(items) != 3 || !left[future.Hash] || !left[recent.Hash] || !left[early.Hash] {
		t.Fatalf("remaining items = %+v", items)
	}
	if env.rpc.count("eth_getTransactionByHash") != 1 {
		t.Fatalf("only one tx should be checked, got %d", env.rpc.count("eth_getTransactionByHash"))
	}
	if c.signerCacheManager.HasSigned(ready.TxOutItem.CacheHash()) {
		t.Fatal("dropped tx should be removed from signer cache")
	}

	// rpc errors keep the item and stop the pass
	env.bridge.height = 1250 // early item now in its buffer as well
	env.rpc.set("eth_getTransactionByHash", covErr("down"))
	c.unstuckAction()
	items, _ = c.ethScanner.blockMetaAccessor.GetSignedTxItems()
	if len(items) != 3 {
		t.Fatalf("errors must keep items, got %d", len(items))
	}
}

func TestCovUnstuckTx(t *testing.T) {
	if notMocknetCov() {
		t.Skip("mocknet only: cancel-tx gas pricing uses the mocknet fee config")
	}
	env := newCovEnv(t, nil)
	c := env.client
	log := zerolog.Nop()
	to := ecommon.HexToAddress(covRouter.String())

	orig := env.covSignedTx(t, etypes.NewTransaction(3, to, big.NewInt(0), 21000, big.NewInt(100_000_000_000), nil))
	item := env.stuckItem(t, orig.Hash().Hex(), 800)

	// committed tx -> nothing to do
	env.rpc.set("eth_getTransactionByHash", covResult(covTxByHash(t, orig, true)))
	if err := c.unstuckTx(log, item); err != nil {
		t.Fatal(err)
	}
	if env.rpc.count("eth_sendRawTransaction") != 0 {
		t.Fatal("committed tx should not be cancelled")
	}

	// invalid vault pubkey -> dropped
	env.rpc.set("eth_getTransactionByHash", covResult(covTxByHash(t, orig, false)))
	bad := item
	bad.VaultPubKey = "bogus"
	if err := c.unstuckTx(log, bad); err != nil {
		t.Fatal(err)
	}

	// pending -> cancel tx to self at doubled original price (orig*1.1 > 2*current)
	env.rpc.set("eth_sendRawTransaction", covResult(orig.Hash().Hex()))
	if err := c.unstuckTx(log, item); err != nil {
		t.Fatal(err)
	}
	sent := covSentTx(t, env.rpc)
	if sent.Nonce() != 3 || sent.Value().Sign() != 0 || !strings.EqualFold(sent.To().Hex(), env.vaultAddr.String()) {
		t.Fatalf("cancel tx nonce=%d value=%s to=%s", sent.Nonce(), sent.Value(), sent.To().Hex())
	}
	if sent.Type() != etypes.DynamicFeeTxType || sent.GasFeeCap().Cmp(big.NewInt(200_000_000_000)) != 0 {
		t.Fatalf("cancel tx type=%d feeCap=%s", sent.Type(), sent.GasFeeCap())
	}
	if sent.GasTipCap().Cmp(big.NewInt(20_000_000_000)) != 0 {
		t.Fatalf("tip cap = %s", sent.GasTipCap())
	}
	if sent.Gas() != 100_000 {
		t.Fatalf("cancel gas = %d", sent.Gas())
	}
	if !c.signerCacheManager.HasSigned(item.TxOutItem.CacheHash()) {
		t.Fatal("cancel tx should be recorded in signer cache")
	}

	// cheap original -> doubled current scanner price (2 * 20 gwei)
	cheap := env.covSignedTx(t, etypes.NewTransaction(4, to, big.NewInt(0), 21000, big.NewInt(1_000_000_000), nil))
	env.rpc.set("eth_getTransactionByHash", covResult(covTxByHash(t, cheap, false)))
	env.rpc.set("eth_sendRawTransaction", covErr("already known"))
	if err := c.unstuckTx(log, item); err != nil {
		t.Fatalf("already known should be tolerated: %v", err)
	}
	sent = covSentTx(t, env.rpc)
	if sent.Nonce() != 4 || sent.GasFeeCap().Cmp(big.NewInt(40_000_000_000)) != 0 {
		t.Fatalf("cancel nonce=%d feeCap=%s", sent.Nonce(), sent.GasFeeCap())
	}

	// broadcast failure is returned
	env.rpc.set("eth_sendRawTransaction", covErr("underpriced"))
	if err := c.unstuckTx(log, item); err == nil || !strings.Contains(err.Error(), "broadcast the cancel") {
		t.Fatalf("expected broadcast error, got %v", err)
	}

	// sign failure is returned
	remote := env.useTSS(t, covKeyManager{err: errors.New("tss down")})
	remoteItem := item
	remoteItem.VaultPubKey = remote.String()
	if err := c.unstuckTx(log, remoteItem); err == nil || !strings.Contains(err.Error(), "fail to sign") {
		t.Fatalf("expected sign error, got %v", err)
	}
}

func TestCovUnstuckTxFixedGasRate(t *testing.T) {
	env := newCovEnv(t, func(cfg *config.BifrostChainConfiguration) {
		cfg.BlockScanner.FixedGasRate = 5_000_000_000
	})
	to := ecommon.HexToAddress(covRouter.String())
	orig := env.covSignedTx(t, etypes.NewTransaction(8, to, big.NewInt(0), 21000, big.NewInt(1), nil))
	item := env.stuckItem(t, orig.Hash().Hex(), 800)
	env.rpc.set("eth_getTransactionByHash", covResult(covTxByHash(t, orig, false)))
	env.rpc.set("eth_sendRawTransaction", covResult(orig.Hash().Hex()))
	if err := env.client.unstuckTx(zerolog.Nop(), item); err != nil {
		t.Fatal(err)
	}
	sent := covSentTx(t, env.rpc)
	if sent.Type() != etypes.LegacyTxType || sent.GasPrice().Cmp(big.NewInt(10_000_000_000)) != 0 || sent.Nonce() != 8 {
		t.Fatalf("cancel type=%d price=%s nonce=%d", sent.Type(), sent.GasPrice(), sent.Nonce())
	}
}

// TestCovUnstuckTxNilTxOutItem: a pending signed item recorded without a
// TxOutItem (corrupt or legacy store entry) cannot be cancelled, since the
// cancel is signed at the outbound's height. unstuckTx must drop it (nil error,
// so the caller removes it) without signing or broadcasting, not panic.
func TestCovUnstuckTxNilTxOutItem(t *testing.T) {
	env := newCovEnv(t, nil)
	to := ecommon.HexToAddress(covRouter.String())
	orig := env.covSignedTx(t, etypes.NewTransaction(1, to, big.NewInt(0), 21000, big.NewInt(1), nil))
	env.rpc.set("eth_getTransactionByHash", covResult(covTxByHash(t, orig, false)))
	item := evmtypes.SignedTxItem{Hash: orig.Hash().Hex(), Height: 1, VaultPubKey: env.pubKey.String()}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unstuckTx panicked on a nil TxOutItem: %v", r)
		}
	}()
	if err := env.client.unstuckTx(zerolog.Nop(), item); err != nil {
		t.Fatalf("expected the item to be dropped without error, got %v", err)
	}
	if n := env.rpc.count("eth_sendRawTransaction"); n != 0 {
		t.Fatalf("expected no cancel broadcast, got %d", n)
	}
}
