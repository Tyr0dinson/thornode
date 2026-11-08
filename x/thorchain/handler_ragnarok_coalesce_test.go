package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

// ragnarokBTCGasManager quotes max gas in BTC, the outbound's own gas asset.
type ragnarokBTCGasManager struct {
	*DummyGasManager
}

func (m *ragnarokBTCGasManager) GetMaxGas(cosmos.Context, common.Chain) (common.Coin, error) {
	return common.NewCoin(common.BTCAsset, cosmos.NewUint(1_000)), nil
}

func (m *ragnarokBTCGasManager) GetGasRate(cosmos.Context, common.Chain) cosmos.Uint {
	return cosmos.NewUint(1)
}

// A gas-asset ragnarok outbound goes unsigned for a signing period, and real
// LackSigning retries it at the due block rounded up to RescheduleCoalesceBlocks
// (10 reference blocks). The ragnarok handler must find that retry, not the due
// block, whether the observation lands after the retry's height or before it
// (the original's late signature), so the vault is not slashed:
//   - the block time halves at 10050: due at 10551, retried at 10560, the next
//     multiple of the scaled 20-block interval
//   - no block time change, scheduled off the interval at 10005: due at 10305,
//     retried at 10310 (on develop the walk stepped to 10305 only)
func (HandlerRagnarokSuite) TestRagnarokFindsCoalescedRetry(c *C) {
	refMs := constants.ThorchainBlockTime.Milliseconds()
	for _, tc := range []struct {
		name               string
		scheduled          int64
		change             int64 // 0: no block time change
		due, retry         int64
		observeBeforeRetry bool // observe between the due block and the retry height
	}{
		{name: "after a speed-up", scheduled: 10000, change: 10050, due: 10551, retry: 10560},
		{name: "late, after a speed-up", scheduled: 10000, change: 10050, due: 10551, retry: 10560, observeBeforeRetry: true},
		{name: "no change, off the interval", scheduled: 10005, due: 10305, retry: 10310},
		{name: "late, no change, off the interval", scheduled: 10005, due: 10305, retry: 10310, observeBeforeRetry: true},
	} {
		comment := Commentf(tc.name)
		ctx, k := setupKeeperForTest(c)
		mgr := NewDummyMgrWithKeeper(k)
		gas := &ragnarokBTCGasManager{DummyGasManager: NewDummyGasManager()}
		mgr.gasMgr = gas
		mgr.txOutStore = newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), gas)
		recorder := &xmrMemoCompatibilitySlasher{DummySlasher: NewDummySlasher()}
		mgr.slasher = recorder

		ctx = ctx.WithBlockHeight(tc.scheduled)
		k.SetMimir(ctx, constants.RescheduleCoalesceBlocks.String(), 10)
		k.SetMimir(ctx, constants.EnableMemolessOutbound.String(), 0)
		k.SetMimir(ctx, constants.MaxOutboundAttempts.String(), 0)
		pool := NewPool()
		pool.Asset = common.BTCAsset
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		// the only vault, so the retry keeps it
		vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), nil)
		vault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))))
		c.Assert(k.SetVault(ctx, vault), IsNil)
		from, err := vault.PubKey.GetAddress(common.BTCChain)
		c.Assert(err, IsNil)

		item := TxOutItem{
			Chain:       common.BTCChain,
			InHash:      common.BlankTxID,
			ToAddress:   GetRandomBTCAddress(),
			VaultPubKey: vault.PubKey,
			Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
			MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1_000))},
			GasRate:     1,
			Memo:        NewRagnarokMemo(tc.scheduled).String(),
		}
		queued := NewTxOut(tc.scheduled)
		queued.TxArray = []TxOutItem{item}
		c.Assert(k.SetTxOut(ctx, queued), IsNil)
		k.SetRagnarokPending(ctx, 1)

		if tc.change > 0 {
			k.SetMimir(ctx, constants.BlockTimeMs.String(), refMs/2)
			ctx = ctx.WithBlockHeight(tc.change)
			k.UpdateBlockTime(ctx)
			c.Assert(k.GetBlockTimeHistory(ctx), HasLen, 1, comment)
		}
		period := mgr.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
		c.Assert(k.GetBlockTimeHistory(ctx).HeightAfter(tc.scheduled, period*refMs, refMs), Equals, tc.due, comment)

		// unsigned for a whole period: LackSigning retries it on the same vault
		ctx = ctx.WithBlockHeight(tc.due)
		c.Assert(newSlasher(k, mgr.EventMgr()).LackSigning(ctx, mgr), IsNil)
		original, err := k.GetTxOut(ctx, tc.scheduled)
		c.Assert(err, IsNil)
		c.Assert(original.TxArray, HasLen, 1, comment)
		c.Assert(original.TxArray[0].OutHash, Equals, common.BlankTxID, comment)
		retry, err := k.GetTxOut(ctx, tc.retry)
		c.Assert(err, IsNil)
		c.Assert(retry.TxArray, HasLen, 1, comment)
		c.Assert(retry.TxArray[0].VaultPubKey.Equals(vault.PubKey), Equals, true, comment)

		observeAt := tc.retry + 1
		if tc.observeBeforeRetry {
			observeAt = tc.due + 1
		}
		ctx = ctx.WithBlockHeight(observeAt)
		tx := NewObservedTx(
			common.NewTx(GetRandomTxHash(), from, item.ToAddress, common.NewCoins(item.Coin), item.MaxGas, item.Memo),
			ctx.BlockHeight(), vault.PubKey, ctx.BlockHeight(),
		)
		_, err = NewRagnarokHandler(mgr).handle(ctx, *NewMsgRagnarok(tx, tc.scheduled, GetRandomBech32Addr()))
		c.Assert(err, IsNil)

		retry, err = k.GetTxOut(ctx, tc.retry)
		c.Assert(err, IsNil)
		c.Check(retry.TxArray[0].OutHash.Equals(tx.Tx.ID), Equals, true, comment)
		c.Check(recorder.calls, Equals, 0, comment)
		pending, err := k.GetRagnarokPending(ctx)
		c.Assert(err, IsNil)
		c.Check(pending, Equals, int64(0), comment)
	}
}

