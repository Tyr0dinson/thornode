package evm

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	etypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	covTknAsset    = "AVAX.TKN-0X333C3310824B7C685133F2BEDB2CA4B8B4DF633D"
	covAggregator  = "0x4ac1d98d9cef99ec6546ded4bd550b0b287aad6d"
	covTargetAsset = "0x333c3310824b7c685133f2bedb2ca4b8b4df633d"
)

func covOutMemo() string {
	return "OUT:" + ttypes.GetRandomTxHash().String()
}

func covTxOut(t *testing.T, env *covEnv, amount uint64) stypes.TxOutItem {
	t.Helper()
	toAddr, err := ttypes.GetRandomPubKey().GetAddress(env.client.cfg.ChainID)
	require.NoError(t, err)
	gasAsset := env.client.cfg.ChainID.GetGasAsset()
	return stypes.TxOutItem{
		Chain:       env.client.cfg.ChainID,
		ToAddress:   toAddr,
		VaultPubKey: env.client.localPubKey,
		Coins:       common.Coins{common.NewCoin(gasAsset, cosmos.NewUint(amount))},
		MaxGas:      common.Gas{common.NewCoin(gasAsset, cosmos.NewUint(300_000))},
		GasRate:     1,
		Memo:        covOutMemo(),
		InHash:      ttypes.GetRandomTxHash(),
	}
}

// ---------------------------------------------------------------------------
// construction
// ---------------------------------------------------------------------------

func TestCovNewEVMClientAuthModes(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, func(cfg *config.BifrostChainConfiguration) {
		cfg.AuthorizationBearer = "secret-token"
	})
	require.Equal(t, "Bearer secret-token", env.rpc.lastHeader().Get("Authorization"))

	env = newCovEnv(t, common.AVAXChain, func(cfg *config.BifrostChainConfiguration) {
		cfg.UserName = "alice"
		cfg.Password = "pw"
	})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:pw"))
	require.Equal(t, want, env.rpc.lastHeader().Get("Authorization"))

	// with a db path the scanner storage is persisted on disk
	dir := t.TempDir()
	env = newCovEnv(t, common.AVAXChain, func(cfg *config.BifrostChainConfiguration) {
		cfg.BlockScanner.DBPath = dir
	})
	require.NotNil(t, env.client.evmScanner)
}

func TestCovNewEVMClientErrors(t *testing.T) {
	keys := covThorKeys(t)
	bridge := newCovBridge()
	pkm := newCovPubKeyMgr()
	poolMgr := thorclient.NewPoolMgr(bridge)
	mm := covMetrics(t)

	// unsupported transport for each auth mode
	for _, mod := range []func(*config.BifrostChainConfiguration){
		func(*config.BifrostChainConfiguration) {},
		func(cfg *config.BifrostChainConfiguration) { cfg.AuthorizationBearer = "x" },
		func(cfg *config.BifrostChainConfiguration) { cfg.UserName, cfg.Password = "u", "p" },
	} {
		cfg := covChainConfig(common.AVAXChain, "ftp://invalid")
		mod(&cfg)
		c, err := NewEVMClient(keys, cfg, nil, bridge, mm, pkm, poolMgr)
		require.Error(t, err)
		require.Nil(t, c)
	}

	// chain id lookup fails
	rpc := newCovRPC(t)
	rpc.setError("eth_chainId", "boom")
	c, err := NewEVMClient(keys, covChainConfig(common.AVAXChain, rpc.srv.URL), nil, bridge, mm, pkm, poolMgr)
	require.ErrorContains(t, err, "fail to get chain id")
	require.Nil(t, c)

	// chain id zero is rejected
	rpc.setResult("eth_chainId", "0x0")
	c, err = NewEVMClient(keys, covChainConfig(common.AVAXChain, rpc.srv.URL), nil, bridge, mm, pkm, poolMgr)
	require.ErrorContains(t, err, "invalid")
	require.Nil(t, c)

	// whitelisted token missing from the token list fails scanner creation
	rpc.setResult("eth_chainId", hexutil.EncodeBig(covChainID))
	cfg := covChainConfig(common.AVAXChain, rpc.srv.URL)
	cfg.BlockScanner.WhitelistTokens = []string{"0x000000000000000000000000000000000000dEaD"}
	c, err = NewEVMClient(keys, cfg, nil, bridge, mm, pkm, poolMgr)
	require.ErrorContains(t, err, "fail to create evm block scanner")
	require.NotNil(t, c)
}

// ---------------------------------------------------------------------------
// accessors
// ---------------------------------------------------------------------------

func TestCovClientAccessors(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client

	require.Equal(t, common.AVAXChain, c.GetConfig().ChainID)
	require.False(t, c.IsBlockScannerHealthy())
	h, err := c.GetBlockScannerHeight()
	require.NoError(t, err)
	require.Equal(t, int64(1), h)
	require.NoError(t, c.RollbackBlockScanner())

	// invalid pubkey yields an empty address
	require.Equal(t, "", c.GetAddress(common.PubKey("bogus")))

	// gas fee is computed from the current gas price (zero until the cache fills)
	fee := c.GetGasFee(10)
	require.Equal(t, common.GetEVMGasFee(common.AVAXChain, big.NewInt(0), 10), fee)

	// nothing has been recorded for the vault yet
	_, _, err = c.GetLatestTxForVault(c.localPubKey.String())
	require.Error(t, err)
}

