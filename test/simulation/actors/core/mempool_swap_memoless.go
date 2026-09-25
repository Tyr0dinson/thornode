package core

import (
	"errors"
	"fmt"
	rand "math/rand/v2"
	"strconv"
	"strings"
	"time"

	sdkmath "cosmossdk.io/math"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	mempoolMemolessSetupDollarsPerRune int64 = common.One
	mempoolMemolessSetupCapTOR         int64 = 100_000_000_000_000
	mempoolMemolessSetupPoolCapBPS     int64 = 0
	mempoolMemolessSetupMaxCount       int64 = 20
	mempoolMemolessSetupAdvQueue       int64 = 1

	mempoolMemolessPreFinalitySamples = 3

	// mempoolMemolessWindowAttempts bounds how many references an actor may spend
	// looking for POL's pre-finality window. Fifteen POL confirmations are only
	// thirty seconds of mocknet wall clock, and a loaded simulation can leave
	// Bifrost far enough behind the chain that an inbound is observed and
	// confirmation counted in the same breath. A literal reference that is
	// genuinely admitted before finality still fails every attempt.
	mempoolMemolessWindowAttempts = 2
)

// mempoolMemolessMissedWindow marks an attempt that lost the race with POL
// finality rather than one that caught a reference resolving speculatively.
type mempoolMemolessMissedWindow struct {
	err error
}

func (e *mempoolMemolessMissedWindow) Error() string { return e.err.Error() }

func (e *mempoolMemolessMissedWindow) Unwrap() error { return e.err }

type mempoolMemolessMimirSetting struct {
	key          string
	enabledValue int64
	fallback     int64
}

var mempoolMemolessMimirSettings = []mempoolMemolessMimirSetting{
	{key: "DollarsPerRune", enabledValue: mempoolMemolessSetupDollarsPerRune, fallback: 0},
	{key: constants.MempoolSwapCapTOR.String(), enabledValue: mempoolMemolessSetupCapTOR, fallback: 0},
	{key: constants.MempoolSwapPoolCapBPS.String(), enabledValue: mempoolMemolessSetupPoolCapBPS, fallback: 50},
	{key: constants.MempoolSwapMaxCount.String(), enabledValue: mempoolMemolessSetupMaxCount, fallback: 1000},
	// Mocknet sets this operational Mimir to one through its environment when no
	// on-chain value exists, so one is also the correct absent-key fallback.
	{key: constants.EnableAdvSwapQueue.String(), enabledValue: mempoolMemolessSetupAdvQueue, fallback: 1},
}

// MempoolSwapMemolessActor exercises the live finality boundary for literal
// reference memos. The registered memo resolves to an otherwise mempool-eligible
// POL.POL -> BTC.BTC market swap, but the observed raw r:<ref> is a
// ReferenceRead memo and therefore must wait for POL finality before resolution.
type MempoolSwapMemolessActor struct {
	SwapActor

	registrationTxID common.TxID
	registeredMemo   string
	reference        uint64
	referenceMemo    string
}

// NewMempoolSwapMemolessSimulationActor returns a self-contained actor. It is
// safe to append after other mempool-swap scenarios: effective Mimir values are
// snapshotted before setup and restored when the scenario finishes or fails.
func NewMempoolSwapMemolessSimulationActor(rng *rand.Rand) *Actor {
	a := &MempoolSwapMemolessActor{
		SwapActor: SwapActor{
			Actor: *NewActor("MempoolSwap-MemolessReferenceExclusion", rng),
			from:  common.POLAsset,
			to:    common.BTCAsset,
		},
	}
	// The scenario may replay a lost pre-finality race on a fresh reference,
	// after settling the inbound the previous attempt left in flight.
	a.Timeout = 16 * time.Minute
	a.Ops = append(a.Ops, a.run)
	return &a.Actor
}

func (a *MempoolSwapMemolessActor) run(config *OpConfig) OpResult {
	err := a.runScenario(config)
	return OpResult{Finish: true, Error: err}
}

