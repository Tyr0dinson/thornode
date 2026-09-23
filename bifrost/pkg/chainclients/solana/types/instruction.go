package types

import (
	"encoding/binary"
)

type CompiledInstruction struct {
	ProgramIDIndex int
	Accounts       []int
	Data           []byte
}

func NewCompiledInstruction(programIDIndex int, accounts []int, data []byte) CompiledInstruction {
	return CompiledInstruction{
		ProgramIDIndex: programIDIndex,
		Accounts:       accounts,
		Data:           data,
	}
}

// Create transfer instruction data
func serializeTransferInstructionData(amount uint64) []byte {
	var data []byte

	// Append transfer instruction
	transferInstruction := uint32(2)
	transferInstructionBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(transferInstructionBytes, transferInstruction)
	data = append(data, transferInstructionBytes...)

	// Append amount
	amountBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(amountBytes, amount)
	data = append(data, amountBytes...)

	return data
}

// NewCompiledTransferInstruction creates a CompiledIntstruction for a transfer instruction
func NewCompiledTransferInstruction(programIndex, fromIndex, toIndex int, amount uint64) CompiledInstruction {
	accounts := []int{fromIndex, toIndex}
	data := serializeTransferInstructionData(amount)
	return NewCompiledInstruction(programIndex, accounts, data)
}

// serializeAdvanceNonceInstructionData encodes the SystemProgram AdvanceNonceAccount
// instruction: the 4-byte little-endian SystemInstruction enum index 4, with no
// further arguments.
func serializeAdvanceNonceInstructionData() []byte {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, 4)
	return data
}

// NewCompiledAdvanceNonceInstruction builds the SystemProgram AdvanceNonceAccount
// instruction. Account order is fixed by the System program:
//
//	0: nonce account            (writable)
//	1: RecentBlockhashes sysvar (readonly)
//	2: nonce authority          (signer)
//
// This must be the first instruction in a durable-nonce transaction.
func NewCompiledAdvanceNonceInstruction(programIndex, nonceIndex, recentBlockhashesIndex, authorityIndex int) CompiledInstruction {
	accounts := []int{nonceIndex, recentBlockhashesIndex, authorityIndex}
	return NewCompiledInstruction(programIndex, accounts, serializeAdvanceNonceInstructionData())
}

type Instruction struct {
	ProgramID PublicKey
	Accounts  []AccountMeta
	Data      []byte
}
