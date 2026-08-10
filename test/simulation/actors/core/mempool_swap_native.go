package core

import (
	"errors"
	"fmt"
	rand "math/rand/v2"
	"strings"
	"time"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simcommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	nativeMempoolRuneNonstream    = "native-rune-nonstream"
	nativeMempoolRuneStream       = "native-rune-stream"
	nativeMempoolSynthNonstream   = "native-synth-nonstream"
	nativeMempoolSynthStream      = "native-synth-stream"
	nativeMempoolSecuredNonstream = "native-secured-nonstream"
	nativeMempoolSecuredStream    = "native-secured-stream"
)

type nativeMempoolTarget struct {
	name        string
	target      common.Asset
	nonstreamID string
	streamID    string
}

func nativeMempoolTargets() []nativeMempoolTarget {
	return []nativeMempoolTarget{
		{
			name:        "rune",
			target:      common.RuneAsset(),
			nonstreamID: nativeMempoolRuneNonstream,
			streamID:    nativeMempoolRuneStream,
		},
		{
			name:        "btc-synth",
			target:      common.BTCAsset.GetSyntheticAsset(),
			nonstreamID: nativeMempoolSynthNonstream,
			streamID:    nativeMempoolSynthStream,
		},
		{
			name:        "btc-secured",
			target:      common.BTCAsset.GetSecuredAsset(),
			nonstreamID: nativeMempoolSecuredNonstream,
			streamID:    nativeMempoolSecuredStream,
		},
	}
}

type nativeMempoolMimir struct {
	key          string
	setupValue   int64
	mocknetValue int64
}

var nativeMempoolMimirs = []nativeMempoolMimir{
	{key: "DollarsPerRune", setupValue: 100_000_000, mocknetValue: 0},
	{key: constants.MempoolSwapCapTOR.String(), setupValue: 100_000_000_000_000, mocknetValue: 0},
	{key: constants.MempoolSwapPoolCapBPS.String(), setupValue: 0, mocknetValue: 50},
	{key: constants.EnableAdvSwapQueue.String(), setupValue: 1, mocknetValue: 1},
	{key: constants.MaxSynthPerPoolDepth.String(), setupValue: 6_000, mocknetValue: 3_500},
	{key: constants.SynthSlipMinBps.String(), setupValue: 0, mocknetValue: 0},
	{key: constants.SecuredAssetSlipMinBps.String(), setupValue: 0, mocknetValue: 5},
}

// MempoolSwapNativeSimulationActor exercises the live observed-in policy for
// THOR-native destinations. It is intentionally a single, sequential actor:
// every case takes a snapshot relative to the same locked user, and exact TOR
// usage assertions would be ambiguous if another mempool actor ran alongside it.
type MempoolSwapNativeSimulationActor struct {
	simtypes.Actor
}

// NewMempoolSwapNativeSimulationActor returns an actor for POL inbounds
// targeting RUNE, BTC synth, and BTC secured assets. The actor sets the Mimirs
// it needs by active-node vote and restores the values that were effective when
// it started, so it is safe to append after another sequential mempool actor.
func NewMempoolSwapNativeSimulationActor(rng *rand.Rand) *simtypes.Actor {
	a := &MempoolSwapNativeSimulationActor{
		Actor: *simtypes.NewActor("MempoolSwap-NativeTargets", rng),
	}
	a.Timeout = 15 * time.Minute
	a.Ops = append(a.Ops, a.run)
	return &a.Actor
}

func (a *MempoolSwapNativeSimulationActor) run(config *simtypes.OpConfig) simtypes.OpResult {
	err := a.runScenarios(config)
	return simtypes.OpResult{Finish: true, Error: err}
}

