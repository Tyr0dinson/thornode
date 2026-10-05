package thorchain

// Mainnet-weighted performance benchmarks.
//
// The fixture mirrors mainnet's state shape (test/performance/testdata/mainnet-shape.json:
// pools, mimir, node and vault counts) and the benchmarks mirror the mainnet message mix
// (test/performance/mainnet-mix.json): MsgObservedTxQuorum dominates tx gas, followed by
// MsgTssKeysignFail and MsgPriceFeedQuorumBatch, with streaming swaps driving EndBlock.
//
// Every iteration runs on a fresh cache context branched from the same seeded state,
// so each op does identical work. Each benchmark reports gas/op: the infinite gas meter
// still counts store access, and gas_used feeds LastResultsHash, so a gas/op delta means
// the change is consensus-visible and needs a coordinated upgrade.
//
// Run via scripts/perf/bench.sh (or make perf-bench).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strings"
	"testing"
	"time"

	storetypes "cosmossdk.io/store/types"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	sdked25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ---------------------------------------------------------------------------
// mainnet shape
// ---------------------------------------------------------------------------

type perfShape struct {
	Pools []struct {
		Asset        string `json:"asset"`
		Status       string `json:"status"`
		BalanceRune  string `json:"balance_rune"`
		BalanceAsset string `json:"balance_asset"`
		PoolUnits    string `json:"pool_units"`
		LPUnits      string `json:"LP_units"`
		SynthUnits   string `json:"synth_units"`
	} `json:"pools"`
	NodesByStatus map[string]int `json:"nodes_by_status"`
	AsgardVaults  []struct {
		Members int `json:"members"`
	} `json:"asgard_vaults"`
	Mimir map[string]int64 `json:"mimir"`
}

func loadPerfShape(b testing.TB) perfShape {
	b.Helper()
	path := os.Getenv("PERF_SHAPE")
	if path == "" {
		path = filepath.Join("..", "..", "test", "performance", "testdata", "mainnet-shape.json")
	}
	bz, err := os.ReadFile(path)
	if err != nil {
		b.Fatalf("read %s: %v (run make perf-mix)", path, err)
	}
	var s perfShape
	if err = json.Unmarshal(bz, &s); err != nil {
		b.Fatal(err)
	}
	return s
}

// mimir keys that would halt or pause the paths we want to measure, or that encode
// mainnet heights/pubkeys which mean nothing in the fixture.
var perfSkipMimirPrefixes = []string{
	"HALT", "PAUSE", "SOLVENCYHALT", "NODEPAUSE", "COMPROMISEDVAULT", "SCHEDULEDMIGRATION",
	"STOPSOLVENCYCHECK", "RAGNAROK",
}

func uintOr(s string, def uint64) cosmos.Uint {
	if s == "" {
		return cosmos.NewUint(def)
	}
	return cosmos.NewUintFromString(s)
}

// ---------------------------------------------------------------------------
// deterministic identities: the fixture must be byte-identical across runs so
// gas/op (and the work done) only changes when the code does
// ---------------------------------------------------------------------------

func perfPriv(label string) secp256k1.PrivKey {
	return secp256k1.GenPrivKeySecp256k1([]byte("thornode-perf/" + label))
}

func perfPubKey(b testing.TB, label string) common.PubKey {
	b.Helper()
	pk, err := common.NewPubKeyFromCrypto(perfPriv(label).PubKey())
	if err != nil {
		b.Fatal(err)
	}
	return pk
}

func perfConsPubKey(b testing.TB, label string) string {
	b.Helper()
	priv := ed25519.GenPrivKeyFromSecret([]byte("thornode-perf-cons/" + label))
	pk, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeConsPub, &sdked25519.PubKey{Key: priv.PubKey().Bytes()})
	if err != nil {
		b.Fatal(err)
	}
	return pk
}

func perfAddr(b testing.TB, chain common.Chain, label string) common.Address {
	b.Helper()
	addr, err := perfPubKey(b, label).GetAddress(chain)
	if err != nil {
		b.Fatal(err)
	}
	return addr
}

