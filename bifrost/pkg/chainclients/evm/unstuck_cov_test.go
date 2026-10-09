package evm

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"

	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	evmtypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// covUnstuckEnv returns an env whose RPC serves eth_getTransactionByHash from txs.
func covUnstuckEnv(t *testing.T) (*covEnv, map[string]json.RawMessage) {
	t.Helper()
	config.Init()
	env := newCovEnv(t, common.AVAXChain, nil)
	txs := map[string]json.RawMessage{}
	env.rpc.set("eth_getTransactionByHash", func(params []json.RawMessage) (any, error) {
		h := strings.ToLower(covParamString(params, 0))
		if h == strings.ToLower(ecommon.HexToHash("0xbad").Hex()) {
			return nil, errors.New("rpc failure")
		}
		if tx, ok := txs[h]; ok {
			return tx, nil
		}
		return nil, nil
	})
	return env, txs
}

func covSignedItem(env *covEnv, hash string, height, toiHeight int64) evmtypes.SignedTxItem {
	toi := &stypes.TxOutItem{
		Chain:       common.AVAXChain,
		VaultPubKey: env.client.localPubKey,
		Memo:        "OUT:" + ttypes.GetRandomTxHash().String(),
		Height:      toiHeight,
		InHash:      ttypes.GetRandomTxHash(),
	}
	return evmtypes.SignedTxItem{
		Hash:        hash,
		Height:      height,
		VaultPubKey: env.client.localPubKey.String(),
		TxOutItem:   toi,
	}
}

func covSignedItems(t *testing.T, env *covEnv) []evmtypes.SignedTxItem {
	t.Helper()
	items, err := env.client.evmScanner.blockMetaAccessor.GetSignedTxItems()
	require.NoError(t, err)
	return items
}

func TestCovUnstuckActionGates(t *testing.T) {
	env, _ := covUnstuckEnv(t)
	c := env.client
	acc := c.evmScanner.blockMetaAccessor
	notFound := ecommon.HexToHash("0x01").Hex()
	require.NoError(t, acc.AddSignedTxItem(covSignedItem(env, notFound, 800, 800)))

	// signing halted -> nothing happens
	env.bridge.setMimir("HALTSIGNING", 1)
	c.unstuckAction()
	require.Len(t, covSignedItems(t, env), 1)
	env.bridge.setMimir("HALTSIGNING", -1)

	// height lookup failure after the halt check
	env.bridge.heightCalls = 0
	env.bridge.heightFailFrom = 2
	c.unstuckAction()
	require.Len(t, covSignedItems(t, env), 1)
	env.bridge.heightFailFrom = 0

	// constants failure
	env.bridge.constsErr = errors.New("no constants")
	c.unstuckAction()
	require.Len(t, covSignedItems(t, env), 1)
	env.bridge.constsErr = nil

	// invalid signing period
	env.bridge.consts = map[string]int64{constants.SigningTransactionPeriod.String(): 0}
	c.unstuckAction()
	require.Len(t, covSignedItems(t, env), 1)
	env.bridge.consts = map[string]int64{constants.SigningTransactionPeriod.String(): 300}

	// not found on chain -> removed from block meta
	c.unstuckAction()
	require.Empty(t, covSignedItems(t, env))
}

func TestCovUnstuckActionScheduling(t *testing.T) {
	env, txs := covUnstuckEnv(t)
	c := env.client
	acc := c.evmScanner.blockMetaAccessor
	other := ecommon.HexToAddress("0x000000000000000000000000000000000000bEEF")

	// a committed (non-pending) tx
	mined := covSignTx(t, c.kw.GetPrivKey(), 0, &other, big.NewInt(1), nil)
	txs[strings.ToLower(mined.Hash().Hex())] = covTxJSON(t, mined, big.NewInt(5))

	future := covSignedItem(env, ecommon.HexToHash("0x02").Hex(), 1001, 1001)     // height beyond thorchain
	tooRecent := covSignedItem(env, ecommon.HexToHash("0x03").Hex(), 900, 900)    // within wait blocks
	beforeBuffer := covSignedItem(env, ecommon.HexToHash("0x04").Hex(), 800, 950) // not in reschedule buffer
	committed := covSignedItem(env, mined.Hash().Hex(), 800, 800)                 // eligible
	for _, it := range []evmtypes.SignedTxItem{future, tooRecent, beforeBuffer, committed} {
		require.NoError(t, acc.AddSignedTxItem(it))
	}

	remaining := func() map[string]bool {
		out := map[string]bool{}
		for _, it := range covSignedItems(t, env) {
			out[it.Hash] = true
		}
		return out
	}

	c.unstuckAction()
	left := remaining()
	require.Len(t, left, 3)
	require.True(t, left[future.Hash])
	require.True(t, left[tooRecent.Hash])
	require.True(t, left[beforeBuffer.Hash])
	require.False(t, left[committed.Hash])

	// an rpc failure stops the pass and keeps the item for the next attempt
	failing := covSignedItem(env, ecommon.HexToHash("0xbad").Hex(), 800, 800)
	require.NoError(t, acc.AddSignedTxItem(failing))
	c.unstuckAction()
	left = remaining()
	require.Len(t, left, 4)
	require.True(t, left[failing.Hash])
	require.Equal(t, 0, env.rpc.count("eth_sendRawTransaction"))
}

