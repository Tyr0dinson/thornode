package main

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"log"

	"gitlab.com/thorchain/thornode/bifrost/pkg/chainclients/solana/types"
)

func main() {
	fmt.Println("Hello, World!")

	solTx := types.Transaction{}

	// Create account pubkeys
	vaultAddr := "GqtbinEwhbrr5WcD4phdaZKFunWWw9L42szKyZfru9Jr"
	toAddr := "F11ZiYBC2b7pkuJYA9kRJtAWA2CjvYKmusG99VHHbSDJ"
	vaultPubKey, err := types.PublicKeyFromString(vaultAddr)
	if err != nil {
		fmt.Errorf("fail to create vault public key: %w", err)
		return
	}
	toPubkey, err := types.PublicKeyFromString(toAddr)
	if err != nil {
		fmt.Errorf("fail to create to public key: %w", err)
		return
	}

	accountKeys := []types.PublicKey{vaultPubKey, toPubkey, types.SystemProgramID, types.MemoProgramID}
	memo := "OUT:6A79FDB347E3C2C203F5E54D115E8F2B40B230D9C8D80426D54D1C2E03171036"

	// Memo instruction
	// Program index is 3 (referencing accountKeys) for the memo program
	// Account index is 0 for the vault/signer
	memoInstruction := types.NewCompiledInstruction(3, []int{0}, []byte(memo))

	// Transfer instruction
	// Program index is 2 (referencing accountKeys) for the system program
	// Account index is 0 for the vault/signer
	// Account index is 1 for the recipient
	transferInstruction := types.NewCompiledTransferInstruction(2, 0, 1, uint64(10000))

	header := types.MessageHeader{
		NumRequireSignatures:        1,
		NumReadonlySignedAccounts:   0,
		NumReadonlyUnsignedAccounts: 2,
	}

	recentBlockhash := "AH46FaE5ecVkauWg46kARVdvD5w3cJTUk9qybQKnhMKT"

	message := types.NewMessage(types.MessageVersionV0, header, accountKeys, recentBlockhash, []types.CompiledInstruction{memoInstruction, transferInstruction})
	transaction := types.Transaction{
		Message:    message,
		Signatures: []string{},
	}

	transaction.Signatures = append(solTx.Signatures, "2idJ2vUnD2uxedhPCoqHLKbBWZQdkQgDfkxzrYY9YeYPVyXLWAttBNJvcbBGGu9cDXMkkkzbVdkEsTDYJg9X1RRH")

	serialized, err := transaction.Serialize()
	if err != nil {
		fmt.Errorf("fail to serialize transaction: %w", err)
		return
	}

	// Encode the serialized transaction to base64
	base64Tx := base64.StdEncoding.EncodeToString(serialized)

	fmt.Println(base64Tx)

	// Write the base64 transaction to a file
	err = ioutil.WriteFile("transaction.txt", []byte(base64Tx), 0644)
	if err != nil {
		log.Fatalf("failed to write transaction to file: %v", err)
	}

	fmt.Println("Transaction saved to transaction.txt")
}
