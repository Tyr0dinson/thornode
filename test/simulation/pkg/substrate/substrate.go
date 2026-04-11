package substrate

import (
	"crypto/ed25519"
	"fmt"
	"time"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
	"golang.org/x/crypto/blake2b"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ss58"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Substrate
////////////////////////////////////////////////////////////////////////////////////////

// Client is a lite chain client for Substrate chain simulation tests.
type Client struct {
	chain   common.Chain
	rpc     *rpc.Client
	keys    *thorclient.Keys
	privKey ed25519.PrivateKey
	pubKey  common.PubKey
	address common.Address
	params  substrate.Params
}

var _ LiteChainClient = &Client{}

// NewConstructor returns a constructor for creating a Substrate lite chain clients.
func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

// NewClient creates a new Substrate lite chain client.
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
		return nil, fmt.Errorf("failed to create pubkey: %w", err)
	}

	address, err := pubKey.GetAddress(chain)
	if err != nil {
		return nil, fmt.Errorf("failed to get address from pubkey(%s): %w", pk, err)
	}

	// Extract raw Ed25519 private key (64 bytes: seed + public key)
	privKey := ed25519.PrivateKey(pkStruct.Bytes())

	rpcClient := rpc.NewClient(host, time.Second*30)

	params, found := substrate.ChainParams[chain]
	if !found {
		return nil, fmt.Errorf("failed to get chain params: %s", chain)
	}

	return &Client{
		chain:   chain,
		rpc:     rpcClient,
		keys:    keys,
		privKey: privKey,
		pubKey:  pubKey,
		address: address,
		params:  params,
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

	balance, err := c.rpc.GetBalance(address.String(), "")
	if err != nil {
		return nil, fmt.Errorf("fail to get balance: %w", err)
	}

	// Convert from native asset to thorchain units (1e8)
	amount := c.chain.NativeGasToThorchain(balance)

	coins := common.Coins{
		common.NewCoin(c.chain.GetGasAsset(), amount),
	}

	return &common.Account{
		Coins: coins,
	}, nil
}

// SignTx constructs and signs a Substrate transaction.
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
	genesisHash, err := types.HexDecode(genesisHashHex)
	if err != nil {
		return nil, fmt.Errorf("fail to decode genesis hash: %w", err)
	}

	// Convert from thorchain units (1e8) to native units
	amount := c.chain.ThorchainToNativeGas(tx.Coin.Amount)

	// Build call data
	var callData []byte
	if tx.Memo != "" {
		// Use batch_all: [transfer_keep_alive, system_remark(memo)]
		transferCall := types.BalanceTransferKeepAlive(
			c.params.BalancesPallet,
			c.params.BalancesKeepAliveCall,
			destPubkey,
			amount,
		)
		remarkCall := types.SystemRemark(
			c.params.SystemPallet,
			c.params.SystemRemarkCall,
			[]byte(tx.Memo),
		)
		callData = types.BatchAll(
			c.params.UtilityPallet,
			c.params.UtilityBatchAllCall,
			[][]byte{transferCall, remarkCall},
		)
	} else {
		// Simple transfer
		callData = types.BalanceTransferKeepAlive(
			c.params.BalancesPallet,
			c.params.BalancesKeepAliveCall,
			destPubkey,
			amount,
		)
	}

	// Build signing payload
	era := types.ImmortalEra()
	payload := types.SigningPayload(
		callData, era, uint64(nonce), 0,
		c.params.CheckMetadataHash, types.MetadataModeDisabled,
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
	ext := &types.Extrinsic{
		Signer:            signerPub,
		SignatureType:     types.SignatureTypeEd25519,
		Signature:         signature,
		Era:               era,
		Nonce:             uint64(nonce),
		Tip:               0,
		MetadataMode:      types.MetadataModeDisabled,
		CheckMetadataHash: c.params.CheckMetadataHash,
		CallData:          callData,
	}

	return types.EncodeSignedExtrinsic(ext), nil
}

// BroadcastTx submits a signed extrinsic to the network.
func (c *Client) BroadcastTx(signed []byte) (string, error) {
	hexTx := types.HexEncode(signed)
	txHash, err := c.rpc.SubmitExtrinsic(hexTx)
	if err != nil {
		return "", fmt.Errorf("failed to broadcast tx: %w", err)
	}
	return txHash, nil
}
