// querier_helpers.go contains all helper functions used by the query handlers.
// These functions support various query operations but are not query handlers themselves.

package thorchain

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitlab.com/thorchain/thornode/v3/common/wasmpermissions"

	sdkmath "cosmossdk.io/math"
	"github.com/blang/semver"
	"github.com/cometbft/cometbft/libs/log"
	tmhttp "github.com/cometbft/cometbft/rpc/client/http"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	zerolog "github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	keeperv1 "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/v1"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// ============ Package Variables ============

var (
	initManager = func(_ cosmos.Context, _ *Mgrs) {}
	queryExport = func(_ sdk.Context, _ *Mgrs) ([]byte, error) {
		return nil, fmt.Errorf("export query not supported")
	}
	tendermintClient   *tmhttp.HTTP
	initTendermintOnce = sync.Once{}
	nullLogger         = log.NewNopLogger()
)

// ============ Helper Functions ============

func initTendermint() {
	// get tendermint port from config
	portSplit := strings.Split(config.GetThornode().Tendermint.RPC.ListenAddress, ":")
	port := portSplit[len(portSplit)-1]

	// setup tendermint client
	var err error
	tendermintClient, err = tmhttp.New(fmt.Sprintf("tcp://localhost:%s", port), "/websocket")
	if err != nil {
		zerolog.Fatal().Err(err).Msg("fail to create tendermint client")
	}
}

func getPeerIDFromPubKey(pubkey common.PubKey) string {
	peerID, err := conversion.GetPeerIDFromPubKey(pubkey.String())
	if err != nil {
		// Don't break the entire endpoint if something goes wrong with the Peer ID derivation.
		return err.Error()
	}

	return peerID.String()
}

func getVaultChainAddresses(ctx cosmos.Context, vault Vault) []*types.VaultAddress {
	var result []*types.VaultAddress
	allChains := append(vault.GetChains(), common.THORChain)
	for _, c := range allChains.Distinct() {
		addr, err := vault.PubKey.GetAddress(c)
		if err != nil {
			ctx.Logger().Error("fail to get address for %s:%w", c.String(), err)
			continue
		}
		result = append(result,
			&types.VaultAddress{
				Chain:   c.String(),
				Address: addr.String(),
			})
	}
	return result
}

func getNodePreflightResult(ctx cosmos.Context, mgr *Mgrs, nodeAcc NodeAccount) (types.NodePreflightStatus, error) {
	constAccessor := mgr.GetConstants()
	preflightResult := types.NodePreflightStatus{}
	status, err := mgr.ValidatorMgr().NodeAccountPreflightCheck(ctx, nodeAcc, constAccessor)
	preflightResult.Status = status.String()
	if err != nil {
		preflightResult.Reason = err.Error()
		preflightResult.Code = 1
	} else {
		preflightResult.Reason = "OK"
		preflightResult.Code = 0
	}
	return preflightResult, nil
}

// Estimates current rewards for the NodeAccount taking into account bond-weighted rewards and slash points
func getNodeCurrentRewards(ctx cosmos.Context, mgr *Mgrs, nodeAcc NodeAccount, lastChurnHeight int64, totalBondReward, totalEffectiveBond, bondHardCap cosmos.Uint) (cosmos.Uint, error) {
	slashPts, err := mgr.Keeper().GetNodeAccountSlashPoints(ctx, nodeAcc.NodeAddress)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("fail to get node slash points: %w", err)
	}

	// Find number of blocks since the last churn (the last bond reward payout)
	totalActiveBlocks := ctx.BlockHeight() - lastChurnHeight

	// find number of blocks they were well behaved (ie active - slash points)
	earnedBlocks := totalActiveBlocks - slashPts
	if earnedBlocks < 0 {
		earnedBlocks = 0
	}

	naEffectiveBond := nodeAcc.Bond
	if naEffectiveBond.GT(bondHardCap) {
		naEffectiveBond = bondHardCap
	}

	// reward = totalBondReward * (naEffectiveBond / totalEffectiveBond) * (unslashed blocks since last churn / blocks since last churn)
	reward := common.GetUncappedShare(naEffectiveBond, totalEffectiveBond, totalBondReward)
	reward = common.GetUncappedShare(cosmos.NewUint(uint64(earnedBlocks)), cosmos.NewUint(uint64(totalActiveBlocks)), reward)
	return reward, nil
}