func (a *MempoolSwapNativeSimulationActor) runScenarios(config *simtypes.OpConfig) (err error) {
	user, err := nativeMempoolAcquirePOLUser(config.Users)
	if err != nil {
		return err
	}
	defer user.Release()

	restore, err := nativeMempoolSnapshotMimirs()
	if err != nil {
		return fmt.Errorf("snapshot native-target mempool Mimirs: %w", err)
	}
	setup := make(map[string]int64, len(nativeMempoolMimirs))
	for _, setting := range nativeMempoolMimirs {
		setup[setting.key] = setting.setupValue
	}

	defer func() {
		cleanupErr := a.restoreMimirs(config, restore)
		if cleanupErr != nil {
			cleanupErr = fmt.Errorf("restore native-target mempool Mimirs: %w", cleanupErr)
		}
		err = errors.Join(err, cleanupErr)
	}()
	if err = a.applyMimirs(config, setup); err != nil {
		return fmt.Errorf("configure native-target mempool Mimirs: %w", err)
	}

	for _, target := range nativeMempoolTargets() {
		a.Log().Info().Str("scenario", target.nonstreamID).Msg("running native-target exclusion scenario")
		if err = a.runNonstreaming(user, target); err != nil {
			return fmt.Errorf("%s: %w", target.nonstreamID, err)
		}
		a.Log().Info().Str("scenario", target.streamID).Msg("running native-target streaming scenario")
		if err = a.runStreaming(user, target); err != nil {
			return fmt.Errorf("%s: %w", target.streamID, err)
		}
	}

	return nil
}

func (a *MempoolSwapNativeSimulationActor) runNonstreaming(user *simtypes.User, target nativeMempoolTarget) error {
	destination := user.Address(common.THORChain)
	before, err := nativeMempoolRecipientBalance(destination, target.target)
	if err != nil {
		return err
	}
	baselineUsage, err := thornode.GetMempoolSwapUsageTOR()
	if err != nil {
		return fmt.Errorf("get baseline mempool TOR usage: %w", err)
	}
	amount, err := nativeMempoolSwapAmount(user, target.target, destination)
	if err != nil {
		return err
	}

	memo := fmt.Sprintf("=:%s:%s", target.target, destination)
	txID, err := a.broadcastPOLSwap(user, memo, amount)
	if err != nil {
		return err
	}
	if _, err = nativeMempoolWaitObservedUnfinalized(txID, 25*time.Second); err != nil {
		return err
	}
	if err = nativeMempoolAssertExcluded(txID, destination, target.target, before, baselineUsage); err != nil {
		return err
	}

	// Keep the exclusion assertion alive across more than one THOR block. This
	// catches a delayed queue admission rather than only sampling the observation block.
	if err = nativeMempoolWaitBlocks(2, 15*time.Second); err != nil {
		return err
	}
	if _, err = nativeMempoolWaitObservedUnfinalized(txID, 2*time.Second); err != nil {
		return fmt.Errorf("plain native target finalized before the exclusion could be rechecked: %w", err)
	}
	if err = nativeMempoolAssertExcluded(txID, destination, target.target, before, baselineUsage); err != nil {
		return err
	}

	after, err := nativeMempoolWaitFinalCredit(txID, destination, target.target, before, baselineUsage, 2*time.Minute)
	if err != nil {
		return err
	}
	return nativeMempoolVerifyExactCredit(txID, destination, target.target, before, after, cosmos.ZeroUint())
}

