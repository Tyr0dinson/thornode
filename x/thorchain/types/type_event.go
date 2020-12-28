package types

import (
	"fmt"
	"strconv"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

// all event types support by THORChain
const (
	SwapEventType             = `swap`
	AddLiquidityEventType     = `add_liquidity`
	WithdrawEventType         = `withdraw`
	DonateEventType           = `donate`
	PoolEventType             = `pool`
	RewardEventType           = `rewards`
	RefundEventType           = `refund`
	BondEventType             = `bond`
	GasEventType              = `gas`
	ReserveEventType          = `reserve`
	SlashEventType            = `slash`
	ErrataEventType           = `errata`
	FeeEventType              = `fee`
	OutboundEventType         = `outbound`
	TSSKeygenMetricEventType  = `tss_keygen`
	TSSKeysignMetricEventType = `tss_keysign`
	SlashPointEventType       = `slash_points`
)

// PoolMods a list of pool modifications
type PoolMods []PoolMod

// NewPoolMod create a new instance of PoolMod
func NewPoolMod(asset common.Asset, runeAmt cosmos.Uint, runeAdd bool, assetAmt cosmos.Uint, assetAdd bool) PoolMod {
	return PoolMod{
		Asset:    asset,
		RuneAmt:  runeAmt,
		RuneAdd:  runeAdd,
		AssetAmt: assetAmt,
		AssetAdd: assetAdd,
	}
}

// NewEventSwap create a new swap event
func NewEventSwap(pool common.Asset, priceTarget, fee, tradeSlip, liquidityFeeInRune cosmos.Uint, inTx common.Tx, emitAsset common.Coin) EventSwap {
	return EventSwap{
		Pool:               pool,
		PriceTarget:        priceTarget,
		TradeSlip:          tradeSlip,
		LiquidityFee:       fee,
		LiquidityFeeInRune: liquidityFeeInRune,
		InTx:               inTx,
		EmitAsset:          emitAsset,
	}
}

// Type return a string that represent the type, it should not duplicated with other event
func (e EventSwap) Type() string {
	return SwapEventType
}

// Events convert EventSwap to key value pairs used in cosmos
func (e EventSwap) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("pool", e.Pool.String()),
		cosmos.NewAttribute("price_target", e.PriceTarget.String()),
		cosmos.NewAttribute("trade_slip", e.TradeSlip.String()),
		cosmos.NewAttribute("liquidity_fee", e.LiquidityFee.String()),
		cosmos.NewAttribute("liquidity_fee_in_rune", e.LiquidityFeeInRune.String()),
		cosmos.NewAttribute("emit_asset", e.EmitAsset.String()),
	)
	evt = evt.AppendAttributes(e.InTx.ToAttributes()...)
	return cosmos.Events{evt}, nil
}

// NewEventAddLiquidity create a new add liquidity event
func NewEventAddLiquidity(pool common.Asset,
	su cosmos.Uint,
	runeAddress common.Address,
	runeAmount,
	assetAmount cosmos.Uint,
	runeTxID,
	assetTxID common.TxID,
	assetAddress common.Address) EventAddLiquidity {
	return EventAddLiquidity{
		Pool:          pool,
		ProviderUnits: su,
		RuneAddress:   runeAddress,
		RuneAmount:    runeAmount,
		AssetAmount:   assetAmount,
		RuneTxID:      runeTxID,
		AssetTxID:     assetTxID,
		AssetAddress:  assetAddress,
	}
}

// Type return the event type
func (e EventAddLiquidity) Type() string {
	return AddLiquidityEventType
}

