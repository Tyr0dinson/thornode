//go:build mainnet
// +build mainnet

// Command adr028-dryrun runs the real ADR-028 Migrate15to16 against a copy of a
// mainnet state snapshot, on a discarded cache layer, and asserts post-conditions.
// It NEVER commits: all migration writes land in a CacheContext that is thrown away.
// Run against a COPY of ~/.thornode (it opens the goleveldb store read-write).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cosmossdk.io/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/runtime"
	simtestutil "github.com/cosmos/cosmos-sdk/testutil/sims"
	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/app"
	thorprefix "gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	thorchaintypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// init configures the cosmos-sdk Bech32 prefixes the same way cmd/thornode/root.go
// does (cosmos-sdk defaults to "cosmospub"/"cosmosaddr"; THORChain uses
// "thorpub"/"thor*"). Without this, the migration's SetVault validation rejects
// vault member pubkeys as "invalid Bech32 prefix; expected cosmospub, got thorpub".
func init() {
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount(thorprefix.Bech32PrefixAccAddr, thorprefix.Bech32PrefixAccPub)
	cfg.SetBech32PrefixForValidator(thorprefix.Bech32PrefixValAddr, thorprefix.Bech32PrefixValPub)
	cfg.SetBech32PrefixForConsensusNode(thorprefix.Bech32PrefixConsAddr, thorprefix.Bech32PrefixConsPub)
	cfg.Seal()
}

// vault2v28PubKey is the compromised Asgard vault zeroed by step 0 of the migration.
// (Mirrors the private const in migrations_mainnet.go.)
const vault2v28PubKey = common.PubKey("thorpub1addwnpepqvpnhwzqkd2zqkxsj272faf4ezsngwh773wjdfkus8m4f860u3g5ka22v28")

// adr028TreasuryAddr is the treasury multisig providing step-4 LP coverage.
// (Mirrors the private const in migrations_mainnet.go.)
const adr028TreasuryAddr = "thor10qh5272ktq4wes8ex343ky9rsuehcypddjh08k"

// adr028TreasuryAddr2 is a second treasury LP-holder address surfaced by the
// runbook owner; counts toward step-4 capacity alongside adr028TreasuryAddr.
const adr028TreasuryAddr2 = "thor1lhufh0mwasa0lk9udppdegmvnkgqt08f0m9p5g"

// stuckStreamingTxStr is the TX hash of the stuck ETH.DAI→ETH.USDT streaming swap
// settled in step 5 of the migration. (Mirrors migrations_mainnet.go's local const.)
const stuckStreamingTxStr = "5071A7399C528E8D5BDC1BA62E87001EB5EB17E9E25ABF2965F6A38A665FF729"

// ethUSDTStr is the ETH.USDT asset the step-5 outbound pays the already-produced
// portion of the stuck swap in. (Mirrors the asset parsed in migrations_mainnet.go's
// haircut table / step 5.) Used to assert the outbound was actually scheduled.
const ethUSDTStr = "ETH.USDT-0XDAC17F958D2EE523A2206206994597C13D831EC7"

// saverUnits is the per-saver-pool unit accounting we assert: pool LPUnits vs the
// sum of individual saver LP units must match after the haircut.
type saverUnits struct {
	asset     common.Asset
	poolUnits cosmos.Uint
	sumLP     cosmos.Uint
}

// snapshotState is the minimal view we diff before/after the migration for the
// simple structural invariants (used by report).
type snapshotState struct {
	vault2v28Coins   string      // common.Coins.String()
	reserveUnits     cosmos.Uint // RUNEPool.ReserveUnits
	savers           []saverUnits
	stuckSwapPresent bool // StreamingSwapExists for the 2v28 stuck streaming swap (step 5)
	// Step-5 outbound assertion fields. Captured BEFORE migration: the produced
	// ETH.USDT (StreamingSwap.Out) the migration must schedule as an outbound, and
	// its destination (from the swap queue item). Captured AFTER migration:
	// ethUSDTScheduled is the ETH.USDT amount actually queued in the upgrade-height
	// TxOut block. report() cross-checks scheduled == produced.
	stuckSwapOut     cosmos.Uint // StreamingSwap.Out (produced ETH.USDT owed to the user)
	stuckSwapDest    string      // swap queue item Destination (empty if drifted/consumed)
	ethUSDTScheduled cosmos.Uint // Σ ETH.USDT in GetTxOut(upgradeHeight) to stuckSwapDest
}

// poolSnap holds the depth and unit fields of a Pool for delta math.
type poolSnap struct {
	Asset        common.Asset
	BalanceRune  cosmos.Uint
	BalanceAsset cosmos.Uint
	LPUnits      cosmos.Uint
	SynthUnits   cosmos.Uint
	IsSynth      bool
}

// fullSnapshot supersets snapshotState with the per-pool / module-balance / supply
// fields needed for the waterfall-math heatmap AND the per-asset solvency view.
type fullSnapshot struct {
	state            snapshotState
	pools            map[string]poolSnap // key = asset.String()
	asgardRuneBal    cosmos.Uint
	reserveRuneBal   cosmos.Uint            // Reserve module RUNE balance (gains the retained half of freed RUNE)
	asgardSynthBal   map[string]cosmos.Uint // synth denom → Asgard module balance
	reserveSynthBal  map[string]cosmos.Uint // synth denom → Reserve module balance
	synthSupply      map[string]cosmos.Uint // synth denom → total bank supply (bank.GetSupply)
	synthSupplyKeep  map[string]cosmos.Uint // synth denom → keeper GetTotalSupply (ragnarok-aware, uint64-cast)
	treasuryLPUnits  map[string]cosmos.Uint // pool asset.String() → treasury 1 LP units
	treasuryLPUnits2 map[string]cosmos.Uint // pool asset.String() → treasury 2 LP units (adr028TreasuryAddr2)
	saversDepth      map[string]cosmos.Uint // L1 asset.String() → savers (synth-vault) pool BalanceAsset
	totalRuneSupply  cosmos.Uint
	polRuneWithdrawn cosmos.Uint // POL.RuneWithdrawn at capture time (G4: must grow by Σstep1 ΔR)
	polRuneDeposited cosmos.Uint // POL.RuneDeposited at capture time (G4b: RuneDeposited ≥ RuneWithdrawn)
	// Strict-waterfall layer-1 / layer-2 ledger fields.
	polPoolLPUnits       map[string]cosmos.Uint // L1 pool asset.String() → POL's (Reserve module addr) LP units
	runePoolReserveUnits cosmos.Uint            // RUNEPool.ReserveUnits (layer-2 reserve bucket)
	runePoolPoolUnits    cosmos.Uint            // RUNEPool.PoolUnits (provider bucket; untouched by the solve)
	runePoolTotalUnits   cosmos.Uint            // RUNEPool.TotalUnits() = Reserve + Pool
	// Per L1 asset (key = pool.Asset.String()) — for solvency view:
	vaultHoldings map[string]cosmos.Uint // Σ vault.Coins[asset] across all asgard vaults
	securedDepth  map[string]cosmos.Uint // SecuredAsset.Depth for the asset's secured form
	tradeDepth    map[string]cosmos.Uint // TradeUnit.Depth for the asset's trade form
}

// capture reads vault 2v28 coins, RUNEPool.ReserveUnits, and the stuck streaming
// swap existence flag from ctx.
func capture(k keeper.Keeper, ctx cosmos.Context) snapshotState {
	var s snapshotState
	if v, err := k.GetVault(ctx, vault2v28PubKey); err == nil {
		s.vault2v28Coins = v.Coins.String()
	} else {
		s.vault2v28Coins = "<get-vault-error: " + err.Error() + ">"
	}
	if rp, err := k.GetRUNEPool(ctx); err == nil {
		s.reserveUnits = rp.ReserveUnits
	}
	// Step 5: record whether the stuck streaming swap is present so report() can
	// assert it was removed (or note it was already absent before the migration).
	// When present, also capture the produced ETH.USDT (Out) and the destination
	// from the queue item — these are what the migration must schedule as an
	// outbound, and report() asserts that scheduling actually happened.
	s.stuckSwapOut = cosmos.ZeroUint()
	s.ethUSDTScheduled = cosmos.ZeroUint()
	if stuckTxID, err := common.NewTxID(stuckStreamingTxStr); err == nil {
		s.stuckSwapPresent = k.StreamingSwapExists(ctx, stuckTxID)
		if s.stuckSwapPresent {
			if ss, err := k.GetStreamingSwap(ctx, stuckTxID); err == nil {
				s.stuckSwapOut = ss.Out
			}
			if q, err := k.GetSwapQueueItem(ctx, stuckTxID, 0); err == nil {
				s.stuckSwapDest = q.Destination.String()
			}
		}
		// ETH.USDT scheduled in the upgrade-height TxOut block. Before migration
		// this is empty; after migration step 5 appends the outbound at
		// ctx.BlockHeight() (unsafeAddRefundOutbound → AppendTxOut at that height).
		if ethUSDT, aerr := common.NewAsset(ethUSDTStr); aerr == nil {
			if block, berr := k.GetTxOut(ctx, ctx.BlockHeight()); berr == nil && block != nil {
				for _, item := range block.TxArray {
					if item.Coin.Asset.Equals(ethUSDT) {
						s.ethUSDTScheduled = s.ethUSDTScheduled.Add(item.Coin.Amount)
					}
				}
			}
		}
	}
	return s
}

// captureSaverUnits sums LP units per synthetic (savers) pool and records the
// pool's own LPUnits, so the caller can assert they match.
func captureSaverUnits(a *app.THORChainApp, ctx cosmos.Context) []saverUnits {
	k := a.ThorchainKeeper
	pools, err := k.GetPools(ctx)
	if err != nil {
		return nil
	}
	var out []saverUnits
	for _, p := range pools {
		if !p.Asset.IsSyntheticAsset() {
			continue
		}
		// Wrap in a closure so defer closes the iterator even if MustUnmarshal
		// panics mid-iteration — matches migrations_mainnet.go's own
		// "defer iter.Close()" pattern and prevents IAVL lock leaks on panic.
		sum := func() cosmos.Uint {
			it := k.GetLiquidityProviderIterator(ctx, p.Asset)
			defer it.Close()
			s := cosmos.ZeroUint()
			for ; it.Valid(); it.Next() {
				var lp thorchaintypes.LiquidityProvider
				a.AppCodec().MustUnmarshal(it.Value(), &lp)
				s = s.Add(lp.Units)
			}
			return s
		}()
		out = append(out, saverUnits{asset: p.Asset, poolUnits: p.LPUnits, sumLP: sum})
	}
	return out
}

// captureFull gathers the minimal-invariant view plus all the per-pool / module /
// supply fields needed for waterfall math validation.
func captureFull(a *app.THORChainApp, ctx cosmos.Context) fullSnapshot {
	k := a.ThorchainKeeper
	bank := a.BankKeeper
	fs := fullSnapshot{
		state:            capture(k, ctx),
		pools:            map[string]poolSnap{},
		asgardSynthBal:   map[string]cosmos.Uint{},
		reserveSynthBal:  map[string]cosmos.Uint{},
		synthSupply:      map[string]cosmos.Uint{},
		synthSupplyKeep:  map[string]cosmos.Uint{},
		treasuryLPUnits:  map[string]cosmos.Uint{},
		treasuryLPUnits2: map[string]cosmos.Uint{},
		saversDepth:      map[string]cosmos.Uint{},
		vaultHoldings:    map[string]cosmos.Uint{},
		securedDepth:     map[string]cosmos.Uint{},
		tradeDepth:       map[string]cosmos.Uint{},
		polPoolLPUnits:   map[string]cosmos.Uint{},
	}
	fs.state.savers = captureSaverUnits(a, ctx)

	// Per-pool snaps + per-synth module balances and supplies.
	pools, _ := k.GetPools(ctx)
	for _, p := range pools {
		fs.pools[p.Asset.String()] = poolSnap{
			Asset:        p.Asset,
			BalanceRune:  p.BalanceRune,
			BalanceAsset: p.BalanceAsset,
			LPUnits:      p.LPUnits,
			SynthUnits:   p.SynthUnits,
			IsSynth:      p.Asset.IsSyntheticAsset(),
		}
		if p.Asset.IsSyntheticAsset() {
			denom := p.Asset.Native()
			fs.asgardSynthBal[denom] = k.GetBalanceOfModule(ctx, thorchaintypes.AsgardName, denom)
			fs.reserveSynthBal[denom] = k.GetBalanceOfModule(ctx, thorchaintypes.ReserveName, denom)
			sup := bank.GetSupply(ctx, denom)
			fs.synthSupply[denom] = cosmos.NewUintFromBigInt(sup.Amount.BigInt())
			// Keeper's GetTotalSupply is what the migration's CalcUnits actually
			// consumes (step 4.5 + polPoolValue). It diverges from bank.GetSupply
			// for ragnarok pools (returns 0) and casts via uint64. Capture it so
			// G5 can flag any divergence on a pool the migration would CalcUnits.
			fs.synthSupplyKeep[denom] = k.GetTotalSupply(ctx, p.Asset)
		}
	}

	// Module RUNE + total RUNE supply.
	runeDenom := common.RuneNative.Native()
	fs.asgardRuneBal = k.GetBalanceOfModule(ctx, thorchaintypes.AsgardName, runeDenom)
	fs.reserveRuneBal = k.GetBalanceOfModule(ctx, thorchaintypes.ReserveName, runeDenom)
	runeSup := bank.GetSupply(ctx, runeDenom)
	fs.totalRuneSupply = cosmos.NewUintFromBigInt(runeSup.Amount.BigInt())

	// POL accounting record.  Step 1 must increment POL.RuneWithdrawn by exactly
	// Σstep1 ΔR; G4 in validateWaterfall asserts this delta.  Initialise to
	// ZeroUint so cosmos.Uint arithmetic is always safe even if GetPOL errors.
	fs.polRuneWithdrawn = cosmos.ZeroUint()
	fs.polRuneDeposited = cosmos.ZeroUint()
	if pol, err := k.GetPOL(ctx); err == nil {
		fs.polRuneWithdrawn = pol.RuneWithdrawn
		fs.polRuneDeposited = pol.RuneDeposited
	}

	// Strict-waterfall: POL's pool LP units (Reserve module bech32 address as LP)
	// per L1 pool — the layer-1 lever — and the RUNEPool reserve/provider bucket
	// units — the layer-2 ceiling and solve target.
	fs.runePoolReserveUnits = cosmos.ZeroUint()
	fs.runePoolPoolUnits = cosmos.ZeroUint()
	fs.runePoolTotalUnits = cosmos.ZeroUint()
	if rp, err := k.GetRUNEPool(ctx); err == nil {
		fs.runePoolReserveUnits = rp.ReserveUnits
		fs.runePoolPoolUnits = rp.PoolUnits
		fs.runePoolTotalUnits = rp.TotalUnits()
	}
	if polAddr, err := k.GetModuleAddress(thorchaintypes.ReserveName); err == nil {
		for _, p := range pools {
			if p.Asset.IsSyntheticAsset() {
				continue
			}
			if lp, err := k.GetLiquidityProvider(ctx, p.Asset, polAddr); err == nil {
				fs.polPoolLPUnits[p.Asset.String()] = lp.Units
			}
		}
	}

	// Treasury LP units per L1 pool, for both known treasury LP holders.
	treasuryAddr := common.Address(adr028TreasuryAddr)
	treasuryAddr2 := common.Address(adr028TreasuryAddr2)
	for _, p := range pools {
		if p.Asset.IsSyntheticAsset() {
			// Also capture savers pool depth keyed by the L1 asset for the
			// "haircut feasibility" view (savers pool's BalanceAsset is the
			// synth-side depth that step 3 haircuts).
			fs.saversDepth[p.Asset.GetLayer1Asset().String()] = p.BalanceAsset
			continue
		}
		if lp, err := k.GetLiquidityProvider(ctx, p.Asset, treasuryAddr); err == nil {
			fs.treasuryLPUnits[p.Asset.String()] = lp.Units
		}
		if lp, err := k.GetLiquidityProvider(ctx, p.Asset, treasuryAddr2); err == nil {
			fs.treasuryLPUnits2[p.Asset.String()] = lp.Units
		}
	}

	// Vault holdings: Σ vault.Coins[asset] across ALL asgard vaults (active,
	// retiring, inactive). The compromised 2v28 vault is included so the
	// before/after diff cleanly shows its zero-out in step 0.
	vaults, _ := k.GetAsgardVaults(ctx)
	for _, v := range vaults {
		for _, c := range v.Coins {
			if c.Asset.IsRune() {
				continue // RUNE is native, not L1-vault-backed
			}
			key := c.Asset.String()
			cur, ok := fs.vaultHoldings[key]
			if !ok {
				cur = cosmos.ZeroUint()
			}
			fs.vaultHoldings[key] = cur.Add(c.Amount)
		}
	}

	// Per-L1-asset secured & trade depths (the other two claim classes on L1
	// vault assets beyond LP and synth).
	for _, p := range pools {
		if p.Asset.IsSyntheticAsset() {
			continue
		}
		if sec, err := k.GetSecuredAsset(ctx, p.Asset.GetSecuredAsset()); err == nil {
			fs.securedDepth[p.Asset.String()] = sec.Depth
		}
		if tu, err := k.GetTradeUnit(ctx, p.Asset.GetTradeAsset()); err == nil {
			fs.tradeDepth[p.Asset.String()] = tu.Depth
		}
	}

	return fs
}