// ragnarokRetryFixture is a BTC ragnarok outbound queued at scheduled on the
// only vault, with no block time change, retried by the real LackSigning.
type ragnarokRetryFixture struct {
	ctx      cosmos.Context
	k        keeper.Keeper
	mgr      *DummyMgr
	recorder *xmrMemoCompatibilitySlasher
	vault    Vault
	from     common.Address
	item     TxOutItem
}

func newRagnarokRetryFixture(c *C, scheduled, coalesce int64) *ragnarokRetryFixture {
	ctx, k := setupKeeperForTest(c)
	mgr := NewDummyMgrWithKeeper(k)
	gas := &ragnarokBTCGasManager{DummyGasManager: NewDummyGasManager()}
	mgr.gasMgr = gas
	mgr.txOutStore = newTxOutStorage(k, mgr.GetConstants(), mgr.EventMgr(), gas)
	recorder := &xmrMemoCompatibilitySlasher{DummySlasher: NewDummySlasher()}
	mgr.slasher = recorder

	ctx = ctx.WithBlockHeight(scheduled)
	k.SetMimir(ctx, constants.RescheduleCoalesceBlocks.String(), coalesce)
	k.SetMimir(ctx, constants.EnableMemolessOutbound.String(), 0)
	k.SetMimir(ctx, constants.MaxOutboundAttempts.String(), 0)
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceRune = cosmos.NewUint(100 * common.One)
	pool.BalanceAsset = cosmos.NewUint(100 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	// the only vault, so every retry keeps it
	vault := NewVault(1, ActiveVault, AsgardVault, GetRandomPubKey(), common.Chains{common.BTCChain}.Strings(), nil)
	vault.AddFunds(common.NewCoins(common.NewCoin(common.BTCAsset, cosmos.NewUint(100*common.One))))
	c.Assert(k.SetVault(ctx, vault), IsNil)
	from, err := vault.PubKey.GetAddress(common.BTCChain)
	c.Assert(err, IsNil)

	item := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      common.BlankTxID,
		ToAddress:   GetRandomBTCAddress(),
		VaultPubKey: vault.PubKey,
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One)),
		MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1_000))},
		GasRate:     1,
		Memo:        NewRagnarokMemo(scheduled).String(),
	}
	queued := NewTxOut(scheduled)
	queued.TxArray = []TxOutItem{item}
	c.Assert(k.SetTxOut(ctx, queued), IsNil)
	k.SetRagnarokPending(ctx, 1)
	return &ragnarokRetryFixture{ctx: ctx, k: k, mgr: mgr, recorder: recorder, vault: vault, from: from, item: item}
}

