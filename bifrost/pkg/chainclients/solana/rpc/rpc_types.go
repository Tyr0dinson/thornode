package rpc

import (
	"encoding/json"
	"strconv"
)

////////////////////////////////////////////////////////////////////////////////////////
// rpc_types contains the structs for the responses from the Solana RPC
////////////////////////////////////////////////////////////////////////////////////////

type RPCTransaction struct {
	Meta        RPCMeta    `json:"meta"`
	Transaction RPCTxnData `json:"transaction"`
	Version     RPCVersion `json:"version"`
}

type RPCMeta struct {
	ComputeUnitsConsumed uint64                `json:"computeUnitsConsumed"`
	Err                  *RPCTransactionError  `json:"err"` // Nullable error struct
	Fee                  uint64                `json:"fee"`
	InnerInstructions    []RPCInnerInstruction `json:"innerInstructions"`
	LoadedAddresses      RPCAddrList           `json:"loadedAddresses"`
	LogMessages          []string              `json:"logMessages"`
	PostBalances         []uint64              `json:"postBalances"`
	PostTokenBalances    []RPCTokenBalance     `json:"postTokenBalances"`
	PreBalances          []uint64              `json:"preBalances"`
	PreTokenBalances     []RPCTokenBalance     `json:"preTokenBalances"`
	Rewards              []RPCReward           `json:"rewards"`
	Status               RPCTxnStatus          `json:"status"`
}

// Represents an error in a Solana transaction
type RPCTransactionError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Inner instructions represent program calls within a transaction
type RPCInnerInstruction struct {
	Index        int              `json:"index"`        // Index of the instruction in the transaction
	Instructions []RPCInstruction `json:"instructions"` // List of instructions
}

// Represents token balances before and after a transaction
type RPCTokenBalance struct {
	AccountIndex int            `json:"accountIndex"`  // Index of the account in the transaction
	Mint         string         `json:"mint"`          // Mint address of the token
	Owner        string         `json:"owner"`         // Owner of the token account
	ProgramId    string         `json:"programId"`     // Token program ID
	Amount       RPCTokenAmount `json:"uiTokenAmount"` // Token amount with decimals
}

// Represents token amount details, including decimals
type RPCTokenAmount struct {
	Amount      string  `json:"amount"`         // Raw token amount as string
	Decimals    int     `json:"decimals"`       // Token decimal places
	UIAmount    float64 `json:"uiAmount"`       // Human-readable token amount
	UIAmountStr string  `json:"uiAmountString"` // String representation
}

// Represents an individual reward entry
type RPCReward struct {
	Pubkey      string  `json:"pubkey"`      // Account receiving the reward
	Lamports    int64   `json:"lamports"`    // Reward amount in lamports
	PostBalance uint64  `json:"postBalance"` // Account balance after the reward
	RewardType  *string `json:"rewardType"`  // Optional reward type (e.g., "staking")
}

// Represents transaction status
type RPCTxnStatus struct {
	Ok  interface{}          `json:"Ok,omitempty"`  // Indicates success
	Err *RPCTransactionError `json:"Err,omitempty"` // Error details if transaction failed
}

type RPCTxnData struct {
	Message    RPCMessage `json:"message"`
	Signatures []string   `json:"signatures"`
}

type RPCAddrList struct {
	Readonly []interface{} `json:"readonly"`
	Writable []interface{} `json:"writable"`
}

type RPCMessage struct {
	AccountKeys         []string                 `json:"accountKeys"`
	AddressLookupTables []RPCAddressLookupTables `json:"addressTableLookups"`
	Header              RPCHeader                `json:"header"`
	Instructions        []RPCInstruction         `json:"instructions"`
	RecentBlockhash     string                   `json:"recentBlockhash"`
}

type RPCAddressLookupTables struct {
	AccountKey      string `json:"accountKey"`
	WritableIndexes []int  `json:"writableIndexes"`
	ReadonlyIndexes []int  `json:"readonlyIndexes"`
}

type RPCHeader struct {
	NumReadonlySignedAccounts   int `json:"numReadonlySignedAccounts"`
	NumReadonlyUnsignedAccounts int `json:"numReadonlyUnsignedAccounts"`
	NumRequiredSignatures       int `json:"numRequiredSignatures"`
}

type RPCInstruction struct {
	Accounts       []int       `json:"accounts"`
	Data           string      `json:"data"`
	ProgramIdIndex int         `json:"programIdIndex"`
	StackHeight    interface{} `json:"stackHeight"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Custom type for Version to handle both string and numeric values
type RPCVersion struct {
	Value string
}

func (v *RPCVersion) UnmarshalJSON(b []byte) error {
	if b[0] == '"' {
		return json.Unmarshal(b, &v.Value)
	}
	var num int
	if err := json.Unmarshal(b, &num); err != nil {
		return err
	}
	v.Value = strconv.Itoa(num)
	return nil
}

////////////////////////////////////////////////////////////////////////////////////////
// Helpers
////////////////////////////////////////////////////////////////////////////////////////

func (r *RPCTransaction) GetInstructions() []RPCInstruction {
	return r.Transaction.Message.Instructions
}

func (r *RPCTransaction) GetInnerInstructions() []RPCInnerInstruction {
	return r.Meta.InnerInstructions
}

func (r *RPCTransaction) GetAccountAtIndex(i int) string {
	if i >= len(r.Transaction.Message.AccountKeys) || len(r.Transaction.Message.AccountKeys) == 0 {
		return ""
	}
	return r.Transaction.Message.AccountKeys[i]
}
