package features

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// SpecialAddressOutbounds
////////////////////////////////////////////////////////////////////////////////////////

func SpecialAddressOutbounds() *Actor {
	a := NewActor("SpecialAddressOutbounds")

	// BTC and LTC Taproot addresses
	a.Children[NewSpecialAddressOutboundActor(common.BTCAsset, "bcrt1pe9jf8v94pcrjcu56mal7qwmg5nfnyvwmf5gkm2laat09rp4gfpkqczmpr6")] = true
	a.Children[NewSpecialAddressOutboundActor(common.LTCAsset, "rltc1pfjprj08c0q3fnja438st6tsnvwzwgpwkfaar535m4ald6rms8hssy3s7rp")] = true

	return a
}

////////////////////////////////////////////////////////////////////////////////////////
// SpecialAddressOutboundActor
////////////////////////////////////////////////////////////////////////////////////////

type SpecialAddressOutboundActor struct {
	Actor
	account    *User
	asset      common.Asset
	address    string
	runeAmount cosmos.Uint
	txid       common.TxID
}

func NewSpecialAddressOutboundActor(targetAsset common.Asset, targetAddress string) *Actor {
	a := &SpecialAddressOutboundActor{
		Actor:   *NewActor(fmt.Sprintf("Feature-SpecialAddressOutbound-%s", targetAsset)),
		asset:   targetAsset,
		address: targetAddress,
	}

	a.SetLogger(a.Log().With().Stringer("asset", targetAsset).Logger())

	// lock a user that has sufficient RUNE balance
	a.Ops = append(a.Ops, a.acquireUser)

	// send the swap
	a.Ops = append(a.Ops, a.swap)

	// ensure the saver is ejected and release the account
	a.Ops = append(a.Ops, a.verifySpecialAddressOutbound)

	return &a.Actor
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *SpecialAddressOutboundActor) acquireUser(config *OpConfig) OpResult {
	// determine the asset amount
	pool, err := thornode.GetPool(a.asset)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get pool")
		return OpResult{
			Continue: false,
		}
	}

	// amount is 1% of the pool asset depth
	a.runeAmount = cosmos.NewUintFromString(pool.BalanceRune).QuoUint64(100)

	for _, user := range config.Users {
		a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())

		// skip users already being used
		if !user.Acquire() {
			continue
		}

		// skip users that with insufficient RUNE balance
		thorAddress, err := user.PubKey(common.THORChain).GetAddress(common.THORChain)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get thor address")
			user.Release()
			continue
		}
		thorBalances, err := thornode.GetBalances(thorAddress)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get thorchain balances")
			user.Release()
			continue
		}
		if thorBalances.GetCoin(common.RuneAsset()).Amount.LT(a.runeAmount) {
			a.Log().Error().Msg("user has insufficient RUNE balance")
			user.Release()
			continue
		}

		a.account = user
		break
	}

	// remain pending if no user is available
	return OpResult{
		Continue: a.account != nil,
	}
}

func (a *SpecialAddressOutboundActor) swap(config *OpConfig) OpResult {
	memo := fmt.Sprintf("=:%s:%s", a.asset, a.address)
	accAddr, err := a.account.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get thor address")
		return OpResult{
			Continue: false,
		}
	}
	deposit := types.NewMsgDeposit(
		common.NewCoins(common.NewCoin(common.RuneAsset(), a.runeAmount)),
		memo,
		accAddr,
	)
	a.txid, err = a.account.Thorchain.Broadcast(deposit)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast tx")
		return OpResult{
			Continue: false,
		}
	}

	a.Log().Info().Stringer("txid", a.txid).Msg("broadcasted swap tx")
	return OpResult{
		Continue: true,
	}
}

func (a *SpecialAddressOutboundActor) verifySpecialAddressOutbound(config *OpConfig) OpResult {
	details, err := thornode.GetTxDetails(a.txid.String())
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get tx details")
		return OpResult{
			Continue: false,
		}
	}

	if len(details.OutTxs) == 0 {
		a.Log().Info().Msg("outbound not yet processed")
		return OpResult{
			Continue: false,
		}
	}

	if *details.OutTxs[0].ToAddress != a.address {
		a.Log().Error().Str("to_address", *details.OutTxs[0].ToAddress).Msg("outbound to unexpected address")
		return OpResult{
			Continue: false,
			Error:    fmt.Errorf("outbound to unexpected address"),
		}
	}

	a.account.Release() // release the user
	return OpResult{
		Continue: true,
	}
}
