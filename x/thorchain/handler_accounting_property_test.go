package thorchain

import (
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// This campaign deliberately keeps a ledger outside Pool and LP. The oracle
// only adds observed inputs and payouts; it does not call pool unit, swap, or
// withdrawal calculation functions to derive expected balances.
type AccountingPropertySuite struct{}

var _ = Suite(&AccountingPropertySuite{})

func accountingVaultPubKey(c *C, label string) common.PubKey {
	pub := secp256k1.GenPrivKeyFromSecret([]byte("accounting vault " + label)).PubKey()
	encoded, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pub)
	c.Assert(err, IsNil)
	return common.PubKey(encoded)
}

func accountingDigest(seed int64, label string) [32]byte {
	return sha256.Sum256([]byte(fmt.Sprintf("accounting:%d:%s", seed, label)))
}

func accountingTxID(seed int64, label string) common.TxID {
	h := accountingDigest(seed, label)
	return common.TxID(fmt.Sprintf("%X", h))
}

func accountingSigner(seed int64, label string) cosmos.AccAddress {
	h := accountingDigest(seed, label)
	return cosmos.AccAddress(h[:20])
}

func accountingThorAddress(seed int64, label string) common.Address {
	return common.Address(accountingSigner(seed, label).String())
}

func accountingETHAddress(seed int64, label string) common.Address {
	h := accountingDigest(seed, label)
	addr, err := common.NewAddress(fmt.Sprintf("0x%x", h[:20]))
	if err != nil {
		panic(err)
	}
	return addr
}

func accountingLedgerBalanced(poolRune, poolAsset, pendingRune, pendingAsset, paidRune, paidAsset, feeRune, inRune, inAsset uint64) bool {
	return poolRune+pendingRune+paidRune+feeRune == inRune &&
		poolAsset+pendingAsset+paidAsset == inAsset
}

func (AccountingPropertySuite) TestOracleDetectsPerturbation(c *C) {
	// Every term in the ledger is independently observable. A one-unit
	// accounting error must be detectable even below a normal swap amount.
	c.Assert(accountingLedgerBalanced(80, 90, 5, 3, 10, 7, 5, 100, 100), Equals, true)
	c.Assert(accountingLedgerBalanced(81, 90, 5, 3, 10, 7, 5, 100, 100), Equals, false)
	c.Assert(accountingLedgerBalanced(80, 90, 6, 3, 10, 7, 5, 100, 100), Equals, false)
	c.Assert(accountingLedgerBalanced(80, 90, 5, 3, 10, 8, 5, 100, 100), Equals, false)
	c.Assert(accountingLedgerBalanced(80, 90, 5, 3, 10, 7, 6, 100, 100), Equals, false)
}

func accountingPropertySeeds() []int64 {
	if s := os.Getenv("THORNODE_ACCOUNTING_SEED"); s != "" {
		seed, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			panic("invalid THORNODE_ACCOUNTING_SEED: " + err.Error())
		}
		return []int64{seed}
	}
	n := 4
	if s := os.Getenv("THORNODE_ACCOUNTING_SEEDS"); s != "" {
		var err error
		n, err = strconv.Atoi(s)
		if err != nil || n < 1 || n > 256 {
			panic("THORNODE_ACCOUNTING_SEEDS must be between 1 and 256")
		}
	}
	seeds := make([]int64, n)
	for i := range seeds {
		seeds[i] = int64(i + 1)
	}
	if n == 4 {
		// Seed 15 reaches a rounded-to-zero 1-bps withdrawal after dust adds.
		seeds[3] = 15
	}
	return seeds
}

func (AccountingPropertySuite) TestPoolFlowConservation(c *C) {
	for _, seed := range accountingPropertySeeds() {
		accountingPoolFlow(c, seed)
	}
}

// A swap transfers its implicit fee in RUNE to Reserve. This checks the
// complete pool plus Reserve flow for both directions, including three-part
// sub-swap sequences. Those parts call the swapper directly with a fresh
// StreamingSwap and no destination, so they cover pool arithmetic and event
// counters only; TestPersistedStreamingSwapConservation covers a queued V2
// stream's accumulated msg.State and its eventual outbound.
func (AccountingPropertySuite) TestSwapFlowConservation(c *C) {
	for _, seed := range accountingPropertySeeds() {
		accountingSwapFlow(c, seed)
	}
}