// Events return cosmos.Events which is cosmos.Attribute(key value pairs)
func (e EventAddLiquidity) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("pool", e.Pool.String()),
		cosmos.NewAttribute("liquidity_provider_units", e.ProviderUnits.String()),
		cosmos.NewAttribute("rune_address", e.RuneAddress.String()),
		cosmos.NewAttribute("rune_amount", e.RuneAmount.String()),
		cosmos.NewAttribute("asset_amount", e.AssetAmount.String()),
		cosmos.NewAttribute("asset_address", e.AssetAddress.String()),
	)
	if !e.RuneTxID.Equals(e.AssetTxID) && !e.RuneTxID.IsEmpty() {
		evt = evt.AppendAttributes(cosmos.NewAttribute(fmt.Sprintf("%s_txid", common.RuneAsset().Chain), e.RuneTxID.String()))
	}

	if !e.AssetTxID.IsEmpty() {
		evt = evt.AppendAttributes(cosmos.NewAttribute(fmt.Sprintf("%s_txid", e.Pool.Chain), e.AssetTxID.String()))
	}
	return cosmos.Events{
		evt,
	}, nil
}

// NewEventWithdraw create a new withdraw event
func NewEventWithdraw(pool common.Asset, su cosmos.Uint, basisPts int64, asym cosmos.Dec, inTx common.Tx, emitAsset, emitRune cosmos.Uint) EventWithdraw {
	return EventWithdraw{
		Pool:          pool,
		ProviderUnits: su,
		BasisPoints:   basisPts,
		Asymmetry:     asym,
		InTx:          inTx,
		EmitAsset:     emitAsset,
		EmitRune:      emitRune,
	}
}

// Type return the withdraw event type
func (e EventWithdraw) Type() string {
	return WithdrawEventType
}

// Events return the cosmos event
func (e EventWithdraw) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("pool", e.Pool.String()),
		cosmos.NewAttribute("liquidity_provider_units", e.ProviderUnits.String()),
		cosmos.NewAttribute("basis_points", strconv.FormatInt(e.BasisPoints, 10)),
		cosmos.NewAttribute("asymmetry", e.Asymmetry.String()),
		cosmos.NewAttribute("emit_asset", e.EmitAsset.String()),
		cosmos.NewAttribute("emit_rune", e.EmitRune.String()))
	evt = evt.AppendAttributes(e.InTx.ToAttributes()...)
	return cosmos.Events{evt}, nil
}

// NewEventDonate create a new donate event
func NewEventDonate(pool common.Asset, inTx common.Tx) EventDonate {
	return EventDonate{
		Pool: pool,
		InTx: inTx,
	}
}

// Type return donate event type
func (e EventDonate) Type() string {
	return DonateEventType
}

// Events get all events
func (e EventDonate) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("pool", e.Pool.String()))
	evt = evt.AppendAttributes(e.InTx.ToAttributes()...)
	return cosmos.Events{evt}, nil
}

// NewEventPool create a new pool change event
func NewEventPool(pool common.Asset, status PoolStatus) EventPool {
	return EventPool{
		Pool:   pool,
		Status: status,
	}
}

// Type return pool event type
func (e EventPool) Type() string {
	return PoolEventType
}

// Events provide an instance of cosmos.Events
func (e EventPool) Events() (cosmos.Events, error) {
	return cosmos.Events{
		cosmos.NewEvent(e.Type(),
			cosmos.NewAttribute("pool", e.Pool.String()),
			cosmos.NewAttribute("pool_status", e.Status.String())),
	}, nil
}

// NewEventRewards create a new reward event
func NewEventRewards(bondReward cosmos.Uint, poolRewards []PoolAmt) EventRewards {
	return EventRewards{
		BondReward:  bondReward,
		PoolRewards: poolRewards,
	}
}

// Type return reward event type
func (e EventRewards) Type() string {
	return RewardEventType
}

func (e EventRewards) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("bond_reward", e.BondReward.String()),
	)
	for _, item := range e.PoolRewards {
		evt = evt.AppendAttributes(cosmos.NewAttribute(item.Asset.String(), strconv.FormatInt(item.Amount, 10)))
	}
	return cosmos.Events{evt}, nil
}

// NewEventRefund create a new EventRefund
func NewEventRefund(code uint32, reason string, inTx common.Tx, fee common.Fee) EventRefund {
	return EventRefund{
		Code:   code,
		Reason: reason,
		InTx:   inTx,
		Fee:    fee,
	}
}

// Type return reward event type
func (e EventRefund) Type() string {
	return RefundEventType
}

