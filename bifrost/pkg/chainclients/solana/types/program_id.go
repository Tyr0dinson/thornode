package types

var (
	SystemProgramID = MustPublicKeyFromString("11111111111111111111111111111111")
	MemoProgramID   = MustPublicKeyFromString("MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr")
	// SysVarRecentBlockhashesID is the RecentBlockhashes sysvar, a required account
	// of the SystemProgram AdvanceNonceAccount instruction used by durable-nonce
	// transactions (#661).
	SysVarRecentBlockhashesID = MustPublicKeyFromString("SysvarRecentB1ockHashes11111111111111111111")
)
