package architecture

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

const (
	dynamicArbMinFeeBaseMinSlipBps       = uint64(10)
	dynamicArbMinFeeFiveMinSlipBps       = uint64(5)
	dynamicArbMinFeeOneMinSlipBps        = uint64(1)
	dynamicArbMinFeeLastMileDivergence   = 15.0
	dynamicArbMinFeeMinProfitRuneExample = 2.0

	dynamicArbMinFeeHistoricalSampleDays      = 30
	dynamicArbMinFeeHistoricalHourBlocks      = 600
	dynamicArbMinFeeHistoricalDayBlocks       = dynamicArbMinFeeHistoricalHourBlocks * 24
	dynamicArbMinFeeHistoricalSampleSeed      = int64(28028)
	dynamicArbMinFeeHistoricalMinImprovement  = 1.0
	dynamicArbMinFeeHistoricalOracleBandBps   = 10.0
	dynamicArbMinFeeHistoricalGatewayBaseURL  = "https://gateway.liquify.com/chain/thorchain_api"
	dynamicArbMinFeeHistoricalRunEnvVar       = "RUN_DYNAMIC_ARB_MIN_FEE_GATEWAY_ESTIMATE"
	dynamicArbMinFeeHistoricalHTTPTimeoutSecs = 20
	dynamicArbMinFeeHistoricalBlockWorkers    = 16
	dynamicArbMinFeeHistoricalFetchRetries    = 3
	dynamicArbMinFeeHistoricalRetryDelayMS    = 500
	dynamicArbMinFeeHistoricalUserAgent       = "thornode-dynamic-arb-min-fee-estimator"
	dynamicArbMinFeeHistoricalPoolColumnWidth = 58
)

var dynamicArbMinFeeHistoricalFloors = []uint64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

type dynamicArbMinFeeLogger interface {
	Logf(format string, args ...any)
}

type dynamicArbMinFeeFixture struct {
	name             string
	asset            common.Asset
	liveAssetDepth   uint64
	liveRuneDepth    uint64
	referenceRunePer float64
}

type dynamicArbMinFeeSimulation struct {
	inputRune        cosmos.Uint
	emitAsset        cosmos.Uint
	liquidityFee     cosmos.Uint
	liquidityFeeRune cosmos.Uint
	naturalSlipBps   cosmos.Uint
	chargedSlipBps   cosmos.Uint
	profitRune       float64
	preDeviationBps  float64
	postDeviationBps float64
	prePool          thorchain.Pool
	postPool         thorchain.Pool
}

type dynamicArbMinFeeReportRow struct {
	marker     string
	label      string
	floor      string
	inputRune  string
	prePrice   string
	postPrice  string
	diff       string
	postDevBps string
	postDevPct string
	profitRune string
	slip       string
}

type dynamicArbMinFeeHistoricalReportRow struct {
	marker               string
	label                string
	floor                string
	legs                 int
	txIDs                int
	actualFeeRune        string
	projectedFeeRune     string
	feeReductionCostRune string
	feeImpact            string
	legShare             string
}

type dynamicArbMinFeeHistoricalPool struct {
	Asset        string `json:"asset"`
	BalanceRune  string `json:"balance_rune"`
	BalanceAsset string `json:"balance_asset"`
	Status       string `json:"status"`
}

type dynamicArbMinFeeHistoricalLastBlock struct {
	Thorchain int64 `json:"thorchain"`
}

type dynamicArbMinFeeHistoricalBlock struct {
	BeginBlockEvents    []dynamicArbMinFeeHistoricalEvent `json:"begin_block_events"`
	EndBlockEvents      []dynamicArbMinFeeHistoricalEvent `json:"end_block_events"`
	FinalizeBlockEvents []dynamicArbMinFeeHistoricalEvent `json:"finalize_block_events"`
	Txs                 []struct {
		Result struct {
			Events []dynamicArbMinFeeHistoricalEvent `json:"events"`
		} `json:"result"`
	} `json:"txs"`
}

type dynamicArbMinFeeHistoricalEvent map[string]string

type dynamicArbMinFeeHistoricalPoolState struct {
	rune  cosmos.Uint
	asset cosmos.Uint
}

type dynamicArbMinFeeHistoricalCandidate struct {
	feeReductionCostRune cosmos.Uint
	postPool             thorchain.Pool
}

type dynamicArbMinFeeHistoricalSummary struct {
	latestHeight       int64
	blocks             int64
	startHeight        int64
	endHeight          int64
	sampleDays         int
	sampleHours        int
	sampleSeed         int64
	sampleWindows      []dynamicArbMinFeeHistoricalSampleWindow
	allSwapLegs        int
	allSwapFeeRune     cosmos.Uint
	basicLegs          int
	oracleLegs         int
	byFloor            map[uint64]*dynamicArbMinFeeHistoricalTotals
	byFloorInBand      map[uint64]*dynamicArbMinFeeHistoricalTotals
	byFloorOutBand     map[uint64]*dynamicArbMinFeeHistoricalTotals
	byPool             map[string]*dynamicArbMinFeeHistoricalTotals
	byPoolInBand       map[string]*dynamicArbMinFeeHistoricalTotals
	byPoolFloor        map[string]map[uint64]*dynamicArbMinFeeHistoricalTotals
	byPoolFloorInBand  map[string]map[uint64]*dynamicArbMinFeeHistoricalTotals
	byPoolFloorOutBand map[string]map[uint64]*dynamicArbMinFeeHistoricalTotals
}

type dynamicArbMinFeeHistoricalTotals struct {
	legs                 int
	txIDs                map[string]struct{}
	actualFeeRune        cosmos.Uint
	feeReductionCostRune cosmos.Uint
}

type dynamicArbMinFeeHistoricalSampleWindow struct {
	dayIndex    int
	hourIndex   int
	startHeight int64
	endHeight   int64
}

var dynamicArbMinFeeDemoMinSlipBps = []uint64{
	dynamicArbMinFeeFiveMinSlipBps,
	dynamicArbMinFeeOneMinSlipBps,
}

