package types

import (
	"encoding/json"
	"strings"

	"github.com/blang/semver"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/openapi/gen"
)

// QueryKeygenBlock query keygen, displays signed keygen requests
type QueryKeygenBlock struct {
	KeygenBlock KeygenBlock `json:"keygen_block"`
	Signature   string      `json:"signature"`
}

// String implement fmt.Stringer
func (n QueryKeygenBlock) String() string {
	return n.KeygenBlock.String()
}

// QueryKeysign query keysign result
type QueryKeysign struct {
	Keysign   TxOut  `json:"keysign"`
	Signature string `json:"signature"`
}

// QueryYggdrasilVaults query yggdrasil vault result
// TODO remove on hard fork
type QueryYggdrasilVaults struct {
	BlockHeight           int64               `json:"block_height,omitempty"`
	PubKey                common.PubKey       `json:"pub_key,omitempty"`
	Coins                 common.Coins        `json:"coins"`
	Type                  VaultType           `json:"type,omitempty"`
	StatusSince           int64               `json:"status_since,omitempty"`
	Membership            []string            `json:"membership,omitempty"`
	Chains                []string            `json:"chains,omitempty"`
	InboundTxCount        int64               `json:"inbound_tx_count,omitempty"`
	OutboundTxCount       int64               `json:"outbound_tx_count,omitempty"`
	PendingTxBlockHeights []int64             `json:"pending_tx_block_heights,omitempty"`
	Routers               []ChainContract     `json:"routers"`
	Status                NodeStatus          `json:"status"`
	Bond                  cosmos.Uint         `json:"bond"`
	TotalValue            cosmos.Uint         `json:"total_value"`
	Addresses             []QueryChainAddress `json:"addresses"`
}

// QueryVaultResp used represent the informat return to client for query asgard
type QueryVaultResp struct {
	BlockHeight           int64               `json:"block_height,omitempty"`
	PubKey                common.PubKey       `json:"pub_key,omitempty"`
	Coins                 common.Coins        `json:"coins"`
	Type                  VaultType           `json:"type,omitempty"`
	Status                VaultStatus         `json:"status"`
	StatusSince           int64               `json:"status_since,omitempty"`
	Membership            []string            `json:"membership,omitempty"`
	Chains                []string            `json:"chains,omitempty"`
	InboundTxCount        int64               `json:"inbound_tx_count,omitempty"`
	OutboundTxCount       int64               `json:"outbound_tx_count,omitempty"`
	PendingTxBlockHeights []int64             `json:"pending_tx_block_heights,omitempty"`
	Routers               []ChainContract     `json:"routers"`
	Addresses             []QueryChainAddress `json:"addresses"`
	Frozen                []string            `json:"frozen,omitempty"`
}

type QueryChainAddress struct {
	Chain   common.Chain   `json:"chain"`
	Address common.Address `json:"address"`
}

// QueryLiquidityProvider holds all the information related to a liquidity provider
type QueryLiquidityProvider struct {
	Asset              common.Asset   `json:"asset"`
	RuneAddress        common.Address `json:"rune_address,omitempty"`
	AssetAddress       common.Address `json:"asset_address,omitempty"`
	LastAddHeight      int64          `json:"last_add_height,omitempty"`
	LastWithdrawHeight int64          `json:"last_withdraw_height,omitempty"`
	Units              cosmos.Uint    `json:"units"`
	PendingRune        cosmos.Uint    `json:"pending_rune"`
	PendingAsset       cosmos.Uint    `json:"pending_asset"`
	PendingTxId        string         `json:"pending_tx_id,omitempty"`
	RuneDepositValue   cosmos.Uint    `json:"rune_deposit_value"`
	AssetDepositValue  cosmos.Uint    `json:"asset_deposit_value"`
	RuneRedeemValue    cosmos.Uint    `json:"rune_redeem_value"`
	AssetRedeemValue   cosmos.Uint    `json:"asset_redeem_value"`
	LuviDepositValue   cosmos.Uint    `json:"luvi_deposit_value"`
	LuviRedeemValue    cosmos.Uint    `json:"luvi_redeem_value"`
	LuviGrowthPct      cosmos.Dec     `json:"luvi_growth_pct"`
}

// QueryBlockTx overrides the openapi type with a custom Tx field for marshaling.
type QueryBlockTx struct {
	openapi.BlockTx
	Tx json.RawMessage `json:"tx,omitempty"`
}

func (q QueryBlockTx) MarshalJSON() ([]byte, error) {
	toSerialize := map[string]interface{}{}
	toSerialize["hash"] = q.Hash
	toSerialize["tx"] = q.Tx
	toSerialize["result"] = q.Result
	return json.Marshal(toSerialize)
}

