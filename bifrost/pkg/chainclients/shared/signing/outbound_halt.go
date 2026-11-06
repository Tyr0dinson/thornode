package signing

// HALTOUTBOUND / HaltOutbound<Chain> gating.
//
// HaltOutbound stops value leaving the network without stopping the churn. Under
// it bifrost still signs INTERNAL vault-to-vault transfers (churn migrations and
// UTXO/Monero/Solana self-consolidations) and refuses everything else: swaps,
// refunds, withdrawals, ragnarok, and any bifrost-autonomous transaction that is
// not a self-transfer.
//
// "Internal" is defined strictly and fails closed (see IsInternalTxOut): the memo
// must parse as MIGRATE or CONSOLIDATE AND the destination must be the address of
// an Active or Retiring Asgard vault on that chain. A self-send without one of
// those memos (an EVM unstuck/cancel transaction, say) is not internal on its
// own; callers that sign such transactions classify the outbound they belong to.
//
// Unlike HALTSIGNING, HaltOutbound does not pause the churn on THORChain: keeping
// migrations flowing is the point of it.

import (
	"fmt"
	"strings"

	"github.com/rs/zerolog"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// OutboundHaltChecker is the slice of thorclient.ThorchainBridge the outbound
// halt needs: the mimir reads plus the Asgard set used to recognise internal
// transfers.
type OutboundHaltChecker interface {
	HaltChecker
	GetAsgards() (ttypes.Vaults, error)
}

// CheckOutboundHalt reports whether HALTOUTBOUND (global) or HaltOutbound<Chain>
// is active at the given THORChain height. Like CheckSigningHalt it does no
// logging and leaves the error policy to the caller.
func CheckOutboundHalt(bridge HaltChecker, chain common.Chain, height int64) (halted, global bool, err error) {
	haltGlobal, err := bridge.GetMimir(constants.MimirKeyHaltOutbound)
	if err != nil {
		return false, false, fmt.Errorf("fail to get %s mimir: %w", constants.MimirKeyHaltOutbound, err)
	}
	if haltGlobal > 0 && haltGlobal <= height {
		return true, true, nil
	}
	chainKey := fmt.Sprintf(constants.MimirTemplateHaltOutbound, chain)
	haltChain, err := bridge.GetMimir(chainKey)
	if err != nil {
		return false, false, fmt.Errorf("fail to get %s mimir: %w", chainKey, err)
	}
	if haltChain > 0 && haltChain <= height {
		return true, false, nil
	}
	return false, false, nil
}

// isInternalMemo reports whether memo parses as a MIGRATE or CONSOLIDATE memo.
func isInternalMemo(memo string) bool {
	memo = strings.TrimSpace(memo)
	if memo == "" {
		return false
	}
	parsed, err := mem.ParseMemo(common.LatestVersion, memo)
	if err != nil {
		return false
	}
	return parsed.IsType(mem.TxMigrate) || parsed.IsType(mem.TxConsolidate)
}

// IsInternalTxOut reports whether tx is a vault-to-vault transfer that
// HaltOutbound lets through. It is internal only when:
//
//   - every memo it carries (Memo and, when set, OriginalMemo, the memo a
//     memoless outbound keeps for the signer) parses as MIGRATE or CONSOLIDATE,
//     and at least one of them is set; and
//   - ToAddress is the address, on tx.Chain, of an Asgard vault in vaults whose
//     status is Active or Retiring.
//
// Anything else, including an empty memo, an unparsable memo, an address that
// only matches an Inactive vault, or an address that cannot be derived, is
// external.
func IsInternalTxOut(tx stypes.TxOutItem, vaults ttypes.Vaults) bool {
	if tx.Memo == "" && tx.OriginalMemo == "" {
		return false
	}
	if tx.Memo != "" && !isInternalMemo(tx.Memo) {
		return false
	}
	if tx.OriginalMemo != "" && !isInternalMemo(tx.OriginalMemo) {
		return false
	}
	return IsAsgardAddress(tx.Chain, tx.ToAddress, vaults)
}

// IsAsgardAddress reports whether addr is the address, on chain, of an Active or
// Retiring Asgard vault in vaults.
func IsAsgardAddress(chain common.Chain, addr common.Address, vaults ttypes.Vaults) bool {
	if addr.IsEmpty() || chain.IsEmpty() {
		return false
	}
	for i := range vaults {
		v := &vaults[i]
		if v.Type != ttypes.VaultType_AsgardVault {
			continue
		}
		if v.Status != ttypes.VaultStatus_ActiveVault && v.Status != ttypes.VaultStatus_RetiringVault {
			continue
		}
		vaultAddr, err := v.GetAddress(chain)
		if err != nil || vaultAddr.IsEmpty() {
			continue
		}
		if common.AddressEquals(chain, vaultAddr, addr) {
			return true
		}
	}
	return false
}

// OutboundHaltBlocks reports whether HaltOutbound forbids signing tx at height.
// It returns false when no outbound halt is active, without touching the vault
// set. Under an active halt it fetches the Asgard vaults and blocks unless
// IsInternalTxOut accepts tx. Any read error is returned and the caller must
// not sign (fail closed). The halt that applies is tx.Chain's.
func OutboundHaltBlocks(bridge OutboundHaltChecker, tx stypes.TxOutItem, height int64) (blocked, global bool, err error) {
	halted, global, err := CheckOutboundHalt(bridge, tx.Chain, height)
	if err != nil || !halted {
		return false, false, err
	}
	vaults, err := bridge.GetAsgards()
	if err != nil {
		return false, global, fmt.Errorf("fail to get asgard vaults for outbound halt: %w", err)
	}
	return !IsInternalTxOut(tx, vaults), global, nil
}

// IsOutboundHalted is the retry-loop wrapper over CheckOutboundHalt, in the style
// of IsSigningHalted: it reads the current THORChain height itself, logs, and
// treats any read error as halted. Use it for bifrost-autonomous signing that is
// never internal (for example Solana durable-nonce provisioning).
func IsOutboundHalted(bridge HaltChecker, chain common.Chain, logger zerolog.Logger) bool {
	height, err := bridge.GetBlockHeight()
	if err != nil {
		logger.Err(err).Stringer("chain", chain).Msg("fail to get thorchain block height, treating outbound as halted")
		return true
	}
	halted, global, err := CheckOutboundHalt(bridge, chain, height)
	if err != nil {
		logger.Err(err).Stringer("chain", chain).Msg("fail to check outbound halt, treating outbound as halted")
		return true
	}
	logOutboundHalt(logger, chain, halted, global)
	return halted
}

// IsTxOutBlockedByOutboundHalt is the retry-loop wrapper over OutboundHaltBlocks
// for bifrost-autonomous signing of a TxOutItem (consolidation, keep-alive, EVM
// unstuck). It reads the current height, logs, and treats any read error as
// blocked.
func IsTxOutBlockedByOutboundHalt(bridge OutboundHaltChecker, tx stypes.TxOutItem, logger zerolog.Logger) bool {
	height, err := bridge.GetBlockHeight()
	if err != nil {
		logger.Err(err).Stringer("chain", tx.Chain).Msg("fail to get thorchain block height, treating outbound as halted")
		return true
	}
	blocked, global, err := OutboundHaltBlocks(bridge, tx, height)
	if err != nil {
		logger.Err(err).Stringer("chain", tx.Chain).Msg("fail to check outbound halt, treating outbound as halted")
		return true
	}
	logOutboundHalt(logger, tx.Chain, blocked, global)
	return blocked
}

func logOutboundHalt(logger zerolog.Logger, chain common.Chain, halted, global bool) {
	switch {
	case halted && global:
		logger.Info().Stringer("chain", chain).Msg("external outbound skipped: HALTOUTBOUND is active")
	case halted:
		logger.Info().Stringer("chain", chain).Msgf("external outbound skipped: outbound for %s is halted", chain)
	}
}

// IsCancelBlockedByOutboundHalt decides whether HaltOutbound forbids signing an
// EVM unstuck/cancel transaction. A cancel is a zero-value self-send with no
// memo, so it is never internal on its own; it is classified by the stuck
// outbound it replaces. Only a stuck internal transfer (a migration) may be
// cancelled under HaltOutbound, which keeps the churn moving; a stuck external
// outbound is left alone, as it is under HALTSIGNING. A missing stuck item is
// external (fail closed). chain overrides the stuck item's chain so the halt key
// and vault addresses are always the signing client's.
func IsCancelBlockedByOutboundHalt(bridge OutboundHaltChecker, chain common.Chain, stuck *stypes.TxOutItem, logger zerolog.Logger) bool {
	var tx stypes.TxOutItem
	if stuck != nil {
		tx = *stuck
	}
	tx.Chain = chain
	return IsTxOutBlockedByOutboundHalt(bridge, tx, logger)
}