func TestCovGetAccountsAndBalances(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client
	env.rpc.setResult("eth_getTransactionCount", "0x7")

	acct, err := c.GetAccount(c.localPubKey, nil)
	require.NoError(t, err)
	require.Equal(t, int64(7), acct.Sequence)
	require.Len(t, acct.Coins, 1)
	require.Equal(t, uint64(common.One), acct.Coins[0].Amount.Uint64())

	// GetAccountByAddress resolves the router of a known vault address
	acct, err = c.GetAccountByAddress(env.vaultAddr(t).String(), nil)
	require.NoError(t, err)
	require.Equal(t, int64(7), acct.Sequence)

	vault := ttypes.Vault{
		PubKey: c.localPubKey,
		Coins:  common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(5))),
	}
	acct, err = c.GetVaultAccount(vault, nil)
	require.NoError(t, err)
	require.Equal(t, int64(7), acct.Sequence)
	require.Len(t, acct.Coins, 1)
	require.True(t, acct.Coins[0].Asset.Equals(common.AVAXAsset))

	// token balances are reported with the token symbol
	require.NoError(t, c.evmScanner.tokenManager.SaveTokenMeta("TKN", "0x333C3310824B7C685133F2BeDb2CA4b8b4DF633d", 18))
	coins, err := c.GetBalances(env.vaultAddr(t).String(), nil, covRouter.String())
	require.NoError(t, err)
	require.Len(t, coins, 2)

	// balance errors are skipped per token
	env.rpc.setError("eth_getBalance", "balance unavailable")
	env.rpc.setError("eth_call", "call unavailable")
	coins, err = c.GetBalances(env.vaultAddr(t).String(), nil, covRouter.String())
	require.NoError(t, err)
	require.Empty(t, coins)
	coins, err = c.GetVaultBalances(env.vaultAddr(t).String(), nil, covRouter.String(), vault)
	require.NoError(t, err)
	require.Empty(t, coins)

	// nonce errors propagate
	env.rpc.setError("eth_getTransactionCount", "nonce unavailable")
	_, err = c.GetAccount(c.localPubKey, nil)
	require.Error(t, err)
	_, err = c.GetVaultAccount(vault, nil)
	require.Error(t, err)
	_, err = c.GetAccountByAddress(env.vaultAddr(t).String(), nil)
	require.Error(t, err)

	// unknown address has no router
	require.Equal(t, common.NoAddress, c.getSmartContractByAddress(common.Address("0x000000000000000000000000000000000000dEaD")))
}

// ---------------------------------------------------------------------------
// outbound tx data
// ---------------------------------------------------------------------------

func TestCovGetOutboundTxData(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client
	tkn, err := common.NewAsset(covTknAsset)
	require.NoError(t, err)

	native := covTxOut(t, env, 1000)
	outMemo, err := mem.ParseMemo(common.LatestVersion, native.Memo)
	require.NoError(t, err)
	refundMemo, err := mem.ParseMemo(common.LatestVersion, "REFUND:"+ttypes.GetRandomTxHash().String())
	require.NoError(t, err)
	migrateMemo, err := mem.ParseMemo(common.LatestVersion, "MIGRATE:10")
	require.NoError(t, err)

	// plain native transferOut
	data, _, value, err := c.getOutboundTxData(native, outMemo, covRouter)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	require.Equal(t, "10000000000000", value.String()) // 1000 * 1e10

	// aggregator: refunds can not use transferOutAndCall
	agg := native
	agg.Aggregator = covAggregator
	agg.AggregatorTargetAsset = covTargetAsset
	_, _, _, err = c.getOutboundTxData(agg, refundMemo, covRouter)
	require.ErrorContains(t, err, "can't use transferOutAndCall")

	// aggregator: only native outbounds allowed
	aggTkn := agg
	aggTkn.Coins = common.Coins{common.NewCoin(tkn, cosmos.NewUint(1000))}
	_, _, _, err = c.getOutboundTxData(aggTkn, outMemo, covRouter)
	require.ErrorContains(t, err, "only be used when outbound asset is native")

	// aggregator: address that does not round trip is dropped silently
	badAgg := agg
	badAgg.Aggregator = "0x1234"
	data, _, value, err = c.getOutboundTxData(badAgg, outMemo, covRouter)
	require.NoError(t, err)
	require.Nil(t, data)
	require.Nil(t, value)
	badTarget := agg
	badTarget.AggregatorTargetAsset = "0x1234"
	data, _, _, err = c.getOutboundTxData(badTarget, outMemo, covRouter)
	require.NoError(t, err)
	require.Nil(t, data)

	// aggregator success, with and without a target limit
	data, _, value, err = c.getOutboundTxData(agg, outMemo, covRouter)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	require.Equal(t, "10000000000000", value.String())
	limit := cosmos.NewUint(55)
	agg.AggregatorTargetLimit = &limit
	data2, _, _, err := c.getOutboundTxData(agg, outMemo, covRouter)
	require.NoError(t, err)
	require.NotEqual(t, data, data2)

	// migrate: aggregator not allowed
	_, _, _, err = c.getOutboundTxData(agg, migrateMemo, covRouter)
	require.ErrorContains(t, err, "migration can't use aggregator")

	// migrate native
	data, _, _, err = c.getOutboundTxData(native, migrateMemo, covRouter)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	// migrate token to an unknown vault fails
	migTkn := native
	migTkn.Coins = common.Coins{common.NewCoin(tkn, cosmos.NewUint(1000))}
	_, _, _, err = c.getOutboundTxData(migTkn, migrateMemo, covRouter)
	require.ErrorContains(t, err, "fail to get new smart contract address")

	// migrate token to a known vault uses transferAllowance
	newVault := ttypes.GetRandomPubKey()
	newRouter := common.Address("0x000000000000000000000000000000000000bEEF")
	env.pkm.add(newVault, newRouter)
	newAddr, err := newVault.GetAddress(common.AVAXChain)
	require.NoError(t, err)
	migTkn.ToAddress = newAddr
	data, _, value, err = c.getOutboundTxData(migTkn, migrateMemo, covRouter)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	require.Equal(t, int64(0), value.Int64())
	method, err := c.vaultABI.MethodById(data[:4])
	require.NoError(t, err)
	require.Equal(t, "transferAllowance", method.Name)
}