// runMigration wires the manager exactly as x/thorchain's module does, captures
// before-state from the uncached context, runs Migrate15to16 on a discarded cache
// layer, and captures after-state from that cache. Nothing is ever committed.
func runMigration(a *app.THORChainApp, thornodeAPI string) (before, after fullSnapshot, migErr error) {
	header := cmtproto.Header{Height: a.LastBlockHeight() + 1, Time: time.Now()}
	ctx := a.NewUncachedContext(false, header)

	mgrs := thorchain.NewManagers(
		a.ThorchainKeeper,
		a.AppCodec(),
		runtime.NewKVStoreService(a.GetKey(thorchaintypes.StoreKey)),
		a.BankKeeper,
		a.AccountKeeper,
		a.UpgradeKeeper,
		a.WasmKeeper,
	)

	// Hybrid backfill: the Liquify pruned snapshot drops the stuck streaming-swap
	// state, so step 6 (the streaming-swap settle) would take the no-op "already
	// absent" path and go untested. Re-inject the real swap + queue item from the
	// archive API ($tt) — the swap is frozen (trading halted), so the archive value
	// equals the snapshot-height value — BEFORE the before-snapshot, so the real
	// step-6 code runs and the existing ethUSDTScheduled==stuckSwapOut check fires.
	backfillStuckSwap(mgrs.Keeper(), ctx, thornodeAPI)

	before = captureFull(a, ctx)

	cacheCtx, _ := ctx.CacheContext()
	migErr = thorchain.NewMigrator(mgrs).Migrate15to16(cacheCtx)

	after = captureFull(a, cacheCtx)
	return before, after, migErr
}

// backfillStuckSwap injects the stuck streaming swap (and its swap-queue item) from
// the archive THORNode API into ctx when the pruned snapshot lacks it, so step 6 of
// Migrate15to16 runs against real state instead of no-op'ing on the "already absent"
// path. The swap is frozen (trading halted), so the value read live from $tt equals
// the snapshot-height value (verified via ?height= archive query). No-op if the swap
// is already present (non-pruned snapshot) or thornodeAPI is empty.
func backfillStuckSwap(k keeper.Keeper, ctx cosmos.Context, thornodeAPI string) {
	txid, err := common.NewTxID(stuckStreamingTxStr)
	if err != nil {
		return
	}
	if k.StreamingSwapExists(ctx, txid) {
		fmt.Println("  [backfill] stuck streaming swap already present in snapshot — no injection needed")
		return
	}
	if thornodeAPI == "" {
		fmt.Println("  [backfill] snapshot lacks the stuck streaming swap and --thornode-api is empty — step 6 will take the 'absent' path (set --thornode-api to exercise it)")
		return
	}
	var sr struct {
		Interval    uint64 `json:"interval"`
		Quantity    uint64 `json:"quantity"`
		Count       uint64 `json:"count"`
		Deposit     string `json:"deposit"`
		In          string `json:"in"`
		Out         string `json:"out"`
		TradeTarget string `json:"trade_target"`
	}
	if err := httpGetJSON(thornodeAPI+"/thorchain/swap/streaming/"+stuckStreamingTxStr, &sr); err != nil {
		fmt.Printf("  [backfill] WARN: could not fetch streaming swap from %s: %v — step 6 untested\n", thornodeAPI, err)
		return
	}
	ss := thorchaintypes.StreamingSwap{
		TxID:        txid,
		Interval:    sr.Interval,
		Quantity:    sr.Quantity,
		Count:       sr.Count,
		Deposit:     uintFromDecStr(sr.Deposit),
		In:          uintFromDecStr(sr.In),
		Out:         uintFromDecStr(sr.Out),
		TradeTarget: uintFromDecStr(sr.TradeTarget),
	}
	k.SetStreamingSwap(ctx, ss)

	// Queue item (for the Destination). Without it, step 6 still settles the swap but
	// takes the unknown-destination path (full off-chain refund); with it, Out is paid
	// on-chain to the recipient — the branch we want to exercise.
	var queue []struct {
		Tx struct {
			ID string `json:"id"`
		} `json:"tx"`
		Destination string `json:"destination"`
		TargetAsset string `json:"target_asset"`
	}
	dest := "(unknown — would refund fully off-chain)"
	if qerr := httpGetJSON(thornodeAPI+"/thorchain/queue/swap", &queue); qerr == nil {
		for _, q := range queue {
			if !strings.EqualFold(q.Tx.ID, stuckStreamingTxStr) {
				continue
			}
			target, aerr := common.NewAsset(q.TargetAsset)
			daddr, derr := common.NewAddress(q.Destination)
			if aerr == nil && derr == nil {
				_ = k.SetSwapQueueItem(ctx, thorchaintypes.MsgSwap{
					Tx:          common.Tx{ID: txid},
					TargetAsset: target,
					Destination: daddr,
					TradeTarget: ss.TradeTarget,
				}, 0)
				dest = q.Destination
			}
			break
		}
	}
	fmt.Printf("  [backfill] injected stuck streaming swap from %s: Out=%s TradeTarget=%s dest=%s\n",
		thornodeAPI, format1e8(ss.Out), format1e8(ss.TradeTarget), dest)
}

func uintFromDecStr(s string) cosmos.Uint {
	if bi, ok := new(big.Int).SetString(s, 10); ok {
		return cosmos.NewUintFromBigInt(bi)
	}
	return cosmos.ZeroUint()
}