// lackSigning runs the real LackSigning at height and checks the copy queued
// at from was replaced by one at to.
func (f *ragnarokRetryFixture) lackSigning(c *C, height, from, to int64, comment CommentInterface) {
	f.ctx = f.ctx.WithBlockHeight(height)
	c.Assert(newSlasher(f.k, f.mgr.EventMgr()).LackSigning(f.ctx, f.mgr), IsNil, comment)
	replaced, err := f.k.GetTxOut(f.ctx, from)
	c.Assert(err, IsNil)
	c.Assert(replaced.TxArray, HasLen, 1, comment)
	c.Assert(replaced.TxArray[0].OutHash, Equals, common.BlankTxID, comment)
	retry, err := f.k.GetTxOut(f.ctx, to)
	c.Assert(err, IsNil)
	c.Assert(retry.TxArray, HasLen, 1, comment)
	c.Assert(retry.TxArray[0].OutHash.IsEmpty(), Equals, true, comment)
}

func (f *ragnarokRetryFixture) setCoalesce(coalesce int64) {
	f.k.SetMimir(f.ctx, constants.RescheduleCoalesceBlocks.String(), coalesce)
}

// observe handles the payment at height and checks it was matched to the live
// copy at live, without a slash.
func (f *ragnarokRetryFixture) observe(c *C, height, scheduled, live int64, comment CommentInterface) {
	f.ctx = f.ctx.WithBlockHeight(height)
	tx := NewObservedTx(
		common.NewTx(GetRandomTxHash(), f.from, f.item.ToAddress, common.NewCoins(f.item.Coin), f.item.MaxGas, f.item.Memo),
		height, f.vault.PubKey, height,
	)
	_, err := NewRagnarokHandler(f.mgr).handle(f.ctx, *NewMsgRagnarok(tx, scheduled, GetRandomBech32Addr()))
	c.Assert(err, IsNil, comment)
	retry, err := f.k.GetTxOut(f.ctx, live)
	c.Assert(err, IsNil)
	c.Assert(retry.TxArray, HasLen, 1, comment)
	c.Check(retry.TxArray[0].OutHash.Equals(tx.Tx.ID), Equals, true, comment)
	c.Check(f.recorder.calls, Equals, 0, comment)
	pending, err := f.k.GetRagnarokPending(f.ctx)
	c.Assert(err, IsNil)
	c.Check(pending, Equals, int64(0), comment)
}

