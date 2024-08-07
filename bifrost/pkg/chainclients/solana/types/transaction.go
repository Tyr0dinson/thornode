package types

import (
	"crypto/ed25519"
	"fmt"
)

////////////////////////////////////////////////////////////////////////////////////////
// transaction.go contains the structs and functions to serialize and deserialize a solana transaction
////////////////////////////////////////////////////////////////////////////////////////

type Signature []byte

type Transaction struct {
	Signatures []Signature
	Message    Message
}

func NewTransaction(message Message) Transaction {
	signatures := make([]Signature, 0, message.Header.NumRequireSignatures)
	for i := uint8(0); i < message.Header.NumRequireSignatures; i++ {
		signatures = append(signatures, make([]byte, 64))
	}
	return Transaction{
		Signatures: signatures,
		Message:    message,
	}
}

func (tx *Transaction) Serialize() ([]byte, error) {
	if len(tx.Signatures) == 0 || len(tx.Signatures) != int(tx.Message.Header.NumRequireSignatures) {
		return nil, fmt.Errorf("invalid number of signatures")
	}

	signatureCount := UintToVarLenBytes(uint64(len(tx.Signatures)))
	messageData, err := tx.Message.Serialize()
	if err != nil {
		return nil, err
	}

	output := make([]byte, 0, len(signatureCount)+len(signatureCount)*64+len(messageData))
	output = append(output, signatureCount...)
	for _, sig := range tx.Signatures {
		output = append(output, sig...)
	}
	output = append(output, messageData...)

	return output, nil
}

func (tx *Transaction) AddSignature(sig []byte) error {
	data, err := tx.Message.Serialize()
	if err != nil {
		return fmt.Errorf("failed to serialize message, err: %v", err)
	}
	for i := uint8(0); i < tx.Message.Header.NumRequireSignatures; i++ {
		a := tx.Message.Accounts[i]
		if ed25519.Verify(a.Bytes(), data, sig) {
			tx.Signatures[i] = sig
			return nil
		}
	}
	return fmt.Errorf("no matching signer")
}
