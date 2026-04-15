package types

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewTransferTransactionOmitsEmptyMemoInstruction(t *testing.T) {
	from := MustPublicKeyFromString("E6wak7XKQtXVS8DqRRFnhToht7Grdz7BHWMQaCdJdaqc")
	to := MustPublicKeyFromString("2YpsG3TShyPDPWbtU5hTtGpb5XbKDQqaWVspVdWVaJSc")

	tx := NewTransferTransaction(from, to, 6_300_002, "", "ignored-for-test")

	require.Len(t, tx.Message.Accounts, 3)
	require.Equal(t, uint8(1), tx.Message.Header.NumReadonlyUnsignedAccounts)
	require.Len(t, tx.Message.Instructions, 1)
	require.Equal(t, 2, tx.Message.Instructions[0].ProgramIDIndex)
	require.Len(t, tx.Message.Instructions[0].Accounts, 2)
	require.Equal(t, []int{0, 1}, tx.Message.Instructions[0].Accounts)
	require.Equal(t, uint64(63_000_020), binary.LittleEndian.Uint64(tx.Message.Instructions[0].Data[4:12]))
}

func TestNewTransferTransactionIncludesMemoInstructionWhenPresent(t *testing.T) {
	from := MustPublicKeyFromString("E6wak7XKQtXVS8DqRRFnhToht7Grdz7BHWMQaCdJdaqc")
	to := MustPublicKeyFromString("2YpsG3TShyPDPWbtU5hTtGpb5XbKDQqaWVspVdWVaJSc")

	tx := NewTransferTransaction(from, to, 6_331_658, "r:00001", "ignored-for-test")

	require.Len(t, tx.Message.Accounts, 4)
	require.Equal(t, uint8(2), tx.Message.Header.NumReadonlyUnsignedAccounts)
	require.Len(t, tx.Message.Instructions, 2)
	require.Equal(t, 3, tx.Message.Instructions[0].ProgramIDIndex)
	require.Equal(t, []byte("r:00001"), tx.Message.Instructions[0].Data)
	require.Equal(t, 2, tx.Message.Instructions[1].ProgramIDIndex)
}
