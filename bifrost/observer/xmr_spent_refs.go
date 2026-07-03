package observer

import (
	"bytes"
	"sort"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	tctypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// xmrSpentRefsConflictKey intentionally excludes the ref set. Different ref
// sets for the same outbound must collide so only one authorized set can reach
// the sidecar at a time.
type xmrSpentRefsConflictKey struct {
	chain          string
	txID           string
	observedPubKey string
}

type xmrSpentRefsImportState int

const (
	// xmrSpentRefsImportPending: an import of this ref set may be in flight;
	// differing authorized sets are blocked until the outcome is known.
	xmrSpentRefsImportPending xmrSpentRefsImportState = iota
	// xmrSpentRefsImportConfirmed: the local sidecar accepted this ref set;
	// differing sets stay blocked (the sidecar bindings are authoritative).
	xmrSpentRefsImportConfirmed
	// xmrSpentRefsImportFailed: the import failed terminally or exhausted its
	// retries; a differing authorized set may replace this entry so one bad
	// authorized attestation cannot pin the txid until restart.
	xmrSpentRefsImportFailed
)

type xmrSpentRefsConflictEntry struct {
	identity  string
	state     xmrSpentRefsImportState
	updatedAt time.Time
}

// xmrSpentRefsConflictKeyFor identifies the outbound across gossip payloads,
// without depending on the order or contents of refs. The tx ID is normalized
// because TxID equality is case-insensitive (and hex validation trims and
// accepts mixed case), so case variants of the same spend tx must collide
// here rather than evade conflict detection. PubKey equality is exact, so a
// case-variant pubkey fails the authority checks long before this point and
// needs no normalization.
func xmrSpentRefsConflictKeyFor(obsTx common.ObservedTx) xmrSpentRefsConflictKey {
	return xmrSpentRefsConflictKey{
		chain:          obsTx.Tx.Chain.String(),
		txID:           strings.ToLower(strings.TrimSpace(obsTx.Tx.ID.String())),
		observedPubKey: obsTx.ObservedPubKey.String(),
	}
}

type plannedXMROutbound struct {
	chain            common.Chain
	toAddress        common.Address
	vaultPubKey      common.PubKey
	vaultPubKeyFrost common.PubKey
	coins            common.Coins
	memo             string
	maxGas           common.Gas
}

// shouldApplyXMRSpentRefsFromAttestation decides whether a newly received XMR
// outbound attestation is allowed to seed the local sidecar with spent refs.
//
// Returning true does not mean this node has observed or attested the outbound.
// It only permits a sidecar import. The sidecar still verifies the Monero spend,
// and the scanner must later emit a local observation before this node votes.
//
// Local attestations keep the existing local trust path. Peer attestations need
// extra authority checks because the sidecar can verify Monero spend facts, but
// it cannot verify THORChain intent such as the planned destination, amount, or
// migration memo.
func (s *AttestationGossip) shouldApplyXMRSpentRefsFromAttestation(obsTx common.ObservedTx, att *common.Attestation) bool {
	if att == nil {
		return false
	}
	if bytes.Equal(att.PubKey, s.pubKey) {
		// This node produced the attestation, so the refs came from its local
		// signing/observation path. No peer authority check is needed here, but
		// the ref set still seeds conflict detection for later peer variants.
		return s.rememberAuthorizedXMRSpentRefs(obsTx)
	}
	// AddAttestation has already verified the signature over the exact
	// ObservedTx payload. The checks below decide whether this peer is an
	// acceptable authority for source-vault spent refs before quorum.
	if !s.isActiveAttesterPubKey(att.PubKey) {
		s.logger.Debug().
			Str("tx_id", obsTx.Tx.ID.String()).
			Msg("skip peer xmr spent refs: attester is not an active validator")
		return false
	}
	if !s.attesterInObservedVaultMembership(obsTx.ObservedPubKey, att.PubKey) {
		s.logger.Debug().
			Str("tx_id", obsTx.Tx.ID.String()).
			Str("vault", obsTx.ObservedPubKey.String()).
			Msg("skip peer xmr spent refs: attester is not in source vault membership")
		return false
	}
	if !s.observedXMRMatchesPlannedOutbound(obsTx) {
		s.logger.Debug().
			Str("tx_id", obsTx.Tx.ID.String()).
			Str("memo", obsTx.Tx.Memo).
			Msg("skip peer xmr spent refs: observed outbound does not match planned txout")
		return false
	}
	if !s.rememberAuthorizedXMRSpentRefs(obsTx) {
		return false
	}
	return true
}

// rememberAuthorizedXMRSpentRefs reserves the outbound/ref identity before the
// sidecar import runs. Pending or confirmed identities block differing sets;
// terminally failed identities can be replaced so one bad authorized peer does
// not pin the txid for the process lifetime.
func (s *AttestationGossip) rememberAuthorizedXMRSpentRefs(obsTx common.ObservedTx) bool {
	key := xmrSpentRefsConflictKeyFor(obsTx)
	identity := xmrSpentRefsIdentity(obsTx.SpentOutputRefs)
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authorizedXMRSpentRefs == nil {
		s.authorizedXMRSpentRefs = make(map[xmrSpentRefsConflictKey]*xmrSpentRefsConflictEntry)
	}
	entry, ok := s.authorizedXMRSpentRefs[key]
	if !ok {
		s.authorizedXMRSpentRefs[key] = &xmrSpentRefsConflictEntry{
			identity:  identity,
			state:     xmrSpentRefsImportPending,
			updatedAt: now,
		}
		return true
	}
	if entry.identity == identity {
		// Same set again: a new import attempt may start, so a previously
		// failed entry goes back to pending. A confirmed entry stays confirmed.
		if entry.state == xmrSpentRefsImportFailed {
			entry.state = xmrSpentRefsImportPending
		}
		entry.updatedAt = now
		return true
	}
	if entry.state == xmrSpentRefsImportFailed {
		// The recorded set never made it into the sidecar, so it holds no
		// authority; let the differing authorized set take its place.
		s.logger.Warn().
			Str("tx_id", obsTx.Tx.ID.String()).
			Str("vault", obsTx.ObservedPubKey.String()).
			Msg("replacing failed authorized xmr spent ref set with differing authorized set")
		entry.identity = identity
		entry.state = xmrSpentRefsImportPending
		entry.updatedAt = now
		return true
	}
	s.logger.Warn().
		Str("tx_id", obsTx.Tx.ID.String()).
		Str("vault", obsTx.ObservedPubKey.String()).
		Msg("skip peer xmr spent refs: conflicting authorized ref set already seen")
	return false
}

// resolveAuthorizedXMRSpentRefs records the final import outcome for a ref set
// previously admitted by rememberAuthorizedXMRSpentRefs. The identity guard
// keeps a late outcome from clobbering an entry that was replaced meanwhile.
func (s *AttestationGossip) resolveAuthorizedXMRSpentRefs(key xmrSpentRefsConflictKey, identity string, applied bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.authorizedXMRSpentRefs[key]
	if !ok || entry.identity != identity {
		return
	}
	if applied {
		entry.state = xmrSpentRefsImportConfirmed
	} else if entry.state != xmrSpentRefsImportConfirmed {
		entry.state = xmrSpentRefsImportFailed
	}
	entry.updatedAt = time.Now()
}

// recordCommittedXMRSpentRefs stores a thornode-committed quorum ref set that
// the local sidecar accepted. Committed refs outrank pre-quorum gossip, so this
// overwrites whatever conflict memory an earlier attestation left behind.
func (s *AttestationGossip) recordCommittedXMRSpentRefs(key xmrSpentRefsConflictKey, identity string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.authorizedXMRSpentRefs == nil {
		s.authorizedXMRSpentRefs = make(map[xmrSpentRefsConflictKey]*xmrSpentRefsConflictEntry)
	}
	s.authorizedXMRSpentRefs[key] = &xmrSpentRefsConflictEntry{
		identity:  identity,
		state:     xmrSpentRefsImportConfirmed,
		updatedAt: time.Now(),
	}
}

// pruneAuthorizedXMRSpentRefsLocked expires conflict memory. The TTL is double
// the non-quorum attestation timeout so the memory comfortably outlives the
// observed-tx state buckets it protects, while still bounding the map. The
// caller must hold s.mu.
func (s *AttestationGossip) pruneAuthorizedXMRSpentRefsLocked(now time.Time) {
	ttl := 2 * s.config.NonQuorumTimeout
	if ttl <= 0 {
		ttl = 2 * defaultNonQuorumTimeout
	}
	for key, entry := range s.authorizedXMRSpentRefs {
		if now.Sub(entry.updatedAt) > ttl {
			delete(s.authorizedXMRSpentRefs, key)
		}
	}
}

// xmrSpentRefsIdentity sorts refs before joining them so gossip order does not
// create a false conflict for the same sidecar binding.
func xmrSpentRefsIdentity(refs []common.OutputRef) string {
	if len(refs) == 0 {
		return ""
	}
	ids := make([]string, len(refs))
	for i, ref := range refs {
		ids[i] = common.SpentOutputRefIdentity(ref)
	}
	sort.Strings(ids)
	return strings.Join(ids, "|")
}

// isActiveAttesterPubKey maps the raw secp256k1 attestation key to the peer ID
// used by the gossip layer and checks it against the current active validator
// set. THORChain accounting only accepts active-validator observations, so peer
// spent-ref authority follows the same active set.
func (s *AttestationGossip) isActiveAttesterPubKey(pubKey []byte) bool {
	peerID, err := conversion.GetPeerIDFromSecp256PubKey(pubKey)
	if err != nil {
		s.logger.Debug().Err(err).Msg("skip peer xmr spent refs: cannot derive attester peer id")
		return false
	}
	return s.isActiveValidator(peerID)
}

// attesterInObservedVaultMembership requires the peer introducing spent refs to
// be a member of the source XMR vault. Newly churned-in validators may import
// refs so they can observe, but they should not be able to introduce refs for an
// old-vault spend unless they were part of that source vault.
func (s *AttestationGossip) attesterInObservedVaultMembership(vaultPubKey common.PubKey, attesterPubKey []byte) bool {
	if s.bridge == nil || vaultPubKey.IsEmpty() {
		return false
	}
	vault, err := s.bridge.GetVault(vaultPubKey.String())
	if err != nil {
		s.logger.Debug().Err(err).Str("vault", vaultPubKey.String()).Msg("skip peer xmr spent refs: cannot load source vault")
		return false
	}
	if vault.IsEmpty() {
		return false
	}
	// XMR vault observations use the FROST pubkey when present. Fall back to the
	// regular vault pubkey for older/non-FROST records so the check remains
	// compatible with both vault shapes.
	if !vault.PubKeyFrost.IsEmpty() {
		if !vault.PubKeyFrost.Equals(vaultPubKey) {
			return false
		}
	} else if !vault.PubKey.Equals(vaultPubKey) {
		return false
	}
	return rawSecpPubKeyInPubKeys(attesterPubKey, vault.GetMembership())
}

// rawSecpPubKeyInPubKeys compares the raw secp256k1 key carried in an
// attestation with the bech32 account pubkeys stored in vault membership.
func rawSecpPubKeyInPubKeys(raw []byte, pubKeys common.PubKeys) bool {
	for _, pubKey := range pubKeys {
		pk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeAccPub, pubKey.String())
		if err != nil {
			continue
		}
		if bytes.Equal(pk.Bytes(), raw) {
			return true
		}
	}
	return false
}