func TestCovBuildOutboundTx(t *testing.T) {
	if notMocknetCov() {
		t.Skip("mocknet only: the test aggregator is only registered on mocknet")
	}
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client
	toi := covTxOut(t, env, 1000)
	memo, err := mem.ParseMemo(common.LatestVersion, toi.Memo)
	require.NoError(t, err)

	// success (gas price cache empty -> uses outbound rate)
	tx, err := c.buildOutboundTx(toi, memo, 3)
	require.NoError(t, err)
	require.NotNil(t, tx)
	require.Equal(t, uint64(3), tx.Nonce())
	require.Equal(t, big.NewInt(1e9), tx.GasPrice())
	require.Equal(t, uint64(3_000_000), tx.Gas()) // 300000*1e10 / 1e9
	require.Equal(t, strings.ToLower(covRouter.String()), strings.ToLower(tx.To().Hex()))

	// current gas price above the lower bound is used as-is
	c.evmScanner.gasPrice.Store(big.NewInt(2e9))
	tx, err = c.buildOutboundTx(toi, memo, 3)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(2e9), tx.GasPrice())

	// current gas price below 2/3 of outbound rate is raised to the lower bound
	c.evmScanner.gasPrice.Store(big.NewInt(1))
	toi.GasRate = 3
	tx, err = c.buildOutboundTx(toi, memo, 3)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(2e9), tx.GasPrice())
	toi.GasRate = 1
	c.evmScanner.gasPrice.Store(big.NewInt(0))

	// l1 fee reduces the max gas units
	c.cfg.EVM.ExtraL1GasFee = 100_000
	tx, err = c.buildOutboundTx(toi, memo, 3)
	require.NoError(t, err)
	require.Equal(t, uint64(2_000_000), tx.Gas())
	c.cfg.EVM.ExtraL1GasFee = 0

	// max gas too low -> abort without error
	low := toi
	low.MaxGas = common.Gas{common.NewCoin(common.AVAXAsset, cosmos.NewUint(1))}
	tx, err = c.buildOutboundTx(low, memo, 3)
	require.NoError(t, err)
	require.Nil(t, tx)

	// token outbound uses the token multiplier
	tkn, err := common.NewAsset(covTknAsset)
	require.NoError(t, err)
	tknToi := toi
	tknToi.Coins = common.Coins{common.NewCoin(tkn, cosmos.NewUint(1000))}
	tx, err = c.buildOutboundTx(tknToi, memo, 3)
	require.NoError(t, err)
	require.Equal(t, uint64(9_000_000), tx.Gas())
	require.Equal(t, int64(0), tx.Value().Int64())

	// aggregator outbounds use the aggregator gas limit
	agg := toi
	agg.Aggregator = covAggregator
	agg.AggregatorTargetAsset = covTargetAsset
	tx, err = c.buildOutboundTx(agg, memo, 3)
	require.NoError(t, err)
	require.NotNil(t, tx)
	require.Equal(t, uint64(30_000_000), tx.Gas())

	// estimated gas above aggregator limit -> abort
	env.rpc.setResult("eth_estimateGas", hexutil.EncodeUint64(500_000))
	tx, err = c.buildOutboundTx(agg, memo, 3)
	require.NoError(t, err)
	require.Nil(t, tx)

	// unknown aggregator -> abort
	unknownAgg := agg
	unknownAgg.Aggregator = "0x000000000000000000000000000000000000dEaD"
	env.rpc.setResult("eth_estimateGas", "0x5208")
	tx, err = c.buildOutboundTx(unknownAgg, memo, 3)
	require.NoError(t, err)
	require.Nil(t, tx)

	// estimate gas failure -> abort without error
	env.rpc.setError("eth_estimateGas", "execution reverted")
	tx, err = c.buildOutboundTx(toi, memo, 3)
	require.NoError(t, err)
	require.Nil(t, tx)
	env.rpc.setResult("eth_estimateGas", "0x5208")

	// refund through aggregator is rejected
	refundMemo, err := mem.ParseMemo(common.LatestVersion, "REFUND:"+ttypes.GetRandomTxHash().String())
	require.NoError(t, err)
	_, err = c.buildOutboundTx(agg, refundMemo, 3)
	require.ErrorContains(t, err, "failed to get outbound tx data")

	// vault without router
	noRouter := toi
	noRouter.VaultPubKey = ttypes.GetRandomPubKey()
	_, err = c.buildOutboundTx(noRouter, memo, 3)
	require.ErrorContains(t, err, "fail to get smart contract address")

	// migration from a vault without router falls back to the destination router
	migrateMemo, err := mem.ParseMemo(common.LatestVersion, "MIGRATE:10")
	require.NoError(t, err)
	noRouter.ToAddress = env.vaultAddr(t)
	tx, err = c.buildOutboundTx(noRouter, migrateMemo, 3)
	require.NoError(t, err)
	require.NotNil(t, tx)
	require.Equal(t, strings.ToLower(covRouter.String()), strings.ToLower(tx.To().Hex()))
}