func (a *MempoolSwapMemolessActor) runScenario(config *OpConfig) (err error) {
	originalMimirs, err := a.snapshotMimirs()
	if err != nil {
		return fmt.Errorf("snapshot mempool Mimirs: %w", err)
	}

	// Setup can partially succeed, so restoration must be armed before the first
	// vote rather than only after every key reaches its requested value.
	defer func() {
		if restoreErr := a.restoreMimirs(config, originalMimirs); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore mempool Mimirs: %w", restoreErr))
		}
	}()

	enabledMimirs := make(map[string]int64, len(mempoolMemolessMimirSettings))
	for _, setting := range mempoolMemolessMimirSettings {
		enabledMimirs[setting.key] = setting.enabledValue
	}
	if err = a.applyMimirs(config, enabledMimirs); err != nil {
		return fmt.Errorf("enable mempool swaps: %w", err)
	}

	if err = a.acquireSwapUser(config); err != nil {
		return err
	}
	defer func() {
		if a.user != nil {
			a.user.Release()
			a.user = nil
		}
	}()

	for attempt := 1; attempt <= mempoolMemolessWindowAttempts; attempt++ {
		err = a.runReferenceAttempt()
		var missed *mempoolMemolessMissedWindow
		if !errors.As(err, &missed) {
			return err
		}
		a.Log().Warn().
			Err(err).
			Int("attempt", attempt).
			Str("txid", a.swapTxID).
			Msg("POL finality outran the pre-finality window; retrying on a fresh reference")
	}
	return err
}

// runReferenceAttempt registers a fresh one-shot reference, broadcasts the
// literal-reference inbound, and proves it stayed outside every mempool-swap
// surface until POL finality settled it exactly once.
//
// A collapsed pre-finality window proves nothing about the ReferenceRead policy,
// so it is reported as a retryable miss. The abandoned inbound is still a swap
// that must settle correctly, so it is verified rather than merely drained: that
// both keeps its assertions and leaves the next attempt settled balances.
func (a *MempoolSwapMemolessActor) runReferenceAttempt() error {
	if err := a.refreshUserBalances(); err != nil {
		return err
	}
	if err := a.quoteSwap(); err != nil {
		return err
	}
	if err := a.registerReference(); err != nil {
		return err
	}
	if err := a.loadReference(); err != nil {
		return err
	}

	baselineUsage, err := thornode.GetMempoolSwapUsageTOR()
	if err != nil {
		return fmt.Errorf("get baseline mempool TOR usage: %w", err)
	}

	if err = a.broadcastReferenceSwap(); err != nil {
		return err
	}
	if err = a.verifyPreFinalityExclusion(baselineUsage); err != nil {
		var missed *mempoolMemolessMissedWindow
		if errors.As(err, &missed) {
			if settleErr := a.verifyFinalizedSwap(baselineUsage); settleErr != nil {
				return errors.Join(err, settleErr)
			}
		}
		return err
	}
	if err = a.verifyFinalizedSwap(baselineUsage); err != nil {
		return err
	}

	a.Log().Info().
		Str("txid", a.swapTxID).
		Str("reference_memo", a.referenceMemo).
		Msg("memoless reference correctly bypassed speculative execution and settled once after finality")
	return nil
}

// refreshUserBalances re-reads the locked user's balances so an attempt quotes
// and asserts against the state left by any earlier attempt's settled swap.
func (a *MempoolSwapMemolessActor) refreshUserBalances() error {
	spendable, err := a.userSpendableBalance(a.user)
	if err != nil {
		return fmt.Errorf("get %s balance: %w", a.from, err)
	}
	a.fromBalance = spendable
	toAccount, err := a.user.ChainClients[a.to.Chain].GetAccount(nil)
	if err != nil {
		return fmt.Errorf("get %s balance: %w", a.to, err)
	}
	a.toBalance = accountAssetAmount(toAccount, a.to)
	return nil
}

func (a *MempoolSwapMemolessActor) snapshotMimirs() (map[string]int64, error) {
	mimirs, err := thornode.GetMimirs()
	if err != nil {
		return nil, err
	}

	values := make(map[string]int64, len(mempoolMemolessMimirSettings))
	for _, setting := range mempoolMemolessMimirSettings {
		values[setting.key] = mempoolMemolessEffectiveMimir(mimirs, setting.key, setting.fallback)
	}
	return values, nil
}

