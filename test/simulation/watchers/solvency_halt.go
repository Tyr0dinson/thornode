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
	solvencyHaltRecheckAttempts = 2
	solvencyHaltRecheckDelay    = 2 * time.Second
)

type outboundQueueItem struct {
	Chain        string `json:"chain"`
	Memo         string `json:"memo"`
	OriginalMemo string `json:"original_memo"`
}

func fetchActiveSolvencyHalts(cl *http.Client, endpoint string) (map[string]int64, error) {
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

	activeHalts := make(map[string]int64)
	for k, v := range mimirRes {
		if strings.HasPrefix(k, "SOLVENCYHALT") && v > 0 {
			activeHalts[k] = v
		}
	}

	return activeHalts, nil
}

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

func allHaltedChainsHavePendingRagnarok(activeHalts map[string]int64, pendingChains map[string]struct{}) bool {
	chains := haltedChains(activeHalts)
	if len(chains) == 0 {
		return false
	}
	for _, chain := range chains {
		if _, ok := pendingChains[chain]; !ok {
			return false
		}
	}
	return true
}

func NewSolvencyHalt() *Watcher {
	cl := log.With().Str("watcher", "solvency").Logger()

	return &Watcher{
		Name:     "SolvencyHalt",
		Interval: time.Second,
		Fn: func(config *OpConfig) error {
			endpoint := fmt.Sprintf("%s/thorchain/mimir", thornodeURL)
			queueEndpoint := fmt.Sprintf("%s/thorchain/queue/outbound", thornodeURL)

			activeHalts, err := fetchActiveSolvencyHalts(httpClient, endpoint)
			if err != nil {
				cl.Error().Err(err).Msg("failed to get mimir response")
				return nil
			}
			if len(activeHalts) == 0 {
				return nil
			}

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

			pendingRagnarokChains, err := fetchPendingRagnarokChains(httpClient, queueEndpoint)
			if err != nil {
				cl.Error().Err(err).Msg("failed to get outbound queue response")
			} else if allHaltedChainsHavePendingRagnarok(activeHalts, pendingRagnarokChains) {
				cl.Warn().
					Strs("chains", haltedChains(activeHalts)).
					Msg("ignoring solvency halt while pending ragnarok outbounds drain")
				return nil
			}

			keys := make([]string, 0, len(activeHalts))
			for k := range activeHalts {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			key := keys[0]
			value := activeHalts[key]
			cl.Error().Str("mimir", key).Int64("value", value).Msg("solvency halt detected")
			return fmt.Errorf("solvency halt detected: %s=%d", key, value)
		},
	}
}
