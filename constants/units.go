package constants

import (
	"math"
	"math/bits"
	"strings"
)

// Unit says how a block-denominated setting follows the BLOCKTIMEMS mimir.
// Settings are stored and voted in reference blocks of the network's default
// block time (ThorchainBlockTime) and converted to real blocks when read, so
// they keep their wall-clock meaning at any block time. With r =
// ThorchainBlockTime / BLOCKTIMEMS:
type Unit uint8

const (
	// UnitCount is unchanged by block time: counts, amounts, basis points,
	// flags, and per-block caps where more throughput is the point.
	UnitCount Unit = iota + 1
	// UnitDuration is a span of blocks, read as ceil(v·r); 0 and negative
	// values (off / unset) pass through.
	UnitDuration
	// UnitPerBlockRate is an amount per block, read as v/r rounded half up;
	// 0 and negative values pass through.
	UnitPerBlockRate
	// UnitHeight is an absolute block height and is never scaled.
	UnitHeight
	// UnitWallClock is already a wall-clock quantity (e.g. milliseconds) and is
	// never scaled.
	UnitWallClock
)

func (u Unit) String() string {
	switch u {
	case UnitCount:
		return "count"
	case UnitDuration:
		return "duration"
	case UnitPerBlockRate:
		return "per-block-rate"
	case UnitHeight:
		return "height"
	case UnitWallClock:
		return "wall-clock"
	default:
		return "untagged"
	}
}

// Scales reports whether values of this unit change with block time.
func (u Unit) Scales() bool {
	return u == UnitDuration || u == UnitPerBlockRate
}

// Unit returns the constant's unit tag, or 0 if it is untagged (a unit test
// fails for every untagged ConstantName).
func (cn ConstantName) Unit() Unit {
	return constantUnits[cn]
}

// MimirKeyUnit returns the unit tag of a mimir key, matched case-insensitively
// against the ConstantNames and the block-count mimirs that aren't constants.
// Keys built from templates (per chain or asset) are not matched and return 0.
func MimirKeyUnit(key string) Unit {
	return mimirKeyUnits[strings.ToUpper(key)]
}

// ScaleDuration converts a duration of refBlocks reference blocks (refMs each)
// to blocks of blockMs: ceil(refBlocks·refMs/blockMs), saturating at
// MaxInt64. Values <= 0 and non-positive block times pass through.
func ScaleDuration(refBlocks, refMs, blockMs int64) int64 {
	if refBlocks <= 0 || refMs <= 0 || blockMs <= 0 || refMs == blockMs {
		return refBlocks
	}
	hi, lo := bits.Mul64(uint64(refBlocks), uint64(refMs))
	if hi >= uint64(blockMs) {
		return math.MaxInt64
	}
	q, rem := bits.Div64(hi, lo, uint64(blockMs))
	if rem > 0 {
		q++
	}
	if q > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(q)
}

// ScalePerBlockRate converts an amount per reference block (refMs) to an
// amount per block of blockMs: perRefBlock·blockMs/refMs, rounded half up and
// saturating at MaxInt64. Values <= 0 and non-positive block times pass
// through.
func ScalePerBlockRate(perRefBlock, refMs, blockMs int64) int64 {
	if perRefBlock <= 0 || refMs <= 0 || blockMs <= 0 || refMs == blockMs {
		return perRefBlock
	}
	hi, lo := bits.Mul64(uint64(perRefBlock), uint64(blockMs))
	if hi >= uint64(refMs) {
		return math.MaxInt64
	}
	q, rem := bits.Div64(hi, lo, uint64(refMs))
	if rem >= uint64(refMs)-rem { // rem >= refMs/2, without overflow
		q++
	}
	if q > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(q)
}

// Scale converts v of unit u from reference blocks (refMs) to blocks of
// blockMs; units that don't scale return v unchanged.
func (u Unit) Scale(v, refMs, blockMs int64) int64 {
	switch u {
	case UnitDuration:
		return ScaleDuration(v, refMs, blockMs)
	case UnitPerBlockRate:
		return ScalePerBlockRate(v, refMs, blockMs)
	default:
		return v
	}
}

