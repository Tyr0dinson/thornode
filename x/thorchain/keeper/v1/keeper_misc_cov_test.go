package keeperv1

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/hashicorp/go-metrics"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// corruptKeyCov writes bytes under key that no protobuf message can decode,
// so that getters exercise their unmarshal error branches.
func corruptKeyCov(ctx cosmos.Context, k KVStore, key []byte) {
	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	store.Set(key, []byte{0xff, 0xff, 0xff})
}

type KeeperMiscCovSuite struct{}

var _ = Suite(&KeeperMiscCovSuite{})

func xmrRefCov(spend common.TxID, n string) common.OutputRef {
	return common.OutputRef{
		TxHash:      "TXHASH" + n,
		OutputIndex: 1,
		KeyImage:    "KEYIMAGE" + n,
		SpendTxHash: spend.String(),
	}
}

func (s *KeeperMiscCovSuite) TestXMRSpentOutputRefs(c *C) {
	ctx, k := setupKeeperForTest(c)
	vault := GetRandomPubKey()
	spend := GetRandomTxHash()

	// empty vault or refs are a no-op
	c.Assert(k.SetXMRSpentOutputRefs(ctx, common.EmptyPubKey, spend, []common.OutputRef{xmrRefCov(spend, "1")}, 1, 1), IsNil)
	c.Assert(k.SetXMRSpentOutputRefs(ctx, vault, spend, nil, 1, 1), IsNil)
	refs, err := k.GetXMRSpentOutputRefs(ctx, vault)
	c.Assert(err, IsNil)
	c.Assert(refs, HasLen, 0)
	refs, err = k.GetXMRSpentOutputRefs(ctx, common.EmptyPubKey)
	c.Assert(err, IsNil)
	c.Assert(refs, IsNil)

	// invalid refs are rejected
	bad := xmrRefCov(spend, "1")
	bad.KeyImage = ""
	c.Assert(k.SetXMRSpentOutputRefs(ctx, vault, spend, []common.OutputRef{bad}, 1, 1), NotNil)
	mismatch := xmrRefCov(GetRandomTxHash(), "1")
	c.Assert(k.SetXMRSpentOutputRefs(ctx, vault, spend, []common.OutputRef{mismatch}, 1, 1), NotNil)

	// heights default to the block height
	r1, r2 := xmrRefCov(spend, "2"), xmrRefCov(spend, "1")
	c.Assert(k.SetXMRSpentOutputRefs(ctx, vault, spend, []common.OutputRef{r1, r2}, 0, 0), IsNil)
	// the same ref stored at another height is de-duplicated on read
	c.Assert(k.SetXMRSpentOutputRefs(ctx, vault, spend, []common.OutputRef{r1}, 20, 25), IsNil)
	refs, err = k.GetXMRSpentOutputRefs(ctx, vault)
	c.Assert(err, IsNil)
	c.Assert(refs, HasLen, 2)
	// sorted by identity
	c.Check(common.SpentOutputRefIdentity(refs[0]) < common.SpentOutputRefIdentity(refs[1]), Equals, true)

	// another vault is isolated
	refs, err = k.GetXMRSpentOutputRefs(ctx, GetRandomPubKey())
	c.Assert(err, IsNil)
	c.Assert(refs, HasLen, 0)

	// a write far in the future prunes refs older than the retention window
	spend2 := GetRandomTxHash()
	r3 := xmrRefCov(spend2, "3")
	future := xmrSpentOutputRefRetentionBlocks + 100
	c.Assert(k.SetXMRSpentOutputRefs(ctx, vault, spend2, []common.OutputRef{r3}, future, future), IsNil)
	refs, err = k.GetXMRSpentOutputRefs(ctx, vault)
	c.Assert(err, IsNil)
	c.Assert(refs, HasLen, 1)
	c.Check(refs[0].KeyImage, Equals, "KEYIMAGE3")

	// corrupt value surfaces an error
	corruptKeyCov(ctx, k, xmrSpentOutputRefKey(vault, 1, 1, r1))
	_, err = k.GetXMRSpentOutputRefs(ctx, vault)
	c.Assert(err, NotNil)
}

