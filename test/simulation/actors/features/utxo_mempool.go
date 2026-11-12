package features

import (
	"fmt"
	rand "math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/utxo"
)

type utxoMempoolResult struct {
	Chain  common.Chain
	Status string
	Method string
	Detail string
}

type utxoMempoolReport struct {
	mu      sync.Mutex
	results map[common.Chain]utxoMempoolResult
}

func newUtxoMempoolReport() *utxoMempoolReport {
	return &utxoMempoolReport{
		results: map[common.Chain]utxoMempoolResult{},
	}
}

func (r *utxoMempoolReport) record(result utxoMempoolResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.results[result.Chain]; ok && existing.Status == "fail" {
		return
	}
	r.results[result.Chain] = result
}

func (r *utxoMempoolReport) snapshot() []utxoMempoolResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	results := make([]utxoMempoolResult, 0, len(r.results))
	for _, res := range r.results {
		results = append(results, res)
	}
	return results
}

var utxoMempoolSummary = newUtxoMempoolReport()

////////////////////////////////////////////////////////////////////////////////////////
// UTXOMempoolActor
////////////////////////////////////////////////////////////////////////////////////////

// UTXOMempoolActor checks whether a chain's UTXO listing still returns spent UTXOs
// while a spend is still in the mempool. ZEC uses getaddressutxos; other UTXO chains
// use listunspent.
type UTXOMempoolActor struct {
	Actor

	chain     common.Chain
	user      *User
	client    *utxo.Client
	from      common.Address
	spendAmt  cosmos.Uint
	txID      string
	inputKeys map[string]struct{}
}

func NewUTXOMempoolActor(chain common.Chain, rng *rand.Rand) *Actor {
	a := &UTXOMempoolActor{
		Actor: *NewActor(fmt.Sprintf("%s Mempool UTXO", chain), rng),
		chain: chain,
	}
	a.Interval = 250 * time.Millisecond

	a.Ops = append(a.Ops, a.acquireUser)
	a.Ops = append(a.Ops, a.broadcastSpend)
	a.Ops = append(a.Ops, a.loadInputs)
	a.Ops = append(a.Ops, a.checkMempoolUTXOs)
	a.Ops = append(a.Ops, a.releaseUser)

	return &a.Actor
}

func NewZECMempoolUTXOActor(rng *rand.Rand) *Actor {
	return NewUTXOMempoolActor(common.ZECChain, rng)
}

////////////////////////////////////////////////////////////////////////////////////////
// UTXOMempoolSummaryActor
////////////////////////////////////////////////////////////////////////////////////////

// UTXOMempoolSummaryActor logs a pass/fail summary for all UTXO mempool checks.
type UTXOMempoolSummaryActor struct {
	Actor
}

func NewUTXOMempoolSummaryActor(rng *rand.Rand) *Actor {
	a := &UTXOMempoolSummaryActor{
		Actor: *NewActor("UTXO Mempool Summary", rng),
	}
	a.Ops = append(a.Ops, a.reportSummary)
	return &a.Actor
}

func (a *UTXOMempoolSummaryActor) reportSummary(config *OpConfig) OpResult {
	results := utxoMempoolSummary.snapshot()
	sort.Slice(results, func(i, j int) bool {
		return results[i].Chain.String() < results[j].Chain.String()
	})

	seen := make(map[common.Chain]bool, len(results))
	passed := 0
	failed := 0
	inconclusive := 0
	for _, res := range results {
		seen[res.Chain] = true
		switch res.Status {
		case "pass":
			passed++
		case "fail":
			failed++
		case "inconclusive":
			inconclusive++
		}
	}

	missing := make([]string, 0)
	for _, chain := range common.GetUTXOChains() {
		if !seen[chain] {
			missing = append(missing, chain.String())
		}
	}

	a.Log().Info().
		Str("mode", "soft-fail").
		Int("passed", passed).
		Int("failed", failed).
		Int("inconclusive", inconclusive).
		Int("missing", len(missing)).
		Msg("utxo mempool summary")

	for _, res := range results {
		event := a.Log().Info()
		if res.Status == "fail" {
			event = a.Log().Warn()
		}
		event.
			Stringer("chain", res.Chain).
			Str("status", res.Status).
			Str("method", res.Method).
			Str("detail", res.Detail).
			Msg("utxo mempool result")
	}

	if len(missing) > 0 {
		a.Log().Warn().
			Str("missing_chains", strings.Join(missing, ",")).
			Msg("utxo mempool results missing")
	}

	return OpResult{Finish: true}
}

////////////////////////////////////////////////////////////////////////////////////////
// Ops
////////////////////////////////////////////////////////////////////////////////////////

