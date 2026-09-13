package evm

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum"
	ecommon "github.com/ethereum/go-ethereum/common"
	ethclient "github.com/ethereum/go-ethereum/ethclient"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/endpoints"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
)

// LookupTxEverywhere is the signer-cache verification read for EVM chains. A
// broadcast may have reached only one endpoint's mempool, so every non-disabled
// endpoint is asked (pinned, no failover). Found on any endpoint is Found; NotFound
// on every endpoint with every endpoint reachable is NotFound; anything else is
// Ambiguous, which keeps the cache entry and prevents a duplicate outbound.
func LookupTxEverywhere(ctx context.Context, mgr *endpoints.Manager, client *ethclient.Client, hash ecommon.Hash) signercache.LookupResult {
	ambiguous := false
	for _, e := range mgr.Entries() {
		if e.State() == endpoints.StateDisabled {
			continue
		}
		tx, _, err := client.TransactionByHash(endpoints.PinContext(ctx, e.Name), hash)
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
