package watchers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

var (
	// Recheck a detected halt before failing the run so short-lived mimir states
	// do not abort simulations that recover on the next poll.
	solvencyHaltRecheckAttempts = 2
	solvencyHaltRecheckDelay    = 2 * time.Second

	// XMR can report a temporary solvency halt while the sidecar reconciles
	// post-spend balance and change output state. Give that simulation-only
	// transient longer to clear before treating it as a real halt.
	xmrSolvencyHaltRecheckAttempts = 60
	xmrSolvencyHaltRecheckDelay    = 5 * time.Second
)

// outboundQueueItem is the subset of outbound queue fields needed to identify
// Ragnarok outbounds.
type outboundQueueItem struct {
	Chain        string `json:"chain"`
	Memo         string `json:"memo"`
	OriginalMemo string `json:"original_memo"`
}

// fetchActiveSolvencyHalts returns only positive SOLVENCYHALT mimir entries.
// Zero or negative values are treated as inactive halts.
func fetchActiveSolvencyHalts(cl *http.Client, endpoint string) (map[string]int64, error) {
	mimirRes, err := fetchMimir(cl, endpoint)
	if err != nil {
		return nil, err
	}

	activeHalts := make(map[string]int64)
	for k, v := range mimirRes {
		if strings.HasPrefix(k, "SOLVENCYHALT") && v > 0 {
			activeHalts[k] = v
		}
	}

	return activeHalts, nil
}

// fetchMimir loads the thorchain mimir endpoint into a key/value map.
func fetchMimir(cl *http.Client, endpoint string) (map[string]int64, error) {
	resp, err := cl.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mimir response returned non-200 status: %d", resp.StatusCode)
	}

	mimirRes := make(map[string]int64)
	if err = json.NewDecoder(resp.Body).Decode(&mimirRes); err != nil {
		return nil, err
	}

	return mimirRes, nil
}

// fetchPendingRagnarokChains returns chains that still have Ragnarok outbounds
// in the queue.
func fetchPendingRagnarokChains(cl *http.Client, endpoint string) (map[string]struct{}, error) {
	resp, err := cl.Get(endpoint)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("outbound queue response returned non-200 status: %d", resp.StatusCode)
	}

	var outboundItems []outboundQueueItem
	if err = json.NewDecoder(resp.Body).Decode(&outboundItems); err != nil {
		return nil, err
	}

	chains := make(map[string]struct{})
	for _, item := range outboundItems {
		// Prefer original_memo because outbound processing can rewrite memo while
		// preserving the user-visible source memo here.
		memo := item.OriginalMemo
		if memo == "" {
			memo = item.Memo
		}
		if strings.HasPrefix(memo, "RAGNAROK:") {
			chains[item.Chain] = struct{}{}
		}
	}

	return chains, nil
}

// fetchActiveRagnarokChains returns chains with active RAGNAROK-* mimir values.
func fetchActiveRagnarokChains(cl *http.Client, endpoint string) (map[string]struct{}, error) {
	mimirRes, err := fetchMimir(cl, endpoint)
	if err != nil {
		return nil, err
	}

	chains := make(map[string]struct{})
	for k, v := range mimirRes {
		if v <= 0 || !strings.HasPrefix(k, "RAGNAROK-") {
			continue
		}
		// RAGNAROK keys are asset-scoped, for example RAGNAROK-BTC-BTC.
		// Solvency halts are chain-scoped, so compare only the chain prefix.
		asset := strings.TrimPrefix(k, "RAGNAROK-")
		chain, _, _ := strings.Cut(asset, "-")
		if chain != "" {
			chains[chain] = struct{}{}
		}
	}

	return chains, nil
}

// fetchRagnarokInProgress checks the network-level ragnarok endpoint because
// solvency halts can be raised during the drain before per-chain ragnarok Mimir
// and outbound queue state are visible to the watcher.
func fetchRagnarokInProgress(cl *http.Client, endpoint string) (bool, error) {
	resp, err := cl.Get(endpoint)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("ragnarok response returned non-200 status: %d", resp.StatusCode)
	}

	var inProgress bool
	if err = json.NewDecoder(resp.Body).Decode(&inProgress); err != nil {
		return false, err
	}
	return inProgress, nil
}

// haltedChains normalizes active halt mimir keys into sorted chain names.
func haltedChains(activeHalts map[string]int64) []string {
	chains := make([]string, 0, len(activeHalts))
	for k := range activeHalts {
		chain := strings.TrimPrefix(k, "SOLVENCYHALT")
		chain = strings.TrimSuffix(chain, "CHAIN")
		if chain == "" {
			continue
		}
		chains = append(chains, chain)
	}
	sort.Strings(chains)
	return chains
}

// allHaltedChainsInSet reports whether every halted chain is covered by the
// supplied chain set.
func allHaltedChainsInSet(activeHalts map[string]int64, coveredChains map[string]struct{}) bool {
	halted := haltedChains(activeHalts)
	if len(halted) == 0 {
		return false
	}
	for _, chain := range halted {
		if _, ok := coveredChains[chain]; !ok {
			return false
		}
	}
	return true
}

func onlyXMRHalted(activeHalts map[string]int64) bool {
	halted := haltedChains(activeHalts)
	if len(halted) == 0 {
		return false
	}
	for _, chain := range halted {
		if chain != "XMR" {
			return false
		}
	}
	return true
}

