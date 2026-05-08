package features

import (
	"bufio"
	"fmt"
	rand "math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/utxo"
)

const priceDashUSD = 50 // assumed DASH price in USD for threshold calculations

type DashInstantSendActor struct {
	Actor

	account *User
	inbound common.Address
	dash    *utxo.Client

	highValueUSDLimit uint64
	minFeeRateSats    uint64
}

// DashInstantSend wires up a feature actor that exercises different InstantSend paths.
func DashInstantSend(rng *rand.Rand) *Actor {
	a := &DashInstantSendActor{
		// Match the mocknet docker-compose InstantSend policy used by simulation.
		// config/default.yaml keeps the DASH USD threshold at the network default.
		Actor:             *NewActor("Feature-DASH-InstantSend", rng),
		highValueUSDLimit: 100,
		minFeeRateSats:    8,
	}
	a.Timeout = 5 * time.Minute

	a.Ops = []Op{
		a.acquireUser,
		a.initDashContext,
		a.happyPathInstantSend,
		a.highValueFallback,
		a.lowFeeFallback,
		a.releaseAccount,
	}

	return &a.Actor
}

func (a *DashInstantSendActor) acquireUser(config *OpConfig) OpResult {
	for _, user := range config.Users {
		client, ok := user.ChainClients[common.DASHChain].(*utxo.Client)
		if !ok {
			continue
		}
		if !user.Acquire() {
			continue
		}

		a.account = user
		a.dash = client
		a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())
		break
	}

	return OpResult{Continue: a.account != nil}
}

// initDashContext fetches the inbound address, computes pricing defaults, and logs
// the parameters that will drive the scenarios.
func (a *DashInstantSendActor) initDashContext(_ *OpConfig) OpResult {
	inboundAddr, _, err := thornode.GetInboundAddress(common.DASHChain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to fetch DASH inbound address")
		return OpResult{Continue: false}
	}
	if inboundAddr.IsEmpty() {
		return OpResult{Finish: true, Error: fmt.Errorf("DASH inbound address missing")}
	}
	a.inbound = inboundAddr

	a.Log().Info().
		Stringer("inbound", a.inbound).
		Uint64("high_value_usd_limit", a.highValueUSDLimit).
		Uint64("min_fee_rate_sat_per_vbyte", a.minFeeRateSats).
		Msg("dash instantsend context loaded")

	return OpResult{Continue: true}
}