func accountingSwapFlow(c *C, seed int64) {
	//nolint:gosec // This seeded generator produces reproducible test cases, not cryptographic values.
	rng := rand.New(rand.NewSource(seed))
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	mgr.txOutStore = NewTxStoreDummy()
	asset := common.ETHAsset
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.Decimals = 8
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	reserveStart := k.GetBalanceOfModule(ctx, ReserveName, common.RuneAsset().Native()).Uint64()
	inRune, inAsset := pool.BalanceRune.Uint64(), pool.BalanceAsset.Uint64()
	var outRune, outAsset uint64
	trace := []string{fmt.Sprintf("seed=%d", seed)}
	fail := func(format string, args ...any) {
		c.Fatalf("%s: %s", strings.Join(trace, " | "), fmt.Sprintf(format, args...))
	}
	check := func() {
		p, err := k.GetPool(ctx, asset)
		if err != nil {
			fail("get pool: %v", err)
		}
		reserve := k.GetBalanceOfModule(ctx, ReserveName, common.RuneAsset().Native()).Uint64()
		if reserve < reserveStart || !accountingLedgerBalanced(
			p.BalanceRune.Uint64(), p.BalanceAsset.Uint64(), 0, 0, outRune, outAsset,
			reserve-reserveStart, inRune, inAsset) {
			fail("swap flow mismatch: pool=%s/%s paid=%d/%d reserve delta=%d in=%d/%d",
				p.BalanceRune, p.BalanceAsset, outRune, outAsset, reserve-reserveStart, inRune, inAsset)
		}
		if !p.LPUnits.Equal(pool.LPUnits) {
			fail("swap changed LP units: %s", p.LPUnits)
		}
	}
	check()
	for step := 0; step < 12; step++ {
		fromRune := step%2 == 0
		source, target := asset, common.RuneAsset()
		from := accountingETHAddress(seed, "swap-eth-from")
		if fromRune {
			source, target = common.RuneAsset(), asset
			from = accountingThorAddress(seed, "swap-rune-from")
		}
		amount := uint64(100_000_000 + rng.Intn(9)*10_000_000)
		parts := uint64(1)
		if step%3 == 0 {
			parts = 3
		}
		trace = append(trace, fmt.Sprintf("swap %s->%s amount=%d parts=%d", source, target, amount, parts))
		for part := uint64(0); part < parts; part++ {
			subAmount := amount / parts
			if part == parts-1 {
				subAmount += amount % parts
			}
			var gas common.Gas
			if !fromRune {
				gas = common.Gas{common.NewCoin(asset, cosmos.OneUint())}
			}
			tx := common.NewTx(accountingTxID(seed, fmt.Sprintf("swap-%d-%d", step, part)), from, from,
				common.NewCoins(common.NewCoin(source, cosmos.NewUint(subAmount))), gas, "")
			tx.Chain = source.GetChain()
			swp := StreamingSwap{}
			if parts > 1 {
				swp.Quantity, swp.Count, swp.Interval = parts, part, 1
			}
			beforeEvents := len(ctx.EventManager().Events())
			emit, used, events, err := newSwapper().Swap(ctx, k, tx, target, common.NoopAddress,
				cosmos.ZeroUint(), "", "", nil, swp, 20_000, mgr, nil, nil)
			if err != nil {
				fail("swap part=%d: %v", part, err)
			}
			if !used.Equal(cosmos.NewUint(subAmount)) || len(events) != 1 ||
				!events[0].EmitAsset.Amount.Equal(emit) || len(ctx.EventManager().Events()) <= beforeEvents {
				fail("swap result/event mismatch: used=%s emitted=%s events=%d", used, emit, len(events))
			}
			if parts > 1 && (events[0].StreamingSwapQuantity != parts || events[0].StreamingSwapCount != part+1) {
				fail("streaming event count=%d/%d, want %d/%d", events[0].StreamingSwapCount, events[0].StreamingSwapQuantity, part+1, parts)
			}
			if fromRune {
				inRune += subAmount
				outAsset += emit.Uint64()
			} else {
				inAsset += subAmount
				outRune += emit.Uint64()
			}
			check()
		}
	}
	// The equal-asset rejection is before any pool write, event or outbound.
	before, _ := k.GetPool(ctx, asset)
	beforeEvents := len(ctx.EventManager().Events())
	beforeOut, err := mgr.TxOutStore().GetOutboundItems(ctx)
	if err != nil {
		fail("get outbound items: %v", err)
	}
	tx := common.NewTx(accountingTxID(seed, "rejected-swap"), accountingETHAddress(seed, "reject-from"), accountingETHAddress(seed, "reject-to"),
		common.NewCoins(common.NewCoin(asset, cosmos.NewUint(common.One))),
		common.Gas{common.NewCoin(asset, cosmos.OneUint())}, "")
	tx.Chain = asset.GetChain()
	trace = append(trace, "reject equal-asset swap")
	_, _, _, err = newSwapper().Swap(ctx, k, tx, asset, common.NoopAddress, cosmos.ZeroUint(), "", "", nil, StreamingSwap{}, 20_000, mgr, nil, nil)
	if err == nil {
		fail("equal-asset swap accepted")
	}
	after, _ := k.GetPool(ctx, asset)
	afterOut, err := mgr.TxOutStore().GetOutboundItems(ctx)
	if err != nil || !reflect.DeepEqual(before, after) || len(ctx.EventManager().Events()) != beforeEvents || len(beforeOut) != len(afterOut) {
		fail("rejected swap mutated pool, events or outbound: %v", err)
	}
	check()
}