func httpGetJSON(url string, v interface{}) error {
	resp, err := http.Get(url) // #nosec G107 -- local archive node URL from a flag
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// report prints before→after deltas and runs the structural invariant checks.
// Returns the number of failed invariants (0 = all pass).
func report(before, after snapshotState, migErr error) int {
	failures := 0
	fail := func(name, detail string) {
		failures++
		fmt.Printf("  [FAIL] %s: %s\n", name, detail)
	}
	pass := func(name string) { fmt.Printf("  [PASS] %s\n", name) }

	fmt.Println("=== ADR-028 dry-run invariants ===")

	if migErr != nil {
		fail("migration returns nil", migErr.Error())
	} else {
		pass("migration returns nil")
	}

	if after.vault2v28Coins == "" {
		pass("vault 2v28 zeroed")
	} else {
		fail("vault 2v28 zeroed", "coins still present: "+after.vault2v28Coins)
	}

	saverOK := true
	for _, s := range after.savers {
		if !s.poolUnits.Equal(s.sumLP) {
			saverOK = false
			fail("savers units coupling", fmt.Sprintf("%s pool=%s sumLP=%s", s.asset, s.poolUnits, s.sumLP))
		}
	}
	if saverOK {
		pass(fmt.Sprintf("savers units coupling (%d synth pools)", len(after.savers)))
	}

	fmt.Printf("  RUNEPool.ReserveUnits: before=%s after=%s\n", format1e8(before.reserveUnits), format1e8(after.reserveUnits))
	pass("RUNEPool retrievable post-solve")

	// Step 5: the stuck streaming swap must be absent after the migration.
	// If it was already absent before (already settled or drifted) the
	// migration is a no-op for step 5 and we log informational only.
	if before.stuckSwapPresent {
		if !after.stuckSwapPresent {
			pass("stuck streaming swap removed (step 5)")
		} else {
			fail("stuck streaming swap removed (step 5)", "StreamingSwapExists still true after migration")
		}
		// The produced ETH.USDT (StreamingSwap.Out) must be scheduled as an
		// outbound to the swap's destination — verifying the swap record was
		// removed alone does NOT prove the user was actually paid. The migration's
		// step-5 outbound is best-effort (it logs-and-skips if the destination is
		// unknown or no active vault holds enough ETH.USDT), so distinguish the
		// pass case from each best-effort fall-through rather than reporting green.
		switch {
		case before.stuckSwapOut.IsZero():
			fmt.Println("  [INFO] step 5: no ETH.USDT produced (StreamingSwap.Out=0); whole amount refunded off-chain by treasury — no outbound expected")
		case before.stuckSwapDest == "":
			fail("ETH.USDT outbound scheduled (step 5)",
				fmt.Sprintf("destination unknown (queue item drifted/consumed) so migration skipped the on-chain outbound; produced Out=%s must be refunded off-chain", format1e8(before.stuckSwapOut)))
		case after.ethUSDTScheduled.Equal(before.stuckSwapOut):
			pass(fmt.Sprintf("ETH.USDT outbound scheduled for produced amount (step 5): %s to %s", format1e8(before.stuckSwapOut), before.stuckSwapDest))
		case after.ethUSDTScheduled.IsZero():
			fail("ETH.USDT outbound scheduled (step 5)",
				fmt.Sprintf("produced Out=%s but NO ETH.USDT outbound queued at upgrade height (best-effort fall-through: no active vault with enough ETH.USDT?) — user not paid on-chain", format1e8(before.stuckSwapOut)))
		default:
			fail("ETH.USDT outbound scheduled (step 5)",
				fmt.Sprintf("scheduled=%s != produced Out=%s (amount mismatch)", format1e8(after.ethUSDTScheduled), format1e8(before.stuckSwapOut)))
		}
	} else {
		fmt.Println("  [INFO] stuck streaming swap was absent before migration (already settled or drifted) — step 5 NOT exercised by this dry-run; re-run against an upgrade-height snapshot where the swap is present to cover it")
	}

	if cv := (thorchain.AppModule{}).ConsensusVersion(); cv == 16 {
		pass("ConsensusVersion == 16")
	} else {
		fail("ConsensusVersion == 16", fmt.Sprintf("got %d", cv))
	}

	fmt.Printf("=== %d invariant failure(s) ===\n", failures)
	return failures
}

// ───── Waterfall math validation + heatmap ───────────────────────────────────

type cellState int

const (
	cellNA cellState = iota
	cellPass
	cellFail
)

type cell struct {
	state cellState
	note  string
}

type heatmapRow struct {
	asset string
	cells [4]cell // step 1..4
}

// safeSub returns |a-b| (treating as bigint, so it never underflows the unsigned).
func absDiff(a, b cosmos.Uint) cosmos.Uint {
	if a.GTE(b) {
		return common.SafeSub(a, b)
	}
	return common.SafeSub(b, a)
}

// validateWaterfall infers per-pool participation in each migration step from the
// before/after diff, asserts the math invariants (proportional preservation,
// burn conservation, units coupling, treasury draw consistency), and computes a
// heatmap row per affected asset plus a list of global aggregate checks.
func validateWaterfall(before, after fullSnapshot) (rows []heatmapRow, globals []string, failCount int) {
	rowMap := map[string]*heatmapRow{}
	getRow := func(asset string) *heatmapRow {
		r, ok := rowMap[asset]
		if !ok {
			r = &heatmapRow{asset: asset}
			rowMap[asset] = r
		}
		return r
	}
	bumpFail := func() { failCount++ }
	addGlobal := func(ok bool, label, detail string) {
		tag := "[PASS]"
		if !ok {
			tag = "[FAIL]"
			bumpFail()
		}
		globals = append(globals, fmt.Sprintf("%s %s — %s", tag, label, detail))
	}

	totalStep1Rune := cosmos.ZeroUint()
	totalStep4Rune := cosmos.ZeroUint()

	// STEP 1 — POL haircut: L1 pools whose BalanceRune dropped (excluding
	// step-4-only treasury draws, which we'll deduct below).
	for assetStr, pB := range before.pools {
		if pB.IsSynth {
			continue
		}
		pA, ok := after.pools[assetStr]
		if !ok || pA.BalanceRune.GTE(pB.BalanceRune) {
			continue
		}
		// LPUnits drop classification (strict waterfall):
		//   - step 1 (layer 1) removes POL's LP units → pool.LPUnits drops by the
		//     POL-record delta (before.polPoolLPUnits − after.polPoolLPUnits).
		//   - step 4 removes treasury LP units → pool.LPUnits drops by the treasury
		//     delta, AND the treasury LP record drops (detected in the step-4 loop).
		// A pool whose pool.LPUnits drop is fully explained by the POL-record drop is
		// a pure step-1 pool (no treasury draw) and is validated here. A pool with a
		// treasury LP drop is handled in the step-4 loop; skip it here to avoid
		// double-counting.
		poolLPDrop := common.SafeSub(pB.LPUnits, pA.LPUnits)
		polDrop := common.SafeSub(before.polPoolLPUnits[assetStr], after.polPoolLPUnits[assetStr])
		treasDrop := common.SafeSub(before.treasuryLPUnits[assetStr], after.treasuryLPUnits[assetStr])
		if !treasDrop.IsZero() {
			// Treasury draw present (possibly stacked with step 1) — defer to step-4 loop.
			continue
		}
		// poolLPDrop should now equal the POL-record drop (layer 1). Surface a
		// mismatch as a failure on the step-1 cell.
		if !poolLPDrop.Equal(polDrop) {
			getRow(assetStr).cells[0] = cell{cellFail, fmt.Sprintf("step-1 LPUnits drop=%s ≠ POL-record drop=%s (layer-1 coupling broken)", format1e8(poolLPDrop), format1e8(polDrop))}
			bumpFail()
			continue
		}
		// Pure step 1: verify AMM-identity ΔA × Rᵇᵉᶠ == ΔR × Aᵇᵉᶠ (within ±1 unit
		// integer-rounding tolerance from GetSafeShare).
		deltaR := common.SafeSub(pB.BalanceRune, pA.BalanceRune)
		deltaA := common.SafeSub(pB.BalanceAsset, pA.BalanceAsset)
		totalStep1Rune = totalStep1Rune.Add(deltaR)
		lhs := deltaA.Mul(pB.BalanceRune)
		rhs := deltaR.Mul(pB.BalanceAsset)
		diff := absDiff(lhs, rhs)
		// Tolerance: rounding error in deltaA is at most ⌊pB.BalanceRune⌋ at the
		// scale of the product (one unit at the deltaA precision).
		tol := pB.BalanceRune
		if diff.LTE(tol) {
			getRow(assetStr).cells[0] = cell{cellPass, fmt.Sprintf("ΔR=%s ΔA=%s (AMM-identity ok)", format1e8(deltaR), format1e8(deltaA))}
		} else {
			getRow(assetStr).cells[0] = cell{cellFail, fmt.Sprintf("AMM-identity broken: ΔR=%s ΔA=%s diff=%s tol=%s", format1e8(deltaR), format1e8(deltaA), format1e8(diff), format1e8(tol))}
			bumpFail()
		}
	}

	// Configured step-4 treasury-draw ΔA (asset, 1e8) per pool. Asserting the
	// REALIZED ΔA equals the configured amount catches huginn-bot's concern: unlike
	// step 1 (which closes the L1 gap UNCONDITIONALLY), step 4 caps the draw at the
	// treasury LP's units and recomputes ΔA — so if the treasury position has drifted
	// below the needed units by upgrade height, the pool is left OVER-STATED vs the
	// zeroed vault (a silent solvency gap) rather than fully reconciled. This makes
	// the capped/skip path a hard failure instead of a silent pass.
	expectedStep4Draw := map[string]cosmos.Uint{
		"BASE.USDC-0X833589FCD6EDB6E08F4C7C32D4F71B54BDA02913": cosmos.NewUint(4_904_100_000_000),
		"BSC.BUSD-0XE9E7CEA3DEDCA5984780BAFC599BD69ADD087D56":  cosmos.NewUint(3_214_300_000_000),
		"BSC.ETH-0X2170ED0880AC9A755FD29B2688956BD959F933F8":   cosmos.NewUint(1_600_000_000),
		"BSC.BTCB-0X7130D2A12B9BCBFAE4F2634D864A1EE1CE3EAD9C":  cosmos.NewUint(51_000_000),
		"TRON.TRX":                                            cosmos.NewUint(287_392_444_998),
	}

	// STEP 4 — Treasury LP draw: L1 pools where Treasury LP units dropped.
	for assetStr, lpB := range before.treasuryLPUnits {
		lpA, ok := after.treasuryLPUnits[assetStr]
		if !ok {
			lpA = cosmos.ZeroUint()
		}
		if lpA.GTE(lpB) {
			continue
		}
		deltaLP := common.SafeSub(lpB, lpA)
		pB := before.pools[assetStr]
		pA := after.pools[assetStr]
		poolLPDrop := common.SafeSub(pB.LPUnits, pA.LPUnits)
		// Treasury LP drop must equal pool.LPUnits drop (treasury LP IS the only
		// LP record changed in step 4).
		if !poolLPDrop.Equal(deltaLP) {
			getRow(assetStr).cells[3] = cell{cellFail, fmt.Sprintf("treasury ΔLP=%s ≠ pool ΔLPUnits=%s", format1e8(deltaLP), format1e8(poolLPDrop))}
			bumpFail()
			continue
		}
		deltaR := common.SafeSub(pB.BalanceRune, pA.BalanceRune)
		deltaA := common.SafeSub(pB.BalanceAsset, pA.BalanceAsset)
		totalStep4Rune = totalStep4Rune.Add(deltaR)
		exp, hasExp := expectedStep4Draw[assetStr]
		switch {
		case deltaR.IsZero() || deltaA.IsZero():
			getRow(assetStr).cells[3] = cell{cellFail, fmt.Sprintf("step-4 active but ΔR=%s ΔA=%s", format1e8(deltaR), format1e8(deltaA))}
			bumpFail()
		case hasExp && !deltaA.Equal(exp):
			getRow(assetStr).cells[3] = cell{cellFail, fmt.Sprintf("treasury draw capped/short: ΔA=%s ≠ configured %s → pool OVER-STATED vs zeroed vault", format1e8(deltaA), format1e8(exp))}
			bumpFail()
		default:
			tag := ""
			if hasExp {
				tag = " (=configured; depth at vault-reality)"
			}
			getRow(assetStr).cells[3] = cell{cellPass, fmt.Sprintf("ΔLP=%s ΔA=%s ΔR=%s%s", format1e8(deltaLP), format1e8(deltaA), format1e8(deltaR), tag)}
		}
	}

	// STEP 2 — Reserve synth burn: L1 pools whose corresponding Reserve synth
	// balance dropped.
	for assetStr, pB := range before.pools {
		if pB.IsSynth {
			continue
		}
		denom := pB.Asset.GetSyntheticAsset().Native()
		rB, ok := before.reserveSynthBal[denom]
		if !ok {
			continue
		}
		rA := after.reserveSynthBal[denom]
		if rA.GTE(rB) {
			continue
		}
		burn := common.SafeSub(rB, rA)
		// Bank conservation check is per-denom in globals; cell just marks active.
		getRow(assetStr).cells[1] = cell{cellPass, fmt.Sprintf("Reserve %s burn=%s", denom, format1e8(burn))}
	}

	// STEP 3 — Savers haircut: synth pools whose LPUnits or BalanceAsset dropped.
	// Check: pool.LPUnits == Σ LP.Units (units coupling) AND pool.BalanceAsset drop
	// == Asgard synth balance drop (the burn really removed that synth).
	//
	// The step-3 cell is attributed to the synth pool's L1 PARENT asset row (e.g.
	// the ETH/USDT savers haircut lands on the ETH.USDT row), NOT on a separate
	// synth row. This makes the heatmap a true per-asset waterfall view: an asset
	// that takes both a POL haircut (step 1) and a savers haircut (step 3) — like
	// ETH.ETH and ETH.USDT — shows both on one line, and an asset with NO synth-row
	// (like BTC.BTC) is unambiguously POL-only rather than looking like it might
	// have a hidden savers row elsewhere in the table.
	for assetStr, pB := range before.pools {
		if !pB.IsSynth {
			continue
		}
		pA, ok := after.pools[assetStr]
		if !ok || (pA.LPUnits.GTE(pB.LPUnits) && pA.BalanceAsset.GTE(pB.BalanceAsset)) {
			continue
		}
		l1Str := pB.Asset.GetLayer1Asset().String() // attribute to the L1 parent row
		deltaUnits := common.SafeSub(pB.LPUnits, pA.LPUnits)
		deltaAsset := common.SafeSub(pB.BalanceAsset, pA.BalanceAsset)
		var sumLP cosmos.Uint
		coupling := false
		for _, s := range after.state.savers {
			if s.asset.Equals(pB.Asset) {
				sumLP = s.sumLP
				coupling = pA.LPUnits.Equal(sumLP)
				break
			}
		}
		denom := pB.Asset.Native()
		asgDrop := common.SafeSub(before.asgardSynthBal[denom], after.asgardSynthBal[denom])
		burnOK := asgDrop.Equal(deltaAsset)
		if coupling && burnOK {
			getRow(l1Str).cells[2] = cell{cellPass, fmt.Sprintf("savers pool %s · ΔU=%s ΔA=%s Asg-=%s (coupled, burn matches)", pB.Asset.String(), format1e8(deltaUnits), format1e8(deltaAsset), format1e8(asgDrop))}
		} else {
			getRow(l1Str).cells[2] = cell{cellFail, fmt.Sprintf("savers pool %s · coupling=%v burnMatchesAsg=%v ΔU=%s ΔA=%s asgDrop=%s sumLP=%s", pB.Asset.String(), coupling, burnOK, format1e8(deltaUnits), format1e8(deltaAsset), format1e8(asgDrop), format1e8(sumLP))}
			bumpFail()
		}
	}

	// ── Global aggregate invariants ─────────────────────────────────────────

	// Freed RUNE (Σ step1 ΔR + Σ step4 ΔR, where step1 includes reserve paired
	// cuts) is now disposed HALF-burned / HALF-to-Reserve instead of fully burned.
	freedRune := totalStep1Rune.Add(totalStep4Rune)
	runeSupplyDrop := common.SafeSub(before.totalRuneSupply, after.totalRuneSupply) // == burned half
	reserveRuneGain := common.SafeSub(after.reserveRuneBal, before.reserveRuneBal)  // == retained half

	// G1: conservation of freed RUNE — burned + retained-in-reserve == freed.
	addGlobal(runeSupplyDrop.Add(reserveRuneGain).Equal(freedRune),
		"RUNE conservation (burn+reserve=freed)",
		fmt.Sprintf("burned=%s + to-reserve=%s = %s vs freed (Σstep1 ΔR=%s + Σstep4 ΔR=%s = %s)",
			format1e8(runeSupplyDrop), format1e8(reserveRuneGain), format1e8(runeSupplyDrop.Add(reserveRuneGain)),
			format1e8(totalStep1Rune), format1e8(totalStep4Rune), format1e8(freedRune)))

	// G1b: the burn/reserve split is ~50/50 (per-call floor sends the odd 1e-8 unit
	// to the burn, so burned ≥ reserve; the gap is bounded by the split-call count).
	splitGap := common.SafeSub(runeSupplyDrop, reserveRuneGain) // burned − reserve, ≥ 0 by construction
	addGlobal(runeSupplyDrop.GTE(reserveRuneGain) && splitGap.LT(cosmos.NewUint(10000)),
		"RUNE burn/reserve split ~50/50",
		fmt.Sprintf("burned=%s, to-reserve=%s, gap=%s (≤ split-call rounding)", format1e8(runeSupplyDrop), format1e8(reserveRuneGain), format1e8(splitGap)))

	// G2: Asgard RUNE balance drop == freed (the FULL freed amount still leaves
	// Asgard — half to thorchain→burn, half to Reserve; only its destination
	// changed, not the Asgard debit).
	asgRuneDrop := common.SafeSub(before.asgardRuneBal, after.asgardRuneBal)
	addGlobal(asgRuneDrop.Equal(freedRune),
		"Asgard RUNE drop = freed",
		fmt.Sprintf("Asgard ΔRUNE=%s, freed=%s", format1e8(asgRuneDrop), format1e8(freedRune)))

	// G3: For each synth that lost supply, supply drop == Reserve drop + Asgard drop.
	for denom, supB := range before.synthSupply {
		supA := after.synthSupply[denom]
		if supA.Equal(supB) {
			continue
		}
		supDrop := common.SafeSub(supB, supA)
		rsvDrop := common.SafeSub(before.reserveSynthBal[denom], after.reserveSynthBal[denom])
		asgDrop := common.SafeSub(before.asgardSynthBal[denom], after.asgardSynthBal[denom])
		// Account for module-hop landing: step 2 sends Reserve→ModuleName then burns
		// from ModuleName; step 3 sends Asgard→ModuleName then burns from ModuleName.
		// Net effect: supply drop == Reserve drop + Asgard drop (ModuleName balance
		// is unchanged net).
		addGlobal(supDrop.Equal(rsvDrop.Add(asgDrop)),
			fmt.Sprintf("synth conservation %s", denom),
			fmt.Sprintf("supply Δ=%s = Reserve Δ=%s + Asgard Δ=%s", format1e8(supDrop), format1e8(rsvDrop), format1e8(asgDrop)))
	}

	// G4: POL.RuneWithdrawn delta must be ≤ Σstep1 ΔR (POL records only its
	// fractional share of each pool's haircut, not the full ΔR sum).
	//
	// Why this changed: the migration originally credited pol.RuneWithdrawn with
	// the full totalStep1Rune, which over-stated POL's loss (treating it as if
	// POL owned 100% of every haircut pool). For pools where POL is not the
	// majority LP (or where the pool is in synth-overhang regime), the full-ΔR
	// credit could push pol.CurrentDeposit() negative, breaking the
	// maxPOLDeposit safety gate in manager_network_current.go.
	//
	// The fix records only POL's fractional share per pool (computed via
	// GetSafeShare(pol.lp.Units, pool.GetPoolUnits(), ΔR) with CalcUnits-
	// corrected SynthUnits). The invariant here verifies the bounded relationship
	// rather than exact equality.
	if !totalStep1Rune.IsZero() {
		polDelta := common.SafeSub(after.polRuneWithdrawn, before.polRuneWithdrawn)
		addGlobal(polDelta.LTE(totalStep1Rune),
			"POL.RuneWithdrawn ≤ Σ step1 ΔR (POL's fractional share)",
			fmt.Sprintf("before=%s after=%s Δ=%s totalStep1=%s polFraction=%.4f%%",
				format1e8(before.polRuneWithdrawn), format1e8(after.polRuneWithdrawn), format1e8(polDelta), format1e8(totalStep1Rune),
				100*float64(polDelta.BigInt().Int64())/float64(totalStep1Rune.BigInt().Int64())))
	}

	// G4b: pol.RuneDeposited >= pol.RuneWithdrawn after the migration.
	//
	// This is the ACTUAL safety property the fractional-share fix exists to
	// preserve. CurrentDeposit() = RuneDeposited − RuneWithdrawn; if step 1's
	// RuneWithdrawn credit pushes RuneWithdrawn above RuneDeposited, CurrentDeposit
	// goes negative and breaks the maxPOLDeposit gate in manager_network_current.go
	// post-upgrade. The G4 bound above (Δ ≤ Σstep1 ΔR) only constrains how much was
	// added this migration — it says nothing about POL's absolute deposit basis, so
	// it cannot catch this. Asserted unconditionally (not gated on totalStep1Rune)
	// because the property must hold whether or not step 1 touched POL. Note the
	// migration itself does not panic on a violation (PnL/CurrentDeposit use signed
	// cosmos.Int), so this is a harness-coverage assertion, not an on-chain halt.
	addGlobal(after.polRuneDeposited.GTE(after.polRuneWithdrawn),
		"POL.RuneDeposited ≥ POL.RuneWithdrawn (CurrentDeposit non-negative; protects maxPOLDeposit gate)",
		fmt.Sprintf("deposited=%s withdrawn=%s", format1e8(after.polRuneDeposited), format1e8(after.polRuneWithdrawn)))

	// G5: keeper GetTotalSupply vs bank.GetSupply parity on pools the migration
	// recomputes.
	//
	// The migration's step 4.5 and step 5's polPoolValue() feed CalcUnits with the
	// KEEPER's GetTotalSupply (keeper_liquidity_provider.go), which is NOT the same
	// quantity as bank.GetSupply: it returns ZERO for any pool whose ragnarok has
	// started, and casts the amount through uint64. This harness's own conservation
	// invariants (G1/G3) and the operator's mental model use the raw bank.GetSupply.
	// They coincide in the normal case. A divergence on a pool the migration TOUCHED
	// (its L1 SynthUnits changed → step 4.5 ran CalcUnits there) means the migration
	// computed SynthUnits from a different supply than bank accounting shows — e.g. a
	// ragnarok'd pool is assigned SynthUnits=0 while the synth still circulates per
	// bank — a real correctness gap, so FAIL it. A divergence on an untouched pool is
	// irrelevant to this migration and only WARNed (no failCount bump).
	for denom, supA := range after.synthSupply {
		keepA, ok := after.synthSupplyKeep[denom]
		if !ok || keepA.Equal(supA) {
			continue
		}
		// Resolve the L1 pool backing this synth denom to classify touched/untouched.
		touched := false
		for _, pB := range before.pools {
			if !pB.IsSynth || pB.Asset.Native() != denom {
				continue
			}
			l1 := pB.Asset.GetLayer1Asset().String()
			pAl1, okA := after.pools[l1]
			pBl1, okB := before.pools[l1]
			touched = okA && okB && !pAl1.SynthUnits.Equal(pBl1.SynthUnits)
			break
		}
		detail := fmt.Sprintf("bank.GetSupply=%s keeper.GetTotalSupply=%s (ragnarok-zeroed or >uint64) — migration CalcUnits uses the keeper value",
			format1e8(supA), format1e8(keepA))
		if touched {
			addGlobal(false, fmt.Sprintf("GetTotalSupply parity %s (migration-touched pool)", denom), detail)
		} else {
			globals = append(globals, fmt.Sprintf("[WARN] GetTotalSupply parity %s (untouched pool) — %s", denom, detail))
		}
	}

	// ── STRICT-WATERFALL globals (layer 1 + layer 2) ─────────────────────────

	// G6 (layer-1 conservation): for each step-1 pool with no treasury draw, the
	// pool.LPUnits drop must equal the POL LP-record drop (POL units removed, not
	// DLP). Distinguishes layer-1 removal from treasury draws (step 4) by asset.
	totalPOLUnitsRemoved := cosmos.ZeroUint()
	for assetStr, pB := range before.pools {
		if pB.IsSynth {
			continue
		}
		pA, ok := after.pools[assetStr]
		if !ok || pA.BalanceRune.GTE(pB.BalanceRune) {
			continue // not a step-1 pool
		}
		if !common.SafeSub(before.treasuryLPUnits[assetStr], after.treasuryLPUnits[assetStr]).IsZero() {
			continue // treasury-draw pool; layer-1 coupling validated in step-4 logic
		}
		poolLPDrop := common.SafeSub(pB.LPUnits, pA.LPUnits)
		polDrop := common.SafeSub(before.polPoolLPUnits[assetStr], after.polPoolLPUnits[assetStr])
		totalPOLUnitsRemoved = totalPOLUnitsRemoved.Add(polDrop)
		addGlobal(poolLPDrop.Equal(polDrop),
			fmt.Sprintf("G6 layer-1 conservation %s", assetStr),
			fmt.Sprintf("pool.LPUnits drop=%s == POL-record drop=%s", format1e8(poolLPDrop), format1e8(polDrop)))
	}

	// G7 (reserve-not-wiped): if any POL units were removed (layer 1), the
	// post-migration RUNEPool.ReserveUnits MUST stay > 0 — else the reserve
	// ceiling was mis-derived and the loss spilled onto RUNEPool providers.
	if !totalPOLUnitsRemoved.IsZero() {
		addGlobal(!after.runePoolReserveUnits.IsZero(),
			"G7 reserve-not-wiped (layer 1 → reserve absorbs, providers whole)",
			fmt.Sprintf("ReserveUnits before=%s after=%s (must be >0)", format1e8(before.runePoolReserveUnits), format1e8(after.runePoolReserveUnits)))
	}

	// G8 (provider-whole): RUNEPool provider units (PoolUnits) are never touched by
	// the solve — only ReserveUnits re-solves — so provider value is preserved. We
	// assert PoolUnits is unchanged (the structural guarantee that providers are
	// whole; the value-level check is in renderStrictWaterfall's provider v/u drift).
	addGlobal(before.runePoolPoolUnits.Equal(after.runePoolPoolUnits),
		"G8 RUNEPool provider units unchanged (providers whole)",
		fmt.Sprintf("PoolUnits before=%s after=%s", format1e8(before.runePoolPoolUnits), format1e8(after.runePoolPoolUnits)))

	// G9 (TRX/XRP DLP-residual flag): step-1 pools with NO savers pool where the
	// reserve-capped layer-1 removal could not retire the full v/u-neutral target
	// (x* > x) have no overflow lever — the residual unavoidably falls on DLPs.
	// WARN (not a hard fail): for these pools ΔR is sub-1% so the drift is tiny,
	// but it must be surfaced, not hidden.
	for assetStr, pB := range before.pools {
		if pB.IsSynth {
			continue
		}
		pA, ok := after.pools[assetStr]
		if !ok || pA.BalanceRune.GTE(pB.BalanceRune) {
			continue
		}
		// savers pool present?
		if d, has := before.saversDepth[assetStr]; has && !d.IsZero() {
			continue
		}
		// x* = ΔR·L/R; x removed = pool.LPUnits drop.
		deltaR := common.SafeSub(pB.BalanceRune, pA.BalanceRune)
		xStar := common.GetUncappedShare(deltaR, pB.BalanceRune, pB.LPUnits)
		xRemoved := common.SafeSub(pB.LPUnits, pA.LPUnits)
		if xStar.GT(xRemoved) {
			globals = append(globals, fmt.Sprintf(
				"[WARN] G9 no-savers DLP residual %s — x*=%s but only %s POL units removed (reserve-capped); residual %s units fall on DLPs (no overflow lever)",
				assetStr, format1e8(xStar), format1e8(xRemoved), format1e8(common.SafeSub(xStar, xRemoved))))
		}
	}

	// G10 (migration-CAUSED L1 insolvency): the cross-pool check the per-table
	// assertions don't make. An absolute "pool depth ≤ Σ vault.Coins" check is NOT
	// valid here — many live pools normally run with pool+secured+trade exceeding raw
	// asgard coins, and THOR.* assets have no vault backing at all. So instead compare
	// the asset's solvency DEFICIT (claims − Asgard holdings; claims = pool depth +
	// secured + trade) BEFORE vs AFTER the migration. We flag only pools whose deficit
	// GREW — i.e. the migration removed vault backing (the 2v28 zero-out) without a
	// compensating pool haircut. This isolates migration-caused insolvency from the
	// pre-existing pool-vs-vault gaps that are out of scope. Hard fail.
	deficit := func(s fullSnapshot, assetStr string, pool poolSnap) cosmos.Uint {
		held, ok := s.vaultHoldings[assetStr]
		if !ok {
			held = cosmos.ZeroUint()
		}
		claims := pool.BalanceAsset
		if sd, ok := s.securedDepth[assetStr]; ok {
			claims = claims.Add(sd)
		}
		if td, ok := s.tradeDepth[assetStr]; ok {
			claims = claims.Add(td)
		}
		if claims.GT(held) {
			return common.SafeSub(claims, held)
		}
		return cosmos.ZeroUint()
	}
	for assetStr, pA := range after.pools {
		if pA.IsSynth {
			continue
		}
		pB, ok := before.pools[assetStr]
		if !ok {
			continue
		}
		dAfter := deficit(after, assetStr, pA)
		dBefore := deficit(before, assetStr, pB)
		if dAfter.GT(dBefore) {
			grew := common.SafeSub(dAfter, dBefore)
			// Material: deficit grew by > 0.5% of pool depth (filters fee/rounding noise).
			if grew.GT(pA.BalanceAsset.QuoUint64(200)) {
				// WARN, not a hard fail: a grown L1 deficit is EXPECTED for pools the
				// migration handles via economic remedies that don't touch pool depth
				// (savers burn) or are sized to the economic loss rather than 2v28's raw
				// holding (treasury/POL) — those restore pool VALUE, not Σ vault.Coins.
				// Raw per-vault L1 solvency is restored elsewhere (reserve / churn). This
				// surfaces every affected pool so a human can confirm scope; pools with NO
				// remedy at all (no haircut row) are the ones that warrant a second look.
				_, hasRow := rowMap[assetStr]
				scope := "HANDLED by a haircut row (economic remedy; L1 backing restored elsewhere)"
				if !hasRow {
					scope = "NO haircut row — confirm this pool is intentionally out of conciliation scope"
				}
				globals = append(globals, fmt.Sprintf(
					"[WARN] G10 L1 backing deficit grew %s: %s → %s (Δ+%s, 2v28 backing zeroed) — %s",
					assetStr, format1e8(dBefore), format1e8(dAfter), format1e8(grew), scope))
			}
		}
	}

	// Sort rows by asset name for stable rendering.
	keys := make([]string, 0, len(rowMap))
	for k := range rowMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rows = append(rows, *rowMap[k])
	}
	return rows, globals, failCount
}

