package types

import "gitlab.com/thorchain/thornode/common"

////////////////////////////////////////////////////////////////////////////////////////
// transaction.go contains the structs and functions to serialize and deserialize a solana transaction
////////////////////////////////////////////////////////////////////////////////////////

type Signature []byte

type Transaction struct {
	Signatures []string
	Message    Message
}

func (tx *Transaction) Serialize() ([]byte, error) {
	// if len(tx.Signatures) == 0 || len(tx.Signatures) != int(tx.Message.Header.NumRequireSignatures) {
	// 	return nil, fmt.Errorf("invalid number of signatures")
	// }

	signatureCount := UintToVarLenBytes(uint64(len(tx.Signatures)))
	messageData, err := tx.Message.Serialize()
	if err != nil {
		return nil, err
	}

	output := make([]byte, 0, len(signatureCount)+len(signatureCount)*64+len(messageData))
	output = append(output, signatureCount...)
	for _, sig := range tx.Signatures {
		sigBytes, err := common.Base58Decode(sig)
		if err != nil {
			return nil, err
		}
		output = append(output, sigBytes...)
	}
	output = append(output, messageData...)

	return output, nil
}