// The advanced swap queue holds a V2 stream's accumulated input and output in
// msg.State until the final sub-swap, then settleSwap pays State.Out as one
// outbound. Drive a market stream through AdvSwapQueueMgr and reconcile every
// block: the stored In/Out must match what the pool and Reserve independently
// observed, no outbound may appear early, and the final outbound must equal
// the total output the pool released.
func (AccountingPropertySuite) TestPersistedStreamingSwapConservation(c *C) {
	for _, seed := range accountingPropertySeeds() {
		accountingPersistedStream(c, seed)
	}
}

func accountingPersistedStream(c *C, seed int64) {
	//nolint:gosec // This seeded generator produces reproducible test cases, not cryptographic values.
	rng := rand.New(rand.NewSource(seed))
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	mgr.txOutStore = NewTxStoreDummy()
	asset := common.ETHAsset
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.Decimals = 8
	pool.BalanceRune = cosmos.NewUint(1000 * common.One)
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.LPUnits = cosmos.NewUint(1000 * common.One)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	reserveStart := k.GetBalanceOfModule(ctx, ReserveName, common.RuneAsset().Native()).Uint64()

	// With a nonzero min slip, streaming quantity is capped by deposit /
	// (min-slip share of pool depth). Set a 10 bps floor, 1 RUNE per sub-swap
	// on 1000 RUNE depth, so admission runs the cap; the 10-18 RUNE deposit
	// still leaves room for the requested 3-5 sub-swaps.
	k.SetMimir(ctx, constants.L1SlipMinBps.String(), 10)
	quantity := uint64(3 + rng.Intn(3))
	amount := uint64(10*common.One + rng.Intn(9)*common.One)
	// The outbound leg is L1 so the dummy txout store records it.
	from := accountingThorAddress(seed, "stream-from")
	dest := accountingETHAddress(seed, "stream-dest")
	txID := accountingTxID(seed, "stream")
	trace := fmt.Sprintf("seed=%d stream RUNE->ETH amount=%d quantity=%d", seed, amount, quantity)
	tx := common.NewTx(txID, from, from,
		common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(amount))),
		common.Gas{}, "=:ETH.ETH:"+dest.String())
	tx.Chain = common.THORChain
	msg := NewMsgSwap(tx, asset, dest, cosmos.ZeroUint(), common.NoAddress, cosmos.ZeroUint(),
		"", "", nil, types.SwapType_market, quantity, 1, types.SwapVersion_v2, accountingSigner(seed, "stream-signer"))
	queue := mgr.AdvSwapQueueMgr()
	c.Assert(queue.AddSwapQueueItem(ctx, mgr, msg), IsNil, Commentf(trace))
	queued, err := k.GetAdvSwapQueueItem(ctx, txID, 0)
	c.Assert(err, IsNil, Commentf(trace))
	c.Assert(queued.State.Quantity, Equals, quantity, Commentf("%s admitted quantity", trace))

	outbounds := func() []TxOutItem {
		items, err := mgr.TxOutStore().GetOutboundItems(ctx)
		c.Assert(err, IsNil, Commentf(trace))
		var matched []TxOutItem
		for _, item := range items {
			if item.InHash.Equals(txID) {
				matched = append(matched, item)
			}
		}
		return matched
	}
	// released returns the RUNE the stream has consumed (kept by the pool or
	// paid to Reserve as fee) and the asset the pool has released, observed
	// from pool and Reserve balances only.
	released := func() (uint64, uint64) {
		p, err := k.GetPool(ctx, asset)
		c.Assert(err, IsNil, Commentf(trace))
		reserve := k.GetBalanceOfModule(ctx, ReserveName, common.RuneAsset().Native()).Uint64()
		c.Assert(reserve >= reserveStart, Equals, true, Commentf(trace))
		c.Assert(p.LPUnits.Equal(pool.LPUnits), Equals, true, Commentf(trace))
		runeIn := p.BalanceRune.Uint64() - pool.BalanceRune.Uint64() + (reserve - reserveStart)
		return runeIn, pool.BalanceAsset.Uint64() - p.BalanceAsset.Uint64()
	}

	for block := int64(1); block <= int64(quantity); block++ {
		ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
		c.Assert(queue.EndBlock(ctx, mgr, false), IsNil, Commentf("%s block=%d", trace, block))
		runeIn, assetOut := released()
		if uint64(block) < quantity {
			item, err := k.GetAdvSwapQueueItem(ctx, txID, 0)
			c.Assert(err, IsNil, Commentf("%s block=%d", trace, block))
			c.Assert(item.State.Count, Equals, uint64(block), Commentf("%s block=%d", trace, block))
			c.Assert(item.State.FailedSwaps, HasLen, 0, Commentf("%s block=%d %v", trace, block, item.State.FailedSwapReasons))
			c.Assert(item.State.In.Uint64(), Equals, runeIn, Commentf("%s block=%d stored In", trace, block))
			c.Assert(item.State.Out.Uint64(), Equals, assetOut, Commentf("%s block=%d stored Out", trace, block))
			c.Assert(outbounds(), HasLen, 0, Commentf("%s block=%d early outbound", trace, block))
			continue
		}
		c.Assert(k.HasAdvSwapQueueItem(ctx, txID, 0), Equals, false, Commentf(trace))
		c.Assert(runeIn, Equals, amount, Commentf("%s final In", trace))
		items := outbounds()
		c.Assert(items, HasLen, 1, Commentf("%s final outbound", trace))
		c.Assert(items[0].Coin.Asset.Equals(asset), Equals, true, Commentf(trace))
		c.Assert(items[0].ToAddress.Equals(dest), Equals, true, Commentf(trace))
		c.Assert(items[0].Coin.Amount.Uint64(), Equals, assetOut, Commentf("%s final outbound amount", trace))
	}

	// Further blocks must not re-pay the completed stream.
	ctx = ctx.WithBlockHeight(ctx.BlockHeight() + 1)
	c.Assert(queue.EndBlock(ctx, mgr, false), IsNil, Commentf(trace))
	c.Assert(outbounds(), HasLen, 1, Commentf("%s repeated payout", trace))
}