// NewSolvencyHalt builds a watcher that fails the simulation on persistent,
// unexpected solvency halts.
func NewSolvencyHalt() *Watcher {
	cl := log.With().Str("watcher", "solvency").Logger()

	return &Watcher{
		Name:     "SolvencyHalt",
		Interval: time.Second,
		Fn: func(config *OpConfig) error {
			endpoint := fmt.Sprintf("%s/thorchain/mimir", thornodeURL)
			queueEndpoint := fmt.Sprintf("%s/thorchain/queue/outbound", thornodeURL)
			ragnarokEndpoint := fmt.Sprintf("%s/thorchain/ragnarok", thornodeURL)

			activeHalts, err := fetchActiveSolvencyHalts(httpClient, endpoint)
			if err != nil {
				cl.Error().Err(err).Msg("failed to get mimir response")
				return nil
			}
			if len(activeHalts) == 0 {
				return nil
			}
			// Suites that intentionally induce insolvency own the halt assertion.
			// Log and return before transient/Ragnarok/XMR retries so an expected
			// XMR halt cannot block this watcher for five minutes.
			if config != nil && config.SolvencyHaltLogOnly() {
				for _, key := range sortedHaltKeys(activeHalts) {
					cl.Warn().
						Str("mimir", key).
						Int64("value", activeHalts[key]).
						Msg("solvency halt detected (log-only mode)")
				}
				return nil
			}

			// Give the network a chance to clear short-lived halt mimirs before
			// treating the condition as persistent.
			transient := false
			for attempt := 0; attempt < solvencyHaltRecheckAttempts; attempt++ {
				time.Sleep(solvencyHaltRecheckDelay)

				recheck, recheckErr := fetchActiveSolvencyHalts(httpClient, endpoint)
				if recheckErr != nil {
					cl.Error().Err(recheckErr).
						Int("attempt", attempt+1).
						Msg("failed to recheck mimir response")
					continue
				}
				if len(recheck) == 0 {
					transient = true
					cl.Warn().Int("attempt", attempt+1).Msg("transient solvency halt cleared on recheck")
					break
				}
				activeHalts = recheck
			}
			if transient {
				return nil
			}

			// During protocol ragnarok, solvency Mimir can be raised while funds
			// are actively draining and before the outbound queue/per-chain
			// ragnarok Mimir checks below see the same state. Treat the network
			// ragnarok endpoint as the source of truth for that transition window.
			networkRagnarok, err := fetchRagnarokInProgress(httpClient, ragnarokEndpoint)
			if err != nil {
				cl.Error().Err(err).Msg("failed to get ragnarok response")
			} else if networkRagnarok {
				cl.Warn().
					Strs("chains", haltedChains(activeHalts)).
					Msg("ignoring solvency halt while network ragnarok drains")
				return nil
			}

			// Ragnarok can intentionally create temporary solvency halt states
			// while queued outbounds are still draining.
			pendingRagnarokChains, err := fetchPendingRagnarokChains(httpClient, queueEndpoint)
			if err != nil {
				cl.Error().Err(err).Msg("failed to get outbound queue response")
			} else if allHaltedChainsInSet(activeHalts, pendingRagnarokChains) {
				cl.Warn().
					Strs("chains", haltedChains(activeHalts)).
					Msg("ignoring solvency halt while pending ragnarok outbounds drain")
				return nil
			}

			// Active Ragnarok mimirs are another intentional drain mode.
			// Ignore halts only when every halted chain is part of that active Ragnarok.
			activeRagnarokChains, err := fetchActiveRagnarokChains(httpClient, endpoint)
			if err != nil {
				cl.Error().Err(err).Msg("failed to get ragnarok mimir response")
			} else if allHaltedChainsInSet(activeHalts, activeRagnarokChains) {
				cl.Warn().
					Strs("chains", haltedChains(activeHalts)).
					Msg("ignoring solvency halt while active ragnarok drains")
				return nil
			}

			if onlyXMRHalted(activeHalts) {
				for attempt := 0; attempt < xmrSolvencyHaltRecheckAttempts; attempt++ {
					time.Sleep(xmrSolvencyHaltRecheckDelay)

					recheck, recheckErr := fetchActiveSolvencyHalts(httpClient, endpoint)
					if recheckErr != nil {
						cl.Error().Err(recheckErr).
							Int("attempt", attempt+1).
							Msg("failed to recheck xmr solvency halt")
						continue
					}
					if len(recheck) == 0 {
						cl.Warn().
							Int("attempt", attempt+1).
							Msg("transient xmr solvency halt cleared on extended recheck")
						return nil
					}
					activeHalts = recheck
					if !onlyXMRHalted(activeHalts) {
						cl.Error().
							Strs("chains", haltedChains(activeHalts)).
							Msg("non-xmr solvency halt detected during xmr transient recheck")
						break
					}
				}
			}

			// Sort keys so log-only output and fatal errors are deterministic.
			keys := sortedHaltKeys(activeHalts)

			// Fatal mode returns the first sorted halt to keep the error stable
			// when multiple chains are halted at once.
			key := keys[0]
			value := activeHalts[key]
			cl.Error().Str("mimir", key).Int64("value", value).Msg("solvency halt detected")
			return fmt.Errorf("solvency halt detected: %s=%d", key, value)
		},
	}
}

func sortedHaltKeys(activeHalts map[string]int64) []string {
	keys := make([]string, 0, len(activeHalts))
	for key := range activeHalts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