func perfAccAddr(label string) cosmos.AccAddress {
	return cosmos.AccAddress(perfPriv(label).PubKey().Address())
}

func perfTxID(b testing.TB, label string) common.TxID {
	b.Helper()
	sum := sha256.Sum256([]byte("thornode-perf-tx/" + label))
	id, err := common.NewTxID(strings.ToUpper(hex.EncodeToString(sum[:])))
	if err != nil {
		b.Fatal(err)
	}
	return id
}

func perfNodeAccount(b testing.TB, label string, status NodeStatus) (NodeAccount, secp256k1.PrivKey) {
	b.Helper()
	priv := perfPriv(label)
	pk := perfPubKey(b, label)
	na := NewNodeAccount(
		cosmos.AccAddress(priv.PubKey().Address()),
		status,
		common.PubKeySet{Secp256k1: pk, Ed25519: pk},
		perfConsPubKey(b, label),
		cosmos.NewUint(common.One*500_000),
		perfAddr(b, common.THORChain, label+"/bond"),
		1,
	)
	na.Version = constants.SWVersion.String()
	na.IPAddress = "192.168.0.1"
	if status == NodeActive {
		na.ActiveBlockHeight = 10
	}
	return na, priv
}

// ---------------------------------------------------------------------------
// fixture
// ---------------------------------------------------------------------------

type perfNode struct {
	na   NodeAccount
	priv secp256k1.PrivKey
}

type perfFixture struct {
	ctx    cosmos.Context
	mgr    *Mgrs
	nodes  []perfNode // active nodes with signing keys
	vaults []Vault
}

// newPerfFixture seeds a manager with mainnet-shaped state.
func newPerfFixture(b testing.TB) *perfFixture {
	b.Helper()
	ctx, mgr := setupBenchmarkManager(b)
	ctx = ctx.WithBlockTime(time.Unix(1_700_000_000, 0))
	k := mgr.Keeper()
	shape := loadPerfShape(b)
	f := &perfFixture{ctx: ctx, mgr: mgr}

	// mimir
	keys := make([]string, 0, len(shape.Mimir))
	for key := range shape.Mimir {
		keys = append(keys, key)
	}
	sort.Strings(keys)
outer:
	for _, key := range keys {
		for _, p := range perfSkipMimirPrefixes {
			if strings.HasPrefix(key, p) {
				continue outer
			}
		}
		k.SetMimir(ctx, key, shape.Mimir[key])
	}

	// pools + network fees per chain
	chains := map[common.Chain]bool{}
	for _, p := range shape.Pools {
		asset, err := common.NewAsset(p.Asset)
		if err != nil {
			continue
		}
		status := PoolAvailable
		if p.Status != "Available" {
			status = PoolStaged
		}
		pool := NewPool()
		pool.Asset = asset
		pool.Status = status
		pool.BalanceRune = uintOr(p.BalanceRune, 1)
		pool.BalanceAsset = uintOr(p.BalanceAsset, 1)
		pool.LPUnits = uintOr(p.LPUnits, 1)
		pool.SynthUnits = uintOr(p.SynthUnits, 0)
		pool.CalcUnits(cosmos.ZeroUint())
		if err = k.SetPool(ctx, pool); err != nil {
			b.Fatal(err)
		}
		if !asset.Chain.IsTHORChain() {
			chains[asset.Chain] = true
		}
	}
	chainList := make([]string, 0, len(chains))
	for c := range chains {
		chainList = append(chainList, c.String())
	}
	sort.Strings(chainList)
	for _, c := range chainList {
		chain := common.Chain(c)
		if err := k.SaveNetworkFee(ctx, chain, NetworkFee{
			Chain:              chain,
			TransactionSize:    250,
			TransactionFeeRate: 10,
		}); err != nil {
			b.Fatal(err)
		}
	}

	// nodes
	active := shape.NodesByStatus["Active"]
	if active == 0 {
		active = 100
	}
	for i := 0; i < active; i++ {
		na, priv := perfNodeAccount(b, fmt.Sprintf("active-%d", i), NodeActive)
		if err := k.SetNodeAccount(ctx, na); err != nil {
			b.Fatal(err)
		}
		f.nodes = append(f.nodes, perfNode{na: na, priv: priv})
	}
	for _, st := range []NodeStatus{NodeStandby, NodeDisabled} {
		for i := 0; i < shape.NodesByStatus[st.String()]; i++ {
			na, _ := perfNodeAccount(b, fmt.Sprintf("%s-%d", st, i), st)
			if err := k.SetNodeAccount(ctx, na); err != nil {
				b.Fatal(err)
			}
		}
	}

	// asgard vaults: active nodes split across vaults like mainnet
	nVaults := len(shape.AsgardVaults)
	if nVaults == 0 {
		nVaults = 1
	}
	for v := 0; v < nVaults; v++ {
		vault := NewVault(1, ActiveVault, AsgardVault, perfPubKey(b, fmt.Sprintf("vault-%d", v)), chainList, nil)
		for i := v; i < len(f.nodes); i += nVaults {
			vault.Membership = append(vault.Membership, f.nodes[i].na.PubKeySet.Secp256k1.String())
		}
		for _, p := range shape.Pools {
			asset, err := common.NewAsset(p.Asset)
			if err != nil || asset.Chain.IsTHORChain() {
				continue
			}
			vault.AddFunds(common.Coins{common.NewCoin(asset, uintOr(p.BalanceAsset, 1))})
		}
		if err := k.SetVault(ctx, vault); err != nil {
			b.Fatal(err)
		}
		f.vaults = append(f.vaults, vault)
	}
	return f
}