// happyPathInstantSend builds a DASH tx that should satisfy fee/value constraints and
// then verifies that InstantSend was accepted by Bifrost via the mempool path.
//
// Verification method: we poll Bifrost's Prometheus counter
// `dash_instantsend_observed_total` (served on `http://localhost:9000/metrics`).
// The actor records the counter value before broadcast and expects it to increase
// shortly after broadcast. This is a Bifrost-side signal (tx observed and processed),
// not just a Dash-side signal (tx `instantlock=true` in RPC).
//
// Thornode does not receive a "mempool vs mined" flag, nor any InstantSend/InstantLock
// indicator. So Thornode endpoints alone can't prove a tx was accepted from the Dash
// mempool. Instead, we combine signals:
//  1. Bifrost metric increments (Bifrost observed InstantSend in mempool), and
//  2. Thornode exposes tx stages for the txid, and at that exact moment we confirm
//     via Dash RPC that the tx is still unconfirmed and still in the mempool, and the
//     Dash height hasn't advanced since broadcast.
func (a *DashInstantSendActor) happyPathInstantSend(_ *OpConfig) OpResult {
	if a.dash == nil {
		return OpResult{Finish: true, Error: fmt.Errorf("dash client not initialized")}
	}

	amount := a.highValueUSDLimit * common.One * 9 / 10 / priceDashUSD // 90% of threshold

	const (
		maxAttempts      = 10
		metricWaitWindow = 12 * time.Second
		thornodeWaitMax  = 25 * time.Second
		blockSyncTimeout = 30 * time.Second
	)

	var sawMempoolLock bool
	var sawConfirmedBeforeLock bool
	var sawThornodeTx bool

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		startMetric, _ := readInstantSendMetric()

		// Broadcast right after a new DASH block to maximize time in the mempool before the next block.
		broadcastHeight := int64(0)
		if _, height, err := a.dash.WaitForBlockAdvance(blockSyncTimeout); err == nil {
			broadcastHeight = height
		} else {
			a.Log().Debug().Err(err).Msg("failed to sync to next DASH block before broadcast")
		}

		txid, err := a.broadcastDashTx("happy-path", amount, 0)
		if err != nil {
			return OpResult{Finish: true, Error: err}
		}
		if broadcastHeight == 0 {
			if height, err := a.dash.GetBlockCount(); err == nil {
				broadcastHeight = height
			}
		}

		// Treat InstantSend as "accepted" when Bifrost observes it (metric increments),
		// then additionally prove Thornode observed it before mining by correlating with Dash RPC.
		attemptSawMempoolLock := false
		metricObserved := false
		metricDeadline := time.Now().Add(metricWaitWindow)
		for time.Now().Before(metricDeadline) {
			currentMetric, _ := readInstantSendMetric()
			// analyze-ignore(float-comparison)
			if currentMetric-startMetric > 0 {
				metricObserved = true
				break
			}

			txInfo, err := a.dash.DashGetRawTransactionInfo(txid)
			if err == nil {
				if txInfo.Confirmations == 0 && txInfo.InstantLock {
					attemptSawMempoolLock = true
					sawMempoolLock = true
				}
				// If the tx makes it into a block before we see an InstantLock while unconfirmed,
				// this attempt cannot succeed via the mempool path.
				if txInfo.Confirmations > 0 && !attemptSawMempoolLock {
					sawConfirmedBeforeLock = true
					break
				}
			}

			time.Sleep(200 * time.Millisecond)
		}

		if !metricObserved {
			a.Log().Info().Int("attempt", attempt).Str("txid", txid).Msg("InstantSend metric did not move; retrying")
			time.Sleep(time.Second)
			continue
		}

		// Wait for Thornode to expose tx stages for this txid, but only count success if the tx
		// is still in Dash mempool (confirmations=0 + mempool entry exists) and the Dash tip
		// hasn't advanced since broadcast.
		thornodeDeadline := time.Now().Add(thornodeWaitMax)
		for time.Now().Before(thornodeDeadline) {
			// Bail early if Dash has mined a new block since broadcast; this attempt can no longer
			// prove "observed by Thornode before mining".
			if broadcastHeight > 0 {
				currentHeight, err := a.dash.GetBlockCount()
				if err == nil && currentHeight != broadcastHeight {
					break
				}
			}

			stages, err := thornode.GetTxStages(txid)
			if err != nil {
				time.Sleep(250 * time.Millisecond)
				continue
			}

			sawTx := stages.InboundObserved.FinalCount > 0 || stages.InboundObserved.Completed
			if pre, ok := stages.InboundObserved.GetPreConfirmationCountOk(); ok && *pre > 0 {
				sawTx = true
			}
			if !sawTx {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			sawThornodeTx = true

			// Re-check the constraints at the moment Thornode sees the tx.
			currentHeight, err := a.dash.GetBlockCount()
			if err != nil || broadcastHeight == 0 || currentHeight != broadcastHeight {
				break
			}

			txInfo, err := a.dash.DashGetRawTransactionInfo(txid)
			if err != nil || txInfo.Confirmations != 0 {
				break
			}

			if _, err := a.dash.GetMempoolEntry(txid); err != nil {
				break
			}

			a.Log().
				Info().
				Int("attempt", attempt).
				Str("txid", txid).
				Int64("dash_height", broadcastHeight).
				Msg("InstantSend observed by Thornode while tx remained in Dash mempool")
			return OpResult{Continue: true}
		}

		a.Log().Info().Int("attempt", attempt).Str("txid", txid).Msg("InstantSend metric moved, but tx was not provably observed before mining; retrying")
		time.Sleep(time.Second)
	}

	switch {
	case sawThornodeTx:
		return OpResult{Finish: true, Error: fmt.Errorf("thornode saw the tx, but it was not provably still in the Dash mempool at that moment")}
	case sawMempoolLock:
		return OpResult{Finish: true, Error: fmt.Errorf("tx became InstantLocked while unconfirmed, but thornode never exposed it before mining")}
	case sawConfirmedBeforeLock:
		return OpResult{Finish: true, Error: fmt.Errorf("tx mined before InstantLock was observed in mempool; increase Dash mocknet block time")}
	default:
		return OpResult{Finish: true, Error: fmt.Errorf("InstantSend lock not observed via mempool")}
	}
}

// highValueFallback pushes a tx above the USD threshold so it should bypass
// InstantSend and only appear via blocks.
func (a *DashInstantSendActor) highValueFallback(_ *OpConfig) OpResult {
	amount := a.highValueUSDLimit * common.One * 11 / 10 / priceDashUSD // 110% of threshold
	return a.runScenario("high-value-fallback", amount, 0)
}

