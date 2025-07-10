package thorchain

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	maxDeviation = 1
)

// PriceFeedQuorum to handle MsgObservedTx
type PriceFeedQuorumBatchHandler struct {
	mgr             Manager
	requiredSymbols []string
	requiredVersion []byte
}

// NewPriceFeedQuorumBatchHandler create a new instance of PriceFeedQuorumBatchHandler
func NewPriceFeedQuorumBatchHandler(mgr Manager) PriceFeedQuorumBatchHandler {
	symbols := mgr.GetConstants().GetStringValue(constants.RequiredPriceFeeds)

	hash := sha256.Sum256([]byte(symbols))

	return PriceFeedQuorumBatchHandler{
		mgr:             mgr,
		requiredSymbols: strings.Split(symbols, ","),
		requiredVersion: hash[:8],
	}
}

// Run is the main entry point of PriceFeedQuorumBatchHandler
func (h PriceFeedQuorumBatchHandler) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(*types.MsgPriceFeedQuorumBatch)
	if !ok {
		return nil, errInvalidMessage
	}

	err := h.validate(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("MsgPriceFeedQuorumBatch failed validation", "error", err)
		return nil, err
	}

	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error("fail to handle MsgPriceFeedQuorumBatch message", "error", err)
	}
	return result, err
}

func (h PriceFeedQuorumBatchHandler) validate(ctx cosmos.Context, msg types.MsgPriceFeedQuorumBatch) error {
	return msg.ValidateBasic()
}

func (h PriceFeedQuorumBatchHandler) handle(ctx cosmos.Context, msg types.MsgPriceFeedQuorumBatch) (*cosmos.Result, error) {
	ctx.Logger().Info("handle MsgPriceFeedQuorumBatch")

	// ignore price feed tx, if prices have already been processed to avoid
	// to prevent reorder attacks
	iterator := h.mgr.Keeper().GetPriceIterator(ctx)
	if iterator.Valid() {
		ctx.Logger().Error("price feed already processed")
		return &cosmos.Result{}, nil
	}

	activeNodes, err := h.mgr.Keeper().ListActiveValidators(ctx)
	if err != nil {
		err = wrapError(ctx, err, "fail to list active validators")
		return nil, err
	}

	// [BTC][thor1...]*big.Float{}
	pricesByAsset := map[string]map[string]*big.Float{}
	for _, symbol := range h.requiredSymbols {
		pricesByAsset[symbol] = map[string]*big.Float{}
	}

	missingPrices := map[string]int{}
	deviatingPrices := map[string]int{}

	for _, node := range activeNodes {
		missingPrices[node.NodeAddress.String()] = 0
		deviatingPrices[node.NodeAddress.String()] = 0
	}

	for _, qpf := range msg.QuoPriceFeeds {
		pf, err := h.handleQuorumPriceFeed(ctx, qpf, activeNodes)
		if err != nil {
			ctx.Logger().Error("fail to handle QuorumPriceFeed", "error", err)
			continue
		}

		for i, symbol := range h.requiredSymbols {
			rate := pf.Rates[i]
			if rate == nil || rate.Amount == 0 {
				missingPrices[pf.Node.String()] += 1
				continue
			}
			pricesByAsset[symbol][pf.Node.String()] = rate.BigFloat()
		}
	}

	// Verify and calculate on chain price

	// analyze-ignore(map-iteration)
	for symbol, priceByNode := range pricesByAsset {
		if !HasSuperMajority(len(priceByNode), len(activeNodes)) {
			ctx.Logger().Error(
				"not enough prices for asset", "asset", symbol,
				"num_prices", len(priceByNode), "num_nodes", len(activeNodes))
			continue
		}

		allPricesForSymbol := []*big.Float{}

		for _, node := range activeNodes {
			price, found := priceByNode[node.NodeAddress.String()]
			if !found {
				missingPrices[node.NodeAddress.String()]++
				continue
			}
			h.telemetryPrice(symbol, node.NodeAddress.String(), price)
			allPricesForSymbol = append(allPricesForSymbol, price)
		}

		deviation, median, err := ComputeMedianAbsoluteDeviation(allPricesForSymbol)
		if err != nil {
			ctx.Logger().Error("fail to compute deviation", "err", err)
			continue
		}

		margin := new(big.Float).Mul(deviation, big.NewFloat(maxDeviation))

		lowerBound := new(big.Float).Sub(median, margin)
		upperBound := new(big.Float).Add(median, margin)

		h.telemetryBounds(symbol, upperBound, lowerBound)

		// analyze-ignore(map-iteration)
		for node, price := range priceByNode {
			if price.Cmp(lowerBound) == -1 || price.Cmp(upperBound) == 1 {
				deviatingPrices[node]++
			}
		}

		oraclePrice := OraclePrice{
			Symbol: symbol,
			Price:  median.Text('f', -1),
		}

		err = h.mgr.Keeper().SetPrice(ctx, oraclePrice)
		if err != nil {
			ctx.Logger().Error("fail to set price", "err", err)
		}

		h.telemetryPrice(symbol, "final", median)

		priceEvent := NewEventOraclePrice(symbol, oraclePrice.Price)
		err = h.mgr.EventMgr().EmitEvent(ctx, priceEvent)
		if err != nil {
			ctx.Logger().Error("fail to emit switch event", "error", err)
		}
	}

	return &cosmos.Result{}, nil
}

