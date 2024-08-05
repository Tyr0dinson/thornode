package types

import (
	"encoding/binary"

	"gitlab.com/thorchain/thornode/common"
)

type MessageVersion string

const (
	MessageVersionLegacy = "legacy"
	MessageVersionV0     = "v0"
)

type AccountMeta struct {
	PubKey     PublicKey
	IsSigner   bool
	IsWritable bool
}

type MessageHeader struct {
	NumRequireSignatures        uint8
	NumReadonlySignedAccounts   uint8
	NumReadonlyUnsignedAccounts uint8
}

type Message struct {
	Version             MessageVersion
	Header              MessageHeader
	Accounts            []PublicKey
	RecentBlockHash     string
	Instructions        []CompiledInstruction
	AddressLookupTables []CompiledAddressLookupTable
}

type CompiledAddressLookupTable struct {
	AccountKey      PublicKey
	WritableIndexes []uint8
	ReadonlyIndexes []uint8
}

func NewMessage(version MessageVersion, header MessageHeader, accounts []PublicKey, recentBlockHash string, instructions []CompiledInstruction) Message {
	return Message{
		Version:         version,
		Header:          header,
		Accounts:        accounts,
		RecentBlockHash: recentBlockHash,
		Instructions:    instructions,
	}
}

func (m *Message) Serialize() ([]byte, error) {
	b := []byte{}

	b = append(b, m.Header.NumRequireSignatures)
	b = append(b, m.Header.NumReadonlySignedAccounts)
	b = append(b, m.Header.NumReadonlyUnsignedAccounts)

	b = append(b, UintToVarLenBytes(uint64(len(m.Accounts)))...)
	for _, key := range m.Accounts {
		b = append(b, key[:]...)
	}

	blockHash, err := common.Base58Decode(m.RecentBlockHash)
	if err != nil {
		return nil, err
	}
	b = append(b, blockHash...)

	b = append(b, UintToVarLenBytes(uint64(len(m.Instructions)))...)
	for _, instruction := range m.Instructions {
		b = append(b, byte(instruction.ProgramIDIndex))
		b = append(b, UintToVarLenBytes(uint64(len(instruction.Accounts)))...)
		for _, accountIdx := range instruction.Accounts {
			b = append(b, byte(accountIdx))
		}

		b = append(b, UintToVarLenBytes(uint64(len(instruction.Data)))...)
		b = append(b, instruction.Data...)
	}

	// if len(m.Version) > 0 && m.Version != MessageVersionLegacy {
	// 	versionNum, err := strconv.Atoi(string(m.Version[1:]))
	// 	if err != nil || versionNum > 255 {
	// 		return nil, fmt.Errorf("failed to parse message version")
	// 	}
	// 	if versionNum > 128 {
	// 		return nil, fmt.Errorf("unexpected message version")
	// 	}
	// 	b = append([]byte{byte(versionNum + 128)}, b...)
	// }

	return b, nil
}

func UintToVarLenBytes(l uint64) []byte {
	if l == 0 {
		return []byte{0x0}
	}
	b := make([]byte, binary.MaxVarintLen64)
	binary.PutUvarint(b, l)
	trimTrailingZeroByte(&b)
	return b
}

func trimTrailingZeroByte(b *[]byte) {
	for len(*b) > 0 {
		if (*b)[len(*b)-1] != 0 {
			break
		}
		*b = (*b)[:len(*b)-1]
	}
}