func (a *MempoolSwapNativeSimulationActor) runStreaming(user *simtypes.User, target nativeMempoolTarget) error {
	destination := user.Address(common.THORChain)
	before, err := nativeMempoolRecipientBalance(destination, target.target)
	if err != nil {
		return err
	}
	baselineUsage, err := thornode.GetMempoolSwapUsageTOR()
	if err != nil {
		return fmt.Errorf("get baseline mempool TOR usage: %w", err)
	}
	amount, err := nativeMempoolSwapAmount(user, target.target, destination)
	if err != nil {
		return err
	}

	// Three one-block parts are long enough to prove advanced-queue behavior,
	// but finish comfortably inside POL's 15-confirmation simulation window.
	memo := fmt.Sprintf("=:%s:%s:0/1/3", target.target, destination)
	txID, err := a.broadcastPOLSwap(user, memo, amount)
	if err != nil {
		return err
	}
	if _, err = nativeMempoolWaitObservedUnfinalized(txID, 25*time.Second); err != nil {
		return err
	}
	if err = nativeMempoolWaitReservation(txID, baselineUsage, 20*time.Second); err != nil {
		return err
	}

	current, err := nativeMempoolRecipientBalance(destination, target.target)
	if err != nil {
		return err
	}
	if !current.Equal(before) {
		return fmt.Errorf("recipient %s was credited before POL finality: before %s, current %s", target.target, before, current)
	}
	if err = nativeMempoolAssertNoRealOutbound(txID); err != nil {
		return err
	}

	staged, err := nativeMempoolWaitStagedBeforeFinality(txID, 25*time.Second)
	if err != nil {
		return err
	}
	expectedEscrow := nativeMempoolEscrowAsset(target.target)
	if err = nativeMempoolValidateStaged(staged, txID, destination, expectedEscrow); err != nil {
		return err
	}
	stagedAmount, err := cosmos.ParseUint(staged.TxOutItem.Coin.Amount)
	if err != nil {
		return fmt.Errorf("parse staged native amount %q: %w", staged.TxOutItem.Coin.Amount, err)
	}
	usage, err := thornode.GetMempoolSwapUsageTOR()
	if err != nil {
		return err
	}
	if !usage.GT(baselineUsage) {
		return fmt.Errorf("staged native stream released TOR before finality: baseline %s, current %s", baselineUsage, usage)
	}
	current, err = nativeMempoolRecipientBalance(destination, target.target)
	if err != nil {
		return err
	}
	if !current.Equal(before) {
		return fmt.Errorf("staged native stream credited %s before finality: before %s, current %s", target.target, before, current)
	}
	if err = nativeMempoolAssertNoRealOutbound(txID); err != nil {
		return err
	}

	after, err := nativeMempoolWaitFinalCredit(txID, destination, target.target, before, baselineUsage, 2*time.Minute)
	if err != nil {
		return err
	}
	return nativeMempoolVerifyExactCredit(txID, destination, target.target, before, after, stagedAmount)
}

func (a *MempoolSwapNativeSimulationActor) broadcastPOLSwap(user *simtypes.User, memo string, amount cosmos.Uint) (string, error) {
	client := user.ChainClients[common.POLChain]
	txID, err := simcommon.DepositL1(a.Log(), client, common.POLAsset, memo, amount)
	if err != nil {
		return "", fmt.Errorf("broadcast POL swap: %w", err)
	}
	normalized, err := common.NewTxID(txID)
	if err != nil {
		return "", fmt.Errorf("normalize POL txid %q: %w", txID, err)
	}
	a.Log().Info().Stringer("txid", normalized).Stringer("amount", amount).Str("memo", memo).Msg("broadcasted native-target POL swap")
	return normalized.String(), nil
}

