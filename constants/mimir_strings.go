package constants

// All strings used in Mimir keys should be recorded here and referred to from elsewhere,
// except for strings referring to arbitrary Assets/Chains.
// Each string should clearly indicate its usage for the final Mimir key (key, template, reference)
// and no Mimir key should require the combination of more than two strings.
const (
	// TODO: Perhaps complete this list with May 2024's pending !3561.
	MimirKeyPendulumIgnoreL1Assets          = "PendulumIgnoreL1Assets"
	MimirKeyPendulumIgnoreEffectiveSecurity = "PendulumIgnoreEffectiveSecurity"

	MimirTemplateConfMultiplierBasisPoints = "ConfMultiplierBasisPoints-%s" // Use with Chain
	MimirTemplateMaxConfirmations          = "MaxConfirmations-%s"          // Use with Chain
	MimirTemplateSwapSlipBasisPointsMin    = "SwapSlipBasisPointsMin-%s"    // Use with MimirRef

	MimirRefL1           = "L1"           // Use with SwapSlipBasisPoints
	MimirRefSynth        = "Synth"        // Use with SwapSlipBasisPoints
	MimirRefTradeAccount = "TradeAccount" // Use with SwapSlipBasisPoints
)
