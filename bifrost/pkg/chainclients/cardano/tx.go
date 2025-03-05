package cardano

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"

	"github.com/fxamacker/cbor/v2"
	"gitlab.com/thorchain/thornode/v3/common/cardano"
	"golang.org/x/crypto/blake2b"
)

type Tx struct {
	_          struct{} `cbor:",toarray"`
	Body       TxBody
	WitnessSet WitnessSet
	Valid      bool
	Metadata   map[uint64]any
}

// NewTx returns a pointer to a new Transaction
func NewTx() *Tx {
	return &Tx{
		Body:       NewTxBody(),
		WitnessSet: NewTXWitness(),
		Valid:      true,
	}
}

// Bytes returns a slice of cbor marshalled bytes
func (t *Tx) Bytes() ([]byte, error) {
	txArray := []any{
		t.Body,
		t.WitnessSet,
		t.Valid,
		t.Metadata,
	}
	return cbor.Marshal(txArray)
}

// Hex returns hex encoding of the transacion bytes
func (t *Tx) Hex() (string, error) {
	bytes, err := t.Bytes()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// Hash performs a blake2b hash of the transaction body and returns a slice of [32]byte
func (t *Tx) Hash() ([32]byte, error) {
	if err := t.CalculateAuxiliaryDataHash(); err != nil {
		return [32]byte{}, err
	}
	txBody, err := cbor.Marshal(t.Body)
	if err != nil {
		var bt [32]byte
		return bt, err
	}

	txHash := blake2b.Sum256(txBody)
	return txHash, nil
}

// Fee returns the fee(in lovelaces) required by the transaction from the linear formula
// fee = txFeeFixed + txFeePerByte*tx_len_in_bytes
func (t *Tx) Fee(lfee *LinearFee) (uint64, error) {
	if err := t.CalculateAuxiliaryDataHash(); err != nil {
		return 0, err
	}
	txCbor, err := cbor.Marshal(t)
	if err != nil {
		return 0, err
	}
	txBodyLen := len(txCbor)
	fee := lfee.TxFeeFixed + lfee.TxFeePerByte*uint64(txBodyLen)

	return fee, nil
}

// SetFee sets the fee
func (t *Tx) SetFee(fee uint64) {
	t.Body.Fee = fee
}

func (t *Tx) CalculateAuxiliaryDataHash() error {
	if t.Metadata != nil {
		mdBytes, err := cbor.Marshal(&t.Metadata)
		if err != nil {
			return fmt.Errorf("cannot serialize metadata: %w", err)
		}
		auxHash := blake2b.Sum256(mdBytes)
		t.Body.AuxiliaryDataHash = auxHash[:]
	}
	return nil
}

// AddInputs adds the inputs to the transaction body
func (t *Tx) AddInputs(inputs ...*TxInput) {
	t.Body.Inputs.TxIns = append(t.Body.Inputs.TxIns, inputs...)
}

// AddOutputs adds the outputs to the transaction body
func (t *Tx) AddOutputs(outputs ...TxOutput) {
	t.Body.Outputs = append(t.Body.Outputs, outputs...)
}

type TxInputSet struct {
	cbor.Marshaler
	TxIns []*TxInput
}

func (txI *TxInputSet) MarshalCBOR() ([]byte, error) {
	var buf bytes.Buffer
	encodeCBORHead(&buf, cborTypeTag, 258)
	err := cbor.MarshalToBuffer(txI.TxIns, &buf)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// TxBody contains the inputs, outputs, fee and titme to live for the transaction.
type TxBody struct {
	Inputs            TxInputSet `cbor:"0,keyasint"`
	Outputs           []TxOutput `cbor:"1,keyasint"`
	Fee               uint64     `cbor:"2,keyasint"`
	TTL               uint32     `cbor:"3,keyasint,omitempty"`
	AuxiliaryDataHash []byte     `cbor:"7,keyasint,omitempty"`
}

// NewTxBody returns a pointer to a new transaction body.
func NewTxBody() TxBody {
	return TxBody{
		Outputs: make([]TxOutput, 0),
	}
}

// Bytes returns a slice of cbor Marshalled bytes.
func (b *TxBody) Bytes() ([]byte, error) {
	return cbor.Marshal(b)
}

// Hex returns hex encoded string of the transaction bytes.
func (b *TxBody) Hex() (string, error) {
	by, err := b.Bytes()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(by), nil
}

type TxInput struct {
	cbor.Marshaler

	TxHash []byte
	Index  uint16
	Amount uint
}

// NewTxInput creates and returns a *TxInput from Transaction Hash(Hex Encoded), Transaction Index and Amount.
func NewTxInput(txHash string, txIx uint16, amount uint) *TxInput {
	hash, _ := hex.DecodeString(txHash)

	return &TxInput{
		TxHash: hash,
		Index:  txIx,
		Amount: amount,
	}
}

func (txI *TxInput) MarshalCBOR() ([]byte, error) {
	type arrayInput struct {
		_      struct{} `cbor:",toarray"`
		TxHash []byte
		Index  uint16
	}
	input := arrayInput{
		TxHash: txI.TxHash,
		Index:  txI.Index,
	}
	return cbor.Marshal(input)
}

type TxOutput struct {
	_       struct{} `cbor:",toarray"`
	Address cardano.Address
	Amount  uint
}

func NewTxOutput(addr cardano.Address, amount uint) TxOutput {
	return TxOutput{
		Address: addr,
		Amount:  amount,
	}
}

type VKeyWitnessSet []*VKeyWitness

func (v *VKeyWitnessSet) Len() int {
	return len(*v)
}

func (v *VKeyWitnessSet) Append(witness *VKeyWitness) {
	*v = append(*v, witness)
}

// MarshalCBOR implements cbor.Marshaler.
func (v *VKeyWitnessSet) MarshalCBOR() ([]byte, error) {
	var buf bytes.Buffer
	encodeCBORHead(&buf, cborTypeTag, 258)
	arr := []*VKeyWitness(*v)
	err := cbor.MarshalToBuffer(arr, &buf)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type WitnessSet struct {
	VKeys *VKeyWitnessSet `cbor:"0,keyasint,omitempty"`
}

// NewTXWitness returns a pointer to a Witness created from VKeyWitnesses.
func NewTXWitness(vks ...*VKeyWitness) WitnessSet {
	if len(vks) == 0 {
		return WitnessSet{}
	}

	vkeys := VKeyWitnessSet(vks)
	return WitnessSet{VKeys: &vkeys}
}

// VKeyWitness - Witness for use with Shelley based transactions
type VKeyWitness struct {
	_         struct{} `cbor:",toarray"`
	VKey      []byte
	Signature []byte
}

// NewVKeyWitness creates a Witness for Shelley Based transactions from a verification key and transaction signature.
func NewVKeyWitness(vkey, signature []byte) *VKeyWitness {
	return &VKeyWitness{
		VKey: vkey, Signature: signature,
	}
}

// BootstrapWitness for use with Byron/Legacy based transactions
type BootstrapWitness struct {
	_          struct{} `cbor:",toarray"`
	VKey       []byte
	Signature  []byte
	ChainCode  []byte
	Attributes []byte
}

// Fees

// LinearFee contains parameters for the linear fee equation `TxFeeFixed + len_bytes(tx) * TxFeePerByte`.
// These are provided in the protocol parameters
type LinearFee struct {
	TxFeePerByte uint64
	TxFeeFixed   uint64
}

// NewLinearFee returns a pointer to a new LinearFee from the provided params
func NewLinearFee(feePerByte uint64, fixedFee uint64) *LinearFee {
	return &LinearFee{
		TxFeePerByte: feePerByte,
		TxFeeFixed:   fixedFee,
	}
}

// encoding util

const (
	maxAdditionalInformationWithoutArgument       = 23
	additionalInformationWith1ByteArgument        = 24
	additionalInformationWith2ByteArgument        = 25
	additionalInformationWith4ByteArgument        = 26
	additionalInformationWith8ByteArgument        = 27
	cborTypeTag                             uint8 = 0xc0
)

// encodeHead writes CBOR head of specified type t and returns number of bytes written.
func encodeCBORHead(e *bytes.Buffer, t byte, n uint64) int {
	if n <= maxAdditionalInformationWithoutArgument {
		const headSize = 1
		e.WriteByte(t | byte(n))
		return headSize
	}

	if n <= math.MaxUint8 {
		const headSize = 2
		scratch := [headSize]byte{
			t | byte(additionalInformationWith1ByteArgument),
			byte(n),
		}
		e.Write(scratch[:])
		return headSize
	}

	if n <= math.MaxUint16 {
		const headSize = 3
		var scratch [headSize]byte
		scratch[0] = t | byte(additionalInformationWith2ByteArgument)
		binary.BigEndian.PutUint16(scratch[1:], uint16(n))
		e.Write(scratch[:])
		return headSize
	}

	if n <= math.MaxUint32 {
		const headSize = 5
		var scratch [headSize]byte
		scratch[0] = t | byte(additionalInformationWith4ByteArgument)
		binary.BigEndian.PutUint32(scratch[1:], uint32(n))
		e.Write(scratch[:])
		return headSize
	}

	const headSize = 9
	var scratch [headSize]byte
	scratch[0] = t | byte(additionalInformationWith8ByteArgument)
	binary.BigEndian.PutUint64(scratch[1:], n)
	e.Write(scratch[:])
	return headSize
}