// TestDynamicArbMinFeeBehavior mirrors the swap slip calculation
// from swap_current.go without changing production code. The fixture values are
// a deterministic snapshot of live-like pool depths and Binance prices from
// 2026-05-02:
//
//	BTC pool: 203.69785409 BTC / 31,407,112.75487153 RUNE, BTC/RUNE reference ~= 154,291.87
//	ETH pool: 2,671.607805 ETH / 12,140,269.14743294 RUNE, ETH/RUNE reference ~= 4,544.91
func TestDynamicArbMinFeeBehavior(t *testing.T) {
	fixtures := []dynamicArbMinFeeFixture{
		{
			name:             "BTC",
			asset:            common.BTCAsset,
			liveAssetDepth:   20_369_785_409,
			liveRuneDepth:    3_140_711_275_487_153,
			referenceRunePer: 78_225.98 / 0.507,
		},
		{
			name:             "ETH",
			asset:            common.ETHAsset,
			liveAssetDepth:   267_160_780_500,
			liveRuneDepth:    1_214_026_914_743_294,
			referenceRunePer: 2_304.27 / 0.507,
		},
	}

	for _, fixture := range fixtures {
		pool := dynamicArbMinFeePoolBelowReference(fixture, dynamicArbMinFeeLastMileDivergence)
		_, baseOK, baseMaxProfit := dynamicArbMinFeeMostPriceCorrectingProfitableInput(
			pool,
			fixture.referenceRunePer,
			dynamicArbMinFeeBaseMinSlipBps,
			dynamicArbMinFeeMinProfitRuneExample,
		)
		initialDeviation := dynamicArbMinFeeDeviationBps(pool, fixture.referenceRunePer)
		t.Logf("")
		t.Logf("=== %s last-mile arb demo ===", fixture.name)
		t.Logf("Reference: %s RUNE/%s. Initial pool: %s RUNE/%s (%s from reference).", dynamicArbMinFeeFormatPrice(fixture.referenceRunePer), fixture.asset.Ticker, dynamicArbMinFeeFormatPrice(dynamicArbMinFeePoolPriceRunePerAsset(pool)), fixture.asset.Ticker, dynamicArbMinFeeFormatBps(initialDeviation))
		t.Logf("Arb minimum profit threshold used in this demo: %s RUNE", dynamicArbMinFeeFormatFloat(dynamicArbMinFeeMinProfitRuneExample))
		t.Logf(
			"SKIP Default 10 bps floor: best possible profit is %s RUNE (< %s), so a rational arb skips and the pool remains %s skewed.",
			dynamicArbMinFeeFormatFloat(baseMaxProfit),
			dynamicArbMinFeeFormatFloat(dynamicArbMinFeeMinProfitRuneExample),
			dynamicArbMinFeeFormatBps(initialDeviation),
		)

		profitMaxRows := []dynamicArbMinFeeReportRow{}
		followUpRows := []dynamicArbMinFeeReportRow{}
		for _, dynamicArbFloorBps := range dynamicArbMinFeeDemoMinSlipBps {
			maxProfitInput, _ := dynamicArbMinFeeMaxProfitInput(
				pool,
				fixture.referenceRunePer,
				dynamicArbFloorBps,
				dynamicArbMinFeeInputToReference(pool, fixture.referenceRunePer, dynamicArbFloorBps).Uint64(),
			)
			maxProfitTrade := dynamicArbMinFeeSimulateRuneToAsset(pool, cosmos.NewUint(maxProfitInput), fixture.referenceRunePer, dynamicArbFloorBps)

			t.Logf(
				"OK Dynamic arb %d bps floor: profit-max first trade has profit=%s RUNE at input=%s RUNE, post_dev=%s, charged=%s bps.",
				dynamicArbFloorBps,
				dynamicArbMinFeeFormatFloat(maxProfitTrade.profitRune),
				dynamicArbMinFeeFormatRune(maxProfitTrade.inputRune),
				dynamicArbMinFeeFormatBps(maxProfitTrade.postDeviationBps),
				maxProfitTrade.chargedSlipBps,
			)

			profitMaxRows = append(profitMaxRows,
				dynamicArbMinFeeReportFromSim("OK", fmt.Sprintf("profit-max %d bps", dynamicArbFloorBps), dynamicArbFloorBps, maxProfitTrade, fixture.referenceRunePer),
			)

			followUpMaxProfitInput, _ := dynamicArbMinFeeMaxProfitInput(
				maxProfitTrade.postPool,
				fixture.referenceRunePer,
				dynamicArbFloorBps,
				dynamicArbMinFeeInputToReference(maxProfitTrade.postPool, fixture.referenceRunePer, dynamicArbFloorBps).Uint64(),
			)
			followUpMaxProfitTrade := dynamicArbMinFeeSimulateRuneToAsset(maxProfitTrade.postPool, cosmos.NewUint(followUpMaxProfitInput), fixture.referenceRunePer, dynamicArbFloorBps)
			if followUpMaxProfitTrade.profitRune >= dynamicArbMinFeeMinProfitRuneExample {
				t.Logf(
					"FOLLOW-UP after %d bps profit-max state (%s): profitable second trade exists. best profit=%s RUNE at input=%s RUNE, post_dev=%s.",
					dynamicArbFloorBps,
					dynamicArbMinFeeFormatBps(maxProfitTrade.postDeviationBps),
					dynamicArbMinFeeFormatFloat(followUpMaxProfitTrade.profitRune),
					dynamicArbMinFeeFormatRune(followUpMaxProfitTrade.inputRune),
					dynamicArbMinFeeFormatBps(followUpMaxProfitTrade.postDeviationBps),
				)
				followUpRows = append(followUpRows,
					dynamicArbMinFeeReportFromSim("OK", fmt.Sprintf("follow-up %d bps", dynamicArbFloorBps), dynamicArbFloorBps, followUpMaxProfitTrade, fixture.referenceRunePer),
				)
			} else {
				t.Logf(
					"SKIP Follow-up after %d bps profit-max state (%s): best possible profit is %s RUNE (< %s), so the second arb is skipped.",
					dynamicArbFloorBps,
					dynamicArbMinFeeFormatBps(maxProfitTrade.postDeviationBps),
					dynamicArbMinFeeFormatFloat(followUpMaxProfitTrade.profitRune),
					dynamicArbMinFeeFormatFloat(dynamicArbMinFeeMinProfitRuneExample),
				)
				followUpRows = append(followUpRows,
					dynamicArbMinFeeSkippedReport("SKIP", fmt.Sprintf("follow-up skipped %d bps", dynamicArbFloorBps), dynamicArbFloorBps, maxProfitTrade.postPool, fixture.referenceRunePer),
				)
			}

			if maxProfitTrade.profitRune <= baseMaxProfit {
				t.Fatalf("%s %d bps: profit %.2f did not exceed base max profit %.2f", fixture.name, dynamicArbFloorBps, maxProfitTrade.profitRune, baseMaxProfit)
			}
			if maxProfitTrade.profitRune < dynamicArbMinFeeMinProfitRuneExample {
				t.Fatalf("%s %d bps: profit %.2f below minimum %.2f", fixture.name, dynamicArbFloorBps, maxProfitTrade.profitRune, dynamicArbMinFeeMinProfitRuneExample)
			}
		}
		t.Logf("")
		dynamicArbMinFeeLogRows(t, profitMaxRows)
		t.Logf("")
		dynamicArbMinFeeLogRows(t, followUpRows)

		if baseOK {
			t.Fatalf("%s: expected 10 bps floor to remain below minimum profit threshold", fixture.name)
		}
	}
}

// TestDynamicArbMinFeeHistoricalPublicGatewayEstimate is a manual, public-data
// estimate for the ADR-028 eligibility criteria. It intentionally skips during
// normal test runs because it fetches live public gateway data.
//
// Run manually with:
//
//	RUN_DYNAMIC_ARB_MIN_FEE_GATEWAY_ESTIMATE=1 go test ./docs/architecture -run TestDynamicArbMinFeeHistoricalPublicGatewayEstimate -count=1 -v
//
// The summary table reports all qualifying candidates, then splits the same
// candidate set by whether the pre-swap pool price was inside or outside the
// configured oracle band. It samples one deterministic pseudo-random hour from
// each of the last 30 days and simulates dynamic arb floors from 0 through 9 bps.
func TestDynamicArbMinFeeHistoricalPublicGatewayEstimate(t *testing.T) {
	if os.Getenv(dynamicArbMinFeeHistoricalRunEnvVar) != "1" {
		t.Skipf("set %s=1 to fetch live public gateway data", dynamicArbMinFeeHistoricalRunEnvVar)
	}

	summary, err := dynamicArbMinFeeHistoricalEstimate()
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("")
	t.Logf("=== ADR-028 historical public-gateway estimate ===")
	t.Logf("Gateway: %s", dynamicArbMinFeeHistoricalGatewayBaseURL)
	t.Logf("Sample: %d deterministic one-hour windows over the last %d days (seed=%d).", summary.sampleHours, summary.sampleDays, summary.sampleSeed)
	t.Logf("Sampled blocks: %d total (%d blocks/hour); covered height range %d -> %d, latest=%d.", summary.blocks, dynamicArbMinFeeHistoricalHourBlocks, summary.startHeight, summary.endHeight, summary.latestHeight)
	t.Logf("Sample windows: %s", dynamicArbMinFeeHistoricalFormatSampleWindows(summary.sampleWindows))
	t.Logf("Swap legs scanned: all=%d, basic ADR-028 floor-bound trade/secured=%d, with oracle prices=%d.", summary.allSwapLegs, summary.basicLegs, summary.oracleLegs)
	t.Logf("Observed fee from all scanned swap legs: %s RUNE.", dynamicArbMinFeeFormatRune(summary.allSwapFeeRune))
	t.Logf("Oracle band: +/-%.0f bps pre-swap deviation. Minimum simulated improvement: %.0f bps.", dynamicArbMinFeeHistoricalOracleBandBps, dynamicArbMinFeeHistoricalMinImprovement)
	t.Logf("")
	dynamicArbMinFeeHistoricalLogSummaryRows(t, summary)
	t.Logf("")
	dynamicArbMinFeeHistoricalLogPoolRows(t, "top pools by qualifying 10 -> 0 bps fee diff", summary.byPool, summary.byFloor[0])
	t.Logf("")
	dynamicArbMinFeeHistoricalLogPoolRows(t, fmt.Sprintf("top pools by qualifying 10 -> 0 bps fee diff within +/-%.0f bps oracle band", dynamicArbMinFeeHistoricalOracleBandBps), summary.byPoolInBand, summary.byFloorInBand[0])
	t.Logf("")
	dynamicArbMinFeeHistoricalLogTopPoolFloorRows(t, summary, 3)
}

