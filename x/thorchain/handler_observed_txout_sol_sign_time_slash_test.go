package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gopkg.in/check.v1"
)

// A SOL sign-time vote reaches THORNode non-final and, by design, never forms
// consensus, so the observe slash point charged for it was never redeemed: every
// signer kept one point per SOL outbound. Once the outbound's final consensus forms,
// landed or failed, each signer's sign-time vote is redeemed like any observation
// that reached consensus.

type signTimeSlashScenario struct {
	f         solNonceFixture
	issuedAt  int64
	inHash    common.TxID
	payout    common.Coin
	recipient common.Address
	txID      common.TxID
}

func newSignTimeSlashScenario(c *C, ctx cosmos.Context, mgr Manager) signTimeSlashScenario {
	f := newSolNonceFixture(c, ctx, mgr)
	f.fundAndBond(c, ctx, mgr)
	mgr.Keeper().SetSolVaultNonce(ctx, SolVaultNonce{
		VaultPubKey: f.vault.PubKey, NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 500,
		UpdatedHeight: 900, SetByTx: GetRandomTxHash(),
	})
	recipient, err := GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	sc := signTimeSlashScenario{
		f:         f,
		issuedAt:  950,
		inHash:    GetRandomTxHash(),
		payout:    common.NewCoin(common.SOLAsset, cosmos.NewUint(250_000_000)),
		recipient: recipient,
		txID:      GetRandomTxHash(),
	}
	block := NewTxOut(sc.issuedAt)
	block.TxArray = []TxOutItem{{
		Chain: common.SOLChain, ToAddress: recipient, VaultPubKey: f.vault.PubKey, VaultPubKeyEddsa: f.vault.PubKeyEddsa,
		Coin:   sc.payout,
		MaxGas: common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(solNonceFee))},
		InHash: sc.inHash, Memo: "OUT:" + sc.inHash.String(),
	}}
	c.Assert(mgr.Keeper().SetTxOut(ctx, block), IsNil)
	return sc
}

// signTime is a signer's sign-time vote of the payout, in the shape Bifrost sends it.
func (sc signTimeSlashScenario) signTime(gas uint64) common.ObservedTx {
	tx := signTimeVote(sc.f.obs(sc.txID, nil, false), sc.issuedAt)
	tx.Tx.ToAddress = sc.recipient
	tx.Tx.Coins = common.NewCoins(sc.payout)
	tx.Tx.Gas = common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(gas))}
	tx.Tx.Memo = "OUT:" + sc.inHash.String()
	return tx
}

// landed is the scanners' final observation of the landed payout, at its slot.
func (sc signTimeSlashScenario) landed(c *C) common.ObservedTx {
	tx := sc.signTime(solNonceFee)
	tx.BlockHeight, tx.FinaliseHeight = 1000, 1000
	tx.SolNonce = &common.SolNonceUpdate{NonceAccount: sc.f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000}
	return tx
}

// failed is the scanners' final observation of the payout landed FAILED, in the
// nonce-creation shape.
func (sc signTimeSlashScenario) failed(c *C) common.ObservedTx {
	tx := sc.f.obs(sc.txID, &common.SolNonceUpdate{NonceAccount: sc.f.nonceAcct, Nonce: solNonceValue(c), Slot: 1000}, true)
	tx.Tx.ToAddress = common.Address(sc.f.nonceAcct)
	tx.Tx.Memo = ""
	tx.Tx.Coins = common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.OneUint()))
	tx.Tx.Gas = common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(solNonceFee-1))}
	c.Assert(isSolNonceCreateTx(tx), Equals, true)
	return tx
}

func observeOut(c *C, ctx cosmos.Context, mgr Manager, tx common.ObservedTx, nas ...NodeAccount) {
	for _, na := range nas {
		_, err := NewObservedTxOutHandler(mgr).Run(ctx, NewMsgObservedTxOut(common.ObservedTxs{tx}, na.NodeAddress))
		c.Assert(err, IsNil)
	}
}

func checkSlashPoints(c *C, ctx cosmos.Context, mgr Manager, want map[int]int64, nas []NodeAccount) {
	for i, na := range nas {
		pts, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		c.Check(pts, Equals, want[i], Commentf("node %d", i))
	}
}

func (s *SolNonceCreateSuite) TestSignTimeVotesKeepNoSlashPointWhenLanded(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	sc := newSignTimeSlashScenario(c, ctx, mgr)
	nas := sc.f.nas

	observeOut(c, ctx, mgr, sc.signTime(solNonceFee), nas[:3]...)
	ctx = ctx.WithBlockHeight(1001)
	observeOut(c, ctx, mgr, sc.landed(c), nas...)

	old, err := mgr.Keeper().GetTxOut(ctx, sc.issuedAt)
	c.Assert(err, IsNil)
	c.Assert(old.TxArray[0].OutHash.Equals(sc.txID), Equals, true, Commentf("the payout is matched"))
	checkSlashPoints(c, ctx, mgr, map[int]int64{}, nas)
}

func (s *SolNonceCreateSuite) TestSignTimeVotesKeepNoSlashPointWhenFailed(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	sc := newSignTimeSlashScenario(c, ctx, mgr)
	nas := sc.f.nas

	observeOut(c, ctx, mgr, sc.signTime(solNonceFee), nas[:3]...)
	ctx = ctx.WithBlockHeight(1001)
	observeOut(c, ctx, mgr, sc.failed(c), nas...)

	re, err := mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Assert(re.TxArray, HasLen, 1, Commentf("the unpaid payout is re-issued"))
	checkSlashPoints(c, ctx, mgr, map[int]int64{}, nas)
}

// A sign-time vote can reach THORNode after the outbound's final consensus.
func (s *SolNonceCreateSuite) TestLateSignTimeVoteKeepsNoSlashPoint(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	sc := newSignTimeSlashScenario(c, ctx, mgr)
	nas := sc.f.nas

	observeOut(c, ctx, mgr, sc.landed(c), nas...)
	ctx = ctx.WithBlockHeight(1001)
	observeOut(c, ctx, mgr, sc.signTime(solNonceFee), nas[0])

	checkSlashPoints(c, ctx, mgr, map[int]int64{}, nas)
}

// Only one sign-time vote per signer is redeemed. The existing allowance refunds a
// signer's first redundant variant; any further non-final variant keeps its point,
// so non-final votes cannot be filed for free.
func (s *SolNonceCreateSuite) TestExtraSignTimeVariantsKeepTheirSlashPoint(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	sc := newSignTimeSlashScenario(c, ctx, mgr)
	nas := sc.f.nas

	observeOut(c, ctx, mgr, sc.signTime(solNonceFee), nas[:3]...)
	observeOut(c, ctx, mgr, sc.signTime(solNonceFee+1), nas[0])
	observeOut(c, ctx, mgr, sc.signTime(solNonceFee+2), nas[0])
	ctx = ctx.WithBlockHeight(1001)
	observeOut(c, ctx, mgr, sc.landed(c), nas...)

	checkSlashPoints(c, ctx, mgr, map[int]int64{0: 1}, nas)
}

// Non-final votes of a tx that never reaches final consensus keep their point, as
// any unconfirmed observation does.
func (s *SolNonceCreateSuite) TestSignTimeVotesWithoutFinalConsensusKeepTheirSlashPoint(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	sc := newSignTimeSlashScenario(c, ctx, mgr)
	nas := sc.f.nas

	observeOut(c, ctx, mgr, sc.signTime(solNonceFee), nas[:3]...)

	checkSlashPoints(c, ctx, mgr, map[int]int64{0: 1, 1: 1, 2: 1}, nas)
}