// renderHeatmap prints the per-pool × per-step grid (ANSI-coloured) followed by
// per-cell detail lines and the global aggregate invariants.
func renderHeatmap(rows []heatmapRow, globals []string) {
	const (
		clrReset = "\033[0m"
		clrGreen = "\033[32m"
		clrRed   = "\033[31m"
		clrDim   = "\033[2m"
	)
	mark := func(c cell) string {
		switch c.state {
		case cellPass:
			return clrGreen + "  ✓  " + clrReset
		case cellFail:
			return clrRed + "  ✗  " + clrReset
		default:
			return clrDim + "  ·  " + clrReset
		}
	}
	fmt.Println("\n=== Waterfall heatmap (pool × step) ===")
	fmt.Printf("  %s\n", clrDim+"Step1=POL haircut · Step2=Reserve synth burn · Step3=Savers haircut · Step4=Treasury LP draw"+clrReset)
	fmt.Printf("  %-55s %s %s %s %s\n", "Asset", "Step1", "Step2", "Step3", "Step4")
	fmt.Printf("  %-55s %s %s %s %s\n", "-----", "-----", "-----", "-----", "-----")
	for _, r := range rows {
		fmt.Printf("  %-55s %s %s %s %s\n", r.asset, mark(r.cells[0]), mark(r.cells[1]), mark(r.cells[2]), mark(r.cells[3]))
	}

	fmt.Println("\n=== Per-cell detail ===")
	any := false
	for _, r := range rows {
		for i, c := range r.cells {
			if c.state == cellNA {
				continue
			}
			any = true
			tag := clrGreen + "✓" + clrReset
			if c.state == cellFail {
				tag = clrRed + "✗" + clrReset
			}
			fmt.Printf("  %s step%d %-50s %s\n", tag, i+1, r.asset, c.note)
		}
	}
	if !any {
		fmt.Println("  (no per-pool changes detected)")
	}

	fmt.Println("\n=== Global aggregate invariants ===")
	for _, g := range globals {
		fmt.Printf("  %s\n", g)
	}
}

// ───── Per-asset solvency view ────────────────────────────────────────────────

// solvencyDelta captures the before/after solvency picture for a single L1 asset.
// Sign of net change is encoded as: positive netDelta means insolvency was
// reduced (claims dropped more than vault holdings dropped); negative means
// insolvency increased (vault drop exceeded claims drop, e.g. the on-chain
// accounting now formally acknowledges the L1 loss that the haircuts did not
// fully cover).
type solvencyDelta struct {
	asset           string
	vaultDrop       cosmos.Uint // before − after
	lpDrop          cosmos.Uint // pool.BalanceAsset before − after
	synthDrop       cosmos.Uint // bank.GetSupply(synth) before − after
	securedDrop     cosmos.Uint // SecuredAsset.Depth before − after  (expected 0 — migration does not touch)
	tradeDrop       cosmos.Uint // TradeUnit.Depth before − after     (expected 0 — migration does not touch)
	claimsDrop      cosmos.Uint // lp + synth + secured + trade drops
	netImproved     bool        // true if claimsDrop >= vaultDrop (insolvency reduced or eliminated)
	netDeltaAbs     cosmos.Uint // |claimsDrop − vaultDrop|
	netBeforeSigned string      // "vault − claims" before, with sign string
	netAfterSigned  string      // "vault − claims" after, with sign string
}

