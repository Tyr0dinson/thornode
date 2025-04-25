package ton

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	sdkmath "cosmossdk.io/math"
	cosmoscryptoed25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	tonaddress "github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/tlb"
	"github.com/xssnick/tonutils-go/ton"
	"github.com/xssnick/tonutils-go/ton/wallet"
	"github.com/xssnick/tonutils-go/tvm/cell"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"

	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// TON
////////////////////////////////////////////////////////////////////////////////////////

const (
	V3R2CodeHex       = "B5EE9C724101010100710000DEFF0020DD2082014C97BA218201339CBAB19F71B0ED44D0D31FD31F31D70BFFE304E0A4F2608308D71820D31FD31FD31FF82313BBF263ED44D0D31FD31FD3FFD15132BAF2A15144BAF2A204F901541055F910F2A3F8009320D74A96D307D402FB00E8D101A4C8CB1FCB1FCBFFC9ED5410BD6DAD"
	TxFee             = 5_000_000 // 0.005 TON
	myLocalTonLiteKey = "E7XwFSQzNkcRepUC23J2nRpASXpnsEKmyyHYV4u/FZY="
)

type Client struct {
	chain      common.Chain
	keys       *thorclient.Keys
	k          *cosmoscryptoed25519.PrivKey
	api        ton.APIClientWrapped
	walletCode *cell.Cell
	accountID  string
}

var _ types.LiteChainClient = &Client{}

func NewConstructor(host string) types.LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (types.LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

func NewClient(chain common.Chain, host string, keys *thorclient.Keys) (types.LiteChainClient, error) {
	// Get private key
	priv, err := keys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("failed to get private key: %w", err)
	}

	// Get public key
	pub, ok := priv.PubKey().(*cosmoscryptoed25519.PubKey)
	if !ok {
		return nil, fmt.Errorf("failed to cast pubkey to ed25519")
	}

	// Create TON wallet address from public key
	tonWallet, err := wallet.AddressFromPubKey(ed25519.PublicKey(pub.Bytes()), wallet.V3R2, wallet.DefaultSubwallet)
	if err != nil {
		return nil, fmt.Errorf("failed to get wallet address: %w", err)
	}

	ctx := context.Background()
	pool := liteclient.NewConnectionPool()

	if host == "" {
		return nil, fmt.Errorf("no client config set")
	}

	// Setup connection based on host parameter
	if err := pool.AddConnection(ctx, host, myLocalTonLiteKey); err != nil {
		return nil, fmt.Errorf("fail to add client connection: %w", err)
	}

	// Create API client with fast proof checks
	api := ton.NewAPIClient(pool, ton.ProofCheckPolicyFast).WithRetry()

	// Parse wallet code
	data, err := hex.DecodeString(V3R2CodeHex)
	if err != nil {
		return nil, fmt.Errorf("failed to decode V3R2CodeHex: %w", err)
	}

	walletCode, err := cell.FromBOC(data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode BOC: %w", err)
	}

	accountID := tonWallet.Bounce(false).Testnet(true).String()

	return &Client{
		chain:      chain,
		keys:       keys,
		k:          priv,
		api:        api,
		walletCode: walletCode,
		accountID:  accountID,
	}, nil
}

// fromThor converts THORChain amount (1e8) to TON native amount (1e9)
func fromThor(amount sdkmath.Uint) tlb.Coins {
	nanoAmount := amount.MulUint64(10) // 1e8 -> 1e9
	return tlb.FromNanoTON(nanoAmount.BigInt())
}

// toThor converts TON native amount (1e9) to THORChain amount (1e8)
func toThor(nanoAmount tlb.Coins) sdkmath.Uint {
	amount := nanoAmount.Nano().Div(nanoAmount.Nano(), tlb.FromNanoTON(big.NewInt(10)).Nano()) // 1e9 -> 1e8
	return sdkmath.NewUintFromBigInt(amount)
}

// GetAccount returns the account details for the given public key
func (c *Client) GetAccount(pk *common.PubKey) (*common.Account, error) {
	var address string
	var err error

	if pk != nil {
		addr, err := pk.GetAddress(c.chain)
		if err != nil {
			return nil, fmt.Errorf("fail to get address from pubkey: %w", err)
		}
		address = addr.String()
	} else {
		// Use client's own address if none provided
		address = c.accountID
	}

	// Parse TON address
	tonAddress, err := tonaddress.ParseAddr(address)
	if err != nil {
		return nil, fmt.Errorf("failed to parse TON address: %w", err)
	}

	// Get current block info
	ctx := context.Background()
	info, err := c.api.GetMasterchainInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get master chain info: %w", err)
	}

	// Get account information
	account, err := c.api.GetAccount(ctx, info, tonAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to get account: %w", err)
	}

	// Create TON asset
	asset, err := common.NewAsset("TON.TON")
	if err != nil {
		return nil, fmt.Errorf("failed to create asset: %w", err)
	}

	var amount sdkmath.Uint
	var sequence int64

	// Handle uninitialized account case
	if account.State == nil {
		amount = sdkmath.ZeroUint()
	} else {
		// Convert TON balance to THORChain format
		amount = toThor(account.State.Balance)

		// Get sequence number (if needed)
		result, err := c.api.RunGetMethod(ctx, info, tonAddress, "seqno")
		if err == nil {
			seq, err := result.Int(0)
			if err == nil {
				sequence = seq.Int64()
			}
		}
	}

	// Create and return account info
	return &common.Account{
		Sequence:      sequence,
		AccountNumber: 0,
		Coins:         common.NewCoins(common.NewCoin(asset, amount)),
	}, nil
}

