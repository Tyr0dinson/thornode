package evm

import (
	"context"
	"errors"
	"time"

	"github.com/ethereum/go-ethereum"
	ecommon "github.com/ethereum/go-ethereum/common"
	ethclient "github.com/ethereum/go-ethereum/ethclient"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
)

// LookupTxEverywhere is the signer-cache verification read for EVM chains. A
// broadcast may have reached only one endpoint's mempool, so every endpoint worth
// asking is asked (pinned, no failover): found on any endpoint is Found; NotFound
// on every endpoint, each of which answered, is NotFound; anything else, including
// having queried no endpoint at all, is Ambiguous, which keeps the cache entry and
// prevents a duplicate outbound.
//
// maxDown skips endpoints that have been continuously non-healthy for longer than
// that (the signer-cache expiry): they cannot inform the answer, and treating them
// as "cannot confirm absence" forever would wedge re-signing of a stuck outbound.
func LookupTxEverywhere(ctx context.Context, mgr *endpoints.Manager, client *ethclient.Client, hash ecommon.Hash, maxDown time.Duration) signercache.LookupResult {
	candidates := mgr.VerificationCandidates(maxDown)
	if len(candidates) == 0 {
		return signercache.LookupAmbiguous // absence needs at least one witness
	}
	perEndpoint := VerificationBudget(ctx, len(candidates))
	ambiguous := false
	for _, e := range candidates {
		ectx, cancel := context.WithTimeout(endpoints.PinContext(ctx, e.Name), perEndpoint)
		tx, _, err := client.TransactionByHash(ectx, hash)
		cancel()
		switch {
		case err == nil && tx != nil:
			// Mined or pending on this endpoint: the recorded broadcast is alive.
			return signercache.LookupFound
		case err != nil && !errors.Is(err, ethereum.NotFound):
			ambiguous = true
		}
	}
	if ambiguous {
		return signercache.LookupAmbiguous
	}
	return signercache.LookupNotFound
}

// VerificationBudget splits the caller's remaining deadline across the queries a
// verification will make, so one stalled endpoint cannot consume the whole budget.
func VerificationBudget(ctx context.Context, queries int) time.Duration {
	const fallback = 3 * time.Second
	if queries < 1 {
		queries = 1
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return fallback
	}
	per := time.Until(deadline) / time.Duration(queries)
	if per <= 0 {
		per = time.Millisecond
	}
	return per
}
