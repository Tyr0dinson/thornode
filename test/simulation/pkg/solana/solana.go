package solana

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"time"

	btypes "github.com/blocto/solana-go-sdk/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Solana
////////////////////////////////////////////////////////////////////////////////////////

type Client struct {
	chain common.Chain
	rpc   *rpc.SolRPC

	keys    *thorclient.Keys
	signer  btypes.Account
	pubKey  common.PubKey
	address common.Address
}

var _ LiteChainClient = &Client{}

// Wait for finalized Solana confirmation so simulation actors do not proceed on
// signatures that later disappear before Bifrost can observe the inbound.
var (
	// Mocknet Solana finalization can take materially longer on container-heavy
	// local ARM setups. Keep the finalized commitment, but give setup and actor
	// funding broadcasts more time before declaring the tx lost.
	solanaBroadcastConfirmationAttempts     = 60
	solanaBroadcastConfirmationPollInterval = time.Second
	solanaBroadcastRebroadcastEvery         = 5
	solanaAccountCommitment                 = "finalized"
	solanaBroadcastConfirmationCommitment   = "finalized"
)

func NewConstructor(host string) LiteChainClientConstructor {
	return func(chain common.Chain, keys *thorclient.Keys) (LiteChainClient, error) {
		return NewClient(chain, host, keys)
	}
}

func NewClient(chain common.Chain, host string, keys *thorclient.Keys) (LiteChainClient, error) {
	pkStruct, err := keys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("fail to get private key: %w", err)
	}

	account, err := btypes.AccountFromBytes(pkStruct.Bytes())
	if err != nil {
		return nil, fmt.Errorf("fail to get account: %w", err)
	}

	pk, err := cryptocodec.ToCmtPubKeyInterface(pkStruct.PubKey())
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

	rpc := rpc.NewSolRPC(host, time.Second*30)

	return &Client{
		chain:   chain,
		rpc:     rpc,
		keys:    keys,
		pubKey:  pubKey,
		address: address,
		signer:  account,
	}, nil
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
	return c.GetAccountByAddress(address)
}

// GetAccountByAddress returns the SOL balance of address and its balance of every
// simulation SPL token (see Tokens), read from its associated token accounts.
func (c *Client) GetAccountByAddress(address common.Address) (*common.Account, error) {
	lamportsBalance, err := c.rpc.GetBalance(address.String(), solanaAccountCommitment, 0)
	if err != nil {
		return nil, fmt.Errorf("fail to get balance: %w", err)
	}
	tokenAccountLamports, err := c.rpc.GetOwnedTokenAccountLamports(address.String(), solanaAccountCommitment)
	if err != nil {
		return nil, fmt.Errorf("fail to get owned token account lamports: %w", err)
	}
	lamportsBalance = lamportsBalance.Add(lamportsBalance, tokenAccountLamports)

	// get amount
	amount := cosmos.NewUintFromBigInt(lamportsBalance)
	amount = amount.Quo(cosmos.NewUint(10)) // 1e9 -> 1e8

	coins := common.Coins{
		common.NewCoin(common.SOLAsset, amount),
	}

	tokenCoins, err := c.tokenCoins(address)
	if err != nil {
		return nil, err
	}
	coins = append(coins, tokenCoins...)

	return &common.Account{
		Coins: coins,
	}, nil
}

// tokenCoins returns address's balance of every simulation SPL token, in 1e8 units.
// A missing token account is a zero balance.
func (c *Client) tokenCoins(address common.Address) (common.Coins, error) {
	owner, err := stypes.PublicKeyFromStringStrict(address.String())
	if err != nil {
		return nil, fmt.Errorf("invalid solana address %s: %w", address, err)
	}
	coins := common.Coins{}
	for asset, token := range Tokens() {
		_, mint, ok := tokenByAsset(asset)
		if !ok {
			return nil, fmt.Errorf("invalid simulation token %s", asset)
		}
		ata, err := stypes.FindAssociatedTokenAddress(owner, mint)
		if err != nil {
			return nil, fmt.Errorf("fail to derive %s token account: %w", asset, err)
		}
		acct, err := c.rpc.GetTokenAccount(ata.String(), solanaAccountCommitment)
		if err != nil {
			return nil, fmt.Errorf("fail to get %s token account: %w", asset, err)
		}
		balance := big.NewInt(0)
		if acct.Exists && acct.Mint == token.Address {
			balance = acct.Amount
		}
		converted, err := common.ConvertDecimals(balance, token.Decimals, common.THORChainDecimals)
		if err != nil {
			return nil, err
		}
		coins = append(coins, common.NewCoin(asset, cosmos.NewUintFromBigInt(converted)))
	}
	return coins, nil
}

