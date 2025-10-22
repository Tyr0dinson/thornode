package tron

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	_ "embed"

	"cosmossdk.io/math"
	ethabi "github.com/ethereum/go-ethereum/accounts/abi"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/tron/api"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/tokenlist"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// TRC20 ABI
////////////////////////////////////////////////////////////////////////////////////////

//go:embed abi/trc20.json
var trc20ABIJson string

var trc20ABI ethabi.ABI

func init() {
	var err error
	trc20ABI, err = ethabi.JSON(strings.NewReader(trc20ABIJson))
	if err != nil {
		panic(fmt.Errorf("failed to parse TRC20 ABI: %w", err))
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// ABI
////////////////////////////////////////////////////////////////////////////////////////

// TRC20ABI returns the parsed TRC20 ABI
func TRC20ABI() ethabi.ABI {
	return trc20ABI
}

////////////////////////////////////////////////////////////////////////////////////////
// Tokens
////////////////////////////////////////////////////////////////////////////////////////

// Tokens returns the list of TRC20 tokens that are used in the simulation.
func Tokens(chain common.Chain) map[common.Asset]tokenlist.ERC20Token {
	tokenMap := make(map[common.Asset]tokenlist.ERC20Token)
	tokens := tokenlist.GetEVMTokenList(chain).Tokens

	for _, token := range tokens {
		tokenMap[token.Asset(chain)] = token
	}

	return tokenMap
}

////////////////////////////////////////////////////////////////////////////////////////
// Tron
////////////////////////////////////////////////////////////////////////////////////////

type Client struct {
	chain   common.Chain
	api     *api.TronApi
	keys    *thorclient.Keys
	privKey *ecdsa.PrivateKey
	address common.Address
}

var _ LiteChainClient = &Client{}

func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

func NewClient(
	chain common.Chain,
	host string,
	keys *thorclient.Keys,
) (LiteChainClient, error) {
	// extract the private key
	privKey, err := keys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}

	// derive the public key
	pk, err := cryptocodec.ToCmtPubKeyInterface(privKey.PubKey())
	if err != nil {
		return nil, fmt.Errorf("fail to get tm pub key: %w", err)
	}
	pubKey, err := common.NewPubKeyFromCrypto(pk)
	if err != nil {
		return nil, fmt.Errorf("fail to get pub key: %w", err)
	}

	// get pubkey address for the chain
	address, err := pubKey.GetAddress(chain)
	if err != nil {
		return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
	}

	client := Client{
		chain:   chain,
		api:     api.NewTronApi(host, time.Second*2),
		keys:    keys,
		address: address,
	}

	priv, err := keys.GetPrivateKey()
	if err != nil {
		return nil, err
	}

	client.privKey, err = evm.GetPrivateKey(priv)
	if err != nil {
		return nil, err
	}

	return &client, nil
}

func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	address := c.address

	if pk != nil {
		var err error
		address, err = pk.GetAddress(c.chain)
		if err != nil {
			return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
		}
	}

	// Get TRX balance
	balance, err := c.api.GetBalance(address.String())
	if err != nil {
		return nil, err
	}

	amount := math.NewUint(balance).Mul(math.NewUint(100)) // 1e6 -> 1e8
	coins := common.Coins{
		common.NewCoin(common.TRXAsset, amount),
	}

	// Get TRC20 token balances
	for asset, token := range Tokens(c.chain) {
		tokenBalance, err := c.getTokenBalance(address.String(), token.Address)
		if err != nil {
			// Skip tokens that fail to query
			continue
		}

		// Convert from token decimals to 1e8
		tokenBalance.Mul(tokenBalance, big.NewInt(common.One))
		tokenBalance.Div(tokenBalance, big.NewInt(1).Exp(big.NewInt(10), big.NewInt(int64(token.Decimals)), nil))

		coins = append(coins, common.NewCoin(asset, math.NewUintFromBigInt(tokenBalance)))
	}

	return &common.Account{
		Coins: coins,
	}, nil
}

