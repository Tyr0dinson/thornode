package solana

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"time"

	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/decred/dcrd/dcrec/edwards"
	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/solana/rpc"
	stypes "gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/common"

	. "gitlab.com/thorchain/thornode/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

func ctx() context.Context {
	return context.Background()
}

////////////////////////////////////////////////////////////////////////////////////////
// Solana
////////////////////////////////////////////////////////////////////////////////////////

type Client struct {
	chain common.Chain
	rpc   *rpc.SolRPC

	keys    *thorclient.Keys
	privKey *edwards.PrivateKey
	pubKey  common.PubKey
	address common.Address
}

var _ LiteChainClient = &Client{}

func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

func NewClient(chain common.Chain, host string, keys *thorclient.Keys) (LiteChainClient, error) {
	// extract the private key
	privateKey, err := keys.GetPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}

	fmt.Println("SOL NewClient privateKey:", privateKey.Bytes())

	privKey, _, err := edwards.PrivKeyFromScalar(edwards.Edwards(), privateKey.Bytes())
	if err != nil {
		return nil, fmt.Errorf("fail to parse private key: %w", err)
	}

	pk, err := cryptocodec.ToTmPubKeyInterface(privateKey.PubKey())
	if err != nil {
		return nil, fmt.Errorf("failed to get tm pub key: %w", err)
	}
	pubKey, err := common.NewPubKeyFromCrypto(pk)
	if err != nil {
		return nil, fmt.Errorf("fail to create pubkey: %w", err)
	}

	// get pubkey address for the chain
	address, err := pubKey.GetAddress(chain)
	if err != nil {
		return nil, fmt.Errorf("fail to get address from pubkey(%s): %w", pk, err)
	}

	// dial the rpc host
	rpc := rpc.NewSolRPC(host, time.Second*30)

	return &Client{
		chain:   chain,
		rpc:     rpc,
		keys:    keys,
		privKey: privKey,
		pubKey:  pubKey,
		address: address,
	}, nil
}

func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	fmt.Println("SOL GetAccount")

	lamportsBalance, err := c.rpc.GetBalance(pk.String(), "finalized", 0)
	if err != nil {
		return nil, fmt.Errorf("fail to get balance: %w", err)
	}

	fmt.Println("SOL GetAccount balance:", lamportsBalance)

	// get amount
	amount := sdk.NewUint(lamportsBalance)
	amount = amount.Quo(sdk.NewUint(10)) // 1e9 -> 1e8

	coins := common.Coins{
		common.NewCoin(common.SOLAsset, amount),
	}

	return &common.Account{
		Coins: coins,
	}, nil
}

func (c *Client) SignTx(tx SimTx) ([]byte, error) {
	fmt.Println("SOL SignTx")

	// get latest blockhash
	blockhash, err := c.rpc.GetLatestBlockhash()
	if err != nil {
		return nil, fmt.Errorf("fail to get latest blockhash: %w", err)
	}

	// public keys
	fromPubKey := stypes.MustPublicKeyFromString(c.pubKey.String())
	toPubKey := stypes.MustPublicKeyFromString(tx.ToAddress.String())

	// create transaction
	transaction := stypes.NewTransferTransaction(fromPubKey, toPubKey, tx.Coin.Amount.Uint64(), tx.Memo, blockhash)
	rawTx, err := transaction.Serialize()
	if err != nil {
		return nil, fmt.Errorf("fail to serialize transaction: %w", err)
	}

	// Sign rawTx
	signature, err := c.sign(rawTx)
	if err != nil {
		return nil, fmt.Errorf("fail to sign transaction: %w", err)
	}

	transaction.AddSignature(signature)
	signedTx, err := transaction.Serialize()
	if err != nil {
		return nil, fmt.Errorf("fail to serialize signed transaction: %w", err)
	}

	return signedTx, nil
}

func (c *Client) sign(msg []byte) ([]byte, error) {
	privKey := ed25519.PrivateKey(c.privKey.Serialize())
	signatureBytes := ed25519.Sign(ed25519.PrivateKey(privKey), msg)
	return signatureBytes, nil
}

func (c *Client) BroadcastTx(signed []byte) (string, error) {
	fmt.Println("SOL BroadcastTx")

	base64Tx := base64.StdEncoding.EncodeToString(signed)
	txSig, err := c.rpc.BroadcastTx(base64Tx)
	if err != nil {
		return "", fmt.Errorf("failed to broadcast tx: %w", err)
	}

	return txSig, nil
}
