package rpc

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"

	"gitlab.com/thorchain/thornode/common"
)

// Response struct for getBlock
type GetBlockResponse struct {
	Jsonrpc string      `json:"jsonrpc"`
	Id      int         `json:"id"`
	Result  BlockResult `json:"result"`
	Error   RPCError    `json:"error"`
}

// Structs for the response data
type BlockResult struct {
	Blockhash         string        `json:"blockhash"`
	PreviousBlockhash string        `json:"previousBlockhash"`
	ParentSlot        int           `json:"parentSlot"`
	Transactions      []Transaction `json:"transactions"`
}

type Transaction struct {
	Meta        Meta    `json:"meta"`
	Transaction TxnData `json:"transaction"`
	Version     Version `json:"version"`
}

// Serialize the transaction
func (tx Transaction) Serialize() ([]byte, error) {
	var serializedTx []byte

	// Serialize signatures
	signatureCount := uint8(len(tx.Transaction.Signatures))
	serializedTx = append(serializedTx, signatureCount)
	for _, sig := range tx.Transaction.Signatures {
		sigBytes, err := common.Base58Decode(sig)
		if err != nil {
			return nil, fmt.Errorf("failed to decode transaction signature: %w %s", err, sig)
		}
		serializedTx = append(serializedTx, sigBytes...)
	}

	// Serialize recent blockhash
	blockhashBytes, err := common.Base58Decode(tx.Transaction.Message.RecentBlockhash)
	if err != nil {
		return nil, fmt.Errorf("failed to decode blockhash: %w", err)
	}
	serializedTx = append(serializedTx, blockhashBytes...)

	// Serialize instructions
	instructionCount := uint8(len(tx.Transaction.Message.Instructions))
	serializedTx = append(serializedTx, instructionCount)
	for _, instruction := range tx.Transaction.Message.Instructions {
		programIDIndex := uint8(instruction.ProgramIdIndex)
		serializedTx = append(serializedTx, programIDIndex)

		accountCount := uint8(len(instruction.Accounts))
		serializedTx = append(serializedTx, accountCount)
		for _, account := range instruction.Accounts {
			accountIndex := uint8(account)
			serializedTx = append(serializedTx, accountIndex)
		}

		dataLength := uint16(len(instruction.Data))
		dataLengthBytes := make([]byte, 2)
		binary.LittleEndian.PutUint16(dataLengthBytes, dataLength)
		serializedTx = append(serializedTx, dataLengthBytes...)

		dataBytes, err := common.Base58Decode(instruction.Data)
		if err != nil {
			return nil, fmt.Errorf("failed to decode dataBytes: %w", err)
		}
		serializedTx = append(serializedTx, dataBytes...)
	}

	return serializedTx, nil
}

// Custom type for Version to handle both string and numeric values
type Version struct {
	Value string
}

func (v *Version) UnmarshalJSON(b []byte) error {
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

type Meta struct {
	ComputeUnitsConsumed int           `json:"computeUnitsConsumed"`
	Err                  interface{}   `json:"err"`
	Fee                  int           `json:"fee"`
	InnerInstructions    []interface{} `json:"innerInstructions"`
	LoadedAddresses      AddrList      `json:"loadedAddresses"`
	LogMessages          []string      `json:"logMessages"`
	PostBalances         []int64       `json:"postBalances"`
	PostTokenBalances    []interface{} `json:"postTokenBalances"`
	PreBalances          []int64       `json:"preBalances"`
	PreTokenBalances     []interface{} `json:"preTokenBalances"`
	Rewards              interface{}   `json:"rewards"`
	Status               TxnStatus     `json:"status"`
}

type AddrList struct {
	Readonly []interface{} `json:"readonly"`
	Writable []interface{} `json:"writable"`
}

type TxnStatus struct {
	Ok interface{} `json:"Ok"`
}

type TxnData struct {
	Message    Message  `json:"message"`
	Signatures []string `json:"signatures"`
}

type Message struct {
	AccountKeys     []string      `json:"accountKeys"`
	Header          Header        `json:"header"`
	Instructions    []Instruction `json:"instructions"`
	RecentBlockhash string        `json:"recentBlockhash"`
}

type Header struct {
	NumReadonlySignedAccounts   int `json:"numReadonlySignedAccounts"`
	NumReadonlyUnsignedAccounts int `json:"numReadonlyUnsignedAccounts"`
	NumRequiredSignatures       int `json:"numRequiredSignatures"`
}

type Instruction struct {
	Accounts       []int       `json:"accounts"`
	Data           string      `json:"data"`
	ProgramIdIndex int         `json:"programIdIndex"`
	StackHeight    interface{} `json:"stackHeight"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// GetBlock - makes a getBlock RPC call to the Solana RPC. Will only return a
// "finalized" block.
func (s *SolRPC) GetBlock(height int64) (*BlockResult, error) {
	params := []interface{}{
		height,
		map[string]interface{}{
			"encoding":                       "json",
			"transactionDetails":             "full",
			"rewards":                        false,
			"commitment":                     "finalized",
			"maxSupportedTransactionVersion": 0,
		},
	}

	respBody, err := s.post("getBlock", params)
	if err != nil {
		return nil, fmt.Errorf("failed to call getBlock: %w", err)
	}

	// s.logger.Info().Int64("height", height).Str("response", string(respBody)).Msg("block fetched")

	var response GetBlockResponse
	if err := json.Unmarshal(respBody, &response); err != nil {
		return nil, err
	}

	if response.Error.Code != 0 {
		// Sometimes solana skips a slot, so in that case return an empty response to skip the slot
		if response.Error.Message == fmt.Sprintf("Slot %d was skipped, or missing due to ledger jump to recent snapshot", height) {
			fmt.Printf("Slot %d was skipped or missing. Returning empty result.\n", height)
			return nil, nil
		}
		return nil, fmt.Errorf("RPC Error: %s", response.Error.Message)
	}

	return &response.Result, nil
}
