package rpc

import (
	"strconv"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// Account holds the decoded System::Account values used by the client.
type Account struct {
	Nonce   uint32
	Balance cosmos.Uint // Free balance in native units.
}

// SignedBlock is the response from chain_getBlock.
type SignedBlock struct {
	Block Block `json:"block"`
}

// Block represents a Substrate block.
type Block struct {
	Header     Header   `json:"header"`
	Extrinsics []string `json:"extrinsics"` // hex-encoded extrinsics
}

// Header represents a Substrate block header.
type Header struct {
	ParentHash     string `json:"parentHash"`
	Number         string `json:"number"` // hex-encoded block number
	StateRoot      string `json:"stateRoot"`
	ExtrinsicsRoot string `json:"extrinsicsRoot"`
}

// BlockNumber returns the block number as uint64.
func (h *Header) BlockNumber() (uint64, error) {
	s := strings.TrimPrefix(h.Number, "0x")
	return strconv.ParseUint(s, 16, 64)
}

// RuntimeVersion holds the runtime version information.
type RuntimeVersion struct {
	SpecName           string `json:"specName"`
	SpecVersion        uint32 `json:"specVersion"`
	TransactionVersion uint32 `json:"transactionVersion"`
}
