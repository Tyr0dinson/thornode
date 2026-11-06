package utxo

import (
	"bytes"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/firoutil"
)

func (c *Client) signFIRO(tx *wire.MsgTx, amounts map[string]int64, source []byte) ([]byte, error) {
	previous := make([]*wire.TxOut, len(tx.TxIn))
	for i, in := range tx.TxIn {
		key := fmt.Sprintf("%s-%d", in.PreviousOutPoint.Hash, in.PreviousOutPoint.Index)
		previous[i] = wire.NewTxOut(amounts[key], source)
	}
	hashes, err := firoutil.SignatureHashes(tx, previous)
	if err != nil {
		return nil, err
	}
	for i, hash := range hashes {
		signature, err := c.privKey.Sign(hash)
		if err != nil {
			return nil, err
		}
		tx.TxIn[i].SignatureScript, err = firoutil.SignatureScript(hash, signature, c.privKey.PubKey())
		if err != nil {
			return nil, err
		}
	}
	err = firoutil.VerifyTransaction(tx, previous)
	if err != nil {
		return nil, err
	}
	var serialized bytes.Buffer
	err = tx.Serialize(&serialized)
	if err != nil {
		return nil, err
	}
	return serialized.Bytes(), nil
}