// branch returns a throwaway context on top of the fixture state.
func (f *perfFixture) branch(height int64) cosmos.Context {
	cctx, _ := f.ctx.CacheContext()
	return cctx.
		WithBlockHeight(height).
		WithEventManager(sdk.NewEventManager()).
		WithGasMeter(storetypes.NewInfiniteGasMeter())
}

// commit writes a branch back into the fixture state.
func (f *perfFixture) commitInto(fn func(ctx cosmos.Context)) {
	cctx, write := f.ctx.CacheContext()
	cctx = cctx.WithEventManager(sdk.NewEventManager())
	fn(cctx)
	write()
}

func (f *perfFixture) vaultAddress(b testing.TB, v int, chain common.Chain) common.Address {
	b.Helper()
	addr, err := f.vaults[v].PubKey.GetAddress(chain)
	if err != nil {
		b.Fatal(err)
	}
	return addr
}

func (f *perfFixture) quorumMsg(b testing.TB, obsTx common.ObservedTx, inbound bool, signers []perfNode) *types.MsgObservedTxQuorum {
	b.Helper()
	data, err := obsTx.GetSignablePayloadWithInbound(inbound)
	if err != nil {
		b.Fatal(err)
	}
	quo := &common.QuorumTx{ObsTx: obsTx, Inbound: inbound}
	for _, n := range signers {
		sig, err := n.priv.Sign(data)
		if err != nil {
			b.Fatal(err)
		}
		quo.Attestations = append(quo.Attestations, &common.Attestation{PubKey: n.priv.PubKey().Bytes(), Signature: sig})
	}
	return types.NewMsgObservedTxQuorum(quo, signers[0].na.NodeAddress)
}

// inboundSwap builds an observed ETH inbound carrying a streaming swap memo to BTC,
// the dominant inbound memo class on mainnet.
func (f *perfFixture) inboundSwap(b testing.TB, vault int, height int64, label string) common.ObservedTx {
	b.Helper()
	return common.NewObservedTx(
		common.Tx{
			ID:          perfTxID(b, "in/"+label),
			Chain:       common.ETHChain,
			FromAddress: perfAddr(b, common.ETHChain, "user/"+label),
			ToAddress:   f.vaultAddress(b, vault, common.ETHChain),
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(5*common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "=:BTC.BTC:" + perfAddr(b, common.BTCChain, "dest/"+label).String() + ":0/1/0",
		},
		height, f.vaults[vault].PubKey, height,
	)
}

// perfLoop runs fn b.N times under the perf=measured pprof label.
func perfLoop(b *testing.B, fn func(i int)) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	pprof.Do(context.Background(), pprof.Labels("perf", "measured"), func(context.Context) {
		for i := 0; i < b.N; i++ {
			fn(i)
		}
	})
}