// LackSigning places a retry with the RescheduleCoalesceBlocks of the block it
// runs in, and a later change of the mimir does not move it. The ragnarok
// handler computes retry heights with the current value, so it must find the
// persisted retry when that value has changed since:
//   - coalescing off at the retry (10305), on (10) when observed: the walk
//     computes 10310
//   - 20 at the retry (10320), 10 when observed: the walk computes 10310,
//     below the retry; observed after the retry, and before it (a late
//     signature of the original, while the retry is still queued ahead)
func (HandlerRagnarokSuite) TestRagnarokFindsRetryAfterCoalesceChange(c *C) {
	const scheduled, due = int64(10005), int64(10305)
	for _, tc := range []struct {
		name          string
		before, after int64 // RescheduleCoalesceBlocks at the retry, then when observed
		retry         int64
		observeAt     int64
	}{
		{name: "coalescing enabled after the retry", before: 0, after: 10, retry: 10305, observeAt: 10307},
		{name: "interval shortened after the retry", before: 20, after: 10, retry: 10320, observeAt: 10321},
		{name: "late, interval shortened after the retry", before: 20, after: 10, retry: 10320, observeAt: 10306},
	} {
		comment := Commentf(tc.name)
		f := newRagnarokRetryFixture(c, scheduled, tc.before)
		f.lackSigning(c, due, scheduled, tc.retry, comment)
		f.setCoalesce(tc.after)
		f.observe(c, tc.observeAt, scheduled, tc.retry, comment)
	}
}

// The mimir changes between two retries, and again before the observation:
// coalescing off at the first retry (10305), 10 at the second (10610), 20 when
// observed. No computed height (10320, then 10620) holds a retry, so the walk
// follows the persisted copies.
func (HandlerRagnarokSuite) TestRagnarokFindsRetryAfterCoalesceChangeBetweenRetries(c *C) {
	const scheduled = int64(10005)
	comment := Commentf("coalesce changed between retries")
	f := newRagnarokRetryFixture(c, scheduled, 0)
	f.lackSigning(c, 10305, scheduled, 10305, comment)
	f.setCoalesce(10)
	f.lackSigning(c, 10605, 10305, 10610, comment)
	f.setCoalesce(20)
	f.observe(c, 10611, scheduled, 10610, comment)
}

// LackSigning skips an outbound whose pool is gone without rescheduling it, and
// never examines that height again, so the copy stays live for many signing
// periods. After the coalesce interval changes, the computed heights miss it
// (10305, 10605, ...), so the walk must find it by its persisted copy at 10310.
func (HandlerRagnarokSuite) TestRagnarokFindsCopyLeftLiveBySkippedReschedule(c *C) {
	const scheduled = int64(10005)
	comment := Commentf("copy left live by a skipped reschedule")
	f := newRagnarokRetryFixture(c, scheduled, 10)
	f.lackSigning(c, 10305, scheduled, 10310, comment)

	f.k.RemovePool(f.ctx, common.BTCAsset)
	f.ctx = f.ctx.WithBlockHeight(10610)
	c.Assert(newSlasher(f.k, f.mgr.EventMgr()).LackSigning(f.ctx, f.mgr), IsNil)
	live, err := f.k.GetTxOut(f.ctx, 10310)
	c.Assert(err, IsNil)
	c.Assert(live.TxArray, HasLen, 1)
	c.Assert(live.TxArray[0].OutHash.IsEmpty(), Equals, true, Commentf("the skipped copy stays live"))
	ahead, err := f.k.GetTxOut(f.ctx, 10610)
	c.Assert(err, IsNil)
	c.Assert(ahead.TxArray, HasLen, 0)

	f.setCoalesce(0)
	// several signing periods later
	f.observe(c, 11500, scheduled, 10310, comment)
}