func dynamicArbMinFeePoolBelowReference(f dynamicArbMinFeeFixture, deviationBps float64) thorchain.Pool {
	balanceRune := uint64(math.Round(float64(f.liveAssetDepth) * f.referenceRunePer * (1 - deviationBps/10_000)))
	return thorchain.Pool{
		Asset:        f.asset,
		BalanceRune:  cosmos.NewUint(balanceRune),
		BalanceAsset: cosmos.NewUint(f.liveAssetDepth),
		LPUnits:      cosmos.NewUint(f.liveRuneDepth),
		Status:       thorchain.PoolAvailable,
		Decimals:     8,
	}
}

func dynamicArbMinFeeSimulateRuneToAsset(pool thorchain.Pool, inputRune cosmos.Uint, referenceRunePerAsset float64, minSlipBps uint64) dynamicArbMinFeeSimulation {
	swapper := thorchain.SwapperImpl{}
	naturalSlipBps := swapper.CalcSwapSlip(pool.BalanceRune, inputRune)
	emitAsset, liquidityFee, chargedSlipBps := swapper.GetSwapCalc(
		pool.BalanceRune,
		inputRune,
		pool.BalanceAsset,
		naturalSlipBps,
		cosmos.NewUint(minSlipBps),
	)
	emitAsset = cosmos.RoundToDecimal(emitAsset, pool.Decimals)

	postPool := pool
	postPool.BalanceRune = postPool.BalanceRune.Add(inputRune)
	postPool.BalanceAsset = common.SafeSub(postPool.BalanceAsset, emitAsset)

	// Mirror swap_current.go's conversion of asset-denominated liquidity fee to
	// system-income RUNE, then deduct that RUNE from the stored pool depth.
	postPool.BalanceAsset = common.SafeSub(postPool.BalanceAsset, liquidityFee)
	liquidityFeeRune := postPool.RuneDisbursementForAssetAdd(liquidityFee)
	postPool.BalanceAsset = postPool.BalanceAsset.Add(liquidityFee)
	postPool.BalanceRune = common.SafeSub(postPool.BalanceRune, liquidityFeeRune)

	return dynamicArbMinFeeSimulation{
		inputRune:        inputRune,
		emitAsset:        emitAsset,
		liquidityFee:     liquidityFee,
		liquidityFeeRune: liquidityFeeRune,
		naturalSlipBps:   naturalSlipBps,
		chargedSlipBps:   chargedSlipBps,
		profitRune:       dynamicArbMinFeeRuneProfit(inputRune, emitAsset, referenceRunePerAsset),
		preDeviationBps:  dynamicArbMinFeeDeviationBps(pool, referenceRunePerAsset),
		postDeviationBps: dynamicArbMinFeeDeviationBps(postPool, referenceRunePerAsset),
		prePool:          pool,
		postPool:         postPool,
	}
}