// ---------------------------------------------------------------------------
// signing
// ---------------------------------------------------------------------------

type covTSS struct {
	tss.ThorchainKeyManager
	err error
}

func (k *covTSS) RemoteSign([]byte, common.SigningAlgo, string, int64) ([]byte, []byte, error) {
	return nil, nil, k.err
}

func TestCovSignTxValidation(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client
	good := covTxOut(t, env, 1000)

	c.signingReady.Store(false)
	_, _, _, err := c.SignTx(good, 1)
	require.ErrorContains(t, err, "still initializing")
	c.signingReady.Store(true)

	bad := good
	bad.Chain = common.BTCChain
	_, _, _, err = c.SignTx(bad, 1)
	require.ErrorContains(t, err, "not support")

	bad = good
	bad.ToAddress = ""
	_, _, _, err = c.SignTx(bad, 1)
	require.ErrorContains(t, err, "to address is empty")

	bad = good
	bad.VaultPubKey = ""
	_, _, _, err = c.SignTx(bad, 1)
	require.ErrorContains(t, err, "vault public key is empty")

	bad = good
	bad.Coins = nil
	_, _, _, err = c.SignTx(bad, 1)
	require.ErrorContains(t, err, "coins is empty")

	bad = good
	bad.Memo = "NOTAMEMO:x"
	_, _, _, err = c.SignTx(bad, 1)
	require.ErrorContains(t, err, "fail to parse memo")

	bad = good
	bad.Memo = "SWAP:AVAX.AVAX:" + env.vaultAddr(t).String()
	_, _, _, err = c.SignTx(bad, 1)
	require.ErrorContains(t, err, "inbound memo")

	bad = good
	bad.Checkpoint = []byte("not-json")
	_, _, _, err = c.SignTx(bad, 1)
	require.ErrorContains(t, err, "fail to unmarshal checkpoint")

	// pending nonce far ahead of the finalized nonce
	env.rpc.set("eth_getTransactionCount", func(params []json.RawMessage) (any, error) {
		if covParamString(params, 1) == "pending" {
			return "0x10", nil
		}
		return "0x0", nil
	})
	_, _, _, err = c.SignTx(good, 1)
	require.ErrorContains(t, err, "pending nonce too far in future")

	// finalized nonce lookup failure
	env.rpc.set("eth_getTransactionCount", func(params []json.RawMessage) (any, error) {
		if covParamString(params, 1) == "pending" {
			return "0x0", nil
		}
		return nil, errors.New("no finalized nonce")
	})
	_, _, _, err = c.SignTx(good, 1)
	require.ErrorContains(t, err, "finalized nonce")

	// pending nonce lookup failure
	env.rpc.setError("eth_getTransactionCount", "no nonce")
	_, _, _, err = c.SignTx(good, 1)
	require.ErrorContains(t, err, "nonce")
}

func TestCovSignTxPaths(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client

	// memoless outbound with checkpoint nonce signs with that nonce
	toi := covTxOut(t, env, 1000)
	toi.Memo = ""
	toi.Checkpoint = []byte("4")
	raw, cp, obs, err := c.SignTx(toi, 1)
	require.NoError(t, err)
	require.NotEmpty(t, raw)
	require.Equal(t, "4", string(cp))
	require.NotNil(t, obs)
	require.Equal(t, int64(100), obs.BlockHeight)
	signed := &etypes.Transaction{}
	require.NoError(t, signed.UnmarshalJSON(raw))
	require.Equal(t, uint64(4), signed.Nonce())

	// liability for nonce 4 was recorded
	liab := c.getPendingLiability(env.vaultAddr(t).String(), 0)
	require.Positive(t, liab.Sign())

	// chain height failure falls back to scanner height
	env.rpc.setError("eth_blockNumber", "no height")
	c.evmScanner.updateCurrentBlockHeight(42)
	toi = covTxOut(t, env, 1000)
	_, _, obs, err = c.SignTx(toi, 1)
	require.NoError(t, err)
	require.Equal(t, int64(42), obs.BlockHeight)

	// insufficient balance once pending liabilities are subtracted
	env.rpc.setResult("eth_getBalance", "0x1")
	_, _, _, err = c.SignTx(covTxOut(t, env, 1000), 1)
	require.ErrorContains(t, err, "insufficient gas asset balance")

	// balance lookup failure
	env.rpc.setError("eth_getBalance", "no balance")
	_, _, _, err = c.SignTx(covTxOut(t, env, 1000), 1)
	require.ErrorContains(t, err, "fail to get gas asset balance")
	env.rpc.setResult("eth_getBalance", "0xde0b6b3a7640000")

	// build abort (gas estimate failure) returns nothing
	env.rpc.setError("eth_estimateGas", "execution reverted")
	raw, cp, obs, err = c.SignTx(covTxOut(t, env, 1000), 1)
	require.NoError(t, err)
	require.Nil(t, raw)
	require.Nil(t, cp)
	require.Nil(t, obs)
	env.rpc.setResult("eth_estimateGas", "0x5208")

	// build error is returned
	noRouter := covTxOut(t, env, 1000)
	noRouter.VaultPubKey = ttypes.GetRandomPubKey()
	_, _, _, err = c.SignTx(noRouter, 1)
	require.ErrorContains(t, err, "smart contract address")

	// tx already signed (fresh signer cache entry) is skipped
	cached := covTxOut(t, env, 1000)
	require.NoError(t, c.signerCacheManager.SetSigned(cached.CacheHash(), cached.CacheVault(c.GetChain()), "0xabc"))
	raw, _, _, err = c.SignTx(cached, 1)
	require.NoError(t, err)
	require.Nil(t, raw)
}

