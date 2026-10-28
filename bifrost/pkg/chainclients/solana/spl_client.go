package solana

import (
	"errors"
	"fmt"
	"math/big"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// isSPLOutbound reports whether the outbound's coin is a token rather than SOL.
// Callers have already required exactly one coin.
func isSPLOutbound(tx stypes.TxOutItem) bool {
	return len(tx.Coins) == 1 && !tx.Coins[0].Asset.Equals(common.SOLAsset)
}

// splHaltedAt reports whether THORChain's HaltSOLTokens mimir pauses SPL signing at
// the keysign height. It is read as of that height, so every keysign party decides
// alike. A bridge that cannot read at a height (tests) falls back to the live read.
func (c *SOLClient) splHaltedAt(height int64) (bool, error) {
	if c.bridge == nil {
		return false, nil
	}
	var value int64
	var err error
	if r, ok := c.bridge.(mimirHeightReader); ok {
		value, err = r.GetMimirAtHeight(constants.MimirKeyHaltSOLTokens, height)
	} else {
		value, err = c.bridge.GetMimir(constants.MimirKeyHaltSOLTokens)
	}
	if err != nil {
		return false, err
	}
	return splHaltActive(value, height), nil
}

// errSPLSelfSend refuses an SPL outbound from a vault to its own address. It would
// transfer from the vault's token account to the same account, which the Token
// program accepts as a no-op: the transaction lands and pays the fee, but no balance
// changes, so it can never be observed as the outbound and THORChain would re-issue
// it, burning a fee on every attempt.
var errSPLSelfSend = errors.New("SPL self-send cannot be observed")

// errSPLZeroAmount refuses an SPL outbound whose amount is zero in the mint's base
// units: a zero amount, or one below one base unit once converted from THORChain's
// 1e8 units. There is nothing to transfer, so no node can ever sign it.
var errSPLZeroAmount = errors.New("token value is zero")

// splRecipientIsTokenAccount reports whether an SPL outbound's recipient address is
// itself a token account or a mint, that is, an account owned by a token program. The
// outbound pays the recipient's associated token account, derived from the address as
// a wallet; for such an address that account belongs to an address nobody can sign
// for, so the tokens could never be moved again. An account that does not exist yet,
// or one owned by any other program, is a wallet. A failed read is an error, so the
// outbound is retried rather than signed through.
func (c *SOLClient) splRecipientIsTokenAccount(tx stypes.TxOutItem) (bool, error) {
	if c.rpcClient == nil {
		return false, nil
	}
	meta, err := c.rpcClient.GetAccountMeta(tx.ToAddress.String())
	if err != nil {
		return false, fmt.Errorf("fail to read recipient account %s: %w", tx.ToAddress, err)
	}
	return meta.Owner == types.TokenProgramID.String() || meta.Owner == types.Token2022ProgramID.String(), nil
}

// splRecipientUndeliverable reads the recipient's associated token account of an SPL
// outbound and reports whether it exists but can never receive the outbound, which
// runs CreateIdempotent on it and then TransferChecked into it. Both fail on chain
// every time when the account:
//   - is owned by an address other than the recipient (ownerReassigned): SPL Token
//     lets an account's owner be reassigned (SetAuthority AccountOwner), after which
//     CreateIdempotent fails with InvalidOwner;
//   - is frozen, e.g. by the issuer, after which the transfer fails.
//
// A missing account is created by the outbound. A failed read is returned as an error;
// the caller fails open.
func (c *SOLClient) splRecipientUndeliverable(p types.SPLTransferParams) (ownerReassigned, frozen bool, err error) {
	if c.rpcClient == nil {
		return false, false, nil
	}
	dest, err := c.rpcClient.GetTokenAccount(p.DestATA.String(), "confirmed")
	if err != nil {
		return false, false, fmt.Errorf("fail to read recipient token account %s: %w", p.DestATA, err)
	}
	if !dest.Exists {
		return false, false, nil
	}
	return dest.Owner != p.Recipient.String(), dest.State == "frozen", nil
}

// splTransferParams resolves an SPL outbound: the whitelisted mint, the amount in
// the mint's base units, and the vault's and recipient's associated token accounts.
// Everything is derived from the outbound and the whitelist alone, so every keysign
// party builds identical bytes without reading chain state.
func (c *SOLClient) splTransferParams(tx stypes.TxOutItem, vault, recipient types.PublicKey) (types.SPLTransferParams, error) {
	if recipient == vault {
		return types.SPLTransferParams{}, errSPLSelfSend
	}
	coin := tx.Coins[0]
	token, ok := c.spl.tokenByAsset(coin.Asset)
	if !ok {
		return types.SPLTransferParams{}, fmt.Errorf("asset %s is not a whitelisted SPL token", coin.Asset)
	}
	amount, err := splFromTHORChain(coin.Amount, token.decimals)
	if err != nil {
		return types.SPLTransferParams{}, fmt.Errorf("fail to convert %s amount: %w", coin.Asset, err)
	}
	if amount == 0 {
		return types.SPLTransferParams{}, errSPLZeroAmount
	}
	source, err := types.FindAssociatedTokenAddress(vault, token.mint)
	if err != nil {
		return types.SPLTransferParams{}, fmt.Errorf("fail to derive vault token account: %w", err)
	}
	dest, err := types.FindAssociatedTokenAddress(recipient, token.mint)
	if err != nil {
		return types.SPLTransferParams{}, fmt.Errorf("fail to derive recipient token account: %w", err)
	}
	return types.SPLTransferParams{
		Vault:     vault,
		Recipient: recipient,
		Mint:      token.mint,
		SourceATA: source,
		DestATA:   dest,
		Amount:    amount,
		Decimals:  uint8(token.decimals),
		Memo:      tx.Memo,
	}, nil
}

// splOutboundParams resolves the vault and recipient keys of an SPL outbound, then
// its transfer parameters.
func (c *SOLClient) splOutboundParams(tx stypes.TxOutItem) (types.SPLTransferParams, error) {
	vaultAddr, err := tx.VaultPubKeyEddsa.GetAddress(c.GetChain())
	if err != nil {
		return types.SPLTransferParams{}, fmt.Errorf("fail to get vault address: %w", err)
	}
	vault, err := types.PublicKeyFromString(vaultAddr.String())
	if err != nil {
		return types.SPLTransferParams{}, fmt.Errorf("fail to create vault public key: %w", err)
	}
	recipient, err := types.PublicKeyFromString(tx.ToAddress.String())
	if err != nil {
		return types.SPLTransferParams{}, fmt.Errorf("fail to create to public key: %w", err)
	}
	return c.splTransferParams(tx, vault, recipient)
}

// splTokenCoins returns the vault's balance of every whitelisted SPL token, read from
// its associated token accounts at finalized commitment. A missing token account is a
// zero balance. Any read failure fails the whole call: reporting a vault without one
// of its tokens would make it look insolvent in that token.
func (c *SOLClient) splTokenCoins(addr string) (common.Coins, error) {
	tokenAccounts, err := c.spl.vaultTokenAccounts(addr)
	if err != nil {
		return nil, err
	}
	coins := make(common.Coins, 0, len(tokenAccounts))
	for _, ta := range tokenAccounts {
		acct, err := c.rpcClient.GetTokenAccount(ta.address, "finalized")
		if err != nil {
			return nil, fmt.Errorf("fail to get %s token account %s: %w", ta.token.asset, ta.address, err)
		}
		amount := big.NewInt(0)
		if acct.Exists && acct.Mint == ta.token.mint.String() {
			amount = acct.Amount
		}
		coinAmount, err := splToTHORChain(amount, ta.token.decimals)
		if err != nil {
			return nil, err
		}
		coin := common.NewCoin(ta.token.asset, coinAmount)
		coin.Decimals = int64(ta.token.decimals)
		coins = append(coins, coin)
	}
	return coins, nil
}

// splTokenAccountRent returns the rent-exempt minimum of an SPL Token account.
func (c *SOLClient) splTokenAccountRent() uint64 {
	if c.rpcClient != nil {
		if rent, err := c.rpcClient.GetMinimumBalanceForRentExemption(splTokenAccountSize); err == nil && rent > 0 {
			return rent
		}
	}
	return splTokenAccountRentLamports
}

// splRecipientRentLamports estimates the rent an SPL outbound will pay to create the
// recipient's associated token account: zero if it already exists. It only sizes the
// gas of the signer's own early observation; the scanner's final observation books
// the vault's actual lamport spend, and THORChain corrects any difference. A failed
// read estimates zero.
func (c *SOLClient) splRecipientRentLamports(p types.SPLTransferParams) uint64 {
	if c.rpcClient == nil {
		return 0
	}
	dest, err := c.rpcClient.GetTokenAccount(p.DestATA.String(), "confirmed")
	if err != nil || dest.Exists {
		return 0
	}
	return c.splTokenAccountRent()
}

// splDurablePreSignReject is durablePreSignReject for an SPL outbound. On top of
// simulation it refuses to sign when:
//   - the vault's token account is missing, frozen, or holds less than the amount,
//   - the recipient's token account exists but is frozen,
//   - the vault cannot pay the fee plus the recipient token account's rent (when it
//     must be created), or would be left below rent-exempt.
//
// As for SOL, RPC failures fail open.
func (c *SOLClient) splDurablePreSignReject(tx stypes.TxOutItem, solTx types.Transaction) string {
	p, err := c.splOutboundParams(tx)
	if err != nil {
		return ""
	}
	rentNeeded := uint64(0)
	dest, err := c.rpcClient.GetTokenAccount(p.DestATA.String(), "confirmed")
	switch {
	case err != nil:
		c.logger.Warn().Err(err).Str("token_account", p.DestATA.String()).Msg("pre-sign filter: fail to read recipient token account; not filtering")
	case !dest.Exists:
		rentNeeded = c.splTokenAccountRent()
	case dest.State == "frozen":
		return "durable_filter_recipient_token_frozen"
	}

	src, err := c.rpcClient.GetTokenAccount(p.SourceATA.String(), "confirmed")
	switch {
	case err != nil:
		c.logger.Warn().Err(err).Str("token_account", p.SourceATA.String()).Msg("pre-sign filter: fail to read vault token account; not filtering")
	case !src.Exists || src.Amount.Cmp(new(big.Int).SetUint64(p.Amount)) < 0:
		return "durable_filter_vault_token_balance"
	case src.State == "frozen":
		return "durable_filter_vault_token_frozen"
	}

	fee := uint64(baseFeePerSigLamports) * uint64(len(solTx.Signatures))
	if reason := c.vaultLamportsReject(p.Vault.String(), fee+rentNeeded); reason != "" {
		return reason
	}
	return c.simulateReject(tx, solTx)
}
