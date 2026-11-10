package evm

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
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
	_, _, res := FindTxEverywhere(ctx, mgr, client, hash, maxDown)
	return res
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
// endpoint answered NotFound (and none could not be asked: see
// endpoints.Manager.VerificationCandidates), or LookupAmbiguous (nil transaction)
// otherwise.
func FindTxEverywhere(ctx context.Context, mgr *endpoints.Manager, client *ethclient.Client, hash ecommon.Hash, maxDown time.Duration) (*etypes.Transaction, bool, signercache.LookupResult) {
	candidates, unverified := mgr.VerificationCandidates(maxDown)
	if len(candidates) == 0 {
		mgr.NoteAmbiguousVerification(maxDown)
		return nil, false, signercache.LookupAmbiguous
	}
	perEndpoint := VerificationBudget(ctx, len(candidates))
	var failed []string
	for _, e := range candidates {
		ectx, cancel := context.WithTimeout(endpoints.PinContext(ctx, e.Name), perEndpoint)
		tx, pending, err := client.TransactionByHash(ectx, hash)
		cancel()
		switch {
		case err == nil && tx != nil:
			return tx, pending, signercache.LookupFound
		case err != nil && !errors.Is(err, ethereum.NotFound):
			failed = append(failed, e.Name)
		}
	}
	if len(failed) > 0 || unverified > 0 {
		mgr.NoteAmbiguousVerification(maxDown, failed...)
		return nil, false, signercache.LookupAmbiguous
	}
	return nil, false, signercache.LookupNotFound
}

// ReorgWitness returns the endpoint that answers one reorg plan's headers,
// transaction re-confirmations and rescanned blocks, to be pinned with
// endpoints.PinContext ("" means unpinned). A single endpoint served the block that
// revealed the reorg, so its answers are used as before failover existed. With
// several, the active endpoint is the witness only when
//   - its block at the scanned block's height minus one is the scanned block's parent,
//   - its block at the scanned height is the scanned block itself (two hashes computed
//     the same way from the same height, equal only for the same block): it is on the
//     scanned fork, so a receipt it reports describes the scanned chain, and
//   - its tip is at least chainHeight, the height the scan was driven by (0 skips
//     this): it is as far as the endpoint that served the scan, so its "not found"
//     cannot miss a transaction re-mined in a block that endpoint already has.
//
// Its "not found" is then proof and the hashes it reports repair the block records
// consistently. Otherwise the reorg step fails and is retried. No other endpoint is
// asked, so one still on the old fork cannot veto an errata. An active endpoint that
// cannot answer these reads at all is sidelined (Manager.Sideline): it answers its
// probes, so nothing else would move the active role off it, and the next attempt's
// witness is the next endpoint, which has to pass the same checks.
func ReorgWitness(ctx context.Context, mgr *endpoints.Manager, client *ethclient.Client, scanned *etypes.Header, chainHeight int64) (string, error) {
	if mgr == nil || mgr.Len() < 2 {
		return "", nil
	}
	active := mgr.Active()
	if !active.Usable() {
		return "", fmt.Errorf("active endpoint %s is not usable", active.Name)
	}
	pinned := endpoints.PinContext(ctx, active.Name)
	header := func(height int64) (*etypes.Header, error) {
		return witnessHeader(ctx, mgr, client, active.Name, height)
	}
	parentHeight := scanned.Number.Int64() - 1
	parent, err := header(parentHeight)
	if err != nil {
		return "", err
	}
	if parent.Hash() != scanned.ParentHash {
		return "", fmt.Errorf("endpoint %s has block %d as %s, not the reorg parent %s: it is behind or on another fork", active.Name, parentHeight, parent.Hash().Hex(), scanned.ParentHash.Hex())
	}
	same, err := header(scanned.Number.Int64())
	if err != nil {
		return "", err
	}
	if same.Hash() != scanned.Hash() {
		return "", fmt.Errorf("endpoint %s has block %d as %s, not the scanned block %s: it holds a competing block", active.Name, scanned.Number.Int64(), same.Hash().Hex(), scanned.Hash().Hex())
	}
	if chainHeight > 0 {
		tip, err := client.BlockNumber(pinned)
		if err != nil {
			mgr.Sideline(active.Name, "cannot serve the reorg witness's tip")
			return "", fmt.Errorf("endpoint %s: cannot read its tip: %w", active.Name, err)
		}
		if int64(tip) < chainHeight {
			return "", fmt.Errorf("endpoint %s is at block %d, behind the chain height %d the scan was driven by", active.Name, tip, chainHeight)
		}
	}
	return active.Name, nil
}