func (h PriceFeedQuorumBatchHandler) handleQuorumPriceFeed(
	ctx cosmos.Context,
	qpf *common.QuorumPriceFeed,
	active NodeAccounts,
) (*PriceFeed, error) {
	pf := qpf.PriceFeed

	if bytes.Compare(pf.Version, h.requiredVersion) != 0 {
		return nil, fmt.Errorf("version does not match required version")
	}

	if len(pf.Rates) != len(h.requiredSymbols) {
		return nil, fmt.Errorf("amount of rates does not match required symbols")
	}

	signBz, err := pf.GetSignablePayload()
	if err != nil {
		return nil, fmt.Errorf("fail to get signable price feed payload: %v", err)
	}

	atts := qpf.Attestations

	if len(atts) == 0 {
		return nil, fmt.Errorf("no attestation found")
	}

	if len(atts) > 1 {
		return nil, fmt.Errorf("found more than one attestation")
	}

	att := atts[0]

	_, err = verifyQuorumAttestation(active, signBz, att)
	if err != nil {
		return nil, fmt.Errorf("fail to verify quorum price feed attestation: %v", err)
	}

	pk := secp256k1.PubKey{Key: att.PubKey}
	address := cosmos.AccAddress(pk.Address())

	timestamp := time.UnixMilli(pf.Time)

	h.telemetryLatency(address.String(), time.UnixMilli(qpf.PriceFeed.Time))

	if timestamp.Before(ctx.HeaderInfo().Time) {
		return nil, fmt.Errorf("price feed too old: %s", timestamp)
	}

	return &PriceFeed{
		Node:  address,
		Rates: pf.Rates,
	}, nil
}

func (h PriceFeedQuorumBatchHandler) telemetryBounds(
	symbol string,
	upper, lower *big.Float,
) {
	values := map[string]*big.Float{
		"upper": upper,
		"lower": lower,
	}

	// analyze-ignore(map-iteration)
	for label, value := range values {
		v, _ := value.Float32()
		telemetry.SetGaugeWithLabels(
			[]string{"thornode", "oracle_price_bounds"},
			v,
			[]metrics.Label{
				telemetry.NewLabel("symbol", symbol),
				telemetry.NewLabel("type", label),
			},
		)
	}
}

func (h PriceFeedQuorumBatchHandler) telemetryPrice(
	symbol, node string,
	price *big.Float,
) {
	v, _ := price.Float32()
	telemetry.SetGaugeWithLabels(
		[]string{"thornode", "oracle_price"},
		v,
		[]metrics.Label{
			telemetry.NewLabel("symbol", symbol),
			telemetry.NewLabel("node", node),
		},
	)
}

func (h PriceFeedQuorumBatchHandler) telemetryLatency(
	node string,
	timestamp time.Time,
) {
	latency := time.Since(timestamp).Seconds()

	telemetry.SetGaugeWithLabels(
		[]string{"thornode", "price_feed_latency"},
		float32(latency),
		[]metrics.Label{
			telemetry.NewLabel("node", node),
		},
	)
}

func ComputeMedianAbsoluteDeviation(values []*big.Float) (*big.Float, *big.Float, error) {
	median, err := ComputeMedian(values)
	if err != nil {
		return nil, nil, err
	}

	deviations := make([]*big.Float, len(values))
	for i, value := range values {
		difference := new(big.Float).Sub(value, median)
		deviations[i] = new(big.Float).Abs(difference)
	}

	mad, err := ComputeMedian(deviations)
	if err != nil {
		return nil, nil, err
	}

	return mad, median, nil
}

func ComputeMedian(values []*big.Float) (*big.Float, error) {
	if len(values) == 1 {
		return values[0], nil
	}

	sort.Slice(values, func(i, j int) bool {
		return values[i].Cmp(values[j]) < 0
	})

	midpoint := len(values) / 2

	if len(values)%2 == 1 {
		return values[midpoint], nil
	}

	sum := new(big.Float).Add(values[midpoint-1], values[midpoint])
	return sum.Quo(sum, big.NewFloat(2)), nil
}