// Events return events
func (e EventRefund) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("code", strconv.FormatUint(uint64(e.Code), 10)),
		cosmos.NewAttribute("reason", e.Reason),
	)
	evt = evt.AppendAttributes(e.InTx.ToAttributes()...)
	return cosmos.Events{evt}, nil
}

// NewEventBond create a new Bond Events
func NewEventBond(amount cosmos.Uint, bondType BondType, txIn common.Tx) EventBond {
	return EventBond{
		Amount:   amount,
		BondType: bondType,
		TxIn:     txIn,
	}
}

// Type return bond event Type
func (e EventBond) Type() string {
	return BondEventType
}

// Events return all the event attributes
func (e EventBond) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("amount", e.Amount.String()),
		cosmos.NewAttribute("bound_type", string(e.BondType)))
	evt = evt.AppendAttributes(e.TxIn.ToAttributes()...)
	return cosmos.Events{evt}, nil
}

// NewEventGas create a new EventGas instance
func NewEventGas() *EventGas {
	return &EventGas{
		Pools: make([]GasPool, 0),
	}
}

// UpsertGasPool update the Gas Pools hold by EventGas instance
// if the given gasPool already exist, then it merge the gasPool with internal one , otherwise add it to the list
func (e *EventGas) UpsertGasPool(pool GasPool) {
	for i, p := range e.Pools {
		if p.Asset == pool.Asset {
			e.Pools[i].RuneAmt = p.RuneAmt.Add(pool.RuneAmt)
			e.Pools[i].AssetAmt = p.AssetAmt.Add(pool.AssetAmt)
			return
		}
	}
	e.Pools = append(e.Pools, pool)
}

// Type return event type
func (e *EventGas) Type() string {
	return GasEventType
}

func (e *EventGas) Events() (cosmos.Events, error) {
	events := make(cosmos.Events, 0, len(e.Pools))
	for _, item := range e.Pools {
		evt := cosmos.NewEvent(e.Type(),
			cosmos.NewAttribute("asset", item.Asset.String()),
			cosmos.NewAttribute("asset_amt", item.AssetAmt.String()),
			cosmos.NewAttribute("rune_amt", item.RuneAmt.String()),
			cosmos.NewAttribute("transaction_count", strconv.FormatInt(item.Count, 10)))
		events = append(events, evt)
	}
	return events, nil
}

// NewEventReserve create a new instance of EventReserve
func NewEventReserve(contributor ReserveContributor, inTx common.Tx) EventReserve {
	return EventReserve{
		ReserveContributor: contributor,
		InTx:               inTx,
	}
}

func (e EventReserve) Type() string {
	return ReserveEventType
}

func (e EventReserve) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("contributor_address", e.ReserveContributor.Address.String()),
		cosmos.NewAttribute("amount", e.ReserveContributor.Amount.String()),
	)
	evt = evt.AppendAttributes(e.InTx.ToAttributes()...)
	return cosmos.Events{
		evt,
	}, nil
}

func NewEventSlash(pool common.Asset, slashAmount []PoolAmt) EventSlash {
	return EventSlash{
		Pool:        pool,
		SlashAmount: slashAmount,
	}
}

// Type return slash event type
func (e EventSlash) Type() string {
	return SlashEventType
}

func (e EventSlash) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("pool", e.Pool.String()))
	for _, item := range e.SlashAmount {
		evt = evt.AppendAttributes(cosmos.NewAttribute(item.Asset.String(), strconv.FormatInt(item.Amount, 10)))
	}
	return cosmos.Events{evt}, nil
}

func NewEventErrata(txID common.TxID, pools PoolMods) EventErrata {
	return EventErrata{
		TxID:  txID,
		Pools: pools,
	}
}

// Type return slash event type
func (e EventErrata) Type() string {
	return ErrataEventType
}