func TestCovSignTxRemoteSigner(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client

	// use a vault that is not the local key so the TSS path is taken
	remoteVault := ttypes.GetRandomPubKey()
	env.pkm.add(remoteVault, covRouter)
	toi := covTxOut(t, env, 1000)
	toi.VaultPubKey = remoteVault

	setTSS := func(err error) {
		kw, kwErr := evm.NewKeySignWrapper(c.kw.GetPrivKey(), c.localPubKey, &covTSS{err: err}, covChainID, "AVAX")
		require.NoError(t, kwErr)
		c.kw = kw
	}

	// not selected for keysign
	setTSS(tss.ErrNotSelectedForKeysign)
	raw, cp, obs, err := c.SignTx(toi, 1)
	require.NoError(t, err)
	require.Nil(t, raw)
	require.Nil(t, cp)
	require.Nil(t, obs)

	// generic keysign failure keeps the checkpoint
	setTSS(errors.New("tss down"))
	_, cp, _, err = c.SignTx(toi, 1)
	require.ErrorContains(t, err, "fail to sign")
	require.Equal(t, "0", string(cp))

	// blame without nodes is not posted
	setTSS(tss.NewKeysignError(ttypes.Blame{FailReason: "x"}))
	_, _, _, err = c.SignTx(toi, 1)
	require.Error(t, err)
	require.Empty(t, env.bridge.keysignFailures)

	// blame with nodes is posted to thorchain
	blame := ttypes.Blame{FailReason: "x", BlameNodes: []ttypes.Node{{Pubkey: "pk1"}}}
	setTSS(tss.NewKeysignError(blame))
	_, _, _, err = c.SignTx(toi, 1)
	require.ErrorContains(t, err, "fail to sign")
	require.Len(t, env.bridge.keysignFailures, 1)

	// posting the blame can fail as well
	env.bridge.keysignFailErr = errors.New("post failed")
	_, _, _, err = c.SignTx(toi, 1)
	require.ErrorContains(t, err, "post failed")
	require.Len(t, env.bridge.keysignFailures, 2)
}

func TestCovCheckAndApproveAllowance(t *testing.T) {
	if notMocknetCov() {
		t.Skip("mocknet only: expects the mocknet approval dust wei")
	}
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client
	tkn, err := common.NewAsset(covTknAsset)
	require.NoError(t, err)
	toi := covTxOut(t, env, 1000)
	toi.Coins = common.Coins{common.NewCoin(tkn, cosmos.NewUint(1000))}
	mimirKey := fmt.Sprintf(constants.MimirTemplateEVMAllowanceCheck, common.AVAXChain)

	// mimir lookup error -> skip
	env.bridge.mimirErr = errors.New("mimir down")
	tx, err := c.checkAndApproveAllowance(toi, 0)
	require.NoError(t, err)
	require.Nil(t, tx)
	env.bridge.mimirErr = nil

	// mimir disabled -> skip
	tx, err = c.checkAndApproveAllowance(toi, 0)
	require.NoError(t, err)
	require.Nil(t, tx)

	env.bridge.setMimir(mimirKey, 1)

	// no coins -> skip
	none := toi
	none.Coins = nil
	tx, err = c.checkAndApproveAllowance(none, 0)
	require.NoError(t, err)
	require.Nil(t, tx)

	// multiple coins -> error
	multi := toi
	multi.Coins = common.Coins{toi.Coins[0], common.NewCoin(common.AVAXAsset, cosmos.NewUint(1))}
	_, err = c.checkAndApproveAllowance(multi, 0)
	require.ErrorContains(t, err, "more than 1 coin")

	// native asset never needs approval
	tx, err = c.checkAndApproveAllowance(covTxOut(t, env, 1000), 0)
	require.NoError(t, err)
	require.Nil(t, tx)

	// invalid vault pubkey
	badVault := toi
	badVault.VaultPubKey = common.PubKey("bogus")
	_, err = c.checkAndApproveAllowance(badVault, 0)
	require.ErrorContains(t, err, "fail to get vault address")

	// vault without router
	noRouter := toi
	noRouter.VaultPubKey = ttypes.GetRandomPubKey()
	_, err = c.checkAndApproveAllowance(noRouter, 0)
	require.ErrorContains(t, err, "no router contract")

	// allowance call failure
	env.rpc.setError("eth_call", "call failed")
	_, err = c.checkAndApproveAllowance(toi, 0)
	require.ErrorContains(t, err, "fail to call allowance")

	// malformed allowance result
	env.rpc.setResult("eth_call", "0x01")
	_, err = c.checkAndApproveAllowance(toi, 0)
	require.ErrorContains(t, err, "fail to unpack allowance")

	// sufficient allowance (max) -> no approval
	maxAllowance := "0x" + strings.Repeat("f", 64)
	env.rpc.set("eth_call", func(params []json.RawMessage) (any, error) {
		if strings.Contains(string(params[0]), "0xdd62ed3e") {
			return maxAllowance, nil
		}
		return "0x0000000000000000000000000000000000000000000000000000000000000012", nil
	})
	tx, err = c.checkAndApproveAllowance(toi, 0)
	require.NoError(t, err)
	require.Nil(t, tx)

	// insufficient allowance -> approval tx using rounded network gas price
	maxAllowance = "0x" + strings.Repeat("0", 64)
	c.evmScanner.gasPrice.Store(big.NewInt(5e10 + 7))
	tx, err = c.checkAndApproveAllowance(toi, 9)
	require.NoError(t, err)
	require.NotNil(t, tx)
	require.Equal(t, uint64(9), tx.Nonce())
	require.Equal(t, strings.ToLower("0x333C3310824B7C685133F2BeDb2CA4b8b4DF633d"), strings.ToLower(tx.To().Hex()))
	// 5e10 rounded down to 1e10 multiple, plus 1 wei mocknet dust for AVAX
	require.Equal(t, big.NewInt(5e10+1), tx.GasPrice())
	method, err := c.evmScanner.erc20ABI.MethodById(tx.Data()[:4])
	require.NoError(t, err)
	require.Equal(t, "approve", method.Name)

	// tiny network gas price rounds up to the minimum then bounded by outbound rate
	c.evmScanner.gasPrice.Store(big.NewInt(5))
	toi.GasRate = 30 // 30 gwei -> lower bound 20 gwei
	tx, err = c.checkAndApproveAllowance(toi, 9)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(2e10+1), tx.GasPrice())

	// zero network gas price uses the outbound rate directly
	c.evmScanner.gasPrice.Store(big.NewInt(0))
	tx, err = c.checkAndApproveAllowance(toi, 9)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(3e10+1), tx.GasPrice())
}

