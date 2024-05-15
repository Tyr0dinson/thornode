package constants

// All strings used in Mimir keys should be recorded here and referred to from elsewhere,
// except for strings referring to arbitrary Assets/Chains.
// Each string should clearly indicate its usage for the final Mimir key (key, template, reference)
// and no Mimir key should require the combination of more than two strings.
const (
	MimirRegex = "^[a-zA-Z0-9-]+$"

	MimirKeyDollarsPerRune          = "DollarsPerRune"
	MimirKeyHaltChainGlobal         = "HaltChainGlobal"
	MimirKeyHaltChurning            = "HaltChurning"
	MimirKeyHaltSigningGlobal       = "HaltSigning" // Note this key string has no "Global" on the end.
	MimirKeyHaltTradingGlobal       = "HaltTrading" // Note this key string has no "Global" on the end.
	MimirKeyMaximumBondInRune       = "MaximumBondInRune"
	MimirKeyMaxUTXOsToSpend         = "MaxUTXOsToSpend"
	MimirKeyNodePauseChainGlobal    = "NodePauseChainGlobal"
	MimirKeyPauseLPGlobal           = "PauseLP"           // Note this key string has no "Global" on the end.
	MimirKeyStopSolvencyCheckGlobal = "StopSolvencyCheck" // Note this key string has no "Global" on the end.
	MimirKeyTHORNames               = "THORNames"

	MimirTemplateConfMultiplierBasisPoints = "ConfMultiplierBasisPoints-%s" // Use with Chain
	MimirTemplateHaltChain                 = "Halt%sChain"                  // Use with Chain
	MimirTemplateHaltSigning               = "HaltSigning%s"                // Use with Chain
	MimirTemplateHaltTrading               = "Halt%sTrading"                // Use with Chain
	MimirTemplateLending                   = "Lending-%s"                   // Use with Asset MimirString
	MimirTemplateMaxConfirmations          = "MaxConfirmations-%s"          // Use with Chain
	MimirTemplatePauseAsymWithdrawal       = "PauseAsymWithdrawal-%s"       // Use with Chain
	MimirTemplatePauseLP                   = "PauseLP%s"                    // Use with Chain
	MimirTemplatePOL                       = "POL-%s"                       // Use with Asset MimirString
	MimirTemplateRagnarok                  = "Ragnarok-%s"                  // Use with Asset MimirString
	MimirTemplateSolvencyHaltChain         = "SolvencyHalt%sChain"          // Use with Chain
	MimirTemplateStopSolvencyCheck         = "StopSolvencyCheck%s"          // Use with Chain
	MimirTemplateTorAnchor                 = "TorAnchor-%s"                 // Use with Asset MimirString
)
