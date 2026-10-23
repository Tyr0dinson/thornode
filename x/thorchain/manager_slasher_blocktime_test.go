package thorchain

import (
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

// blockTimeChangeKeeper reports a block time history.
type blockTimeChangeKeeper struct {
	*TestSlashingLackKeeper
	history constants.BlockTimeHistory
}

func (k *blockTimeChangeKeeper) GetBlockTimeHistory(_ cosmos.Context) constants.BlockTimeHistory {
	return k.history
}

func (k *blockTimeChangeKeeper) GetConfigInt64Ref(ctx cosmos.Context, key constants.ConstantName) int64 {
	return k.GetConfigInt64(ctx, key)
}

// lackSigningReschedules runs LackSigning at block now over one unsigned
// outbound scheduled at height, under the given block time history, and reports
// whether it rescheduled the outbound.
func lackSigningReschedules(c *C, history constants.BlockTimeHistory, height, now int64) bool {
	ctx, _ := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(now)
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = ActiveVault
	vault.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(500000*common.One))}
	inHash := GetRandomTxHash()
	toi := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		VaultPubKey: vault.PubKey,
		ToAddress:   GetRandomBTCAddress(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(31239*common.One)),
		MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(150000))},
		Memo:        "REFUND:" + inHash.String(),
	}
	txOut := NewTxOut(height)
	txOut.TxArray = append(txOut.TxArray, toi)
	k := &blockTimeChangeKeeper{
		TestSlashingLackKeeper: &TestSlashingLackKeeper{
			txOut:       txOut,
			txOutHeight: height,
			na:          GetRandomValidatorNode(NodeActive),
			vaults:      Vaults{vault},
			voter:       ObservedTxVoter{Actions: []TxOutItem{toi}, FinalisedHeight: height},
			slashPts:    make(map[string]int64),
		},
		history: history,
	}
	mgr := NewDummyMgrWithKeeper(k)
	mgr.txOutStore = NewTxStoreDummy()
	c.Assert(newSlasher(k, NewDummyEventMgr()).LackSigning(ctx, mgr), IsNil)
	return !k.txOut.TxArray[0].OutHash.IsEmpty()
}

// When the block time goes up, an outbound scheduled under the faster block
// time is rescheduled once it is a full SigningTransactionPeriod old in
// wall-clock time: not in the change block, though the shorter period in blocks
// already lies behind it, and exactly once.
func (s *SlashingSuite) TestLackSigningAfterBlockTimeIncrease(c *C) {
	period := constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.SigningTransactionPeriod)
	refMs := constants.ThorchainBlockTime.Milliseconds()
	start := 2 * period
	change := 10 * period
	// half the block time from start, back to the default at change
	history := constants.BlockTimeHistory{{Height: start, Ms: refMs / 2}, {Height: change, Ms: refMs}}
	// period+5 fast blocks old at the change: 5 blocks past the period in blocks
	// it is scheduled under, but only about half a period old in time
	height := change - period - 5
	c.Check(lackSigningReschedules(c, history, height, change), Equals, false)

	// due once (period+4) half blocks plus the slow blocks up to it make a period
	due := change + (period*refMs-(period+4)*refMs/2)/refMs - 1
	c.Check(lackSigningReschedules(c, history, height, due-1), Equals, false)
	c.Check(lackSigningReschedules(c, history, height, due), Equals, true)
	c.Check(lackSigningReschedules(c, history, height, due+1), Equals, false)
}

// When the block time goes down, an outbound scheduled under the slower block
// time is rescheduled once it is a full period old in wall-clock time, sooner
// in blocks than the new period.
func (s *SlashingSuite) TestLackSigningAfterBlockTimeDecrease(c *C) {
	period := constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.SigningTransactionPeriod)
	refMs := constants.ThorchainBlockTime.Milliseconds()
	change := 10 * period
	history := constants.BlockTimeHistory{{Height: change, Ms: refMs / 2}}
	// half a period old at the change
	height := change - 1 - period/2
	// the remaining half period takes period fast blocks
	due := change + period - 1
	c.Check(lackSigningReschedules(c, history, height, due-1), Equals, false)
	c.Check(lackSigningReschedules(c, history, height, due), Equals, true)
	c.Check(lackSigningReschedules(c, history, height, due+1), Equals, false)
	// far short of the new period in blocks
	c.Check(due-height < 2*period, Equals, true)
}

// ScaleConfigInt64 scales to the block time in force at the current height.
func (k *blockTimeChangeKeeper) ScaleConfigInt64(ctx cosmos.Context, key constants.ConstantName, v int64) int64 {
	refMs := constants.ThorchainBlockTime.Milliseconds()
	if !key.Unit().Scales() {
		return v
	}
	return key.Unit().Scale(v, refMs, k.history.MsAt(ctx.BlockHeight(), refMs))
}

func (k *blockTimeChangeKeeper) RefBlocksSince(ctx cosmos.Context, height int64) int64 {
	refMs := constants.ThorchainBlockTime.Milliseconds()
	return k.history.ElapsedMs(height, ctx.BlockHeight(), refMs) / refMs
}

func (k *blockTimeChangeKeeper) RefHeightAfter(_ cosmos.Context, from, refBlocks int64) int64 {
	refMs := constants.ThorchainBlockTime.Milliseconds()
	return k.history.HeightAfter(from, refBlocks*refMs, refMs)
}