// QueryBlockResponse overrides the openapi type with a custom Txs field for marshaling.
type QueryBlockResponse struct {
	openapi.BlockResponse
	Txs []QueryBlockTx `json:"txs"`
}

// MarshalJSON custom marshalling to add the custom Tx field.
func (q QueryBlockResponse) MarshalJSON() ([]byte, error) {
	toSerialize := map[string]interface{}{}
	toSerialize["id"] = q.Id
	toSerialize["header"] = q.Header
	toSerialize["begin_block_events"] = q.BeginBlockEvents
	toSerialize["end_block_events"] = q.EndBlockEvents
	toSerialize["txs"] = q.Txs
	return json.Marshal(toSerialize)
}

// NewQueryLiquidityProvider creates a new QueryLiquidityProvider based on the given liquidity provider and pool
func NewQueryLiquidityProvider(lp LiquidityProvider, pool Pool, synthSupply cosmos.Uint, version semver.Version) QueryLiquidityProvider {
	_, runeRedeemValue := lp.GetRuneRedeemValue(version, pool, synthSupply)
	_, assetRedeemValue := lp.GetAssetRedeemValue(version, pool, synthSupply)
	_, luviDepositValue := lp.GetLuviDepositValue(pool)
	_, luviRedeemValue := lp.GetLuviRedeemValue(runeRedeemValue, assetRedeemValue)

	lgp := cosmos.NewDec(0)
	if !luviDepositValue.IsZero() {
		ldv := cosmos.NewDec(luviDepositValue.BigInt().Int64())
		lrv := cosmos.NewDec(luviRedeemValue.BigInt().Int64())
		lgp = lrv.Sub(ldv)
		lgp = lgp.Quo(ldv)
	}

	return QueryLiquidityProvider{
		Asset:              lp.Asset.GetLayer1Asset(),
		AssetAddress:       lp.AssetAddress,
		RuneAddress:        lp.RuneAddress,
		LastAddHeight:      lp.LastAddHeight,
		LastWithdrawHeight: lp.LastWithdrawHeight,
		PendingAsset:       lp.PendingAsset,
		PendingRune:        lp.PendingRune,
		PendingTxId:        lp.PendingTxID.String(),
		Units:              lp.Units,
		AssetDepositValue:  lp.AssetDepositValue,
		RuneDepositValue:   lp.RuneDepositValue,
		RuneRedeemValue:    runeRedeemValue,
		AssetRedeemValue:   assetRedeemValue,
		LuviRedeemValue:    luviRedeemValue,
		LuviDepositValue:   luviDepositValue,
		LuviGrowthPct:      lgp,
	}
}

// QueryMimirV2IDs contains the information about the mimir V2s currently being voted on
type QueryMimirV2IDs struct {
	ID        int             `json:"id"`
	Name      string          `json:"name"`
	VoteKey   string          `json:"vote_key"`
	LegacyKey string          `json:"legacy_key"`
	Type      string          `json:"type"`
	Votes     map[int64]int64 `json:"votes"`
}

// TODO: Deprecate InboundObserved.Started field in favour of the observation counting.
type InboundObservedStage struct {
	Started              *bool  `json:"started,omitempty"`
	PreConfirmationCount uint64 `json:"pre_confirmation_count,omitempty"`
	FinalCount           uint64 `json:"final_count"`
	Completed            bool   `json:"completed"`
}

// Querier context contains the query's provided height, but not the full block context,
// so do not use BlockTime to provide a timestamp estimate.
type InboundConfirmationCountedStage struct {
	CountingStartHeight             int64        `json:"counting_start_height,omitempty"`
	Chain                           common.Chain `json:"chain,omitempty"`
	ExternalObservedHeight          int64        `json:"external_observed_height,omitempty"`
	ExternalConfirmationDelayHeight int64        `json:"external_confirmation_delay_height,omitempty"`
	RemainingConfirmationSeconds    *int64       `json:"remaining_confirmation_seconds,omitempty"`
	Completed                       bool         `json:"completed"`
}

type DoneStage struct {
	Completed bool `json:"completed"`
}

type StreamingStatus struct {
	Interval uint64 `json:"interval"`
	Quantity uint64 `json:"quantity"`
	Count    uint64 `json:"count"`
}

func NewStreamingStatus(streamingSwap StreamingSwap) StreamingStatus {
	return StreamingStatus{
		Interval: streamingSwap.Interval,
		Quantity: streamingSwap.Quantity,
		Count:    streamingSwap.Count,
	}
}

