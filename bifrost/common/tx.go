package common

import (
	"encoding/json"

	"github.com/tendermint/tendermint/crypto"
)

type Tx interface {
	// Gets the Msg.
	GetMsgs() []Msg
}

// StdTx def
type StdTx struct {
	Msgs       []Msg          `json:"msg"`
	Signatures []StdSignature `json:"signatures"`
	Memo       string         `json:"memo"`
	Source     int64          `json:"source"`
	Data       []byte         `json:"data"`
}

// NewStdTx to instantiate an instance
func NewStdTx(msgs []Msg, sigs []StdSignature, memo string, source int64, data []byte) StdTx {
	return StdTx{
		Msgs:       msgs,
		Signatures: sigs,
		Memo:       memo,
		Source:     source,
		Data:       data,
	}
}

// GetMsgs def
func (tx StdTx) GetMsgs() []Msg { return tx.Msgs }

// StdSignDoc def
type StdSignDoc struct {
	ChainID       string            `json:"chain_id"`
	AccountNumber int64             `json:"account_number"`
	Sequence      int64             `json:"sequence"`
	Memo          string            `json:"memo"`
	Source        int64             `json:"source"`
	Msgs          []json.RawMessage `json:"msgs"`
	Data          []byte            `json:"data"`
}

// StdSignature Signature
type StdSignature struct {
	crypto.PubKey `json:"pub_key"` // optional
	Signature     []byte           `json:"signature"`
	AccountNumber int64            `json:"account_number"`
	Sequence      int64            `json:"sequence"`
}

// StdSignMsg def
type StdSignMsg struct {
	ChainID       string `json:"chain_id"`
	AccountNumber int64  `json:"account_number"`
	Sequence      int64  `json:"sequence"`
	Msgs          []Msg  `json:"msgs"`
	Memo          string `json:"memo"`
	Source        int64  `json:"source"`
	Data          []byte `json:"data"`
}

// Bytes gets message bytes
func (msg StdSignMsg) Bytes() []byte {
	return StdSignBytes(msg.ChainID, msg.AccountNumber, msg.Sequence, msg.Msgs, msg.Memo, msg.Source, msg.Data)
}

// StdSignBytes returns the bytes to sign for a transaction.
func StdSignBytes(chainID string, accnum int64, sequence int64, msgs []Msg, memo string, source int64, data []byte) []byte {
	var msgsBytes []json.RawMessage
	for _, msg := range msgs {
		msgsBytes = append(msgsBytes, json.RawMessage(msg.GetSignBytes()))
	}
	bz, err := Cdc.MarshalJSON(StdSignDoc{
		AccountNumber: accnum,
		ChainID:       chainID,
		Memo:          memo,
		Msgs:          msgsBytes,
		Sequence:      sequence,
		Source:        source,
		Data:          data,
	})
	if err != nil {
		panic(err)
	}
	return MustSortJSON(bz)
}
