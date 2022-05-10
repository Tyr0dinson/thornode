package evm

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	ecommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/syndtr/goleveldb/leveldb"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/shared/evm/types"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

const (
	decimalMethod     = "decimals"
	symbolMethod      = "symbol"
	routerContractABI = "[{\"inputs\":[{\"internalType\":\"address\",\"name\":\"rune\",\"type\":\"address\"}],\"stateMutability\":\"nonpayable\",\"type\":\"constructor\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"to\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"asset\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"memo\",\"type\":\"string\"}],\"name\":\"Deposit\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"oldVault\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newVault\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"address\",\"name\":\"asset\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"memo\",\"type\":\"string\"}],\"name\":\"TransferAllowance\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"vault\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"to\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"address\",\"name\":\"asset\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"memo\",\"type\":\"string\"}],\"name\":\"TransferOut\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"oldVault\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"newVault\",\"type\":\"address\"},{\"components\":[{\"internalType\":\"address\",\"name\":\"asset\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"indexed\":false,\"internalType\":\"struct Router.Coin[]\",\"name\":\"coins\",\"type\":\"tuple[]\"},{\"indexed\":false,\"internalType\":\"string\",\"name\":\"memo\",\"type\":\"string\"}],\"name\":\"VaultTransfer\",\"type\":\"event\"},{\"inputs\":[],\"name\":\"RUNE\",\"outputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address[]\",\"name\":\"recipients\",\"type\":\"address[]\"},{\"components\":[{\"internalType\":\"address\",\"name\":\"asset\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"internalType\":\"struct Router.Coin[]\",\"name\":\"coins\",\"type\":\"tuple[]\"},{\"internalType\":\"string[]\",\"name\":\"memos\",\"type\":\"string[]\"}],\"name\":\"batchTransferOut\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address payable\",\"name\":\"vault\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"asset\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"memo\",\"type\":\"string\"}],\"name\":\"deposit\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"router\",\"type\":\"address\"},{\"internalType\":\"address payable\",\"name\":\"asgard\",\"type\":\"address\"},{\"components\":[{\"internalType\":\"address\",\"name\":\"asset\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"}],\"internalType\":\"struct Router.Coin[]\",\"name\":\"coins\",\"type\":\"tuple[]\"},{\"internalType\":\"string\",\"name\":\"memo\",\"type\":\"string\"}],\"name\":\"returnVaultAssets\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"router\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"newVault\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"asset\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"memo\",\"type\":\"string\"}],\"name\":\"transferAllowance\",\"outputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address payable\",\"name\":\"to\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"asset\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"amount\",\"type\":\"uint256\"},{\"internalType\":\"string\",\"name\":\"memo\",\"type\":\"string\"}],\"name\":\"transferOut\",\"outputs\":[],\"stateMutability\":\"payable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"\",\"type\":\"address\"}],\"name\":\"vaultAllowance\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"}]"
	erc20ContractABI  = "[{\"inputs\":[],\"stateMutability\":\"nonpayable\",\"type\":\"constructor\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"tokenOwner\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"spender\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"tokens\",\"type\":\"uint256\"}],\"name\":\"Approval\",\"type\":\"event\"},{\"anonymous\":false,\"inputs\":[{\"indexed\":true,\"internalType\":\"address\",\"name\":\"from\",\"type\":\"address\"},{\"indexed\":true,\"internalType\":\"address\",\"name\":\"to\",\"type\":\"address\"},{\"indexed\":false,\"internalType\":\"uint256\",\"name\":\"tokens\",\"type\":\"uint256\"}],\"name\":\"Transfer\",\"type\":\"event\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"delegate\",\"type\":\"address\"}],\"name\":\"allowance\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"delegate\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"numTokens\",\"type\":\"uint256\"}],\"name\":\"approve\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"tokenOwner\",\"type\":\"address\"}],\"name\":\"balanceOf\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"decimals\",\"outputs\":[{\"internalType\":\"uint8\",\"name\":\"\",\"type\":\"uint8\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"name\",\"outputs\":[{\"internalType\":\"string\",\"name\":\"\",\"type\":\"string\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"symbol\",\"outputs\":[{\"internalType\":\"string\",\"name\":\"\",\"type\":\"string\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[],\"name\":\"totalSupply\",\"outputs\":[{\"internalType\":\"uint256\",\"name\":\"\",\"type\":\"uint256\"}],\"stateMutability\":\"view\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"receiver\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"numTokens\",\"type\":\"uint256\"}],\"name\":\"transfer\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"},{\"inputs\":[{\"internalType\":\"address\",\"name\":\"owner\",\"type\":\"address\"},{\"internalType\":\"address\",\"name\":\"buyer\",\"type\":\"address\"},{\"internalType\":\"uint256\",\"name\":\"numTokens\",\"type\":\"uint256\"}],\"name\":\"transferFrom\",\"outputs\":[{\"internalType\":\"bool\",\"name\":\"\",\"type\":\"bool\"}],\"stateMutability\":\"nonpayable\",\"type\":\"function\"}]"
)