type SwapStatus struct {
	Pending   bool             `json:"pending"`
	Streaming *StreamingStatus `json:"streaming,omitempty"`
}

type OutboundDelayStage struct {
	RemainingDelayBlocks  *int64 `json:"remaining_delay_blocks,omitempty"`
	RemainingDelaySeconds *int64 `json:"remaining_delay_seconds,omitempty"`
	Completed             bool   `json:"completed"`
}

type OutboundSignedStage struct {
	ScheduledOutboundHeight *int64 `json:"scheduled_outbound_height,omitempty"`
	BlocksSinceScheduled    *int64 `json:"blocks_since_scheduled,omitempty"`
	Completed               bool   `json:"completed"`
}

// TODO: Deprecate SwapFinalised field in favour of SwapStatus.
type QueryTxStages struct {
	// Pointers so that the omitempty can recognise 'nil'.
	// Structs used for all stages for easier user looping through 'Completed' fields.
	InboundObserved            InboundObservedStage             `json:"inbound_observed"`
	InboundConfirmationCounted *InboundConfirmationCountedStage `json:"inbound_confirmation_counted,omitempty"`
	InboundFinalised           *DoneStage                       `json:"inbound_finalised,omitempty"`
	SwapStatus                 *SwapStatus                      `json:"swap_status,omitempty"`
	SwapFinalised              *DoneStage                       `json:"swap_finalised,omitempty"`
	OutboundDelay              *OutboundDelayStage              `json:"outbound_delay,omitempty"`
	OutboundSigned             *OutboundSignedStage             `json:"outbound_signed,omitempty"`
}

// Get the largest number of signers for a not-final (pre-confirmation-counting) and final Txs respectively.
func countSigners(voter ObservedTxVoter) (notFinalCount, finalCount uint64) {
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
		if final && uint64(len(signersMap)) > finalCount {
			finalCount = uint64(len(signersMap))
		} else if uint64(len(signersMap)) > notFinalCount {
			notFinalCount = uint64(len(signersMap))
		}
	}
	return
}

// TODO: Remove isSwap and isPending arguments when SwapFinalised deprecated.
// TODO: Deprecate InboundObserved.Started field in favour of the observation counting.
func NewQueryTxStages(ctx cosmos.Context, voter ObservedTxVoter, isSwap, isPending, pending bool, streamingSwap StreamingSwap) (result QueryTxStages) {
	result.InboundObserved.PreConfirmationCount, result.InboundObserved.FinalCount = countSigners(voter)
	result.InboundObserved.Completed = !voter.Tx.IsEmpty()

	// If not Completed, fill in Started and do not proceed.
	if !result.InboundObserved.Completed {
		obStart := (len(voter.Txs) != 0)
		result.InboundObserved.Started = &obStart
		return result
	}

	// Current block height is relevant in the confirmation counting and outbound stages.
	currentHeight := ctx.BlockHeight()

	// Only fill in InboundConfirmationCounted when confirmation counting took place.
	if voter.Height != 0 {
		var confCount InboundConfirmationCountedStage

		// Set the Completed state first.
		confCount.Completed = !(voter.Tx.FinaliseHeight > voter.Tx.BlockHeight)

		// Only fill in other fields if not Completed.
		if !confCount.Completed {
			confCount.CountingStartHeight = voter.Height
			confCount.Chain = voter.Tx.Tx.Chain
			confCount.ExternalObservedHeight = voter.Tx.BlockHeight
			confCount.ExternalConfirmationDelayHeight = voter.Tx.FinaliseHeight

			estConfMs := voter.Tx.Tx.Chain.ApproximateBlockMilliseconds() * (confCount.ExternalConfirmationDelayHeight - confCount.ExternalObservedHeight)
			if currentHeight > confCount.CountingStartHeight {
				estConfMs -= (currentHeight - confCount.CountingStartHeight) * common.THORChain.ApproximateBlockMilliseconds()
			}
			estConfSec := estConfMs / 1000
			// Floor at 0.
			if estConfSec < 0 {
				estConfSec = 0
			}
			confCount.RemainingConfirmationSeconds = &estConfSec
		}

		result.InboundConfirmationCounted = &confCount
	}

	var inboundFinalised DoneStage
	inboundFinalised.Completed = (voter.FinalisedHeight != 0)
	result.InboundFinalised = &inboundFinalised

	var swapStatus SwapStatus
	swapStatus.Pending = pending
	// Only display the SwapStatus stage's Streaming field when there's streaming information available.
	if streamingSwap.Valid() == nil {
		streaming := NewStreamingStatus(streamingSwap)
		swapStatus.Streaming = &streaming
	}
	result.SwapStatus = &swapStatus

	// Whether there's an external outbound or not, show the SwapFinalised stage from the start.
	if isSwap {
		var swapFinalisedState DoneStage

		swapFinalisedState.Completed = false
		if !isPending && result.InboundFinalised.Completed {
			// Record as completed only when not pending after the inbound has already been finalised.
			swapFinalisedState.Completed = true
		}

		result.SwapFinalised = &swapFinalisedState
	}

	// Only fill ExternalOutboundDelay and ExternalOutboundKeysign for inbound transactions with an external outbound;
	// namely, transactions with an outbound_height .
	if voter.OutboundHeight == 0 {
		return result
	}

	// Only display the OutboundDelay stage when there's a delay.
	if voter.OutboundHeight > voter.FinalisedHeight {
		var outDelay OutboundDelayStage

		// Set the Completed state first.
		outDelay.Completed = (currentHeight >= voter.OutboundHeight)

		// Only fill in other fields if not Completed.
		if !outDelay.Completed {
			remainBlocks := voter.OutboundHeight - currentHeight
			outDelay.RemainingDelayBlocks = &remainBlocks

			remainSec := remainBlocks * common.THORChain.ApproximateBlockMilliseconds() / 1000
			outDelay.RemainingDelaySeconds = &remainSec
		}

		result.OutboundDelay = &outDelay
	}

	var outSigned OutboundSignedStage

	// Set the Completed state first.
	outSigned.Completed = (voter.Tx.Status != Status_incomplete)

	// Only fill in other fields if not Completed.
	if !outSigned.Completed {
		scheduledHeight := voter.OutboundHeight
		outSigned.ScheduledOutboundHeight = &scheduledHeight

		// Only fill in BlocksSinceScheduled if the outbound delay is complete.
		if currentHeight >= scheduledHeight {
			sinceScheduled := currentHeight - scheduledHeight
			outSigned.BlocksSinceScheduled = &sinceScheduled
		}
	}

	result.OutboundSigned = &outSigned

	return result
}