// Events return a cosmos.Events type
func (e EventErrata) Events() (cosmos.Events, error) {
	events := make(cosmos.Events, 0, len(e.Pools))
	for _, item := range e.Pools {
		evt := cosmos.NewEvent(e.Type(),
			cosmos.NewAttribute("in_tx_id", e.TxID.String()),
			cosmos.NewAttribute("asset", item.Asset.String()),
			cosmos.NewAttribute("rune_amt", item.RuneAmt.String()),
			cosmos.NewAttribute("rune_add", strconv.FormatBool(item.RuneAdd)),
			cosmos.NewAttribute("asset_amt", item.AssetAmt.String()),
			cosmos.NewAttribute("asset_add", strconv.FormatBool(item.AssetAdd)))
		events = append(events, evt)
	}
	return events, nil
}

// NewEventFee create a new EventFee
func NewEventFee(txID common.TxID, fee common.Fee) EventFee {
	return EventFee{
		TxID: txID,
		Fee:  fee,
	}
}

// Type get a string represent the event type
func (e EventFee) Type() string {
	return FeeEventType
}

// Events return events of cosmos.Event type
func (e EventFee) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("tx_id", e.TxID.String()),
		cosmos.NewAttribute("coins", e.Fee.Coins.String()),
		cosmos.NewAttribute("pool_deduct", e.Fee.PoolDeduct.String()))
	return cosmos.Events{evt}, nil
}

// NewEventOutbound create a new instance of EventOutbound
func NewEventOutbound(inTxID common.TxID, tx common.Tx) EventOutbound {
	return EventOutbound{
		InTxID: inTxID,
		Tx:     tx,
	}
}

// Type return a string which represent the type of this event
func (e EventOutbound) Type() string {
	return OutboundEventType
}

// Events return sdk events
func (e EventOutbound) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("in_tx_id", e.InTxID.String()))
	evt = evt.AppendAttributes(e.Tx.ToAttributes()...)
	return cosmos.Events{evt}, nil
}

// NewEventTssKeygenMetric create a new EventTssMetric
func NewEventTssKeygenMetric(pubkey common.PubKey, medianDurationMS int64) EventTssKeygenMetric {
	return EventTssKeygenMetric{
		PubKey:           pubkey,
		MedianDurationMs: medianDurationMS,
	}
}

// Type  return a string which represent the type of this event
func (e EventTssKeygenMetric) Type() string {
	return TSSKeygenMetricEventType
}

// Events return cosmos sdk events
func (e EventTssKeygenMetric) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("pubkey", e.PubKey.String()),
		cosmos.NewAttribute("median_duration_ms", strconv.FormatInt(e.MedianDurationMs, 10)))
	return cosmos.Events{evt}, nil
}

// NewEventTssKeysignMetric create a new EventTssMetric
func NewEventTssKeysignMetric(txID common.TxID, medianDurationMS int64) EventTssKeysignMetric {
	return EventTssKeysignMetric{
		TxID:             txID,
		MedianDurationMs: medianDurationMS,
	}
}

// Type  return a string which represent the type of this event
func (e EventTssKeysignMetric) Type() string {
	return TSSKeysignMetricEventType
}

// Events return cosmos sdk events
func (e EventTssKeysignMetric) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("txid", e.TxID.String()),
		cosmos.NewAttribute("median_duration_ms", strconv.FormatInt(e.MedianDurationMs, 10)))
	return cosmos.Events{evt}, nil
}

// NewEventSlashPoint create a new slash point event
func NewEventSlashPoint(addr cosmos.AccAddress, slashPoints int64, reason string) EventSlashPoint {
	return EventSlashPoint{
		NodeAddress: addr,
		SlashPoints: slashPoints,
		Reason:      reason,
	}
}

// Type return a string which represent the type of this event
func (e EventSlashPoint) Type() string {
	return SlashPointEventType
}

// Events return cosmos sdk events
func (e EventSlashPoint) Events() (cosmos.Events, error) {
	evt := cosmos.NewEvent(e.Type(),
		cosmos.NewAttribute("node_address", e.NodeAddress.String()),
		cosmos.NewAttribute("slash_points", strconv.FormatInt(e.SlashPoints, 10)),
		cosmos.NewAttribute("reason", e.Reason))
	return cosmos.Events{evt}, nil
}
