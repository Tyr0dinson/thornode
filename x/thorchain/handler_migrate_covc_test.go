package thorchain

import (
	"errors"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type HandlerMigrateCovCESuite struct{}

var _ = Suite(&HandlerMigrateCovCESuite{})

// covCEFailSlasher records SlashVault calls and optionally fails them.
type covCEFailSlasher struct {
	Slasher
	fail  bool
	calls int
}

func (s *covCEFailSlasher) SlashVault(ctx cosmos.Context, vaultPK common.PubKey, coins common.Coins, mgr Manager) error {
	s.calls++
	if s.fail {
		return errKaboom
	}
	return nil
}

// covCEOutboundFixture builds a tx out at height 1 whose last item is a gas
// asset outbound that only matches the observed tx through the gas override,
// preceded by items that must be skipped.
type covCEOutboundFixture struct {
	vault    Vault
	toAddr   common.Address
	fromAddr common.Address
	txOut    *TxOut
	observed ObservedTx
}

func newCovCEOutboundFixture(c *C, memo string) covCEOutboundFixture {
	vault := GetRandomVault()
	toAddr := GetRandomETHAddress()
	fromAddr, err := vault.PubKey.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)

	txOut := NewTxOut(1)
	txOut.TxArray = append(txOut.TxArray,
		// other chain: skipped
		TxOutItem{
			InHash: common.BlankTxID, Chain: common.BTCChain, ToAddress: GetRandomBTCAddress(), VaultPubKey: vault.PubKey,
			Coin: common.NewCoin(common.BTCAsset, cosmos.NewUint(1)), Memo: memo,
		},
		// no vault pubkey: skipped
		TxOutItem{
			InHash: common.BlankTxID, Chain: common.ETHChain, ToAddress: toAddr,
			Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(1000)), Memo: memo,
		},
		// unparsable vault pubkey: skipped
		TxOutItem{
			InHash: common.BlankTxID, Chain: common.ETHChain, ToAddress: toAddr, VaultPubKey: common.PubKey("bogus"),
			Coin: common.NewCoin(common.ETHAsset, cosmos.NewUint(1000)), Memo: memo,
		},
		// gas asset outbound: coins differ, but coin+max gas == observed coin+gas
		TxOutItem{
			InHash: common.BlankTxID, Chain: common.ETHChain, ToAddress: toAddr, VaultPubKey: vault.PubKey,
			Coin:   common.NewCoin(common.ETHAsset, cosmos.NewUint(1000)),
			MaxGas: common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(100))},
			Memo:   memo,
		},
	)

	observed := NewObservedTx(common.Tx{
		ID:          GetRandomTxHash(),
		Chain:       common.ETHChain,
		FromAddress: fromAddr,
		ToAddress:   toAddr,
		Coins:       common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1050))},
		Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(50))},
		Memo:        memo,
	}, 1, vault.PubKey, 1)

	return covCEOutboundFixture{vault: vault, toAddr: toAddr, fromAddr: fromAddr, txOut: txOut, observed: observed}
}

func (s *HandlerMigrateCovCESuite) TestGasOverrideMatchCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(10)
	fx := newCovCEOutboundFixture(c, NewMigrateMemo(1).String())
	c.Assert(mgr.Keeper().SetTxOut(ctx, fx.txOut), IsNil)

	slasher := &covCEFailSlasher{Slasher: mgr.slasher}
	mgr.slasher = slasher
	h := NewMigrateHandler(mgr)

	_, err := h.Run(ctx, &types.MsgMimir{})
	c.Check(errors.Is(err, errInvalidMessage), Equals, true)
	_, err = h.Run(ctx, &MsgMigrate{})
	c.Check(err, NotNil)

	res, err := h.Run(ctx, NewMsgMigrate(fx.observed, 1, GetRandomBech32Addr()))
	c.Assert(err, IsNil)
	c.Assert(res, NotNil)
	c.Check(slasher.calls, Equals, 0)

	stored, err := mgr.Keeper().GetTxOut(ctx, 1)
	c.Assert(err, IsNil)
	for i, item := range stored.TxArray {
		if i == 3 {
			c.Check(item.OutHash.Equals(fx.observed.Tx.ID), Equals, true)
		} else {
			c.Check(item.OutHash.IsEmpty(), Equals, true, Commentf("item %d", i))
		}
	}
	h2, err := mgr.Keeper().GetLastSignedHeight(ctx)
	c.Assert(err, IsNil)
	c.Check(h2, Equals, int64(1))
}

func (s *HandlerMigrateCovCESuite) TestNoMatchAndErrorsCovCE(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(10)
	fx := newCovCEOutboundFixture(c, NewMigrateMemo(1).String())
	c.Assert(mgr.Keeper().SetTxOut(ctx, fx.txOut), IsNil)
	k := mgr.Keeper()

	// real gas exceeds max gas: no match -> slash
	noMatch := fx.observed
	noMatch.Tx.Coins = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(900))}
	noMatch.Tx.Gas = common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(200))}
	slasher := &covCEFailSlasher{Slasher: mgr.slasher}
	mgr.slasher = slasher
	_, err := NewMigrateHandler(mgr).Run(ctx, NewMsgMigrate(noMatch, 1, GetRandomBech32Addr()))
	c.Assert(err, IsNil)
	c.Check(slasher.calls, Equals, 1)

	// gas asset outbound where coin+gas differs: no match -> slash
	noMatch.Tx.Coins = common.Coins{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))}
	noMatch.Tx.Gas = common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(1))}
	_, err = NewMigrateHandler(mgr).Run(ctx, NewMsgMigrate(noMatch, 1, GetRandomBech32Addr()))
	c.Assert(err, IsNil)
	c.Check(slasher.calls, Equals, 2)

	// slash failure
	slasher.fail = true
	_, err = NewMigrateHandler(mgr).Run(ctx, NewMsgMigrate(noMatch, 1, GetRandomBech32Addr()))
	c.Check(err, ErrorMatches, "(?s)fail to slash account.*kaboom.*")
	slasher.fail = false

	// keeper failures
	mgr.K = newCovCEFailKeeper(k, "GetTxOut")
	_, err = NewMigrateHandler(mgr).Run(ctx, NewMsgMigrate(fx.observed, 1, GetRandomBech32Addr()))
	c.Check(err, ErrorMatches, "kaboom.*")

	mgr.K = newCovCEFailKeeper(k, "SetTxOut")
	_, err = NewMigrateHandler(mgr).Run(ctx, NewMsgMigrate(fx.observed, 1, GetRandomBech32Addr()))
	c.Check(err, ErrorMatches, "(?s)fail to save tx out.*kaboom.*")

	// failing to record the last signed height is only logged
	mgr.K = newCovCEFailKeeper(k, "SetLastSignedHeight")
	_, err = NewMigrateHandler(mgr).Run(ctx, NewMsgMigrate(fx.observed, 1, GetRandomBech32Addr()))
	c.Check(err, IsNil)
	mgr.K = k
	stored, err := k.GetTxOut(ctx, 1)
	c.Assert(err, IsNil)
	c.Check(stored.TxArray[3].OutHash.Equals(fx.observed.Tx.ID), Equals, true)
}