type QueryPlannedOutTx struct {
	Chain     common.Chain   `json:"chain"`
	ToAddress common.Address `json:"to_address"`
	Coin      common.Coin    `json:"coin"`
	Refund    bool           `json:"refund"`
}

func NewQueryPlannedOutTxs(outTxs ...TxOutItem) []QueryPlannedOutTx {
	var result []QueryPlannedOutTx
	var isRefund bool
	for _, outTx := range outTxs {
		// Assume that any outbound memo not matching the expected pattern isn't a refund memo.
		isRefund = strings.HasPrefix(outTx.Memo, "REFUND")

		result = append(result, QueryPlannedOutTx{outTx.Chain, outTx.ToAddress, outTx.Coin, isRefund})
	}

	return result
}

type QueryTxStatus struct {
	// A Tx pointer so that the omitempty can recognise 'nil'.
	Tx            *common.Tx          `json:"tx,omitempty"`
	PlannedOutTxs []QueryPlannedOutTx `json:"planned_out_txs,omitempty"`
	OutTxs        []common.Tx         `json:"out_txs,omitempty"`
	Stages        QueryTxStages       `json:"stages"`
}

// TODO: Remove isSwap and isPending arguments when SwapFinalised deprecated.
func NewQueryTxStatus(ctx cosmos.Context, voter ObservedTxVoter, isSwap, isPending, pending bool, streamingSwap StreamingSwap) QueryTxStatus {
	var result QueryTxStatus

	// If there's a consensus Tx, display that.
	// If not, but there's at least one observation, display the first observation's Tx.
	// If there are no observations yet, don't display a Tx (only showing the 'Observation' stage with 'Started' false).
	if !voter.Tx.Tx.IsEmpty() {
		result.Tx = &voter.Tx.Tx
	} else if len(voter.Txs) > 0 {
		result.Tx = &voter.Txs[0].Tx
	}

	// If there are no voter Actions yet, result PlannedOutTxs will stay empty and not be displayed.
	result.PlannedOutTxs = NewQueryPlannedOutTxs(voter.Actions...)

	// If there are no voter OutTxs yet, result OutTxs will stay empty and not be displayed.
	result.OutTxs = voter.OutTxs

	result.Stages = NewQueryTxStages(ctx, voter, isSwap, isPending, pending, streamingSwap)

	return result
}

