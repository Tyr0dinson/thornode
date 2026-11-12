package features

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	rand "math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/utxo"
)

const (
	dashInstantSendSmallPayment = 18 * common.One / 10
	dashInstantSendLargePayment = 22 * common.One / 10
	dashInstantSendMaxAttempts  = 10
	dashInstantSendLowFee       = 600
)

type DashInstantSendActor struct {
	Actor

	account *User
	inbound common.Address
	dash    *utxo.Client

	getMimir      func(string) (int64, error)
	voteFee       func(*OpConfig, int64) error
	readMetrics   func() (instantSendMetrics, error)
	readyDeadline time.Time
	readyError    error
}

// DashInstantSend wires up a feature actor that exercises different InstantSend paths.
func DashInstantSend(rng *rand.Rand) *Actor {
	a := &DashInstantSendActor{
		Actor:       *NewActor("Feature-DASH-InstantSend", rng),
		readMetrics: readInstantSendMetrics,
	}
	a.Timeout = 5 * time.Minute
	a.Interval = time.Second
	a.voteFee = func(config *OpConfig, value int64) error {
		actor := NewMultiNodeMimirActor("DASHInstantSendMinFeeRate", value, rng)
		return actor.Ops[0](config).Error
	}

	a.Ops = []Op{
		a.waitForReadiness,
		// Finish setup before reserving an account so failed setup cannot leave it locked.
		a.initContext,
		a.acquireUser,
		a.happyPath,
		a.largerPayment,
		a.lowFee,
		a.releaseAccount,
	}

	return &a.Actor
}

// requiredBalance covers every scenario's full retry budget, including fees.
func (a *DashInstantSendActor) requiredBalance(defaultFee uint64) uint64 {
	const payments = 2*dashInstantSendSmallPayment + dashInstantSendLargePayment
	fees := 2*defaultFee + dashInstantSendLowFee
	return dashInstantSendMaxAttempts * (payments + fees)
}

// acquireUser reserves an available Dash account that can fund every retry.
func (a *DashInstantSendActor) acquireUser(config *OpConfig) OpResult {
	for _, user := range config.Users {
		client, ok := user.ChainClients[common.DASHChain].(*utxo.Client)
		if !ok {
			continue
		}
		if !user.Acquire() {
			continue
		}

		account, err := client.GetAccount(nil)
		if err != nil {
			a.Log().Error().Err(err).Str("user", user.Name()).Msg("failed to read DASH balance")
			user.Release()
			continue
		}
		required := cosmos.NewUint(a.requiredBalance(client.GetGasSats()))
		balance := account.Coins.GetCoin(common.DASHAsset).Amount
		if balance.LT(required) {
			a.Log().Info().Str("user", user.Name()).Stringer("balance", balance).
				Stringer("required", required).Msg("insufficient DASH for InstantSend retry budget")
			user.Release()
			continue
		}

		a.account = user
		a.dash = client
		a.SetLogger(a.Log().With().Str("user", user.Name()).Logger())
		break
	}

	if a.account == nil {
		return OpResult{Error: fmt.Errorf("no available DASH account can fund the InstantSend retry budget")}
	}
	return OpResult{Continue: true}
}

// initContext fetches the inbound address for the scenarios.
func (a *DashInstantSendActor) initContext(_ *OpConfig) OpResult {
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
		Msg("dash instantsend context loaded")

	return OpResult{Continue: true}
}

// happyPath checks that a standard deposit is finalised before mining.
func (a *DashInstantSendActor) happyPath(_ *OpConfig) OpResult {
	return a.observePayment("happy-path", dashInstantSendSmallPayment, 0)
}