func TestCovUnstuckTx(t *testing.T) {
	env, txs := covUnstuckEnv(t)
	c := env.client
	clog := zerolog.Nop()
	other := ecommon.HexToAddress("0x000000000000000000000000000000000000bEEF")

	// rpc failure
	err := c.unstuckTx(clog, covSignedItem(env, ecommon.HexToHash("0xbad").Hex(), 1, 1))
	require.ErrorContains(t, err, "fail to get transaction by txid")

	// not found clears the signer cache entry for that tx
	item := covSignedItem(env, ecommon.HexToHash("0x05").Hex(), 1, 1)
	require.NoError(t, c.signerCacheManager.SetSigned(item.TxOutItem.CacheHash(), item.TxOutItem.CacheVault(c.GetChain()), item.Hash))
	require.NoError(t, c.unstuckTx(clog, item))
	require.False(t, c.signerCacheManager.HasSigned(item.TxOutItem.CacheHash()))

	// pending tx
	pending := covSignTx(t, c.kw.GetPrivKey(), 7, &other, big.NewInt(1), nil)
	txs[strings.ToLower(pending.Hash().Hex())] = covTxJSON(t, pending, nil)

	// invalid vault pubkey: nothing to do
	item = covSignedItem(env, pending.Hash().Hex(), 1, 1)
	item.VaultPubKey = "bogus"
	require.NoError(t, c.unstuckTx(clog, item))
	require.Equal(t, 0, env.rpc.count("eth_sendRawTransaction"))

	// successful cancel: same nonce, to self, doubled gas price
	var sent *etypes.Transaction
	env.rpc.set("eth_sendRawTransaction", func(params []json.RawMessage) (any, error) {
		raw := covParamString(params, 0)
		tx := &etypes.Transaction{}
		if uErr := tx.UnmarshalBinary(ecommon.FromHex(raw)); uErr != nil {
			return nil, uErr
		}
		sent = tx
		return tx.Hash().Hex(), nil
	})
	item = covSignedItem(env, pending.Hash().Hex(), 1, 1)
	require.NoError(t, c.unstuckTx(clog, item))
	require.NotNil(t, sent)
	require.Equal(t, uint64(7), sent.Nonce())
	require.Equal(t, strings.ToLower(env.vaultAddr(t).String()), strings.ToLower(sent.To().Hex()))
	require.Equal(t, 0, sent.Value().Sign())
	// network gas price is 0, so 2x the original price is used
	require.Equal(t, new(big.Int).Mul(pending.GasPrice(), big.NewInt(2)), sent.GasPrice())
	hash, ok := c.signerCacheManager.GetSignedTxHash(item.TxOutItem.CacheHash())
	require.True(t, ok)
	require.Equal(t, sent.Hash().Hex(), hash)

	// a higher network gas price wins over the inflated original
	c.evmScanner.gasPrice.Store(big.NewInt(1e12))
	require.NoError(t, c.unstuckTx(clog, covSignedItem(env, pending.Hash().Hex(), 1, 1)))
	require.Equal(t, big.NewInt(2e12), sent.GasPrice())

	// broadcast failure
	env.rpc.setError("eth_sendRawTransaction", "replacement transaction underpriced")
	err = c.unstuckTx(clog, covSignedItem(env, pending.Hash().Hex(), 1, 1))
	require.ErrorContains(t, err, "fail to broadcast the cancel transaction")

	// signing failure for a TSS vault
	remote := ttypes.GetRandomPubKey()
	kw, err := evm.NewKeySignWrapper(c.kw.GetPrivKey(), c.localPubKey, &covTSS{err: errors.New("tss down")}, covChainID, "AVAX")
	require.NoError(t, err)
	c.kw = kw
	item = covSignedItem(env, pending.Hash().Hex(), 1, 1)
	item.VaultPubKey = remote.String()
	err = c.unstuckTx(clog, item)
	require.ErrorContains(t, err, "fail to sign tx for cancelling")

	// committed tx is a no-op
	txs[strings.ToLower(pending.Hash().Hex())] = covTxJSON(t, pending, big.NewInt(3))
	require.NoError(t, c.unstuckTx(clog, covSignedItem(env, pending.Hash().Hex(), 1, 1)))

	// a pending signed item without a TxOutItem (corrupt or legacy store entry)
	// cannot be cancelled, since the cancel is signed at the outbound's height:
	// it is dropped (nil error, so the caller removes it) without signing or
	// broadcasting, instead of panicking the client
	txs[strings.ToLower(pending.Hash().Hex())] = covTxJSON(t, pending, nil)
	nilToi := covSignedItem(env, pending.Hash().Hex(), 1, 1)
	nilToi.TxOutItem = nil
	sentBefore := env.rpc.count("eth_sendRawTransaction")
	require.NotPanics(t, func() { err = c.unstuckTx(clog, nilToi) })
	require.NoError(t, err)
	require.Equal(t, sentBefore, env.rpc.count("eth_sendRawTransaction"))
}