// WitnessStillHolds is the last read of a reorg plan: once the plan has read every
// answer it uses from the witness, the witness must still hold the scanned block (its
// block at the scanned height hashes to the scanned header's hash, as in ReorgWitness).
// A node's chain is a single chain, so the failed receipts, "not found" answers,
// headers and rescanned blocks it reported between the two checks describe the
// scanned fork. A witness that moved to another fork meanwhile leaves the step
// undecided, and it is retried. "" (a single endpoint) is not checked, as
// ReorgWitness does not check it; a read error sidelines the witness, as there.
func WitnessStillHolds(ctx context.Context, mgr *endpoints.Manager, client *ethclient.Client, witness string, scanned *etypes.Header) error {
	if witness == "" {
		return nil
	}
	h, err := witnessHeader(ctx, mgr, client, witness, scanned.Number.Int64())
	if err != nil {
		return err
	}
	if h.Hash() != scanned.Hash() {
		return fmt.Errorf("endpoint %s moved off the scanned block %d during the reorg plan (it has %s, not %s): its answers may describe another fork", witness, scanned.Number.Int64(), h.Hash().Hex(), scanned.Hash().Hex())
	}
	return nil
}

// FailedReceiptOnScannedFork checks that a failed receipt the witness reported for a
// stored transaction is from a block on the scanned fork before it becomes an
// errata: at the scanned height (always, for a transaction the scanned block lists)
// it must be the scanned block, one below it the scanned block's parent, and at any
// other height the witness's own block there (one pinned header read: the witness
// holds the scanned block, so its blocks below are the scanned block's ancestors and
// those above extend it; WitnessStillHolds re-checks it still holds it). A receipt
// naming another block is from another fork as far as the plan can tell, and the
// step is retried. "" (a single endpoint) is not checked, as in ReorgWitness.
func FailedReceiptOnScannedFork(ctx context.Context, mgr *endpoints.Manager, client *ethclient.Client, witness string, scanned *etypes.Header, listed bool, receipt ReceiptBlock) error {
	if witness == "" {
		return nil
	}
	height, at := scanned.Number.Int64(), int64(-1)
	if receipt.Number != nil {
		at = receipt.Number.Int64()
	}
	switch {
	case listed || at == height:
		if at != height || receipt.Hash != scanned.Hash() {
			return fmt.Errorf("endpoint %s: the failed receipt is in block %d %s, not the scanned block %d %s", witness, at, receipt.Hash.Hex(), height, scanned.Hash().Hex())
		}
	case at == height-1:
		if receipt.Hash != scanned.ParentHash {
			return fmt.Errorf("endpoint %s: the failed receipt is in block %d %s, not the scanned block's parent %s", witness, at, receipt.Hash.Hex(), scanned.ParentHash.Hex())
		}
	case at >= 0:
		h, err := witnessHeader(ctx, mgr, client, witness, at)
		if err != nil {
			return err
		}
		if h.Hash() != receipt.Hash {
			return fmt.Errorf("endpoint %s: the failed receipt is in block %d %s, not the witness's block at that height %s", witness, at, receipt.Hash.Hex(), h.Hash().Hex())
		}
	case at < 0:
		return fmt.Errorf("endpoint %s: the failed receipt names no block", witness)
	}
	return nil
}

// SidelineWitness puts a witness that failed one of its reorg plan's reads (a header,
// a re-confirmation, a rescanned block or its receipts) in the rate-limit cooldown
// and moves the active role off it (Manager.Sideline), so the retried step's witness
// is the next endpoint, held to the same checks: a witness whose other reads succeed
// would otherwise be picked again on every retry. "" (a single endpoint) is left as is.
func SidelineWitness(mgr *endpoints.Manager, witness, reason string) {
	if mgr == nil || witness == "" {
		return
	}
	mgr.Sideline(witness, reason)
}

// witnessHeader reads the witness's header at height, pinned to it. An error other
// than "not found" sidelines it (Manager.Sideline): it cannot serve the witness's reads.
func witnessHeader(ctx context.Context, mgr *endpoints.Manager, client *ethclient.Client, name string, height int64) (*etypes.Header, error) {
	h, err := client.HeaderByNumber(endpoints.PinContext(ctx, name), big.NewInt(height))
	if err != nil {
		if !errors.Is(err, ethereum.NotFound) {
			mgr.Sideline(name, fmt.Sprintf("cannot serve the reorg witness's block %d", height))
		}
		return nil, fmt.Errorf("endpoint %s: cannot read block %d: %w", name, height, err)
	}
	return h, nil
}

// ScannedTxHashes returns the hashes of the scanned block's transactions in the form
// block metas store them (TxMetaKey), for planning a reorg. A witness holds the scanned
// block (ReorgWitness), so it cannot call a transaction that block lists unknown; if it
// does, it is inconsistent, and the reorg step is retried instead of sending an errata
// (a backstop). Its answer for any other transaction, and a failed receipt for a
// listed one, are used as they are.
func ScannedTxHashes(txs etypes.Transactions) map[string]bool {
	listed := make(map[string]bool, len(txs))
	for _, tx := range txs {
		if tx != nil {
			listed[TxMetaKey(tx.Hash().Hex())] = true
		}
	}
	return listed
}

// TxMetaKey is a transaction hash in the form block metas store it: lowercase hex
// without the 0x prefix.
func TxMetaKey(hash string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(hash, "0x"), "0X"))
}
