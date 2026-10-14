package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// frostLTCKeygenActive reports whether asgard keygens are expected to produce
// a FROST (Taproot) key for LTC at this height: the build supports Taproot
// vaults for LTC, EnableFrostLTC is set, and HaltFrostLTCKeygen is not active.
// Mainnet and chainnet builds always return false.
func frostLTCKeygenActive(ctx cosmos.Context, k keeper.Keeper) bool {
	if !common.LTCChain.SupportsTaprootVaults() {
		return false
	}
	enabled, err := k.GetMimir(ctx, constants.MimirKeyEnableFrostLTC)
	if err != nil || enabled <= 0 {
		return false
	}
	halt, err := k.GetMimir(ctx, constants.MimirKeyHaltFrostLTCKeygen)
	if err != nil {
		return false
	}
	if halt > 0 && halt <= ctx.BlockHeight() {
		return false
	}
	return true
}

// validateAsgardKeygenFrost checks the FROST pool pubkey of an asgard keygen
// report against the keygen policy.
//
// While frostLTCKeygenActive, a successful report whose chain set includes LTC
// must carry the FROST pubkey (a failed LTC DKG fails the keygen
// instead of producing a vault that silently lacks LTC), and that key must be a
// secp256k1 Taproot output key distinct from the ECDSA pool key. While it is
// not active, no asgard report may carry one, which keeps today's behaviour on
// every other build and on stagenet before the opt-in.
func validateAsgardKeygenFrost(ctx cosmos.Context, k keeper.Keeper, msg *MsgTssPool) error {
	active := frostLTCKeygenActive(ctx, k)
	chains := msg.GetChains()
	if msg.PoolPubKeyFrost.IsEmpty() {
		if active && msg.IsSuccess() {
			if chains.Has(common.LTCChain) {
				return cosmos.ErrUnknownRequest("asgard keygen must set the frost pool pubkey for LTC while EnableFrostLTC is active")
			}
			// Leaving LTC out of the report is not a way around the FROST
			// requirement: while the network serves LTC, a successful keygen
			// must keep serving it, so a failed LTC DKG has to be reported as a
			// failure with blame rather than as a vault without LTC.
			if activeAsgardsServeLTC(ctx, k) {
				return cosmos.ErrUnknownRequest("asgard keygen must include LTC and its frost pool pubkey while EnableFrostLTC is active and the active vaults serve LTC")
			}
		}
		return nil
	}
	if !active {
		return cosmos.ErrUnknownRequest("asgard keygen must not set frost pool pubkey")
	}
	if !msg.IsSuccess() {
		return cosmos.ErrUnknownRequest("failed asgard keygen must not set frost pool pubkey")
	}
	if !chains.Has(common.LTCChain) {
		return cosmos.ErrUnknownRequest("asgard keygen frost pool pubkey requires LTC in the chain set")
	}
	if msg.PoolPubKeyFrost.Equals(msg.PoolPubKey) {
		return cosmos.ErrUnknownRequest("frost pool pubkey must differ from the ecdsa pool pubkey")
	}
	algo, err := msg.PoolPubKeyFrost.KeyAlgo()
	if err != nil || algo != common.SigningAlgoSecp256k1 {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("frost pool pubkey must be a secp256k1 key (algo %q, err %v)", algo, err))
	}
	if _, err := msg.PoolPubKeyFrost.GetAddressForAlgo(common.LTCChain, common.SigningAlgoSecp256k1Taproot); err != nil {
		return cosmos.ErrUnknownRequest(fmt.Sprintf("frost pool pubkey has no LTC taproot address: %v", err))
	}
	return nil
}

// activeAsgardsServeLTC reports whether any active asgard vault lists LTC in
// its chain set. A read failure counts as serving LTC: failing a keygen and
// retrying is recoverable, creating a vault without LTC is not.
func activeAsgardsServeLTC(ctx cosmos.Context, k keeper.Keeper) bool {
	vaults, err := k.GetAsgardVaultsByStatus(ctx, ActiveVault)
	if err != nil {
		ctx.Logger().Error("fail to get active asgard vaults for the LTC keygen policy", "error", err)
		return true
	}
	for _, vault := range vaults {
		if vault.GetChains().Has(common.LTCChain) {
			return true
		}
	}
	return false
}