func (a *MempoolSwapMemolessActor) applyMimirs(config *OpConfig, values map[string]int64) error {
	for _, setting := range mempoolMemolessMimirSettings {
		value, ok := values[setting.key]
		if !ok {
			continue
		}
		if err := a.setMimirAllActive(config, setting, value); err != nil {
			return err
		}
	}
	return nil
}

func (a *MempoolSwapMemolessActor) restoreMimirs(config *OpConfig, values map[string]int64) error {
	var result error
	for i := len(mempoolMemolessMimirSettings) - 1; i >= 0; i-- {
		setting := mempoolMemolessMimirSettings[i]
		value, ok := values[setting.key]
		if !ok {
			continue
		}
		if err := a.setMimirAllActive(config, setting, value); err != nil {
			result = errors.Join(result, fmt.Errorf("restore %s=%d: %w", setting.key, value, err))
		}
	}
	return result
}

func (a *MempoolSwapMemolessActor) setMimirAllActive(config *OpConfig, setting mempoolMemolessMimirSetting, value int64) error {
	mimirs, err := thornode.GetMimirs()
	if err != nil {
		return err
	}
	if mempoolMemolessEffectiveMimir(mimirs, setting.key, setting.fallback) == value {
		return nil
	}

	nodes, err := thornode.GetNodes()
	if err != nil {
		return err
	}
	active := make(map[string]struct{})
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			active[strings.ToLower(node.NodeAddress)] = struct{}{}
		}
	}
	if len(active) == 0 {
		return fmt.Errorf("no active nodes available to vote %s=%d", setting.key, value)
	}

	voters := make([]*User, 0, len(active))
	for _, nodeUser := range config.NodeUsers {
		address, addressErr := nodeUser.PubKey(common.THORChain).GetThorAddress()
		if addressErr != nil {
			return addressErr
		}
		if _, ok := active[strings.ToLower(address.String())]; ok {
			voters = append(voters, nodeUser)
		}
	}
	if len(voters) != len(active) {
		return fmt.Errorf("found %d simulation signers for %d active nodes", len(voters), len(active))
	}

	for _, nodeUser := range voters {
		if err := mempoolMemolessAcquireUser(nodeUser, 30*time.Second); err != nil {
			return err
		}
		address, addressErr := nodeUser.PubKey(common.THORChain).GetThorAddress()
		if addressErr != nil {
			nodeUser.Release()
			return addressErr
		}
		msg := thortypes.NewMsgMimir(setting.key, value, address)
		txID, broadcastErr := nodeUser.Thorchain.BroadcastWithBlocking(msg)
		nodeUser.Release()
		if broadcastErr != nil {
			return fmt.Errorf("vote %s=%d from %s: %w", setting.key, value, address, broadcastErr)
		}
		a.Log().Info().
			Str("key", setting.key).
			Int64("value", value).
			Stringer("signer", address).
			Stringer("txid", txID).
			Msg("broadcasted mempool simulation Mimir vote")
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		mimirs, err = thornode.GetMimirs()
		if err == nil && mempoolMemolessEffectiveMimir(mimirs, setting.key, setting.fallback) == value {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for effective Mimir %s=%d", setting.key, value)
}

func (a *MempoolSwapMemolessActor) acquireSwapUser(config *OpConfig) error {
	pool, err := thornode.GetPool(a.from)
	if err != nil {
		return fmt.Errorf("get %s pool: %w", a.from, err)
	}
	targetAmount := sdkmath.NewUintFromString(pool.BalanceAsset).QuoUint64(1000)
	if targetAmount.IsZero() {
		return fmt.Errorf("%s pool is too shallow for memoless simulation", a.from)
	}

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		result := a.acquireUserForAmount(config, targetAmount, false)
		switch {
		case result.Error != nil:
			return result.Error
		case result.Continue:
			return nil
		case result.Finish:
			return fmt.Errorf("no simulation user can fund %s %s", targetAmount, a.from)
		default:
			time.Sleep(500 * time.Millisecond)
		}
	}
	return fmt.Errorf("timed out acquiring a simulation user for the memoless swap")
}