// lowFeeFallback drops the fee rate below the InstantSend minimum so it should
// only show up on the block path.
func (a *DashInstantSendActor) lowFeeFallback(_ *OpConfig) OpResult {
	amount := a.highValueUSDLimit * common.One * 9 / 10 / priceDashUSD // 90% of threshold

	// Estimate a realistic tx size and set a fee rate below the InstantSend minimum.
	const estimatedVBytes uint64 = 300
	feeSats := (a.minFeeRateSats*estimatedVBytes + 3) / 4
	if feeSats < 300 { // around 1 sat/vB at 300 vB
		feeSats = 300
	}

	return a.runScenario("low-fee-fallback", amount, feeSats)
}

func (a *DashInstantSendActor) releaseAccount(_ *OpConfig) OpResult {
	if a.account != nil {
		a.account.Release()
	}
	a.account = nil
	a.dash = nil
	return OpResult{Finish: true}
}

// runScenario crafts and broadcasts a DASH deposit, then waits for fallback
// observation via block inclusion.
func (a *DashInstantSendActor) runScenario(name string, amount uint64, feeSats uint64) OpResult {
	startMetric, _ := readInstantSendMetric()

	txid, err := a.broadcastDashTx(name, amount, feeSats)
	if err != nil {
		return OpResult{Finish: true, Error: err}
	}

	return a.awaitObservation(txid, startMetric, name)
}

func (a *DashInstantSendActor) broadcastDashTx(scenario string, amount uint64, feeSats uint64) (string, error) {
	// Build memo and tx with a fixed fee override tuned for the scenario.
	// Use NOOP to avoid pool-availability issues.
	// This actor is focused on InstantSend finality paths, not swap execution.
	tx := SimTx{
		Chain:     common.DASHChain,
		ToAddress: a.inbound,
		Coin:      common.NewCoin(common.DASHAsset, cosmos.NewUint(amount)),
		Memo:      "NOOP",
		FeeSats:   feeSats,
	}

	payload, err := a.account.ChainClients[common.DASHChain].SignTx(tx)
	if err != nil {
		return "", fmt.Errorf("failed to sign %s tx: %w", scenario, err)
	}

	txid, err := a.account.ChainClients[common.DASHChain].BroadcastTx(payload)
	if err != nil {
		return "", fmt.Errorf("failed to broadcast %s tx: %w", scenario, err)
	}

	a.Log().Info().
		Str("scenario", scenario).
		Str("txid", txid).
		Uint64("fee_sats", feeSats).
		Str("amount_dash", fmt.Sprintf("%.8f", float64(amount)/float64(common.One))).
		Msg("broadcasted DASH tx")

	return txid, nil
}

// awaitObservation polls metrics and tx details until the fallback tx is seen
// via the block path or a timeout occurs.
func (a *DashInstantSendActor) awaitObservation(txid string, startMetric float64, label string) OpResult {
	deadline := time.Now().Add(4 * time.Minute)
	var lastErr error

	for time.Now().Before(deadline) {
		currentMetric, _ := readInstantSendMetric()
		delta := currentMetric - startMetric

		stages, err := thornode.GetTxStages(txid)
		if err != nil {
			a.Log().Error().Err(err).Str("scenario", label).Str("txid", txid).Msg("failed to fetch tx stages")
			lastErr = err
			time.Sleep(2 * time.Second)
			continue
		}

		sawTx := stages.InboundObserved.FinalCount > 0 || stages.InboundObserved.Completed
		// analyze-ignore(float-comparison)
		sawInstant := delta > 0

		if sawTx {
			if sawInstant {
				return OpResult{Finish: true, Error: fmt.Errorf("scenario %s observed via InstantSend unexpectedly", label)}
			}
			a.Log().Info().Str("scenario", label).Msg("observed via block path")
			return OpResult{Continue: true}
		}

		time.Sleep(2 * time.Second)
	}

	if lastErr != nil {
		return OpResult{Finish: true, Error: fmt.Errorf("timeout waiting for %s: last error %w", label, lastErr)}
	}

	return OpResult{Finish: true, Error: fmt.Errorf("timeout waiting for %s", label)}
}

// readInstantSendMetric scrapes the local Bifrost metrics endpoint for the
// InstantSend observation counter.
func readInstantSendMetric() (float64, error) {
	resp, err := http.Get("http://localhost:9000/metrics")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "dash_instantsend_observed_total") {
			parts := strings.Fields(line)
			if len(parts) == 2 {
				return strconv.ParseFloat(parts[1], 64)
			}
		}
	}

	return 0, scanner.Err()
}
