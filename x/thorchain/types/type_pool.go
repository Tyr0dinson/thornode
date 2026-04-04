package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// Valid is to check whether the pool status is valid or not
func (x PoolStatus) Valid() error {
	if _, ok := PoolStatus_value[x.String()]; !ok {
		return errors.New("invalid pool status")
	}
	return nil
}

// MarshalJSON marshal PoolStatus to JSON in string form
func (x PoolStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(x.String())
}

// UnmarshalJSON convert string form back to PoolStatus
func (x *PoolStatus) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	*x = GetPoolStatus(s)
	return nil
}

// GetPoolStatus from string
func GetPoolStatus(ps string) PoolStatus {
	if val, ok := PoolStatus_value[ps]; ok {
		return PoolStatus(val)
	}
	return PoolStatus_Suspended
}

// Pools represent a list of pools
type Pools []Pool

// NewPool Returns a new Pool
func NewPool() Pool {
	return Pool{
		BalanceRune:         cosmos.ZeroUint(),
		BalanceAsset:        cosmos.ZeroUint(),
		SynthUnits:          cosmos.ZeroUint(),
		LPUnits:             cosmos.ZeroUint(),
		LegacyBalanceRune:   cosmos.ZeroUint(),
		LegacyBalanceAsset:  cosmos.ZeroUint(),
		LegacyLpUnits:       cosmos.ZeroUint(),
		PlainBalanceRune:    cosmos.ZeroUint(),
		PlainBalanceAsset:   cosmos.ZeroUint(),
		PlainLpUnits:        cosmos.ZeroUint(),
		PendingInboundRune:  cosmos.ZeroUint(),
		PendingInboundAsset: cosmos.ZeroUint(),
		Status:              PoolStatus_Available,
	}
}

// Valid check whether the pool is valid or not, if asset is empty then it is not valid
func (m Pool) Valid() error {
	if m.IsEmpty() {
		return errors.New("pool asset cannot be empty")
	}
	return nil
}

func (m Pool) UsesLiquidityBuckets() bool {
	return !m.Asset.IsSyntheticAsset() &&
		!m.Asset.IsDerivedAsset() &&
		(!normalizeUint(m.SynthUnits).IsZero() || m.HasLiquidityBuckets())
}

func normalizeUint(u cosmos.Uint) cosmos.Uint {
	if u.IsNil() {
		return cosmos.ZeroUint()
	}
	return u
}

func (m Pool) hasPlainLiquidityBucket() bool {
	return !normalizeUint(m.PlainBalanceRune).IsZero() ||
		!normalizeUint(m.PlainBalanceAsset).IsZero() ||
		!normalizeUint(m.PlainLpUnits).IsZero()
}

func (m Pool) HasLiquidityBuckets() bool {
	return !normalizeUint(m.LegacyBalanceRune).IsZero() ||
		!normalizeUint(m.LegacyBalanceAsset).IsZero() ||
		!normalizeUint(m.LegacyLpUnits).IsZero() ||
		m.hasPlainLiquidityBucket()
}

func (m *Pool) EnsureLiquidityBuckets() {
	if !m.UsesLiquidityBuckets() {
		return
	}
	m.LegacyBalanceRune = normalizeUint(m.LegacyBalanceRune)
	m.LegacyBalanceAsset = normalizeUint(m.LegacyBalanceAsset)
	m.LegacyLpUnits = normalizeUint(m.LegacyLpUnits)
	m.PlainBalanceRune = normalizeUint(m.PlainBalanceRune)
	m.PlainBalanceAsset = normalizeUint(m.PlainBalanceAsset)
	m.PlainLpUnits = normalizeUint(m.PlainLpUnits)
	if !m.HasLiquidityBuckets() {
		m.LegacyBalanceRune = m.BalanceRune
		m.LegacyBalanceAsset = m.BalanceAsset
		m.LegacyLpUnits = m.LPUnits
	} else if !m.hasPlainLiquidityBucket() &&
		(!m.LegacyBalanceRune.Equal(m.BalanceRune) ||
			!m.LegacyBalanceAsset.Equal(m.BalanceAsset) ||
			!m.LegacyLpUnits.Equal(m.LPUnits)) {
		m.LegacyBalanceRune = m.BalanceRune
		m.LegacyBalanceAsset = m.BalanceAsset
		m.LegacyLpUnits = m.LPUnits
	}
	m.SyncLiquidityTotals()
}

func (m *Pool) SyncLiquidityTotals() {
	if !m.UsesLiquidityBuckets() {
		return
	}
	m.BalanceRune = normalizeUint(m.LegacyBalanceRune).Add(normalizeUint(m.PlainBalanceRune))
	m.BalanceAsset = normalizeUint(m.LegacyBalanceAsset).Add(normalizeUint(m.PlainBalanceAsset))
	m.LPUnits = normalizeUint(m.LegacyLpUnits).Add(normalizeUint(m.PlainLpUnits))
}