func newSaver(lp LiquidityProvider, pool Pool) *types.QuerySaverResponse {
	assetRedeemableValue := lp.GetSaversAssetRedeemValue(pool)

	gp := cosmos.NewDec(0)
	if !lp.AssetDepositValue.IsZero() {
		adv := cosmos.NewDec(lp.AssetDepositValue.BigInt().Int64())
		arv := cosmos.NewDec(assetRedeemableValue.BigInt().Int64())
		gp = arv.Sub(adv)
		gp = gp.Quo(adv)
	}

	return &types.QuerySaverResponse{
		Asset:              lp.Asset.GetLayer1Asset().String(),
		AssetAddress:       lp.AssetAddress.String(),
		LastAddHeight:      lp.LastAddHeight,
		LastWithdrawHeight: lp.LastWithdrawHeight,
		Units:              lp.Units.String(),
		AssetDepositValue:  lp.AssetDepositValue.String(),
		AssetRedeemValue:   assetRedeemableValue.String(),
		GrowthPct:          gp.String(),
	}
}

func newStreamingSwap(streamingSwap StreamingSwap, msgSwap MsgSwap) *types.QueryStreamingSwapResponse {
	var sourceAsset common.Asset
	// Leave the source_asset field empty if there is more than a single input Coin.
	if len(msgSwap.Tx.Coins) == 1 {
		sourceAsset = msgSwap.Tx.Coins[0].Asset
	}

	var failedSwaps []int64
	// Leave this nil (null rather than []) if the source is nil.
	if streamingSwap.FailedSwaps != nil {
		failedSwaps = make([]int64, len(streamingSwap.FailedSwaps))
		for i := range streamingSwap.FailedSwaps {
			failedSwaps[i] = int64(streamingSwap.FailedSwaps[i])
		}
	}

	return &types.QueryStreamingSwapResponse{
		TxId:              streamingSwap.TxID.String(),
		Interval:          int64(streamingSwap.Interval),
		Quantity:          int64(streamingSwap.Quantity),
		Count:             int64(streamingSwap.Count),
		LastHeight:        streamingSwap.LastHeight,
		TradeTarget:       streamingSwap.TradeTarget.String(),
		SourceAsset:       sourceAsset.String(),
		TargetAsset:       msgSwap.TargetAsset.String(),
		Destination:       msgSwap.Destination.String(),
		Deposit:           streamingSwap.Deposit.String(),
		In:                streamingSwap.In.String(),
		Out:               streamingSwap.Out.String(),
		FailedSwaps:       failedSwaps,
		FailedSwapReasons: streamingSwap.FailedSwapReasons,
	}
}

func extractVoter(ctx cosmos.Context, tx_id string, mgr *Mgrs) (common.TxID, ObservedTxVoter, error) {
	if len(tx_id) == 0 {
		return "", ObservedTxVoter{}, errors.New("tx id not provided")
	}
	hash, err := common.NewTxID(tx_id)
	if err != nil {
		ctx.Logger().Error("fail to parse tx id", "error", err)
		return "", ObservedTxVoter{}, fmt.Errorf("fail to parse tx id: %w", err)
	}
	voter, err := mgr.Keeper().GetObservedTxInVoter(ctx, hash)
	if err != nil {
		ctx.Logger().Error("fail to get observed tx voter", "error", err)
		return "", ObservedTxVoter{}, fmt.Errorf("fail to get observed tx voter: %w", err)
	}
	return hash, voter, nil
}

