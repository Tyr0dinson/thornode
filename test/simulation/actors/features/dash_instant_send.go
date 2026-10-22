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

type DashInstantSendActor struct {
	Actor

	account *User
	inbound common.Address
	dash    *utxo.Client
}

// DashInstantSend wires up a feature actor that exercises different InstantSend paths.
func DashInstantSend(rng *rand.Rand) *Actor {
	a := &DashInstantSendActor{
		Actor: *NewActor("Feature-DASH-InstantSend", rng),
	}
	a.Timeout = 5 * time.Minute

	a.Ops = []Op{
		a.acquireUser,
		a.initContext,
		a.happyPathInstantSend,
		a.largerPaymentInstantSend,
		a.lowFeeInstantSend,
		a.releaseAccount,
	}

	return &a.Actor
}

// acquireUser reserves an available account with a Dash client for these scenarios.
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

// happyPathInstantSend checks that a standard deposit is observed before mining.
func (a *DashInstantSendActor) happyPathInstantSend(_ *OpConfig) OpResult {
	return a.observeInstantSend("happy-path", 18*common.One/10, 0)
}

// observeInstantSend verifies that Bifrost accepted a DASH transaction via the mempool path.
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
func (a *DashInstantSendActor) observeInstantSend(name string, amount, feeSats uint64) OpResult {
	if a.dash == nil {
		return OpResult{Finish: true, Error: fmt.Errorf("Dash client not initialized")}
	}

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
		_, height, err := a.dash.WaitForBlockAdvance(blockSyncTimeout)
		if err == nil {
			broadcastHeight = height
		} else {
			a.Log().Debug().Err(err).Msg("failed to sync to next DASH block before broadcast")
		}

		txID, err := a.broadcastTx(name, amount, feeSats)
		if err != nil {
			return OpResult{Finish: true, Error: err}
		}
		if broadcastHeight == 0 {
			height, err := a.dash.GetBlockCount()
			if err == nil {
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

			txInfo, err := a.dash.GetRawTransactionVerbose(txID)
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
			a.Log().Info().Int("attempt", attempt).Str("txid", txID).Msg("InstantSend metric did not move; retrying")
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

			stages, err := thornode.GetTxStages(txID)
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

			txInfo, err := a.dash.GetRawTransactionVerbose(txID)
			if err != nil || txInfo.Confirmations != 0 {
				break
			}

			_, err = a.dash.GetMempoolEntry(txID)
			if err != nil {
				break
			}

			a.Log().
				Info().
				Int("attempt", attempt).
				Str("txid", txID).
				Int64("dash_height", broadcastHeight).
				Msg("InstantSend observed by Thornode while tx remained in Dash mempool")
			return OpResult{Continue: true}
		}

		a.Log().Info().Int("attempt", attempt).Str("txid", txID).Msg("InstantSend metric moved, but tx was not provably observed before mining; retrying")
		time.Sleep(time.Second)
	}

	switch {
	case sawThornodeTx:
		return OpResult{Finish: true, Error: fmt.Errorf("failed to confirm observation before mining")}
	case sawMempoolLock:
		return OpResult{Finish: true, Error: fmt.Errorf("InstantSend-locked tx not observed by THORChain before mining")}
	case sawConfirmedBeforeLock:
		return OpResult{Finish: true, Error: fmt.Errorf("tx mined before InstantSend lock; increase Dash mocknet block time")}
	default:
		return OpResult{Finish: true, Error: fmt.Errorf("InstantSend lock not observed via mempool")}
	}
}

// largerPaymentInstantSend checks a payment above the former $100 simulation limit
// at the fixed $50 DASH price.
func (a *DashInstantSendActor) largerPaymentInstantSend(_ *OpConfig) OpResult {
	return a.observeInstantSend("larger-payment", 22*common.One/10, 0)
}

// lowFeeInstantSend checks a fee below the former InstantSend floor while still paying for relay.
func (a *DashInstantSendActor) lowFeeInstantSend(_ *OpConfig) OpResult {
	return a.observeInstantSend("low-fee", 18*common.One/10, 600)
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

// broadcastTx signs and broadcasts a NOOP deposit with the scenario's amount and fee override.
func (a *DashInstantSendActor) broadcastTx(scenario string, amount uint64, feeSats uint64) (string, error) {
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