// QuerySaver holds all the information related to a saver
type QuerySaver struct {
	Asset              common.Asset   `json:"asset"`
	AssetAddress       common.Address `json:"asset_address"`
	LastAddHeight      int64          `json:"last_add_height,omitempty"`
	LastWithdrawHeight int64          `json:"last_withdraw_height,omitempty"`
	Units              cosmos.Uint    `json:"units"`
	AssetDepositValue  cosmos.Uint    `json:"asset_deposit_value"`
	AssetRedeemValue   cosmos.Uint    `json:"asset_redeem_value"`
	GrowthPct          cosmos.Dec     `json:"growth_pct"`
}

// NewQuerySaver creates a new QuerySaver based on the given liquidity provider parameters and pool
func NewQuerySaver(lp LiquidityProvider, pool Pool) QuerySaver {
	assetRedeemableValue := lp.GetSaversAssetRedeemValue(pool)

	gp := cosmos.NewDec(0)
	if !lp.AssetDepositValue.IsZero() {
		adv := cosmos.NewDec(lp.AssetDepositValue.BigInt().Int64())
		arv := cosmos.NewDec(assetRedeemableValue.BigInt().Int64())
		gp = arv.Sub(adv)
		gp = gp.Quo(adv)
	}

	return QuerySaver{
		Asset:              lp.Asset.GetLayer1Asset(),
		AssetAddress:       lp.AssetAddress,
		LastAddHeight:      lp.LastAddHeight,
		LastWithdrawHeight: lp.LastWithdrawHeight,
		Units:              lp.Units,
		AssetDepositValue:  lp.AssetDepositValue,
		AssetRedeemValue:   lp.GetSaversAssetRedeemValue(pool),
		GrowthPct:          gp,
	}
}

// QueryStreamingSwap holds all the information related to a streaming swap
type QueryStreamingSwap struct {
	TxID              common.TxID    `json:"tx_id,omitempty"`
	Interval          uint64         `json:"interval,omitempty"`
	Quantity          uint64         `json:"quantity,omitempty"`
	Count             uint64         `json:"count,omitempty"`
	LastHeight        int64          `json:"last_height,omitempty"`
	TradeTarget       cosmos.Uint    `json:"trade_target"`
	SourceAsset       common.Asset   `json:"source_asset,omitempty"`
	TargetAsset       common.Asset   `json:"target_asset,omitempty"`
	Destination       common.Address `json:"destination,omitempty"`
	Deposit           cosmos.Uint    `json:"deposit"`
	In                cosmos.Uint    `json:"in"`
	Out               cosmos.Uint    `json:"out"`
	FailedSwaps       []uint64       `json:"failed_swaps,omitempty"`
	FailedSwapReasons []string       `json:"failed_swap_reasons,omitempty"`
}

// NewQueryStreamingSwap create a new QueryStreamingSwap based on the given streaming swap parameter
func NewQueryStreamingSwap(streamingSwap StreamingSwap, msgSwap MsgSwap) QueryStreamingSwap {
	var sourceAsset common.Asset
	// Leave the source_asset field empty if there is more than a single input Coin.
	if len(msgSwap.Tx.Coins) == 1 {
		sourceAsset = msgSwap.Tx.Coins[0].Asset
	}

	return QueryStreamingSwap{
		TxID:              streamingSwap.TxID,
		Interval:          streamingSwap.Interval,
		Quantity:          streamingSwap.Quantity,
		Count:             streamingSwap.Count,
		LastHeight:        streamingSwap.LastHeight,
		TradeTarget:       streamingSwap.TradeTarget,
		SourceAsset:       sourceAsset,
		TargetAsset:       msgSwap.TargetAsset,
		Destination:       msgSwap.Destination,
		Deposit:           streamingSwap.Deposit,
		In:                streamingSwap.In,
		Out:               streamingSwap.Out,
		FailedSwaps:       streamingSwap.FailedSwaps,
		FailedSwapReasons: streamingSwap.FailedSwapReasons,
	}
}

// QueryVaultPubKeyContract is a type to combine PubKey and it's related contract
type QueryVaultPubKeyContract struct {
	PubKey  common.PubKey   `json:"pub_key"`
	Routers []ChainContract `json:"routers"`
}

// QueryVaultsPubKeys represent the result for query vaults pubkeys
type QueryVaultsPubKeys struct {
	Asgard    []QueryVaultPubKeyContract `json:"asgard"`
	Yggdrasil []QueryVaultPubKeyContract `json:"yggdrasil"` // TODO remove on hard fork
	Inactive  []QueryVaultPubKeyContract `json:"inactive"`
}
