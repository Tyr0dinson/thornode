package solana

import (
	"encoding/binary"
	"math/big"

	"github.com/mr-tron/base58"
	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// splTransfer is an SPL Token Transfer or TransferChecked instruction, top-level or
// invoked by another program (CPI).
type splTransfer struct {
	valid     bool // false when the instruction is a transfer but malformed
	source    int  // account index of the source token account
	dest      int  // account index of the destination token account
	mint      int  // account index of the mint, -1 for Transfer
	authority int  // account index of the owner or delegate that signed the transfer
	decimals  int  // -1 for Transfer
	amount    uint64
}

// parseSPLTransfer reports whether ins (already known to target the SPL Token
// program) is a Transfer or TransferChecked, and decodes it. A malformed transfer is
// still reported as a transfer (valid=false) so it counts toward the one-transfer
// rule instead of being silently ignored.
func parseSPLTransfer(ins rpc.RPCInstruction) (splTransfer, bool) {
	data, err := base58.Decode(ins.Data)
	if err != nil || len(data) == 0 {
		return splTransfer{}, false
	}
	switch data[0] {
	case types.TokenInstructionTransfer:
		// data: disc | amount u64 ; accounts: source, destination, authority(, signers)
		if len(data) < 9 || len(ins.Accounts) < 3 {
			return splTransfer{}, true
		}
		return splTransfer{
			valid:     true,
			source:    ins.Accounts[0],
			dest:      ins.Accounts[1],
			mint:      -1,
			authority: ins.Accounts[2],
			decimals:  -1,
			amount:    binary.LittleEndian.Uint64(data[1:9]),
		}, true
	case types.TokenInstructionTransferChecked:
		// data: disc | amount u64 | decimals u8 ; accounts: source, mint, destination, authority(, signers)
		if len(data) < 10 || len(ins.Accounts) < 4 {
			return splTransfer{}, true
		}
		return splTransfer{
			valid:     true,
			source:    ins.Accounts[0],
			mint:      ins.Accounts[1],
			dest:      ins.Accounts[2],
			authority: ins.Accounts[3],
			decimals:  int(data[9]),
			amount:    binary.LittleEndian.Uint64(data[1:9]),
		}, true
	default:
		return splTransfer{}, false
	}
}

// splTxInItem observes the SPL Token transfer of a whitelisted mint that a transaction
// makes to or from a vault, top-level or invoked by another program (CPI). Wallets
// commonly add a Jito tip, a fee in SOL or tokens, or run the transfer from a smart
// wallet; everything that touches no vault is ignored. Every field comes from the
// finalized transaction (its instructions, inner instructions and meta token
// balances), never from current account state, so every observer derives the same
// item.
//
//   - The transfer: the transaction's only token transfer, or, among several, the
//     only one into a vault's canonical token account or out of a vault's token
//     account. Two transfers into vaults are never guessed between.
//   - Sender / To are the OWNERS of the source / destination token accounts, so an
//     inbound credits the vault wallet and a refund targets the sender's wallet (see
//     splTransferSender). The destination's owner may not change within the
//     transaction, and the destination must be its owner's associated token account.
//   - The amount is the instruction amount, and the destination's balance must have
//     grown by exactly that much. That rejects a self-transfer, anything that credits
//     less than it debits, and any other credit to the destination in the same
//     transaction, so the vault is never credited more than it holds.
//   - Gas is the fee payer's total lamport spend when the fee payer is the sender.
//     For a vault outbound that is the fee plus any rent the vault paid to create
//     the recipient's associated token account, so the vault's SOL balance on
//     THORChain keeps matching the chain.
func (s *SOLScanner) splTxInItem(
	txn *rpc.TransactionResult,
	blockHeight int64,
	memo string,
	transfers []splTransfer,
	accounts []string,
	advancedNonceAcct string,
) (*stypes.TxInItem, error) {
	txID := txn.Transaction.Signatures[0]
	logger := s.logger.With().Str("txID", txID).Logger()
	accountByIndex := func(i int) string {
		if i < 0 || i >= len(accounts) {
			return ""
		}
		return accounts[i]
	}
	pre := rpc.ParseTokenBalances(txn.Meta.PreTokenBalances)
	post := rpc.ParseTokenBalances(txn.Meta.PostTokenBalances)

	t := transfers[0]
	if len(transfers) > 1 {
		var vaultTransfers []splTransfer
		for _, tr := range transfers {
			if s.splTransferTouchesVault(tr, pre, post, accountByIndex) {
				vaultTransfers = append(vaultTransfers, tr)
			}
		}
		if len(vaultTransfers) != 1 {
			logger.Warn().Int("tokenTransferCount", len(transfers)).Int("vaultTransferCount", len(vaultTransfers)).
				Msg("Rejected transaction without exactly one token transfer to or from a vault")
			return nil, nil
		}
		t = vaultTransfers[0]
	}
	if !t.valid {
		logger.Debug().Msg("malformed token transfer instruction")
		return nil, nil
	}
	if accountByIndex(t.source) == "" || accountByIndex(t.dest) == "" {
		logger.Debug().Msg("token transfer account index out of range")
		return nil, nil
	}

	dstPost, ok := rpc.FindTokenBalance(post, t.dest)
	if !ok {
		logger.Debug().Msg("no post token balance for transfer destination")
		return nil, nil
	}
	token, ok := s.spl.tokenByMint(dstPost.Mint)
	if !ok {
		logger.Debug().Str("mint", dstPost.Mint).Msg("token transfer of a non-whitelisted mint")
		return nil, nil
	}
	if dstPost.ProgramID != "" && dstPost.ProgramID != types.TokenProgramID.String() {
		logger.Debug().Str("program", dstPost.ProgramID).Msg("token account not owned by the SPL Token program")
		return nil, nil
	}
	if dstPost.UITokenAmount.Decimals != token.decimals ||
		(t.decimals >= 0 && t.decimals != token.decimals) ||
		(t.mint >= 0 && accountByIndex(t.mint) != dstPost.Mint) {
		logger.Warn().Str("mint", dstPost.Mint).Int("decimals", dstPost.UITokenAmount.Decimals).
			Int("whitelist_decimals", token.decimals).Msg("token transfer does not match the whitelisted mint")
		return nil, nil
	}

	sender, ok := s.splTransferSender(t, pre, post, dstPost.Mint, accountByIndex, logger)
	if !ok || dstPost.Owner == "" {
		logger.Debug().Msg("cannot resolve token transfer source or destination owner")
		return nil, nil
	}
	// While HaltSOLTokens is in force a token deposit from outside the vaults is not
	// observed here: the vault token account's scan, paused meanwhile, observes it once
	// the halt is cleared. A vault's own transfer is observed regardless.
	if s.spl.isHalted() && !s.isVaultAddress(sender) {
		logger.Debug().Str("sender", sender).Msg("SPL halted: token deposit left for its token account's scan")
		return nil, nil
	}

	// Only credit the owner's canonical associated token account. Anyone can create a
	// token account owned by a vault (or hand one to it); Bifrost neither scans nor
	// spends from those, so crediting the vault would book funds it cannot pay out.
	dstPre, dstExisted := rpc.FindTokenBalance(pre, t.dest)
	if dstExisted && dstPre.Owner != dstPost.Owner {
		logger.Warn().Str("pre_owner", dstPre.Owner).Str("post_owner", dstPost.Owner).
			Msg("token transfer destination owner changed within the transaction")
		return nil, nil
	}
	dstOwner, err := types.PublicKeyFromStringStrict(dstPost.Owner)
	if err != nil {
		return nil, nil
	}
	canonical, err := types.FindAssociatedTokenAddress(dstOwner, token.mint)
	if err != nil || canonical.String() != accountByIndex(t.dest) {
		logger.Debug().Str("destination", accountByIndex(t.dest)).Str("owner", dstPost.Owner).
			Msg("token transfer destination is not the owner's associated token account")
		return nil, nil
	}

	amount := new(big.Int).SetUint64(t.amount)
	postAmt, ok := dstPost.RawAmount()
	if !ok {
		return nil, nil
	}
	preAmt := big.NewInt(0)
	if dstExisted {
		if preAmt, ok = dstPre.RawAmount(); !ok {
			return nil, nil
		}
	}
	if amount.Sign() == 0 || new(big.Int).Sub(postAmt, preAmt).Cmp(amount) != 0 {
		logger.Debug().Stringer("amount", amount).Stringer("pre", preAmt).Stringer("post", postAmt).
			Msg("token destination balance change does not match the transfer amount")
		return nil, nil
	}
	// The token's minimum deposit floors deposits from outside the vaults, as the dust
	// threshold does for SOL: otherwise a deposit costs only the transaction fee. The
	// vaults' own transfers are observed whatever their amount.
	if t.amount < token.minDeposit && !s.isVaultAddress(sender) {
		logger.Debug().Uint64("amount", t.amount).Uint64("min_deposit", token.minDeposit).
			Msg("token deposit below the minimum deposit")
		return nil, nil
	}
	coinAmount, err := splToTHORChain(amount, token.decimals)
	if err != nil || coinAmount.IsZero() {
		logger.Debug().Stringer("amount", amount).Msg("token transfer amount below THORChain precision")
		return nil, nil
	}

	gasLamports := txn.Meta.Fee
	if accountByIndex(0) == sender && len(txn.Meta.PreBalances) > 0 && len(txn.Meta.PostBalances) > 0 &&
		txn.Meta.PreBalances[0] > txn.Meta.PostBalances[0] {
		if spent := txn.Meta.PreBalances[0] - txn.Meta.PostBalances[0]; spent > gasLamports {
			gasLamports = spent
		}
	}

	coin := common.NewCoin(token.asset, coinAmount)
	coin.Decimals = int64(token.decimals)
	gasCoin := common.NewCoin(common.SOLAsset, convertLamportsToTHORChain(gasLamports))
	gasCoin.Decimals = 9

	return &stypes.TxInItem{
		BlockHeight: blockHeight,
		Tx:          txID,
		Memo:        memo,
		Sender:      sender,
		To:          dstPost.Owner,
		Coins:       []common.Coin{coin},
		Gas:         common.Gas{gasCoin},
		SolNonce:    s.observedSolNonce(advancedNonceAcct, sender, blockHeight, txID),
	}, nil
}

// isVaultAddress reports whether addr is one of THORChain's SOL vaults. A scanner
// without a pubkey manager (unit tests) has no vaults.
func (s *SOLScanner) isVaultAddress(addr string) bool {
	if s.pubKeyMgr == nil {
		return false
	}
	ok, _ := s.pubKeyMgr.IsValidPoolAddress(addr, common.SOLChain)
	return ok
}

// splTransferSender returns who authorized token transfer t: the source token
// account's owner BEFORE the transaction. SPL lets a transaction reassign a token
// account's owner (SetAuthority AccountOwner), so the post-transaction owner of an
// account an outsider controlled can be any address, including a vault: trusting it
// would observe a vault outbound the vault never signed. So a source whose owner or
// mint changes within the transaction is not observed.
//
// A source created within the transaction (an aggregator's output, a wallet's first
// token account) has no prior owner. Its owner is trusted only when that owner signed
// the transfer itself: an owner set after the transfer, or a transfer by a delegate,
// is refused, as is a source created and closed again within the transaction. Vaults
// never send from such an account, so a vault owner is refused too.
func (s *SOLScanner) splTransferSender(
	t splTransfer,
	pre, post []rpc.RPCTokenBalance,
	mint string,
	accountByIndex func(int) string,
	logger zerolog.Logger,
) (string, bool) {
	srcPost, hasPost := rpc.FindTokenBalance(post, t.source)
	if src, hasPre := rpc.FindTokenBalance(pre, t.source); hasPre {
		if src.Mint != mint || src.Owner == "" {
			return "", false
		}
		if hasPost && (srcPost.Owner != src.Owner || srcPost.Mint != src.Mint) {
			logger.Warn().Str("pre_owner", src.Owner).Str("post_owner", srcPost.Owner).
				Msg("token transfer source owner changed within the transaction")
			return "", false
		}
		return src.Owner, true
	}
	if !hasPost || srcPost.Mint != mint || srcPost.Owner == "" {
		return "", false
	}
	if accountByIndex(t.authority) != srcPost.Owner || s.isVaultAddress(srcPost.Owner) {
		logger.Warn().Str("owner", srcPost.Owner).Str("authority", accountByIndex(t.authority)).
			Msg("token transfer from a source created in the transaction was not signed by its owner")
		return "", false
	}
	return srcPost.Owner, true
}

// splTransferTouchesVault reports whether token transfer t moves tokens into a vault's
// canonical token account for a whitelisted mint, or out of a token account a vault
// owned before the transaction. Among several token transfers, only such a transfer is
// observed.
func (s *SOLScanner) splTransferTouchesVault(t splTransfer, pre, post []rpc.RPCTokenBalance, accountByIndex func(int) string) bool {
	if !t.valid {
		return false
	}
	if src, ok := rpc.FindTokenBalance(pre, t.source); ok && s.isVaultAddress(src.Owner) {
		return true
	}
	dst, ok := rpc.FindTokenBalance(post, t.dest)
	if !ok || !s.isVaultAddress(dst.Owner) {
		return false
	}
	token, ok := s.spl.tokenByMint(dst.Mint)
	if !ok {
		return false
	}
	owner, err := types.PublicKeyFromStringStrict(dst.Owner)
	if err != nil {
		return false
	}
	canonical, err := types.FindAssociatedTokenAddress(owner, token.mint)
	return err == nil && canonical.String() == accountByIndex(t.dest)
}

// splInnerInstructions returns the SPL Token transfers among the transaction's inner
// instructions (CPIs), in execution order, and the last memo written through one.
// Smart wallets and other programs move tokens and write memos this way.
func (s *SOLScanner) splInnerInstructions(txn *rpc.TransactionResult, accountByIndex func(int) string) ([]splTransfer, string) {
	var transfers []splTransfer
	memo := ""
	for _, ins := range rpc.ParseInnerInstructions(txn.Meta.InnerInstructions) {
		programID := accountByIndex(ins.ProgramIdIndex)
		switch {
		case types.TokenProgramID.EqualsString(programID):
			if transfer, isTransfer := parseSPLTransfer(ins); isTransfer {
				transfers = append(transfers, transfer)
			}
		case types.MemoProgramID.EqualsString(programID):
			memoBytes, err := base58.Decode(ins.Data)
			if err != nil {
				s.logger.Error().Err(err).Str("memoData", ins.Data).Msg("Failed to decode inner memo")
				continue
			}
			memo = string(memoBytes)
		}
	}
	return transfers, memo
}

// solTransfersTouchVault reports whether any of a transaction's top-level SystemProgram
// transfers is from or to a vault.
func (s *SOLScanner) solTransfersTouchVault(transfers []solTransfer) bool {
	for _, t := range transfers {
		if s.isVaultAddress(t.from) || s.isVaultAddress(t.to) {
			return true
		}
	}
	return false
}