// TODO: Remove isSwap and isPending code when SwapFinalised field deprecated.
func checkPending(ctx cosmos.Context, keeper keeper.Keeper, voter ObservedTxVoter) (isSwap, isPending, pending bool, streamingSwap StreamingSwap) {
	// If there's no (confirmation-counting-complete) consensus transaction yet, don't spend time checking the swap status.
	if voter.Tx.IsEmpty() || !voter.Tx.IsFinal() {
		return
	}

	pending = keeper.HasSwapQueueItem(ctx, voter.TxID, 0) || keeper.HasAdvSwapQueueItem(ctx, voter.TxID)

	// Only look for streaming information when a swap is pending.
	if pending {
		var err error
		streamingSwap, err = keeper.GetStreamingSwap(ctx, voter.TxID)
		if err != nil {
			// Log the error, but continue without streaming information.
			ctx.Logger().Error("fail to get streaming swap", "error", err)
		}
	}

	memo, err := ParseMemoWithTHORNames(ctx, keeper, voter.Tx.Tx.Memo)
	if err != nil {
		// If unable to parse, assume not a (valid) swap or limit swap memo.
		return
	}

	memoType := memo.GetType()
	// If the memo asset is a synth, as with Savers add liquidity or withdraw, a swap is assumed to be involved.
	if memoType == TxSwap || memoType == TxLimitSwap || memo.GetAsset().IsSyntheticAsset() {
		isSwap = true
		// Only check the KVStore when the inbound transaction has already been finalised
		// and when there haven't been any Actions planned.
		// This will also check the KVStore when an inbound transaction has no output,
		// such as the output being not enough to cover a fee.
		if voter.FinalisedHeight != 0 && len(voter.Actions) == 0 {
			// Use of Swap Queue or Adv Swap Queue depends on Mimir key AdvSwapQueueBooks rather than memo type, so check both.
			isPending = pending
		}
	}

	return
}

// Get the largest number of signers for a not-final (pre-confirmation-counting) and final Txs respectively.
func countSigners(voter ObservedTxVoter) (int64, int64) {
	var notFinalCount, finalCount int
	for i, refTx := range voter.Txs {
		signersMap := make(map[string]bool)
		final := refTx.IsFinal()
		for f, tx := range voter.Txs {
			// Earlier Txs already checked against all, so no need to check,
			// but do include the signers of the current Txs.
			if f < i {
				continue
			}
			// Count larger number of signers for not-final and final observations separately.
			if tx.IsFinal() != final {
				continue
			}
			if !refTx.Tx.EqualsEx(tx.Tx) {
				continue
			}

			for _, signer := range tx.GetSigners() {
				signersMap[signer.String()] = true
			}
		}
		if final && len(signersMap) > finalCount {
			finalCount = len(signersMap)
		} else if !final && len(signersMap) > notFinalCount {
			notFinalCount = len(signersMap)
		}
	}
	return int64(notFinalCount), int64(finalCount)
}