func (a *MempoolSwapNativeSimulationActor) applyMimirs(config *simtypes.OpConfig, values map[string]int64) error {
	nodes, err := thornode.GetNodes()
	if err != nil {
		return err
	}
	active := make(map[string]bool)
	for _, node := range nodes {
		if strings.EqualFold(node.Status, "Active") {
			active[node.NodeAddress] = true
		}
	}
	if len(active) == 0 {
		return fmt.Errorf("no active nodes found for Mimir voting")
	}

	voters := 0
	for _, nodeUser := range config.NodeUsers {
		address, addressErr := nodeUser.PubKey(common.THORChain).GetThorAddress()
		if addressErr != nil {
			return addressErr
		}
		if !active[address.String()] {
			continue
		}
		if !nodeUser.Acquire() {
			return fmt.Errorf("active Mimir voter %s is locked", address)
		}
		messages := make([]sdk.Msg, 0, len(values))
		for _, setting := range nativeMempoolMimirs {
			value, ok := values[setting.key]
			if !ok {
				continue
			}
			messages = append(messages, thortypes.NewMsgMimir(setting.key, value, address))
		}
		if len(messages) == 0 {
			nodeUser.Release()
			return fmt.Errorf("no native-target Mimir values selected")
		}
		_, broadcastErr := nodeUser.Thorchain.BroadcastWithBlocking(messages...)
		nodeUser.Release()
		if broadcastErr != nil {
			return fmt.Errorf("broadcast Mimir votes from %s: %w", address, broadcastErr)
		}
		voters++
	}
	if voters != len(active) {
		return fmt.Errorf("found %d active nodes but only %d matching simulation voters", len(active), voters)
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		mimirs, getErr := thornode.GetMimirs()
		if getErr == nil && nativeMempoolMimirsMatch(mimirs, values) {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("timed out waiting for effective Mimir values %v", values)
}

func (a *MempoolSwapNativeSimulationActor) restoreMimirs(config *simtypes.OpConfig, values map[string]int64) error {
	var restoreErr error
	for _, setting := range nativeMempoolMimirs {
		err := a.applyMimirs(config, map[string]int64{setting.key: values[setting.key]})
		if err != nil {
			restoreErr = errors.Join(restoreErr, fmt.Errorf("restore %s: %w", setting.key, err))
		}
	}
	return restoreErr
}

func nativeMempoolAcquirePOLUser(users []*simtypes.User) (*simtypes.User, error) {
	for _, user := range users {
		client, ok := user.ChainClients[common.POLChain]
		if !ok || client == nil || !user.Acquire() {
			continue
		}
		account, err := client.GetAccount(nil)
		if err != nil {
			user.Release()
			continue
		}
		if accountAssetAmount(account, common.POLAsset).GT(common.POLChain.DustThreshold()) {
			return user, nil
		}
		user.Release()
	}
	return nil, fmt.Errorf("no unlocked simulation user with spendable POL")
}

func nativeMempoolSwapAmount(user *simtypes.User, target common.Asset, destination common.Address) (cosmos.Uint, error) {
	pool, err := thornode.GetPool(common.POLAsset)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("get POL pool: %w", err)
	}
	depth, err := cosmos.ParseUint(pool.BalanceAsset)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("parse POL pool depth %q: %w", pool.BalanceAsset, err)
	}
	account, err := user.ChainClients[common.POLChain].GetAccount(nil)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("get POL account: %w", err)
	}
	spendable := swapSpendableBalance(common.POLAsset, accountAssetAmount(account, common.POLAsset))
	if spendable.IsZero() {
		return cosmos.ZeroUint(), fmt.Errorf("POL account has no spendable balance")
	}

	amount := depth.QuoUint64(2_000)
	if amount.IsZero() {
		amount = cosmos.OneUint()
	}
	_, amount, err = getSwapQuoteWithRecommendedMin(common.POLAsset, target, amount, spendable, destination, false)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("quote POL to %s: %w", target, err)
	}
	return amount, nil
}

func nativeMempoolRecipientBalance(address common.Address, asset common.Asset) (cosmos.Uint, error) {
	balances, err := thornode.GetBalances(address)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("get recipient %s balance: %w", asset, err)
	}
	coin := balances.GetCoin(asset)
	if coin.Amount.IsNil() {
		return cosmos.ZeroUint(), nil
	}
	return coin.Amount, nil
}