// A staged deposit is a refundable liability even though it has minted no LP
// units. Repeating the withdrawal must not pay it twice.
func (AccountingPropertySuite) TestPendingLiquidityRefundConservation(c *C) {
	for _, seed := range accountingPropertySeeds() {
		//nolint:gosec // This seeded generator produces reproducible test cases, not cryptographic values.
		rng := rand.New(rand.NewSource(seed))
		ctx, mgr := setupManagerForTest(c)
		k := mgr.Keeper()
		pool := NewPool()
		pool.Asset = common.ETHAsset
		pool.Status = PoolAvailable
		pool.Decimals = 8
		c.Assert(k.SetPool(ctx, pool), IsNil)
		runeAddr, assetAddr := accountingThorAddress(seed, "pending-rune"), accountingETHAddress(seed, "pending-asset")
		amount := uint64(1 + rng.Intn(10_000_000))
		trace := fmt.Sprintf("seed=%d stage-rune=%d refund repeat", seed, amount)
		add := NewAddLiquidityHandler(mgr)
		err := add.addLiquidity(ctx, common.ETHAsset, cosmos.NewUint(amount), cosmos.ZeroUint(),
			runeAddr, assetAddr, accountingTxID(seed, "pending-stage"), true, constants.GetConstantValues(GetCurrentVersion()))
		c.Assert(err, IsNil, Commentf(trace))
		staged, err := k.GetPool(ctx, common.ETHAsset)
		c.Assert(err, IsNil, Commentf(trace))
		c.Assert(staged.PendingInboundRune.Uint64(), Equals, amount, Commentf(trace))
		c.Assert(staged.BalanceRune.IsZero(), Equals, true, Commentf(trace))
		msg := NewMsgWithdrawLiquidity(common.Tx{ID: accountingTxID(seed, "pending-refund")}, runeAddr, cosmos.NewUint(10_000), common.ETHAsset, common.EmptyAsset, accountingSigner(seed, "pending-signer"))
		r, a, units, gas, err := withdraw(ctx, *msg, mgr)
		c.Assert(err, IsNil, Commentf(trace))
		c.Assert(r.Uint64(), Equals, amount, Commentf(trace))
		c.Assert(a.IsZero() && units.IsZero() && gas.IsZero(), Equals, true, Commentf(trace))
		after, err := k.GetPool(ctx, common.ETHAsset)
		c.Assert(err, IsNil, Commentf(trace))
		c.Assert(after.PendingInboundRune.IsZero() && after.BalanceRune.IsZero(), Equals, true, Commentf(trace))
		beforeEvents := len(ctx.EventManager().Events())
		r, a, _, _, err = withdraw(ctx, *msg, mgr)
		c.Assert(err, NotNil, Commentf(trace))
		c.Assert(r.IsZero() && a.IsZero(), Equals, true, Commentf(trace))
		repeated, _ := k.GetPool(ctx, common.ETHAsset)
		c.Assert(reflect.DeepEqual(after, repeated), Equals, true, Commentf(trace))
		c.Assert(len(ctx.EventManager().Events()), Equals, beforeEvents, Commentf(trace))
	}
}