func (m Pool) GetLegacyBalanceRune() cosmos.Uint {
	if !m.UsesLiquidityBuckets() || !m.HasLiquidityBuckets() {
		return m.BalanceRune
	}
	return normalizeUint(m.LegacyBalanceRune)
}

func (m Pool) GetLegacyBalanceAsset() cosmos.Uint {
	if !m.UsesLiquidityBuckets() || !m.HasLiquidityBuckets() {
		return m.BalanceAsset
	}
	return normalizeUint(m.LegacyBalanceAsset)
}

func (m Pool) GetPlainBalanceRune() cosmos.Uint {
	if !m.UsesLiquidityBuckets() || !m.HasLiquidityBuckets() {
		return cosmos.ZeroUint()
	}
	return normalizeUint(m.PlainBalanceRune)
}

func (m Pool) GetPlainBalanceAsset() cosmos.Uint {
	if !m.UsesLiquidityBuckets() || !m.HasLiquidityBuckets() {
		return cosmos.ZeroUint()
	}
	return normalizeUint(m.PlainBalanceAsset)
}

func (m Pool) GetLegacyLPUnits() cosmos.Uint {
	if !m.UsesLiquidityBuckets() || !m.HasLiquidityBuckets() {
		return m.LPUnits
	}
	return normalizeUint(m.LegacyLpUnits)
}

func (m Pool) GetPlainLPUnits() cosmos.Uint {
	if !m.UsesLiquidityBuckets() || !m.HasLiquidityBuckets() {
		return cosmos.ZeroUint()
	}
	return normalizeUint(m.PlainLpUnits)
}

func (m Pool) GetLegacyPoolUnits() cosmos.Uint {
	return m.GetLegacyLPUnits().Add(m.SynthUnits)
}

func (m Pool) GetPlainPoolUnits() cosmos.Uint {
	return m.GetPlainLPUnits()
}

func (m Pool) GetSynthBackingAssetBalance() cosmos.Uint {
	return m.GetLegacyBalanceAsset()
}

func (m Pool) GetSynthUtilizationBasisPoints() cosmos.Uint {
	return common.GetUncappedShare(m.SynthUnits, m.GetLegacyPoolUnits(), cosmos.NewUint(10_000))
}

func (m *Pool) GetPoolUnits() cosmos.Uint {
	if m.UsesLiquidityBuckets() {
		return m.GetLegacyPoolUnits().Add(m.GetPlainPoolUnits())
	}
	return m.LPUnits.Add(m.SynthUnits)
}

func (m *Pool) CalcUnits(s cosmos.Uint) cosmos.Uint {
	m.EnsureLiquidityBuckets()

	// Calculate synth units
	// (L*S)/(2*A-S)
	// S := k.GetTotalSupply(ctx, p.Asset.GetSyntheticAsset())
	if m.BalanceAsset.IsZero() || m.Asset.IsSyntheticAsset() {
		m.SynthUnits = cosmos.ZeroUint()
	} else {
		numerator := m.GetLegacyLPUnits().Mul(s)
		denominator := common.SafeSub(m.GetLegacyBalanceAsset().MulUint64(2), s)
		if denominator.IsZero() {
			denominator = cosmos.OneUint()
		}
		m.SynthUnits = numerator.Quo(denominator)
	}
	if m.UsesLiquidityBuckets() {
		m.EnsureLiquidityBuckets()
	}
	return m.GetPoolUnits()
}

// IsAvailable check whether the pool is in Available status
func (m Pool) IsAvailable() bool {
	return m.Status == PoolStatus_Available
}

// IsStaged check whether the pool is in Staged status
func (m Pool) IsStaged() bool {
	return m.Status == PoolStatus_Staged
}

// IsEmpty will return true when the asset is empty
func (m Pool) IsEmpty() bool {
	return m.Asset.IsEmpty()
}

// String implement fmt.Stringer
func (m Pool) String() string {
	sb := strings.Builder{}
	sb.WriteString(fmt.Sprintln("rune-balance: " + m.BalanceRune.String()))
	sb.WriteString(fmt.Sprintln("asset-balance: " + m.BalanceAsset.String()))
	sb.WriteString(fmt.Sprintln("asset: " + m.Asset.String()))
	sb.WriteString(fmt.Sprintln("synth-units: " + m.SynthUnits.String()))
	sb.WriteString(fmt.Sprintln("lp-units: " + m.LPUnits.String()))
	sb.WriteString(fmt.Sprintln("legacy-rune-balance: " + m.LegacyBalanceRune.String()))
	sb.WriteString(fmt.Sprintln("legacy-asset-balance: " + m.LegacyBalanceAsset.String()))
	sb.WriteString(fmt.Sprintln("legacy-lp-units: " + m.LegacyLpUnits.String()))
	sb.WriteString(fmt.Sprintln("plain-rune-balance: " + m.PlainBalanceRune.String()))
	sb.WriteString(fmt.Sprintln("plain-asset-balance: " + m.PlainBalanceAsset.String()))
	sb.WriteString(fmt.Sprintln("plain-lp-units: " + m.PlainLpUnits.String()))
	sb.WriteString(fmt.Sprintln("pending-inbound-rune: " + m.PendingInboundRune.String()))
	sb.WriteString(fmt.Sprintln("pending-inbound-asset: " + m.PendingInboundAsset.String()))
	sb.WriteString(fmt.Sprintln("status: " + m.Status.String()))
	sb.WriteString(fmt.Sprintln("decimals:" + strconv.FormatInt(m.Decimals, 10)))
	return sb.String()
}