type (
	symbolFetcher   func(token string) (string, error)
	decimalsFetcher func(token string) (uint64, error)
)

// TokensHelper is a wrapper around a EVM-based token LevelDB with other helper functions
type TokensHelper struct {
	tokenDb         *LevelDBTokenMeta
	defaultDecimals uint64
	nativeAsset     common.Asset
	requestTimeout  time.Duration
	tokenWhitelist  []ERC20Token
	erc20ABI        *abi.ABI
	client          *ethclient.Client
	logger          zerolog.Logger
}

// NewTokensHelper returns an instance of TokensHelper
func NewTokensHelper(db *leveldb.DB,
	prefixTokenMeta string,
	nativeAsset common.Asset,
	defaultDecimals uint64,
	requestTimeout time.Duration,
	tokenWhitelist []ERC20Token,
	rpcHost string) (*TokensHelper, error) {

	tokenDb, _ := NewLevelDBTokenMeta(db, prefixTokenMeta)
	_, erc20ABI, err := GetContractABI(routerContractABI, erc20ContractABI)

	if err != nil {
		return nil, fmt.Errorf("fail to create contract abi: %w", err)
	}

	client, err := ethclient.Dial(rpcHost)
	if err != nil {
		return nil, fmt.Errorf("fail to dial %s rpc host(%s): %w", nativeAsset.Symbol, rpcHost, err)
	}

	return &TokensHelper{
		tokenDb:         tokenDb,
		defaultDecimals: defaultDecimals,
		nativeAsset:     nativeAsset,
		erc20ABI:        erc20ABI,
		tokenWhitelist:  tokenWhitelist,
		client:          client,
		requestTimeout:  requestTimeout,
		logger:          log.Logger.With().Str("module", "tokens_helper").Str("chain", nativeAsset.Chain.String()).Logger(),
	}, nil
}

func (h *TokensHelper) getContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), h.requestTimeout)
}

// GetTokens return all the token meta data
func (h *TokensHelper) GetTokens() ([]*types.TokenMeta, error) {
	return h.tokenDb.GetTokens()
}

func (h *TokensHelper) GetTokenMeta(token string) (types.TokenMeta, error) {
	tokenMeta, err := h.tokenDb.GetTokenMeta(token)
	if err != nil {
		return types.TokenMeta{}, fmt.Errorf("fail to get token meta: %w", err)
	}
	if tokenMeta.IsEmpty() {
		isWhiteListToken := false
		for _, item := range h.tokenWhitelist {
			if strings.EqualFold(item.Address, token) {
				isWhiteListToken = true
				break
			}
		}
		if !isWhiteListToken {
			return types.TokenMeta{}, fmt.Errorf("token: %s is not whitelisted", token)
		}
		symbol, err := h.getSymbol(token)
		if err != nil {
			return types.TokenMeta{}, fmt.Errorf("fail to get symbol: %w", err)
		}
		decimals, err := h.getDecimals(token)
		if err != nil {
			h.logger.Err(err).Msgf("fail to get decimals from smart contract, default to: %d", h.defaultDecimals)
		}
		// e.logger.Info().Msgf("token:%s, decimals: %d", token, decimals)
		tokenMeta = types.NewTokenMeta(symbol, token, decimals)
		if err = h.tokenDb.SaveTokenMeta(symbol, token, decimals); err != nil {
			return types.TokenMeta{}, fmt.Errorf("fail to save token meta: %w", err)
		}
	}
	return tokenMeta, nil
}

func (h *TokensHelper) SaveTokenMeta(symbol, address string, decimals uint64) error {
	return h.tokenDb.SaveTokenMeta(symbol, address, decimals)
}

// IsNative returns true if the token address equals the native token address
func IsNative(token string) bool {
	return strings.EqualFold(token, nativeToken)
}

func (h *TokensHelper) GetAssetFromTokenAddress(token string) (common.Asset, error) {
	if IsNative(token) {
		return h.nativeAsset, nil
	}
	tokenMeta, err := h.GetTokenMeta(token)
	if err != nil {
		return common.EmptyAsset, fmt.Errorf("fail to get token meta: %w", err)
	}
	if tokenMeta.IsEmpty() {
		return common.EmptyAsset, fmt.Errorf("token metadata is empty")
	}
	return common.NewAsset(fmt.Sprintf("%s.%s-%s", h.nativeAsset.Symbol, tokenMeta.Symbol, strings.ToUpper(tokenMeta.Address)))
}