func (AccountingPropertySuite) TestSeed15RoundedWithdrawRegression(c *C) {
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset := common.ETHAsset
	addr := accountingThorAddress(15, "rounded-rune")
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.Decimals = 8
	pool.BalanceRune = cosmos.NewUint(common.One)
	pool.BalanceAsset = cosmos.NewUint(common.One)
	pool.LPUnits = cosmos.NewUint(664)
	c.Assert(k.SetPool(ctx, pool), IsNil)
	lp := LiquidityProvider{Asset: asset, RuneAddress: addr, AssetAddress: accountingETHAddress(15, "rounded-asset"), Units: cosmos.NewUint(664)}
	k.SetLiquidityProvider(ctx, lp)
	before, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	beforeLP, err := k.GetLiquidityProvider(ctx, asset, addr)
	c.Assert(err, IsNil)
	beforeEvents := len(ctx.EventManager().Events())
	msg := NewMsgWithdrawLiquidity(common.Tx{ID: accountingTxID(15, "rounded-withdraw")}, addr, cosmos.OneUint(), asset, common.EmptyAsset, accountingSigner(15, "rounded-signer"))
	r, a, units, gas, err := withdraw(ctx, *msg, mgr)
	c.Assert(err, NotNil, Commentf("seed 15 minimized: 664 units x 1/10000 rounds to zero"))
	c.Assert(r.IsZero() && a.IsZero() && units.IsZero() && gas.IsZero(), Equals, true)
	after, err := k.GetPool(ctx, asset)
	c.Assert(err, IsNil)
	afterLP, err := k.GetLiquidityProvider(ctx, asset, addr)
	c.Assert(err, IsNil)
	c.Assert(reflect.DeepEqual(before, after) && reflect.DeepEqual(beforeLP, afterLP), Equals, true)
	c.Assert(len(ctx.EventManager().Events()), Equals, beforeEvents)
}