func TestCovSignTxApprovalErrors(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client
	tkn, err := common.NewAsset(covTknAsset)
	require.NoError(t, err)
	env.bridge.setMimir(fmt.Sprintf(constants.MimirTemplateEVMAllowanceCheck, common.AVAXChain), 1)
	env.rpc.set("eth_call", func(params []json.RawMessage) (any, error) {
		if strings.Contains(string(params[0]), "0xdd62ed3e") {
			return "0x" + strings.Repeat("0", 64), nil
		}
		return "0x0000000000000000000000000000000000000000000000000000000000000012", nil
	})

	// allowance check error aborts signing
	multi := covTxOut(t, env, 1000)
	multi.Coins = common.Coins{common.NewCoin(tkn, cosmos.NewUint(1)), common.NewCoin(common.AVAXAsset, cosmos.NewUint(1))}
	_, _, _, err = c.SignTx(multi, 1)
	require.ErrorContains(t, err, "more than 1 coin")

	// approval broadcast failure returns the checkpoint nonce
	toi := covTxOut(t, env, 1000)
	toi.Coins = common.Coins{common.NewCoin(tkn, cosmos.NewUint(1000))}
	env.rpc.setError("eth_sendRawTransaction", "rejected")
	_, cp, _, err := c.SignTx(toi, 1)
	require.ErrorContains(t, err, "fail to broadcast approval transaction")
	require.Equal(t, "0", string(cp))

	// approval signing via TSS: not selected -> nothing, other error -> checkpoint
	remoteVault := ttypes.GetRandomPubKey()
	env.pkm.add(remoteVault, covRouter)
	toi.VaultPubKey = remoteVault
	kw, err := evm.NewKeySignWrapper(c.kw.GetPrivKey(), c.localPubKey, &covTSS{err: tss.ErrNotSelectedForKeysign}, covChainID, "AVAX")
	require.NoError(t, err)
	c.kw = kw
	raw, cp, _, err := c.SignTx(toi, 1)
	require.NoError(t, err)
	require.Nil(t, raw)
	require.Nil(t, cp)

	kw, err = evm.NewKeySignWrapper(c.kw.GetPrivKey(), c.localPubKey, &covTSS{err: errors.New("tss down")}, covChainID, "AVAX")
	require.NoError(t, err)
	c.kw = kw
	_, cp, _, err = c.SignTx(toi, 1)
	require.ErrorContains(t, err, "fail to sign approval transaction")
	require.Equal(t, "0", string(cp))
}

// ---------------------------------------------------------------------------
// broadcast / observe
// ---------------------------------------------------------------------------

