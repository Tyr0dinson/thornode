package solana

import (
	"encoding/binary"
	"math/big"

	"github.com/mr-tron/base58"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/rpc"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

// splTransfer is a top-level SPL Token Transfer or TransferChecked instruction.
type splTransfer struct {
	valid    bool // false when the instruction is a transfer but malformed
	source   int  // account index of the source token account
	dest     int  // account index of the destination token account
	mint     int  // account index of the mint, -1 for Transfer
	decimals int  // -1 for Transfer
	amount   uint64
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
			valid:    true,
			source:   ins.Accounts[0],
			dest:     ins.Accounts[1],
			mint:     -1,
			decimals: -1,
			amount:   binary.LittleEndian.Uint64(data[1:9]),
		}, true
	case types.TokenInstructionTransferChecked:
		// data: disc | amount u64 | decimals u8 ; accounts: source, mint, destination, authority(, signers)
		if len(data) < 10 || len(ins.Accounts) < 4 {
			return splTransfer{}, true
		}
		return splTransfer{
			valid:    true,
			source:   ins.Accounts[0],
			mint:     ins.Accounts[1],
			dest:     ins.Accounts[2],
			decimals: int(data[9]),
			amount:   binary.LittleEndian.Uint64(data[1:9]),
		}, true
	default:
		return splTransfer{}, false
	}
}

// splTxInItem observes a transaction whose only value transfer is a single top-level
// SPL Token transfer of a whitelisted mint. Every field comes from the finalized
// transaction (its instructions and meta token balances), never from current account
// state, so every observer derives the same item.
//
//   - Sender / To are the OWNERS of the source / destination token accounts, so an
//     inbound credits the vault wallet and a refund targets the sender's wallet.
//   - The amount is the instruction amount, and the destination's balance must have
//     grown by exactly that much. That rejects a self-transfer and anything that
//     credits less than it debits, so the vault is never credited more than it holds.
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
	if len(transfers) != 1 {
		logger.Warn().Int("tokenTransferCount", len(transfers)).Msg("Rejected transaction with multiple token transfer instructions")
		return nil, nil
	}
	t := transfers[0]
	if !t.valid {
		logger.Debug().Msg("malformed token transfer instruction")
		return nil, nil
	}
	accountAt := func(i int) string {
		if i < 0 || i >= len(accounts) {
			return ""
		}
		return accounts[i]
	}
	if accountAt(t.source) == "" || accountAt(t.dest) == "" {
		logger.Debug().Msg("token transfer account index out of range")
		return nil, nil
	}

	pre := rpc.ParseTokenBalances(txn.Meta.PreTokenBalances)
	post := rpc.ParseTokenBalances(txn.Meta.PostTokenBalances)

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
		(t.mint >= 0 && accountAt(t.mint) != dstPost.Mint) {
		logger.Warn().Str("mint", dstPost.Mint).Int("decimals", dstPost.UITokenAmount.Decimals).
			Int("whitelist_decimals", token.decimals).Msg("token transfer does not match the whitelisted mint")
		return nil, nil
	}

	src, ok := rpc.FindTokenBalance(post, t.source)
	if !ok {
		// The source may have been closed later in the same transaction.
		src, ok = rpc.FindTokenBalance(pre, t.source)
	}
	if !ok || src.Mint != dstPost.Mint || src.Owner == "" || dstPost.Owner == "" {
		logger.Debug().Msg("cannot resolve token transfer source or destination owner")
		return nil, nil
	}

	amount := new(big.Int).SetUint64(t.amount)
	postAmt, ok := dstPost.RawAmount()
	if !ok {
		return nil, nil
	}
	preAmt := big.NewInt(0)
	if dstPre, found := rpc.FindTokenBalance(pre, t.dest); found {
		if preAmt, ok = dstPre.RawAmount(); !ok {
			return nil, nil
		}
	}
	if amount.Sign() == 0 || new(big.Int).Sub(postAmt, preAmt).Cmp(amount) != 0 {
		logger.Debug().Stringer("amount", amount).Stringer("pre", preAmt).Stringer("post", postAmt).
			Msg("token destination balance change does not match the transfer amount")
		return nil, nil
	}
	coinAmount, err := splToTHORChain(amount, token.decimals)
	if err != nil || coinAmount.IsZero() {
		logger.Debug().Stringer("amount", amount).Msg("token transfer amount below THORChain precision")
		return nil, nil
	}

	gasLamports := txn.Meta.Fee
	if accountAt(0) == src.Owner && len(txn.Meta.PreBalances) > 0 && len(txn.Meta.PostBalances) > 0 &&
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
		Sender:      src.Owner,
		To:          dstPost.Owner,
		Coins:       []common.Coin{coin},
		Gas:         common.Gas{gasCoin},
		SolNonce:    s.observedSolNonce(advancedNonceAcct, src.Owner, blockHeight, txID),
	}, nil
}
