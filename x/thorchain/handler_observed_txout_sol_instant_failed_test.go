package thorchain

import (
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"

	. "gopkg.in/check.v1"
)

// zly (!5272): the signers' instant (pre-landing, non-final) observations of a durable
// outbound reach quorum first and the outbound is processed as paid; the tx then lands
// FAILED and the scanners report it, final, in the nonce-creation shape. Driven through
// the real ObservedTxOutHandler, vote by vote. The end state must match the order where
// the failure is the only consensus (TestFailedDurableTxBooksFeeAndReissues): only the
// fee leaves the vault, the advanced nonce commits, the payout is re-issued.
func (s *SolNonceCreateSuite) TestInstantPayoutThenFailedDurableTx(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	f := newSolNonceFixture(c, ctx, mgr)
	f.fundAndBond(c, ctx, mgr)
	active, err := mgr.Keeper().ListActiveValidators(ctx)
	c.Assert(err, IsNil)
	c.Assert(active, HasLen, len(f.nas))
	start := uint64(10 * common.One)
	mgr.Keeper().SetSolVaultNonce(ctx, SolVaultNonce{
		VaultPubKey: f.vault.PubKey, NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 500,
		UpdatedHeight: 900, SetByTx: GetRandomTxHash(),
	})

	recipient, err := GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	const issuedAt = int64(950)
	inHash := GetRandomTxHash()
	payout := common.NewCoin(common.SOLAsset, cosmos.NewUint(250_000_000))
	pending := TxOutItem{
		Chain: common.SOLChain, ToAddress: recipient, VaultPubKey: f.vault.PubKey, VaultPubKeyEddsa: f.vault.PubKeyEddsa,
		Coin:   payout,
		MaxGas: common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(solNonceFee))},
		InHash: inHash, Memo: "OUT:" + inHash.String(),
	}
	block := NewTxOut(issuedAt)
	block.TxArray = []TxOutItem{pending}
	c.Assert(mgr.Keeper().SetTxOut(ctx, block), IsNil)

	run := func(ctx cosmos.Context, tx common.ObservedTx, na NodeAccount) {
		_, runErr := NewObservedTxOutHandler(mgr).Run(ctx, NewMsgObservedTxOut(common.ObservedTxs{tx}, na.NodeAddress))
		c.Assert(runErr, IsNil)
	}

	// 1. The signers' instant observation of the payout as broadcast, in the shape the
	// observer sends it (signTimeVote), to the recipient with the outbound memo. Three of
	// four is a supermajority.
	txID := GetRandomTxHash()
	instant := signTimeVote(f.obs(txID, nil, false), issuedAt)
	instant.Tx.ToAddress = recipient
	instant.Tx.Coins = common.NewCoins(payout)
	instant.Tx.Gas = common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(solNonceFee))}
	instant.Tx.Memo = pending.Memo
	for _, na := range f.nas[:3] {
		run(ctx, instant, na)
	}

	// 2. The tx landed FAILED. Every scanner reports it final, in the nonce-creation shape.
	ctx = ctx.WithBlockHeight(1001)
	newNonce := solNonceValue(c)
	failed := f.obs(txID, &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: newNonce, Slot: 1000}, true)
	failed.Tx.ToAddress = common.Address(f.nonceAcct)
	failed.Tx.Memo = ""
	failed.Tx.Coins = common.NewCoins(common.NewCoin(common.SOLAsset, cosmos.OneUint()))
	failed.Tx.Gas = common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(solNonceFee-1))}
	c.Assert(isSolNonceCreateTx(failed), Equals, true)
	for _, na := range f.nas {
		run(ctx, failed, na)
	}

	n, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	re, err := mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	old, err := mgr.Keeper().GetTxOut(ctx, issuedAt)
	c.Assert(err, IsNil)

	// Same end state as when the failure is the only consensus.
	c.Check(f.vaultSOL(c, ctx, mgr), Equals, start-solNonceFee, Commentf("only the fee may leave the vault"))
	c.Check(found && n.Nonce == newNonce && n.SetByTx.Equals(txID), Equals, true, Commentf("advanced nonce must commit"))
	c.Assert(re.TxArray, HasLen, 1, Commentf("the unpaid payout must be re-issued"))
	c.Check(re.TxArray[0].InHash.Equals(inHash), Equals, true)
	c.Check(re.TxArray[0].Coin.Equals(payout), Equals, true)
	c.Check(old.TxArray[0].OutHash.Equals(txID), Equals, false, Commentf("the failed tx must not be recorded as the payout"))
}