func (s *KeeperMiscCovSuite) TestXMRSpentOutputRefKeyHelpers(c *C) {
	vault := GetRandomPubKey()
	ref := xmrRefCov(GetRandomTxHash(), "1")
	prefix := xmrSpentOutputRefVaultPrefix(vault)
	c.Check(strings.HasSuffix(string(prefix), "/"), Equals, true)
	c.Check(strings.Contains(string(prefix), strings.ToUpper(vault.String())), Equals, true)

	// negative heights clamp to zero
	key := xmrSpentOutputRefKey(vault, -5, -7, ref)
	height, ok := xmrSpentOutputRefHeightFromKey(prefix, key)
	c.Check(ok, Equals, true)
	c.Check(height, Equals, int64(0))

	key = xmrSpentOutputRefKey(vault, 3, 42, ref)
	height, ok = xmrSpentOutputRefHeightFromKey(prefix, key)
	c.Check(ok, Equals, true)
	c.Check(height, Equals, int64(42))

	// mismatched prefix, missing separator and non-numeric heights are rejected
	_, ok = xmrSpentOutputRefHeightFromKey([]byte("other/"), key)
	c.Check(ok, Equals, false)
	_, ok = xmrSpentOutputRefHeightFromKey(prefix, append(append([]byte{}, prefix...), []byte("/abc")...))
	c.Check(ok, Equals, false)
	_, ok = xmrSpentOutputRefHeightFromKey(prefix, append(append([]byte{}, prefix...), []byte("abc/def")...))
	c.Check(ok, Equals, false)
}

func (s *KeeperMiscCovSuite) TestSolVaultNonce(c *C) {
	ctx, k := setupKeeperForTest(c)
	vault := GetRandomPubKey()

	rec, found, err := k.GetSolVaultNonce(ctx, vault)
	c.Assert(err, IsNil)
	c.Check(found, Equals, false)
	c.Check(rec.Nonce, Equals, "")

	want := SolVaultNonce{VaultPubKey: vault, NonceAccount: "acct", Nonce: "nonceAbC", Slot: 9, UpdatedHeight: 18}
	k.SetSolVaultNonce(ctx, want)
	rec, found, err = k.GetSolVaultNonce(ctx, vault)
	c.Assert(err, IsNil)
	c.Check(found, Equals, true)
	c.Check(rec, DeepEquals, want)

	iter := k.GetSolVaultNonceIterator(ctx)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Assert(iter.Close(), IsNil)
	c.Check(count, Equals, 1)

	corruptKeyCov(ctx, k, k.GetKey(prefixSolVaultNonce, vault.String()))
	_, found, err = k.GetSolVaultNonce(ctx, vault)
	c.Assert(err, NotNil)
	c.Check(found, Equals, false)
}

func (s *KeeperMiscCovSuite) TestSolNonceAttestVoter(c *C) {
	ctx, k := setupKeeperForTest(c)
	vault := GetRandomPubKey()
	lastTx := GetRandomTxHash()

	// empty voter carries the requested value
	voter, err := k.GetSolNonceAttestVoter(ctx, vault, "acct", "nonce", lastTx, 5)
	c.Assert(err, IsNil)
	c.Check(voter.VaultPubKey, Equals, vault)
	c.Check(voter.Nonce, Equals, "nonce")
	c.Check(voter.Slot, Equals, int64(5))
	c.Check(voter.Signers, HasLen, 0)

	voter.Signers = []string{"a", "b"}
	k.SetSolNonceAttestVoter(ctx, voter)
	got, err := k.GetSolNonceAttestVoter(ctx, vault, "acct", "nonce", lastTx, 5)
	c.Assert(err, IsNil)
	c.Check(got.Signers, DeepEquals, []string{"a", "b"})

	// a different slot is a different vote
	got, err = k.GetSolNonceAttestVoter(ctx, vault, "acct", "nonce", lastTx, 6)
	c.Assert(err, IsNil)
	c.Check(got.Signers, HasLen, 0)

	// nonce values are case sensitive
	c.Check(solNonceAttestVoterKey(vault, "acct", "nonce", lastTx, 5) ==
		solNonceAttestVoterKey(vault, "acct", "NONCE", lastTx, 5), Equals, false)

	corruptKeyCov(ctx, k, k.GetKey(prefixSolNonceAttestVoter, solNonceAttestVoterKey(vault, "acct", "nonce", lastTx, 5)))
	_, err = k.GetSolNonceAttestVoter(ctx, vault, "acct", "nonce", lastTx, 5)
	c.Assert(err, NotNil)
}

