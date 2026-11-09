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
// those memos is not internal on its own. The exception is an EVM unstuck/cancel
// transaction: a zero-value self-send that moves no value, which is allowed
// whenever its stuck TxOutItem is known (see IsCancelBlockedByOutboundHalt).
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
	return checkOutboundHalt(bridge.GetMimir, chain, height)
}

// MimirAtHeightReader reads a mimir as of a consensus height (the bridge's
// ?height= state query), so every member of a keysign party sees the same value.
type MimirAtHeightReader interface {
	GetMimirAtHeight(key string, height int64) (int64, error)
}

// CheckOutboundHaltAtHeight is CheckOutboundHalt with both mimirs read as of
// height instead of live. Use it where all members of a keysign party must take
// the same decision from one snapshot.
func CheckOutboundHaltAtHeight(bridge MimirAtHeightReader, chain common.Chain, height int64) (halted, global bool, err error) {
	return checkOutboundHalt(func(key string) (int64, error) {
		return bridge.GetMimirAtHeight(key, height)
	}, chain, height)
}

func checkOutboundHalt(getMimir func(string) (int64, error), chain common.Chain, height int64) (halted, global bool, err error) {
	haltGlobal, err := getMimir(constants.MimirKeyHaltOutbound)
	if err != nil {
		return false, false, fmt.Errorf("fail to get %s mimir: %w", constants.MimirKeyHaltOutbound, err)
	}
	if haltGlobal > 0 && haltGlobal <= height {
		return true, true, nil
	}
	chainKey := fmt.Sprintf(constants.MimirTemplateHaltOutbound, chain)
	haltChain, err := getMimir(chainKey)
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

// asgardsAtHeightReader is implemented by the production thorchain bridge.
type asgardsAtHeightReader interface {
	GetAsgardsAtHeight(height int64) (ttypes.Vaults, error)
}

// OutboundHaltBlocks reports whether HaltOutbound forbids signing tx at height.
// It returns false when no outbound halt is active, without touching the vault
// set. Under an active halt it fetches the Asgard vaults and blocks unless
// IsInternalTxOut accepts tx. Any read error is returned and the caller must
// not sign (fail closed). The halt that applies is tx.Chain's.
//
// The vaults are read as of height when the bridge supports it, not from the
// live one-block cache: in the rotation block a cached pre-rotation list lacks
// the new Active vault, and a round-1 MIGRATE to it would be refused.
func OutboundHaltBlocks(bridge OutboundHaltChecker, tx stypes.TxOutItem, height int64) (blocked, global bool, err error) {
	halted, global, err := CheckOutboundHalt(bridge, tx.Chain, height)
	if err != nil || !halted {
		return false, false, err
	}
	var vaults ttypes.Vaults
	if r, ok := bridge.(asgardsAtHeightReader); ok {
		vaults, err = r.GetAsgardsAtHeight(height)
	} else {
		vaults, err = bridge.GetAsgards()
	}
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
// for bifrost-autonomous signing of a TxOutItem (consolidation, keep-alive). It
// reads the current height, logs, and treats any read error as blocked.
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
// EVM unstuck/cancel transaction for a stuck outbound. A cancel is a zero-value
// self-send at the stuck transaction's nonce: it moves no value, so it is
// allowed under HaltOutbound whatever the stuck outbound was. Cancelling a stuck
// external payment also serves the halt, because it removes a live copy of the
// payment, and it frees the nonce so later migrations from the vault can land.
// The one case refused is a stuck entry without its TxOutItem: unstuckTx needs
// the item to sign, so under an active halt (or when the halt cannot be read)
// that entry is left alone (fail closed). chain is the signing client's chain
// and selects the halt key.
func IsCancelBlockedByOutboundHalt(bridge HaltChecker, chain common.Chain, stuck *stypes.TxOutItem, logger zerolog.Logger) bool {
	if stuck != nil {
		return false
	}
	height, err := bridge.GetBlockHeight()
	if err != nil {
		logger.Err(err).Stringer("chain", chain).Msg("fail to get thorchain block height, not cancelling stuck tx without its outbound item")
		return true
	}
	halted, _, err := CheckOutboundHalt(bridge, chain, height)
	if err != nil {
		logger.Err(err).Stringer("chain", chain).Msg("fail to check outbound halt, not cancelling stuck tx without its outbound item")
		return true
	}
	if halted {
		logger.Info().Stringer("chain", chain).Msg("outbound is halted: not cancelling stuck tx without its outbound item")
	}
	return halted
}