func dynamicArbMinFeeInputToReference(pool thorchain.Pool, referenceRunePerAsset float64, minSlipBps uint64) cosmos.Uint {
	lo := uint64(0)
	hi := pool.BalanceRune.QuoUint64(100).Uint64()
	for dynamicArbMinFeePoolPriceRunePerAsset(dynamicArbMinFeeSimulateRuneToAsset(pool, cosmos.NewUint(hi), referenceRunePerAsset, minSlipBps).postPool) < referenceRunePerAsset {
		hi *= 2
	}
	for lo < hi {
		mid := lo + (hi-lo)/2
		postPrice := dynamicArbMinFeePoolPriceRunePerAsset(dynamicArbMinFeeSimulateRuneToAsset(pool, cosmos.NewUint(mid), referenceRunePerAsset, minSlipBps).postPool)
		if postPrice < referenceRunePerAsset {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return cosmos.NewUint(lo)
}

func dynamicArbMinFeeMostPriceCorrectingProfitableInput(pool thorchain.Pool, referenceRunePerAsset float64, minSlipBps uint64, minProfitRune float64) (dynamicArbMinFeeSimulation, bool, float64) {
	targetInput := dynamicArbMinFeeInputToReference(pool, referenceRunePerAsset, minSlipBps).Uint64()
	if targetInput == 0 {
		return dynamicArbMinFeeSimulation{}, false, 0
	}

	maxProfitInput, maxProfit := dynamicArbMinFeeMaxProfitInput(pool, referenceRunePerAsset, minSlipBps, targetInput)
	if maxProfit < minProfitRune {
		return dynamicArbMinFeeSimulation{}, false, maxProfit
	}

	lo := maxProfitInput
	hi := targetInput
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		profit := dynamicArbMinFeeSimulateRuneToAsset(pool, cosmos.NewUint(mid), referenceRunePerAsset, minSlipBps).profitRune
		if profit >= minProfitRune {
			lo = mid
		} else {
			hi = mid - 1
		}
	}

	return dynamicArbMinFeeSimulateRuneToAsset(pool, cosmos.NewUint(lo), referenceRunePerAsset, minSlipBps), true, maxProfit
}

func dynamicArbMinFeeMaxProfitInput(pool thorchain.Pool, referenceRunePerAsset float64, minSlipBps uint64, targetInput uint64) (uint64, float64) {
	lo := uint64(0)
	hi := targetInput
	for hi-lo > 3 {
		third := (hi - lo) / 3
		m1 := lo + third
		m2 := hi - third
		p1 := dynamicArbMinFeeSimulateRuneToAsset(pool, cosmos.NewUint(m1), referenceRunePerAsset, minSlipBps).profitRune
		p2 := dynamicArbMinFeeSimulateRuneToAsset(pool, cosmos.NewUint(m2), referenceRunePerAsset, minSlipBps).profitRune
		if p1 < p2 {
			lo = m1
		} else {
			hi = m2
		}
	}

	bestInput := lo
	bestProfit := dynamicArbMinFeeSimulateRuneToAsset(pool, cosmos.NewUint(lo), referenceRunePerAsset, minSlipBps).profitRune
	for input := lo + 1; input <= hi; input++ {
		profit := dynamicArbMinFeeSimulateRuneToAsset(pool, cosmos.NewUint(input), referenceRunePerAsset, minSlipBps).profitRune
		if profit > bestProfit {
			bestInput = input
			bestProfit = profit
		}
	}
	return bestInput, bestProfit
}

func dynamicArbMinFeeRuneProfit(inputRune, emitAsset cosmos.Uint, referenceRunePerAsset float64) float64 {
	runeIn := float64(inputRune.Uint64()) / float64(common.One)
	runeOut := float64(emitAsset.Uint64()) / float64(common.One) * referenceRunePerAsset
	return runeOut - runeIn
}

func dynamicArbMinFeeDeviationBps(pool thorchain.Pool, referenceRunePerAsset float64) float64 {
	return (dynamicArbMinFeePoolPriceRunePerAsset(pool)/referenceRunePerAsset - 1) * 10_000
}

func dynamicArbMinFeePoolPriceRunePerAsset(pool thorchain.Pool) float64 {
	return float64(pool.BalanceRune.Uint64()) / float64(pool.BalanceAsset.Uint64())
}

func dynamicArbMinFeeLogRows(c dynamicArbMinFeeLogger, rows []dynamicArbMinFeeReportRow) {
	c.Logf("TABLE %-23s %-8s %12s %13s %13s %12s %11s %11s %10s %12s", "case", "floor", "input RUNE", "pre price", "post price", "diff/ref", "post dev", "post %", "profit", "slip")
	c.Logf("   %-23s %-8s %12s %13s %13s %12s %11s %11s %10s %12s", "-----------------------", "--------", "------------", "-------------", "-------------", "------------", "-----------", "-----------", "----------", "------------")
	for _, row := range rows {
		c.Logf("%s %-23s %-8s %12s %13s %13s %12s %11s %11s %10s %12s",
			row.marker,
			row.label,
			row.floor,
			row.inputRune,
			row.prePrice,
			row.postPrice,
			row.diff,
			row.postDevBps,
			row.postDevPct,
			row.profitRune,
			row.slip,
		)
	}
}

func dynamicArbMinFeeHistoricalLogSummaryRows(c dynamicArbMinFeeLogger, summary dynamicArbMinFeeHistoricalSummary) {
	rows := make([]dynamicArbMinFeeHistoricalReportRow, 0, len(dynamicArbMinFeeHistoricalFloors)*3)
	for _, floor := range dynamicArbMinFeeHistoricalFloors {
		rows = append(rows, dynamicArbMinFeeHistoricalReport("OK", "all candidates", fmt.Sprintf("10->%d bps", floor), summary.byFloor[floor], summary.byFloor[floor], summary.allSwapFeeRune))
	}
	for _, floor := range dynamicArbMinFeeHistoricalFloors {
		rows = append(rows, dynamicArbMinFeeHistoricalReport("OK", fmt.Sprintf("within +/-%.0f bps band", dynamicArbMinFeeHistoricalOracleBandBps), fmt.Sprintf("10->%d bps", floor), summary.byFloorInBand[floor], summary.byFloor[floor], summary.allSwapFeeRune))
	}
	for _, floor := range dynamicArbMinFeeHistoricalFloors {
		rows = append(rows, dynamicArbMinFeeHistoricalReport("OUT", fmt.Sprintf("outside +/-%.0f bps band", dynamicArbMinFeeHistoricalOracleBandBps), fmt.Sprintf("10->%d bps", floor), summary.byFloorOutBand[floor], summary.byFloor[floor], summary.allSwapFeeRune))
	}

	c.Logf("TABLE %-24s %-9s %7s %7s %12s %12s %12s %10s %10s", "case", "floor", "legs", "txids", "actual fee", "projected", "fee diff", "diff/all", "leg share")
	c.Logf("   %-24s %-9s %7s %7s %12s %12s %12s %10s %10s", "------------------------", "---------", "-------", "-------", "------------", "------------", "------------", "----------", "----------")
	for _, row := range rows {
		c.Logf("%s %-24s %-9s %7d %7d %12s %12s %12s %10s %10s",
			row.marker,
			row.label,
			row.floor,
			row.legs,
			row.txIDs,
			row.actualFeeRune,
			row.projectedFeeRune,
			row.feeReductionCostRune,
			row.feeImpact,
			row.legShare,
		)
	}
}

func dynamicArbMinFeeHistoricalReport(marker, label, floor string, totals, denominator *dynamicArbMinFeeHistoricalTotals, allSwapFeeRune cosmos.Uint) dynamicArbMinFeeHistoricalReportRow {
	projectedFeeRune := common.SafeSub(totals.actualFeeRune, totals.feeReductionCostRune)
	return dynamicArbMinFeeHistoricalReportRow{
		marker:               marker,
		label:                label,
		floor:                floor,
		legs:                 totals.legs,
		txIDs:                len(totals.txIDs),
		actualFeeRune:        dynamicArbMinFeeFormatRune(totals.actualFeeRune),
		projectedFeeRune:     dynamicArbMinFeeFormatRune(projectedFeeRune),
		feeReductionCostRune: dynamicArbMinFeeFormatRune(totals.feeReductionCostRune),
		feeImpact:            dynamicArbMinFeeHistoricalFormatUintRatio(totals.feeReductionCostRune, allSwapFeeRune),
		legShare:             dynamicArbMinFeeHistoricalFormatRatio(totals.legs, denominator.legs),
	}
}

func dynamicArbMinFeeHistoricalLogPoolRows(c dynamicArbMinFeeLogger, title string, byPool map[string]*dynamicArbMinFeeHistoricalTotals, denominator *dynamicArbMinFeeHistoricalTotals) {
	type poolRow struct {
		pool   string
		totals *dynamicArbMinFeeHistoricalTotals
	}

	rows := make([]poolRow, 0, len(byPool))
	for pool, totals := range byPool {
		rows = append(rows, poolRow{pool: pool, totals: totals})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].totals.feeReductionCostRune.GT(rows[j].totals.feeReductionCostRune)
	})

	c.Logf("TABLE %s", title)
	c.Logf("   %-*s %7s %7s %12s %12s %10s", dynamicArbMinFeeHistoricalPoolColumnWidth, "pool", "legs", "txids", "actual fee", "fee diff", "diff share")
	c.Logf("   %-*s %7s %7s %12s %12s %10s", dynamicArbMinFeeHistoricalPoolColumnWidth, strings.Repeat("-", dynamicArbMinFeeHistoricalPoolColumnWidth), "-------", "-------", "------------", "------------", "----------")
	for i, row := range rows {
		if i >= 20 {
			break
		}

		costShare := "n/a"
		if denominator != nil {
			costShare = dynamicArbMinFeeHistoricalFormatUintRatio(row.totals.feeReductionCostRune, denominator.feeReductionCostRune)
		}
		c.Logf("   %-*s %7d %7d %12s %12s %10s",
			dynamicArbMinFeeHistoricalPoolColumnWidth,
			dynamicArbMinFeeHistoricalFormatPoolName(row.pool),
			row.totals.legs,
			len(row.totals.txIDs),
			dynamicArbMinFeeFormatRune(row.totals.actualFeeRune),
			dynamicArbMinFeeFormatRune(row.totals.feeReductionCostRune),
			costShare,
		)
	}
}

func dynamicArbMinFeeHistoricalLogTopPoolFloorRows(c dynamicArbMinFeeLogger, summary dynamicArbMinFeeHistoricalSummary, limit int) {
	type poolRow struct {
		pool   string
		totals *dynamicArbMinFeeHistoricalTotals
	}

	rows := make([]poolRow, 0, len(summary.byPool))
	for pool, totals := range summary.byPool {
		rows = append(rows, poolRow{pool: pool, totals: totals})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].totals.feeReductionCostRune.GT(rows[j].totals.feeReductionCostRune)
	})

	if limit > len(rows) {
		limit = len(rows)
	}
	for i := 0; i < limit; i++ {
		pool := rows[i].pool
		c.Logf("TABLE %s floor estimate by 10 -> 0 bps fee diff rank", dynamicArbMinFeeHistoricalFormatPoolName(pool))
		dynamicArbMinFeeHistoricalLogPoolFloorSummaryRows(c, summary, pool)
	}
}

func dynamicArbMinFeeHistoricalLogPoolFloorSummaryRows(c dynamicArbMinFeeLogger, summary dynamicArbMinFeeHistoricalSummary, pool string) {
	rows := make([]dynamicArbMinFeeHistoricalReportRow, 0, len(dynamicArbMinFeeHistoricalFloors)*3)
	for _, floor := range dynamicArbMinFeeHistoricalFloors {
		totals := dynamicArbMinFeeHistoricalPoolFloorTotals(summary.byPoolFloor, pool, floor)
		rows = append(rows, dynamicArbMinFeeHistoricalReport("OK", "all candidates", fmt.Sprintf("10->%d bps", floor), totals, totals, summary.allSwapFeeRune))
	}
	for _, floor := range dynamicArbMinFeeHistoricalFloors {
		rows = append(rows, dynamicArbMinFeeHistoricalReport(
			"OK",
			fmt.Sprintf("within +/-%.0f bps band", dynamicArbMinFeeHistoricalOracleBandBps),
			fmt.Sprintf("10->%d bps", floor),
			dynamicArbMinFeeHistoricalPoolFloorTotals(summary.byPoolFloorInBand, pool, floor),
			dynamicArbMinFeeHistoricalPoolFloorTotals(summary.byPoolFloor, pool, floor),
			summary.allSwapFeeRune,
		))
	}
	for _, floor := range dynamicArbMinFeeHistoricalFloors {
		rows = append(rows, dynamicArbMinFeeHistoricalReport(
			"OUT",
			fmt.Sprintf("outside +/-%.0f bps band", dynamicArbMinFeeHistoricalOracleBandBps),
			fmt.Sprintf("10->%d bps", floor),
			dynamicArbMinFeeHistoricalPoolFloorTotals(summary.byPoolFloorOutBand, pool, floor),
			dynamicArbMinFeeHistoricalPoolFloorTotals(summary.byPoolFloor, pool, floor),
			summary.allSwapFeeRune,
		))
	}

	c.Logf("   %-24s %-9s %7s %7s %12s %12s %12s %10s %10s", "case", "floor", "legs", "txids", "actual fee", "projected", "fee diff", "diff/all", "leg share")
	c.Logf("   %-24s %-9s %7s %7s %12s %12s %12s %10s %10s", "------------------------", "---------", "-------", "-------", "------------", "------------", "------------", "----------", "----------")
	for _, row := range rows {
		c.Logf("   %-24s %-9s %7d %7d %12s %12s %12s %10s %10s",
			row.label,
			row.floor,
			row.legs,
			row.txIDs,
			row.actualFeeRune,
			row.projectedFeeRune,
			row.feeReductionCostRune,
			row.feeImpact,
			row.legShare,
		)
	}
}