func (s *KeeperMiscCovSuite) TestAssertAdvSwapQueueIndexUnchanged(c *C) {
	if notMocknetCov() {
		c.Skip("mocknet only: the index assertion only exists in the mocknet build")
	}
	ctx, k := setupKeeperForTest(c)
	msg := MsgSwap{
		Tx:          GetRandomTx(),
		TargetAsset: common.BTCAsset,
		SwapType:    types.SwapType_market,
	}
	msg.Tx.Coins = common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.NewUint(100)))
	c.Check(func() { k.assertAdvSwapQueueIndexUnchanged(ctx, msg) }, PanicMatches, ".*not indexed under its current key.*")

	c.Assert(k.SetAdvSwapQueueIndex(ctx, msg), IsNil)
	k.assertAdvSwapQueueIndexUnchanged(ctx, msg)

	// an unreadable index also panics
	corruptKeyCov(ctx, k, k.GetKey(prefixAdvSwapQueueMarketIndex, ""))
	c.Check(func() { k.assertAdvSwapQueueIndexUnchanged(ctx, msg) }, PanicMatches, ".*fail to read index.*")
}

func (s *KeeperMiscCovSuite) TestVersionAndMinJoinLast(c *C) {
	ctx, k := setupKeeperForTest(c)

	_, found := k.GetVersionWithCtx(ctx)
	c.Check(found, Equals, false)
	k.SetVersionWithCtx(ctx, semver.MustParse("3.1.2"))
	v, found := k.GetVersionWithCtx(ctx)
	c.Check(found, Equals, true)
	c.Check(v.String(), Equals, "3.1.2")

	// unset: falls back to the computed min join version and height 0
	ver, height := k.GetMinJoinLast(ctx)
	c.Check(ver.Equals(k.GetMinJoinVersion(ctx)), Equals, true)
	c.Check(height, Equals, int64(0))

	// with active validators the min join version is stored with its height
	for i := 0; i < 3; i++ {
		na := GetRandomValidatorNode(NodeActive)
		na.Version = "1.2.3"
		c.Assert(k.SetNodeAccount(ctx, na), IsNil)
	}
	k.SetMinJoinLast(ctx)
	ver, height = k.GetMinJoinLast(ctx)
	c.Check(ver.String(), Equals, "1.2.3")
	c.Check(height, Equals, int64(18))

	// unchanged version at a later height keeps the original height
	later := ctx.WithBlockHeight(30)
	k.SetMinJoinLast(later)
	_, height = k.GetMinJoinLast(later)
	c.Check(height, Equals, int64(18))

	// corrupt stored record: falls back
	corruptKeyCov(ctx, k, k.GetKey(prefixMinJoinLast, ""))
	ver, height = k.GetMinJoinLast(ctx)
	c.Check(height, Equals, int64(0))
	c.Check(ver.String(), Equals, "1.2.3")

	// SetVersion/GetVersion on the keeper struct
	k.SetVersion(semver.MustParse("9.9.9"))
	c.Check(k.GetVersion().String(), Equals, "9.9.9")
}

