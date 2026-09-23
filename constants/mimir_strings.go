package constants

// All strings used in Mimir keys should be recorded here and referred to from elsewhere,
// except for strings referring to arbitrary Assets/Chains.
// Each string should clearly indicate its usage for the final Mimir key (key, template, reference)
// and no Mimir key should require the combination of more than two strings.
const (
	MimirKeySecuredAssetHaltGlobal    = "HaltSecuredGlobal"
	MimirKeyWasmPermissionless        = "WasmPermissionless"
	MimirKeyWasmHaltGlobal            = "HaltWasmGlobal"
	MimirKeyWasmMinGasPrice           = "WasmMinGasPrice"
	MimirKeyScheduledMigration        = "ScheduledMigration"
	MimirKeyDisableTSSSignerFiltering = "DisableTSSSignerFiltering"
	// MimirKeySignatureRefreshBlocks tunes the bifrost vault keep-alive self-sign
	// staleness threshold in THORChain blocks (read by bifrost only, never by
	// consensus code). Unset defaults to ~1 day of blocks; 0 disables the keep-alive.
	MimirKeySignatureRefreshBlocks = "SignatureRefreshBlocks"
	MimirKeyStableReserveEnabled   = "StableReserveEnabled"
	// MimirKeySOLDurableNonce gates bifrost provisioning of per-vault Solana
	// durable-nonce accounts (#661). Read by bifrost only (never by consensus code):
	// >0 enables provisioning of a nonce account for each active SOL vault; SOL
	// outbounds then anchor on the stored nonce (which never expires) instead of a
	// recent blockhash, removing the ~60s keysign+broadcast expiry window. Unset/0
	// leaves the feature dormant — no accounts are created and every outbound uses
	// the existing recent-blockhash path unchanged. Disabling after rollout is
	// graceful: provisioning stops and retiring vaults close their nonce accounts at
	// churn, while extant nonce accounts keep serving outbounds until then. Operator
	// emergency stop remains HaltSigningSOL.
	MimirKeySOLDurableNonce = "SOLDurableNonce"

	MimirTemplateEVMAllowanceCheck         = "EVMAllowanceCheck-%s"         // Use with Chain
	MimirTemplateEVMDirectERC20Inbound     = "EVMDirectERC20Inbound-%s"     // Use with Chain
	MimirTemplateConfMultiplierBasisPoints = "ConfMultiplierBasisPoints-%s" // Use with Chain
	MimirTemplateMaxConfirmations          = "MaxConfirmations-%s"          // Use with Chain
	MimirTemplateSwapSlipBasisPointsMin    = "SwapSlipBasisPointsMin-%s"    // Use with MimirRef
	MimirTemplateSecuredAssetHaltDeposit   = "HaltSecuredDeposit-%s"        // Use with Chain
	MimirTemplateSecuredAssetHaltWithdraw  = "HaltSecuredWithdraw-%s"       // Use with Chain
	MimirTemplateTradeAssetHaltDeposit     = "HaltTradeDeposit-%s"          // Use with Chain
	MimirTemplateTradeAssetHaltWithdraw    = "HaltTradeWithdraw-%s"         // Use with Chain
	MimirTemplateWasmHaltChecksum          = "HaltWasmCs-%s"                // Encode the checksum to base32 to fit within mimir's 64 char limit and case insenstivity. Truncate trailing `=` for brevity
	MimirTemplateWasmHaltContract          = "HaltWasmContract-%s"          // Use contract address checksum (last 6) for brevity and to fit inside mimir's 64 char length
	MimirTemplateWasmHaltDeployer          = "HaltWasmDeployer-%s"          // Use full deployer address to prevent a deployer from instantiating new contracts
	MimirTemplateMaxGas                    = "MaxGas-%s"                    // Use with Chain (e.g., MaxGas-ETH)
	MimirTemplateRevShareThorName          = "REVSHARE-%s"                  // Use with thorname (e.g., REVSHARE-foobar)
	MimirTemplateSwitch                    = "EnableSwitch-%s-%s"           // Use with Chain, Symbol
	MimirTemplatePauseLPDeposit            = "PauseLPDeposit-%s"            // Use with Asset MimirString
	MimirTemplateOverSolvencyToTreasuryBps = "OverSolvencyToTreasuryBps-%s" // Use with Asset MimirString
	MimirTemplateHaltSigning               = "HaltSigning%s"                // Use with Chain (mixed case, e.g., HaltSigningETH)
	MimirTemplateHaltTrading               = "Halt%sTrading"                // Use with Chain (mixed case, e.g., HaltETHTrading)
	MimirTemplateSkipSignerCache           = "SkipSignerCache%s"            // Use with Chain (e.g., SkipSignerCacheBTC). Operational flag to allow signer cache bypass on expiry.
	MimirKeyHaltTradingGlobal              = "HaltTrading"                  // Global trading halt
	MimirTemplateAllowNoVault              = "AllowNoVault-%s"              // Use with Chain; authorizes noop:novault vault debits on that chain

	MimirRefL1           = "L1"           // Use with SwapSlipBasisPoints
	MimirRefSynth        = "Synth"        // Use with SwapSlipBasisPoints
	MimirRefTradeAccount = "TradeAccount" // Use with SwapSlipBasisPoints

	MimirMaxBatchSize           = "AttestationMaxBatchSize"
	MimirPeerConcurrentSends    = "AttestationPeerConcurrentSends"
	MimirPeerConcurrentReceives = "AttestationPeerConcurrentReceives"
)