// observedXMRMatchesPlannedOutbound checks THORChain intent before importing
// peer refs. This is the Bifrost-side counterpart to sidecar verification:
// Bifrost proves the tx was scheduled by THORChain, while the sidecar proves the
// Monero spend really contains the claimed spent outputs.
func (s *AttestationGossip) observedXMRMatchesPlannedOutbound(obsTx common.ObservedTx) bool {
	if s.bridge == nil {
		return false
	}
	parsed, err := memo.ParseMemo(common.LatestVersion, obsTx.Tx.Memo)
	if err != nil {
		s.logger.Debug().Err(err).Str("memo", obsTx.Tx.Memo).Msg("skip peer xmr spent refs: cannot parse memo")
		return false
	}

	switch {
	case parsed.IsType(memo.TxMigrate), parsed.IsType(memo.TxRagnarok):
		// Migration/ragnarok memos carry the keysign height. Load the whole
		// public txout for that height because a newly churned-in validator may
		// not have the old vault pubkey in its local signer-key list.
		return s.observedXMRMatchesKeysignHeight(obsTx, parsed.GetBlockHeight())
	case parsed.IsType(memo.TxOutbound), parsed.IsType(memo.TxRefund):
		// Regular outbounds/refunds are tied back to the inbound voter actions,
		// which are THORChain's planned txouts for that inbound.
		return s.observedXMRMatchesInboundVoterAction(obsTx, parsed.GetTxID())
	default:
		return false
	}
}