func TestCovBroadcastAndObserve(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client

	_, err := c.BroadcastTx(stypes.TxOutItem{}, []byte("garbage"))
	require.Error(t, err)

	toi := covTxOut(t, env, 1000)
	raw, _, _, err := c.SignTx(toi, 1)
	require.NoError(t, err)

	// rpc rejection
	env.rpc.setError("eth_sendRawTransaction", "insufficient funds")
	_, err = c.BroadcastTx(toi, raw)
	require.ErrorContains(t, err, "insufficient funds")

	// "already known" is acceptable; signer cache and signed tx items are updated
	env.rpc.setError("eth_sendRawTransaction", "already known")
	txid, err := c.BroadcastTx(toi, raw)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(txid, "0x"))
	require.True(t, c.signerCacheManager.HasSigned(toi.CacheHash()))
	items, err := c.evmScanner.blockMetaAccessor.GetSignedTxItems()
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, txid, items[0].Hash)
	require.Equal(t, int64(1000), items[0].Height)

	// thorchain height failure does not fail the broadcast and skips the item
	env.bridge.heightErr = errors.New("no height")
	env.rpc.setResult("eth_sendRawTransaction", txid)
	toi2 := covTxOut(t, env, 1000)
	raw2, _, _, err := c.SignTx(toi2, 1)
	require.NoError(t, err)
	txid2, err := c.BroadcastTx(toi2, raw2)
	require.NoError(t, err)
	items, err = c.evmScanner.blockMetaAccessor.GetSignedTxItems()
	require.NoError(t, err)
	require.Len(t, items, 1)
	env.bridge.heightErr = nil

	// observed outbound marks the signer cache for the inbound vault key
	inHash := ttypes.GetRandomTxHash()
	txIn := stypes.TxInItem{
		Tx:                  "abc123",
		Memo:                "OUT:" + inHash.String(),
		To:                  "0xdead",
		ObservedVaultPubKey: c.localPubKey,
	}
	c.OnObservedTxIn(txIn, 1)
	require.True(t, c.signerCacheManager.HasSigned(txIn.CacheHash(c.GetChain(), inHash.String())))
	obsTx, bcTx, err := c.GetLatestTxForVault(c.localPubKey.String())
	require.NoError(t, err)
	require.Equal(t, "abc123", obsTx)
	require.Equal(t, txid2, bcTx)

	// non-outbound and unparsable memos are ignored
	for _, memo := range []string{"not a memo", "SWAP:AVAX.AVAX:" + env.vaultAddr(t).String()} {
		txIn.Memo = memo
		c.OnObservedTxIn(txIn, 1)
		require.False(t, c.signerCacheManager.HasSigned(txIn.CacheHash(c.GetChain(), "")))
	}
}

func TestCovConfirmationCounts(t *testing.T) {
	c := &EVMClient{evmScanner: &EVMScanner{}}
	txIn := stypes.TxIn{
		TxArray:              []*stypes.TxInItem{{BlockHeight: 10}},
		ConfirmationRequired: 3,
	}

	c.cfg.ChainID = common.AVAXChain
	require.Equal(t, int64(0), c.GetConfirmationCount(txIn))
	require.True(t, c.ConfirmationCountReady(txIn))

	c.cfg.ChainID = common.BASEChain
	require.Equal(t, int64(12), c.GetConfirmationCount(txIn))
	require.True(t, c.ConfirmationCountReady(txIn))

	c.cfg.ChainID = common.BSCChain
	require.Equal(t, int64(3), c.GetConfirmationCount(txIn))
	c.evmScanner.updateCurrentBlockHeight(12)
	require.False(t, c.ConfirmationCountReady(txIn))
	c.evmScanner.updateCurrentBlockHeight(13)
	require.True(t, c.ConfirmationCountReady(txIn))
	require.True(t, c.ConfirmationCountReady(stypes.TxIn{}))

	c.cfg.ChainID = common.POLChain
	require.Equal(t, int64(polConfirmations), c.GetConfirmationCount(txIn))
	require.True(t, c.ConfirmationCountReady(txIn))
}

// ---------------------------------------------------------------------------
// solvency
// ---------------------------------------------------------------------------

func TestCovReportSolvency(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client
	queue := make(chan stypes.Solvency, 10)
	c.globalSolvencyQueue = queue

	// not a solvency height
	require.NoError(t, c.ReportSolvency(11))
	require.Equal(t, int64(0), c.lastSolvencyCheckHeight.Load())

	// unhealthy scanner at the current scan height is skipped
	c.evmScanner.updateCurrentBlockHeight(20)
	require.NoError(t, c.ReportSolvency(20))
	require.Equal(t, int64(0), c.lastSolvencyCheckHeight.Load())

	// asgard lookup failure
	env.bridge.asgardsErr = errors.New("asgard down")
	require.ErrorContains(t, c.ReportSolvency(30), "fail to get asgards")
	env.bridge.asgardsErr = nil

	// solvent vault (balance 1 AVAX >= 0.5 AVAX) while unhealthy -> reported solvent
	solvent := ttypes.Vault{
		PubKey: c.localPubKey,
		Coins:  common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(common.One/2))),
		Type:   ttypes.VaultType_AsgardVault,
		Status: ttypes.VaultStatus_ActiveVault,
	}
	other := ttypes.Vault{
		PubKey: ttypes.GetRandomPubKey(),
		Coins:  common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(1))),
	}
	env.bridge.asgards = ttypes.Vaults{solvent, other}
	require.NoError(t, c.ReportSolvency(30))
	require.Equal(t, int64(30), c.lastSolvencyCheckHeight.Load())
	require.Len(t, queue, 1)
	msg := <-queue
	require.Equal(t, c.localPubKey, msg.PubKey)
	require.Equal(t, int64(30), msg.Height)

	// insolvent vault (owes 100 AVAX) is reported even when only it is insolvent
	insolvent := solvent
	insolvent.Coins = common.NewCoins(common.NewCoin(common.AVAXAsset, cosmos.NewUint(100*common.One)))
	env.bridge.asgards = ttypes.Vaults{insolvent}
	require.NoError(t, c.ReportSolvency(40))
	require.Len(t, queue, 1)
	msg = <-queue
	require.Equal(t, int64(40), msg.Height)

	// account lookup failure skips the vault
	env.rpc.setError("eth_getTransactionCount", "nonce down")
	env.bridge.asgards = ttypes.Vaults{insolvent}
	require.NoError(t, c.ReportSolvency(50))
	require.Empty(t, queue)
	require.Equal(t, int64(50), c.lastSolvencyCheckHeight.Load())
}

// ---------------------------------------------------------------------------
// liability reconstruction
// ---------------------------------------------------------------------------

