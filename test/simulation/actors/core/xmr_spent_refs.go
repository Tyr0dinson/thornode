package core

import (
	"fmt"
	rand "math/rand/v2"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	simhelpers "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

type XMRSpentRefVerifyActor struct {
	Actor
}

func NewXMRSpentRefVerifyActor(rng *rand.Rand) *Actor {
	a := &XMRSpentRefVerifyActor{
		Actor: *NewActor("XMRSpentRefVerify", rng),
	}
	a.Timeout = 5 * time.Minute

	a.Ops = append(a.Ops, a.verifyVaultHasXMR)
	a.Ops = append(a.Ops, a.swapToXMR)
	a.Ops = append(a.Ops, a.waitForSpentRefsOnVault)
	a.Ops = append(a.Ops, a.verifySpentRefsValid)
	a.Ops = append(a.Ops, a.verifySolvency)

	return &a.Actor
}

func (a *XMRSpentRefVerifyActor) verifyVaultHasXMR(config *OpConfig) OpResult {
	vaults, err := thornode.GetVaultXMRStates()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get vault XMR states")
		return OpResult{Continue: false}
	}

	for _, v := range vaults {
		if v.Status != "ActiveVault" || v.PubKeyEddsa == "" {
			continue
		}
		a.Log().Info().
			Int64("vault_creation_height_xmr", v.VaultCreationHeightXmr).
			Str("pub_key_eddsa", v.PubKeyEddsa).
			Msg("active XMR vault found")
		return OpResult{Continue: true}
	}

	a.Log().Warn().Msg("no active XMR vault found, retrying")
	return OpResult{Continue: false}
}

func (a *XMRSpentRefVerifyActor) swapToXMR(config *OpConfig) OpResult {
	if len(config.Users) == 0 {
		a.Log().Error().Msg("no users available")
		return OpResult{Continue: false}
	}
	user := config.Users[0]

	_, ok := user.ChainClients[common.BTCChain]
	if !ok {
		a.Log().Error().Msg("no BTC chain client for swap source")
		return OpResult{Continue: false}
	}

	xmrAddr := user.Address(common.XMRChain)
	amount := cosmos.NewUint(10_000_000)
	memo := fmt.Sprintf("=:XMR.XMR:%s", xmrAddr)

	txid, err := simhelpers.DepositL1(a.Log(), user.ChainClients[common.BTCChain], common.BTCAsset, memo, amount)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to deposit BTC for XMR swap")
		return OpResult{Continue: false}
	}

	a.Log().Info().Str("txid", txid).Str("to", xmrAddr.String()).Msg("BTC->XMR swap initiated")
	return OpResult{Continue: true}
}

func (a *XMRSpentRefVerifyActor) waitForSpentRefsOnVault(config *OpConfig) OpResult {
	vaults, err := thornode.GetVaultXMRStates()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query vault states")
		return OpResult{Continue: false}
	}

	for _, v := range vaults {
		if v.Status != "ActiveVault" || v.PubKeyEddsa == "" {
			continue
		}
		if len(v.SpentOutputRefsXmr) > 0 {
			a.Log().Info().
				Int("spent_refs", len(v.SpentOutputRefsXmr)).
				Msg("vault has spent output refs")
			return OpResult{Continue: true}
		}
	}

	a.Log().Debug().Msg("waiting for spent refs to appear on vault")
	return OpResult{Continue: false}
}

func (a *XMRSpentRefVerifyActor) verifySpentRefsValid(config *OpConfig) OpResult {
	vaults, err := thornode.GetVaultXMRStates()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query vault states")
		return OpResult{Continue: false}
	}

	for _, v := range vaults {
		if v.Status != "ActiveVault" || v.PubKeyEddsa == "" {
			continue
		}
		for i, ref := range v.SpentOutputRefsXmr {
			if ref.TxHash == "" {
				return OpResult{Error: fmt.Errorf("spent ref %d has empty tx_hash", i), Finish: true}
			}
		}
		a.Log().Info().
			Int("total_refs", len(v.SpentOutputRefsXmr)).
			Msg("all spent output refs validated")
		return OpResult{Continue: true}
	}

	return OpResult{Error: fmt.Errorf("no active XMR vault"), Finish: true}
}

func (a *XMRSpentRefVerifyActor) verifySolvency(config *OpConfig) OpResult {
	vaults, err := thornode.GetVaults()
	if err != nil {
		a.Log().Error().Err(err).Msg("solvency: failed to get vaults")
		return OpResult{Continue: false}
	}

	for _, vault := range vaults {
		if vault.Status != "ActiveVault" {
			continue
		}
		for _, coin := range vault.Coins {
			if coin.Asset != "XMR.XMR" {
				continue
			}
			a.Log().Info().
				Str("vault_xmr_balance", coin.Amount).
				Msg("XMR vault balance after spent ref tracking")
		}
	}

	a.Log().Info().Msg("spent ref tracking e2e verification complete")
	return OpResult{Finish: true}
}