// observedXMRMatchesKeysignHeight matches a migration/ragnarok observation
// against one of the planned txouts in the keysign block referenced by the memo.
func (s *AttestationGossip) observedXMRMatchesKeysignHeight(obsTx common.ObservedTx, height int64) bool {
	if height <= 0 {
		return false
	}
	// Empty pubkey selects /thorchain/keysign/{height}. This is intentionally
	// broader than /thorchain/keysign/{height}/{pubkey} so validators outside
	// the old source vault can still verify a planned migration before import.
	txOut, err := s.bridge.GetKeysign(height, "")
	if err != nil {
		s.logger.Debug().Err(err).Int64("height", height).Msg("skip peer xmr spent refs: cannot load keysign block")
		return false
	}
	for _, item := range txOut.TxArray {
		planned := plannedXMROutboundFromThorclient(item.TxOutItem(txOut.Height))
		if xmrObservedTxMatchesPlanned(obsTx, planned) {
			return true
		}
	}
	return false
}

// observedXMRMatchesInboundVoterAction matches non-migration XMR outbounds
// against the actions recorded on the inbound observed-tx voter.
func (s *AttestationGossip) observedXMRMatchesInboundVoterAction(obsTx common.ObservedTx, inboundID common.TxID) bool {
	if inboundID.IsEmpty() {
		return false
	}
	voter, err := s.bridge.GetObservedTxVoter(inboundID.String())
	if err != nil {
		s.logger.Debug().Err(err).Str("inbound_id", inboundID.String()).Msg("skip peer xmr spent refs: cannot load observed tx voter")
		return false
	}
	if voter == nil {
		return false
	}
	for _, item := range voter.GetActions() {
		if xmrObservedTxMatchesPlanned(obsTx, plannedXMROutboundFromThorchain(item)) {
			return true
		}
	}
	return false
}

