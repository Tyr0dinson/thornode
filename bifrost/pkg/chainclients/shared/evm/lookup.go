package evm

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	ecommon "github.com/ethereum/go-ethereum/common"
	etypes "github.com/ethereum/go-ethereum/core/types"
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
// that. Every EVM caller passes 0 (ask every usable endpoint): endpoint health says
// nothing about whether a transaction an endpoint accepted can still settle, so
// skipping one could authorise a duplicate payment.
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

// FindTxEverywhere is LookupTxEverywhere that also hands back the transaction from
// the first endpoint that knows it, with its pending flag. The unstuck path uses it:
// when the active endpoint does not know a broadcast another endpoint still holds,
// the cancellation must be built from that endpoint's copy (its nonce and gas
// price) instead of giving up, or the vault's nonce stays blocked behind the
// original. The result is LookupFound with a transaction, LookupNotFound when every
// endpoint answered NotFound, or LookupAmbiguous (nil transaction) otherwise.
func FindTxEverywhere(ctx context.Context, mgr *endpoints.Manager, client *ethclient.Client, hash ecommon.Hash, maxDown time.Duration) (*etypes.Transaction, bool, signercache.LookupResult) {
	candidates := mgr.VerificationCandidates(maxDown)
	if len(candidates) == 0 {
		return nil, false, signercache.LookupAmbiguous
	}
	perEndpoint := VerificationBudget(ctx, len(candidates))
	ambiguous := false
	for _, e := range candidates {
		ectx, cancel := context.WithTimeout(endpoints.PinContext(ctx, e.Name), perEndpoint)
		tx, pending, err := client.TransactionByHash(ectx, hash)
		cancel()
		switch {
		case err == nil && tx != nil:
			return tx, pending, signercache.LookupFound
		case err != nil && !errors.Is(err, ethereum.NotFound):
			ambiguous = true
		}
	}
	if ambiguous {
		return nil, false, signercache.LookupAmbiguous
	}
	return nil, false, signercache.LookupNotFound
}

// ReorgWitness returns the endpoint that answers one reorg plan's headers,
// transaction re-confirmations and rescanned blocks, to be pinned with
// endpoints.PinContext ("" means unpinned). A single endpoint served the block that
// revealed the reorg, so its answers are used as before failover existed. With
// several, the active endpoint is the witness only when its block at parentHeight is
// parentHash, the parent of the block being scanned: that proves it has reached the
// reorg tip on the same fork, so its "not found" is proof and the hashes it reports
// repair the block records consistently. Otherwise the reorg step fails and is
// retried. No other endpoint is asked, so one still on the old fork cannot veto an
// errata.
func ReorgWitness(ctx context.Context, mgr *endpoints.Manager, client *ethclient.Client, parentHeight int64, parentHash ecommon.Hash) (string, error) {
	if mgr == nil || mgr.Len() < 2 {
		return "", nil
	}
	active := mgr.Active()
	if !active.Usable() {
		return "", fmt.Errorf("active endpoint %s is not usable", active.Name)
	}
	h, err := client.HeaderByNumber(endpoints.PinContext(ctx, active.Name), big.NewInt(parentHeight))
	if err != nil {
		return "", fmt.Errorf("endpoint %s: cannot read block %d: %w", active.Name, parentHeight, err)
	}
	if h.Hash() != parentHash {
		return "", fmt.Errorf("endpoint %s has block %d as %s, not the reorg parent %s: it is behind or on another fork", active.Name, parentHeight, h.Hash().Hex(), parentHash.Hex())
	}
	return active.Name, nil
}