func (AccountingPropertySuite) TestNativeDepositPendingConservation(c *C) {
	for _, seed := range accountingPropertySeeds() {
		//nolint:gosec // This seeded generator produces reproducible test cases, not cryptographic values.
		rng := rand.New(rand.NewSource(seed))
		ctx, mgr := setupManagerForTest(c)
		ctx = ctx.WithTxBytes([]byte(fmt.Sprintf("accounting-deposit-%d", seed)))
		k := mgr.Keeper()
		node := GetRandomValidatorNode(NodeActive)
		c.Assert(k.SetNodeAccount(ctx, node), IsNil)
		FundModule(c, ctx, k, BondName, node.Bond.Uint64())
		pool := NewPool()
		pool.Asset = common.ETHAsset
		pool.Status = PoolAvailable
		pool.Decimals = 8
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		pool.LPUnits = cosmos.NewUint(100 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		signer := accountingSigner(seed, "deposit-signer")
		assetAddr := accountingETHAddress(seed, "deposit-asset")
		amount := uint64(1_000_000 + rng.Intn(10_000_000))
		FundAccount(c, ctx, k, signer, amount*2)
		accountBefore := k.GetBalance(ctx, signer).AmountOf(common.RuneAsset().Native()).Uint64()
		asgardBefore := k.GetBalanceOfModule(ctx, AsgardName, common.RuneAsset().Native()).Uint64()
		eventsBefore := len(ctx.EventManager().Events())
		trace := fmt.Sprintf("seed=%d native-deposit=%d", seed, amount)
		msg := NewMsgDeposit(common.NewCoins(common.NewCoin(common.RuneNative, cosmos.NewUint(amount))),
			"ADD:ETH.ETH:"+assetAddr.String(), signer)
		_, err := NewDepositHandler(mgr).Run(ctx, msg)
		c.Assert(err, IsNil, Commentf(trace))
		accountAfter := k.GetBalance(ctx, signer).AmountOf(common.RuneAsset().Native()).Uint64()
		asgardAfter := k.GetBalanceOfModule(ctx, AsgardName, common.RuneAsset().Native()).Uint64()
		c.Assert(accountBefore-accountAfter, Equals, amount, Commentf(trace))
		c.Assert(asgardAfter-asgardBefore, Equals, amount, Commentf(trace))
		p, err := k.GetPool(ctx, common.ETHAsset)
		c.Assert(err, IsNil, Commentf(trace))
		c.Assert(p.PendingInboundRune.Uint64(), Equals, amount, Commentf(trace))
		c.Assert(p.BalanceRune.Equal(pool.BalanceRune) && p.BalanceAsset.Equal(pool.BalanceAsset), Equals, true, Commentf(trace))
		lp, err := k.GetLiquidityProvider(ctx, common.ETHAsset, common.Address(signer.String()))
		c.Assert(err, IsNil, Commentf(trace))
		c.Assert(lp.PendingRune.Uint64(), Equals, amount, Commentf(trace))
		c.Assert(len(ctx.EventManager().Events()) > eventsBefore, Equals, true, Commentf(trace))

		// Bad memo fails before the bank transfer and cannot create a claim.
		bad := NewMsgDeposit(common.NewCoins(common.NewCoin(common.RuneNative, cosmos.OneUint())), "BOGUS", signer)
		beforePool, _ := k.GetPool(ctx, common.ETHAsset)
		beforeLP, _ := k.GetLiquidityProvider(ctx, common.ETHAsset, common.Address(signer.String()))
		beforeEvents := len(ctx.EventManager().Events())
		_, err = NewDepositHandler(mgr).Run(ctx, bad)
		c.Assert(err, NotNil, Commentf(trace))
		afterPool, _ := k.GetPool(ctx, common.ETHAsset)
		afterLP, _ := k.GetLiquidityProvider(ctx, common.ETHAsset, common.Address(signer.String()))
		c.Assert(reflect.DeepEqual(beforePool, afterPool) && reflect.DeepEqual(beforeLP, afterLP), Equals, true, Commentf(trace))
		c.Assert(k.GetBalance(ctx, signer).AmountOf(common.RuneAsset().Native()).Uint64(), Equals, accountAfter, Commentf(trace))
		c.Assert(k.GetBalanceOfModule(ctx, AsgardName, common.RuneAsset().Native()).Uint64(), Equals, asgardAfter, Commentf(trace))
		c.Assert(len(ctx.EventManager().Events()), Equals, beforeEvents, Commentf(trace))
	}
}

// Vault coin bookkeeping and persistence only: the test itself debits one
// Asgard record with SubFundsWithDeficit and credits another with AddFunds,
// then checks that the keeper round-trip preserves the total (including dust
// remainders) and leaves pool claims untouched. It does NOT drive churn: no
// migrate outbound is scheduled or observed, so it cannot catch a migration
// that misses a credit or repeats a debit. Real migrate scheduling is covered
// by the network manager MigrateFunds tests, and end-to-end churn solvency by
// test-simulation's solvency checks.
func (AccountingPropertySuite) TestVaultFundsTransferPersistence(c *C) {
	for _, seed := range accountingPropertySeeds() {
		//nolint:gosec // This seeded generator produces reproducible test cases, not cryptographic values.
		rng := rand.New(rand.NewSource(seed))
		ctx, mgr := setupManagerForTest(c)
		k := mgr.Keeper()
		asset := common.ETHAsset
		pool := NewPool()
		pool.Asset = asset
		pool.Status = PoolAvailable
		pool.BalanceRune = cosmos.NewUint(100 * common.One)
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		pool.LPUnits = cosmos.NewUint(100 * common.One)
		c.Assert(k.SetPool(ctx, pool), IsNil)
		retiring := NewVault(1, RetiringVault, AsgardVault, accountingVaultPubKey(c, "A"), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
		active := NewVault(1, ActiveVault, AsgardVault, accountingVaultPubKey(c, "B"), common.Chains{common.ETHChain}.Strings(), []ChainContract{})
		initial := uint64(100*common.One) + uint64(rng.Intn(1000))
		retiring.AddFunds(common.NewCoins(common.NewCoin(asset, cosmos.NewUint(initial))))
		c.Assert(k.SetVault(ctx, retiring), IsNil)
		c.Assert(k.SetVault(ctx, active), IsNil)
		trace := []string{fmt.Sprintf("seed=%d vault-start=%d", seed, initial)}
		for step := 0; step < 12; step++ {
			move := uint64(1 + rng.Int63n(int64(initial/12)))
			trace = append(trace, fmt.Sprintf("move=%d", move))
			deficit := retiring.SubFundsWithDeficit(common.NewCoins(common.NewCoin(asset, cosmos.NewUint(move))))
			c.Assert(deficit.IsEmpty(), Equals, true, Commentf(strings.Join(trace, " | ")))
			active.AddFunds(common.NewCoins(common.NewCoin(asset, cosmos.NewUint(move))))
			c.Assert(k.SetVault(ctx, retiring), IsNil, Commentf(strings.Join(trace, " | ")))
			c.Assert(k.SetVault(ctx, active), IsNil, Commentf(strings.Join(trace, " | ")))
			r, err := k.GetVault(ctx, retiring.PubKey)
			c.Assert(err, IsNil, Commentf(strings.Join(trace, " | ")))
			a, err := k.GetVault(ctx, active.PubKey)
			c.Assert(err, IsNil, Commentf(strings.Join(trace, " | ")))
			c.Assert(r.GetCoin(asset).Amount.Uint64()+a.GetCoin(asset).Amount.Uint64(), Equals, initial, Commentf(strings.Join(trace, " | ")))
			p, err := k.GetPool(ctx, asset)
			c.Assert(err, IsNil, Commentf(strings.Join(trace, " | ")))
			c.Assert(reflect.DeepEqual(pool, p), Equals, true, Commentf(strings.Join(trace, " | ")))
		}
	}
}

func accountingPoolFlow(c *C, seed int64) {
	//nolint:gosec // This seeded generator produces reproducible test cases, not cryptographic values.
	rng := rand.New(rand.NewSource(seed))
	ctx, mgr := setupManagerForTest(c)
	k := mgr.Keeper()
	asset := common.ETHAsset
	runeAddr, assetAddr := accountingThorAddress(seed, "liquidity-rune"), accountingETHAddress(seed, "liquidity-asset")
	otherAssetAddr := accountingETHAddress(seed, "liquidity-other")
	txSequence := 0
	nextTxID := func() common.TxID {
		txSequence++
		return accountingTxID(seed, fmt.Sprintf("liquidity-tx-%d", txSequence))
	}
	pool := NewPool()
	pool.Asset = asset
	pool.Status = PoolAvailable
	pool.Decimals = 8
	c.Assert(k.SetPool(ctx, pool), IsNil)
	add := NewAddLiquidityHandler(mgr)
	consts := constants.GetConstantValues(GetCurrentVersion())
	var inRune, inAsset, outRune, outAsset uint64
	var minted, completed, withdrawn int
	trace := []string{fmt.Sprintf("seed=%d", seed)}
	fail := func(format string, args ...any) {
		c.Fatalf("%s: %s", strings.Join(trace, " | "), fmt.Sprintf(format, args...))
	}
	// The only deposit rejection this campaign expects is dust that rounds
	// to zero LP units against a deep pool. Anything else is a handler
	// regression, even when it leaves state untouched.
	expectedAddRejection := func(err error) bool {
		return strings.Contains(err.Error(), "liquidity units cannot be zero when deposit amounts are non-zero")
	}
	check := func() {
		p, err := k.GetPool(ctx, asset)
		if err != nil {
			fail("get pool: %v", err)
		}
		lp, err := k.GetLiquidityProvider(ctx, asset, runeAddr)
		if err != nil {
			fail("get LP: %v", err)
		}
		if !accountingLedgerBalanced(
			p.BalanceRune.Uint64(), p.BalanceAsset.Uint64(), p.PendingInboundRune.Uint64(),
			p.PendingInboundAsset.Uint64(), outRune, outAsset, 0, inRune, inAsset) {
			fail("flow mismatch: rune pool=%s pending=%s paid=%d in=%d; asset pool=%s pending=%s paid=%d in=%d",
				p.BalanceRune, p.PendingInboundRune, outRune, inRune,
				p.BalanceAsset, p.PendingInboundAsset, outAsset, inAsset)
		}
		if lp.PendingRune.Uint64() != p.PendingInboundRune.Uint64() || lp.PendingAsset.Uint64() != p.PendingInboundAsset.Uint64() {
			fail("pending obligation mismatch: LP=%s/%s pool=%s/%s", lp.PendingRune, lp.PendingAsset, p.PendingInboundRune, p.PendingInboundAsset)
		}
		if lp.Units.Uint64() != p.LPUnits.Uint64() {
			fail("LP unit liability mismatch: LP=%s pool=%s", lp.Units, p.LPUnits)
		}
	}
	amounts := []uint64{1, 10, 100_000_000, 100_000_001, 500_000_000, 10_000_000_000}
	for step := 0; step < 24; step++ {
		p, err := k.GetPool(ctx, asset)
		if err != nil {
			fail("get pool: %v", err)
		}
		lp, err := k.GetLiquidityProvider(ctx, asset, runeAddr)
		if err != nil {
			fail("get LP: %v", err)
		}
		// A failed address binding must not change balances, pending claims,
		// units, events, or outbound scheduling.
		if step%6 == 5 && !lp.Units.IsZero() {
			beforeEvents := len(ctx.EventManager().Events())
			trace = append(trace, "reject mismatched add")
			err = add.addLiquidity(ctx, asset, cosmos.NewUint(common.One), cosmos.NewUint(common.One), runeAddr, otherAssetAddr, nextTxID(), false, consts)
			if err == nil {
				fail("mismatched add accepted")
			}
			after, _ := k.GetPool(ctx, asset)
			if !after.BalanceRune.Equal(p.BalanceRune) || !after.BalanceAsset.Equal(p.BalanceAsset) ||
				!after.PendingInboundRune.Equal(p.PendingInboundRune) || !after.PendingInboundAsset.Equal(p.PendingInboundAsset) ||
				!after.LPUnits.Equal(p.LPUnits) || len(ctx.EventManager().Events()) != beforeEvents {
				fail("rejected add mutated pool or events")
			}
			check()
			continue
		}
		if step%4 == 3 && !lp.Units.IsZero() {
			bps := []uint64{1, 3333, 5000, 9999}[rng.Intn(4)]
			trace = append(trace, fmt.Sprintf("withdraw %d bps", bps))
			msg := NewMsgWithdrawLiquidity(common.Tx{ID: nextTxID()}, runeAddr, cosmos.NewUint(bps), asset, common.EmptyAsset, accountingSigner(seed, "liquidity-signer"))
			beforeEvents := len(ctx.EventManager().Events())
			r, a, _, _, withdrawErr := withdraw(ctx, *msg, mgr)
			if withdrawErr != nil {
				if !strings.Contains(withdrawErr.Error(), "LP units cannot increase") {
					fail("withdraw: %v", withdrawErr)
				}
				after, _ := k.GetPool(ctx, asset)
				afterLP, _ := k.GetLiquidityProvider(ctx, asset, runeAddr)
				if !reflect.DeepEqual(p, after) || !reflect.DeepEqual(lp, afterLP) || len(ctx.EventManager().Events()) != beforeEvents || !r.IsZero() || !a.IsZero() {
					fail("rounded withdrawal rejection mutated state/events")
				}
				trace = append(trace, "rejected rounded withdrawal")
				check()
				continue
			}
			outRune += r.Uint64()
			outAsset += a.Uint64()
			withdrawn++
			check()
			continue
		}
		r, a := amounts[rng.Intn(len(amounts))], amounts[rng.Intn(len(amounts))]
		// Seeding an empty pool with a balanced deposit must always mint
		// units; no rejection is acceptable for it.
		initial := p.BalanceRune.IsZero() || p.BalanceAsset.IsZero()
		if initial {
			r, a = common.One, common.One
		}
		// Exercise split arrivals. The first leg is an outstanding claim;
		// the second consumes it and increases the active pool depth.
		if step%3 == 1 {
			trace = append(trace, fmt.Sprintf("stage rune %d", r))
			err = add.addLiquidity(ctx, asset, cosmos.NewUint(r), cosmos.ZeroUint(), runeAddr, assetAddr, nextTxID(), true, consts)
			if err != nil {
				fail("stage rune: %v", err)
			}
			inRune += r
			check()
			trace = append(trace, fmt.Sprintf("complete asset %d", a))
			p, _ = k.GetPool(ctx, asset)
			lp, _ = k.GetLiquidityProvider(ctx, asset, runeAddr)
			beforeEvents := len(ctx.EventManager().Events())
			err = add.addLiquidity(ctx, asset, cosmos.ZeroUint(), cosmos.NewUint(a), runeAddr, assetAddr, nextTxID(), true, consts)
			if err != nil {
				if initial || !expectedAddRejection(err) {
					fail("complete asset: %v", err)
				}
				after, _ := k.GetPool(ctx, asset)
				afterLP, _ := k.GetLiquidityProvider(ctx, asset, runeAddr)
				if !reflect.DeepEqual(p, after) || !reflect.DeepEqual(lp, afterLP) || len(ctx.EventManager().Events()) != beforeEvents {
					fail("rejected completion mutated state/events: %v", err)
				}
				trace = append(trace, "rejected dust completion")
				check()
				continue
			}
			completed++
		} else {
			trace = append(trace, fmt.Sprintf("add rune=%d asset=%d", r, a))
			beforeEvents := len(ctx.EventManager().Events())
			err = add.addLiquidity(ctx, asset, cosmos.NewUint(r), cosmos.NewUint(a), runeAddr, assetAddr, nextTxID(), false, consts)
			if err != nil {
				if initial || !expectedAddRejection(err) {
					fail("add: %v", err)
				}
				after, _ := k.GetPool(ctx, asset)
				afterLP, _ := k.GetLiquidityProvider(ctx, asset, runeAddr)
				if !reflect.DeepEqual(p, after) || !reflect.DeepEqual(lp, afterLP) || len(ctx.EventManager().Events()) != beforeEvents {
					fail("rejected add mutated state/events: %v", err)
				}
				trace = append(trace, "rejected dust add")
				check()
				continue
			}
			inRune += r
			minted++
		}
		inAsset += a
		check()
	}
	// Exercise the basis point cap and the gas amount retained in the pool.
	lp, err := k.GetLiquidityProvider(ctx, asset, runeAddr)
	if err != nil {
		fail("get LP before final withdraw: %v", err)
	}
	// The campaign must have exercised every transition it claims to cover;
	// a handler that rejected all deposits would otherwise pass vacuously.
	if minted == 0 || completed == 0 || withdrawn == 0 {
		fail("campaign did not exercise every transition: adds=%d completions=%d withdrawals=%d", minted, completed, withdrawn)
	}
	if lp.Units.IsZero() {
		fail("no LP units left for the final withdrawal")
	}
	trace = append(trace, "withdraw 10000 bps")
	msg := NewMsgWithdrawLiquidity(common.Tx{ID: nextTxID()}, runeAddr, cosmos.NewUint(10_000), asset, common.EmptyAsset, accountingSigner(seed, "liquidity-signer"))
	r, a, _, gas, err := withdraw(ctx, *msg, mgr)
	if err != nil {
		fail("full withdraw: %v", err)
	}
	outRune += r.Uint64()
	outAsset += a.Uint64()
	p, _ := k.GetPool(ctx, asset)
	if p.BalanceAsset.LT(gas) {
		fail("pool does not retain returned gas=%s", gas)
	}
	check()
}