func (s *KeeperMiscCovSuite) TestSlashTelemetry(c *C) {
	ctx, _ := setupKeeperForTest(c)

	// init lazily opens ${HOME}/.thornode/slash_telemetry.json; point HOME
	// at a temp dir so the test never writes into the real home directory.
	home := c.MkDir()
	c.Assert(mkdirAllCov(filepath.Join(home, ".thornode")), IsNil)
	restore := setEnvCov("HOME", home)
	initSlashTelemetry()
	restore()

	// capture encoded events in memory
	buf := &bytes.Buffer{}
	slashTelemetryMu.Lock()
	orig := slashTelemetryEnc
	slashTelemetryEnc = json.NewEncoder(buf)
	slashTelemetryMu.Unlock()
	defer func() {
		slashTelemetryMu.Lock()
		slashTelemetryEnc = orig
		slashTelemetryMu.Unlock()
	}()

	addr := GetRandomBech32Addr()
	goCtx := context.WithValue(ctx.Context(), constants.CtxMetricLabels, []metrics.Label{{Name: "handler", Value: "observe"}})
	goCtx = context.WithValue(goCtx, constants.CtxObservedTx, "OBSTX")
	slashTelemetry(ctx.WithContext(goCtx).WithTxBytes([]byte("tx")), 7, addr, "IncSlashPoints")

	var event map[string]any
	c.Assert(json.Unmarshal(buf.Bytes(), &event), IsNil)
	c.Check(event["points"], Equals, float64(7))
	c.Check(event["height"], Equals, float64(18))
	c.Check(event["address"], Equals, addr.String())
	c.Check(event["handler"], Equals, "observe")
	c.Check(event["observed_txid"], Equals, "OBSTX")
	c.Check(event["txid"], HasLen, 64)

	// a function not on the stack yields no caller
	c.Check(getSlashCaller("NoSuchFunctionAnywhere"), Equals, "")
	c.Check(getSlashCallerFromStack([]byte("a\nb\n"), "Other"), Equals, "")
}