// Call newTxStagesResponse from both queryTxStatus (which includes the stages) and queryTxStages.
// TODO: Remove isSwap and isPending arguments when SwapFinalised deprecated in favour of SwapStatus.
// TODO: Deprecate InboundObserved.Started field in favour of the observation counting.
func newTxStagesResponse(ctx cosmos.Context, voter ObservedTxVoter, isSwap, isPending, pending bool, streamingSwap StreamingSwap) (result types.QueryTxStagesResponse) {
	result.InboundObserved.PreConfirmationCount, result.InboundObserved.FinalCount = countSigners(voter)
	result.InboundObserved.Completed = !voter.Tx.IsEmpty()

	// If not Completed, fill in Started and do not proceed.
	if !result.InboundObserved.Completed {
		obStart := (len(voter.Txs) != 0)
		result.InboundObserved.Started = obStart
		return result
	}

	// Current block height is relevant in the confirmation counting and outbound stages.
	currentHeight := ctx.BlockHeight()

	// Confirmation counting
	result.InboundConfirmationCounted = &types.TxStatusResponse_InboundConfirmationCounted{
		SourceChain:       voter.Tx.Tx.Chain.String(),
		SourceChainHeight: voter.Tx.BlockHeight,
		SourceChainHash:   voter.Tx.Tx.ID.String(),
		ExternalObservedHeight: voter.Height,
		ConfCountingStartHeight: voter.Tx.FinaliseHeight,
		Chain:                   voter.Tx.Tx.Chain.String(),
		ChainHeight:             voter.Tx.BlockHeight,
		Hash:                    voter.Tx.Tx.ID.String(),
		ExternalConfDelayHeight: voter.Tx.FinaliseHeight,
		RemainingConfDelaySeconds: 0,
		Completed:               voter.Tx.IsFinal(),
	}

	chainConfDelayHeight := voter.Tx.GetConfirmationRequired()
	if chainConfDelayHeight > (currentHeight - voter.Tx.FinaliseHeight) {
		conf := chainConfDelayHeight - (currentHeight - voter.Tx.FinaliseHeight)
		result.InboundConfirmationCounted.RemainingConfDelaySeconds = conf * common.THORChain.ApproximateBlockMilliseconds() / 1000
	}

	// If not Completed, fill in fields and do not proceed.
	if !result.InboundConfirmationCounted.Completed {
		return result
	}

	// Inbound finalised
	result.InboundFinalised = &types.TxStatusResponse_InboundFinalised{
		SourceChain:             voter.Tx.Tx.Chain.String(),
		SourceChainHeight:       voter.Tx.BlockHeight,
		SourceChainHash:         voter.Tx.Tx.ID.String(),
		InboundConfirmedHeight:  voter.FinalisedHeight,
		Chain:                   voter.Tx.Tx.Chain.String(),
		ChainHeight:             voter.Tx.BlockHeight,
		Hash:                    voter.Tx.Tx.ID.String(),
		FinalizedHeight:         voter.FinalisedHeight,
		Completed:               voter.FinalisedHeight != 0,
	}

	// If not Completed, fill in fields and do not proceed.
	if !result.InboundFinalised.Completed {
		return result
	}

	// Deprecated SwapFinalised stage; replaced by SwapStatus stage.
	// TODO: Remove when the SwapFinalised field is deprecated.
	if isSwap {
		result.SwapFinalised = &types.TxStatusResponse_SwapFinalised{
			Chain:                  voter.Tx.Tx.Chain.String(),
			ChainHeight:            voter.Tx.BlockHeight,
			Hash:                   voter.Tx.Tx.ID.String(),
			ExternalObservedHeight: voter.Height,
			FinalizedHeight:        voter.FinalisedHeight,
			Completed:              !isPending,
		}
	}

	// Swap status (replaces the above once finalised).
	if pending {
		result.SwapStatus = &types.TxStatusResponse_SwapStatus{
			Pending:   pending,
			Streaming: &streamingSwap,
		}
	}

	// Outbound delay
	result.OutboundDelay = &types.TxStatusResponse_OutboundDelay{
		Chain:                 voter.Tx.Tx.Chain.String(),
		ChainHeight:           voter.Tx.BlockHeight,
		Hash:                  voter.Tx.Tx.ID.String(),
		InboundFinalizedHeight: voter.FinalisedHeight,
		OutboundDelay:        0,
		RemainingDelaySeconds: 0,
		RemainingDelayBlocks:  0,
		Completed:             false,
	}

	outDelay := voter.GetOutboundDelay()
	if outDelay > 0 {
		result.OutboundDelay.OutboundDelay = outDelay
		result.OutboundDelay.RemainingDelaySeconds = (outDelay - (currentHeight - voter.FinalisedHeight)) * common.THORChain.ApproximateBlockMilliseconds() / 1000
		result.OutboundDelay.RemainingDelayBlocks = outDelay - (currentHeight - voter.FinalisedHeight)
	}
	result.OutboundDelay.Completed = len(voter.OutTxs) > 0 || currentHeight >= (voter.FinalisedHeight+result.OutboundDelay.OutboundDelay)

	// If not Completed, fill in fields and do not proceed.
	if !result.OutboundDelay.Completed {
		return result
	}

	// Outbound signed
	// For refunds there may be multiple outbound transactions, so do not assume Completed and check all OutTxs.
	var allSigned bool
	if len(voter.OutTxs) > 0 {
		allSigned = true
		for _, tx := range voter.OutTxs {
			if tx.IsEmpty() {
				allSigned = false
			}
		}
	}
	result.OutboundSigned = &types.TxStatusResponse_OutboundSigned{
		ChainHeight:         voter.OutboundHeight,
		ScheduledOutboundHeight: voter.OutboundHeight,
		BlocksSinceSigned:       currentHeight - voter.OutboundHeight,
		Completed:               allSigned,
	}

	return result
}

