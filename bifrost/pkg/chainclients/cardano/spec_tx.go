package cardano

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// TransactionWitnessSet represents the witnesses for a transaction
type TransactionWitnessSet struct {
	VKeyWitnesses []VKeyWitness
	Scripts       []Script
}

// VKeyWitness represents a verification key witness
type VKeyWitness struct {
	VKey      VKey
	Signature Signature
}

// Script represents a native or Plutus script
type Script interface {
	IsScript() bool
}

// TransactionMetadata represents transaction metadata
type TransactionMetadata struct {
	Int   *int64
	Bytes []byte
	Text  string
	Map   map[TransactionMetadata]TransactionMetadata
	List  []TransactionMetadata
}

func (m TransactionMetadata) MarshalCBOR() ([]byte, error) {
	if m.Int != nil {
		return cbor.Marshal(*m.Int)
	}
	if m.Bytes != nil {
		return cbor.Marshal(m.Bytes)
	}
	if m.Text != "" {
		return cbor.Marshal(m.Text)
	}
	if m.Map != nil {
		return cbor.Marshal(m.Map)
	}
	if m.List != nil {
		return cbor.Marshal(m.List)
	}
	return nil, fmt.Errorf("empty metadata")
}

func (m *TransactionMetadata) UnmarshalCBOR(data []byte) error {
	// Try each type in sequence
	var i int64
	if err := cbor.Unmarshal(data, &i); err == nil {
		m.Int = &i
		return nil
	}

	var b []byte
	if err := cbor.Unmarshal(data, &b); err == nil {
		m.Bytes = b
		return nil
	}

	var s string
	if err := cbor.Unmarshal(data, &s); err == nil {
		m.Text = s
		return nil
	}

	var mp map[TransactionMetadata]TransactionMetadata
	if err := cbor.Unmarshal(data, &mp); err == nil {
		m.Map = mp
		return nil
	}

	var l []TransactionMetadata
	if err := cbor.Unmarshal(data, &l); err == nil {
		m.List = l
		return nil
	}

	return fmt.Errorf("invalid metadata format")
}