// measure runs op b.N times, each on a fresh branch of the fixture state at the
// given height, and reports gas/op. The loop runs under the pprof label
// perf=measured so scripts/perf/profile.sh can drop fixture setup from profiles.
func (f *perfFixture) measure(b *testing.B, height int64, op func(ctx cosmos.Context) error) {
	b.Helper()
	run := func() uint64 {
		ctx := f.branch(height)
		// managers keep per-block state in memory (gas events, observers);
		// start every op from an empty block like BeginBlock would
		f.mgr.gasMgr.BeginBlock()
		f.mgr.obMgr = newObserverMgr()
		if err := op(ctx); err != nil {
			b.Fatal(err)
		}
		return ctx.GasMeter().GasConsumed()
	}
	// one untimed op warms lazily initialised state so every measured op,
	// and so gas/op, is identical regardless of b.N
	run()
	var gas uint64
	b.ReportAllocs()
	b.ResetTimer()
	pprof.Do(context.Background(), pprof.Labels("perf", "measured"), func(context.Context) {
		for i := 0; i < b.N; i++ {
			gas += run()
		}
	})
	b.ReportMetric(float64(gas)/float64(b.N), "gas/op")
}

// ---------------------------------------------------------------------------
// MsgObservedTxQuorum (~68% of mainnet tx gas)
// ---------------------------------------------------------------------------

func BenchmarkPerfObservedTxQuorumIn(b *testing.B) {
	f := newPerfFixture(b)
	height := f.ctx.BlockHeight() + 1
	quorum := len(f.nodes)*2/3 + 2

	cases := []struct {
		name      string
		pre       int // attestations already recorded before the measured msg
		attesting int
	}{
		{"fresh-1", 0, 1},
		{"batch-quorum", 0, quorum},
		{"batch-all", 0, len(f.nodes)},
		{"late-1-after-quorum", quorum, 1},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			obsTx := f.inboundSwap(b, 0, height, "quorum-in/"+tc.name)
			h := NewObservedTxQuorumHandler(f.mgr)
			fx := *f
			if tc.pre > 0 {
				fx.commitInto(func(ctx cosmos.Context) {
					if _, err := h.Run(ctx.WithBlockHeight(height), f.quorumMsg(b, obsTx, true, f.nodes[:tc.pre])); err != nil {
						b.Fatal(err)
					}
				})
			}
			msg := f.quorumMsg(b, obsTx, true, f.nodes[tc.pre:tc.pre+tc.attesting])
			if tc.pre+tc.attesting >= quorum {
				// sanity: quorum must finalise the inbound and queue the swap
				ctx := fx.branch(height)
				if _, err := h.Run(ctx, msg); err != nil {
					b.Fatal(err)
				}
				voter, err := f.mgr.Keeper().GetObservedTxInVoter(ctx, obsTx.Tx.ID)
				if err != nil || voter.FinalisedHeight == 0 {
					b.Fatalf("inbound did not reach consensus: %v", err)
				}
			}
			fx.measure(b, height, func(ctx cosmos.Context) error {
				_, err := h.Run(ctx, msg)
				return err
			})
		})
	}
}