// observePayment requires THORChain to finalise this transaction while Dash
// still reports it as InstantSend locked and unconfirmed in the mempool. The Dash
// height must remain unchanged since broadcast so a mined observation cannot pass.
func (a *DashInstantSendActor) observePayment(name string, amount, feeSats uint64) (result OpResult) {
	// A failed scenario has no later release operation; release its account here.
	defer func() {
		if result.Finish {
			a.releaseAccount(nil)
		}
	}()
	if a.dash == nil {
		return OpResult{Finish: true, Error: fmt.Errorf("Dash client not initialized")}
	}

	const (
		thornodeWaitMax  = 25 * time.Second
		blockSyncTimeout = 30 * time.Second
	)

	for attempt := 1; attempt <= dashInstantSendMaxAttempts; attempt++ {
		// Broadcast right after a new DASH block to maximize time before mining.
		_, broadcastHeight, err := a.dash.WaitForBlockAdvance(blockSyncTimeout)
		if err != nil {
			a.Log().Debug().Err(err).Msg("failed to sync to next DASH block before broadcast")
			broadcastHeight, err = a.dash.GetBlockCount()
			if err != nil {
				return OpResult{Finish: true, Error: fmt.Errorf("read Dash height before broadcast: %w", err)}
			}
		}

		txID, err := a.broadcastTx(name, amount, feeSats)
		if err != nil {
			return OpResult{Finish: true, Error: err}
		}

		thornodeDeadline := time.Now().Add(thornodeWaitMax)
		for time.Now().Before(thornodeDeadline) {
			currentHeight, err := a.dash.GetBlockCount()
			if err != nil {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			if currentHeight != broadcastHeight {
				break
			}

			stages, err := thornode.GetTxStages(txID)
			if err != nil {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			if stages.InboundFinalised == nil || !stages.InboundFinalised.Completed {
				time.Sleep(250 * time.Millisecond)
				continue
			}

			// Check Dash after THORChain finalisation, for this exact transaction.
			txInfo, err := a.dash.GetRawTransactionVerbose(txID)
			if err != nil {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			if txInfo.Confirmations != 0 {
				break
			}
			if !txInfo.InstantSendLocked() {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			_, err = a.dash.GetMempoolEntry(txID)
			if err != nil {
				break
			}
			currentHeight, err = a.dash.GetBlockCount()
			if err != nil || currentHeight != broadcastHeight {
				break
			}

			a.Log().Info().
				Int("attempt", attempt).
				Str("txid", txID).
				Int64("dash_height", broadcastHeight).
				Msg("InstantSend finalised by THORChain while tx remained locked in Dash mempool")
			return OpResult{Continue: true}
		}

		a.Log().Info().Int("attempt", attempt).Str("txid", txID).
			Msg("could not verify InstantSend finalisation before mining; retrying")
		time.Sleep(time.Second)
	}

	return OpResult{Finish: true, Error: fmt.Errorf("failed to finalise inbound on THORChain while InstantSend locked in Dash mempool")}
}

// largerPayment checks a payment above the former $100 simulation limit
// at the fixed $50 DASH price.
func (a *DashInstantSendActor) largerPayment(_ *OpConfig) OpResult {
	return a.observePayment("larger-payment", dashInstantSendLargePayment, 0)
}

// lowFee must wait for ChainLock, despite having an InstantSend lock.
func (a *DashInstantSendActor) lowFee(config *OpConfig) (result OpResult) {
	const key = "DASHInstantSendMinFeeRate"
	previous, err := a.getMimir(key)
	if err != nil {
		a.releaseAccount(nil)
		return OpResult{Finish: true, Error: err}
	}
	// Negative votes remove a node vote without clearing the effective Mimir.
	// Zero restores the same local-config fallback as an unset override.
	previous = max(previous, 0)
	// Pin the scenario's floor to 8 duffs/vB regardless of existing overrides,
	// then restore the previous policy even after a partial vote or a failed assertion.
	defer func() {
		restoreErr := a.voteFee(config, previous)
		if restoreErr == nil {
			restoreErr = a.waitForFeeRate(previous, false)
		}
		if restoreErr != nil {
			result = OpResult{Finish: true, Error: errors.Join(result.Error, fmt.Errorf("restore InstantSend fee floor: %w", restoreErr))}
		}
		if result.Finish {
			a.releaseAccount(nil)
		}
	}()
	err = a.voteFee(config, 8)
	if err == nil {
		err = a.waitForFeeRate(8, true)
	}
	if err != nil {
		return OpResult{Finish: true, Error: err}
	}
	id, err := a.broadcastTx("low-fee", dashInstantSendSmallPayment, dashInstantSendLowFee)
	if err != nil {
		return OpResult{Finish: true, Error: err}
	}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		// Read core first, then daemon finality, to avoid claiming a violation when
		// a ChainLock appeared between the two requests.
		stages, stageErr := thornode.GetTxStages(id)
		tx, txErr := a.dash.GetRawTransactionVerbose(id)
		if stageErr == nil && txErr == nil && stages.InboundFinalised != nil && stages.InboundFinalised.Completed {
			if !tx.ChainLock {
				return OpResult{Finish: true, Error: fmt.Errorf("low-fee deposit credited before ChainLock: %s", id)}
			}
			return OpResult{Continue: true}
		}
		time.Sleep(time.Second)
	}
	return OpResult{Finish: true, Error: fmt.Errorf("low-fee deposit did not settle: %s", id)}
}

func (a *DashInstantSendActor) waitForFeeRate(rate int64, bifrost bool) error {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		value, err := a.getMimir("DASHInstantSendMinFeeRate")
		if err == nil && value == rate {
			if !bifrost {
				return nil
			}
			state, readErr := a.readMetrics()
			if readErr == nil && state.minFeeRate == float64(rate) {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("InstantSend fee floor did not become %d", rate)
}

// releaseAccount makes the account available to other actors and finishes this actor.
func (a *DashInstantSendActor) releaseAccount(_ *OpConfig) OpResult {
	if a.account != nil {
		a.account.Release()
	}
	a.account = nil
	a.dash = nil
	return OpResult{Finish: true}
}

// broadcastTx signs and broadcasts a swap deposit with the scenario's amount and fee override.
func (a *DashInstantSendActor) broadcastTx(scenario string, amount uint64, feeSats uint64) (string, error) {
	// Recheck after block synchronization, before spending funds on this attempt.
	_, err := a.checkReadiness(amount, amount)
	if err != nil {
		return "", fmt.Errorf("InstantSend not ready for %s: %w", scenario, err)
	}
	// Build memo and tx with a fixed fee override tuned for the scenario.
	// Only swaps qualify for early execution. The DASH pool must be available.
	destination, err := a.account.PubKey(common.THORChain).GetAddress(common.THORChain)
	if err != nil {
		return "", err
	}
	tx := SimTx{
		Chain:     common.DASHChain,
		ToAddress: a.inbound,
		Coin:      common.NewCoin(common.DASHAsset, cosmos.NewUint(amount)),
		Memo:      "=:THOR.RUNE:" + destination.String(),
		FeeSats:   feeSats,
	}

	payload, err := a.account.ChainClients[common.DASHChain].SignTx(tx)
	if err != nil {
		return "", fmt.Errorf("failed to sign %s tx: %w", scenario, err)
	}

	txID, err := a.account.ChainClients[common.DASHChain].BroadcastTx(payload)
	if err != nil {
		return "", fmt.Errorf("failed to broadcast %s tx: %w", scenario, err)
	}

	a.Log().Info().
		Str("scenario", scenario).
		Str("txid", txID).
		Uint64("fee_sats", feeSats).
		Str("amount_dash", fmt.Sprintf("%.8f", float64(amount)/float64(common.One))).
		Msg("broadcasted DASH tx")

	return txID, nil
}

// waitForReadiness waits for local Bifrost admission before acquiring an account.
// The actor retries this operation once per second, for at most two minutes.
func (a *DashInstantSendActor) waitForReadiness(config *OpConfig) OpResult {
	if config == nil || len(config.NodeUsers) == 0 {
		return OpResult{Finish: true, Error: fmt.Errorf("InstantSend readiness requires a node THORChain bridge")}
	}
	node := config.NodeUsers[0]
	if node == nil || node.Thorchain == nil {
		return OpResult{Finish: true, Error: fmt.Errorf("InstantSend readiness requires a node THORChain bridge")}
	}
	// The per-key endpoint includes mocknet environment defaults; the bulk endpoint does not.
	a.getMimir = node.Thorchain.GetMimir
	if a.readyDeadline.IsZero() {
		a.readyDeadline = time.Now().Add(2 * time.Minute)
	}
	if !time.Now().Before(a.readyDeadline) {
		return a.readinessTimeout()
	}
	// GetMimir has no cancellation parameter. Keep only one check in flight and
	// bound the wait even if that bridge call stalls. No account is held here.
	result := make(chan OpResult, 1)
	go func() {
		retry, err := a.checkReadiness(dashInstantSendSmallPayment, dashInstantSendLargePayment)
		result <- OpResult{Continue: err == nil, Finish: err != nil && !retry, Error: err}
	}()
	timer := time.NewTimer(time.Until(a.readyDeadline))
	defer timer.Stop()
	select {
	case check := <-result:
		a.readyError = check.Error
		return check
	case <-timer.C:
		return a.readinessTimeout()
	}
}

// readinessTimeout retains the last completed check's failure for diagnosis.
func (a *DashInstantSendActor) readinessTimeout() OpResult {
	if a.readyError != nil {
		return OpResult{Finish: true, Error: fmt.Errorf("timed out waiting for InstantSend readiness: %w", a.readyError)}
	}
	return OpResult{Finish: true, Error: fmt.Errorf("timed out waiting for InstantSend readiness: Mimir or metrics lookup did not finish")}
}

// checkReadiness validates the payment range and local Bifrost allowance.
// Only lookup errors and temporary admission states are retryable during startup.
func (a *DashInstantSendActor) checkReadiness(minPayment, maxPayment uint64) (bool, error) {
	if a.getMimir == nil {
		return false, fmt.Errorf("InstantSend Mimir reader unavailable")
	}
	enabled, err := a.getMimir(fmt.Sprintf(constants.MimirTemplateInstantSendEnabled, common.DASHChain))
	if err != nil {
		return true, fmt.Errorf("read InstantSend enabled setting: %w", err)
	}
	if enabled <= 0 {
		return false, fmt.Errorf("DashInstantSendEnabled must be positive; otherwise core disables InstantSend")
	}
	// Core requires explicit activation, regardless of Bifrost startup defaults.
	minAmount, err := a.getMimir(fmt.Sprintf(constants.MimirTemplateInstantSendMinAmount, common.DASHChain))
	if err != nil {
		return true, fmt.Errorf("read InstantSend minimum amount: %w", err)
	}
	if minAmount < 0 {
		minAmount = common.One / 100 // client default: 0.01 DASH
	}
	if uint64(minAmount) > minPayment {
		return false, fmt.Errorf("DashInstantSendMinAmount=%d exceeds payment %d duffs", minAmount, minPayment)
	}
	maxAmount, err := a.getMimir(fmt.Sprintf(constants.MimirTemplateInstantSendMaxAmount, common.DASHChain))
	if err != nil {
		return true, fmt.Errorf("read InstantSend maximum amount: %w", err)
	}
	if maxAmount <= 0 {
		return false, fmt.Errorf("DashInstantSendMaxAmount must be positive (got %d)", maxAmount)
	}
	if uint64(maxAmount) < maxPayment {
		return false, fmt.Errorf("DashInstantSendMaxAmount=%d cannot cover payment %d duffs", maxAmount, maxPayment)
	}
	state, err := a.readMetrics()
	if err != nil {
		return true, fmt.Errorf("read InstantSend metrics: %w", err)
	}
	if state.paused {
		return true, fmt.Errorf("InstantSend allowance is paused")
	}
	if state.available < float64(maxPayment) {
		return true, fmt.Errorf("InstantSend available allowance %.0f is below payment %d duffs", state.available, maxPayment)
	}
	return false, nil
}

type instantSendMetrics struct {
	available  float64 // duffs
	minFeeRate float64 // duffs/vB
	paused     bool
}

// readInstantSendMetrics reads the Bifrost instance used by this simulation.
func readInstantSendMetrics() (instantSendMetrics, error) {
	return scrapeInstantSendMetrics("http://localhost:9000/metrics")
}

// scrapeInstantSendMetrics requires the allowance and pause gauges from a successful scrape.
// Missing or invalid samples must not look like a ready allowance.
func scrapeInstantSendMetrics(endpoint string) (instantSendMetrics, error) {
	var state instantSendMetrics
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return state, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return state, fmt.Errorf("Bifrost metrics returned HTTP %d", resp.StatusCode)
	}
	var paused float64
	values := map[string]*float64{
		"dash_instantsend_available_duffs": &state.available,
		"dash_instantsend_paused":          &paused,
	}
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) == 0 {
			continue
		}
		if parts[0] == "dash_instantsend_min_fee_rate" {
			if len(parts) != 2 {
				return state, fmt.Errorf("malformed InstantSend fee metric")
			}
			state.minFeeRate, err = strconv.ParseFloat(parts[1], 64)
			if err != nil || math.IsNaN(state.minFeeRate) || math.IsInf(state.minFeeRate, 0) || state.minFeeRate < 0 {
				return state, fmt.Errorf("invalid InstantSend fee metric")
			}
			continue
		}
		target, ok := values[parts[0]]
		if !ok {
			continue
		}
		if len(parts) != 2 {
			return state, fmt.Errorf("malformed InstantSend metric %s", parts[0])
		}
		var value float64
		value, err = strconv.ParseFloat(parts[1], 64)
		if err != nil {
			return state, fmt.Errorf("parse InstantSend metric %s: %w", parts[0], err)
		}
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return state, fmt.Errorf("invalid InstantSend metric %s: %s", parts[0], parts[1])
		}
		*target = value
		delete(values, parts[0])
	}
	if err = scanner.Err(); err != nil {
		return state, err
	}
	for name := range values {
		return state, fmt.Errorf("missing InstantSend metric %s", name)
	}
	switch paused {
	case 0:
	case 1:
		state.paused = true
	default:
		return state, fmt.Errorf("invalid InstantSend pause status: %g", paused)
	}
	return state, nil
}