// SignTx signs a transaction
func (c *Client) SignTx(tx types.SimTx) ([]byte, error) {
	// Parse destination address
	toAddress, err := tonaddress.ParseAddr(tx.ToAddress.String())
	if err != nil {
		return nil, fmt.Errorf("failed to parse destination address: %w", err)
	}

	// Create comment cell for memo if present
	var comment *cell.Cell
	if tx.Memo != "" {
		comment, err = wallet.CreateCommentCell(tx.Memo)
		if err != nil {
			return nil, fmt.Errorf("failed to create comment cell: %w", err)
		}
	}

	// Get account
	account, err := c.GetAccount(nil)
	if err != nil {
		return nil, fmt.Errorf("fail to get account: %w", err)
	}

	// Convert amount from THORChain format to TON format
	amount := fromThor(tx.Coin.Amount)

	// Create message
	message := wallet.SimpleMessage(toAddress, amount, comment)
	message.InternalMessage.Bounce = false

	// Create payload
	payload := cell.BeginCell()

	// Store subwallet ID
	err = payload.StoreUInt(uint64(wallet.DefaultSubwallet), 32)
	if err != nil {
		return nil, fmt.Errorf("failed to store subwallet: %w", err)
	}

	// Store valid until timestamp (current + 60 minutes)
	validUntil := uint64(time.Now().Add(60 * time.Minute).Unix())
	err = payload.StoreUInt(validUntil, 32)
	if err != nil {
		return nil, fmt.Errorf("failed to store timeout: %w", err)
	}

	// Store sequence number
	err = payload.StoreUInt(uint64(account.Sequence), 32)
	if err != nil {
		return nil, fmt.Errorf("failed to store seqno: %w", err)
	}

	// Convert internal message to cell
	internalMessage, err := tlb.ToCell(message.InternalMessage)
	if err != nil {
		return nil, fmt.Errorf("failed to convert internal message to cell: %w", err)
	}

	// Store message mode
	err = payload.StoreUInt(uint64(message.Mode), 8)
	if err != nil {
		return nil, fmt.Errorf("failed to store message mode: %w", err)
	}

	// Store internal message reference
	err = payload.StoreRef(internalMessage)
	if err != nil {
		return nil, fmt.Errorf("failed to store internal message: %w", err)
	}

	// Get hash to sign
	hash := payload.EndCell().Hash()

	// Sign the hash
	signature, err := c.k.Sign(hash)
	if err != nil {
		return nil, fmt.Errorf("failed to sign message: %w", err)
	}

	// Create the final message body
	body := cell.BeginCell()
	err = body.StoreSlice(signature, 512)
	if err != nil {
		return nil, fmt.Errorf("failed to store signature: %w", err)
	}

	err = body.StoreBuilder(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to store payload: %w", err)
	}

	// Convert cell to BOC
	return body.EndCell().ToBOC(), nil
}

// BroadcastTx broadcasts a signed transaction to the TON network
func (c *Client) BroadcastTx(signed []byte) (string, error) {
	ctx := context.Background()

	// Decode BOC
	body, err := cell.FromBOC(signed)
	if err != nil {
		return "", fmt.Errorf("failed to parse txBytes: %w", err)
	}

	// Parse address
	fromAddress, err := tonaddress.ParseAddr(c.accountID)
	if err != nil {
		return "", fmt.Errorf("failed to parse wallet address: %w", err)
	}

	// Check if wallet is initialized
	var stateInit *tlb.StateInit
	initialized := true

	// Get current block
	block, err := c.api.CurrentMasterchainInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get master chain info: %w", err)
	}

	// Check if contract is initialized by calling seqno method
	_, err = c.api.WaitForBlock(block.SeqNo).RunGetMethod(ctx, block, fromAddress, "seqno")
	if err != nil {
		execErr, ok := err.(ton.ContractExecError)
		if ok && execErr.Code == ton.ErrCodeContractNotInitialized {
			initialized = false
		} else {
			return "", fmt.Errorf("failed to run method: %w", err)
		}
	}

	// Create stateInit if wallet not initialized
	if !initialized {
		pubKey := c.k.PubKey().Bytes()
		stateInit = &tlb.StateInit{
			Data: cell.BeginCell().
				MustStoreUInt(0, 32).                               // seqno
				MustStoreUInt(uint64(wallet.DefaultSubwallet), 32). // sub wallet
				MustStoreSlice(pubKey, 256).                        // public key
				EndCell(),
			Code: c.walletCode,
		}
	}

	// Create external message
	msg := &tlb.ExternalMessage{
		DstAddr:   fromAddress,
		StateInit: stateInit,
		Body:      body,
	}

	// Send message and wait for transaction
	tx, _, _, err := c.api.SendExternalMessageWaitTransaction(ctx, msg)
	if err != nil {
		return "", fmt.Errorf("failed to broadcast transaction: %w", err)
	}

	// Return transaction hash as hex string
	return hex.EncodeToString(tx.Hash), nil
}