// BenchmarkPerfObservedTxQuorumOut observes a real scheduled outbound: the fixture runs an
// inbound swap to quorum and through EndBlock, then attests the resulting BTC outbound.
func BenchmarkPerfObservedTxQuorumOut(b *testing.B) {
	f := newPerfFixture(b)
	height := f.ctx.BlockHeight() + 1
	quorum := len(f.nodes)*2/3 + 2
	h := NewObservedTxQuorumHandler(f.mgr)
	am := NewAppModule(f.mgr, false, true)

	// non-streaming swap so a single EndBlock schedules the outbound
	in := common.NewObservedTx(
		common.Tx{
			ID:          perfTxID(b, "quorum-out/in"),
			Chain:       common.ETHChain,
			FromAddress: perfAddr(b, common.ETHChain, "user/quorum-out"),
			ToAddress:   f.vaultAddress(b, 0, common.ETHChain),
			Coins:       common.NewCoins(common.NewCoin(common.ETHAsset, cosmos.NewUint(common.One))),
			Gas:         common.Gas{common.NewCoin(common.ETHAsset, cosmos.NewUint(10000))},
			Memo:        "=:BTC.BTC:" + perfAddr(b, common.BTCChain, "dest/quorum-out").String(),
		},
		height, f.vaults[0].PubKey, height,
	)
	var item TxOutItem
	f.commitInto(func(ctx cosmos.Context) {
		ctx = ctx.WithBlockHeight(height)
		if _, err := h.Run(ctx, f.quorumMsg(b, in, true, f.nodes[:quorum])); err != nil {
			b.Fatal(err)
		}
		if _, err := am.EndBlock(ctx); err != nil {
			b.Fatal(err)
		}
		// the outbound is scheduled a few blocks out (txout delay); the voter
		// records it as the inbound's action
		voter, err := f.mgr.Keeper().GetObservedTxInVoter(ctx, in.Tx.ID)
		if err != nil {
			b.Fatal(err)
		}
		if len(voter.Actions) == 0 {
			b.Fatal("fixture swap scheduled no outbound")
		}
		item = voter.Actions[0]
	})

	fromAddr, err := item.VaultPubKey.GetAddress(item.Chain)
	if err != nil {
		b.Fatal(err)
	}
	gasCoin := item.MaxGas
	if len(gasCoin) == 0 {
		gasCoin = common.Gas{common.NewCoin(common.BTCAsset, cosmos.NewUint(1000))}
	}
	out := common.NewObservedTx(
		common.Tx{
			ID:          perfTxID(b, "quorum-out/out"),
			Chain:       item.Chain,
			FromAddress: fromAddr,
			ToAddress:   item.ToAddress,
			Coins:       common.NewCoins(item.Coin),
			Gas:         gasCoin,
			Memo:        item.Memo,
		},
		height+3, item.VaultPubKey, height+3,
	)

	cases := []struct {
		name      string
		attesting int
	}{
		{"fresh-1", 1},
		{"batch-quorum", quorum},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			msg := f.quorumMsg(b, out, false, f.nodes[:tc.attesting])
			if tc.attesting >= quorum {
				// sanity: quorum must match the scheduled outbound, not slash the vault
				ctx := f.branch(height + 3)
				if _, err := h.Run(ctx, msg); err != nil {
					b.Fatal(err)
				}
				voter, err := f.mgr.Keeper().GetObservedTxInVoter(ctx, in.Tx.ID)
				if err != nil || len(voter.OutTxs) == 0 {
					b.Fatalf("outbound observation did not complete the inbound: %v", err)
				}
			}
			f.measure(b, height+3, func(ctx cosmos.Context) error {
				_, err := h.Run(ctx, msg)
				return err
			})
		})
	}
}

// ---------------------------------------------------------------------------
// MsgTssKeysignFail (~15% of mainnet tx gas)
// ---------------------------------------------------------------------------

func BenchmarkPerfTssKeysignFail(b *testing.B) {
	f := newPerfFixture(b)
	height := f.ctx.BlockHeight() + 1
	vault := f.vaults[0]
	var blame []types.Node
	for _, m := range vault.Membership[:3] {
		blame = append(blame, types.Node{Pubkey: m})
	}
	coins := common.Coins{common.NewCoin(common.BTCAsset, cosmos.NewUint(common.One))}

	var signer perfNode
	for _, n := range f.nodes {
		if vault.Contains(n.na.PubKeySet.Secp256k1) {
			signer = n
			break
		}
	}
	msg, err := NewMsgTssKeysignFail(height, types.Blame{FailReason: "fail to keysign", BlameNodes: blame}, "", coins, signer.na.NodeAddress, vault.PubKey)
	if err != nil {
		b.Fatal(err)
	}
	h := NewTssKeysignHandler(f.mgr)
	f.measure(b, height, func(ctx cosmos.Context) error {
		_, err := h.Run(ctx, msg)
		return err
	})
}

