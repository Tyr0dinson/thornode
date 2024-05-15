package constants

// All strings used in Mimir keys should be recorded here and referred to from elsewhere,
// except for hyphen joiners and strings referring to arbitrary Assets/Chains.
// Each string should clearly indicate its usage for the final Mimir key (key, template, prefix, suffix)
// and no Mimir key should require the combination of more than two strings.
const (
	MimirPrefixConfMultiplierBasisPoints = "ConfMultiplierBasisPoints"
	MimirPrefixMaxConfirmations          = "MaxConfirmations"
	MimirPrefixSwapSlipBasisPointsMin    = "SwapSlipBasisPointsMin"

	MimirSuffixL1           = "L1"
	MimirSuffixSynth        = "Synth"
	MimirSuffixTradeAccount = "TradeAccount"
)