func (a *MempoolSwapMemolessActor) quoteSwap() error {
	quote, quoteAmount, err := getSwapQuoteWithRecommendedMin(
		a.from,
		a.to,
		a.swapAmount,
		a.fromBalance,
		a.toAddress,
		a.fromAddress,
		false,
	)
	if err != nil {
		return fmt.Errorf("quote memoless swap: %w", err)
	}
	if err = a.updateSwapAmount(quoteAmount, "recommended minimum for memoless mempool exclusion"); err != nil {
		return err
	}
	setQuoteExpectations(&a.SwapActor, quote)
	return nil
}

func (a *MempoolSwapMemolessActor) registerReference() error {
	a.registeredMemo = buildMemolessSwapRegistrationMemo(a.from, a.to, a.toAddress, a.fromAddress)
	registrationMemo := fmt.Sprintf("REFERENCE:%s:%s", a.from, a.registeredMemo)

	address, err := a.user.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		return err
	}
	deposit := thortypes.NewMsgDeposit(
		common.NewCoins(common.NewCoin(common.RuneNative, sdkmath.ZeroUint())),
		registrationMemo,
		address,
	)
	a.registrationTxID, err = a.user.Thorchain.BroadcastWithBlocking(deposit)
	if err != nil {
		return fmt.Errorf("register memoless swap: %w", err)
	}
	a.Log().Info().
		Stringer("txid", a.registrationTxID).
		Str("memo", registrationMemo).
		Msg("registered memoless swap reference")
	return nil
}

func (a *MempoolSwapMemolessActor) loadReference() error {
	deadline := time.Now().Add(30 * time.Second)
	var response openapi.ReferenceMemoResponse
	var err error
	for time.Now().Before(deadline) {
		response, err = thornode.GetMemoHash(a.registrationTxID.String())
		if err == nil && response.Memo != "" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("query registered reference: %w", err)
	}
	if response.Memo == "" {
		return fmt.Errorf("registered reference was not queryable before timeout")
	}
	if response.Memo != a.registeredMemo {
		return fmt.Errorf("registered memo mismatch: expected %q, got %q", a.registeredMemo, response.Memo)
	}
	if response.Asset != a.from.String() {
		return fmt.Errorf("registered asset mismatch: expected %s, got %s", a.from, response.Asset)
	}

	a.reference, err = strconv.ParseUint(response.Reference, 10, 64)
	if err != nil {
		return fmt.Errorf("parse memoless reference %q: %w", response.Reference, err)
	}
	refCount := constants.NewConstantValue().GetInt64Value(constants.MemolessTxnRefCount)
	a.referenceMemo, err = formatMempoolMemolessReference(a.reference, refCount)
	if err != nil {
		return err
	}
	return nil
}

func (a *MempoolSwapMemolessActor) broadcastReferenceSwap() error {
	inboundAddress, _, err := thornode.GetInboundAddress(a.from.Chain)
	if err != nil {
		return fmt.Errorf("get POL inbound address: %w", err)
	}
	tx := SimTx{
		Chain:       a.from.Chain,
		ToAddress:   inboundAddress,
		Coin:        common.NewCoin(a.from, a.swapAmount),
		Memo:        a.referenceMemo,
		OneShotMemo: true,
	}
	a.swapTxID, err = SignAndBroadcastTx(a.user.ChainClients[a.from.Chain], tx)
	if err != nil {
		return fmt.Errorf("broadcast POL reference swap: %w", err)
	}
	parsed, err := common.NewTxID(a.swapTxID)
	if err != nil {
		return fmt.Errorf("normalize POL swap txid %q: %w", a.swapTxID, err)
	}
	a.swapTxID = parsed.String()
	a.Log().Info().
		Str("txid", a.swapTxID).
		Str("memo", a.referenceMemo).
		Stringer("amount", a.swapAmount).
		Msg("broadcasted literal-reference POL swap")
	return nil
}