func extractBlockHeight(ctx cosmos.Context, heightStr string) (int64, error) {
	if len(heightStr) == 0 {
		return -1, errors.New("block height not provided")
	}
	height, err := strconv.ParseInt(heightStr, 0, 64)
	if err != nil {
		ctx.Logger().Error("fail to parse block height", "error", err)
		return -1, fmt.Errorf("fail to parse block height: %w", err)
	}
	if height > ctx.BlockHeight() {
		return -1, fmt.Errorf("block height not available yet")
	}
	return height, nil
}

// -------------------------------------------------------------------------------------
// Generic Helpers
// -------------------------------------------------------------------------------------

func castTxOutItem(toi TxOutItem, height int64) *types.QueryTxOutItem {
	return &types.QueryTxOutItem{
		Height:                height, // Omitted if 0, for use in openapi.TxDetailsResponse
		VaultPubKey:           toi.VaultPubKey.String(),
		InHash:                toi.InHash.String(),
		OutHash:               toi.OutHash.String(),
		Chain:                 toi.Chain.String(),
		ToAddress:             toi.ToAddress.String(),
		Coin:                  &toi.Coin,
		MaxGas:                toi.MaxGas,
		GasRate:               toi.GasRate,
		Memo:                  toi.Memo,
		Aggregator:            toi.Aggregator,
		AggregatorTargetAsset: toi.AggregatorTargetAsset,
		AggregatorTargetLimit: toi.AggregatorTargetLimit.String(),
		CloutSpent:            toi.CloutSpent.String(),
	}
}

func castObservedTx(observedTx ObservedTx) types.QueryObservedTx {
	// Only display the Status if it is "done", not if "incomplete".
	status := ""
	if observedTx.Status != common.Status_incomplete {
		status = observedTx.Status.String()
	}
	return types.QueryObservedTx{
		Tx:                    observedTx.Tx,
		Status:                status,
		OutHashes:             observedTx.OutHashes,
		BlockHeight:           observedTx.BlockHeight,
		Signers:               observedTx.Signers,
		ObservedPubKey:        observedTx.ObservedPubKey,
		KeysignMs:             observedTx.KeysignMs,
		FinaliseHeight:        observedTx.FinaliseHeight,
		Aggregator:            observedTx.Aggregator,
		AggregatorTarget:      observedTx.AggregatorTarget,
		AggregatorTargetLimit: observedTx.AggregatorTargetLimit,
	}
}

func castVaultRouters(chainContracts []ChainContract) []*types.VaultRouter {
	// Leave this nil (null rather than []) if the source is nil.
	if chainContracts == nil {
		return nil
	}

	routers := make([]*types.VaultRouter, len(chainContracts))
	for i := range chainContracts {
		routers[i] = &types.VaultRouter{
			Chain:  chainContracts[i].Chain.String(),
			Router: chainContracts[i].Router.String(),
		}
	}
	return routers
}

func blockEvent(e sdk.Event) *types.BlockEvent {
	event := types.BlockEvent{}
	event.EventKvPair = append(event.EventKvPair, &types.EventKeyValuePair{
		Key:   "type",
		Value: e.Type,
	})

	for _, a := range e.Attributes {
		event.EventKvPair = append(event.EventKvPair, &types.EventKeyValuePair{
			Key:   a.Key,
			Value: a.Value,
		})
	}
	return &event
}

func eventMap(e sdk.Event) map[string]string {
	m := map[string]string{}
	m["type"] = e.Type
	for _, a := range e.Attributes {
		m[a.Key] = a.Value
	}
	return m
}

