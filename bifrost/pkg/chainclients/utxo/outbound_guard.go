package utxo

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcjson"
	"github.com/btcsuite/btcd/chaincfg/chainhash"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	mem "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Client - Existing Outbound Guard
////////////////////////////////////////////////////////////////////////////////////////

// maxReportedOutboundCandidates bounds how many outbounds reported to THORChain
// for one inbound are looked up on the daemon before a single signing attempt.
const maxReportedOutboundCandidates = 256

// The guard runs under the vault's signing lock, and decoding one candidate
// costs a daemon read per input. One signing attempt spends at most this many
// daemon reads and this much time on candidates, whatever they claim.
const (
	maxOutboundGuardDaemonReads = 1024
	outboundGuardTimeout        = 30 * time.Second
)

// outboundGuardBudget is the daemon work left for one signing attempt. A nil
// budget is unbounded. Its context ends at the deadline, which also ends a
// daemon read and its retries in progress.
type outboundGuardBudget struct {
	reads    int
	deadline time.Time
	ctx      context.Context
	cancel   context.CancelFunc
}

func newOutboundGuardBudget() *outboundGuardBudget {
	deadline := time.Now().Add(outboundGuardTimeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	return &outboundGuardBudget{reads: maxOutboundGuardDaemonReads, deadline: deadline, ctx: ctx, cancel: cancel}
}

func (b *outboundGuardBudget) context() context.Context {
	if b == nil {
		return context.Background()
	}
	return b.ctx
}

func (b *outboundGuardBudget) release() {
	if b != nil {
		b.cancel()
	}
}

// take spends n daemon reads, or reports false if they are not left.
func (b *outboundGuardBudget) take(n int) bool {
	if b == nil {
		return true
	}
	if b.reads < n || time.Now().After(b.deadline) {
		b.reads = 0
		return false
	}
	b.reads -= n
	return true
}

// outboundCandidate is a transaction that may already pay an outbound, and
// whether this node's own observer attributed it to a vault.
type outboundCandidate struct {
	hash      string
	selfLocal bool
}

// existingOutboundPayment reports whether a payment for the same outbound as tx
// is already live on this node's own daemon: in the mempool or confirmed.
//
// The signer cache already skips an outbound whose exact payload this node has
// signed or observed. This check covers the same outbound scheduled again, for
// instance to another vault, and matches it the way THORChain matches an
// observed outbound to its scheduled item: sent by a vault, a memo naming the
// same inbound, the same destination, and an amount within the outbound
// handler's tolerances.
//
// Candidate transactions only ever narrow the decision: each is re-read from the
// node's own daemon and must match on-chain before it blocks signing. A
// candidate this node recorded itself is trusted to be a vault outbound for the
// inbound, so an inconclusive lookup of it fails closed for this attempt; the
// item comes back when it is rescheduled.
func (c *Client) existingOutboundPayment(tx stypes.TxOutItem) (string, signercache.LookupResult) {
	if !outboundGuardApplies(tx) {
		return "", signercache.LookupNotFound
	}
	hashes, err := c.signerCacheManager.ObservedOutbounds(tx.InHash.String())
	if err != nil {
		c.log.Err(err).Stringer("in_hash", tx.InHash).Msg("fail to read observed outbounds")
		return "", signercache.LookupAmbiguous
	}
	candidates := make([]outboundCandidate, 0, len(hashes))
	seen := make(map[string]bool, len(hashes))
	for _, hash := range hashes {
		seen[strings.ToUpper(hash)] = true
		candidates = append(candidates, outboundCandidate{hash: hash, selfLocal: true})
	}

	// THORChain also lists the outbounds other nodes reported for the inbound,
	// before they reach consensus. Those reports are unverified, so they are
	// only ever checked against this node's own daemon.
	voter, found, err := c.bridge.GetObservedTxVoterUncached(tx.InHash.String())
	if err != nil {
		c.log.Debug().Err(err).Stringer("in_hash", tx.InHash).Msg("fail to get reported outbounds")
	} else if found && voter != nil {
		reported := 0
		for _, hash := range voter.ObservedOutboundTxIds {
			if seen[strings.ToUpper(hash)] {
				continue
			}
			// Skip IDs that cannot be a Bitcoin-family transaction ID. This is
			// a format check only: other chains' 64-hex IDs (EVM, Cosmos) pass
			// it and use lookups. The query ranks consensus links first, so
			// the budget still reaches the strongest reports.
			if _, err := chainhash.NewHashFromStr(hash); err != nil {
				continue
			}
			if reported == maxReportedOutboundCandidates {
				c.log.Warn().Stringer("in_hash", tx.InHash).Int("reported", len(voter.ObservedOutboundTxIds)).
					Msg("too many reported outbounds, checking the first ones only")
				break
			}
			seen[strings.ToUpper(hash)] = true
			reported++
			candidates = append(candidates, outboundCandidate{hash: hash})
		}
	}
	if !found || voter == nil || len(voter.Actions) == 0 {
		// Without the action list a payment cannot be assigned to one split leg.
		// A local record still makes signing unsafe until that list is readable.
		if len(hashes) > 0 {
			return hashes[0], signercache.LookupAmbiguous
		}
		return "", signercache.LookupNotFound
	}
	return c.matchExistingOutboundForActions(tx, candidates, voter.Actions)
}

// matchExistingOutbound returns the first candidate that is live and pays tx,
// LookupAmbiguous if a locally recorded candidate could not be checked, or
// LookupNotFound.
func (c *Client) matchExistingOutbound(tx stypes.TxOutItem, candidates []outboundCandidate) (string, signercache.LookupResult) {
	return c.matchExistingOutboundForActions(tx, candidates, nil)
}

func (c *Client) matchExistingOutboundForActions(tx stypes.TxOutItem, candidates []outboundCandidate, actions []ttypes.TxOutItem) (string, signercache.LookupResult) {
	budget := newOutboundGuardBudget()
	defer budget.release()
	result := signercache.LookupNotFound
	ambiguous := ""
	// Live payments of tx, or of a sibling action it could be confused with,
	// seen so far; candidates are distinct transactions. A sibling's own
	// payment shows that an ambiguous payment fitting both must be tx's.
	var payments []stypes.TxInItem
	var paymentHashes []string
	for _, candidate := range candidates {
		if _, err := chainhash.NewHashFromStr(candidate.hash); err != nil {
			continue
		}
		var observed stypes.TxInItem
		var presence signercache.LookupResult
		if c.outboundLookup == nil {
			// An exhausted budget leaves a local record inconclusive, so the
			// attempt fails closed; a reported candidate is only skipped.
			observed, presence = c.lookupOutboundCandidateBudgeted(candidate.hash, tx.InHash, candidate.selfLocal, budget)
		} else {
			observed, presence = c.outboundLookup(candidate.hash, tx.InHash)
		}
		switch presence {
		case signercache.LookupNotFound:
			continue
		case signercache.LookupAmbiguous:
			if candidate.selfLocal && ambiguous == "" {
				ambiguous = candidate.hash
				result = signercache.LookupAmbiguous
			}
			continue
		}
		if observed.Tx == "" && candidate.selfLocal {
			if ambiguous == "" {
				ambiguous = candidate.hash
				result = signercache.LookupAmbiguous
			}
			continue
		}
		if !c.outboundMayPayItemFrom(observed, tx, candidate.selfLocal) {
			continue
		}
		if len(actions) == 0 {
			if c.outboundPaymentAmountMatches(observed, tx) {
				return candidate.hash, signercache.LookupFound
			}
			continue
		}
		if len(actionsPaymentFits(observed, tx, actions)) == 0 {
			continue
		}
		payments = append(payments, observed)
		paymentHashes = append(paymentHashes, candidate.hash)
		if paid := paymentsCoverActions(payments, tx, actions); paid >= 0 {
			return paymentHashes[paid], signercache.LookupFound
		}
	}
	return ambiguous, result
}

// lookupOutboundCandidate reads a transaction from this node's daemon and
// decodes it the way the observer does.
//
// Decoding costs a daemon read for the sender and one per input, and any single
// node can report a hash for an inbound, so a candidate whose own memo cannot
// name inHash is rejected from the one read of the transaction before it is
// decoded. outboundPaysItem would reject it on the same memo afterwards.
func (c *Client) lookupOutboundCandidate(hash string, inHash common.TxID) (stypes.TxInItem, signercache.LookupResult) {
	return c.lookupOutboundCandidateBudgeted(hash, inHash, false, nil)
}

// lookupOutboundCandidateBudgeted is lookupOutboundCandidate spending daemon
// reads from budget. A reported candidate must be sent by a current vault; that
// is checked from the sender's one read, before the reads behind every input.
func (c *Client) lookupOutboundCandidateBudgeted(hash string, inHash common.TxID, selfLocal bool, budget *outboundGuardBudget) (stypes.TxInItem, signercache.LookupResult) {
	if !budget.take(1) {
		return stypes.TxInItem{}, signercache.LookupAmbiguous
	}
	verbose, err := c.rpc.GetRawTransactionVerboseContext(budget.context(), hash)
	presence := utxoVerifyPresence(verbose, err)
	if presence != signercache.LookupFound {
		return stypes.TxInItem{}, presence
	}
	if verbose == nil || len(verbose.Vin) == 0 {
		return stypes.TxInItem{}, signercache.LookupAmbiguous
	}
	if !c.candidateMemoMayNameInbound(verbose, inHash) {
		return stypes.TxInItem{}, signercache.LookupNotFound
	}
	if !budget.take(1) {
		return stypes.TxInItem{}, signercache.LookupAmbiguous
	}
	vinZero, err := c.rpc.GetRawTransactionVerboseContext(budget.context(), verbose.Vin[0].Txid)
	if err != nil || vinZero == nil || int(verbose.Vin[0].Vout) >= len(vinZero.Vout) {
		return stypes.TxInItem{}, signercache.LookupAmbiguous
	}
	vinZeroTxs := map[string]*btcjson.TxRawResult{verbose.Vin[0].Txid: vinZero}
	if !selfLocal {
		sender, senderErr := c.getSender(verbose, vinZeroTxs)
		if senderErr != nil {
			return stypes.TxInItem{}, signercache.LookupAmbiguous
		}
		if !c.isAsgardAddress(sender) {
			return stypes.TxInItem{}, signercache.LookupNotFound
		}
	}
	// Decoding needs the transaction behind each input for the gas. Read them
	// here, so the budget and its deadline are checked before every read.
	for _, vin := range verbose.Vin {
		if _, ok := vinZeroTxs[vin.Txid]; ok {
			continue
		}
		if !budget.take(1) {
			return stypes.TxInItem{}, signercache.LookupAmbiguous
		}
		vinTx, vinErr := c.rpc.GetRawTransactionVerboseContext(budget.context(), vin.Txid)
		if vinErr != nil || vinTx == nil {
			return stypes.TxInItem{}, signercache.LookupAmbiguous
		}
		vinZeroTxs[vin.Txid] = vinTx
	}
	// Decode as a confirmed transaction so a replaceable mempool payment is
	// still recognised.
	observed, err := c.getTxInWithVaultSender(verbose, 0, false, vinZeroTxs, selfLocal)
	if err != nil {
		c.log.Err(err).Str("txid", hash).Msg("fail to decode outbound candidate")
		return stypes.TxInItem{}, signercache.LookupAmbiguous
	}
	if observed.Tx == "" {
		return stypes.TxInItem{}, signercache.LookupAmbiguous
	}
	return observed, signercache.LookupFound
}

// candidateMemoMayNameInbound reports whether a transaction's memo could name
// inHash, without the daemon reads a full decode makes. A memo that cannot be
// read here is left to the full decode, which reports it as inconclusive.
func (c *Client) candidateMemoMayNameInbound(verbose *btcjson.TxRawResult, inHash common.TxID) bool {
	memo, err := c.getMemo(verbose)
	if err != nil || len([]byte(memo)) > constants.MaxMemoSize {
		return true
	}
	m, err := mem.ParseMemo(common.LatestVersion, memo)
	return err == nil && m.GetTxID().Equals(inHash)
}

// outboundPaysItem reports whether an observed transaction is a vault's payment
// of the scheduled outbound tx.
func (c *Client) outboundPaysItem(observed stypes.TxInItem, tx stypes.TxOutItem) bool {
	return c.outboundPaysItemFrom(observed, tx, false)
}

func (c *Client) outboundPaysItemFrom(observed stypes.TxInItem, tx stypes.TxOutItem, selfLocal bool) bool {
	return c.outboundMayPayItemFrom(observed, tx, selfLocal) && c.outboundPaymentAmountMatches(observed, tx)
}

// outboundMayPayItemFrom is outboundPaysItemFrom without the amount check: a
// vault payment for tx's inbound to tx's destination, which pays tx or one of
// its sibling actions.
func (c *Client) outboundMayPayItemFrom(observed stypes.TxInItem, tx stypes.TxOutItem, selfLocal bool) bool {
	if observed.Tx == "" || (!selfLocal && !c.isAsgardAddress(observed.Sender)) {
		return false
	}
	memo, err := mem.ParseMemo(common.LatestVersion, observed.Memo)
	if err != nil || !memo.GetTxID().Equals(tx.InHash) {
		return false
	}
	to := common.Address(c.stripBCHAddress(observed.To))
	scheduledTo := common.Address(c.stripBCHAddress(tx.ToAddress.String()))
	return common.EquivalentAddresses(c.cfg.ChainID, to, scheduledTo)
}

// outboundPaymentAmountMatches reports whether observed pays tx's amount.
func (c *Client) outboundPaymentAmountMatches(observed stypes.TxInItem, tx stypes.TxOutItem) bool {
	gasAsset := c.cfg.ChainID.GetGasAsset()
	scheduled := tx.Coins.GetCoin(gasAsset).Amount
	paid := observed.Coins.GetCoin(gasAsset).Amount
	if scheduled.IsZero() || paid.IsZero() {
		return false
	}
	return outboundAmountMatches(scheduled, tx.MaxGas.ToCoins().GetCoin(gasAsset).Amount,
		paid, observed.Gas.ToCoins().GetCoin(gasAsset).Amount)
}

// A payment can suppress signing only when it identifies one scheduled action,
// and that action is the one being signed. Split legs can share a destination
// and even the same total spend after gas is accounted for. Memos compare as
// THORChain matches actions: the original memo of a memoless outbound, and
// without regard to case.
func candidatePaysUniqueAction(observed stypes.TxInItem, tx stypes.TxOutItem, actions []ttypes.TxOutItem) bool {
	return paymentsCoverActions([]stypes.TxInItem{observed}, tx, actions) >= 0
}

// paymentsCoverActions reports whether live payments show that tx is paid, and
// returns the index of a payment that could be tx's, or -1.
//
// Each payment pays one action and each action is paid at most once. tx could
// be any action that looks like it (an equal split part to one destination),
// so it counts as paid only when no such assignment of the payments leaves one
// of those unpaid; see common.PaymentsCoverTargets. One payment against two
// look-alike parts lets the unpaid part be signed, while a payment for each
// stops a further one. A payment that only a sibling's amount fits also
// counts: with unequal legs, one payment can fit both while the sibling's own
// payment fits only the sibling, and together they show both legs are paid. A
// single payment that identifies tx alone is the unique match.
func paymentsCoverActions(payments []stypes.TxInItem, tx stypes.TxOutItem, actions []ttypes.TxOutItem) int {
	var targets []int
	for i, action := range actions {
		if actionLooksLike(action, tx) {
			targets = append(targets, i)
		}
	}
	fits := make([][]int, len(payments))
	first := -1
	for p, payment := range payments {
		fits[p] = actionsPaymentFits(payment, tx, actions)
		if first < 0 && slices.ContainsFunc(fits[p], func(i int) bool { return slices.Contains(targets, i) }) {
			first = p
		}
	}
	if first < 0 || !common.PaymentsCoverTargets(fits, len(actions), targets) {
		return -1
	}
	return first
}

// actionLooksLike reports whether action is one tx could be: a payment of the
// same amount for the same inbound to the same destination.
func actionLooksLike(action ttypes.TxOutItem, tx stypes.TxOutItem) bool {
	return action.Chain.Equals(tx.Chain) && action.InHash.Equals(tx.InHash) &&
		common.EquivalentAddresses(tx.Chain, action.ToAddress, tx.ToAddress) &&
		strings.EqualFold(action.GetMemo(), tx.GetMemo()) &&
		action.Coin.Amount.Equal(tx.Coins.GetCoin(tx.Chain.GetGasAsset()).Amount)
}

// actionsPaymentFits returns the indices of the actions, among those for tx's
// inbound, destination and memo, that observed could pay.
func actionsPaymentFits(observed stypes.TxInItem, tx stypes.TxOutItem, actions []ttypes.TxOutItem) []int {
	var fits []int
	for i, action := range actions {
		if !action.Chain.Equals(tx.Chain) || !action.InHash.Equals(tx.InHash) ||
			!common.EquivalentAddresses(tx.Chain, action.ToAddress, tx.ToAddress) ||
			!strings.EqualFold(action.GetMemo(), tx.GetMemo()) {
			continue
		}
		gasAsset := tx.Chain.GetGasAsset()
		paid := observed.Coins.GetCoin(gasAsset).Amount
		gas := observed.Gas.ToCoins().GetCoin(gasAsset).Amount
		if !outboundAmountMatches(action.Coin.Amount, action.MaxGas.ToCoins().GetCoin(gasAsset).Amount, paid, gas) {
			continue
		}
		fits = append(fits, i)
	}
	return fits
}

// outboundAmountMatches applies THORChain's outbound amount tolerances for a
// gas asset: the exact amount, or the same total spend once gas is accounted
// for.
func outboundAmountMatches(scheduled, maxGas, paid, gas cosmos.Uint) bool {
	if paid.Equal(scheduled) {
		return true
	}
	intendToSpend := scheduled.Add(maxGas)
	return paid.LTE(intendToSpend) && intendToSpend.Sub(paid).Equal(gas)
}

// outboundGuardApplies reports whether tx pays an inbound that an earlier
// payment could be found for by its memo.
//
// A memoless outbound (EnableMemolessOutbound clears Memo on OUT and REFUND
// items) is deliberately not guarded. Its payment carries no OP_RETURN, so
// nothing on chain ties it to its inbound: this node records no observed
// outbound for it, and THORChain links none. Matching it on vault, destination
// and amount alone would also match a payment for another inbound with the
// same destination and amount, such as two equal swaps to one address, and
// refuse to sign a legitimate outbound. Such an item relies on the signer
// cache and on THORChain's recorded OutTxs, which LackSigning checks before
// recovery.
func outboundGuardApplies(tx stypes.TxOutItem) bool {
	if tx.Memo == "" || tx.InHash.IsEmpty() || tx.InHash.Equals(common.BlankTxID) {
		return false
	}
	memo, err := mem.ParseMemo(common.LatestVersion, tx.Memo)
	if err != nil || !memo.IsOutbound() {
		return false
	}
	return memo.GetTxID().Equals(tx.InHash)
}