// constantUnits tags every ConstantName. Durations and rates are scaled with
// BLOCKTIMEMS wherever they are read; see docs/mimir.md.
var constantUnits = map[ConstantName]Unit{
	EmissionCurve:                       UnitCount,
	MaxRuneSupply:                       UnitCount,
	BlocksPerYear:                       UnitDuration,
	OutboundTransactionFee:              UnitCount,
	NativeTransactionFee:                UnitCount,
	PoolCycle:                           UnitDuration,
	MinRunePoolDepth:                    UnitCount,
	MaxAvailablePools:                   UnitCount,
	StagedPoolCost:                      UnitCount, // charged once per PoolCycle, which scales
	PendingLiquidityAgeLimit:            UnitDuration,
	MinimumNodesForBFT:                  UnitCount,
	DesiredValidatorSet:                 UnitCount,
	AsgardSize:                          UnitCount,
	DerivedDepthBasisPts:                UnitCount,
	DerivedMinDepth:                     UnitCount,
	MaxAnchorSlip:                       UnitCount,
	MaxAnchorBlocks:                     UnitDuration,
	DynamicMaxAnchorSlipBlocks:          UnitDuration,
	DynamicMaxAnchorTarget:              UnitCount,
	DynamicMaxAnchorCalcInterval:        UnitDuration,
	ChurnInterval:                       UnitDuration,
	ChurnRetryInterval:                  UnitDuration,
	MissingBlockChurnOut:                UnitDuration, // missed blocks between churns: a share of the churn interval
	MaxMissingBlockChurnOut:             UnitCount,    // a node count
	MaxTrackMissingBlock:                UnitDuration,
	BadValidatorRedline:                 UnitCount,
	LackOfObservationPenalty:            UnitCount, // charged per observation, not per block
	SigningTransactionPeriod:            UnitDuration,
	DoubleSignMaxAge:                    UnitDuration,
	PauseBond:                           UnitHeight,
	PauseUnbond:                         UnitHeight,
	MinimumBondInRune:                   UnitCount,
	FundMigrationInterval:               UnitDuration,
	MaxOutboundAttempts:                 UnitCount,
	SlashPenalty:                        UnitCount,
	PauseOnSlashThreshold:               UnitCount,
	FailKeygenSlashPoints:               UnitDuration, // "720 blocks of slash", an hour of missed rewards
	FailKeysignSlashPoints:              UnitCount,
	LiquidityLockUpBlocks:               UnitDuration,
	ObserveSlashPoints:                  UnitCount, // charged per observation, not per block
	DoubleBlockSignSlashPoints:          UnitCount,
	MissBlockSignSlashPoints:            UnitCount, // accrues per block; integer points can't be divided (policy item)
	ObservationDelayFlexibility:         UnitDuration,
	JailTimeKeygen:                      UnitDuration,
	JailTimeKeygenBase:                  UnitDuration, // per-blame base, multiplied by the blame count
	JailTimeKeysign:                     UnitDuration,
	MaxKeygenBlameMultiplier:            UnitCount,
	NodePauseChainBlocks:                UnitDuration,
	EnableDerivedAssets:                 UnitCount,
	MinSwapsPerBlock:                    UnitCount,
	MaxSwapsPerBlock:                    UnitCount,
	EnableOrderBooks:                    UnitCount,
	EnableAdvSwapQueue:                  UnitCount,
	AdvSwapQueueRapidSwapMax:            UnitCount,
	MaxSynthPerPoolDepth:                UnitCount,
	MaxSynthsForSaversYield:             UnitCount,
	VirtualMultSynths:                   UnitCount,
	VirtualMultSynthsBasisPoints:        UnitCount,
	MinSlashPointsForBadValidator:       UnitCount, // open question: MissBlockSignSlashPoints accrue per block
	MaxBondProviders:                    UnitCount,
	MinTxOutVolumeThreshold:             UnitPerBlockRate,
	TxOutDelayRate:                      UnitPerBlockRate,
	TxOutDelayMax:                       UnitDuration,
	MaxTxOutOffset:                      UnitDuration,
	TNSRegisterFee:                      UnitCount,
	TNSFeeOnSale:                        UnitCount,
	TNSFeePerBlock:                      UnitPerBlockRate,
	StreamingSwapPause:                  UnitHeight,
	StreamingSwapMinBPFee:               UnitCount,
	StreamingSwapMaxLength:              UnitDuration,
	StreamingSwapMaxLengthNative:        UnitDuration,
	StreamingLimitSwapMaxAge:            UnitDuration,
	MinCR:                               UnitCount,
	MaxCR:                               UnitCount,
	LendingLever:                        UnitCount,
	PermittedSolvencyGap:                UnitCount,
	PermittedSolvencyGapUSD:             UnitCount,
	NodeOperatorFee:                     UnitCount,
	ValidatorMaxRewardRatio:             UnitCount,
	MaxNodeToChurnOutForLowVersion:      UnitCount,
	ChurnOutForLowVersionBlocks:         UnitDuration,
	POLMaxNetworkDeposit:                UnitCount,
	POLMaxPoolMovement:                  UnitPerBlockRate, // POLCycle runs every block
	POLTargetSynthPerPoolDepth:          UnitCount,
	POLBuffer:                           UnitCount,
	RagnarokProcessNumOfLPPerIteration:  UnitCount,
	SynthYieldBasisPoints:               UnitCount,
	SynthYieldCycle:                     UnitDuration,
	MinimumL1OutboundFeeUSD:             UnitCount,
	MinimumPoolLiquidityFee:             UnitCount, // summed over a PoolCycle, which scales
	ChurnMigrateRounds:                  UnitCount,
	AllowWideBlame:                      UnitCount,
	MaxAffiliateFeeBasisPoints:          UnitCount,
	TargetOutboundFeeSurplusRune:        UnitCount,
	MaxOutboundFeeMultiplierBasisPoints: UnitCount,
	MinOutboundFeeMultiplierBasisPoints: UnitCount,
	NativeOutboundFeeUSD:                UnitCount,
	NativeTransactionFeeUSD:             UnitCount,
	TNSRegisterFeeUSD:                   UnitCount,
	TNSFeePerBlockUSD:                   UnitPerBlockRate,
	EnableUSDFees:                       UnitCount,
	PreferredAssetOutboundFeeMultiplier: UnitCount,
	FeeUSDRoundSignificantDigits:        UnitCount,
	MigrationVaultSecurityBps:           UnitCount,
	CloutReset:                          UnitDuration,
	CloutLimit:                          UnitCount,
	KeygenRetryInterval:                 UnitDuration,
	SaversStreamingSwapsInterval:        UnitDuration,
	RescheduleCoalesceBlocks:            UnitDuration,
	L1SlipMinBps:                        UnitCount,
	SynthSlipMinBps:                     UnitCount,
	TradeAccountsSlipMinBps:             UnitCount,
	DerivedSlipMinBps:                   UnitCount,
	SlipMinBpsMax:                       UnitCount,
	TradeAccountsEnabled:                UnitCount,
	TradeAccountsDepositEnabled:         UnitCount,
	SecuredAssetSlipMinBps:              UnitCount,
	StableSlipMinBps:                    UnitCount,
	WasmArbSlipMinBps:                   UnitCount,
	EVMDisableContractWhitelist:         UnitCount,
	OperationalVotesMin:                 UnitCount,
	RUNEPoolEnabled:                     UnitCount,
	RUNEPoolDepositMaturityBlocks:       UnitDuration,
	RUNEPoolMaxReserveBackstop:          UnitCount,
	SaversEjectInterval:                 UnitDuration,
	SystemIncomeBurnRateBps:             UnitCount,
	DevFundSystemIncomeBps:              UnitCount,
	DevFundAddress:                      UnitCount,
	PendulumAssetsBasisPoints:           UnitCount,
	PendulumUseEffectiveSecurity:        UnitCount,
	PendulumUseVaultAssets:              UnitCount,
	TVLCapBasisPoints:                   UnitCount,
	MultipleAffiliatesMaxCount:          UnitCount,
	BondSlashBan:                        UnitCount,
	BankSendEnabled:                     UnitCount,
	RUNEPoolHaltDeposit:                 UnitHeight,
	RUNEPoolHaltWithdraw:                UnitHeight,
	MinRuneForTCYStakeDistribution:      UnitCount,
	MinTCYForTCYStakeDistribution:       UnitCount,
	TCYStakeSystemIncomeBps:             UnitCount,
	TCYClaimingSwapHalt:                 UnitHeight,
	TCYStakeDistributionHalt:            UnitHeight,
	TCYStakingHalt:                      UnitHeight,
	TCYUnstakingHalt:                    UnitHeight,
	TCYClaimingHalt:                     UnitHeight,
	HaltRebond:                          UnitHeight,
	HaltOperatorRotate:                  UnitHeight,
	HaltMemoless:                        UnitHeight,
	RequiredPriceFeeds:                  UnitCount,
	HaltOracle:                          UnitHeight,
	OracleUpdateInterval:                UnitWallClock, // milliseconds, not blocks
	ReserveMaxCap:                       UnitCount,
	MarketingFundSystemIncomeBps:        UnitCount,
	MarketingFundAddress:                UnitCount,
	OverSolvencyToTreasuryBps:           UnitCount,
	OverSolvencyCheckInterval:           UnitDuration,
	OverSolvencyAddress:                 UnitCount,
	MaxDepositTxIDRetries:               UnitCount,
	MaxRetiredVaultRecoveryAttempts:     UnitCount,
	ModifyLimitSwapMaxIterations:        UnitCount,
	POLReserveSystemIncomeBps:           UnitCount,
	POLReserveMaxDeployment:             UnitPerBlockRate, // POLReserveCycle runs every block
	StableReserveEnabled:                UnitCount,
	StableReserveDepegToleranceBps:      UnitCount,
	StableReservePOLShareBps:            UnitCount,
	StableReserveWithdrawalRequestMax:   UnitCount,
	StableReserveOraclePricing:          UnitCount,
	WasmArbContract:                     UnitCount,
	MaxAbsentKeygenReporters:            UnitCount,
	ArtificialRagnarokBlockHeight:       UnitHeight,
	BondLockupPeriod:                    UnitDuration,
	BurnSynths:                          UnitCount,
	DefaultPoolStatus:                   UnitCount,
	EnableMemolessOutbound:              UnitCount,
	ManualSwapsToSynthDisabled:          UnitCount,
	MaximumLiquidityRune:                UnitCount,
	MintSynths:                          UnitCount,
	NumberOfNewNodesPerChurn:            UnitCount,
	SignerConcurrency:                   UnitCount,
	MemolessTxnTTL:                      UnitDuration,
	MemolessTxnRefCount:                 UnitCount,
	MemolessTxnCost:                     UnitCount,
	MemolessTxnMaxUse:                   UnitCount,
	MemolessTxnCostSlopeBPS:             UnitCount,
	StrictBondLiquidityRatio:            UnitCount,
	SwapOutDexAggregationDisabled:       UnitCount,
	P2PGateDisabled:                     UnitCount,
	L1DynamicFeeEnabled:                 UnitCount,
	L1DynamicFeeEpochBlocks:             UnitDuration,
	L1DynamicFeeFloorBPS:                UnitCount,
	L1DynamicFeeCeilingBPS:              UnitCount,
	L1DynamicFeeStepBPS:                 UnitCount,
	L1DynamicFeeDeadbandBPS:             UnitCount,
	L1DynamicFeeWindowEpochs:            UnitCount, // a count of epochs, whose length scales
	VaultDeficitGasHaltMaxGasMultiplier: UnitCount,
	BondYieldEnabled:                    UnitCount,
	MaxMissingObservationChurnOut:       UnitCount,
	MissingObservationChurnOutBps:       UnitCount,
	MissingObservationGuardBps:          UnitCount,
	MissingObservationHalfLife:          UnitDuration,
	MissingObservationMinSamples:        UnitCount,
	MissingObservationGuardMaxBlocks:    UnitDuration,
	BlockTimeMs:                         UnitWallClock, // milliseconds; the scale itself
}

// mimirOnlyUnits tags block-denominated mimir keys that aren't ConstantNames.
var mimirOnlyUnits = map[string]Unit{
	MimirKeySignatureRefreshBlocks: UnitDuration, // bifrost only
	MimirKeyScheduledMigration:     UnitHeight,
	MimirKeyHaltMonovaultKeygen:    UnitHeight,
	"HaltChurning":                 UnitHeight,
	"HaltChainGlobal":              UnitHeight,
	"NodePauseChainGlobal":         UnitHeight,
	MimirKeyHaltTradingGlobal:      UnitHeight,
	"HaltSigning":                  UnitHeight,
}

var mimirKeyUnits = func() map[string]Unit {
	m := make(map[string]Unit, len(constantUnits)+len(mimirOnlyUnits))
	// analyze-ignore(map-iteration)
	for name, u := range constantUnits {
		m[strings.ToUpper(name.String())] = u
	}
	// analyze-ignore(map-iteration)
	for key, u := range mimirOnlyUnits {
		m[strings.ToUpper(key)] = u
	}
	return m
}()