// Attempts are counted in wall-clock time: after the block time goes from
// 1500ms to 6000ms, the 1500ms blocks count for what they lasted, not as
// 6000ms blocks.
func (s *SlashingSuite) TestOutboundAttemptsAcrossBlockTimeIncrease(c *C) {
	ctx, k := setupKeeperForTest(c)
	c.Assert(constants.ThorchainBlockTime.Milliseconds(), Equals, int64(1000))
	period := k.GetConstants().GetInt64Value(constants.SigningTransactionPeriod)
	c.Assert(period, Equals, int64(300))
	// a 1500ms to 6000ms change, past the rate limit
	k.SetBlockTimeHistory(ctx, 6000, constants.BlockTimeHistory{{Height: 100, Ms: 1500}, {Height: 10100, Ms: 6000}})

	attempts := func(now int64) int64 {
		return outboundAttempts(ctx.WithBlockHeight(now), k, 100, period)
	}
	// 9999 blocks of 1500ms: 49 periods, where counting blocks at the new
	// period (50 blocks) would make it 199
	c.Check(attempts(10099), Equals, int64(49))
	c.Check(k.ScaleConfigInt64(ctx.WithBlockHeight(10099), constants.SigningTransactionPeriod, period), Equals, int64(50))
	// 100 periods once 2501 blocks of 6000ms are added
	c.Check(attempts(12599), Equals, int64(99))
	c.Check(attempts(12600), Equals, int64(100))
}

// lackSigningRetiredVault runs LackSigning at the block an unsigned outbound
// from a retired vault, scheduled at height and finalised at 100, falls due
// under history, and reports whether it was rescheduled rather than abandoned
// for too many attempts.
func lackSigningRetiredVault(c *C, history constants.BlockTimeHistory, height int64) bool {
	refMs := constants.ThorchainBlockTime.Milliseconds()
	period := constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.SigningTransactionPeriod)
	now := history.HeightAfter(height, period*refMs, refMs)
	ctx, _ := setupKeeperForTest(c)
	ctx = ctx.WithBlockHeight(now)
	vault := GetRandomVault()
	vault.Type = AsgardVault
	vault.Status = InactiveVault
	vault.Coins = common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(500000*common.One))}
	inHash := GetRandomTxHash()
	toi := TxOutItem{
		Chain:       common.BTCChain,
		InHash:      inHash,
		VaultPubKey: vault.PubKey,
		ToAddress:   GetRandomBTCAddress(),
		Coin:        common.NewCoin(common.BTCAsset, cosmos.NewUint(31239*common.One)),
		MaxGas:      common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(150000))},
		Memo:        "REFUND:" + inHash.String(),
	}
	txOut := NewTxOut(height)
	txOut.TxArray = append(txOut.TxArray, toi)
	k := &blockTimeChangeKeeper{
		TestSlashingLackKeeper: &TestSlashingLackKeeper{
			txOut:       txOut,
			txOutHeight: height,
			na:          GetRandomValidatorNode(NodeActive),
			vaults:      Vaults{vault},
			voter:       ObservedTxVoter{Actions: []TxOutItem{toi}, FinalisedHeight: 100},
			slashPts:    make(map[string]int64),
		},
		history: history,
	}
	mgr := NewDummyMgrWithKeeper(k)
	mgr.txOutStore = NewTxStoreDummy()
	c.Assert(newSlasher(k, NewDummyEventMgr()).LackSigning(ctx, mgr), IsNil)
	return !k.txOut.TxArray[0].OutHash.IsEmpty()
}

// A retired-vault outbound is abandoned after MaxRetiredVaultRecoveryAttempts
// signing periods of wall-clock time, however many blocks a faster block time
// packed into them.
func (s *SlashingSuite) TestRetiredVaultAttemptsAcrossBlockTimeIncrease(c *C) {
	c.Assert(constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.MaxRetiredVaultRecoveryAttempts), Equals, int64(100))
	history := constants.BlockTimeHistory{{Height: 100, Ms: 1500}, {Height: 10100, Ms: 6000}}
	// due at 10150: 51 periods since 100, though 10050 blocks are 201 periods
	// of the current 50-block period
	c.Check(lackSigningRetiredVault(c, history, 10100), Equals, true)
	// due at 12599: 99 periods
	c.Check(lackSigningRetiredVault(c, history, 12549), Equals, true)
	// due at 12600: 100 periods
	c.Check(lackSigningRetiredVault(c, history, 12550), Equals, false)
}

// A non-finalised outbound observation goes stale after three signing periods
// of wall-clock time, not three periods of blocks at the current block time.
func (s *SlashingSuite) TestStaleOutboundObservationAcrossBlockTimeIncrease(c *C) {
	ctx, _ := setupKeeperForTest(c)
	period := constants.GetConstantValues(GetCurrentVersion()).GetInt64Value(constants.SigningTransactionPeriod)
	c.Assert(constants.ThorchainBlockTime.Milliseconds(), Equals, int64(1000))
	k := &blockTimeChangeKeeper{
		TestSlashingLackKeeper: &TestSlashingLackKeeper{},
		history:                constants.BlockTimeHistory{{Height: 100, Ms: 1500}, {Height: 400, Ms: 6000}},
	}
	slasher := newSlasher(k, NewDummyEventMgr())
	voter := ObservedTxVoter{UnfinalizedHeight: 100}
	// more than 900 reference blocks of time: 299 blocks of 1500ms (448.5)
	// then 76 of 6000ms (904.5); 75 of them make only 898.5
	stale := func(now int64) staleOutboundObservationStatus {
		return slasher.staleNonFinalOutboundObservation(ctx.WithBlockHeight(now), voter, period, 0)
	}
	c.Check(stale(474).stale, Equals, false)
	c.Check(stale(475).stale, Equals, true)
	c.Check(stale(475).staleSinceHeight, Equals, int64(475))
	c.Check(stale(475).firstStaleWindow, Equals, true)
	// until a period later at 6000ms: 49 more blocks make 1198.5
	c.Check(stale(524).firstStaleWindow, Equals, true)
	c.Check(stale(525).firstStaleWindow, Equals, false)
}