// EnsureValidPoolStatus make sure the pool is in a valid status otherwise it return an error
func (m Pool) EnsureValidPoolStatus(msg cosmos.Msg) error {
	switch m.Status {
	case PoolStatus_Available:
		return nil
	case PoolStatus_Staged:
		_, ok := msg.(*MsgSwap)
		if ok {
			return errors.New("pool is in staged status, can't swap")
		}
		return nil
	case PoolStatus_Suspended:
		return errors.New("pool suspended")
	default:
		return fmt.Errorf("unknown pool status,%s", m.Status)
	}
}

// AssetValueInRune convert a specific amount of asset amt into its rune value
func (m Pool) AssetValueInRune(amt cosmos.Uint) cosmos.Uint {
	if m.BalanceRune.IsZero() || m.BalanceAsset.IsZero() {
		return cosmos.ZeroUint()
	}
	return common.GetUncappedShare(m.BalanceRune, m.BalanceAsset, amt)
}

// RuneReimbursementForAssetWithdrawal returns the equivalent amount of rune for a
// given amount of asset withdrawn from the pool, taking slip into account. When
// this amount is added to the pool, the constant product of depths rule is
// preserved.
func (m Pool) RuneReimbursementForAssetWithdrawal(amt cosmos.Uint) cosmos.Uint {
	if m.BalanceRune.IsZero() || m.BalanceAsset.IsZero() {
		return cosmos.ZeroUint()
	}
	denom := common.SafeSub(m.BalanceAsset, amt)
	if denom.IsZero() {
		// With slip, as the amount approaches the entire asset balance of the pool
		// the equivalent rune value approaches infinity. Return 0 in the limiting
		// case.
		return cosmos.ZeroUint()
	}
	return common.GetUncappedShare(m.BalanceRune, denom, amt)
}

// RuneValueInAsset convert a specific amount of rune amt into its asset value
func (m Pool) RuneValueInAsset(amt cosmos.Uint) cosmos.Uint {
	if m.BalanceRune.IsZero() || m.BalanceAsset.IsZero() {
		return cosmos.ZeroUint()
	}
	assetAmt := common.GetUncappedShare(m.BalanceAsset, m.BalanceRune, amt)
	return cosmos.RoundToDecimal(assetAmt, m.Decimals)
}

func (m Pools) Get(asset common.Asset) (Pool, bool) {
	for _, p := range m {
		if p.Asset.Equals(asset) {
			return p, true
		}
	}
	return NewPool(), false
}

func (m Pools) Set(pool Pool) Pools {
	for i, p := range m {
		if p.Asset.Equals(pool.Asset) {
			m[i] = pool
		}
	}
	m = append(m, pool)
	return m
}

// RuneDisbursementForAssetAdd returns the equivalent amount of rune for a
// given amount of asset added to the pool, taking slip into account. When this
// amount is withdrawn from the pool, the constant product of depths rule is
// preserved.
func (m Pool) RuneDisbursementForAssetAdd(amt cosmos.Uint) cosmos.Uint {
	if m.BalanceRune.IsZero() || m.BalanceAsset.IsZero() {
		return cosmos.ZeroUint()
	}
	denom := m.BalanceAsset.Add(amt)
	return common.GetUncappedShare(m.BalanceRune, denom, amt)
}

// AssetDisbursementForRuneAdd returns the equivalent amount of asset for a
// given amount of rune added to the pool, taking slip into account. When this
// amount is withdrawn from the pool, the constant product of depths rule is
// preserved.
func (m Pool) AssetDisbursementForRuneAdd(amt cosmos.Uint) cosmos.Uint {
	if m.BalanceRune.IsZero() || m.BalanceAsset.IsZero() {
		return cosmos.ZeroUint()
	}
	denom := m.BalanceRune.Add(amt)
	outAmt := common.GetUncappedShare(m.BalanceAsset, denom, amt)
	return cosmos.RoundToDecimal(outAmt, m.Decimals)
}

func (m Pool) GetLUVI() cosmos.Uint {
	bigInt := &big.Int{}
	balRune := m.BalanceRune.MulUint64(1e12).BigInt()
	balAsset := m.BalanceAsset.MulUint64(1e12).BigInt()
	num := bigInt.Mul(balRune, balAsset)
	num = bigInt.Sqrt(num)
	denom := m.GetPoolUnits().BigInt()
	if len(denom.Bits()) == 0 {
		return cosmos.ZeroUint()
	}
	result := bigInt.Quo(num, denom)
	return cosmos.NewUintFromBigInt(result)
}