func nativeMempoolWaitObservedUnfinalized(txID string, timeout time.Duration) (openapi.TxStagesResponse, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		stages, err := thornode.GetTxStages(txID)
		if err != nil {
			lastErr = err
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if nativeMempoolInboundFinalized(stages) {
			return stages, fmt.Errorf("POL inbound %s finalized before the pre-finality assertion", txID)
		}
		if nativeMempoolObservedUnfinalized(stages) {
			return stages, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return openapi.TxStagesResponse{}, fmt.Errorf("timed out waiting for unfinalized observation of %s: %w", txID, lastErr)
}

func nativeMempoolAssertExcluded(txID string, destination common.Address, target common.Asset, expectedBalance, expectedUsage cosmos.Uint) error {
	usage, err := thornode.GetMempoolSwapUsageTOR()
	if err != nil {
		return err
	}
	if !usage.Equal(expectedUsage) {
		return fmt.Errorf("plain native target reserved TOR before finality: expected %s, got %s", expectedUsage, usage)
	}
	staged, err := thornode.GetStagedOutbounds()
	if err != nil {
		return err
	}
	if item := nativeMempoolFindStaged(staged, txID); item != nil {
		return fmt.Errorf("plain native target created staged outbound before finality")
	}
	if err = nativeMempoolAssertNoRealOutbound(txID); err != nil {
		return err
	}
	if details, detailsErr := thornode.GetSwapDetails(txID); detailsErr == nil {
		return fmt.Errorf("plain native target entered %s swap queue before finality", details.GetQueueType())
	} else if !mempoolQueueNotFound(detailsErr) {
		return fmt.Errorf("query excluded native-target swap queue: %w", detailsErr)
	}
	balance, err := nativeMempoolRecipientBalance(destination, target)
	if err != nil {
		return err
	}
	if !balance.Equal(expectedBalance) {
		return fmt.Errorf("plain native target credited recipient before finality: expected %s, got %s", expectedBalance, balance)
	}
	return nil
}

func nativeMempoolWaitReservation(txID string, baseline cosmos.Uint, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		stages, err := thornode.GetTxStages(txID)
		if err == nil && nativeMempoolInboundFinalized(stages) {
			return fmt.Errorf("POL inbound %s finalized before a TOR reservation was observed", txID)
		}
		usage, usageErr := thornode.GetMempoolSwapUsageTOR()
		if usageErr != nil {
			lastErr = usageErr
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if usage.GT(baseline) {
			details, detailsErr := thornode.GetSwapDetails(txID)
			if detailsErr == nil && strings.EqualFold(details.GetQueueType(), "advanced") {
				return nil
			}
			staged, stagedErr := thornode.GetStagedOutbounds()
			if stagedErr == nil && nativeMempoolFindStaged(staged, txID) != nil {
				return nil
			}
			lastErr = detailsErr
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for TOR reservation and queue/stage state for %s: %w", txID, lastErr)
}

func nativeMempoolWaitStagedBeforeFinality(txID string, timeout time.Duration) (*openapi.StagedOutboundItem, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		stages, err := thornode.GetTxStages(txID)
		if err == nil && nativeMempoolInboundFinalized(stages) {
			return nil, fmt.Errorf("POL inbound %s finalized before its native outbound was staged", txID)
		}
		staged, stagedErr := thornode.GetStagedOutbounds()
		if stagedErr == nil {
			if item := nativeMempoolFindStaged(staged, txID); item != nil {
				return item, nil
			}
		} else {
			lastErr = stagedErr
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, fmt.Errorf("timed out waiting for staged native outbound for %s: %w", txID, lastErr)
}

func nativeMempoolWaitFinalCredit(
	txID string,
	destination common.Address,
	target common.Asset,
	before cosmos.Uint,
	baselineUsage cosmos.Uint,
	timeout time.Duration,
) (cosmos.Uint, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		stages, err := thornode.GetTxStages(txID)
		if err != nil {
			lastErr = err
			time.Sleep(time.Second)
			continue
		}
		balance, balanceErr := nativeMempoolRecipientBalance(destination, target)
		if balanceErr != nil {
			lastErr = balanceErr
			time.Sleep(time.Second)
			continue
		}
		usage, usageErr := thornode.GetMempoolSwapUsageTOR()
		if usageErr != nil {
			lastErr = usageErr
			time.Sleep(time.Second)
			continue
		}
		staged, stagedErr := thornode.GetStagedOutbounds()
		if stagedErr != nil {
			lastErr = stagedErr
			time.Sleep(time.Second)
			continue
		}
		if nativeMempoolInboundFinalized(stages) && balance.GT(before) && usage.Equal(baselineUsage) && nativeMempoolFindStaged(staged, txID) == nil {
			if queueErr := nativeMempoolAssertSwapQueueAbsent(txID); queueErr != nil {
				lastErr = queueErr
				time.Sleep(time.Second)
				continue
			}
			if outboundErr := nativeMempoolAssertNoRealOutbound(txID); outboundErr != nil {
				lastErr = outboundErr
				time.Sleep(time.Second)
				continue
			}
			return balance, nil
		}
		time.Sleep(time.Second)
	}
	return cosmos.ZeroUint(), fmt.Errorf("timed out waiting for finalized native credit for %s: %w", txID, lastErr)
}

func nativeMempoolVerifyExactCredit(
	txID string,
	destination common.Address,
	target common.Asset,
	before, after, stagedAmount cosmos.Uint,
) error {
	var details openapi.TxDetailsResponse
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		candidate, err := thornode.GetTxDetails(txID)
		if err == nil {
			details = candidate
			if len(candidate.Actions) > 0 && (!stagedAmount.IsZero() || len(candidate.OutTxs) > 0) {
				break
			}
		}
		time.Sleep(time.Second)
	}
	delta := common.SafeSub(after, before)
	if err := nativeMempoolValidateFinalSettlement(details, txID, destination, target, delta, stagedAmount); err != nil {
		return err
	}

	// A duplicate native settlement would change the bank balance without an L1
	// signing signal. Hold the locked recipient through three more THOR blocks
	// and prove both its balance and its one-action voter remain stable.
	if err := nativeMempoolWaitBlocks(3, 20*time.Second); err != nil {
		return err
	}
	stable, err := nativeMempoolRecipientBalance(destination, target)
	if err != nil {
		return err
	}
	if !stable.Equal(after) {
		return fmt.Errorf("native recipient balance changed after settlement: %s -> %s", after, stable)
	}
	if err = nativeMempoolAssertSwapQueueAbsent(txID); err != nil {
		return err
	}
	details, err = thornode.GetTxDetails(txID)
	if err != nil {
		return err
	}
	return nativeMempoolValidateFinalSettlement(details, txID, destination, target, delta, stagedAmount)
}

func nativeMempoolValidateFinalSettlement(
	details openapi.TxDetailsResponse,
	txID string,
	destination common.Address,
	target common.Asset,
	expectedAmount, stagedAmount cosmos.Uint,
) error {
	if len(details.Actions) != 1 {
		return fmt.Errorf("expected exactly one native action for %s, got %d", txID, len(details.Actions))
	}
	if stagedAmount.IsZero() {
		if len(details.OutTxs) != 1 {
			return fmt.Errorf("expected exactly one non-streaming internal THOR out tx for %s, got %d", txID, len(details.OutTxs))
		}
	} else if len(details.OutTxs) != 0 {
		return fmt.Errorf("expected no streaming native out tx records for %s, got %d", txID, len(details.OutTxs))
	}

	expectedMemo := "OUT:" + txID
	action := details.Actions[0]
	if !strings.EqualFold(action.GetChain(), common.THORChain.String()) {
		return fmt.Errorf("native action chain mismatch: expected %s, got %s", common.THORChain, action.GetChain())
	}
	if !common.Address(action.GetToAddress()).Equals(destination) {
		return fmt.Errorf("native action destination mismatch: expected %s, got %s", destination, action.GetToAddress())
	}
	if !strings.EqualFold(action.GetOriginalMemo(), expectedMemo) {
		return fmt.Errorf("native action original memo mismatch: expected %s, got %s", expectedMemo, action.GetOriginalMemo())
	}
	actionAsset, err := common.NewAsset(action.Coin.Asset)
	if err != nil {
		return fmt.Errorf("parse native action asset %q: %w", action.Coin.Asset, err)
	}
	if !actionAsset.Equals(target) {
		return fmt.Errorf("native action asset mismatch: expected %s, got %s", target, actionAsset)
	}
	actionAmount, err := cosmos.ParseUint(action.Coin.Amount)
	if err != nil {
		return fmt.Errorf("parse native action amount %q: %w", action.Coin.Amount, err)
	}
	if !expectedAmount.Equal(actionAmount) {
		return fmt.Errorf("recipient credit %s does not equal the single action %s", expectedAmount, actionAmount)
	}
	if !stagedAmount.IsZero() && !stagedAmount.Equal(actionAmount) {
		return fmt.Errorf("recipient action %s does not equal staged escrow %s", actionAmount, stagedAmount)
	}
	if !stagedAmount.IsZero() {
		return nil
	}

	outTx := details.OutTxs[0]
	outTxID, err := common.NewTxID(outTx.GetId())
	if err != nil {
		return fmt.Errorf("parse internal THOR out tx id %q: %w", outTx.GetId(), err)
	}
	if !outTxID.IsBlank() {
		return fmt.Errorf("native settlement recorded non-blank out tx id %s", outTxID)
	}
	if !strings.EqualFold(outTx.GetChain(), common.THORChain.String()) {
		return fmt.Errorf("native settlement recorded external out tx chain %s", outTx.GetChain())
	}
	if !common.Address(outTx.GetToAddress()).Equals(destination) {
		return fmt.Errorf("native out tx destination mismatch: expected %s, got %s", destination, outTx.GetToAddress())
	}
	if !strings.EqualFold(outTx.GetMemo(), expectedMemo) || !strings.EqualFold(outTx.GetMemo(), action.GetOriginalMemo()) {
		return fmt.Errorf("native out tx memo mismatch: expected %s, action original %s, got %s", expectedMemo, action.GetOriginalMemo(), outTx.GetMemo())
	}
	if len(outTx.Coins) != 1 {
		return fmt.Errorf("expected exactly one native out tx coin for %s, got %d", txID, len(outTx.Coins))
	}
	outAsset, err := common.NewAsset(outTx.Coins[0].Asset)
	if err != nil {
		return fmt.Errorf("parse native out tx asset %q: %w", outTx.Coins[0].Asset, err)
	}
	if !outAsset.Equals(target) || !outAsset.Equals(actionAsset) {
		return fmt.Errorf("native out tx asset mismatch: expected %s, action %s, got %s", target, actionAsset, outAsset)
	}
	outAmount, err := cosmos.ParseUint(outTx.Coins[0].Amount)
	if err != nil {
		return fmt.Errorf("parse native out tx amount %q: %w", outTx.Coins[0].Amount, err)
	}
	if !outAmount.Equal(expectedAmount) || !outAmount.Equal(actionAmount) {
		return fmt.Errorf("native out tx amount mismatch: expected %s, action %s, got %s", expectedAmount, actionAmount, outAmount)
	}
	return nil
}

func nativeMempoolAssertSwapQueueAbsent(txID string) error {
	details, err := thornode.GetSwapDetails(txID)
	return nativeMempoolValidateSwapQueueAbsent(txID, details, err)
}

func nativeMempoolValidateSwapQueueAbsent(txID string, details openapi.SwapDetailsResponse, err error) error {
	if err == nil {
		return fmt.Errorf("native-target swap %s remained in %s swap queue after settlement", txID, details.GetQueueType())
	}
	if !mempoolQueueNotFound(err) {
		return fmt.Errorf("query finalized native-target swap queue: %w", err)
	}
	return nil
}

func nativeMempoolAssertNoRealOutbound(txID string) error {
	has, err := mempoolHasRealOutbound(txID)
	if err != nil {
		return err
	}
	if has {
		return fmt.Errorf("native-target swap %s reached the real outbound queue", txID)
	}
	return nil
}

func nativeMempoolValidateStaged(item *openapi.StagedOutboundItem, txID string, destination common.Address, asset common.Asset) error {
	if item == nil || item.TxOutItem == nil {
		return fmt.Errorf("staged native outbound is missing its tx out item")
	}
	txOut := item.TxOutItem
	if !nativeMempoolTxIDEqual(txOut.GetInHash(), txID) {
		return fmt.Errorf("staged native inbound hash mismatch: expected %s, got %s", txID, txOut.GetInHash())
	}
	if !strings.EqualFold(txOut.Chain, common.THORChain.String()) {
		return fmt.Errorf("staged native chain mismatch: expected THOR, got %s", txOut.Chain)
	}
	if txOut.ToAddress != destination.String() {
		return fmt.Errorf("staged native destination mismatch: expected %s, got %s", destination, txOut.ToAddress)
	}
	stagedAsset, err := common.NewAsset(txOut.Coin.Asset)
	if err != nil {
		return err
	}
	if !stagedAsset.Equals(asset) {
		return fmt.Errorf("staged native asset mismatch: expected %s, got %s", asset, stagedAsset)
	}
	if item.GetScheduledHeight() <= 0 || item.GetStagedHeight() <= 0 {
		return fmt.Errorf("staged native outbound has invalid heights: scheduled %d, staged %d", item.GetScheduledHeight(), item.GetStagedHeight())
	}
	return nil
}

func nativeMempoolWaitBlocks(count int64, timeout time.Duration) error {
	start, err := thornode.GetBlockHeight()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		height, heightErr := thornode.GetBlockHeight()
		if heightErr == nil && height >= start+count {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for %d THOR blocks after height %d", count, start)
}

func nativeMempoolObservedUnfinalized(stages openapi.TxStagesResponse) bool {
	observed := stages.InboundObserved.Completed || stages.InboundObserved.GetStarted() || stages.InboundObserved.GetPreConfirmationCount() > 0
	counting := stages.InboundConfirmationCounted != nil && !stages.InboundConfirmationCounted.Completed
	return observed && counting && !nativeMempoolInboundFinalized(stages)
}

func nativeMempoolInboundFinalized(stages openapi.TxStagesResponse) bool {
	return stages.InboundFinalised != nil && stages.InboundFinalised.Completed
}

func nativeMempoolFindStaged(response openapi.StagedOutboundResponse, txID string) *openapi.StagedOutboundItem {
	for i := range response.StagedOutboundItems {
		item := &response.StagedOutboundItems[i]
		if item.TxOutItem != nil && nativeMempoolTxIDEqual(item.TxOutItem.GetInHash(), txID) {
			return item
		}
	}
	return nil
}

func nativeMempoolTxIDEqual(a, b string) bool {
	normalize := func(value string) string {
		value = strings.TrimSpace(value)
		value = strings.TrimPrefix(strings.TrimPrefix(value, "0x"), "0X")
		return value
	}
	return strings.EqualFold(normalize(a), normalize(b))
}

func nativeMempoolEscrowAsset(target common.Asset) common.Asset {
	if target.IsSecuredAsset() {
		return target.GetLayer1Asset()
	}
	return target
}

func nativeMempoolSnapshotMimirs() (map[string]int64, error) {
	current, err := thornode.GetMimirs()
	if err != nil {
		return nil, err
	}
	result := make(map[string]int64, len(nativeMempoolMimirs))
	for _, setting := range nativeMempoolMimirs {
		value := setting.mocknetValue
		if currentValue, ok := nativeMempoolLookupMimir(current, setting.key); ok {
			value = currentValue
		}
		result[setting.key] = value
	}
	return result, nil
}

func nativeMempoolMimirsMatch(current, expected map[string]int64) bool {
	for key, expectedValue := range expected {
		value, ok := nativeMempoolLookupMimir(current, key)
		if !ok {
			for _, setting := range nativeMempoolMimirs {
				if strings.EqualFold(setting.key, key) {
					value, ok = setting.mocknetValue, true
					break
				}
			}
		}
		if !ok || value != expectedValue {
			return false
		}
	}
	return true
}

func nativeMempoolLookupMimir(mimirs map[string]int64, key string) (int64, bool) {
	for currentKey, value := range mimirs {
		if strings.EqualFold(currentKey, key) {
			return value, true
		}
	}
	return 0, false
}