// plannedXMROutboundFromThorclient normalizes the bifrost thorclient txout
// representation into the smaller field set needed by the matcher.
func plannedXMROutboundFromThorclient(item tctypes.TxOutItem) plannedXMROutbound {
	return plannedXMROutbound{
		chain:            item.Chain,
		toAddress:        item.ToAddress,
		vaultPubKey:      item.VaultPubKey,
		vaultPubKeyFrost: item.VaultPubKeyFrost,
		coins:            item.Coins,
		memo:             item.GetMemo(),
		maxGas:           item.MaxGas,
	}
}

// plannedXMROutboundFromThorchain normalizes the THORChain query type used by
// observed-tx voters into the same matcher input as keysign txouts.
func plannedXMROutboundFromThorchain(item thortypes.TxOutItem) plannedXMROutbound {
	return plannedXMROutbound{
		chain:            item.Chain,
		toAddress:        item.ToAddress,
		vaultPubKey:      item.VaultPubKey,
		vaultPubKeyFrost: item.VaultPubKeyFrost,
		coins:            common.NewCoins(item.Coin),
		memo:             item.GetMemo(),
		maxGas:           item.MaxGas,
	}
}

// xmrObservedTxMatchesPlanned is intentionally strict. A peer can only seed
// spent refs for an observed tx that matches THORChain's planned outbound on the
// fields that affect accounting and destination safety.
func xmrObservedTxMatchesPlanned(obsTx common.ObservedTx, planned plannedXMROutbound) bool {
	if !obsTx.Tx.Chain.Equals(common.XMRChain) || !planned.chain.Equals(common.XMRChain) {
		return false
	}
	if len(obsTx.SpentOutputRefs) == 0 || len(obsTx.SpentOutputRefs) > common.MaxXMRSpentOutputRefs {
		return false
	}
	// Prefer the FROST pubkey because XMR monovault observations are attributed
	// to the FROST key. Keep the regular pubkey fallback for non-FROST records.
	plannedVault := planned.vaultPubKeyFrost
	if plannedVault.IsEmpty() {
		plannedVault = planned.vaultPubKey
	}
	if plannedVault.IsEmpty() || !plannedVault.Equals(obsTx.ObservedPubKey) {
		return false
	}
	// The observed FromAddress must be the XMR address derived from the observed
	// vault pubkey. This prevents a planned destination/amount from being paired
	// with an outbound attributed to a different source.
	vaultAddress, err := obsTx.ObservedPubKey.GetAddress(common.XMRChain)
	if err != nil || !obsTx.Tx.FromAddress.Equals(vaultAddress) {
		return false
	}
	if planned.toAddress.IsEmpty() || !obsTx.Tx.ToAddress.Equals(planned.toAddress) {
		return false
	}
	if !strings.EqualFold(obsTx.Tx.Memo, planned.memo) {
		return false
	}
	if !observedCoinsMatchPlannedXMR(obsTx, planned) {
		return false
	}
	return observedGasWithinPlannedMax(obsTx.Tx.Gas, planned.maxGas)
}

