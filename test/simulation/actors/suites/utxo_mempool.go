package suites

import (
	rand "math/rand/v2"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/actors/features"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// UTXO Mempool
////////////////////////////////////////////////////////////////////////////////////////

// UtxoMempool probes UTXO mempool spend visibility across all UTXO chains.
//
// Background: Bitcoin-style RPCs (listunspent) generally exclude UTXOs that have
// already been spent by transactions still in the mempool, so the UTXO set a signer
// sees is already "mempool-aware". Zcashd and Zebra behave differently for the
// address-indexed RPC (getaddressutxos): they can continue to return UTXOs that are
// already spent by an unconfirmed transaction. That is non-standard for the UTXO
// chains we support and can lead to double-spend attempts if the signer relies on
// getaddressutxos alone.
//
// This suite reproduces the issue by broadcasting a self-spend, extracting its input
// outpoints, and then checking whether the chain's UTXO listing still returns those
// spent inputs while the transaction is only in mempool. If the spent outpoints are
// still visible, the suite records a soft-fail to prove the behavior and validate
// the need for temporal storage + reservation filtering in the signer pipeline.
func UtxoMempool(rng *rand.Rand) *Actor {
	a := NewActor("UTXO Mempool", rng)
	for _, chain := range common.GetUTXOChains() {
		a.Append(features.NewUTXOMempoolActor(chain, rng))
	}
	a.Append(features.NewUTXOMempoolSummaryActor(rng))
	return a
}