func TestCovReconstructVaultLiability(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client
	vault := env.vaultAddr(t).String()

	nonces := func(confirmed, pending string) {
		env.rpc.set("eth_getTransactionCount", func(params []json.RawMessage) (any, error) {
			if covParamString(params, 1) == "pending" {
				return pending, nil
			}
			return confirmed, nil
		})
	}

	// no pending txs
	nonces("0x2", "0x2")
	ready, err := c.reconstructVaultLiability(vault)
	require.NoError(t, err)
	require.True(t, ready)

	// confirmed nonce error
	env.rpc.setError("eth_getTransactionCount", "down")
	_, err = c.reconstructVaultLiability(vault)
	require.ErrorContains(t, err, "confirmed nonce")

	// pending nonce error
	env.rpc.set("eth_getTransactionCount", func(params []json.RawMessage) (any, error) {
		if covParamString(params, 1) == "pending" {
			return nil, errors.New("down")
		}
		return "0x0", nil
	})
	_, err = c.reconstructVaultLiability(vault)
	require.ErrorContains(t, err, "pending nonce")

	// pending block error
	nonces("0x2", "0x4")
	env.rpc.setError("eth_getBlockByNumber", "no block")
	_, err = c.reconstructVaultLiability(vault)
	require.ErrorContains(t, err, "pending block")

	// The node answers by block tag: the vault's pending txs are only in the
	// "pending" block; the latest mined block holds a stale and a foreign tx.
	vaultKey := c.kw.GetPrivKey()
	routerAddr := ecommon.HexToAddress(covRouter.String())
	ours := covSignTx(t, vaultKey, 2, &routerAddr, big.NewInt(1000), nil)
	ours2 := covSignTx(t, vaultKey, 3, &routerAddr, big.NewInt(1), nil)
	stale := covSignTx(t, vaultKey, 1, &routerAddr, big.NewInt(1000), nil)
	foreign := covSignTx(t, covExternalKey(t), 2, &routerAddr, big.NewInt(1000), nil)
	pendingBlock := covBlockJSON(t, covHeader(102, ecommon.Hash{}, true), []*etypes.Transaction{ours, ours2, stale, foreign})
	latestBlock := covBlockJSON(t, covHeader(101, ecommon.Hash{}, true), []*etypes.Transaction{stale, foreign})
	var requestedTag string
	env.rpc.set("eth_getBlockByNumber", func(params []json.RawMessage) (any, error) {
		requestedTag = covParamString(params, 0)
		if requestedTag == "pending" {
			return pendingBlock, nil
		}
		return latestBlock, nil
	})
	ready, err = c.reconstructVaultLiability(vault)
	require.NoError(t, err)
	// NOTE (pre-existing bug): reconstructVaultLiability calls
	// BlockByNumber(ctx, nil) expecting the pending block, but go-ethereum
	// sends "latest" for nil. The vault's pending txs are therefore not found,
	// no liability is reconstructed and the vault is not ready until they
	// confirm. Fixing it (requesting "pending") should flip these assertions
	// to ready with liabilities for nonces 2 and 3.
	require.Equal(t, "latest", requestedTag)
	require.False(t, ready)
	require.Equal(t, 0, c.getPendingLiability(vault, 0).Sign())
}

func TestCovReconstructLiabilities(t *testing.T) {
	// no vaults -> ready immediately
	env := newCovEnv(t, common.AVAXChain, nil)
	c := env.client
	c.pubkeyMgr = newCovPubKeyMgr()
	c.signingReady.Store(false)
	c.wg.Add(1)
	c.reconstructLiabilities()
	require.True(t, c.signingReady.Load())

	// one vault with nothing pending (plus an invalid pubkey that is skipped)
	pkm := newCovPubKeyMgr()
	pkm.add(c.localPubKey, covRouter)
	pkm.add(common.PubKey("bogus"), common.NoAddress)
	c.pubkeyMgr = pkm
	c.signingReady.Store(false)
	c.wg.Add(1)
	c.reconstructLiabilities()
	require.True(t, c.signingReady.Load())

	// stop requested while a vault is not yet ready -> never becomes ready
	env.rpc.setError("eth_getTransactionCount", "down")
	c.signingReady.Store(false)
	close(c.stopchan)
	c.wg.Add(1)
	done := make(chan struct{})
	go func() {
		c.reconstructLiabilities()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reconstructLiabilities did not exit after stop")
	}
	require.False(t, c.signingReady.Load())
}

func TestCovStartStop(t *testing.T) {
	env := newCovEnv(t, common.AVAXChain, func(cfg *config.BifrostChainConfiguration) {
		cfg.BlockScanner.BlockHeightDiscoverBackoff = time.Millisecond
		cfg.BlockScanner.BlockRetryInterval = time.Millisecond
	})
	c := env.client
	c.signingReady.Store(false)
	txQ := make(chan stypes.TxIn, 10)
	errQ := make(chan stypes.ErrataBlock, 10)
	solQ := make(chan stypes.Solvency, 10)
	feeQ := make(chan common.NetworkFee, 10)
	c.Start(txQ, errQ, solQ, feeQ)
	require.Eventually(t, c.signingReady.Load, 5*time.Second, 5*time.Millisecond)
	c.Stop()
	require.Equal(t, (chan<- stypes.ErrataBlock)(errQ), c.evmScanner.globalErrataQueue)
	require.Equal(t, (chan<- common.NetworkFee)(feeQ), c.evmScanner.globalNetworkFeeQueue)
}
