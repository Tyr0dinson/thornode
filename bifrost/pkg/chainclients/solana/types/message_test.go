package types

import (
	"testing"

	. "gopkg.in/check.v1"
)

func MessageTestPackage(t *testing.T) { TestingT(t) }

type MessageTestSuite struct{}

var _ = Suite(&MessageTestSuite{})

func (s *MessageTestSuite) TestSerialize(c *C) {
	header := MessageHeader{
		NumRequireSignatures:        1,
		NumReadonlySignedAccounts:   0,
		NumReadonlyUnsignedAccounts: 2,
	}

	senderAccount := MustPublicKeyFromString("9aE476sH92Vz7DMPyq5WLPkrKWivxeuTKEFKd2sZZcde")
	receiverAccount := MustPublicKeyFromString("HEhDGuxaxGr9LuNtBdvbX2uggyAKoxYgHFaAiqxVu8UY")
	accounts := []PublicKey{senderAccount, receiverAccount, SystemProgramID, MemoProgramID}

	transferInstruction := NewCompiledTransferInstruction(2, 0, 1, 1000000000)
	memoInstruction := NewCompiledInstruction(3, []int{0}, []byte("Hello World"))

	m := NewMessage(MessageVersionLegacy, header, accounts, "9rAtxuhtKn8qagc3UtZFyhLrw5zkh6etv43TibaXuSKo", []CompiledInstruction{transferInstruction, memoInstruction})
	serialized, err := m.Serialize()
	c.Assert(err, IsNil)

	// TODO: double check this
	// Expected length of serialized message
	// Header = 3 bytes
	// Accounts = 4 * 32 = 128 bytes
	// RecentBlockHash = 32 bytes
	// Transfer Instruction - 17 bytes
	// - ProgramIDIndex = 1 byte
	// - Accounts = 2 bytes
	// - Data = 14 bytes
	// Memo Instruction - 14 bytes
	// - ProgramIDIndex = 1 byte
	// - Accounts = 2 bytes
	// - Data = 11 bytes

	c.Assert(len(serialized), Equals, 197)
}