func dynamicArbMinFeeHistoricalFormatPoolName(pool string) string {
	if len(pool) <= dynamicArbMinFeeHistoricalPoolColumnWidth {
		return pool
	}
	return pool[:dynamicArbMinFeeHistoricalPoolColumnWidth-3] + "..."
}

func dynamicArbMinFeeHistoricalFormatSampleWindows(windows []dynamicArbMinFeeHistoricalSampleWindow) string {
	parts := make([]string, 0, len(windows))
	for _, window := range windows {
		parts = append(parts, fmt.Sprintf("d-%02d h%02d %d-%d", window.dayIndex, window.hourIndex, window.startHeight, window.endHeight))
	}
	return strings.Join(parts, "; ")
}

func dynamicArbMinFeeReportFromSim(marker, label string, minSlipBps uint64, sim dynamicArbMinFeeSimulation, referenceRunePerAsset float64) dynamicArbMinFeeReportRow {
	postPrice := dynamicArbMinFeePoolPriceRunePerAsset(sim.postPool)
	return dynamicArbMinFeeReportRow{
		marker:     marker,
		label:      label,
		floor:      fmt.Sprintf("%d bps", minSlipBps),
		inputRune:  dynamicArbMinFeeFormatRune(sim.inputRune),
		prePrice:   dynamicArbMinFeeFormatPrice(dynamicArbMinFeePoolPriceRunePerAsset(sim.prePool)),
		postPrice:  dynamicArbMinFeeFormatPrice(postPrice),
		diff:       dynamicArbMinFeeFormatPriceDiff(postPrice - referenceRunePerAsset),
		postDevBps: dynamicArbMinFeeFormatBps(sim.postDeviationBps),
		postDevPct: dynamicArbMinFeeFormatPct(sim.postDeviationBps),
		profitRune: dynamicArbMinFeeFormatFloat(sim.profitRune),
		slip:       fmt.Sprintf("%s->%s bps", sim.naturalSlipBps, sim.chargedSlipBps),
	}
}

func dynamicArbMinFeeSkippedReport(marker, label string, minSlipBps uint64, pool thorchain.Pool, referenceRunePerAsset float64) dynamicArbMinFeeReportRow {
	poolPrice := dynamicArbMinFeePoolPriceRunePerAsset(pool)
	postDevBps := dynamicArbMinFeeDeviationBps(pool, referenceRunePerAsset)
	return dynamicArbMinFeeReportRow{
		marker:     marker,
		label:      label,
		floor:      fmt.Sprintf("%d bps", minSlipBps),
		inputRune:  "0.00",
		prePrice:   dynamicArbMinFeeFormatPrice(poolPrice),
		postPrice:  dynamicArbMinFeeFormatPrice(poolPrice),
		diff:       dynamicArbMinFeeFormatPriceDiff(poolPrice - referenceRunePerAsset),
		postDevBps: dynamicArbMinFeeFormatBps(postDevBps),
		postDevPct: dynamicArbMinFeeFormatPct(postDevBps),
		profitRune: "0.00",
		slip:       "n/a",
	}
}

func dynamicArbMinFeeHistoricalEstimate() (dynamicArbMinFeeHistoricalSummary, error) {
	client := &http.Client{Timeout: time.Duration(dynamicArbMinFeeHistoricalHTTPTimeoutSecs) * time.Second}

	var lastBlocks []dynamicArbMinFeeHistoricalLastBlock
	if err := dynamicArbMinFeeHistoricalFetchJSON(client, dynamicArbMinFeeHistoricalGatewayBaseURL+"/thorchain/lastblock", &lastBlocks); err != nil {
		return dynamicArbMinFeeHistoricalSummary{}, err
	}
	if len(lastBlocks) == 0 {
		return dynamicArbMinFeeHistoricalSummary{}, fmt.Errorf("empty lastblock response")
	}

	latest := lastBlocks[0].Thorchain
	sampleWindows, err := dynamicArbMinFeeHistoricalSampleWindows(latest)
	if err != nil {
		return dynamicArbMinFeeHistoricalSummary{}, err
	}
	startHeight, endHeight, sampleBlocks := dynamicArbMinFeeHistoricalSampleBounds(sampleWindows)

	blocks, err := dynamicArbMinFeeHistoricalFetchSampleBlocks(client, sampleWindows)
	if err != nil {
		return dynamicArbMinFeeHistoricalSummary{}, err
	}

	summary := dynamicArbMinFeeHistoricalSummary{
		latestHeight:       latest,
		blocks:             sampleBlocks,
		startHeight:        startHeight,
		endHeight:          endHeight,
		sampleDays:         dynamicArbMinFeeHistoricalSampleDays,
		sampleHours:        len(sampleWindows),
		sampleSeed:         dynamicArbMinFeeHistoricalSampleSeed,
		sampleWindows:      sampleWindows,
		allSwapFeeRune:     cosmos.ZeroUint(),
		byFloor:            dynamicArbMinFeeHistoricalNewFloorTotals(),
		byFloorInBand:      dynamicArbMinFeeHistoricalNewFloorTotals(),
		byFloorOutBand:     dynamicArbMinFeeHistoricalNewFloorTotals(),
		byPool:             make(map[string]*dynamicArbMinFeeHistoricalTotals),
		byPoolInBand:       make(map[string]*dynamicArbMinFeeHistoricalTotals),
		byPoolFloor:        make(map[string]map[uint64]*dynamicArbMinFeeHistoricalTotals),
		byPoolFloorInBand:  make(map[string]map[uint64]*dynamicArbMinFeeHistoricalTotals),
		byPoolFloorOutBand: make(map[string]map[uint64]*dynamicArbMinFeeHistoricalTotals),
	}

	for _, sampleWindow := range sampleWindows {
		if err := dynamicArbMinFeeHistoricalProcessSampleWindow(client, blocks, sampleWindow, &summary); err != nil {
			return dynamicArbMinFeeHistoricalSummary{}, err
		}
	}

	return summary, nil
}

func dynamicArbMinFeeHistoricalSampleWindows(latest int64) ([]dynamicArbMinFeeHistoricalSampleWindow, error) {
	if latest < dynamicArbMinFeeHistoricalDayBlocks {
		return nil, fmt.Errorf("latest height %d is too low for one full historical sample day", latest)
	}

	rng := rand.New(rand.NewSource(dynamicArbMinFeeHistoricalSampleSeed)) //nolint:gosec
	windows := make([]dynamicArbMinFeeHistoricalSampleWindow, 0, dynamicArbMinFeeHistoricalSampleDays)
	for day := 0; day < dynamicArbMinFeeHistoricalSampleDays; day++ {
		dayEnd := latest - int64(day*dynamicArbMinFeeHistoricalDayBlocks)
		dayStart := dayEnd - dynamicArbMinFeeHistoricalDayBlocks + 1
		if dayStart < 1 {
			break
		}

		hourIndex := rng.Intn(24)
		startHeight := dayStart + int64(hourIndex*dynamicArbMinFeeHistoricalHourBlocks)
		endHeight := startHeight + dynamicArbMinFeeHistoricalHourBlocks - 1
		windows = append(windows, dynamicArbMinFeeHistoricalSampleWindow{
			dayIndex:    day,
			hourIndex:   hourIndex,
			startHeight: startHeight,
			endHeight:   endHeight,
		})
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("no historical sample windows available below latest height %d", latest)
	}
	return windows, nil
}