// A SOL durable nonce reissue moves an outbound's copy to the current height,
// before the outbound falls due again (reissueSolDurableAt). The walk must
// reach that copy though no retry is due yet, and still slash a payment that
// matches no copy.
func (HandlerRagnarokSuite) TestRagnarokFindsSolCopyMovedOffSchedule(c *C) {
	const scheduled, moved = int64(10005), int64(10100)
	for _, tc := range []struct {
		name    string
		observe int64
		matches bool
	}{
		{name: "observed before the next due block", observe: 10150, matches: true},
		{name: "observed after the next due block", observe: 10400, matches: true},
		{name: "payment to another destination", observe: 10150},
	} {
		comment := Commentf(tc.name)
		ctx, k := setupKeeperForTest(c)
		mgr := NewDummyMgrWithKeeper(k)
		recorder := &xmrMemoCompatibilitySlasher{DummySlasher: NewDummySlasher()}
		mgr.slasher = recorder
		ctx = ctx.WithBlockHeight(scheduled)
		k.SetMimir(ctx, constants.RescheduleCoalesceBlocks.String(), 10)

		vault := GetRandomVault()
		from, err := vault.PubKeyEddsa.GetAddress(common.SOLChain)
		c.Assert(err, IsNil)
		to, err := GetRandomEd25519PubKey().GetAddress(common.SOLChain)
		c.Assert(err, IsNil)
		item := TxOutItem{
			Chain:            common.SOLChain,
			InHash:           common.BlankTxID,
			ToAddress:        to,
			VaultPubKey:      vault.PubKey,
			VaultPubKeyEddsa: vault.PubKeyEddsa,
			Coin:             common.NewCoin(common.SOLAsset, cosmos.NewUint(common.One)),
			MaxGas:           common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(5_000))},
			Memo:             NewRagnarokMemo(scheduled).String(),
		}
		// the reissue marks the original done and queues the copy at its height
		original := item
		original.OutHash = common.BlankTxID
		queued := NewTxOut(scheduled)
		queued.TxArray = []TxOutItem{original}
		c.Assert(k.SetTxOut(ctx, queued), IsNil)
		copied := NewTxOut(moved)
		copied.TxArray = []TxOutItem{item}
		c.Assert(k.SetTxOut(ctx, copied), IsNil)
		k.SetRagnarokPending(ctx, 1)

		payTo := item.ToAddress
		if !tc.matches {
			payTo, err = GetRandomEd25519PubKey().GetAddress(common.SOLChain)
			c.Assert(err, IsNil)
		}
		ctx = ctx.WithBlockHeight(tc.observe)
		tx := NewObservedTx(
			common.NewTx(GetRandomTxHash(), from, payTo, common.NewCoins(item.Coin), item.MaxGas, item.Memo),
			tc.observe, vault.PubKeyEddsa, tc.observe,
		)
		_, err = NewRagnarokHandler(mgr).handle(ctx, *NewMsgRagnarok(tx, scheduled, GetRandomBech32Addr()))
		c.Assert(err, IsNil, comment)

		got, err := k.GetTxOut(ctx, moved)
		c.Assert(err, IsNil)
		c.Assert(got.TxArray, HasLen, 1, comment)
		pending, err := k.GetRagnarokPending(ctx)
		c.Assert(err, IsNil)
		if tc.matches {
			c.Check(got.TxArray[0].OutHash.Equals(tx.Tx.ID), Equals, true, comment)
			c.Check(recorder.calls, Equals, 0, comment)
			c.Check(pending, Equals, int64(0), comment)
		} else {
			c.Check(got.TxArray[0].OutHash.IsEmpty(), Equals, true, comment)
			c.Check(recorder.calls, Equals, 1, comment)
			c.Check(pending, Equals, int64(1), comment)
		}
	}
}