// ---------------------------------------------------------------------------
// MsgPriceFeedQuorumBatch (one per block, ~10% of mainnet tx gas)
// ---------------------------------------------------------------------------

func BenchmarkPerfPriceFeedQuorumBatch(b *testing.B) {
	f := newPerfFixture(b)
	height := f.ctx.BlockHeight() + 1
	symbols := strings.Split(f.mgr.GetConstants().GetStringValue(constants.RequiredPriceFeeds), ",")
	version := NewPriceFeedQuorumBatchHandler(f.mgr).requiredVersion

	msg := &types.MsgPriceFeedQuorumBatch{Signer: f.nodes[0].na.NodeAddress}
	for i, n := range f.nodes {
		rates := make([]*common.OraclePrice, len(symbols))
		for j := range symbols {
			price, err := common.NewOraclePrice(big.NewFloat(float64(1000*(j+1)) + float64(i%7)))
			if err != nil {
				b.Fatal(err)
			}
			rates[j] = price
		}
		pf := &common.PriceFeed{Version: version, Time: f.ctx.BlockTime().UnixMilli(), Rates: rates}
		data, err := pf.GetSignablePayload()
		if err != nil {
			b.Fatal(err)
		}
		sig, err := n.priv.Sign(data)
		if err != nil {
			b.Fatal(err)
		}
		msg.QuoPriceFeeds = append(msg.QuoPriceFeeds, &common.QuorumPriceFeed{
			PriceFeed:    pf,
			Attestations: []*common.Attestation{{PubKey: n.priv.PubKey().Bytes(), Signature: sig}},
		})
	}
	h := NewPriceFeedQuorumBatchHandler(f.mgr)
	f.measure(b, height, func(ctx cosmos.Context) error {
		_, err := h.Run(ctx, msg)
		return err
	})
}

// ---------------------------------------------------------------------------
// MsgDeposit streaming swap (most frequent deposit memo)
// ---------------------------------------------------------------------------

func BenchmarkPerfDepositStreamingSwap(b *testing.B) {
	f := newPerfFixture(b)
	height := f.ctx.BlockHeight() + 1
	signer := perfAccAddr("depositor")
	amt := cosmos.NewUint(100 * common.One)
	f.commitInto(func(ctx cosmos.Context) {
		if err := f.mgr.Keeper().MintToModule(ctx, ModuleName, common.NewCoin(common.RuneNative, amt.MulUint64(2))); err != nil {
			b.Fatal(err)
		}
		if err := f.mgr.Keeper().SendFromModuleToAccount(ctx, ModuleName, signer, common.NewCoins(common.NewCoin(common.RuneNative, amt.MulUint64(2)))); err != nil {
			b.Fatal(err)
		}
	})
	msg := NewMsgDeposit(common.Coins{common.NewCoin(common.RuneNative, amt)}, "=:ETH.ETH:"+perfAddr(b, common.ETHChain, "dest/deposit").String()+":0/1/0", signer)
	h := NewDepositHandler(f.mgr)
	f.measure(b, height, func(ctx cosmos.Context) error {
		_, err := h.Run(ctx, msg)
		return err
	})
}

// ---------------------------------------------------------------------------
// Block-level: EndBlock with a mainnet-like streaming swap backlog
// ---------------------------------------------------------------------------

