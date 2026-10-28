package types

var (
	SystemProgramID = MustPublicKeyFromString("11111111111111111111111111111111")
	MemoProgramID   = MustPublicKeyFromString("MemoSq4gqABAXKb96qnH8TysNcWxMyWCqXgDLGmfcHr")
	// MemoV1ProgramID is the first SPL Memo program. It is deprecated but still deployed,
	// and older wallets write deposit memos with it.
	MemoV1ProgramID = MustPublicKeyFromString("Memo1UhkJRfHyvLMcVucJwxXeuD728EqVDDwQDxFMNo")
	// ComputeBudgetProgramID sets a transaction's compute-unit limit and price.
	ComputeBudgetProgramID = MustPublicKeyFromString("ComputeBudget111111111111111111111111111111")
	// SysVarRecentBlockhashesID is the RecentBlockhashes sysvar, a required account
	// of the SystemProgram AdvanceNonceAccount instruction used by durable-nonce
	// transactions.
	SysVarRecentBlockhashesID = MustPublicKeyFromString("SysvarRecentB1ockHashes11111111111111111111")
	// SysVarRentID is the Rent sysvar, a required account of the SystemProgram
	// InitializeNonceAccount and WithdrawNonceAccount instructions used to provision
	// and close durable-nonce accounts.
	SysVarRentID = MustPublicKeyFromString("SysvarRent111111111111111111111111111111111")
	// TokenProgramID is the classic SPL Token program, the only token program whose
	// mints THORChain supports. Token-2022 mints are excluded because their extensions
	// (transfer fees, transfer hooks, permanent delegates) break 1:1 vault accounting.
	TokenProgramID = MustPublicKeyFromString("TokenkegQfeZyiNwAJbNbGKPFXCWuBvf9Ss623VQ5DA")
	// Token2022ProgramID is the Token-2022 program. Its mints are not supported, but its
	// accounts are token accounts all the same, never wallets.
	Token2022ProgramID = MustPublicKeyFromString("TokenzQdBNbLqP5VEhdkAS6EPFLC1PHnBqCXEpPxuEb")
	// AssociatedTokenProgramID derives and creates the canonical token account a wallet
	// holds for a mint. Vaults receive and send SPL tokens only through these accounts.
	AssociatedTokenProgramID = MustPublicKeyFromString("ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL")
)