func (a *UTXOMempoolActor) acquireUser(config *OpConfig) OpResult {
	minAmount := a.chain.DustThreshold().MulUint64(10)

	for _, user := range config.Users {
		if !user.Acquire() {
			continue
		}

		chainClient, ok := user.ChainClients[a.chain]
		if !ok {
			user.Release()
			continue
		}
		chainClientTyped, ok := chainClient.(*utxo.Client)
		if !ok {
			user.Release()
			continue
		}

		acct, err := chainClientTyped.GetAccount(nil)
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get utxo account")
			user.Release()
			continue
		}

		balance := acct.Coins.GetCoin(a.chain.GetGasAsset()).Amount
		if balance.LT(minAmount) {
			user.Release()
			continue
		}

		a.user = user
		a.client = chainClientTyped
		a.from = chainClientTyped.GetAddress()
		a.spendAmt = balance.QuoUint64(10)
		if a.spendAmt.LT(minAmount) {
			a.spendAmt = minAmount
		}

		a.SetLogger(log.With().
			Str("user", user.Name()).
			Stringer("chain", a.chain).
			Stringer("from", a.from).
			Logger())

		a.Log().Info().Stringer("balance", balance).Msg("acquired user for mempool utxo check")
		return OpResult{Continue: true}
	}

	locked := 0
	available := 0
	for _, user := range config.Users {
		if user.IsLocked() {
			locked++
		} else {
			available++
		}
	}
	a.Log().Info().
		Int("available_users", available).
		Int("locked_users", locked).
		Msg("waiting for utxo user with balance")
	return OpResult{Continue: false}
}

func (a *UTXOMempoolActor) broadcastSpend(config *OpConfig) OpResult {
	if a.client == nil || a.user == nil {
		return OpResult{Continue: false}
	}

	tx := SimTx{
		Chain:     a.chain,
		ToAddress: a.from,
		Coin:      common.NewCoin(a.chain.GetGasAsset(), a.spendAmt),
		Memo:      "",
	}

	signed, err := a.client.SignTx(tx)
	if err != nil {
		return OpResult{Finish: true, Error: fmt.Errorf("sign utxo tx: %w", err)}
	}
	txID, err := a.client.BroadcastTx(signed)
	if err != nil {
		return OpResult{Finish: true, Error: fmt.Errorf("broadcast utxo tx: %w", err)}
	}
	a.txID = txID
	a.Log().Info().Str("txid", txID).Msg("broadcasted spend")
	return OpResult{Continue: true}
}

func (a *UTXOMempoolActor) loadInputs(config *OpConfig) OpResult {
	if a.txID == "" || a.client == nil {
		return OpResult{Continue: false}
	}

	raw, err := a.client.GetRawTransactionVerbose(a.txID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("raw tx not yet available")
		return OpResult{Continue: false}
	}
	keys := map[string]struct{}{}
	for _, vin := range raw.Vin {
		if vin.Txid == "" {
			continue
		}
		key := fmt.Sprintf("%s:%d", vin.Txid, vin.Vout)
		keys[key] = struct{}{}
	}
	if len(keys) == 0 {
		return OpResult{Finish: true, Error: fmt.Errorf("no transparent inputs found in tx %s", a.txID)}
	}
	a.inputKeys = keys
	return OpResult{Continue: true}
}

func (a *UTXOMempoolActor) checkMempoolUTXOs(config *OpConfig) OpResult {
	if a.client == nil || a.txID == "" || len(a.inputKeys) == 0 {
		return OpResult{Continue: false}
	}

	raw, err := a.client.GetRawTransactionVerbose(a.txID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("raw tx unavailable during check")
		return OpResult{Continue: false}
	}

	method := "listunspent"
	if a.chain.Equals(common.ZECChain) {
		method = "getaddressutxos"
	}
	inconclusive := func(confirmations uint64) OpResult {
		utxoMempoolSummary.record(utxoMempoolResult{
			Chain:  a.chain,
			Status: "inconclusive",
			Method: method,
			Detail: fmt.Sprintf("tx mined with %d confirmations", confirmations),
		})
		a.Log().Warn().
			Uint64("confirmations", confirmations).
			Msg("tx already mined; mempool check inconclusive")
		return OpResult{Continue: true}
	}

	if raw.Confirmations > 0 {
		return inconclusive(raw.Confirmations)
	}

	utxoKeys, err := a.client.GetAddressUTXOKeys(a.from.String())
	if err != nil {
		return OpResult{Continue: false, Error: fmt.Errorf("get utxos: %w", err)}
	}

	for key := range a.inputKeys {
		if _, ok := utxoKeys[key]; ok {
			err := fmt.Errorf("spent utxo %s still returned by %s (mempool)", key, method)
			utxoMempoolSummary.record(utxoMempoolResult{
				Chain:  a.chain,
				Status: "fail",
				Method: method,
				Detail: err.Error(),
			})
			a.Log().Warn().Err(err).Msg("soft-fail: mempool utxo check failed")
			return OpResult{Continue: true}
		}
	}

	// Daemons mine independently: if the tx was mined after the first check,
	// its inputs left the UTXO set for that reason, not via mempool filtering.
	raw, err = a.client.GetRawTransactionVerbose(a.txID)
	if err != nil {
		a.Log().Warn().Err(err).Msg("raw tx unavailable during recheck")
		return OpResult{Continue: false}
	}
	if raw.Confirmations > 0 {
		return inconclusive(raw.Confirmations)
	}

	utxoMempoolSummary.record(utxoMempoolResult{
		Chain:  a.chain,
		Status: "pass",
		Method: method,
	})
	a.Log().Info().Str("method", method).Msg("spent utxos not returned")
	return OpResult{Continue: true}
}

func (a *UTXOMempoolActor) releaseUser(config *OpConfig) OpResult {
	if a.user != nil {
		a.user.Release()
	}
	return OpResult{Finish: true}
}