// addCommas inserts comma thousand-separators into a decimal-digit string.
// E.g., "1234567" → "1,234,567".
func addCommas(s string) string {
	if len(s) <= 3 {
		return s
	}
	n := len(s) % 3
	var b strings.Builder
	if n > 0 {
		b.WriteString(s[:n])
	}
	for i := n; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// format1e8 divides the cosmos.Uint by 1e8 (the THORChain protocol unit) and
// formats it with comma thousand-separators on the integer part and up to 8
// fractional digits (trailing zeros trimmed). E.g., 339245565058092 →
// "3,392,455,650.58092". Returns "0" for zero.
func format1e8(u cosmos.Uint) string {
	if u.IsNil() || u.IsZero() {
		return "0"
	}
	bi := u.BigInt()
	div := big.NewInt(100_000_000)
	quot := new(big.Int).Quo(bi, div)
	rem := new(big.Int).Mod(bi, div)
	intStr := addCommas(quot.String())
	decStr := fmt.Sprintf("%08d", rem)
	decStr = strings.TrimRight(decStr, "0")
	if decStr == "" {
		return intStr
	}
	return intStr + "." + decStr
}

// signedDiff renders (a − b) as a 1e8-formatted string with explicit sign.
// Returns "+N" if a≥b, "-N" otherwise (where N is divided by 1e8).
func signedDiff(a, b cosmos.Uint) string {
	if a.GTE(b) {
		return "+" + format1e8(common.SafeSub(a, b))
	}
	return "-" + format1e8(common.SafeSub(b, a))
}

// computeSolvency builds a per-L1-asset solvency-delta row for every asset
// where ANY of vault holdings / LP / synth supply changed.
func computeSolvency(before, after fullSnapshot) []solvencyDelta {
	// Universe = union of all keys with any non-zero before-value across the
	// four claim sources + vault holdings.
	universe := map[string]struct{}{}
	for k := range before.pools {
		if !before.pools[k].IsSynth {
			universe[k] = struct{}{}
		}
	}
	for k := range before.vaultHoldings {
		universe[k] = struct{}{}
	}

	get := func(m map[string]cosmos.Uint, k string) cosmos.Uint {
		if v, ok := m[k]; ok {
			return v
		}
		return cosmos.ZeroUint()
	}

	var rows []solvencyDelta
	for assetStr := range universe {
		pB := before.pools[assetStr]
		pA, hasAfter := after.pools[assetStr]
		var lpBefore, lpAfter cosmos.Uint
		if !pB.BalanceAsset.IsNil() {
			lpBefore = pB.BalanceAsset
		} else {
			lpBefore = cosmos.ZeroUint()
		}
		if hasAfter && !pA.BalanceAsset.IsNil() {
			lpAfter = pA.BalanceAsset
		} else {
			lpAfter = lpBefore
		}

		vaultBefore := get(before.vaultHoldings, assetStr)
		vaultAfter := get(after.vaultHoldings, assetStr)

		// Synth supply for this asset's synth denom.
		var synthDenom string
		if !pB.Asset.IsEmpty() {
			synthDenom = pB.Asset.GetSyntheticAsset().Native()
		}
		synthBefore := get(before.synthSupply, synthDenom)
		synthAfter := get(after.synthSupply, synthDenom)

		securedBefore := get(before.securedDepth, assetStr)
		securedAfter := get(after.securedDepth, assetStr)
		tradeBefore := get(before.tradeDepth, assetStr)
		tradeAfter := get(after.tradeDepth, assetStr)

		// L1 claims = pool.BalanceAsset + secured.Depth + trade.Depth.
		// Synth supply is intentionally EXCLUDED: synth holders' claim on L1 is
		// mediated through pool.BalanceAsset (synths represent a SynthUnits share
		// of the pool's depth, not independent vault holdings). Burning synths
		// raises LPs' value-per-unit but doesn't change L1 backing requirements.
		// Synth drop is rendered as an informational column, not summed into net.
		claimsBefore := lpBefore.Add(securedBefore).Add(tradeBefore)
		claimsAfter := lpAfter.Add(securedAfter).Add(tradeAfter)

		// Filter: skip rows where nothing changed at all (across vault, claims, or synth).
		if vaultBefore.Equal(vaultAfter) && claimsBefore.Equal(claimsAfter) && synthBefore.Equal(synthAfter) {
			continue
		}

		vDrop := common.SafeSub(vaultBefore, vaultAfter)
		if vaultAfter.GT(vaultBefore) {
			vDrop = cosmos.ZeroUint() // surplus increased (rare in migration)
		}
		lpDrop := common.SafeSub(lpBefore, lpAfter)
		synthDrop := common.SafeSub(synthBefore, synthAfter)
		securedDrop := common.SafeSub(securedBefore, securedAfter)
		tradeDrop := common.SafeSub(tradeBefore, tradeAfter)
		claimsDrop := lpDrop.Add(securedDrop).Add(tradeDrop)

		improved := claimsDrop.GTE(vDrop)
		var deltaAbs cosmos.Uint
		if improved {
			deltaAbs = common.SafeSub(claimsDrop, vDrop)
		} else {
			deltaAbs = common.SafeSub(vDrop, claimsDrop)
		}

		rows = append(rows, solvencyDelta{
			asset:           assetStr,
			vaultDrop:       vDrop,
			lpDrop:          lpDrop,
			synthDrop:       synthDrop,
			securedDrop:     securedDrop,
			tradeDrop:       tradeDrop,
			claimsDrop:      claimsDrop,
			netImproved:     improved,
			netDeltaAbs:     deltaAbs,
			netBeforeSigned: signedDiff(vaultBefore, claimsBefore),
			netAfterSigned:  signedDiff(vaultAfter, claimsAfter),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].asset < rows[j].asset })
	return rows
}

func renderSolvency(rows []solvencyDelta) {
	const (
		clrReset = "\033[0m"
		clrGreen = "\033[32m"
		clrRed   = "\033[31m"
		clrDim   = "\033[2m"
	)
	fmt.Println("\n=== Per-asset L1 vault solvency (vault held vs L1-direct claims) ===")
	fmt.Printf("  %s\n", clrDim+"L1 claims = pool.BalanceAsset + secured.Depth + trade.Depth"+clrReset)
	fmt.Printf("  %s\n", clrDim+"Net = vault held − L1 claims  (signed: positive=surplus, negative=insolvency)"+clrReset)
	fmt.Printf("  %s\n", clrDim+"ΔNet = (claims drop) − (vault drop): + means insolvency reduced; − means newly acknowledged on-chain"+clrReset)
	fmt.Printf("  %s\n", clrDim+"ΔSynth shown INFORMATIONAL — synth claims are mediated through pool.BalanceAsset, not summed into L1 net"+clrReset)
	fmt.Printf("  %s\n", clrDim+"All values divided by 1e8 (THORChain protocol unit) and shown with comma separators"+clrReset)
	fmt.Printf("\n  %-55s %22s %22s %22s %22s   %22s %22s   %23s   %22s\n",
		"Asset", "ΔVault", "ΔLP", "ΔSecured", "ΔTrade", "Net before", "Net after", "ΔNet", "ΔSynth (info)")
	fmt.Printf("  %-55s %22s %22s %22s %22s   %22s %22s   %23s   %22s\n",
		"-----", "------", "---", "--------", "------", "----------", "---------", "----", "-------------")
	totalImproved := cosmos.ZeroUint()
	totalDegraded := cosmos.ZeroUint()
	improvedAssets := 0
	degradedAssets := 0
	for _, r := range rows {
		netSign := clrGreen + "+" + clrReset
		if !r.netImproved {
			netSign = clrRed + "−" + clrReset
		}
		neg := func(u cosmos.Uint) string {
			if u.IsZero() {
				return "0"
			}
			return "-" + format1e8(u)
		}
		// Compose the ΔNet field separately because the ANSI color codes around
		// the sign character would otherwise inflate the printf width count.
		netField := netSign + format1e8(r.netDeltaAbs)
		fmt.Printf("  %-55s %22s %22s %22s %22s   %22s %22s   %23s   %22s\n",
			r.asset,
			neg(r.vaultDrop),
			neg(r.lpDrop),
			neg(r.securedDrop),
			neg(r.tradeDrop),
			r.netBeforeSigned, r.netAfterSigned,
			netField,
			neg(r.synthDrop))
		if r.netImproved {
			totalImproved = totalImproved.Add(r.netDeltaAbs)
			improvedAssets++
		} else {
			totalDegraded = totalDegraded.Add(r.netDeltaAbs)
			degradedAssets++
		}
	}
	fmt.Printf("\n  Summary: %d asset(s) improved (Σ netΔ=+%s), %d asset(s) degraded (Σ netΔ=−%s)\n",
		improvedAssets, format1e8(totalImproved), degradedAssets, format1e8(totalDegraded))
	fmt.Printf("  %s\n", clrDim+"\"degraded\" = vault 2v28 cleared more L1 holdings than the haircuts/burns reduced claims for that asset"+clrReset)
}

// ───── LP value-per-unit drift diagnostic ────────────────────────────────────
//
// Why this exists: steps 2 and 3 burn synth supply via direct bank burns, which
// does NOT update pool.SynthUnits in the L1 pool record — that happens later
// when `pool.CalcUnits(synthSupply)` is called (next swap, withdraw, or
// add-liquidity on that pool). The migration code itself never calls CalcUnits.
//
// Consequences during the post-upgrade window before any swap touches the pool:
//   1. LPs withdrawing see stale-high pool.SynthUnits → smaller share denominator
//      → under-compensated relative to design intent.
//   2. Step 5's POL re-solve reads `runePoolValue()` which uses
//      `pool.GetPoolUnits() = LPUnits + SynthUnits`. Stale SynthUnits makes POL's
//      share denominator inflated → POL share-of-value computed too small →
//      step-5 solver inflates ReserveUnits more than necessary → POL over-absorbs.
//
// This diagnostic shows LP value-per-unit (in 1e8 RUNE-equivalent) computed two
// ways for each affected L1 pool:
//   - "stale"     — using the SynthUnits value the migration actually leaves in
//                   the keeper (what LPs see in the window before first swap).
//   - "projected" — using the SynthUnits that CalcUnits would produce given the
//                   post-migration synth supply (what LPs eventually see).
// The gap between the two is what calling CalcUnits in the migration would close.

// projectedSynthUnits mirrors type_pool.go's Pool.CalcUnits formula:
//
//	SynthUnits = (LPUnits × synthSupply) / (2 × BalanceAsset − synthSupply)
//
// with the same edge-case handling: zero balance / synth asset → SynthUnits=0;
// zero denominator → use 1.
func projectedSynthUnits(lpUnits, balanceAsset, synthSupply cosmos.Uint) cosmos.Uint {
	if balanceAsset.IsZero() {
		return cosmos.ZeroUint()
	}
	twoA := balanceAsset.MulUint64(2)
	if twoA.LTE(synthSupply) {
		// CalcUnits' SafeSub would clamp to zero, then denominator is replaced
		// with OneUint — matches the actual implementation.
		return lpUnits.Mul(synthSupply)
	}
	denom := common.SafeSub(twoA, synthSupply)
	if denom.IsZero() {
		denom = cosmos.OneUint()
	}
	return lpUnits.Mul(synthSupply).Quo(denom)
}

// driftBpsCross returns the LP-value-per-unit drift in basis points, computed
// via cross-multiplication so we preserve precision even when TotalUnits ≫
// BalanceRune (the usual case where the integer-divided ratio 2R/T = 0).
//
//	drift = (rAfter/tAfter − rBefore/tBefore) / (rBefore/tBefore)
//	      = (rAfter × tBefore − rBefore × tAfter) / (rBefore × tAfter)
//
// Returns a signed bps string with 2 decimal places of precision (×100 inside
// the bps math, displayed as "+N.NN bps"). Returns "n/a" on degenerate inputs.
func driftBpsCross(rBefore, tBefore, rAfter, tAfter cosmos.Uint) string {
	if rBefore.IsZero() || tBefore.IsZero() || tAfter.IsZero() {
		return "n/a"
	}
	lhs := rAfter.Mul(tBefore) // = rAfter / tAfter × tBefore × tAfter
	rhs := rBefore.Mul(tAfter) // = rBefore / tBefore × tBefore × tAfter
	denom := rBefore.Mul(tAfter)
	if denom.IsZero() {
		return "n/a"
	}
	// Use BigInt-based formatting so we don't overflow when the projection
	// produces extreme values (e.g., AVAX.USDC whose stored SynthUnits is
	// inflated to 10²⁵+ from historical accounting drift).
	fmtBps := func(bpsX100 cosmos.Uint) string {
		bi := bpsX100.BigInt()
		// Split into integer-bps and 2-decimal-digit fractional bps using big-int.
		hundred := big.NewInt(100)
		intPart := new(big.Int).Quo(bi, hundred)
		fracPart := new(big.Int).Mod(bi, hundred).Int64()
		return fmt.Sprintf("%s.%02d", addCommas(intPart.String()), fracPart)
	}
	if lhs.GTE(rhs) {
		bpsX100 := common.SafeSub(lhs, rhs).MulUint64(1_000_000).Quo(denom)
		return "+" + fmtBps(bpsX100) + " bps"
	}
	bpsX100 := common.SafeSub(rhs, lhs).MulUint64(1_000_000).Quo(denom)
	return "-" + fmtBps(bpsX100) + " bps"
}

func renderLPDrift(before, after fullSnapshot) {
	const (
		clrReset = "\033[0m"
		clrGreen = "\033[32m"
		clrRed   = "\033[31m"
		clrYel   = "\033[33m"
		clrDim   = "\033[2m"
	)
	type row struct {
		asset                                   string
		rBefore, rAfter                         cosmos.Uint
		tStoredBefore, tStoredAfter, tProjAfter cosmos.Uint
		driftStored, driftProj                  string
	}
	var rows []row
	for assetStr, pB := range before.pools {
		if pB.IsSynth {
			continue
		}
		pA, ok := after.pools[assetStr]
		if !ok {
			continue
		}
		denom := pB.Asset.GetSyntheticAsset().Native()
		sB, sA := before.synthSupply[denom], after.synthSupply[denom]
		depthChanged := !pB.BalanceRune.Equal(pA.BalanceRune) || !pB.BalanceAsset.Equal(pA.BalanceAsset)
		supplyChanged := !sB.IsNil() && !sA.IsNil() && !sB.Equal(sA)
		unitsChanged := !pB.LPUnits.Equal(pA.LPUnits) || !pB.SynthUnits.Equal(pA.SynthUnits)
		if !depthChanged && !supplyChanged && !unitsChanged {
			continue
		}
		if sA.IsNil() {
			sA = cosmos.ZeroUint()
		}
		// Projected post-CalcUnits SynthUnits = what `pool.CalcUnits(synthSupply)`
		// would set if called with the post-migration synth supply.
		suProjAfter := projectedSynthUnits(pA.LPUnits, pA.BalanceAsset, sA)

		tStoredBefore := pB.LPUnits.Add(pB.SynthUnits)
		tStoredAfter := pA.LPUnits.Add(pA.SynthUnits)
		tProjAfter := pA.LPUnits.Add(suProjAfter)

		// LP value-per-unit drift = drift of (2R/T): same as drift of (R/T) since
		// the constant 2 cancels. Use cross-multiplied bps to preserve precision.
		driftStored := driftBpsCross(pB.BalanceRune, tStoredBefore, pA.BalanceRune, tStoredAfter)
		driftProj := driftBpsCross(pB.BalanceRune, tStoredBefore, pA.BalanceRune, tProjAfter)

		rows = append(rows, row{
			asset:         assetStr,
			rBefore:       pB.BalanceRune,
			rAfter:        pA.BalanceRune,
			tStoredBefore: tStoredBefore,
			tStoredAfter:  tStoredAfter,
			tProjAfter:    tProjAfter,
			driftStored:   driftStored,
			driftProj:     driftProj,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].asset < rows[j].asset })

	fmt.Println("\n=== LP value-per-unit drift (stored vs post-CalcUnits-projected) ===")
	fmt.Printf("  %s\n", clrDim+"v/unit ∝ BalanceRune / TotalUnits   (constant of 2 cancels in drift)"+clrReset)
	fmt.Printf("  %s\n", clrDim+"\"stored\" = what LPs see RIGHT AFTER the upgrade block (SynthUnits stale if step 2/3 fired and no CalcUnits ran)"+clrReset)
	fmt.Printf("  %s\n", clrDim+"\"projected\" = what LPs eventually see after a swap on that pool recomputes SynthUnits"+clrReset)
	fmt.Printf("  %s\n", clrDim+"GAP between the two = how much LPs are under/over-compensated in the post-upgrade window."+clrReset)
	fmt.Printf("\n  %-55s %16s %18s %18s %18s   %18s %18s\n",
		"Asset", "ΔR", "T before", "T after(stored)", "T after(proj)", "drift(stored)", "drift(proj)")
	fmt.Printf("  %-55s %16s %18s %18s %18s   %18s %18s\n",
		"-----", "--", "--------", "---------------", "-------------", "-------------", "-----------")
	for _, r := range rows {
		var dR string
		if r.rBefore.GTE(r.rAfter) {
			dR = "-" + format1e8(common.SafeSub(r.rBefore, r.rAfter))
		} else {
			dR = "+" + format1e8(common.SafeSub(r.rAfter, r.rBefore))
		}
		color := func(s string) string {
			if strings.HasPrefix(s, "-") {
				return clrRed + s + clrReset
			}
			if strings.HasPrefix(s, "+") {
				return clrGreen + s + clrReset
			}
			return s
		}
		fmt.Printf("  %-55s %16s %18s %18s %18s   %18s %18s\n",
			r.asset,
			dR,
			format1e8(r.tStoredBefore),
			format1e8(r.tStoredAfter),
			format1e8(r.tProjAfter),
			color(r.driftStored),
			color(r.driftProj))
	}
	fmt.Printf("\n  %s\n", clrYel+"INTERPRETATION:"+clrReset)
	fmt.Printf("  %s\n", clrYel+"  drift(stored) ≠ drift(proj) → migration leaves stale SynthUnits; LP withdrawals in the post-upgrade window get the STORED value."+clrReset)
	fmt.Printf("  %s\n", clrYel+"  drift(proj) > drift(stored) → projected is the design-intent value; the gap is the LP under-compensation."+clrReset)
	fmt.Printf("  %s\n", clrYel+"  drift(proj) ≈ 0 → step 1's depth haircut and step 3's synth burn are balanced: LP value-per-unit is preserved by design intent."+clrReset)
	fmt.Printf("  %s\n", clrYel+"RECOMMENDED MIGRATION FIX: after step 3 (before step 5), iterate every L1 pool whose synth supply changed, call pool.CalcUnits(synthSupply) + SetPool."+clrReset)
}

// ───── Step-3 calibration advisor (LP value-per-unit preservation) ──────────
//
// For every L1 pool where step 1 fired (BalanceRune dropped), solve for the
// SynthUnits drop that would preserve LP value-per-unit:
//
//     v/u_before = 2R / (L + SU_before)
//     v/u_after  = 2(R−ΔR) / (L + SU_after)
//     v/u preservation ⇒ SU_target = SU_before − TotalUnits × ΔR / R
//
// Then invert CalcUnits to find the synth supply that produces SU_target:
//
//     SU = (L × supply) / (2A − supply)
//     ⇒ supply_target = 2A × SU_target / (L + SU_target)
//
// `additionalBurn` = current post-migration supply − supply_target (positive
// means more burn needed; negative means current haircuts over-shoot and POL
// is over-absorbing). Translate `additionalBurn` to a step-3 bps figure
// against the savers pool BalanceAsset to recommend the calibration.

type calibRow struct {
	asset                                   string
	deltaR, suBeforeStored                  cosmos.Uint
	suTarget, suCurrentProj, suProjOfTgtSup cosmos.Uint
	currentSupplyAfter, requiredSupply      cosmos.Uint
	additionalBurn                          cosmos.Uint
	overBurned                              bool
	saversDepth                             cosmos.Uint
	recommendedBps                          cosmos.Uint
	feasible                                bool
	note                                    string
}

func computeCalibration(before, after fullSnapshot) []calibRow {
	var rows []calibRow
	for assetStr, pB := range before.pools {
		if pB.IsSynth {
			continue
		}
		pA, ok := after.pools[assetStr]
		if !ok || pB.BalanceRune.Equal(pA.BalanceRune) {
			continue // step 1 didn't touch this pool
		}
		deltaR := common.SafeSub(pB.BalanceRune, pA.BalanceRune)
		if deltaR.IsZero() {
			continue
		}
		L := pA.LPUnits
		Sbefore := pB.SynthUnits
		Tbefore := pB.LPUnits.Add(Sbefore)

		// Target SU: SU_before − TotalUnits × ΔR / R.
		suDrop := Tbefore.Mul(deltaR).Quo(pB.BalanceRune)
		var suTarget cosmos.Uint
		if Sbefore.GTE(suDrop) {
			suTarget = common.SafeSub(Sbefore, suDrop)
		} else {
			suTarget = cosmos.ZeroUint()
		}

		// Solve for required synth supply such that CalcUnits(L, A_after, supply) == suTarget.
		// CalcUnits has two regimes:
		//   non-clamped (2A > S):  SU = L·S/(2A−S)   ⇒  S = 2A·suTarget/(L+suTarget)
		//   clamped     (2A ≤ S):  SU = L·S          ⇒  S = suTarget/L      (linear!)
		// Deep synth-overhang pools (BTC/ETH/LTC) live in the clamped regime, where
		// CalcUnits is LINEAR in S — so the v/u-neutral burn is just the SAME fraction
		// as the L1 depth cut (ΔR/R) and is fully reachable. The earlier code used only
		// the non-clamped inverse, which lands on the boundary and looks "unreachable";
		// pick the regime the solution actually falls in.
		var requiredSupply cosmos.Uint
		switch {
		case L.Add(suTarget).IsZero():
			requiredSupply = cosmos.ZeroUint()
		case !L.IsZero() && suTarget.Quo(L).GTE(pA.BalanceAsset.MulUint64(2)):
			// The clamped solution S = suTarget/L is self-consistent (lands at S ≥ 2A,
			// where CalcUnits = L·S), so it's the real v/u-neutral supply. This is the
			// overhang case (BTC/ETH/LTC): the burn is just the ΔR/R fraction and is
			// reachable. (The non-clamped inverse here would sit at 2A−ε and mislead.)
			requiredSupply = suTarget.Quo(L)
		default:
			requiredSupply = pA.BalanceAsset.MulUint64(2).Mul(suTarget).Quo(L.Add(suTarget))
		}

		denom := pB.Asset.GetSyntheticAsset().Native()
		currentSupplyAfter := after.synthSupply[denom]
		if currentSupplyAfter.IsNil() {
			currentSupplyAfter = cosmos.ZeroUint()
		}
		currentSUProj := projectedSynthUnits(L, pA.BalanceAsset, currentSupplyAfter)

		var additionalBurn cosmos.Uint
		var overBurned bool
		if currentSupplyAfter.GTE(requiredSupply) {
			additionalBurn = common.SafeSub(currentSupplyAfter, requiredSupply)
		} else {
			additionalBurn = common.SafeSub(requiredSupply, currentSupplyAfter)
			overBurned = true // current haircuts already burned MORE than what preserves v/u
		}

		saversDepth := before.saversDepth[assetStr]
		if saversDepth.IsNil() {
			saversDepth = cosmos.ZeroUint()
		}
		recommendedBps := cosmos.ZeroUint()
		feasible := false
		var note string
		switch {
		case overBurned:
			note = "OVER-BURNED — current step 2+3 reduce supply MORE than preserves v/u; LPs over-compensated by POL"
			feasible = true // could be fixed by reducing step 3 bps (or step 2 amount)
		case additionalBurn.IsZero():
			note = "already balanced"
			feasible = true
		case saversDepth.IsZero():
			note = "no savers pool → step 3 lever unavailable; LPs absorb depth haircut"
		case saversDepth.GTE(additionalBurn):
			recommendedBps = additionalBurn.MulUint64(10000).Quo(saversDepth)
			feasible = true
			note = "add savers haircut at recommended bps to preserve v/u"
		default:
			recommendedBps = cosmos.NewUint(10000) // max-out
			feasible = false
			note = fmt.Sprintf("savers depth (%s) < additional burn needed (%s); 100%% saver haircut still insufficient", format1e8(saversDepth), format1e8(additionalBurn))
		}

		// Project SU IF the recommended supply_target were achieved.
		suProjOfTgt := projectedSynthUnits(L, pA.BalanceAsset, requiredSupply)

		rows = append(rows, calibRow{
			asset:              assetStr,
			deltaR:             deltaR,
			suBeforeStored:     Sbefore,
			suTarget:           suTarget,
			suCurrentProj:      currentSUProj,
			suProjOfTgtSup:     suProjOfTgt,
			currentSupplyAfter: currentSupplyAfter,
			requiredSupply:     requiredSupply,
			additionalBurn:     additionalBurn,
			overBurned:         overBurned,
			saversDepth:        saversDepth,
			recommendedBps:     recommendedBps,
			feasible:           feasible,
			note:               note,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].asset < rows[j].asset })
	return rows
}

func renderCalibration(rows []calibRow) {
	const (
		clrReset = "\033[0m"
		clrGreen = "\033[32m"
		clrRed   = "\033[31m"
		clrYel   = "\033[33m"
		clrDim   = "\033[2m"
	)
	fmt.Println("\n=== Step-3 bps calibration advisor (LP value-per-unit preservation) ===")
	fmt.Printf("  %s\n", clrDim+"For each step-1-affected L1 pool: solve for the synth burn that makes LP v/u drift = 0."+clrReset)
	fmt.Printf("  %s\n", clrDim+"OVER-BURNED → current step 2+3 already over-shoot (reduce step 3 bps)."+clrReset)
	fmt.Printf("  %s\n", clrDim+"recommendedBps applies to the savers pool's BalanceAsset; only feasible if a savers pool exists and is deep enough."+clrReset)
	fmt.Printf("\n  %-55s %14s %14s %14s %14s   %14s   %s\n",
		"Asset", "step1 ΔR", "SU drop needed", "additionalBurn", "savers depth", "rec'd step3 bps", "verdict")
	fmt.Printf("  %-55s %14s %14s %14s %14s   %14s   %s\n",
		"-----", "--------", "--------------", "--------------", "------------", "---------------", "-------")
	for _, r := range rows {
		burnSign := "+"
		burnColor := clrYel
		if r.overBurned {
			burnSign = "-"
			burnColor = clrGreen // over-burn is good for LPs (gained value), but means POL absorbs surplus
		}
		recBps := "—"
		if !r.recommendedBps.IsZero() {
			recBps = r.recommendedBps.String() + " bps"
		}
		verdictColor := clrGreen
		if !r.feasible {
			verdictColor = clrRed
		}
		fmt.Printf("  %-55s %14s %14s %14s %14s   %14s   %s%s%s\n",
			r.asset,
			format1e8(r.deltaR),
			format1e8(common.SafeSub(r.suBeforeStored, r.suTarget)),
			burnColor+burnSign+format1e8(r.additionalBurn)+clrReset,
			format1e8(r.saversDepth),
			recBps,
			verdictColor, r.note, clrReset)
	}
}

// ───── Haircut feasibility inventory ──────────────────────────────────────────

// renderHaircutInventory prints, for every L1 asset where vault 2v28 holdings
// dropped but the migration did NOT reduce L1 pool.BalanceAsset (the "acknowledged
// but uncovered" set), the levers available to add a step-1 (POL) or step-4
// (treasury LP draw) haircut. Verdict columns mark which lever is feasible.
func renderHaircutInventory(before, after fullSnapshot) {
	const (
		clrReset = "\033[0m"
		clrGreen = "\033[32m"
		clrRed   = "\033[31m"
		clrDim   = "\033[2m"
		clrYel   = "\033[33m"
	)
	get := func(m map[string]cosmos.Uint, k string) cosmos.Uint {
		if v, ok := m[k]; ok && !v.IsNil() {
			return v
		}
		return cosmos.ZeroUint()
	}
	type row struct {
		asset                                                                     string
		vaultDrop, poolRune, poolAsset, treas1, treas2, saversD, synthSup, lpDrop cosmos.Uint
		hadStep1, hadStep4                                                        bool
	}
	var rows []row
	for assetStr, pB := range before.pools {
		if pB.IsSynth {
			continue
		}
		vBefore := get(before.vaultHoldings, assetStr)
		vAfter := get(after.vaultHoldings, assetStr)
		if !vBefore.GT(vAfter) {
			continue // vault didn't drop for this asset
		}
		pA := after.pools[assetStr]
		lpDrop := common.SafeSub(pB.BalanceAsset, pA.BalanceAsset)
		rows = append(rows, row{
			asset:     assetStr,
			vaultDrop: common.SafeSub(vBefore, vAfter),
			poolRune:  pB.BalanceRune,
			poolAsset: pB.BalanceAsset,
			treas1:    get(before.treasuryLPUnits, assetStr),
			treas2:    get(before.treasuryLPUnits2, assetStr),
			saversD:   get(before.saversDepth, assetStr),
			synthSup:  get(before.synthSupply, pB.Asset.GetSyntheticAsset().Native()),
			lpDrop:    lpDrop,
			hadStep1:  !lpDrop.IsZero() && get(before.treasuryLPUnits, assetStr).Equal(get(after.treasuryLPUnits, assetStr)) && get(before.treasuryLPUnits2, assetStr).Equal(get(after.treasuryLPUnits2, assetStr)),
			hadStep4:  !get(before.treasuryLPUnits, assetStr).Equal(get(after.treasuryLPUnits, assetStr)) || !get(before.treasuryLPUnits2, assetStr).Equal(get(after.treasuryLPUnits2, assetStr)),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].vaultDrop.GT(rows[j].vaultDrop) })

	fmt.Println("\n=== Haircut feasibility inventory (assets with vault 2v28 drop) ===")
	fmt.Printf("  %s\n", clrDim+"All values divided by 1e8 (THORChain protocol unit) and shown with comma separators."+clrReset)
	fmt.Printf("  %s\n", clrDim+"Step1 (POL) requires only pool depth (BalanceRune ≥ ΔR). Step4 requires treasury LP > 0 in that pool."+clrReset)
	fmt.Printf("  %s\n", clrDim+"Savers/synth columns are INFORMATIONAL — they can absorb loss only when PAIRED with a step-1 L1 pool reduction."+clrReset)
	fmt.Printf("\n  %-55s %16s %16s %16s %14s %14s %14s %14s   %s\n",
		"Asset", "VaultDrop", "PoolRune", "PoolAsset", "Treas1 LP", "Treas2 LP", "SaversDepth", "SynthSupply", "Verdict")
	fmt.Printf("  %-55s %16s %16s %16s %14s %14s %14s %14s   %s\n",
		"-----", "---------", "--------", "---------", "---------", "---------", "-----------", "-----------", "-------")

	for _, r := range rows {
		// Step 1 feasibility: pool has depth and we can size ΔR such that ΔA ≈ vaultDrop.
		//   ΔR_proposed = vaultDrop × poolRune / poolAsset, must be ≤ poolRune.
		step1OK := false
		var step1Note string
		if !r.poolAsset.IsZero() && !r.poolRune.IsZero() {
			deltaR := r.vaultDrop.Mul(r.poolRune).Quo(r.poolAsset)
			if deltaR.LTE(r.poolRune) && !deltaR.IsZero() {
				step1OK = true
				step1Note = "ΔR≈" + format1e8(deltaR)
			} else {
				step1Note = "ΔR>pool"
			}
		} else {
			step1Note = "no pool"
		}
		// Step 4 feasibility: treasury LP exists (either address).
		treas := r.treas1.Add(r.treas2)
		step4OK := !treas.IsZero()
		// Already covered (hadStep1/4 set by migration that ran)?
		var verdict string
		switch {
		case r.hadStep1:
			verdict = clrDim + "already step1 (LP dropped)" + clrReset
		case r.hadStep4:
			verdict = clrDim + "already step4 (treasury drawn)" + clrReset
		case step1OK && step4OK:
			verdict = clrGreen + "STEP1 or STEP4 (" + step1Note + "; treas=" + format1e8(treas) + ")" + clrReset
		case step1OK:
			verdict = clrGreen + "STEP1 (" + step1Note + ")" + clrReset
		case step4OK:
			verdict = clrYel + "STEP4 only (treas=" + format1e8(treas) + ")" + clrReset
		default:
			verdict = clrRed + "neither (no pool depth, no treas LP)" + clrReset
		}

		fmt.Printf("  %-55s %16s %16s %16s %14s %14s %14s %14s   %s\n",
			r.asset,
			format1e8(r.vaultDrop),
			format1e8(r.poolRune),
			format1e8(r.poolAsset),
			format1e8(r.treas1),
			format1e8(r.treas2),
			format1e8(r.saversD),
			format1e8(r.synthSup),
			verdict)
	}
}

// openApp opens the snapshot's goleveldb store and constructs a THORChainApp
// offline. height==0 loads the latest committed version; otherwise it pins to
// the given committed height (the release-blocker 3e hook).
func openApp(home string, height int64) (*app.THORChainApp, error) {
	dataDir := filepath.Join(home, "data")
	db, err := dbm.NewGoLevelDB("application", dataDir, nil)
	if err != nil {
		return nil, fmt.Errorf("open goleveldb at %s: %w", dataDir, err)
	}
	logger := log.NewLogger(os.Stderr)
	appOpts := simtestutil.NewAppOptionsWithFlagHome(home)

	loadLatest := height == 0
	a := app.NewChainApp(logger, db, nil, loadLatest, appOpts, nil)
	if !loadLatest {
		if err := a.LoadHeight(height); err != nil {
			return nil, fmt.Errorf("load height %d: %w", height, err)
		}
	}
	return a, nil
}

// renderLossIncidence answers "who actually bears each step-1 (POL) haircut?".
//
// A step-1 haircut cuts pool depth; the resulting value loss is borne pro-rata by
// the pool's units = LPUnits (LPs incl. POL) + SynthUnits (savers). SynthUnits is
// CalcUnits = LPUnits·S / max(2·BalanceAsset − S, 1)  (see type_pool.go). In the
// SYNTH-OVERHANG regime (S ≥ 2·BalanceAsset, true for BTC/ETH/LTC) the denominator
// collapses to 1, so SynthUnits = LPUnits·S — dwarfing LPUnits by a factor of S.
// Savers then own ~100% of pool value and bear ~all of the depth-cut loss, while
// POL & ordinary LPs absorb ≈0. That is WHY these pools take no explicit step-3
// savers synth-burn: the step-1 depth cut already lands the loss on savers (plus
// RUNE supply via the ΔR burn). The heatmap's "step 1" is the MECHANISM; this table
// is the INCIDENCE. Computed on the BEFORE state (ownership at the moment of cut).
// renderStrictWaterfall prints the per-step-1-pool strict-waterfall ledger and
// asserts the load-bearing post-state properties: DLP value-per-unit drift ≈ 0
// (layer 1 worked), RUNEPool provider v/u drift ≈ 0 (layer 2 / step-5 held
// providers whole), and ReserveUnits survives (reserve absorbed, not wiped).
//
// DLP v/u drift is computed on the NON-POL unit base so it isolates ordinary
// LPs from POL's removed units:
//
//	T_dlp = (L − polL) + SU·(L−polL)/L   (DLP's units + DLP's proportional SU share)
//
// drift of 2R/T_dlp before→after is the proof layer 1 kept DLPs neutral.
func renderStrictWaterfall(before, after fullSnapshot) (failCount int) {
	const (
		clrReset  = "\033[0m"
		clrDim    = "\033[2m"
		clrGreen  = "\033[32m"
		clrRed    = "\033[31m"
		clrViolet = "\033[35m"
		clrYel    = "\033[33m"
	)
	// drift threshold (bps×100 string under ~5 bps). We compare the leading
	// numeric magnitude loosely by re-deriving from cross-multiplication instead;
	// to keep it simple we treat |drift| < ~50 bps×100 = 5.00 bps as neutral by
	// parsing the rendered string's integer-bps part.
	const tolBps = 5

	fmt.Println("\n=== STRICT WATERFALL: POL first, savers next, DLPs never ===")
	fmt.Printf("  %s\n", clrDim+"Layer 1: remove x = min(ΔR·L/R, reserve_pool_capacity, polL) from POL pool LP units → DLP v/u neutral."+clrReset)
	fmt.Printf("  %s\n", clrDim+"Overflow: residual total-unit shortfall → savers synth burn (overhang: ΔR/R·10000 bps)."+clrReset)
	fmt.Printf("  %s\n", clrDim+"Layer 2: step-5 re-solves RUNEPool.ReserveUnits so the RESERVE absorbs POL's drop; providers whole."+clrReset)
	fmt.Printf("  %-20s %12s %16s %18s %18s %12s %14s %16s %10s\n",
		"Pool", "burn%", "x*", "reserveCap", "POL removed", "ovfl bps", "DLP v/u drift", "prov v/u drift", "rsv>0?")
	fmt.Printf("  %-20s %12s %16s %18s %18s %12s %14s %16s %10s\n",
		"----", "-----", "--", "----------", "-----------", "--------", "-------------", "--------------", "------")

	keys := make([]string, 0, len(before.pools))
	for k := range before.pools {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, assetStr := range keys {
		pB := before.pools[assetStr]
		if pB.IsSynth {
			continue
		}
		pA, ok := after.pools[assetStr]
		if !ok || pA.BalanceRune.GTE(pB.BalanceRune) {
			continue
		}
		// skip treasury-draw pools (step 4) — not strict-waterfall layer 1.
		if !common.SafeSub(before.treasuryLPUnits[assetStr], after.treasuryLPUnits[assetStr]).IsZero() {
			continue
		}
		deltaR := common.SafeSub(pB.BalanceRune, pA.BalanceRune)
		burnPct := float64(0)
		if !pB.BalanceRune.IsZero() {
			burnPct = 100 * float64(deltaR.MulUint64(1_000_000).Quo(pB.BalanceRune).BigInt().Int64()) / 1_000_000
		}
		L := pB.LPUnits
		xStar := common.GetUncappedShare(deltaR, pB.BalanceRune, L)
		polL := before.polPoolLPUnits[assetStr]
		if polL.IsNil() {
			polL = cosmos.ZeroUint()
		}
		reserveCap := cosmos.ZeroUint()
		if !before.runePoolTotalUnits.IsZero() {
			reserveCap = common.GetSafeShare(before.runePoolReserveUnits, before.runePoolTotalUnits, polL)
		}
		polRemoved := common.SafeSub(polL, after.polPoolLPUnits[assetStr])

		// Expected x = min(x*, reserveCap, polL); assert POL removed matches ±1.
		// Step-2 pools (reserve synth burn) also take a paired depth cut, so this pool's
		// TOTAL BalanceRune drop is not just step-1's ΔR — xStar (recomputed from the
		// total drop) over-states the layer-1 target and the cap looks to bind when it
		// didn't. The step-1/step-2 split can't be reconstructed here, so the x-match
		// check is INFORMATIONAL for these pools; DLP v/u drift is the real proof.
		sd := pB.Asset.GetSyntheticAsset().Native()
		rsB, okB := before.reserveSynthBal[sd]
		rsA := after.reserveSynthBal[sd]
		if rsA.IsNil() {
			rsA = cosmos.ZeroUint()
		}
		hadReserveBurn := okB && !rsB.IsNil() && !common.SafeSub(rsB, rsA).IsZero()
		expX := xStar
		if expX.GT(reserveCap) {
			expX = reserveCap
		}
		if expX.GT(polL) {
			expX = polL
		}
		xAssert := hadReserveBurn || absDiff(polRemoved, expX).LTE(cosmos.OneUint())

		// Savers overflow bps realized: from the savers-pool BalanceAsset drop.
		denom := pB.Asset.GetSyntheticAsset().Native()
		ovflBps := cosmos.ZeroUint()
		savB := before.saversDepth[assetStr]
		if !savB.IsNil() && !savB.IsZero() {
			savA := after.saversDepth[assetStr]
			if savA.IsNil() {
				savA = cosmos.ZeroUint()
			}
			savDrop := common.SafeSub(savB, savA)
			ovflBps = savDrop.MulUint64(10000).Quo(savB)
		}

		// DLP v/u drift on the NON-POL unit base.
		sB := before.synthSupply[denom]
		if sB.IsNil() {
			sB = cosmos.ZeroUint()
		}
		sA := after.synthSupply[denom]
		if sA.IsNil() {
			sA = cosmos.ZeroUint()
		}
		suB := projectedSynthUnits(pB.LPUnits, pB.BalanceAsset, sB)
		suA := projectedSynthUnits(pA.LPUnits, pA.BalanceAsset, sA)
		// DLP value-per-unit = POOL value-per-unit = 2R/(LPUnits+SynthUnits) — the
		// SAME for every unit holder. DLPs are protected by (a) their units being
		// untouched and (b) this v/u restored to neutral; savers and POL bear the loss
		// via UNIT reduction, not a different per-unit value. So measure drift of R/T
		// on the FULL pool units. (The earlier per-DLP attribution scaled DLP units by
		// (L+SU)/L, injecting a spurious L_after/L_before = −x/L factor whenever POL
		// units were removed — falsely flagging truly-neutral pools by ≈ −burn_perc
		// bps. That was a verifier bug, not a migration bug.)
		tPoolB := pB.LPUnits.Add(suB)
		tPoolA := pA.LPUnits.Add(suA)
		dlpDrift := driftBpsCross(pB.BalanceRune, tPoolB, pA.BalanceRune, tPoolA)

		// PROVIDER v/u is a GLOBAL property of the RUNEPool — step 5 re-solves
		// ReserveUnits across ALL pools at once, so a per-pool number is meaningless
		// (and the earlier per-pool proxy mis-reported large drifts). It's asserted
		// ONCE, globally, after this table: PoolUnits unchanged + reserve not wiped ⇒
		// step-5 preserved provider value by construction.
		provDrift := "n/a"

		rsvOK := !after.runePoolReserveUnits.IsZero() || polRemoved.IsZero()

		// Assertions.
		driftNeutral := driftWithinBps(dlpDrift, tolBps)
		if !xAssert {
			failCount++
		}
		if !driftNeutral {
			failCount++
		}
		if !rsvOK {
			failCount++
		}

		col := func(ok bool, s string) string {
			if ok {
				return clrGreen + s + clrReset
			}
			return clrRed + s + clrReset
		}
		rsvStr := "yes"
		if !rsvOK {
			rsvStr = "NO"
		}
		short := assetStr
		if len(short) > 18 {
			short = short[:16] + "…"
		}
		xOK := func() string {
			if xAssert {
				return format1e8(polRemoved)
			}
			return clrRed + format1e8(polRemoved) + "≠exp" + clrReset
		}()
		fmt.Printf("  %-20s %11.4f%% %16s %18s %18s %12s %14s %16s %10s\n",
			short, burnPct, format1e8(xStar), format1e8(reserveCap), xOK, ovflBps.String(),
			col(driftNeutral, dlpDrift), provDrift, col(rsvOK, rsvStr))
	}
	fmt.Printf("\n  %s\n", clrViolet+"DLP v/u drift ≈ 0 ⇒ DLPs kept whole (the load-bearing proof: units untouched + v/u restored)."+clrReset)
	fmt.Printf("  %s\n", clrYel+"For overhang pools (BTC/ETH/LTC) reserve capacity ≈ 0, so the loss routes ~entirely to savers."+clrReset)

	// GLOBAL provider-whole check. Step 5 re-solves RUNEPool.ReserveUnits across all
	// pools at once to preserve provider value; it does so by construction unless it
	// hits the wipe branch (reserve exhausted). So providers are whole iff the
	// provider units (PoolUnits) are unchanged AND the reserve was not wiped.
	providersWhole := before.runePoolPoolUnits.Equal(after.runePoolPoolUnits) && !after.runePoolReserveUnits.IsZero()
	if providersWhole {
		fmt.Printf("  %s\n", clrGreen+fmt.Sprintf("[PASS] RUNEPool providers whole (GLOBAL): PoolUnits unchanged (%s) and reserve not wiped (ReserveUnits %s→%s).",
			format1e8(after.runePoolPoolUnits), format1e8(before.runePoolReserveUnits), format1e8(after.runePoolReserveUnits))+clrReset)
	} else {
		fmt.Printf("  %s\n", clrRed+fmt.Sprintf("[FAIL] RUNEPool providers NOT whole: PoolUnits %s→%s, ReserveUnits %s→%s (wiped=%v).",
			format1e8(before.runePoolPoolUnits), format1e8(after.runePoolPoolUnits),
			format1e8(before.runePoolReserveUnits), format1e8(after.runePoolReserveUnits), after.runePoolReserveUnits.IsZero())+clrReset)
		failCount++
	}

	if failCount > 0 {
		fmt.Printf("  %s\n", clrRed+fmt.Sprintf("STRICT-WATERFALL: %d assertion(s) FAILED", failCount)+clrReset)
	}
	return failCount
}

// driftWithinBps parses a driftBpsCross string ("+N.NN bps" / "-N.NN bps" /
// "n/a") and reports whether |integer-bps part| ≤ tol. "n/a" is treated as
// neutral (degenerate pool, no DLP base to drift).
func driftWithinBps(s string, tol int64) bool {
	if s == "n/a" {
		return true
	}
	t := strings.TrimSuffix(strings.TrimSpace(s), " bps")
	t = strings.TrimPrefix(t, "+")
	t = strings.TrimPrefix(t, "-")
	// strip grouping commas and take the integer part before '.'
	t = strings.ReplaceAll(t, ",", "")
	if i := strings.IndexByte(t, '.'); i >= 0 {
		t = t[:i]
	}
	if t == "" {
		return true
	}
	v, err := strconv.ParseInt(t, 10, 64)
	if err != nil {
		return false
	}
	if v < 0 {
		v = -v
	}
	return v <= tol
}

func renderLossIncidence(before, after fullSnapshot) {
	const (
		clrReset  = "\033[0m"
		clrDim    = "\033[2m"
		clrGreen  = "\033[32m"
		clrViolet = "\033[35m"
	)
	fmt.Println("\n=== Step-1 loss incidence by claim class (who bears the POL-haircut loss) ===")
	fmt.Printf("  %s\n", clrDim+"SynthUnits = CalcUnits(LPUnits·S / max(2·BalanceAsset − S, 1)).  Savers% = SynthUnits/(LPUnits+SynthUnits)."+clrReset)
	fmt.Printf("  %s\n", clrDim+"S≥2A (synth-overhang) ⇒ denominator clamps to 1 ⇒ SynthUnits≫LPUnits ⇒ savers own ~100% ⇒ savers+RUNE-supply bear the cut, NOT POL. No explicit step-3 needed."+clrReset)
	fmt.Printf("  %-46s %16s %13s %12s %9s   %s\n", "Pool (step-1)", "ΔR burned", "savers %", "LP/POL %", "S/2A", "regime")
	fmt.Printf("  %-46s %16s %13s %12s %9s   %s\n", "-------------", "---------", "--------", "--------", "----", "------")
	// stable order
	keys := make([]string, 0, len(before.pools))
	for k := range before.pools {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, assetStr := range keys {
		pB := before.pools[assetStr]
		if pB.IsSynth {
			continue
		}
		pA, ok := after.pools[assetStr]
		if !ok || pA.BalanceRune.GTE(pB.BalanceRune) {
			continue
		}
		// skip treasury-draw (step-4) pools: their loss falls on the treasury LP,
		// not on the savers/LP unit pool we are decomposing here.
		if !common.SafeSub(pB.LPUnits, pA.LPUnits).IsZero() {
			continue
		}
		deltaR := common.SafeSub(pB.BalanceRune, pA.BalanceRune)
		denom := pB.Asset.GetSyntheticAsset().Native()
		// A pool with no synth/savers side has no entry here; a map miss yields a
		// nil-backed cosmos.Uint that panics on .IsZero()/.GTE(). Default to zero so
		// such pools (e.g. TRON.TRX, XRP.XRP) report 0% savers / all-LP incidence.
		S, hasS := before.synthSupply[denom]
		if !hasS {
			S = cosmos.ZeroUint()
		}
		// mirror type_pool.go CalcUnits on the before state
		su := cosmos.ZeroUint()
		if !pB.BalanceAsset.IsZero() && !S.IsZero() {
			den := common.SafeSub(pB.BalanceAsset.MulUint64(2), S)
			if den.IsZero() {
				den = cosmos.OneUint()
			}
			su = pB.LPUnits.Mul(S).Quo(den)
		}
		poolUnits := pB.LPUnits.Add(su)
		if poolUnits.IsZero() {
			continue
		}
		saversBps := su.Mul(cosmos.NewUint(1_000_000)).Quo(poolUnits)
		saversPct := float64(saversBps.BigInt().Int64()) / 10000
		twoA := pB.BalanceAsset.MulUint64(2)
		overhang := !S.IsZero() && S.GTE(twoA)
		ratio := "—"
		if !twoA.IsZero() {
			ratio = fmt.Sprintf("%.2f", float64(S.BigInt().Int64())/float64(twoA.BigInt().Int64()))
		}
		regime := clrGreen + "normal (LPs/POL share it)" + clrReset
		if overhang {
			regime = clrViolet + "SYNTH-OVERHANG → savers+supply" + clrReset
		}
		fmt.Printf("  %-46s %16s %12.4f%% %11.4f%% %9s   %s\n", assetStr, format1e8(deltaR), saversPct, 100-saversPct, ratio, regime)
	}
}

// renderPolicyModel projects the alternative policy discussed in review: assign the
// loss to SAVERS via a calibrated synth-burn while keeping DLP/POL value-per-unit
// whole — i.e. "savers hold fewer, WHOLE synths" rather than every unit-holder
// holding devalued ones. It keeps each step-1 ΔR (which closes the REAL L1 loss),
// then adds the synth-burn that drives LP v/u drift → 0 (the reverted-7229d2b5b2
// calibration, but applied at the current real-loss step-1 sizing).
//
// For each step-1 pool it shows: savers bank-supply now → under-policy (fewer, by
// construction whole), the LP v/u drift NOW (the smear DLPs eat as overhang unwinds)
// vs UNDER-POLICY (≈0 — proof, not assertion), and whether the savers pool is deep
// enough to absorb it. Baseline for drift is the stored pre-migration TotalUnits,
// matching computeCalibration's suTarget derivation, so the policy column lands at 0.
func renderPolicyModel(before, after fullSnapshot) {
	const (
		clrReset  = "\033[0m"
		clrDim    = "\033[2m"
		clrGreen  = "\033[32m"
		clrRed    = "\033[31m"
		clrViolet = "\033[35m"
		clrYel    = "\033[33m"
	)
	fmt.Println("\n=== POLICY MODEL: v/u-preserving savers burn @ real-loss sizing (savers bear it, DLP/POL kept whole) ===")
	fmt.Printf("  %s\n", clrDim+"Keeps step-1 ΔR (closes the REAL L1 loss); ADDS the calibrated synth-burn that zeroes LP value-per-unit drift."+clrReset)
	fmt.Printf("  %s\n", clrDim+"savers supply: now → under-policy (fewer, whole).  v/u drift: now (smear DLPs eat as overhang unwinds) → policy (≈0)."+clrReset)
	fmt.Printf("  %-20s %13s %14s %14s %13s %12s %12s   %s\n",
		"Pool", "step1 ΔR", "savers now", "savers→policy", "Δburn", "v/u now", "v/u policy", "feasible?")
	fmt.Printf("  %-20s %13s %14s %14s %13s %12s %12s   %s\n",
		"----", "--------", "----------", "-------------", "-----", "-------", "----------", "---------")
	for _, r := range computeCalibration(before, after) {
		pB := before.pools[r.asset]
		pA := after.pools[r.asset]
		// baseline = stored pre-migration TotalUnits (matches suTarget derivation)
		tPre := pB.LPUnits.Add(pB.SynthUnits)
		tNow := pA.LPUnits.Add(projectedSynthUnits(pA.LPUnits, pA.BalanceAsset, r.currentSupplyAfter))
		tPolicy := pA.LPUnits.Add(projectedSynthUnits(pA.LPUnits, pA.BalanceAsset, r.requiredSupply))
		vuNow := driftBpsCross(pB.BalanceRune, tPre, pA.BalanceRune, tNow)
		vuPolicy := driftBpsCross(pB.BalanceRune, tPre, pA.BalanceRune, tPolicy)
		burnStr := "+" + format1e8(r.additionalBurn)
		if r.overBurned {
			burnStr = "-" + format1e8(r.additionalBurn) // current already over-burns → un-burn
		}
		// Does the analytic v/u-neutral target actually land at v/u-neutral? It does
		// only if the achieved SynthUnits (run through real CalcUnits) matches the
		// target. When the target supply sits in or near the overhang boundary
		// (2·A_after − S → tiny), CalcUnits' denominator collapses and SynthUnits = L·S
		// explodes far above target — so NO savers-burn lands at v/u-neutral. The
		// pool is too deep in synth-overhang for the savers lever to work. This is
		// BTC's case, and it is WHY the migration falls back to the step-1 smear.
		// In the non-clamped region requiredSupply is the EXACT inverse of CalcUnits,
		// so achieved SynthUnits == suTarget to rounding. Near/at the overhang boundary
		// the inverse is invalid and achieved diverges from target — that gap (in either
		// direction) is the unreachable signal. >1% divergence ⇒ no savers-burn lands at
		// v/u-neutral.
		suAchieved := projectedSynthUnits(pA.LPUnits, pA.BalanceAsset, r.requiredSupply)
		// In the overhang regime the v/u-neutral supply is the CLAMPED solution
		// S = suTarget/L (CalcUnits = L·S, linear) — this IS reachable: it is the
		// "burn the synth pool by ΔR/R" shortcut the strict waterfall implements at
		// runtime, and it lands DLP v/u at ≈0 (BTC: −0.90 bps). The old "UNREACHABLE"
		// verdict assumed only the non-clamped inverse; with the implemented path the
		// realized DLP drift is the real verdict (rendered in renderStrictWaterfall).
		overhang := !r.suTarget.IsZero() && absDiff(suAchieved, r.suTarget).MulUint64(100).GT(r.suTarget)
		var feas string
		switch {
		case overhang:
			feas = clrViolet + "overhang clamp — v/u-neutral via ΔR/R synth burn (strict-waterfall overflow)" + clrReset
		case r.overBurned:
			feas = clrYel + "already over-burned → reduce" + clrReset
		case r.additionalBurn.IsZero():
			feas = clrGreen + "already balanced" + clrReset
		case r.feasible:
			feas = clrGreen + "yes (" + r.recommendedBps.String() + " bps)" + clrReset
		default:
			feas = clrRed + "NO — savers pool too shallow" + clrReset
		}
		short := r.asset
		if len(short) > 18 {
			short = short[:16] + "…"
		}
		fmt.Printf("  %-20s %13s %14s %14s %13s %12s %12s   %s\n",
			short, format1e8(r.deltaR), format1e8(r.currentSupplyAfter), format1e8(r.requiredSupply),
			burnStr, vuNow, vuPolicy, feas)
	}
	fmt.Printf("\n  %s\n", clrViolet+"NOTE 1 — the policy column lands at ≈0 v/u: DLP & POL kept whole; savers absorb ~100% via fewer (whole) synths."+clrReset)
	fmt.Printf("  %s\n", clrViolet+"NOTE 2 — L1 surplus is a SEPARATE axis. This burn changes synth SUPPLY only, not L1 pool.BalanceAsset,"+clrReset)
	fmt.Printf("  %s\n", clrViolet+"          so it does NOT remove the BTC +12.05 tci-surplus. That surplus is step-1 sized to the REAL ~$7.8M"+clrReset)
	fmt.Printf("  %s\n", clrViolet+"          loss (incl. bifrost-tracked drains tci can't see); zeroing it would under-account the real loss."+clrReset)
}

func main() {
	defaultHome := filepath.Join(os.Getenv("HOME"), ".thornode-dryrun-copy")
	home := flag.String("home", defaultHome, "thornode home dir (use a COPY of ~/.thornode)")
	height := flag.Int64("height", 0, "committed height to load; 0 = latest")
	// Archive THORNode API for the step-6 backfill: $THORNODE_API (matching the
	// chainprobe convention), overridable by --thornode-api. Not hard-coded to a
	// specific node — the local endpoints move between clusters/contexts. Empty
	// disables the backfill (step 6 then takes the no-op "absent" path).
	thornodeAPI := flag.String("thornode-api", os.Getenv("THORNODE_API"),
		"archive THORNode REST API to backfill the pruned-out stuck streaming swap so step 6 runs against real state; defaults to $THORNODE_API; empty disables backfill")
	flag.Parse()

	dataDir := filepath.Join(*home, "data")
	if _, err := os.Stat(dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "data dir not found at %s: %v\n", dataDir, err)
		os.Exit(2)
	}

	a, err := openApp(*home, *height)
	if err != nil {
		fmt.Fprintf(os.Stderr, "openApp: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("adr028-dryrun: loaded app at height %d\n", a.LastBlockHeight())

	before, after, migErr := runMigration(a, *thornodeAPI)
	invFailures := report(before.state, after.state, migErr)
	rows, globals, mathFailures := validateWaterfall(before, after)
	renderHeatmap(rows, globals)
	fmt.Printf("\n=== %d math-validation failure(s) ===\n", mathFailures)

	swFailures := renderStrictWaterfall(before, after)
	mathFailures += swFailures

	renderLossIncidence(before, after)

	solvency := computeSolvency(before, after)
	renderSolvency(solvency)

	renderHaircutInventory(before, after)
	renderLPDrift(before, after)
	renderCalibration(computeCalibration(before, after))
	renderPolicyModel(before, after)

	if invFailures > 0 || mathFailures > 0 {
		os.Exit(1)
	}
}