// convertAmount will convert the amount to 1e8 , the decimals used by THORChain
func (h *TokensHelper) ConvertAmount(token string, amt *big.Int) cosmos.Uint {
	if IsNative(token) {
		return cosmos.NewUintFromBigInt(amt).QuoUint64(common.One * 100)
	}
	decimals := uint64(h.defaultDecimals)
	tokenMeta, err := h.GetTokenMeta(token)
	if err != nil {
		h.logger.Err(err).Msgf("fail to get token meta for token address: %s", token)
	}
	if !tokenMeta.IsEmpty() {
		decimals = tokenMeta.Decimal
	}
	if decimals != h.defaultDecimals {
		var value big.Int
		amt = amt.Mul(amt, value.Exp(big.NewInt(10), big.NewInt(int64(h.defaultDecimals)), nil))
		amt = amt.Div(amt, value.Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	}
	return cosmos.NewUintFromBigInt(amt).QuoUint64(common.One * 100)
}

// return value 0 means use the default value which is common.THORChainDecimals, use 1e8 as precision
func (h *TokensHelper) GetTokenDecimalsForTHORChain(token string) int64 {
	if IsNative(token) {
		return 0
	}
	tokenMeta, err := h.GetTokenMeta(token)
	if err != nil {
		// e.logger.Err(err).Msgf("fail to get token meta for token address: %s", token)
	}
	if tokenMeta.IsEmpty() {
		return 0
	}
	// when the token's precision is more than THORChain , that's fine , just use THORChainDecimals
	if tokenMeta.Decimal >= common.THORChainDecimals {
		return 0
	}
	return int64(tokenMeta.Decimal)
}

// getDecimals calls the token's contract and retrieves its decimals
func (h *TokensHelper) getDecimals(token string) (uint64, error) {
	if IsNative(token) {
		return h.defaultDecimals, nil
	}
	to := ecommon.HexToAddress(token)
	input, err := h.erc20ABI.Pack(decimalMethod)
	if err != nil {
		return h.defaultDecimals, fmt.Errorf("fail to pack decimal method: %w", err)
	}
	ctx, cancel := h.getContext()
	defer cancel()
	res, err := h.client.CallContract(ctx, ethereum.CallMsg{
		To:   &to,
		Data: input,
	}, nil)
	if err != nil {
		return h.defaultDecimals, fmt.Errorf("fail to call smart contract get decimals: %w", err)
	}
	output, err := h.erc20ABI.Unpack(decimalMethod, res)
	if err != nil {
		return h.defaultDecimals, fmt.Errorf("fail to unpack decimal method call result: %w", err)
	}
	switch output[0].(type) {
	case uint8:
		decimals, ok := abi.ConvertType(output[0], new(uint8)).(*uint8)
		if !ok {
			return h.defaultDecimals, fmt.Errorf("dev error: fail to cast uint8")
		}
		return uint64(*decimals), nil
	case *big.Int:
		decimals, ok := abi.ConvertType(output[0], new(*big.Int)).(*big.Int)
		if !ok {
			return h.defaultDecimals, fmt.Errorf("dev error: fail to cast big.Int")
		}
		return decimals.Uint64(), nil
	}
	return h.defaultDecimals, fmt.Errorf("%s is %T fail to parse it", output[0], output[0])
}

// replace the . in symbol to *, and replace the - in symbol to #
// because . and - had been reserved to use in THORChain symbol
var symbolReplacer = strings.NewReplacer(".", "*", "-", "#", `\u0000`, "", "\u0000", "")

func sanitiseSymbol(symbol string) string {
	return symbolReplacer.Replace(symbol)
}

// getSymbol calls the token's contract and retrieves its symbol
func (h *TokensHelper) getSymbol(token string) (string, error) {
	if IsNative(token) {
		return h.nativeAsset.Symbol.String(), nil
	}
	to := ecommon.HexToAddress(token)
	input, err := h.erc20ABI.Pack(symbolMethod)
	if err != nil {
		return "", nil
	}
	ctx, cancel := h.getContext()
	defer cancel()
	res, err := h.client.CallContract(ctx, ethereum.CallMsg{
		To:   &to,
		Data: input,
	}, nil)
	if err != nil {
		return "", fmt.Errorf("fail to call to smart contract and get symbol: %w", err)
	}
	var symbol string
	output, err := h.erc20ABI.Unpack(symbolMethod, res)
	if err != nil {
		symbol = string(res)
		h.logger.Err(err).Msgf("fail to unpack symbol method call,token address: %s , symbol: %s", token, symbol)
		return sanitiseSymbol(symbol), nil
	}
	// nolint
	symbol = *abi.ConvertType(output[0], new(string)).(*string)
	return sanitiseSymbol(symbol), nil
}