func BenchmarkPerfEndBlock(b *testing.B) {
	f := newPerfFixture(b)
	height := f.ctx.BlockHeight() + 1
	h := NewObservedTxQuorumHandler(f.mgr)
	quorum := len(f.nodes)*2/3 + 2

	cases := []struct {
		name  string
		swaps int
	}{
		{"idle", 0},
		{"streaming-12", 12}, // mainnet streaming swap backlog at shape time
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			fx := *f
			cctx, write := f.ctx.CacheContext()
			fx.ctx = cctx
			for s := 0; s < tc.swaps; s++ {
				fx.commitInto(func(ctx cosmos.Context) {
					ctx = ctx.WithBlockHeight(height)
					if _, err := h.Run(ctx, f.quorumMsg(b, f.inboundSwap(b, s%len(f.vaults), height, fmt.Sprintf("backlog-%d", s)), true, f.nodes[:quorum])); err != nil {
						b.Fatal(err)
					}
				})
			}
			_ = write // the backlog stays local to this sub-benchmark
			queued := 0
			for it := f.mgr.Keeper().GetAdvSwapQueueItemIterator(fx.ctx); it.Valid(); it.Next() {
				queued++
			}
			if queued != tc.swaps {
				b.Fatalf("expected %d queued swaps, got %d", tc.swaps, queued)
			}
			am := NewAppModule(f.mgr, false, true)
			fx.measure(b, height+1, func(ctx cosmos.Context) error {
				_, err := am.EndBlock(ctx)
				return err
			})
		})
	}
}

// ---------------------------------------------------------------------------
// Pure helpers on the hot paths
// ---------------------------------------------------------------------------

func BenchmarkPerfParseMemo(b *testing.B) {
	f := newPerfFixture(b)
	memos := map[string]string{
		"swap-streaming": "=:BSC.USDT:0x25501739f6b96f9B73B07F835a368701F2439D2a:269571177/0/0:-_/t1:0/70",
		"swap-limit":     "=<:ETH.USDC:0x64eb1E31663dBd289b1c2Ff7b1D7cD1f3f8AF1f3:1000000/0/0",
		"outbound":       "OUT:568CBFD56AEDB6026C34438464D8001247607F82E23D3F8F5C18E87A3C6F78B0",
		"trade+":         "TRADE+:thor1zxhfu0qmmq6gmgq4sgz0xgq69h0nhqx5yrseu5",
		"swap-btc":       "=:b:bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh:0/1/0",
	}
	names := make([]string, 0, len(memos))
	for n := range memos {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		memo := memos[name]
		b.Run(name, func(b *testing.B) {
			perfLoop(b, func(i int) {
				_, _ = ParseMemoWithTHORNames(f.ctx, f.mgr.Keeper(), memo)
			})
		})
	}
}

func BenchmarkPerfCommonHelpers(b *testing.B) {
	SetupConfigForTest()
	assets := []string{"BTC.BTC", "ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7", "BSC.USDT", "b", "ETH~ETH"}
	b.Run("NewAsset", func(b *testing.B) {
		perfLoop(b, func(i int) {
			_, _ = common.NewAsset(assets[i%len(assets)])
		})
	})
	addrs := []string{
		"bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh",
		"0x25501739f6b96f9B73B07F835a368701F2439D2a",
		"thor1zxhfu0qmmq6gmgq4sgz0xgq69h0nhqx5yrseu5",
		"ltc1q6mmxscea62dxmqqarsej3hlvycuaz5jawhkw39",
		"THGeCrKWLdYz5ojPzWhkU61C5wXvkEoGrh",
	}
	b.Run("NewAddress", func(b *testing.B) {
		perfLoop(b, func(i int) {
			_, _ = common.NewAddress(addrs[i%len(addrs)])
		})
	})
	a1, a2, a3 := common.BTCAsset, common.ETHAsset, common.BTCAsset
	b.Run("AssetEquals", func(b *testing.B) {
		perfLoop(b, func(i int) {
			_ = a1.Equals(a2)
			_ = a1.Equals(a3)
		})
	})
	b.Run("AssetString", func(b *testing.B) {
		perfLoop(b, func(i int) {
			_ = a1.String()
		})
	})
}