func dynamicArbMinFeeHistoricalSampleBounds(windows []dynamicArbMinFeeHistoricalSampleWindow) (int64, int64, int64) {
	startHeight := windows[0].startHeight
	endHeight := windows[0].endHeight
	blocks := int64(0)
	for _, window := range windows {
		if window.startHeight < startHeight {
			startHeight = window.startHeight
		}
		if window.endHeight > endHeight {
			endHeight = window.endHeight
		}
		blocks += window.endHeight - window.startHeight + 1
	}
	return startHeight, endHeight, blocks
}

func dynamicArbMinFeeHistoricalProcessSampleWindow(client *http.Client, blocks map[int64]dynamicArbMinFeeHistoricalBlock, sampleWindow dynamicArbMinFeeHistoricalSampleWindow, summary *dynamicArbMinFeeHistoricalSummary) error {
	pools, err := dynamicArbMinFeeHistoricalFetchPoolStates(client, sampleWindow.endHeight)
	if err != nil {
		return fmt.Errorf("fetch pools at sample end height %d: %w", sampleWindow.endHeight, err)
	}

	eventsByHeight := make(map[int64][]dynamicArbMinFeeHistoricalEvent, dynamicArbMinFeeHistoricalHourBlocks)
	oracleByHeight := make(map[int64]map[string]float64, dynamicArbMinFeeHistoricalHourBlocks)
	stableTxIDsByHeight := make(map[int64]map[string]struct{}, dynamicArbMinFeeHistoricalHourBlocks)
	for height := sampleWindow.startHeight; height <= sampleWindow.endHeight; height++ {
		block, ok := blocks[height]
		if !ok {
			return fmt.Errorf("sample block %d missing from fetched block set", height)
		}
		events := dynamicArbMinFeeHistoricalOrderedEvents(block)
		eventsByHeight[height] = events
		oracleByHeight[height] = dynamicArbMinFeeHistoricalOraclePrices(events)
		stableTxIDsByHeight[height] = dynamicArbMinFeeHistoricalStableToStableTxIDs(events)
		for _, event := range events {
			if event["type"] == "swap" {
				summary.allSwapLegs++
				summary.allSwapFeeRune = summary.allSwapFeeRune.Add(dynamicArbMinFeeHistoricalEventUint(event, "liquidity_fee_in_rune"))
			}
		}
	}

	for height := sampleWindow.endHeight; height >= sampleWindow.startHeight; height-- {
		events := eventsByHeight[height]
		for i := len(events) - 1; i >= 0; i-- {
			event := events[i]
			switch event["type"] {
			case "swap":
				poolName := event["pool"]
				postState, ok := pools[poolName]
				if !ok {
					continue
				}

				prePool, ok := dynamicArbMinFeeHistoricalReverseSwap(postState, event)
				if !ok {
					continue
				}

				if dynamicArbMinFeeHistoricalBasicADR028Candidate(event, stableTxIDsByHeight[height]) {
					summary.basicLegs++
					referenceRunePerAsset, ok := dynamicArbMinFeeHistoricalReferenceRunePerAsset(event, oracleByHeight[height])
					if ok {
						summary.oracleLegs++
						preDev := math.Abs(dynamicArbMinFeeDeviationBps(prePool, referenceRunePerAsset))
						inOracleBand := preDev <= dynamicArbMinFeeHistoricalOracleBandBps
						actualFeeRune := dynamicArbMinFeeHistoricalEventUint(event, "liquidity_fee_in_rune")

						for _, floor := range dynamicArbMinFeeHistoricalFloors {
							candidate, ok := dynamicArbMinFeeHistoricalFeeReductionCandidate(prePool, event, floor)
							if !ok {
								continue
							}
							postDev := math.Abs(dynamicArbMinFeeDeviationBps(candidate.postPool, referenceRunePerAsset))
							if postDev+dynamicArbMinFeeHistoricalMinImprovement > preDev {
								continue
							}

							summary.byFloor[floor].add(event["id"], actualFeeRune, candidate.feeReductionCostRune)
							if inOracleBand {
								summary.byFloorInBand[floor].add(event["id"], actualFeeRune, candidate.feeReductionCostRune)
								dynamicArbMinFeeHistoricalPoolFloorTotals(summary.byPoolFloorInBand, poolName, floor).add(event["id"], actualFeeRune, candidate.feeReductionCostRune)
							} else {
								summary.byFloorOutBand[floor].add(event["id"], actualFeeRune, candidate.feeReductionCostRune)
								dynamicArbMinFeeHistoricalPoolFloorTotals(summary.byPoolFloorOutBand, poolName, floor).add(event["id"], actualFeeRune, candidate.feeReductionCostRune)
							}
							dynamicArbMinFeeHistoricalPoolFloorTotals(summary.byPoolFloor, poolName, floor).add(event["id"], actualFeeRune, candidate.feeReductionCostRune)

							if floor == 0 {
								dynamicArbMinFeeHistoricalPoolTotals(summary.byPool, poolName).add(event["id"], actualFeeRune, candidate.feeReductionCostRune)
								if inOracleBand {
									dynamicArbMinFeeHistoricalPoolTotals(summary.byPoolInBand, poolName).add(event["id"], actualFeeRune, candidate.feeReductionCostRune)
								}
							}
						}
					}
				}

				pools[poolName] = dynamicArbMinFeeHistoricalPoolState{
					rune:  prePool.BalanceRune,
					asset: prePool.BalanceAsset,
				}
			case "pool_balance_change":
				dynamicArbMinFeeHistoricalReversePoolBalanceChange(pools, event)
			}
		}
	}

	return nil
}

func dynamicArbMinFeeHistoricalFetchJSON(client *http.Client, url string, dst any) error {
	var lastErr error
	for attempt := 0; attempt <= dynamicArbMinFeeHistoricalFetchRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*dynamicArbMinFeeHistoricalRetryDelayMS) * time.Millisecond)
		}

		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", dynamicArbMinFeeHistoricalUserAgent)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			defer resp.Body.Close()
			return json.NewDecoder(resp.Body).Decode(dst)
		}

		lastErr = fmt.Errorf("GET %s returned %s", url, resp.Status)
		retryable := resp.StatusCode == http.StatusForbidden ||
			resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode >= http.StatusInternalServerError
		resp.Body.Close()
		if !retryable {
			break
		}
	}
	return lastErr
}

func dynamicArbMinFeeHistoricalFetchPoolStates(client *http.Client, height int64) (map[string]dynamicArbMinFeeHistoricalPoolState, error) {
	var poolResponse []dynamicArbMinFeeHistoricalPool
	url := fmt.Sprintf("%s/thorchain/pools?height=%d", dynamicArbMinFeeHistoricalGatewayBaseURL, height)
	if err := dynamicArbMinFeeHistoricalFetchJSON(client, url, &poolResponse); err != nil {
		return nil, err
	}
	return dynamicArbMinFeeHistoricalPoolStates(poolResponse)
}

func dynamicArbMinFeeHistoricalFetchSampleBlocks(client *http.Client, windows []dynamicArbMinFeeHistoricalSampleWindow) (map[int64]dynamicArbMinFeeHistoricalBlock, error) {
	heights := make([]int64, 0, len(windows)*dynamicArbMinFeeHistoricalHourBlocks)
	seen := make(map[int64]struct{}, len(windows)*dynamicArbMinFeeHistoricalHourBlocks)
	for _, window := range windows {
		for height := window.startHeight; height <= window.endHeight; height++ {
			if _, ok := seen[height]; ok {
				continue
			}
			seen[height] = struct{}{}
			heights = append(heights, height)
		}
	}
	sort.Slice(heights, func(i, j int) bool {
		return heights[i] < heights[j]
	})
	return dynamicArbMinFeeHistoricalFetchBlockHeights(client, heights)
}

