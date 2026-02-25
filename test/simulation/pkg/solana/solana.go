package solana

import (
	"encoding/base64"
	"fmt"
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

	return &common.Account{
		Coins: coins,
	}, nil
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
	transaction := stypes.NewTransferTransaction(fromPubKey, toPubKey, tx.Coin.Amount.Uint64(), tx.Memo, blockhash)
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

// SignTxWithExtraTransfer signs a SOL tx that contains one primary transfer and one
// additional transfer instruction. Used by simulation-only restore paths.
// extraTo must be distinct from sender and primary destination in this builder,
// because duplicate account keys can trigger "Account loaded twice" simulation errors.
func (c *Client) SignTxWithExtraTransfer(tx SimTx, extraTo common.Address, extraAmount cosmos.Uint) ([]byte, error) {
	if extraAmount.IsZero() {
		return nil, fmt.Errorf("extra transfer amount cannot be zero")
	}

	// get latest blockhash
	blockhash, err := c.rpc.GetLatestBlockhash()
	if err != nil {
		return nil, fmt.Errorf("fail to get latest blockhash: %w", err)
	}

	// public keys
	fromPubKey := stypes.MustPublicKeyFromString(c.address.String())
	toPubKey := stypes.MustPublicKeyFromString(tx.ToAddress.String())
	extraToPubKey := stypes.MustPublicKeyFromString(extraTo.String())

	transaction := newTransferTransactionWithExtraTransfer(
		fromPubKey,
		toPubKey,
		extraToPubKey,
		tx.Coin.Amount.Uint64(),
		extraAmount.Uint64(),
		tx.Memo,
		blockhash,
	)
	rawMessage, err := transaction.Message.Serialize()
	if err != nil {
		return nil, fmt.Errorf("fail to serialize transaction: %w", err)
	}

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

func newTransferTransactionWithExtraTransfer(
	from, to, extraTo stypes.PublicKey,
	amount, extraAmount uint64,
	memo, recentBlockHash string,
) stypes.Transaction {
	// Keep three distinct writable account keys for transfer instructions:
	// [0]=from, [1]=primary to, [2]=extra to.
	accountKeys := []stypes.PublicKey{from, to, extraTo, stypes.SystemProgramID, stypes.MemoProgramID}

	// Program index is 4 (MemoProgramID) and account index 0 is signer.
	memoInstruction := stypes.NewCompiledInstruction(4, []int{0}, []byte(memo))

	// THORChain 1e8 units -> lamports 1e9.
	solValue := amount * 10
	extraSolValue := extraAmount * 10

	// Program index is 3 (SystemProgramID), from=0, to=1/2.
	primaryTransferInstruction := stypes.NewCompiledTransferInstruction(3, 0, 1, solValue)
	extraTransferInstruction := stypes.NewCompiledTransferInstruction(3, 0, 2, extraSolValue)

	header := stypes.MessageHeader{
		NumRequireSignatures:        1,
		NumReadonlySignedAccounts:   0,
		NumReadonlyUnsignedAccounts: 2,
	}
	message := stypes.NewMessage(
		header,
		accountKeys,
		recentBlockHash,
		[]stypes.CompiledInstruction{
			primaryTransferInstruction,
			extraTransferInstruction,
			memoInstruction,
		},
	)
	return stypes.NewTransaction(message)
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

func (c *Client) GetHeight() (int64, error) {
	slot, err := c.rpc.GetSlot()
	if err != nil {
		return 0, fmt.Errorf("failed to get slot: %w", err)
	}
	return int64(slot), nil
}
