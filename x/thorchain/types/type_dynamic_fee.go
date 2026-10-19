package types

import (
	"fmt"
	"sort"
	"strings"

	"cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
)

const MaxDynamicFeeHistory = 30

// Values of the DYNAMICFEE-WHITELIST-<THORNAME> mimir. Any other value marks
// the thorname for state reclamation.
const (
	// DynamicFeeStatePerPair charges a dynamic floor tuned per L1 pair.
	DynamicFeeStatePerPair int64 = 1
	// DynamicFeeStateGlobal charges one dynamic floor, tuned across all of the
	// thorname's L1 pairs together, on every L1 pair.
	DynamicFeeStateGlobal int64 = 2
	// DynamicFeeStatePerPairMonitor runs the per-pair controller without
	// charging its floor.
	DynamicFeeStatePerPairMonitor int64 = 3
	// DynamicFeeStateGlobalMonitor runs the global controller without charging
	// its floor.
	DynamicFeeStateGlobalMonitor int64 = 4
)

// DynamicFeeGlobalPair stands in for both pair assets on the record and
// accumulator of a thorname in global mode, so global state shares the
// per-pair key space and per-thorname cleanup.
const DynamicFeeGlobalPair = "*"

// IsValidDynamicFeeState reports whether a whitelist mimir value enrolls the
// thorname in dynamic fees (charging or monitoring).
func IsValidDynamicFeeState(state int64) bool {
	return state >= DynamicFeeStatePerPair && state <= DynamicFeeStateGlobalMonitor
}

// IsGlobalDynamicFeeState reports whether a whitelist mimir value selects the
// global (one record across all pairs) controller.
func IsGlobalDynamicFeeState(state int64) bool {
	return state == DynamicFeeStateGlobal || state == DynamicFeeStateGlobalMonitor
}

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
		Thorname:         thorname,
		PairAssetA:       pairAssetA,
		PairAssetB:       pairAssetB,
		VolumeTor:        math.ZeroUint(),
		FeesTor:          math.ZeroUint(),
		PoolVolumeTor:    math.ZeroUint(),
		OwnPoolVolumeTor: math.ZeroUint(),
	}
}

// AddPair records an L1 pair a global-mode accumulator was credited on,
// keeping the list sorted and unique so it is deterministic.
func (m *DynamicFeeAccumulator) AddPair(pairAssetA, pairAssetB string) {
	key := pairAssetA + "|" + pairAssetB
	i := sort.SearchStrings(m.Pairs, key)
	if i < len(m.Pairs) && m.Pairs[i] == key {
		return
	}
	m.Pairs = append(m.Pairs, "")
	copy(m.Pairs[i+1:], m.Pairs[i:])
	m.Pairs[i] = key
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

// NormalizedSignal returns the epoch's fee revenue as a share of the volume
// the rest of the market put through the same pools over the same span,
// scaled by SignalScale: fees_tor / (pool_volume_tor - own_pool_volume_tor).
//
// Excluding the thorname's own volume matters. When it is most of a pair's
// flow, dividing by the whole pool volume makes the ratio approach the fee
// rate itself, which always reads higher at a higher fee: the controller
// would price a dominant integrator up to the ceiling whatever its demand did.
// Other swappers' volume is an outside index of market activity that the
// thorname's own fee does not move.
//
// Returns (0, false) when the epoch cannot be normalized: no pool volume
// (history written before pool volume was tracked), or other flow below
// minOtherBps of the pool's volume, where there is no outside reference.
func (m *DynamicFeeEpochRecord) NormalizedSignal(minOtherBps uint64) (math.Uint, bool) {
	if m.PoolVolumeTor.IsNil() || m.PoolVolumeTor.IsZero() {
		return math.ZeroUint(), false
	}
	own := m.OwnPoolVolumeTor
	if own.IsNil() {
		own = math.ZeroUint()
	}
	if own.GTE(m.PoolVolumeTor) {
		return math.ZeroUint(), false
	}
	other := m.PoolVolumeTor.Sub(own)
	if other.MulUint64(10_000).LT(m.PoolVolumeTor.MulUint64(minOtherBps)) {
		return math.ZeroUint(), false
	}
	return common.GetSafeShare(m.FeesTor, other, SignalScale), true
}

// Add records one more measured epoch at the probe's bps.
func (m *DynamicFeeProbe) Add(signal math.Uint) {
	if m.SignalSum.IsNil() {
		m.SignalSum = math.ZeroUint()
	}
	m.SignalSum = m.SignalSum.Add(signal)
	m.Epochs++
}

// Mean returns the probe's average normalized fee signal per measured epoch.
func (m *DynamicFeeProbe) Mean() math.Uint {
	if m.Epochs == 0 || m.SignalSum.IsNil() {
		return math.ZeroUint()
	}
	return m.SignalSum.QuoUint64(m.Epochs)
}