func simulate(ctx cosmos.Context, mgr Manager, msg sdk.Msg) (sdk.Events, error) {
	// use the first active node account as the signer
	nodeAccounts, err := mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		return nil, fmt.Errorf("no active node accounts: %w", err)
	}

	// set the signer
	switch m := msg.(type) {
	case *MsgLoanOpen:
		m.Signer = nodeAccounts[0].NodeAddress
	case *MsgLoanRepayment:
		m.Signer = nodeAccounts[0].NodeAddress
	case *MsgSwap:
		m.Signer = nodeAccounts[0].NodeAddress
	}

	// set random txid
	txid := common.TxID(common.RandHexString(64))
	ctx = ctx.WithValue(constants.CtxLoanTxID, txid)

	// validate
	msgV, ok := msg.(sdk.HasValidateBasic)
	if !ok {
		return nil, fmt.Errorf("message doesn't support validation")
	}
	err = msgV.ValidateBasic()
	if err != nil {
		return nil, fmt.Errorf("failed to validate message: %w", err)
	}

	// TODO: evaluate if this CacheMultiStore is still needed given
	//   that the querier is called with a CacheContext.
	// intercept events and avoid modifying state
	cms := ctx.MultiStore().CacheMultiStore() // never call cms.Write()
	em := cosmos.NewEventManager()
	ctx = ctx.WithMultiStore(cms).WithEventManager(em)

	// disable logging
	ctx = ctx.WithLogger(nullLogger)

	// reset the swap queue
	iter := mgr.Keeper().GetSwapQueueIterator(ctx)
	for ; iter.Valid(); iter.Next() {
		mgr.Keeper().DeleteKey(ctx, string(iter.Key()))
	}
	iter.Close()

	// save pool state
	pools, err := mgr.Keeper().GetPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get pools: %w", err)
	}

	// simulate the handler
	_, err = NewInternalHandler(mgr)(ctx, msg)
	if err != nil {
		return nil, fmt.Errorf("failed to simulate handler: %w", err)
	}

	// simulate end block, loop it until the swap queue is empty
	queueEmpty := false
	for count := int64(0); !queueEmpty && count < 1000; count += 1 {
		err = mgr.SwapQ().EndBlock(ctx.WithBlockHeight(ctx.BlockHeight()+count), mgr)
		if err != nil {
			return nil, fmt.Errorf("failed to simulate end block: %w", err)
		}

		for _, pool := range pools {
			_ = mgr.Keeper().SetPool(ctx, pool)
		}

		iter = mgr.Keeper().GetSwapQueueIterator(ctx)
		queueEmpty = !iter.Valid()
		iter.Close()
	}

	return em.Events(), nil
}

// runePerDollarIgnoreHalt mirrors keeper.RunePerDollar, ignoring halts by using
// dollarsPerRuneIgnoreHalt to return the last known price instead of "0"
func runePerDollarIgnoreHalt(ctx cosmos.Context, k keeper.Keeper) cosmos.Uint {
	runePerDollar := dollarsPerRuneIgnoreHalt(ctx, k)

	one := cosmos.NewUint(common.One)

	return common.GetUncappedShare(one, runePerDollar, one)
}

// dollarsPerRuneIgnoreHalt mirrors keeper.DollarsPerRune, but ignoring halts if all
// anchor chains are unavailable with them. This is used for the TOR price on pools to
// ensure a best effort price is returned whenever possible instead of zero.
func dollarsPerRuneIgnoreHalt(ctx cosmos.Context, k keeper.Keeper) cosmos.Uint {
	// check for mimir override
	dollarsPerRune, err := k.GetMimir(ctx, "DollarsPerRune")
	if err == nil && dollarsPerRune > 0 {
		return cosmos.NewUint(uint64(dollarsPerRune))
	}

	usdAssets := k.GetAnchors(ctx, common.TOR)

	// if all anchor chains have trading halt, then ignore trading halt
	ignoreHalt := true
	for _, asset := range usdAssets {
		if !k.IsChainTradingHalted(ctx, asset.Chain) {
			ignoreHalt = false
			break
		}
	}

	p := make([]cosmos.Uint, 0)
	for _, asset := range usdAssets {
		if !ignoreHalt && k.IsChainTradingHalted(ctx, asset.Chain) {
			continue
		}
		pool, err := k.GetPool(ctx, asset)
		if err != nil {
			ctx.Logger().Error("fail to get usd pool", "asset", asset.String(), "error", err)
			continue
		}
		if !pool.IsAvailable() {
			continue
		}
		// value := common.GetUncappedShare(pool.BalanceAsset, pool.BalanceRune, cosmos.NewUint(common.One))
		value := pool.RuneValueInAsset(cosmos.NewUint(constants.DollarMulti * common.One))

		if !value.IsZero() {
			p = append(p, value)
		}
	}
	return common.GetMedianUint(p).QuoUint64(constants.DollarMulti)
}