// DiscoverOutbounds splits a SOL ragnarok payout across vaults A and B, both
// parts queued at 10000 with the same destination, asset and memo. Durable
// nonce reissues move each part on its own: A to 10200, 10500 and lastly 10600,
// where it is live, and B to 10100, 10400 and lastly 10700. The computed
// retries from B's copies (10400, 10700) hold B again, so following them skips
// A's live copy at 10600. The walk must reach each part's live copy, and still
// slash a payment that matches neither.
func (HandlerRagnarokSuite) TestRagnarokFindsSplitPayoutPartMovedIndependently(c *C) {
	const scheduled, observed = int64(10000), int64(10701)
	for _, tc := range []struct {
		name     string
		partB    bool  // the payment is part B's, not part A's
		live     int64 // the paid part's live copy
		stranger bool  // paid to another destination
	}{
		{name: "part A, below part B's copies", live: 10600},
		{name: "part B, on the computed heights", partB: true, live: 10700},
		{name: "payment to another destination", live: 10600, stranger: true},
	} {
		comment := Commentf(tc.name)
		ctx, k := setupKeeperForTest(c)
		mgr := NewDummyMgrWithKeeper(k)
		recorder := &xmrMemoCompatibilitySlasher{DummySlasher: NewDummySlasher()}
		mgr.slasher = recorder
		ctx = ctx.WithBlockHeight(observed)
		k.SetMimir(ctx, constants.RescheduleCoalesceBlocks.String(), 10)

		vaultA, vaultB := GetRandomVault(), GetRandomVault()
		to, err := GetRandomEd25519PubKey().GetAddress(common.SOLChain)
		c.Assert(err, IsNil)
		part := func(v Vault, amount uint64) TxOutItem {
			return TxOutItem{
				Chain:            common.SOLChain,
				InHash:           common.BlankTxID,
				ToAddress:        to,
				VaultPubKey:      v.PubKey,
				VaultPubKeyEddsa: v.PubKeyEddsa,
				Coin:             common.NewCoin(common.SOLAsset, cosmos.NewUint(amount)),
				MaxGas:           common.Gas{common.NewCoin(common.SOLAsset, cosmos.NewUint(5_000))},
				Memo:             NewRagnarokMemo(scheduled).String(),
			}
		}
		// each reissue marks the copy it replaces done
		replaced := func(item TxOutItem) TxOutItem {
			item.OutHash = common.BlankTxID
			return item
		}
		queue := func(height int64, items ...TxOutItem) {
			out := NewTxOut(height)
			out.TxArray = items
			c.Assert(k.SetTxOut(ctx, out), IsNil)
		}
		partA, partB := part(vaultA, common.One), part(vaultB, 2*common.One)
		queue(scheduled, replaced(partA), replaced(partB))
		queue(10100, replaced(partB))
		queue(10200, replaced(partA))
		queue(10400, replaced(partB))
		queue(10500, replaced(partA))
		queue(10600, partA)
		queue(10700, partB)
		k.SetRagnarokPending(ctx, 2)

		paid, vault := partA, vaultA
		if tc.partB {
			paid, vault = partB, vaultB
		}
		from, err := vault.PubKeyEddsa.GetAddress(common.SOLChain)
		c.Assert(err, IsNil)
		payTo := to
		if tc.stranger {
			payTo, err = GetRandomEd25519PubKey().GetAddress(common.SOLChain)
			c.Assert(err, IsNil)
		}
		tx := NewObservedTx(
			common.NewTx(GetRandomTxHash(), from, payTo, common.NewCoins(paid.Coin), paid.MaxGas, paid.Memo),
			observed, vault.PubKeyEddsa, observed,
		)
		_, err = NewRagnarokHandler(mgr).handle(ctx, *NewMsgRagnarok(tx, scheduled, GetRandomBech32Addr()))
		c.Assert(err, IsNil, comment)

		got, err := k.GetTxOut(ctx, tc.live)
		c.Assert(err, IsNil)
		c.Assert(got.TxArray, HasLen, 1, comment)
		pending, err := k.GetRagnarokPending(ctx)
		c.Assert(err, IsNil)
		if tc.stranger {
			c.Check(got.TxArray[0].OutHash.IsEmpty(), Equals, true, comment)
			c.Check(recorder.calls, Equals, 1, comment)
			c.Check(pending, Equals, int64(2), comment)
			continue
		}
		c.Check(got.TxArray[0].OutHash.Equals(tx.Tx.ID), Equals, true, comment)
		c.Check(recorder.calls, Equals, 0, comment)
		c.Check(pending, Equals, int64(1), comment)
		// the other part's live copy is left for its own payment
		other := int64(10700)
		if tc.partB {
			other = 10600
		}
		left, err := k.GetTxOut(ctx, other)
		c.Assert(err, IsNil)
		c.Assert(left.TxArray, HasLen, 1, comment)
		c.Check(left.TxArray[0].OutHash.IsEmpty(), Equals, true, comment)
	}
}