// getTokenBalance queries the TRC20 token balance for an address
func (c *Client) getTokenBalance(address, contract string) (*big.Int, error) {
	// Convert addresses to proper format for ABI encoding
	addresses := []string{address, contract}
	for i, addr := range addresses {
		tmp, err := api.ConvertAddress(addr)
		if err != nil {
			return nil, fmt.Errorf("failed to convert address: %w", err)
		}
		// Convert to EVM format (replace 41 with 0x)
		addresses[i] = strings.Replace(tmp, "41", "0x", 1)
	}

	// Pack the balanceOf call
	data, err := trc20ABI.Pack("balanceOf", ethcommon.HexToAddress(addresses[0]))
	if err != nil {
		return nil, fmt.Errorf("failed to pack balanceOf: %w", err)
	}

	// Call the contract
	input := hex.EncodeToString(data)
	tx, err := c.api.TriggerSmartContract(address, contract, "balanceOf(address)", input, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to trigger smart contract: %w", err)
	}

	// Parse the response
	if len(tx.RawData.Data) == 0 {
		return big.NewInt(0), nil
	}

	// Decode the result
	result, err := hex.DecodeString(tx.RawData.Data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode result: %w", err)
	}

	// Unpack the balance
	unpacked, err := trc20ABI.Unpack("balanceOf", result)
	if err != nil {
		// If unpacking fails, try to parse as raw big.Int
		balance := new(big.Int)
		balance.SetBytes(result)
		return balance, nil
	}

	balance, ok := unpacked[0].(*big.Int)
	if !ok {
		return nil, fmt.Errorf("failed to convert balance to *big.Int")
	}

	return balance, nil
}

func (c *Client) SignContractTx(tx SimContractTx) ([]byte, error) {
	// For TRON TRC20 transfers, we need to use TriggerSmartContract
	// Check if this is a TRC20 transfer
	if tx.Method != "transfer" {
		return nil, fmt.Errorf("only 'transfer' method is supported for TRC20, got: %s", tx.Method)
	}

	// Expect args to be [address, amount]
	if len(tx.Args) != 2 {
		return nil, fmt.Errorf("expected 2 args for transfer, got: %d", len(tx.Args))
	}

	// Pack the transfer call using the provided ABI
	data, err := tx.ABI.Pack(tx.Method, tx.Args...)
	if err != nil {
		return nil, fmt.Errorf("failed to pack transfer call: %w", err)
	}

	// Create the TriggerSmartContract transaction
	input := hex.EncodeToString(data)
	feeLimit := uint64(100_000_000) // 100 TRX fee limit for TRC20 transfers

	tronTx, err := c.api.TriggerSmartContract(
		c.address.String(),
		tx.Contract.String(),
		"transfer(address,uint256)",
		input,
		feeLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create TRC20 transfer tx: %w", err)
	}

	// Sign the transaction
	hash, err := hex.DecodeString(tronTx.TxId)
	if err != nil {
		return nil, fmt.Errorf("failed to decode tx hash: %w", err)
	}

	signature, err := crypto.Sign(hash, c.privKey)
	if err != nil {
		return nil, fmt.Errorf("failed to sign tx: %w", err)
	}

	tronTx.Signature = append(tronTx.Signature, hex.EncodeToString(signature))

	// Marshal and return
	txBytes, err := json.Marshal(tronTx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tx: %w", err)
	}

	return txBytes, nil
}

func (c *Client) SignTx(tx SimTx) ([]byte, error) {
	tronTx, err := c.api.CreateTransaction(
		c.address.String(),
		tx.ToAddress.String(),
		tx.Coin.Amount.Quo(math.NewUint(100)).Uint64(),
		tx.Memo,
	)
	if err != nil {
		return nil, err
	}

	hash, err := hex.DecodeString(tronTx.TxId)
	if err != nil {
		return nil, err
	}

	signature, err := crypto.Sign(hash, c.privKey)
	if err != nil {
		return nil, err
	}

	tronTx.Signature = append(tronTx.Signature, hex.EncodeToString(signature))

	txBytes, err := json.Marshal(tronTx)
	if err != nil {
		return nil, err
	}

	return txBytes, nil
}

func (c *Client) BroadcastTx(signed []byte) (string, error) {
	response, err := c.api.BroadcastTransaction(signed)
	if err != nil {
		return "", err
	}

	return response.TxId, nil
}