func (a *MempoolSwapMemolessActor) verifyPreFinalityExclusion(baselineUsage sdkmath.Uint) error {
	deadline := time.Now().Add(90 * time.Second)
	samples := 0
	lastHeight := int64(-1)

	for time.Now().Before(deadline) {
		stages, err := thornode.GetTxStages(a.swapTxID)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if mempoolMemolessInboundFinalized(stages) {
			return &mempoolMemolessMissedWindow{
				err: fmt.Errorf("POL inbound finalized before %d pre-finality exclusion samples (got %d)", mempoolMemolessPreFinalitySamples, samples),
			}
		}
		if !mempoolMemolessInboundObservedUnfinalized(stages) {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		height, err := thornode.GetBlockHeight()
		if err != nil {
			return err
		}
		if height == lastHeight {
			time.Sleep(250 * time.Millisecond)
			continue
		}

		snapshot, err := a.readExclusionSnapshot()
		if err != nil {
			return err
		}
		// Do not attribute a state transition racing these HTTP reads to the
		// unfinalized window. A sample only counts when finality is still absent
		// after every queried surface has been captured.
		stagesAfter, err := thornode.GetTxStages(a.swapTxID)
		if err != nil {
			return err
		}
		if mempoolMemolessInboundFinalized(stagesAfter) {
			continue
		}
		if err = validateMempoolMemolessExclusion(snapshot, baselineUsage, a.toBalance); err != nil {
			return fmt.Errorf("pre-finality exclusion sample at height %d: %w", height, err)
		}

		samples++
		lastHeight = height
		a.Log().Info().
			Int("sample", samples).
			Int64("height", height).
			Msg("verified literal reference remained outside every mempool-swap surface")
		if samples >= mempoolMemolessPreFinalitySamples {
			return nil
		}
	}
	return fmt.Errorf("timed out observing the mined-but-unfinalized POL reference swap")
}

func (a *MempoolSwapMemolessActor) readExclusionSnapshot() (mempoolMemolessExclusionSnapshot, error) {
	usage, err := thornode.GetMempoolSwapUsageTOR()
	if err != nil {
		return mempoolMemolessExclusionSnapshot{}, fmt.Errorf("get mempool TOR usage: %w", err)
	}
	staged, err := thornode.GetStagedOutbounds()
	if err != nil {
		return mempoolMemolessExclusionSnapshot{}, fmt.Errorf("get staged outbounds: %w", err)
	}
	outbounds, err := thornode.GetOutboundQueue()
	if err != nil {
		return mempoolMemolessExclusionSnapshot{}, fmt.Errorf("get outbound queue: %w", err)
	}
	_, queueErr := thornode.GetSwapDetails(a.swapTxID)
	queueAbsent := mempoolQueueNotFound(queueErr)
	if queueErr != nil && !queueAbsent {
		return mempoolMemolessExclusionSnapshot{}, fmt.Errorf("query swap queue: %w", queueErr)
	}

	account, err := a.user.ChainClients[a.to.Chain].GetAccount(nil)
	if err != nil {
		return mempoolMemolessExclusionSnapshot{}, fmt.Errorf("get BTC recipient balance: %w", err)
	}
	reference, err := thornode.GetMemoHash(a.registrationTxID.String())
	if err != nil {
		return mempoolMemolessExclusionSnapshot{}, fmt.Errorf("query memoless reference: %w", err)
	}
	details, err := thornode.GetTxDetails(a.swapTxID)
	if err != nil {
		return mempoolMemolessExclusionSnapshot{}, fmt.Errorf("query unfinalized inbound details: %w", err)
	}

	return mempoolMemolessExclusionSnapshot{
		usage:         usage,
		staged:        mempoolMemolessStagedContains(staged, a.swapTxID),
		outbound:      mempoolMemolessOutboundsContain(outbounds, a.swapTxID),
		queued:        !queueAbsent,
		recipient:     accountAssetAmount(account, a.to),
		actions:       len(details.Actions),
		outTxs:        len(details.OutTxs),
		referenceUses: mempoolMemolessReferenceUseCount(reference, a.swapTxID),
	}, nil
}

func (a *MempoolSwapMemolessActor) verifyFinalizedSwap(baselineUsage sdkmath.Uint) error {
	deadline := time.Now().Add(4 * time.Minute)
	for time.Now().Before(deadline) {
		stages, err := thornode.GetTxStages(a.swapTxID)
		if err != nil || stages.OutboundSigned == nil || !stages.OutboundSigned.Completed {
			time.Sleep(time.Second)
			continue
		}
		if !mempoolMemolessInboundFinalized(stages) {
			return fmt.Errorf("outbound signed before POL inbound finality")
		}

		if err = a.assertFinalizedState(baselineUsage); err != nil {
			return err
		}
		return nil
	}
	return fmt.Errorf("timed out waiting for finalized memoless swap outbound")
}

func (a *MempoolSwapMemolessActor) assertFinalizedState(baselineUsage sdkmath.Uint) error {
	usage, err := thornode.GetMempoolSwapUsageTOR()
	if err != nil {
		return err
	}
	if !usage.Equal(baselineUsage) {
		return fmt.Errorf("mempool TOR usage changed for reference swap: baseline=%s current=%s", baselineUsage, usage)
	}
	staged, err := thornode.GetStagedOutbounds()
	if err != nil {
		return err
	}
	if mempoolMemolessStagedContains(staged, a.swapTxID) {
		return fmt.Errorf("finalized reference swap retained a staged outbound")
	}
	pending, err := thornode.GetOutboundQueue()
	if err != nil {
		return err
	}
	if mempoolMemolessOutboundsContain(pending, a.swapTxID) {
		return fmt.Errorf("finalized reference swap retained a pending outbound")
	}
	scheduled, err := thornode.GetScheduledOutboundQueue()
	if err != nil {
		return err
	}
	if mempoolMemolessOutboundsContain(scheduled, a.swapTxID) {
		return fmt.Errorf("finalized reference swap retained a scheduled outbound")
	}
	if _, queueErr := thornode.GetSwapDetails(a.swapTxID); !mempoolQueueNotFound(queueErr) {
		if queueErr == nil {
			return fmt.Errorf("finalized reference swap remained in a swap queue")
		}
		return fmt.Errorf("query finalized reference swap queue: %w", queueErr)
	}

	details, err := thornode.GetTxDetails(a.swapTxID)
	if err != nil {
		return err
	}
	if len(details.Actions) != 1 {
		return fmt.Errorf("expected exactly one finalized action, got %d", len(details.Actions))
	}
	if len(details.OutTxs) != 1 {
		return fmt.Errorf("expected exactly one finalized out tx, got %d", len(details.OutTxs))
	}
	action := details.Actions[0]
	outTx := details.OutTxs[0]
	if !mempoolMemolessSameTxID(action.GetInHash(), a.swapTxID) {
		return fmt.Errorf("finalized action in_hash %q does not match %s", action.GetInHash(), a.swapTxID)
	}
	if action.Chain != a.to.Chain.String() || action.ToAddress != a.toAddress.String() {
		return fmt.Errorf("unexpected finalized action destination: chain=%s address=%s", action.Chain, action.ToAddress)
	}
	actionMemo := action.GetMemo()
	if actionMemo == "" {
		actionMemo = action.GetOriginalMemo()
	}
	if strings.HasPrefix(strings.ToUpper(actionMemo), "REFUND:") {
		return fmt.Errorf("memoless swap finalized as a refund")
	}
	if outTx.GetChain() != a.to.Chain.String() || outTx.GetToAddress() != a.toAddress.String() {
		return fmt.Errorf("unexpected finalized out tx destination: chain=%s address=%s", outTx.GetChain(), outTx.GetToAddress())
	}
	if len(outTx.Coins) != 1 || outTx.Coins[0].Asset != a.to.String() {
		return fmt.Errorf("expected one %s outbound coin, got %+v", a.to, outTx.Coins)
	}

	outAmountPlusGas, err := outboundAmountPlusGas(outTx, action, a.to)
	if err != nil {
		return err
	}
	if outAmountPlusGas.LT(a.minExpected) || outAmountPlusGas.GT(a.maxExpected) {
		return fmt.Errorf("finalized out amount %s outside quoted range [%s,%s]", outAmountPlusGas, a.minExpected, a.maxExpected)
	}

	account, err := a.user.ChainClients[a.to.Chain].GetAccount(nil)
	if err != nil {
		return err
	}
	currentBalance := accountAssetAmount(account, a.to)
	if !currentBalance.GT(a.toBalance) {
		return fmt.Errorf("BTC recipient did not increase: before=%s after=%s", a.toBalance, currentBalance)
	}

	reference, err := thornode.GetMemoHash(a.registrationTxID.String())
	if err != nil {
		return err
	}
	if uses := mempoolMemolessReferenceUseCount(reference, a.swapTxID); uses != 1 {
		return fmt.Errorf("expected reference UsedByTxs to contain source txid once, got %d matches in %v", uses, reference.UsedByTxs)
	}
	return nil
}

type mempoolMemolessExclusionSnapshot struct {
	usage         sdkmath.Uint
	staged        bool
	outbound      bool
	queued        bool
	recipient     sdkmath.Uint
	actions       int
	outTxs        int
	referenceUses int
}

func validateMempoolMemolessExclusion(snapshot mempoolMemolessExclusionSnapshot, baselineUsage, baselineRecipient sdkmath.Uint) error {
	switch {
	case !snapshot.usage.Equal(baselineUsage):
		return fmt.Errorf("TOR usage changed: baseline=%s current=%s", baselineUsage, snapshot.usage)
	case snapshot.staged:
		return fmt.Errorf("source tx appeared in staged outbounds")
	case snapshot.outbound:
		return fmt.Errorf("source tx appeared in real outbound queue")
	case snapshot.queued:
		return fmt.Errorf("source tx appeared in swap queue")
	case !snapshot.recipient.Equal(baselineRecipient):
		return fmt.Errorf("recipient changed before finality: baseline=%s current=%s", baselineRecipient, snapshot.recipient)
	case snapshot.actions != 0:
		return fmt.Errorf("source voter acquired %d actions before finality", snapshot.actions)
	case snapshot.outTxs != 0:
		return fmt.Errorf("source voter acquired %d out txs before finality", snapshot.outTxs)
	case snapshot.referenceUses != 0:
		return fmt.Errorf("reference was consumed before finality")
	default:
		return nil
	}
}

func formatMempoolMemolessReference(reference uint64, referenceCount int64) (string, error) {
	if referenceCount <= 0 {
		return "", fmt.Errorf("invalid MemolessTxnRefCount %d", referenceCount)
	}
	if reference > uint64(referenceCount) {
		return "", fmt.Errorf("reference %d exceeds MemolessTxnRefCount %d", reference, referenceCount)
	}
	width := len(strconv.FormatInt(referenceCount, 10))
	return fmt.Sprintf("r:%0*d", width, reference), nil
}

func mempoolMemolessEffectiveMimir(mimirs map[string]int64, key string, fallback int64) int64 {
	for candidate, value := range mimirs {
		if strings.EqualFold(candidate, key) {
			return value
		}
	}
	return fallback
}

func mempoolMemolessAcquireUser(user *User, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if user.Acquire() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out acquiring simulation user %s", user.Name())
}

func mempoolMemolessInboundObservedUnfinalized(stages openapi.TxStagesResponse) bool {
	observed := stages.InboundObserved.GetStarted() || stages.InboundObserved.GetPreConfirmationCount() > 0
	confirming := stages.InboundConfirmationCounted != nil && !stages.InboundConfirmationCounted.Completed
	return observed && confirming && !mempoolMemolessInboundFinalized(stages)
}

func mempoolMemolessInboundFinalized(stages openapi.TxStagesResponse) bool {
	return stages.InboundFinalised != nil && stages.InboundFinalised.Completed
}

func mempoolMemolessStagedContains(staged openapi.StagedOutboundResponse, txID string) bool {
	for _, item := range staged.StagedOutboundItems {
		if item.TxOutItem != nil && mempoolMemolessSameTxID(item.TxOutItem.GetInHash(), txID) {
			return true
		}
	}
	return false
}

func mempoolMemolessOutboundsContain(outbounds []openapi.TxOutItem, txID string) bool {
	for _, item := range outbounds {
		if mempoolMemolessSameTxID(item.GetInHash(), txID) {
			return true
		}
	}
	return false
}

func mempoolMemolessReferenceUseCount(reference openapi.ReferenceMemoResponse, txID string) int {
	count := 0
	for _, usedBy := range reference.UsedByTxs {
		if mempoolMemolessSameTxID(usedBy, txID) {
			count++
		}
	}
	return count
}

func mempoolMemolessSameTxID(left, right string) bool {
	normalize := func(value string) string {
		return strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(value), "0x"), "0X")
	}
	return strings.EqualFold(normalize(left), normalize(right))
}