func dynamicArbMinFeeHistoricalFetchBlockHeights(client *http.Client, heights []int64) (map[int64]dynamicArbMinFeeHistoricalBlock, error) {
	type result struct {
		height int64
		block  dynamicArbMinFeeHistoricalBlock
		err    error
	}

	jobs := make(chan int64)
	results := make(chan result)
	var wg sync.WaitGroup

	for i := 0; i < dynamicArbMinFeeHistoricalBlockWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for height := range jobs {
				var block dynamicArbMinFeeHistoricalBlock
				url := fmt.Sprintf("%s/thorchain/block?height=%d", dynamicArbMinFeeHistoricalGatewayBaseURL, height)
				err := dynamicArbMinFeeHistoricalFetchJSON(client, url, &block)
				results <- result{height: height, block: block, err: err}
			}
		}()
	}

	go func() {
		for _, height := range heights {
			jobs <- height
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	blocks := make(map[int64]dynamicArbMinFeeHistoricalBlock, len(heights))
	for result := range results {
		if result.err != nil {
			return nil, fmt.Errorf("fetch block %d: %w", result.height, result.err)
		}
		blocks[result.height] = result.block
	}

	return blocks, nil
}

func dynamicArbMinFeeHistoricalPoolStates(pools []dynamicArbMinFeeHistoricalPool) (map[string]dynamicArbMinFeeHistoricalPoolState, error) {
	states := make(map[string]dynamicArbMinFeeHistoricalPoolState, len(pools))
	for _, pool := range pools {
		balanceRune, err := cosmos.ParseUint(pool.BalanceRune)
		if err != nil {
			return nil, fmt.Errorf("parse %s balance_rune: %w", pool.Asset, err)
		}
		balanceAsset, err := cosmos.ParseUint(pool.BalanceAsset)
		if err != nil {
			return nil, fmt.Errorf("parse %s balance_asset: %w", pool.Asset, err)
		}
		states[pool.Asset] = dynamicArbMinFeeHistoricalPoolState{
			rune:  balanceRune,
			asset: balanceAsset,
		}
	}
	return states, nil
}

func dynamicArbMinFeeHistoricalOrderedEvents(block dynamicArbMinFeeHistoricalBlock) []dynamicArbMinFeeHistoricalEvent {
	total := len(block.BeginBlockEvents) + len(block.EndBlockEvents) + len(block.FinalizeBlockEvents)
	for _, tx := range block.Txs {
		total += len(tx.Result.Events)
	}

	events := make([]dynamicArbMinFeeHistoricalEvent, 0, total)
	events = append(events, block.BeginBlockEvents...)
	for _, tx := range block.Txs {
		events = append(events, tx.Result.Events...)
	}
	events = append(events, block.EndBlockEvents...)
	events = append(events, block.FinalizeBlockEvents...)
	return events
}

func dynamicArbMinFeeHistoricalOraclePrices(events []dynamicArbMinFeeHistoricalEvent) map[string]float64 {
	prices := make(map[string]float64)
	for _, event := range events {
		if event["type"] != "oracle_price" {
			continue
		}
		price, err := strconv.ParseFloat(event["price"], 64)
		if err != nil {
			continue
		}
		prices[event["symbol"]] = price
	}
	return prices
}

func dynamicArbMinFeeHistoricalStableToStableTxIDs(events []dynamicArbMinFeeHistoricalEvent) map[string]struct{} {
	swapEventsByID := make(map[string][]dynamicArbMinFeeHistoricalEvent)
	for _, event := range events {
		if event["type"] == "swap" {
			swapEventsByID[event["id"]] = append(swapEventsByID[event["id"]], event)
		}
	}

	stableTxIDs := make(map[string]struct{})
	for txID, swapEvents := range swapEventsByID {
		if len(swapEvents) == 0 {
			continue
		}
		_, source, sourceOK := dynamicArbMinFeeHistoricalCoin(swapEvents[0]["coin"])
		_, target, targetOK := dynamicArbMinFeeHistoricalCoin(swapEvents[len(swapEvents)-1]["emit_asset"])
		if sourceOK && targetOK && dynamicArbMinFeeHistoricalIsStable(source) && dynamicArbMinFeeHistoricalIsStable(target) {
			stableTxIDs[txID] = struct{}{}
		}
	}
	return stableTxIDs
}

func dynamicArbMinFeeHistoricalBasicADR028Candidate(event dynamicArbMinFeeHistoricalEvent, stableTxIDs map[string]struct{}) bool {
	if event["type"] != "swap" {
		return false
	}
	if dynamicArbMinFeeHistoricalEventBps(event, "swap_slip") != 10 || dynamicArbMinFeeHistoricalEventBps(event, "pool_slip") >= 10 {
		return false
	}
	if _, ok := stableTxIDs[event["id"]]; ok {
		return false
	}
	_, source, sourceOK := dynamicArbMinFeeHistoricalCoin(event["coin"])
	_, target, targetOK := dynamicArbMinFeeHistoricalCoin(event["emit_asset"])
	if !sourceOK || !targetOK {
		return false
	}
	if source.IsSyntheticAsset() || target.IsSyntheticAsset() {
		return false
	}
	return source.IsTradeAsset() || source.IsSecuredAsset() || target.IsTradeAsset() || target.IsSecuredAsset()
}

func dynamicArbMinFeeHistoricalReferenceRunePerAsset(event dynamicArbMinFeeHistoricalEvent, oraclePrices map[string]float64) (float64, bool) {
	poolAsset, err := common.NewAsset(event["pool"])
	if err != nil {
		return 0, false
	}
	runePrice, runeOK := oraclePrices["RUNE"]
	assetPrice, assetOK := oraclePrices[poolAsset.Ticker.String()]
	if !runeOK || !assetOK || runePrice <= 0 {
		return 0, false
	}
	return assetPrice / runePrice, true
}

func dynamicArbMinFeeHistoricalReverseSwap(postState dynamicArbMinFeeHistoricalPoolState, event dynamicArbMinFeeHistoricalEvent) (thorchain.Pool, bool) {
	inputAmount, source, sourceOK := dynamicArbMinFeeHistoricalCoin(event["coin"])
	emitAmount, _, emitOK := dynamicArbMinFeeHistoricalCoin(event["emit_asset"])
	if !sourceOK || !emitOK {
		return thorchain.Pool{}, false
	}
	liquidityFeeRune := dynamicArbMinFeeHistoricalEventUint(event, "liquidity_fee_in_rune")

	poolAsset, err := common.NewAsset(event["pool"])
	if err != nil {
		return thorchain.Pool{}, false
	}

	prePool := thorchain.Pool{
		Asset:        poolAsset,
		BalanceRune:  postState.rune,
		BalanceAsset: postState.asset,
		Status:       thorchain.PoolAvailable,
		Decimals:     8,
	}
	if source.IsRune() {
		prePool.BalanceRune = common.SafeSub(prePool.BalanceRune, inputAmount).Add(liquidityFeeRune)
		prePool.BalanceAsset = prePool.BalanceAsset.Add(emitAmount)
	} else {
		prePool.BalanceRune = prePool.BalanceRune.Add(emitAmount).Add(liquidityFeeRune)
		prePool.BalanceAsset = common.SafeSub(prePool.BalanceAsset, inputAmount)
	}
	return prePool, true
}

func dynamicArbMinFeeHistoricalFeeReductionCandidate(prePool thorchain.Pool, event dynamicArbMinFeeHistoricalEvent, minSlipBps uint64) (dynamicArbMinFeeHistoricalCandidate, bool) {
	inputAmount, source, ok := dynamicArbMinFeeHistoricalCoin(event["coin"])
	if !ok {
		return dynamicArbMinFeeHistoricalCandidate{}, false
	}

	swapper := thorchain.SwapperImpl{}
	var X, Y cosmos.Uint
	if source.IsRune() {
		X = prePool.BalanceRune
		Y = prePool.BalanceAsset
	} else {
		X = prePool.BalanceAsset
		Y = prePool.BalanceRune
	}

	naturalSlipBps := swapper.CalcSwapSlip(X, inputAmount)
	emitAsset, liquidityFee, _ := swapper.GetSwapCalc(X, inputAmount, Y, naturalSlipBps, cosmos.NewUint(minSlipBps))
	emitAsset = cosmos.RoundToDecimal(emitAsset, prePool.Decimals)

	postPool := prePool
	var liquidityFeeRune cosmos.Uint
	if source.IsRune() {
		postPool.BalanceRune = postPool.BalanceRune.Add(inputAmount)
		postPool.BalanceAsset = common.SafeSub(postPool.BalanceAsset, emitAsset)
		postPool.BalanceAsset = common.SafeSub(postPool.BalanceAsset, liquidityFee)
		liquidityFeeRune = postPool.RuneDisbursementForAssetAdd(liquidityFee)
		postPool.BalanceAsset = postPool.BalanceAsset.Add(liquidityFee)
		postPool.BalanceRune = common.SafeSub(postPool.BalanceRune, liquidityFeeRune)
	} else {
		liquidityFeeRune = liquidityFee
		postPool.BalanceRune = common.SafeSub(postPool.BalanceRune, emitAsset)
		postPool.BalanceAsset = postPool.BalanceAsset.Add(inputAmount)
		postPool.BalanceRune = common.SafeSub(postPool.BalanceRune, liquidityFeeRune)
	}

	actualLiquidityFeeRune := dynamicArbMinFeeHistoricalEventUint(event, "liquidity_fee_in_rune")
	return dynamicArbMinFeeHistoricalCandidate{
		feeReductionCostRune: common.SafeSub(actualLiquidityFeeRune, liquidityFeeRune),
		postPool:             postPool,
	}, true
}

func dynamicArbMinFeeHistoricalReversePoolBalanceChange(pools map[string]dynamicArbMinFeeHistoricalPoolState, event dynamicArbMinFeeHistoricalEvent) {
	poolName := event["asset"]
	state, ok := pools[poolName]
	if !ok {
		return
	}

	runeAmount := dynamicArbMinFeeHistoricalEventUint(event, "rune_amt")
	assetAmount := dynamicArbMinFeeHistoricalEventUint(event, "asset_amt")
	if strings.EqualFold(event["rune_add"], "true") {
		state.rune = common.SafeSub(state.rune, runeAmount)
	} else {
		state.rune = state.rune.Add(runeAmount)
	}
	if strings.EqualFold(event["asset_add"], "true") {
		state.asset = common.SafeSub(state.asset, assetAmount)
	} else {
		state.asset = state.asset.Add(assetAmount)
	}
	pools[poolName] = state
}

func dynamicArbMinFeeHistoricalCoin(coin string) (cosmos.Uint, common.Asset, bool) {
	parts := strings.SplitN(coin, " ", 2)
	if len(parts) != 2 {
		return cosmos.ZeroUint(), common.EmptyAsset, false
	}
	amount, err := cosmos.ParseUint(parts[0])
	if err != nil {
		return cosmos.ZeroUint(), common.EmptyAsset, false
	}
	asset, err := common.NewAsset(parts[1])
	if err != nil {
		return cosmos.ZeroUint(), common.EmptyAsset, false
	}
	return amount, asset, true
}

func dynamicArbMinFeeHistoricalIsStable(asset common.Asset) bool {
	switch asset.Ticker.String() {
	case "USDC", "USDT", "DAI", "LUSD", "USDP", "GUSD", "BUSD", "TOR":
		return true
	default:
		return false
	}
}

func dynamicArbMinFeeHistoricalEventUint(event dynamicArbMinFeeHistoricalEvent, key string) cosmos.Uint {
	amount, err := cosmos.ParseUint(event[key])
	if err != nil {
		return cosmos.ZeroUint()
	}
	return amount
}

func dynamicArbMinFeeHistoricalEventBps(event dynamicArbMinFeeHistoricalEvent, key string) uint64 {
	bps, err := strconv.ParseUint(event[key], 10, 64)
	if err != nil {
		return 0
	}
	return bps
}

func dynamicArbMinFeeHistoricalNewTotals() dynamicArbMinFeeHistoricalTotals {
	return dynamicArbMinFeeHistoricalTotals{
		actualFeeRune:        cosmos.ZeroUint(),
		feeReductionCostRune: cosmos.ZeroUint(),
		txIDs:                make(map[string]struct{}),
	}
}

func dynamicArbMinFeeHistoricalNewFloorTotals() map[uint64]*dynamicArbMinFeeHistoricalTotals {
	totalsByFloor := make(map[uint64]*dynamicArbMinFeeHistoricalTotals, len(dynamicArbMinFeeHistoricalFloors))
	for _, floor := range dynamicArbMinFeeHistoricalFloors {
		totals := dynamicArbMinFeeHistoricalNewTotals()
		totalsByFloor[floor] = &totals
	}
	return totalsByFloor
}

func dynamicArbMinFeeHistoricalPoolTotals(byPool map[string]*dynamicArbMinFeeHistoricalTotals, pool string) *dynamicArbMinFeeHistoricalTotals {
	totals, ok := byPool[pool]
	if ok {
		return totals
	}
	newTotals := dynamicArbMinFeeHistoricalNewTotals()
	totals = &newTotals
	byPool[pool] = totals
	return totals
}

func dynamicArbMinFeeHistoricalPoolFloorTotals(byPool map[string]map[uint64]*dynamicArbMinFeeHistoricalTotals, pool string, floor uint64) *dynamicArbMinFeeHistoricalTotals {
	totalsByFloor, ok := byPool[pool]
	if !ok {
		totalsByFloor = dynamicArbMinFeeHistoricalNewFloorTotals()
		byPool[pool] = totalsByFloor
	}
	totals, ok := totalsByFloor[floor]
	if ok {
		return totals
	}
	newTotals := dynamicArbMinFeeHistoricalNewTotals()
	totals = &newTotals
	totalsByFloor[floor] = totals
	return totals
}

func dynamicArbMinFeeHistoricalFormatRatio(part, total int) string {
	if total == 0 {
		return "0.00%"
	}
	return fmt.Sprintf("%.2f%%", float64(part)*100/float64(total))
}

func dynamicArbMinFeeHistoricalFormatUintRatio(part, total cosmos.Uint) string {
	if total.IsZero() {
		return "0.00%"
	}
	return fmt.Sprintf("%.2f%%", float64(part.Uint64())*100/float64(total.Uint64()))
}

func (t *dynamicArbMinFeeHistoricalTotals) add(txID string, actualFeeRune, feeReductionCostRune cosmos.Uint) {
	if t.txIDs == nil {
		t.txIDs = make(map[string]struct{})
	}
	t.legs++
	t.txIDs[txID] = struct{}{}
	t.actualFeeRune = t.actualFeeRune.Add(actualFeeRune)
	t.feeReductionCostRune = t.feeReductionCostRune.Add(feeReductionCostRune)
}

func dynamicArbMinFeeFormatRune(amount cosmos.Uint) string {
	return dynamicArbMinFeeFormatFloat(float64(amount.Uint64()) / float64(common.One))
}

func dynamicArbMinFeeFormatPrice(price float64) string {
	return dynamicArbMinFeeFormatFloat(price)
}

func dynamicArbMinFeeFormatPriceDiff(diff float64) string {
	return fmt.Sprintf("%+.2f", diff)
}

func dynamicArbMinFeeFormatBps(bps float64) string {
	if math.Abs(bps-math.Round(bps)) < 0.005 {
		return fmt.Sprintf("%+.0f bps", bps)
	}
	return fmt.Sprintf("%+.2f bps", bps)
}

func dynamicArbMinFeeFormatPct(bps float64) string {
	return fmt.Sprintf("%+.4f%%", bps/100)
}

func dynamicArbMinFeeFormatFloat(v float64) string {
	return fmt.Sprintf("%.2f", v)
}
