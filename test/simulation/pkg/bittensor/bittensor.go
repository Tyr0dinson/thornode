package bittensor

import (
	"crypto/ed25519"
	"fmt"
	"time"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"golang.org/x/crypto/blake2b"

	taorpc "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/bittensor/rpc"
	taotypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/bittensor/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ss58"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

const (
	// Bittensor pallet indices (matching bifrost chain client defaults)
	balancesPalletIndex = byte(5)
	transferCallIndex   = byte(3) // transfer_keep_alive
	systemPalletIndex   = byte(0)
	remarkCallIndex     = byte(0)
	utilityPalletIndex  = byte(11)
	batchAllCallIndex   = byte(2)
)

////////////////////////////////////////////////////////////////////////////////////////
// TAO
////////////////////////////////////////////////////////////////////////////////////////

// Client is a lite chain client for TAO (Bittensor) simulation tests.
type Client struct {
	chain common.Chain
	rpc   *taorpc.Client

	keys    *thorclient.Keys
	privKey ed25519.PrivateKey
	pubKey  common.PubKey
	address common.Address
}

var _ LiteChainClient = &Client{}

// NewConstructor returns a constructor for creating TAO lite chain clients.
func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

// NewClient creates a new TAO lite chain client.
func NewClient(chain common.Chain, host string, keys *thorclient.Keys) (LiteChainClient, error) {
	pkStruct, err := keys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}

	pk, err := cryptocodec.ToCmtPubKeyInterface(pkStruct.PubKey())
	if err != nil {
		return nil, fmt.Errorf("failed to get tm pub key: %w", err)
	}
	pubKey, err := common.NewPubKeyFromCrypto(pk)
	if err != nil {
		return nil, fmt.Errorf("fail to create pubkey: %w", err)
	}

	address, err := pubKey.GetAddress(chain)
	if err != nil {
		return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
	}

	// Extract raw Ed25519 private key (64 bytes: seed + public key)
	privKey := ed25519.PrivateKey(pkStruct.Bytes())

	rpcClient := taorpc.NewClient(host, time.Second*30)

	return &Client{
		chain:   chain,
		rpc:     rpcClient,
		keys:    keys,
		privKey: privKey,
		pubKey:  pubKey,
		address: address,
	}, nil
}

// GetAccount returns the account balance for the given public key.
func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	address := c.address
	if pk != nil {
		var err error
		address, err = pk.GetAddress(c.chain)
		if err != nil {
			return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
		}
	}

	raoBalance, err := c.rpc.GetBalance(address.String(), "")
	if err != nil {
		return nil, fmt.Errorf("fail to get balance: %w", err)
	}

	// Convert from rao (1e9) to thorchain units (1e8)
	amount := cosmos.NewUint(raoBalance)
	amount = amount.Quo(cosmos.NewUint(10))

	coins := common.Coins{
		common.NewCoin(common.TAOAsset, amount),
	}

	return &common.Account{
		Coins: coins,
	}, nil
}

// SignTx constructs and signs a TAO transaction.
func (c *Client) SignTx(tx SimTx) ([]byte, error) {
	// Decode destination SS58 address to get the 32-byte account ID
	_, destPubkey, err := ss58.Decode(tx.ToAddress.String())
	if err != nil {
		return nil, fmt.Errorf("fail to decode destination address: %w", err)
	}

	// Get nonce
	nonce, err := c.rpc.GetNonce(c.address.String())
	if err != nil {
		return nil, fmt.Errorf("fail to get nonce: %w", err)
	}

	// Get runtime version
	rv, err := c.rpc.GetRuntimeVersion("")
	if err != nil {
		return nil, fmt.Errorf("fail to get runtime version: %w", err)
	}

	// Get genesis hash (also used as block hash for immortal era transactions)
	genesisHashHex, err := c.rpc.GetGenesisHash()
	if err != nil {
		return nil, fmt.Errorf("fail to get genesis hash: %w", err)
	}
	genesisHash, err := taotypes.HexDecode(genesisHashHex)
	if err != nil {
		return nil, fmt.Errorf("fail to decode genesis hash: %w", err)
	}

	// Convert from thorchain units (1e8) to rao (1e9) with overflow check
	bi := tx.Coin.Amount.BigInt()
	if !bi.IsUint64() {
		return nil, fmt.Errorf("amount too large to convert to rao: %s", tx.Coin.Amount)
	}
	v := bi.Uint64()
	raoAmount := v * 10
	if raoAmount/10 != v {
		return nil, fmt.Errorf("amount overflow converting to rao: %s", tx.Coin.Amount)
	}

	// Build call data
	var callData []byte
	if tx.Memo != "" {
		// Use batch_all: [transfer_keep_alive, system_remark(memo)]
		transferCall := taotypes.BalanceTransferKeepAlive(
			balancesPalletIndex, transferCallIndex, destPubkey, raoAmount,
		)
		remarkCall := taotypes.SystemRemark(systemPalletIndex, remarkCallIndex, []byte(tx.Memo))
		callData = taotypes.BatchAll(utilityPalletIndex, batchAllCallIndex, [][]byte{transferCall, remarkCall})
	} else {
		// Simple transfer
		callData = taotypes.BalanceTransferKeepAlive(
			balancesPalletIndex, transferCallIndex, destPubkey, raoAmount,
		)
	}

	// Build signing payload
	era := taotypes.ImmortalEra()
	payload := taotypes.SigningPayload(
		callData, era, uint64(nonce), 0, taotypes.MetadataModeDisabled,
		rv.SpecVersion, rv.TransactionVersion,
		genesisHash, genesisHash,
	)

	// Sign — if payload > 256 bytes, hash first (Substrate convention)
	signingData := payload
	if len(payload) > 256 {
		hash := blake2b.Sum256(payload)
		signingData = hash[:]
	}
	signature := ed25519.Sign(c.privKey, signingData)

	// Get signer public key (32 bytes)
	signerPub := c.privKey.Public().(ed25519.PublicKey)

	// Encode signed extrinsic
	ext := &taotypes.Extrinsic{
		Signer:        signerPub,
		SignatureType: taotypes.SignatureTypeEd25519,
		Signature:     signature,
		Era:           era,
		Nonce:         uint64(nonce),
		Tip:           0,
		MetadataMode:  taotypes.MetadataModeDisabled,
		CallData:      callData,
	}

	return taotypes.EncodeSignedExtrinsic(ext), nil
}

// BroadcastTx submits a signed extrinsic to the network.
func (c *Client) BroadcastTx(signed []byte) (string, error) {
	hexTx := taotypes.HexEncode(signed)
	txHash, err := c.rpc.SubmitExtrinsic(hexTx)
	if err != nil {
		return "", fmt.Errorf("failed to broadcast tx: %w", err)
	}
	return txHash, nil
}
