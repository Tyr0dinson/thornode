package types

import (
	"fmt"
	"strings"

	"cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
)

const MaxDynamicFeeHistory = 30

// SignalScale is the fixed-point scale of the normalized fee signal
// (fees_tor / pool_volume_tor). The ratio is on the order of 1e-5, so 1e8
// would truncate it to a few hundred units and quantize the gradient; 1e12
// keeps the precision, and it is free with Uint arithmetic.
var SignalScale = math.NewUint(1_000_000_000_000)

func NormalizePairAssets(assetA, assetB string) (string, string) {
	if assetA > assetB {
		return assetB, assetA
	}
	return assetA, assetB
}

func (m *DynamicFeeRecord) Key() string {
	return fmt.Sprintf("%s|%s|%s", strings.ToUpper(m.Thorname), m.PairAssetA, m.PairAssetB)
}

func (m *DynamicFeeRecord) AppendHistory(rec DynamicFeeEpochRecord) {
	m.History = append(m.History, rec)
	if len(m.History) > MaxDynamicFeeHistory {
		m.History = m.History[len(m.History)-MaxDynamicFeeHistory:]
	}
}

func (m *DynamicFeeRecord) LatestHistory() (DynamicFeeEpochRecord, bool) {
	if len(m.History) == 0 {
		return DynamicFeeEpochRecord{}, false
	}
	return m.History[len(m.History)-1], true
}

func (m *DynamicFeeAccumulator) Key() string {
	return fmt.Sprintf("%s|%s|%s", strings.ToUpper(m.Thorname), m.PairAssetA, m.PairAssetB)
}

func NewDynamicFeeAccumulator(thorname, pairAssetA, pairAssetB string) DynamicFeeAccumulator {
	return DynamicFeeAccumulator{
		Thorname:      thorname,
		PairAssetA:    pairAssetA,
		PairAssetB:    pairAssetB,
		VolumeTor:     math.ZeroUint(),
		FeesTor:       math.ZeroUint(),
		PoolVolumeTor: math.ZeroUint(),
	}
}

func (m *DynamicFeePoolAccumulator) Key() string {
	return fmt.Sprintf("%s|%s", m.PairAssetA, m.PairAssetB)
}

func NewDynamicFeePoolAccumulator(pairAssetA, pairAssetB string) DynamicFeePoolAccumulator {
	return DynamicFeePoolAccumulator{
		PairAssetA: pairAssetA,
		PairAssetB: pairAssetB,
		VolumeTor:  math.ZeroUint(),
	}
}

// NormalizedSignal returns the epoch's fee revenue expressed as a share of the
// pool's total volume for the same span, scaled by SignalScale. Normalizing
// against pool volume (rather than the affiliate's own volume, which is
// degenerate at f/10^4 while the floor binds) cancels market-wide activity out
// of the gradient. Returns (0, false) when the pool volume is unknown, which is
// the case for history entries written before pool volume was tracked.
func (m *DynamicFeeEpochRecord) NormalizedSignal() (math.Uint, bool) {
	if m.PoolVolumeTor.IsNil() || m.PoolVolumeTor.IsZero() {
		return math.ZeroUint(), false
	}
	return common.GetSafeShare(m.FeesTor, m.PoolVolumeTor, SignalScale), true
}
