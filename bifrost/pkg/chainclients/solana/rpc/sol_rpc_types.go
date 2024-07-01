package rpc

// const (
// 	PublicKeyLength = 32
// 	MaxSeedLength   = 32
// 	MaxSeed         = 16
// )

// type PublicKey [PublicKeyLength]byte

// // Block represents the data returned by the getBlock endpoint
// type Block struct {
// 	Slot              uint64   `json:"slot"`
// 	ParentSlot        uint64   `json:"parentSlot"`
// 	BlockTime         *int64   `json:"blockTime,omitempty"`
// 	Blockhash         string   `json:"blockhash"`
// 	PreviousBlockhash string   `json:"previousBlockhash"`
// 	Transactions      []string `json:"transactions"`
// }

// // Transaction represents the structure of a transaction within a block
// type Transaction struct {
// 	Signatures []string `json:"signatures"`
// 	Message    struct {
// 		AccountKeys  []string `json:"accountKeys"`
// 		Instructions []struct {
// 			ProgramIDIndex int    `json:"programIdIndex"`
// 			Accounts       []int  `json:"accounts"`
// 			Data           string `json:"data"`
// 		} `json:"instructions"`
// 		RecentBlockhash string `json:"recentBlockhash"`
// 	} `json:"message"`
// }

// type Transaction struct {
// 	// rpc
// 	Slot uint64
// 	Meta *TransactionMeta
// 	// Transaction types.Transaction
// 	BlockTime *int64

// 	// custom
// 	AccountKeys []PublicKey
// }

// // TransactionMetaTokenBalance is a part of TransactionMeta
// type TransactionMetaTokenBalance struct {
// 	AccountIndex  uint64              `json:"accountIndex"`
// 	Mint          string              `json:"mint"`
// 	Owner         string              `json:"owner,omitempty"`
// 	ProgramId     string              `json:"programId,omitempty"`
// 	UITokenAmount TokenAccountBalance `json:"uiTokenAmount"`
// }

// type TokenAccountBalance struct {
// 	Amount         string `json:"amount"`
// 	Decimals       uint8  `json:"decimals"`
// 	UIAmountString string `json:"uiAmountString"`
// }

// type TransactionLoadedAddresses struct {
// 	Writable []string `json:"writable"`
// 	Readonly []string `json:"readonly"`
// }

// type TransactionMeta struct {
// 	// Err               any
// 	Fee               uint64
// 	PreBalances       []int64
// 	PostBalances      []int64
// 	PreTokenBalances  []TransactionMetaTokenBalance
// 	PostTokenBalances []TransactionMetaTokenBalance
// 	LogMessages       []string
// 	InnerInstructions []InnerInstruction
// 	LoadedAddresses   TransactionLoadedAddresses
// 	// ReturnData           *ReturnData
// 	ComputeUnitsConsumed *uint64
// }

// type InnerInstruction struct {
// 	Index uint64
// 	// Instructions []types.CompiledInstruction
// }