func (s *KeeperMiscCovSuite) TestPOLReserveDeposit(c *C) {
	ctx, k := setupKeeperForTest(c)

	rec, err := k.GetPOLReserveDeposit(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(rec.Asset.Equals(common.BTCAsset), Equals, true)
	c.Check(rec.RuneDeposited.IsZero(), Equals, true)

	rec.RuneDeposited = cosmos.NewUint(500)
	c.Assert(k.SetPOLReserveDeposit(ctx, rec), IsNil)
	got, err := k.GetPOLReserveDeposit(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(got.RuneDeposited.Uint64(), Equals, uint64(500))

	iter := k.GetPOLReserveDepositIterator(ctx)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Assert(iter.Close(), IsNil)
	c.Check(count, Equals, 1)

	corruptKeyCov(ctx, k, k.GetKey(prefixPOLReserve, common.ETHAsset.String()))
	_, err = k.GetPOLReserveDeposit(ctx, common.ETHAsset)
	c.Assert(err, NotNil)
}

func (s *KeeperMiscCovSuite) TestNodeMimirs(c *C) {
	ctx, k := setupKeeperForTest(c)
	acc1, acc2 := GetRandomBech32Addr(), GetRandomBech32Addr()

	nm, err := k.GetNodeMimirs(ctx, "HaltBTCChain")
	c.Assert(err, IsNil)
	c.Check(nm.Mimirs, HasLen, 0)

	c.Assert(k.SetNodeMimir(ctx, "HaltBTCChain", 1, acc1), IsNil)
	c.Assert(k.SetNodeMimir(ctx, "haltbtcchain", 1, acc2), IsNil)
	c.Assert(k.SetNodeMimir(ctx, "Foo", 3, acc1), IsNil)
	nm, err = k.GetNodeMimirs(ctx, "HALTBTCCHAIN")
	c.Assert(err, IsNil)
	c.Check(nm.Mimirs, HasLen, 2)

	// negative value deletes the vote
	c.Assert(k.SetNodeMimir(ctx, "HaltBTCChain", -1, acc2), IsNil)
	nm, err = k.GetNodeMimirs(ctx, "HaltBTCChain")
	c.Assert(err, IsNil)
	c.Check(nm.Mimirs, HasLen, 1)
	// deleting the last vote removes the key entirely
	c.Assert(k.SetNodeMimir(ctx, "HaltBTCChain", -1, acc1), IsNil)
	c.Check(k.has(ctx, k.GetKey(prefixNodeMimir, "HALTBTCCHAIN")), Equals, false)

	// purge only removes operational node mimirs
	c.Assert(k.SetNodeMimir(ctx, "HaltETHChain", 1, acc1), IsNil)
	k.PurgeOperationalNodeMimirs(ctx)
	nm, err = k.GetNodeMimirs(ctx, "HaltETHChain")
	c.Assert(err, IsNil)
	c.Check(nm.Mimirs, HasLen, 0)
	nm, err = k.GetNodeMimirs(ctx, "Foo")
	c.Assert(err, IsNil)
	c.Check(nm.Mimirs, HasLen, 1)

	k.DeleteNodeMimirs(ctx, "FOO")
	nm, err = k.GetNodeMimirs(ctx, "Foo")
	c.Assert(err, IsNil)
	c.Check(nm.Mimirs, HasLen, 0)

	// mimir iterator sees set mimirs
	k.SetMimir(ctx, "CovMimir", 4)
	iter := k.GetMimirIterator(ctx)
	found := false
	for ; iter.Valid(); iter.Next() {
		if strings.HasSuffix(string(iter.Key()), "COVMIMIR") {
			found = true
		}
	}
	c.Assert(iter.Close(), IsNil)
	c.Check(found, Equals, true)

	// corrupt node mimir
	corruptKeyCov(ctx, k, k.GetKey(prefixNodeMimir, "BAD"))
	_, err = k.GetNodeMimirs(ctx, "bad")
	c.Assert(err, NotNil)
	c.Assert(k.SetNodeMimir(ctx, "bad", 1, acc1), NotNil)
}

func (s *KeeperMiscCovSuite) TestNodePauseChain(c *C) {
	ctx, k := setupKeeperForTest(c)
	acc := GetRandomBech32Addr()
	c.Check(k.GetNodePauseChain(ctx, acc), Equals, int64(-1))
	k.SetNodePauseChain(ctx.WithBlockHeight(77), acc)
	c.Check(k.GetNodePauseChain(ctx, acc), Equals, int64(77))
}

func (s *KeeperMiscCovSuite) TestGetMimirEnvFallback(c *C) {
	if notMocknetCov() {
		c.Skip("mocknet only: the mimir env fallback only exists in keeper_mimir_mocknet.go")
	}
	ctx, k := setupKeeperForTest(c)

	restore := setEnvCov("COV_ENV_MIMIR", "42")
	v, err := k.GetMimir(ctx, "CovEnvMimir")
	restore()
	c.Assert(err, IsNil)
	c.Check(v, Equals, int64(42))

	restore = setEnvCov("COV_ENV_MIMIR", "notanumber")
	_, err = k.GetMimir(ctx, "CovEnvMimir")
	restore()
	c.Assert(err, ErrorMatches, ".*invalid mimir value for COV_ENV_MIMIR.*")

	// stored values take priority over the env
	k.SetMimir(ctx, "CovEnvMimir", 5)
	restore = setEnvCov("COV_ENV_MIMIR", "42")
	v, err = k.GetMimir(ctx, "CovEnvMimir")
	restore()
	c.Assert(err, IsNil)
	c.Check(v, Equals, int64(5))
}

func (s *KeeperMiscCovSuite) TestTradeUnits(c *C) {
	ctx, k := setupKeeperForTest(c)
	asset := common.BTCAsset.GetTradeAsset()

	tu, err := k.GetTradeUnit(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(tu.Units.IsZero(), Equals, true)

	tu.Units = cosmos.NewUint(10)
	tu.Depth = cosmos.NewUint(20)
	k.SetTradeUnit(ctx, tu)
	got, err := k.GetTradeUnit(ctx, asset)
	c.Assert(err, IsNil)
	c.Check(got.Units.Uint64(), Equals, uint64(10))
	c.Check(got.Depth.Uint64(), Equals, uint64(20))

	iter := k.GetTradeUnitIterator(ctx)
	count := 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Assert(iter.Close(), IsNil)
	c.Check(count, Equals, 1)

	iter = k.GetTradeUnitIteratorWithAddress(ctx, GetRandomBech32Addr())
	c.Check(iter.Valid(), Equals, false)
	c.Assert(iter.Close(), IsNil)

	// trade accounts iterator
	addr := GetRandomBech32Addr()
	ta := NewTradeAccount(addr, asset)
	ta.Units = cosmos.NewUint(3)
	k.SetTradeAccount(ctx, ta)
	iter = k.GetTradeAccountIterator(ctx)
	count = 0
	for ; iter.Valid(); iter.Next() {
		count++
	}
	c.Assert(iter.Close(), IsNil)
	c.Check(count, Equals, 1)

	corruptKeyCov(ctx, k, k.GetKey(prefixTradeUnit, NewTradeUnit(common.ETHAsset.GetTradeAsset()).Key()))
	_, err = k.GetTradeUnit(ctx, common.ETHAsset.GetTradeAsset())
	c.Assert(err, NotNil)
	corruptKeyCov(ctx, k, k.GetKey(prefixTradeAccount, NewTradeAccount(addr, common.ETHAsset.GetTradeAsset()).Key()))
	_, err = k.GetTradeAccount(ctx, addr, common.ETHAsset.GetTradeAsset())
	c.Assert(err, NotNil)
}

func (s *KeeperMiscCovSuite) TestChainContracts(c *C) {
	ctx, k := setupKeeperForTest(c)
	cc := NewChainContract(common.ETHChain, "0xrouter")
	k.SetChainContract(ctx, cc)

	contracts := k.GetChainContracts(ctx, common.Chains{common.ETHChain, common.BTCChain})
	// BTC has no contract and is skipped
	c.Assert(contracts, HasLen, 1)
	c.Check(contracts[0].Router.String(), Equals, "0xrouter")

	// corrupt AVAX: error is logged and the chain skipped
	corruptKeyCov(ctx, k, k.GetKey(prefixChainContract, common.AVAXChain.String()))
	_, err := k.GetChainContract(ctx, common.AVAXChain)
	c.Assert(err, NotNil)
	contracts = k.GetChainContracts(ctx, common.Chains{common.AVAXChain, common.ETHChain})
	c.Assert(contracts, HasLen, 1)
	c.Check(contracts[0].Chain, Equals, common.ETHChain)
}

func (s *KeeperMiscCovSuite) TestRagnarok(c *C) {
	ctx, k := setupKeeperForTest(c)

	pos, err := k.GetRagnarokWithdrawPosition(ctx)
	c.Assert(err, IsNil)
	c.Check(pos.Number, Equals, int64(0))
	k.SetRagnarokWithdrawPosition(ctx, RagnarokWithdrawPosition{Number: 3, Pool: common.BTCAsset})
	pos, err = k.GetRagnarokWithdrawPosition(ctx)
	c.Assert(err, IsNil)
	c.Check(pos.Number, Equals, int64(3))
	c.Check(pos.Pool.Equals(common.BTCAsset), Equals, true)

	k.SetPoolRagnarokStart(ctx.WithBlockHeight(55), common.BTCAsset)
	h, err := k.GetPoolRagnarokStart(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(h, Equals, int64(55))
	k.DeletePoolRagnarokStart(ctx, common.BTCAsset)
	h, err = k.GetPoolRagnarokStart(ctx, common.BTCAsset)
	c.Assert(err, IsNil)
	c.Check(h, Equals, int64(0))

	c.Check(k.RagnarokInProgress(ctx), Equals, false)
	k.SetRagnarokBlockHeight(ctx, 10)
	c.Check(k.RagnarokInProgress(ctx), Equals, true)

	// corrupt height is treated as in progress (fail safe)
	corruptKeyCov(ctx, k, k.GetKey(prefixRagnarokHeight, ""))
	c.Check(k.RagnarokInProgress(ctx), Equals, true)

	corruptKeyCov(ctx, k, k.GetKey(prefixRagnarokPosition, ""))
	_, err = k.GetRagnarokWithdrawPosition(ctx)
	c.Assert(err, NotNil)

	// IsRagnarok checks the gas asset of a token's chain too
	c.Check(k.IsRagnarok(ctx, []common.Asset{common.ETHAsset}), Equals, false)
	k.SetMimir(ctx, "RAGNAROK-ETH-ETH", 1)
	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	c.Check(k.IsRagnarok(ctx, []common.Asset{usdc}), Equals, true)
}

func (s *KeeperMiscCovSuite) TestAnchors(c *C) {
	ctx, k := setupKeeperForTest(c)

	// non TOR assets anchor to their layer1 asset
	anchors := k.GetAnchors(ctx, common.BTCAsset.GetSyntheticAsset())
	c.Assert(anchors, HasLen, 1)
	c.Check(anchors[0].Equals(common.BTCAsset), Equals, true)

	usdc, err := common.NewAsset("ETH.USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48")
	c.Assert(err, IsNil)
	usdt, err := common.NewAsset("ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7")
	c.Assert(err, IsNil)
	for _, a := range []common.Asset{usdc, usdt, common.BTCAsset} {
		pool := NewPool()
		pool.Asset = a
		pool.Status = types.PoolStatus_Available
		pool.BalanceRune = cosmos.NewUint(200 * common.One)
		pool.BalanceAsset = cosmos.NewUint(100 * common.One)
		if a.Equals(usdt) {
			pool.BalanceRune = cosmos.NewUint(100 * common.One)
		}
		c.Assert(k.SetPool(ctx, pool), IsNil)
	}
	c.Check(k.GetAnchors(ctx, common.TOR), HasLen, 0)
	k.SetMimir(ctx, "TorAnchor-ETH-USDC-0XA0B86991C6218B36C1D19D4A2E9EB0CE3606EB48", 1)
	k.SetMimir(ctx, "TorAnchor-ETH-USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7", 1)
	anchors = k.GetAnchors(ctx, common.TOR)
	c.Assert(anchors, HasLen, 2)

	// 1 rune = 0.5 USD in one pool and 1 USD in the other: median 0.75
	price := k.DollarsPerRune(ctx)
	c.Check(price.Uint64(), Equals, uint64(75_000_000))
	c.Check(k.RunePerDollar(ctx).Uint64(), Equals, uint64(133_333_333))

	// halting ETH trading drops both anchors
	k.SetMimir(ctx, "HaltETHTrading", 1)
	c.Check(k.AnchorMedian(ctx, anchors).IsZero(), Equals, true)
	k.SetMimir(ctx, "HaltETHTrading", 0)

	// unavailable pools are ignored
	pool, err := k.GetPool(ctx, usdt)
	c.Assert(err, IsNil)
	pool.Status = types.PoolStatus_Staged
	c.Assert(k.SetPool(ctx, pool), IsNil)
	c.Check(k.AnchorMedian(ctx, anchors).Uint64(), Equals, uint64(50_000_000*constants.DollarMulti))

	// mimir override wins
	k.SetMimir(ctx, "DollarsPerRune", 123)
	c.Check(k.DollarsPerRune(ctx).Uint64(), Equals, uint64(123))

	// a corrupt pool is skipped
	corruptKeyCov(ctx, k, k.GetKey(prefixPool, usdc.String()))
	c.Check(k.AnchorMedian(ctx, []common.Asset{usdc}).IsZero(), Equals, true)
}