func (c *Client) SignTx(tx SimTx) ([]byte, error) {
	// get latest blockhash
	blockhash, err := c.rpc.GetLatestBlockhash()
	if err != nil {
		return nil, fmt.Errorf("fail to get latest blockhash: %w", err)
	}

	// public keys
	fromPubKey := stypes.MustPublicKeyFromString(c.address.String())
	toPubKey := stypes.MustPublicKeyFromString(tx.ToAddress.String())

	// create transaction
	var transaction stypes.Transaction
	if tx.Coin.Asset.Equals(common.SOLAsset) {
		transaction = stypes.NewTransferTransaction(fromPubKey, toPubKey, tx.Coin.Amount.Uint64(), tx.Memo, blockhash)
	} else {
		transaction, err = tokenTransferTransaction(fromPubKey, toPubKey, tx, blockhash)
		if err != nil {
			return nil, err
		}
	}
	rawMessage, err := transaction.Message.Serialize()
	if err != nil {
		return nil, fmt.Errorf("fail to serialize transaction: %w", err)
	}

	// Sign rawTx
	signature, err := c.sign(rawMessage)
	if err != nil {
		return nil, fmt.Errorf("fail to sign transaction: %w", err)
	}
	err = transaction.AddSignature(signature)
	if err != nil {
		return nil, fmt.Errorf("fail to add signature: %w", err)
	}
	signedTx, err := transaction.Serialize()
	if err != nil {
		return nil, fmt.Errorf("fail to serialize signed transaction: %w", err)
	}

	return signedTx, nil
}

// tokenTransferTransaction builds an SPL token transfer the way wallets send one:
// CreateIdempotent for the recipient's associated token account (the sender pays
// rent if it is missing), then TransferChecked from the sender's associated token
// account, with the memo in a Memo instruction. A deposit to a vault therefore
// credits the vault's associated token account, which Bifrost scans.
func tokenTransferTransaction(from, to stypes.PublicKey, tx SimTx, blockhash string) (stypes.Transaction, error) {
	token, mint, ok := tokenByAsset(tx.Coin.Asset)
	if !ok {
		return stypes.Transaction{}, fmt.Errorf("unsupported solana asset %s", tx.Coin.Asset)
	}
	amount, err := common.ConvertDecimals(tx.Coin.Amount.BigInt(), common.THORChainDecimals, token.Decimals)
	if err != nil {
		return stypes.Transaction{}, err
	}
	if amount.Sign() <= 0 || !amount.IsUint64() {
		return stypes.Transaction{}, fmt.Errorf("invalid %s amount %s", tx.Coin.Asset, tx.Coin.Amount)
	}
	source, err := stypes.FindAssociatedTokenAddress(from, mint)
	if err != nil {
		return stypes.Transaction{}, err
	}
	dest, err := stypes.FindAssociatedTokenAddress(to, mint)
	if err != nil {
		return stypes.Transaction{}, err
	}
	return stypes.NewSPLTransferTransaction(stypes.SPLTransferParams{
		Vault:     from, // the builder's fee payer and transfer authority: here the sender
		Recipient: to,
		Mint:      mint,
		SourceATA: source,
		DestATA:   dest,
		Amount:    amount.Uint64(),
		Decimals:  uint8(token.Decimals),
		Memo:      tx.Memo,
	}, blockhash), nil
}

func (c *Client) sign(msg []byte) ([]byte, error) {
	return c.signer.Sign(msg), nil
}

func (c *Client) BroadcastTx(signed []byte) (string, error) {
	base64Tx := base64.StdEncoding.EncodeToString(signed)
	txSig, err := c.rpc.BroadcastTx(base64Tx)
	if err != nil {
		return "", fmt.Errorf("failed to broadcast tx: %w", err)
	}

	var lastErr error
	for attempt := 1; attempt <= solanaBroadcastConfirmationAttempts; attempt++ {
		tx, err := c.rpc.GetTransactionWithCommitment(txSig, solanaBroadcastConfirmationCommitment)
		if err == nil && tx != nil && tx.Slot > 0 {
			if tx.Meta.Err != nil {
				return "", fmt.Errorf("broadcasted tx %s failed on solana node: %v", txSig, tx.Meta.Err)
			}
			return txSig, nil
		}

		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("transaction %s not %s yet", txSig, solanaBroadcastConfirmationCommitment)
		}

		if attempt < solanaBroadcastConfirmationAttempts {
			if solanaBroadcastRebroadcastEvery > 0 && attempt%solanaBroadcastRebroadcastEvery == 0 {
				rebroadcastTxSig, err := c.rpc.BroadcastTx(base64Tx)
				if err != nil {
					lastErr = err
				} else if rebroadcastTxSig != txSig {
					return "", fmt.Errorf("rebroadcast returned different solana signature %s for tx %s", rebroadcastTxSig, txSig)
				}
			}
			time.Sleep(solanaBroadcastConfirmationPollInterval)
		}
	}

	waitDuration := time.Duration(solanaBroadcastConfirmationAttempts) * solanaBroadcastConfirmationPollInterval
	return txSig, fmt.Errorf("broadcasted tx %s not confirmed on solana node after %s: %w", txSig, waitDuration, lastErr)
}