// observedCoinsMatchPlannedXMR accepts the exact planned amount for normal XMR
// outbounds, and also accepts MIGRATE observations where the signer swept the
// unused MaxGas budget into the destination output. Without this exception,
// Bifrost rejects a successful full-vault migration observation and THORNode
// keeps retrying a txout after the old vault has already been drained.
func observedCoinsMatchPlannedXMR(obsTx common.ObservedTx, planned plannedXMROutbound) bool {
	if obsTx.Tx.Coins.EqualsEx(planned.coins) {
		return true
	}
	parsed, err := memo.ParseMemo(common.LatestVersion, obsTx.Tx.Memo)
	if err != nil || !parsed.IsType(memo.TxMigrate) {
		return false
	}
	observedCoin := obsTx.Tx.Coins.GetCoin(common.XMRAsset)
	plannedCoin := planned.coins.GetCoin(common.XMRAsset)
	observedGas := obsTx.Tx.Gas.ToCoins().GetCoin(common.XMRAsset)
	plannedMaxGas := planned.maxGas.ToCoins().GetCoin(common.XMRAsset)
	if observedCoin.IsEmpty() || plannedCoin.IsEmpty() || observedGas.IsEmpty() || plannedMaxGas.IsEmpty() {
		return false
	}
	if observedGas.Amount.GT(plannedMaxGas.Amount) {
		return false
	}
	unusedMaxGas := common.SafeSub(plannedMaxGas.Amount, observedGas.Amount)
	expectedAmount := plannedCoin.Amount.Add(unusedMaxGas)
	if observedCoin.Amount.Equal(expectedAmount) {
		return true
	}
	if unusedMaxGas.IsZero() {
		return false
	}

	// XMR observations are converted from atomic units to THORChain units by
	// flooring. For a MIGRATE sweep whose atomic fee has a remainder, observed gas
	// is floor(fee), while the swept amount is floor(maxGas-fee), making the
	// observed amount one THORChain unit lower than maxGas-floor(fee).
	return observedCoin.Amount.Equal(expectedAmount.Sub(cosmos.OneUint()))
}

// observedGasWithinPlannedMax rejects zero-fee observations and enforces the
// planned MaxGas bound when THORChain supplied one. If MaxGas is absent, the gas
// check falls back to requiring a non-zero XMR fee.
func observedGasWithinPlannedMax(observed, plannedMax common.Gas) bool {
	observedCoin := observed.ToCoins().GetCoin(common.XMRAsset)
	if observedCoin.IsEmpty() || observedCoin.Amount.IsZero() {
		return false
	}
	plannedMaxCoin := plannedMax.ToCoins().GetCoin(common.XMRAsset)
	if plannedMaxCoin.IsEmpty() {
		return true
	}
	return !observedCoin.Amount.GT(plannedMaxCoin.Amount)
}
