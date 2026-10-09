package evm

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"
)

const (
	tokenACov = "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48" // nolint gosec:G101 not a hardcoded credential
	tokenBCov = "0xB0b86991c6218b36c1d19D4a2e9Eb0cE3606eB49" // nolint gosec:G101 not a hardcoded credential
	vaultCov  = "0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473"
	routerCov = "0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44"
)

var whitelistCov = []tokenlist.ERC20Token{
	{Address: tokenACov, Symbol: "USDC"},
	{Address: tokenBCov, Symbol: "TKN"},
}

// erc20 ABI variants used to exercise the decimals type switch and pack errors
const (
	erc20BigDecimalsABICov = `[
	{"constant":true,"inputs":[],"name":"decimals","outputs":[{"name":"","type":"uint256"}],"type":"function"},
	{"constant":true,"inputs":[],"name":"symbol","outputs":[{"name":"","type":"string"}],"type":"function"}]`
	erc20StringDecimalsABICov = `[
	{"constant":true,"inputs":[],"name":"decimals","outputs":[{"name":"","type":"string"}],"type":"function"},
	{"constant":true,"inputs":[],"name":"symbol","outputs":[{"name":"","type":"string"}],"type":"function"}]`
)

func memDBCov(t *testing.T) *leveldb.DB {
	t.Helper()
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func callInputCov(p []json.RawMessage) []byte {
	var arg struct {
		Input hexutil.Bytes `json:"input"`
	}
	_ = json.Unmarshal(p[0], &arg)
	return arg.Input
}

// erc20CallHandlerCov returns an eth_call handler answering symbol/decimals
// with the given raw outputs (nil means return an RPC error).
func erc20CallHandlerCov(t *testing.T, symbolOut, decimalsOut []byte) rpcHandlerCov {
	t.Helper()
	_, erc20ABI, err := GetContractABI(routerContractABI, erc20ContractABI)
	require.NoError(t, err)
	symSel := erc20ABI.Methods[symbolMethod].ID
	decSel := erc20ABI.Methods[decimalMethod].ID
	return func(p []json.RawMessage) (any, error) {
		in := callInputCov(p)
		switch {
		case bytes.HasPrefix(in, symSel):
			if symbolOut == nil {
				return nil, errors.New("symbol reverted")
			}
			return hexutil.Bytes(symbolOut), nil
		case bytes.HasPrefix(in, decSel):
			if decimalsOut == nil {
				return nil, errors.New("decimals reverted")
			}
			return hexutil.Bytes(decimalsOut), nil
		}
		return nil, errors.New("unexpected call")
	}
}

func packOutCov(t *testing.T, a *abi.ABI, method string, v ...any) []byte {
	t.Helper()
	out, err := a.Methods[method].Outputs.Pack(v...)
	require.NoError(t, err)
	return out
}

func newTokenManagerCov(t *testing.T, client *ethclient.Client, db *leveldb.DB, routerABI, erc20ABI string) *TokenManager {
	t.Helper()
	tm, err := NewTokenManager(db, "cov-tokenmeta-", common.ETHAsset, 18, time.Second, whitelistCov, client, routerABI, erc20ABI)
	require.NoError(t, err)
	return tm
}

func TestNewTokenManagerErrorsCov(t *testing.T) {
	_, client := newFakeEthRPCCov(t)
	db := memDBCov(t)

	_, err := NewTokenManager(db, "p", common.ETHAsset, 18, time.Second, nil, client, "bad", erc20ContractABI)
	require.ErrorContains(t, err, "fail to create contract abi")

	_, err = NewTokenManager(db, "p", common.ETHAsset, 18, time.Second, nil, nil, routerContractABI, erc20ContractABI)
	require.EqualError(t, err, "ETH client is nil")

	var nilTM *TokenManager
	require.False(t, nilTM.IsRelevantToken(tokenACov))
}

func TestTokenManagerGetTokenMetaCov(t *testing.T) {
	f, client := newFakeEthRPCCov(t)
	_, erc20ABI, err := GetContractABI(routerContractABI, erc20ContractABI)
	require.NoError(t, err)

	tm := newTokenManagerCov(t, client, memDBCov(t), routerContractABI, erc20ContractABI)
	f.set("eth_call", erc20CallHandlerCov(t,
		packOutCov(t, erc20ABI, symbolMethod, "US.D-C"),
		packOutCov(t, erc20ABI, decimalMethod, uint8(6))))

	meta, err := tm.GetTokenMeta(tokenACov)
	require.NoError(t, err)
	require.Equal(t, "USDC", meta.Symbol) // sanitised
	require.Equal(t, uint64(6), meta.Decimal)
	calls := f.count("eth_call")
	require.Equal(t, 2, calls)

	// cached in the db, no further contract calls
	meta, err = tm.GetTokenMeta(tokenACov)
	require.NoError(t, err)
	require.Equal(t, uint64(6), meta.Decimal)
	require.Equal(t, calls, f.count("eth_call"))

	tokens, err := tm.GetTokens()
	require.NoError(t, err)
	require.Len(t, tokens, 1)

	// asset / decimals / amount helpers with a 6-decimal token
	asset, err := tm.GetAssetFromTokenAddress(tokenACov)
	require.NoError(t, err)
	require.Equal(t, "ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", asset.String())
	require.Equal(t, int64(6), tm.GetTokenDecimalsForTHORChain(tokenACov))
	// 1 USDC (1e6) -> 1e8 in thorchain units
	require.Equal(t, cosmos.NewUint(100_000_000), tm.ConvertAmount(tokenACov, big.NewInt(1_000_000)))
	// 1e8 thorchain units -> 1e6 token units
	require.Equal(t, big.NewInt(1_000_000), tm.ConvertSigningAmount(big.NewInt(100_000_000), tokenACov))

	// native helpers
	native, err := tm.GetAssetFromTokenAddress(NativeTokenAddr)
	require.NoError(t, err)
	require.Equal(t, common.ETHAsset, native)
	require.Equal(t, int64(0), tm.GetTokenDecimalsForTHORChain(NativeTokenAddr))
	sym, err := tm.getSymbol(NativeTokenAddr)
	require.NoError(t, err)
	require.Equal(t, "ETH", sym)
	dec, err := tm.getDecimals(NativeTokenAddr)
	require.NoError(t, err)
	require.Equal(t, uint64(18), dec)

	// non-whitelisted token: helpers fall back to defaults
	other := "0xdAC17F958D2ee523a2206206994597C13D831ec7"
	_, err = tm.GetAssetFromTokenAddress(other)
	require.ErrorContains(t, err, "is not whitelisted")
	require.Equal(t, int64(0), tm.GetTokenDecimalsForTHORChain(other))
	require.Equal(t, cosmos.NewUint(1), tm.ConvertAmount(other, big.NewInt(10_000_000_000)))
	require.Equal(t, big.NewInt(10_000_000_000), tm.ConvertSigningAmount(big.NewInt(1), other))
}

func TestTokenManagerSymbolAndDecimalFailuresCov(t *testing.T) {
	f, client := newFakeEthRPCCov(t)
	_, erc20ABI, err := GetContractABI(routerContractABI, erc20ContractABI)
	require.NoError(t, err)

	// symbol call fails -> GetTokenMeta fails and nothing is cached
	tm := newTokenManagerCov(t, client, memDBCov(t), routerContractABI, erc20ContractABI)
	f.set("eth_call", erc20CallHandlerCov(t, nil, nil))
	_, err = tm.GetTokenMeta(tokenACov)
	require.ErrorContains(t, err, "fail to get symbol")
	tokens, err := tm.GetTokens()
	require.NoError(t, err)
	require.Empty(t, tokens)

	// decimals call fails -> default decimals are used
	f.set("eth_call", erc20CallHandlerCov(t, packOutCov(t, erc20ABI, symbolMethod, "TKN"), nil))
	meta, err := tm.GetTokenMeta(tokenBCov)
	require.NoError(t, err)
	require.Equal(t, "TKN", meta.Symbol)
	require.Equal(t, uint64(18), meta.Decimal)

	// decimals unpack fails (empty output) -> default decimals
	tm = newTokenManagerCov(t, client, memDBCov(t), routerContractABI, erc20ContractABI)
	f.set("eth_call", erc20CallHandlerCov(t, packOutCov(t, erc20ABI, symbolMethod, "TKN"), []byte{}))
	dec, err := tm.getDecimals(tokenACov)
	require.ErrorContains(t, err, "fail to unpack decimal")
	require.Equal(t, uint64(18), dec)

	// symbol output that is not ABI encoded is used raw (sanitised)
	f.set("eth_call", erc20CallHandlerCov(t, []byte("A.B"), nil))
	sym, err := tm.getSymbol(tokenACov)
	require.NoError(t, err)
	require.Equal(t, "AB", sym)
}

func TestTokenManagerDecimalTypesCov(t *testing.T) {
	f, client := newFakeEthRPCCov(t)

	// an ABI declaring decimals() as uint256 decodes to *big.Int, whose value
	// is used as-is
	bigABI, err := abi.JSON(bytes.NewBufferString(erc20BigDecimalsABICov))
	require.NoError(t, err)
	tm := newTokenManagerCov(t, client, memDBCov(t), routerContractABI, erc20BigDecimalsABICov)
	bigDecimals := big.NewInt(9)
	f.set("eth_call", func(p []json.RawMessage) (any, error) {
		return hexutil.Bytes(packOutCov(t, &bigABI, decimalMethod, bigDecimals)), nil
	})
	dec, err := tm.getDecimals(tokenACov)
	require.NoError(t, err)
	require.Equal(t, uint64(9), dec)

	// a uint256 value beyond uint64 is rejected rather than truncated
	bigDecimals = new(big.Int).Lsh(big.NewInt(1), 64)
	dec, err = tm.getDecimals(tokenACov)
	require.ErrorContains(t, err, "invalid decimals")
	require.Equal(t, uint64(18), dec)

	// string decimals -> unsupported type
	strABI, err := abi.JSON(bytes.NewBufferString(erc20StringDecimalsABICov))
	require.NoError(t, err)
	tm = newTokenManagerCov(t, client, memDBCov(t), routerContractABI, erc20StringDecimalsABICov)
	f.set("eth_call", func(p []json.RawMessage) (any, error) {
		return hexutil.Bytes(packOutCov(t, &strABI, decimalMethod, "x")), nil
	})
	dec, err = tm.getDecimals(tokenACov)
	require.ErrorContains(t, err, "fail to parse it")
	require.Equal(t, uint64(18), dec)

	// ABI without symbol/decimals -> pack errors
	tm = newTokenManagerCov(t, client, memDBCov(t), routerContractABI, "[]")
	dec, err = tm.getDecimals(tokenACov)
	require.ErrorContains(t, err, "fail to pack decimal method")
	require.Equal(t, uint64(18), dec)
	sym, err := tm.getSymbol(tokenACov)
	require.NoError(t, err) // current behaviour: pack error is swallowed
	require.Empty(t, sym)
}

func TestTokenManagerDBErrorsCov(t *testing.T) {
	_, client := newFakeEthRPCCov(t)

	// closed db -> lookup errors
	db, err := leveldb.Open(storage.NewMemStorage(), nil)
	require.NoError(t, err)
	tm := newTokenManagerCov(t, client, db, routerContractABI, erc20ContractABI)
	require.NoError(t, db.Close())
	_, err = tm.GetTokenMeta(tokenACov)
	require.ErrorContains(t, err, "fail to get token meta")
	// whitelisted tokens are relevant without touching the db
	require.True(t, tm.IsRelevantToken(tokenACov))
	require.True(t, tm.IsRelevantToken(NativeTokenAddr))
	// unknown token with a broken db is not relevant
	require.False(t, tm.IsRelevantToken("0xdAC17F958D2ee523a2206206994597C13D831ec7"))
	require.False(t, IsRelevantToken("0xdAC17F958D2ee523a2206206994597C13D831ec7", nil, nil, log.Logger))

	// read-only db -> saving fetched metadata fails
	f, client2 := newFakeEthRPCCov(t)
	_, erc20ABI, err := GetContractABI(routerContractABI, erc20ContractABI)
	require.NoError(t, err)
	stor := storage.NewMemStorage()
	rwDB, err := leveldb.Open(stor, nil)
	require.NoError(t, err)
	require.NoError(t, rwDB.Close())
	roDB, err := leveldb.Open(stor, &opt.Options{ReadOnly: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = roDB.Close() })
	tm = newTokenManagerCov(t, client2, roDB, routerContractABI, erc20ContractABI)
	f.set("eth_call", erc20CallHandlerCov(t,
		packOutCov(t, erc20ABI, symbolMethod, "TKN"),
		packOutCov(t, erc20ABI, decimalMethod, uint8(18))))
	_, err = tm.GetTokenMeta(tokenACov)
	require.ErrorContains(t, err, "fail to save token meta")
}

func TestTokenManagerGetBalanceCov(t *testing.T) {
	f, client := newFakeEthRPCCov(t)
	vaultABI, _, err := GetContractABI(routerContractABI, erc20ContractABI)
	require.NoError(t, err)
	tm := newTokenManagerCov(t, client, memDBCov(t), routerContractABI, erc20ContractABI)

	// native balance
	f.set("eth_getBalance", constCov("0x64"))
	bal, err := tm.GetBalance(vaultCov, NativeTokenAddr, nil, routerCov)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(100), bal)

	// token balance via router vaultAllowance
	var gotTo string
	f.set("eth_call", func(p []json.RawMessage) (any, error) {
		var arg struct {
			To    string        `json:"to"`
			Input hexutil.Bytes `json:"input"`
		}
		_ = json.Unmarshal(p[0], &arg)
		gotTo = arg.To
		if !bytes.HasPrefix(arg.Input, vaultABI.Methods["vaultAllowance"].ID) {
			return nil, errors.New("unexpected method")
		}
		return hexutil.Bytes(packOutCov(t, vaultABI, "vaultAllowance", big.NewInt(12345))), nil
	})
	bal, err = tm.GetBalance(vaultCov, tokenACov, big.NewInt(5), routerCov)
	require.NoError(t, err)
	require.Equal(t, big.NewInt(12345), bal)
	require.Equal(t, "0xe65e9d372f8cacc7b6dfcd4af6507851ed31bb44", gotTo)

	// call error
	f.set("eth_call", errCov("reverted"))
	_, err = tm.GetBalance(vaultCov, tokenACov, nil, routerCov)
	require.ErrorContains(t, err, "reverted")

	// unpack error
	f.set("eth_call", constCov("0x"))
	_, err = tm.GetBalance(vaultCov, tokenACov, nil, routerCov)
	require.Error(t, err)

	// router ABI without vaultAllowance -> pack error
	tm = newTokenManagerCov(t, client, memDBCov(t), "[]", erc20ContractABI)
	_, err = tm.GetBalance(vaultCov, tokenACov, nil, routerCov)
	require.ErrorContains(t, err, "fail to create vaultAllowance data")
}