// The ordinary order must still pay exactly once: the signers' instant votes reach a
// supermajority, then every scanner reports the landed (successful) tx, final.
func (s *SolNonceCreateSuite) TestInstantPayoutThenLandedDurableTx(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ctx = ctx.WithBlockHeight(1000)
	f := newSolNonceFixture(c, ctx, mgr)
	f.fundAndBond(c, ctx, mgr)
	start := uint64(10 * common.One)
	mgr.Keeper().SetSolVaultNonce(ctx, SolVaultNonce{
		VaultPubKey: f.vault.PubKey, NonceAccount: f.nonceAcct, Nonce: solNonceValue(c), Slot: 500,
		UpdatedHeight: 900, SetByTx: GetRandomTxHash(),
	})

	recipient, err := GetRandomEd25519PubKey().GetAddress(common.SOLChain)
	c.Assert(err, IsNil)
	const issuedAt = int64(950)
	inHash := GetRandomTxHash()
	payout := common.NewCoin(common.SOLAsset, cosmos.NewUint(250_000_000))
	block := NewTxOut(issuedAt)
	block.TxArray = []TxOutItem{{
		Chain: common.SOLChain, ToAddress: recipient, VaultPubKey: f.vault.PubKey, VaultPubKeyEddsa: f.vault.PubKeyEddsa,
		Coin:   payout,
		MaxGas: common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(solNonceFee))},
		InHash: inHash, Memo: "OUT:" + inHash.String(),
	}}
	c.Assert(mgr.Keeper().SetTxOut(ctx, block), IsNil)

	run := func(ctx cosmos.Context, tx common.ObservedTx, na NodeAccount) {
		_, runErr := NewObservedTxOutHandler(mgr).Run(ctx, NewMsgObservedTxOut(common.ObservedTxs{tx}, na.NodeAddress))
		c.Assert(runErr, IsNil)
	}
	txID := GetRandomTxHash()
	instant := signTimeVote(f.obs(txID, nil, false), issuedAt)
	instant.Tx.ToAddress = recipient
	instant.Tx.Coins = common.NewCoins(payout)
	instant.Tx.Gas = common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(solNonceFee))}
	instant.Tx.Memo = "OUT:" + inHash.String()
	for _, na := range f.nas[:3] {
		run(ctx, instant, na)
	}

	ctx = ctx.WithBlockHeight(1001)
	newNonce := solNonceValue(c)
	// The scanners report the landed tx at its slot, final.
	landed := instant
	landed.BlockHeight, landed.FinaliseHeight = 1000, 1000
	landed.SolNonce = &common.SolNonceUpdate{NonceAccount: f.nonceAcct, Nonce: newNonce, Slot: 1000}
	c.Assert(landed.IsFinal(), Equals, true)
	for _, na := range f.nas {
		run(ctx, landed, na)
	}

	c.Check(f.vaultSOL(c, ctx, mgr), Equals, start-payout.Amount.Uint64()-solNonceFee, Commentf("paid exactly once"))
	old, err := mgr.Keeper().GetTxOut(ctx, issuedAt)
	c.Assert(err, IsNil)
	c.Check(old.TxArray[0].OutHash.Equals(txID), Equals, true)
	re, err := mgr.Keeper().GetTxOut(ctx, ctx.BlockHeight())
	c.Assert(err, IsNil)
	c.Check(re.TxArray, HasLen, 0, Commentf("a paid outbound is not re-issued"))
	n, found, err := mgr.Keeper().GetSolVaultNonce(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	c.Check(found && n.Nonce == newNonce && n.SetByTx.Equals(txID), Equals, true)
	for _, na := range f.nas {
		after, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
		c.Assert(err, IsNil)
		c.Check(after.Bond.Equal(na.Bond), Equals, true, Commentf("no slash"))
	}
}

// signTimeVote gives an observation the shape in which Bifrost sends a signer's
// sign-time observation of a SOL outbound: SOLClient.finishSign uses the outbound's
// THORChain height as the block height, and the observer sends it non-final
// (bifrost/observer TestSOLSignTimeObservationReachesThorchainNonFinal).
func signTimeVote(tx common.ObservedTx, outboundHeight int64) common.ObservedTx {
	tx.BlockHeight = outboundHeight
	tx.FinaliseHeight = outboundHeight + 1
	return tx
}

func (f solNonceFixture) vaultSOL(c *C, ctx cosmos.Context, mgr Manager) uint64 {
	vault, err := mgr.Keeper().GetVault(ctx, f.vault.PubKey)
	c.Assert(err, IsNil)
	return vault.Coins.GetCoin(common.SOLAsset).Amount.Uint64()
}
