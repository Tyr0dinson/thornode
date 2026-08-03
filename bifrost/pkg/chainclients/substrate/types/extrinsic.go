package types

import (
	"encoding/hex"
	"fmt"
	"strings"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"golang.org/x/crypto/blake2b"
)

const (
	// ExtrinsicFormatVersion is the version byte for signed extrinsics.
	// Bit 7 = 1 (signed), bits 0-6 = 4 (version 4).
	ExtrinsicFormatVersion = 0x84

	// SignatureTypeEd25519 is the signature type byte for Ed25519.
	SignatureTypeEd25519 = 0x00

	// MultiAddressID is the MultiAddress variant for account ID (32 bytes).
	MultiAddressID = 0x00

	// MetadataModeDisabled is the CheckMetadataHash mode byte when metadata
	// hash verification is disabled. This is encoded both in the extrinsic
	// body (as part of the signed extensions) and in the signing payload.
	MetadataModeDisabled = 0x00
)

// Extrinsic represents a Substrate extrinsic.
type Extrinsic struct {
	// Signature fields (for signed extrinsics)
	Signer            []byte // 32-byte Ed25519 public key
	SignatureType     byte
	Signature         []byte // 64 bytes for Ed25519/Sr25519, 65 bytes for ECDSA
	Era               []byte // mortal or immortal era
	Nonce             uint64
	Tip               uint64
	MetadataMode      byte // CheckMetadataHash mode (0x00 = disabled)
	CheckMetadataHash bool // if false, MetadataMode is not encoded
	// ChargeAssetTxPayment marks runtimes (e.g. Polkadot Asset Hub) whose fee
	// extension is ChargeAssetTxPayment instead of ChargeTransactionPayment.
	// It encodes Option<AssetId> after the tip — always None (0x00) here,
	// since the vault only ever pays fees in the native asset.
	ChargeAssetTxPayment bool
	CallData             []byte
}

// ErrUnsignedExtrinsic indicates the version byte's bit 7 is clear (the
// extrinsic is an inherent or otherwise unsigned).
var ErrUnsignedExtrinsic = fmt.Errorf("unsigned extrinsic")

// BalanceTransferKeepAlive creates the call data for a Balances::transfer_keep_alive call.
// palletIndex: the pallet index for Balances
// callIndex: the call index for transfer_keep_alive within Balances
// dest: 32-byte destination account ID
// amount: transfer amount in native units (rao for TAO, planck for DOT)
func BalanceTransferKeepAlive(palletIndex, callIndex byte, dest []byte, amount cosmos.Uint) []byte {
	data := []byte{palletIndex, callIndex}
	// MultiAddress::Id variant
	data = append(data, MultiAddressID)
	// 32-byte account ID
	data = append(data, dest...)
	// Compact-encoded amount
	data = append(data, EncodeCompactUint(amount)...)
	return data
}

// SystemRemark creates the call data for a System::remark call.
// palletIndex: the pallet index for System (typically 0)
// callIndex: the call index for remark within System
// remark: the remark bytes (memo)
func SystemRemark(palletIndex, callIndex byte, remark []byte) []byte {
	data := []byte{palletIndex, callIndex}
	// Length-prefixed byte vector (compact-encoded length + data)
	data = append(data, EncodeCompact(uint64(len(remark)))...)
	data = append(data, remark...)
	return data
}

// BatchAll creates the call data for a Utility::batch_all call.
// This batches multiple calls atomically.
func BatchAll(palletIndex, callIndex byte, calls [][]byte) []byte {
	data := []byte{palletIndex, callIndex}
	// Vector of calls: compact-encoded count + concatenated calls
	data = append(data, EncodeCompact(uint64(len(calls)))...)
	for _, call := range calls {
		data = append(data, call...)
	}
	return data
}

// SigningPayload constructs the payload to be signed for a Substrate extrinsic.
// The payload follows the standard Substrate TransactionExtensions order
// (for runtimes including CheckMetadataHash per RFC-0078):
//
//	Encoded extensions: era | compact(nonce) | compact(tip) | [asset_id] | metadata_mode
//	Implicit data:      spec_version | tx_version | genesis_hash | block_hash | Option<Hash>::None
//
// The metadata_mode byte (0x00 = disabled) is from CheckMetadataHash.
// When mode is disabled, the implicit Option<Hash> encodes as None (0x00).
// On ChargeAssetTxPayment runtimes (Asset Hub) the fee extension encodes
// Option<AssetId> directly after the tip — always None (0x00) for native-asset
// fees; its implicit is empty.
func SigningPayload(
	callData, era []byte,
	nonce, tip uint64,
	checkMetadataHash bool,
	metadataMode byte,
	chargeAssetTxPayment bool,
	specVersion, txVersion uint32,
	genesisHash, blockHash []byte,
) ([]byte, error) {
	if checkMetadataHash && metadataMode != MetadataModeDisabled {
		return nil, fmt.Errorf("metadata mode not implemented")
	}
	payload := make([]byte, 0, 256)
	// Call data
	payload = append(payload, callData...)
	// Encoded extensions
	payload = append(payload, era...)
	payload = append(payload, EncodeCompact(nonce)...)
	payload = append(payload, EncodeCompact(tip)...)
	// ChargeAssetTxPayment: Option<AssetId>::None (pay fees in native asset)
	if chargeAssetTxPayment {
		payload = append(payload, 0x00)
	}
	// Metadata hash
	if checkMetadataHash {
		payload = append(payload, metadataMode)
	}
	// Implicit data
	payload = append(payload, EncodeU32LE(specVersion)...)
	payload = append(payload, EncodeU32LE(txVersion)...)
	payload = append(payload, genesisHash...)
	payload = append(payload, blockHash...)
	// CheckMetadataHash implicit: Option<[u8;32]>::None when mode == 0
	if checkMetadataHash {
		payload = append(payload, 0x00) // Option::None
	}
	return payload, nil
}

// EncodeSignedExtrinsic assembles a signed extrinsic from its components.
func EncodeSignedExtrinsic(ext *Extrinsic) []byte {
	// Build the extrinsic body (without length prefix)
	body := make([]byte, 0, 256)

	// Version byte (0x84 = signed, version 4)
	body = append(body, ExtrinsicFormatVersion)

	// Signer: MultiAddress::Id
	body = append(body, MultiAddressID)
	body = append(body, ext.Signer...)

	// Signature type + signature
	body = append(body, ext.SignatureType)
	body = append(body, ext.Signature...)

	// Era
	body = append(body, ext.Era...)

	// Nonce (compact)
	body = append(body, EncodeCompact(ext.Nonce)...)

	// Tip (compact)
	body = append(body, EncodeCompact(ext.Tip)...)

	// ChargeAssetTxPayment: Option<AssetId>::None (pay fees in native asset)
	if ext.ChargeAssetTxPayment {
		body = append(body, 0x00)
	}

	// CheckMetadataHash mode byte
	if ext.CheckMetadataHash {
		body = append(body, ext.MetadataMode)
	}

	// Call data
	body = append(body, ext.CallData...)

	// Length-prefix the entire extrinsic
	length := EncodeCompact(uint64(len(body)))
	result := make([]byte, 0, len(length)+len(body))
	result = append(result, length...)
	result = append(result, body...)

	return result
}

// ImmortalEra returns the immortal era encoding (single byte 0x00).
func ImmortalEra() []byte {
	return []byte{0x00}
}

// MortalEra encodes a mortal era given the period and current block number.
// Period should be a power of 2 between 4 and 65536.
func MortalEra(period uint64, currentBlock uint64) []byte {
	// Find the exponent (period = 2^(exponent+1), min 1, max 15)
	calPeriod := uint64(4) // minimum period
	for calPeriod < period && calPeriod < 65536 {
		calPeriod *= 2
	}

	phase := currentBlock % calPeriod
	quantizeFactor := calPeriod >> 12
	if quantizeFactor < 1 {
		quantizeFactor = 1
	}
	quantizedPhase := (phase / quantizeFactor) * quantizeFactor

	// Calculate encoded value: the low nibble is trailing_zeros(calPeriod) - 1,
	// clamped to [1, 15]. Substrate decodes as period = 2 << (low_nibble).
	var periodExp uint64
	for p := calPeriod; p > 1; p >>= 1 {
		periodExp++
	}
	// periodExp = trailing_zeros(calPeriod) = log2(calPeriod).
	// Substrate encoding uses (trailing_zeros - 1) as the low nibble.
	periodExp-- // now trailing_zeros - 1

	encoded := uint16(periodExp) | uint16(quantizedPhase/quantizeFactor)<<4
	return []byte{byte(encoded), byte(encoded >> 8)}
}

// HexEncode returns the hex-encoded string with 0x prefix.
func HexEncode(data []byte) string {
	return "0x" + hex.EncodeToString(data)
}

// ExtrinsicHash computes the Blake2b-256 hash of a raw SCALE-encoded extrinsic,
// returning the canonical Substrate transaction hash as a 0x-prefixed hex string.
func ExtrinsicHash(raw []byte) string {
	hash := blake2b.Sum256(raw)
	return "0x" + hex.EncodeToString(hash[:])
}

// HexDecode decodes a hex string (with or without 0x prefix).
func HexDecode(s string) ([]byte, error) {
	s = strings.TrimPrefix(s, "0x")
	data, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("failed to decode hex: %w", err)
	}
	return data, nil
}

// HexEncodeHead returns the first n bytes of data as a hex string for debug logging.
func HexEncodeHead(data []byte, n int) string {
	if len(data) <= n {
		return hex.EncodeToString(data)
	}
	return hex.EncodeToString(data[:n])
}

func ParseSignedExtrinsic(data []byte, checkMetadataHash, chargeAssetTxPayment bool) (*Extrinsic, error) {
	// Decode the length prefix
	_, consumed, err := DecodeCompact(data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode extrinsic length: %w", err)
	}
	body := data[consumed:]

	if len(body) == 0 {
		return nil, fmt.Errorf("empty extrinsic body")
	}

	// Check if this is a signed extrinsic (bit 7 set) using format version 4
	versionByte := body[0]
	if versionByte&0x80 == 0 {
		return nil, ErrUnsignedExtrinsic
	}
	if versionByte&0x7F != 4 {
		return nil, fmt.Errorf("unsupported extrinsic version: %d", versionByte&0x7F)
	}

	// Parse signed extrinsic (version 4)
	pos := 1

	// Signer: MultiAddress type
	if pos >= len(body) {
		return nil, fmt.Errorf("extrinsic too short for signer type")
	}
	signerType := body[pos]
	pos++

	if signerType != MultiAddressID {
		return nil, fmt.Errorf("unsupported signer type: %d", signerType)
	}

	// 32-byte signer account ID
	if pos+32 > len(body) {
		return nil, fmt.Errorf("extrinsic too short for signer")
	}
	signerPubkey := body[pos : pos+32]
	pos += 32

	// Signature type: Ed25519 (0x00) and Sr25519 (0x01) use 64-byte signatures,
	// ECDSA (0x02) uses 65 bytes. Reject unknown types to prevent parser misalignment.
	if pos >= len(body) {
		return nil, fmt.Errorf("extrinsic too short for signature type")
	}
	sigType := body[pos]
	pos++
	sigLen := 64
	if sigType == 0x02 { // ECDSA
		sigLen = 65
	} else if sigType > 0x02 {
		return nil, fmt.Errorf("unsupported signature type: %d", sigType)
	}

	if pos+sigLen > len(body) {
		return nil, fmt.Errorf("extrinsic too short for signature")
	}
	signature := body[pos : pos+sigLen]
	pos += sigLen

	// Era
	if pos >= len(body) {
		return nil, fmt.Errorf("extrinsic too short for era")
	}
	eraStart := pos
	if body[pos] == 0x00 {
		pos++ // immortal era
	} else {
		if pos+2 > len(body) {
			return nil, fmt.Errorf("extrinsic too short for mortal era")
		}
		pos += 2 // mortal era
	}
	era := body[eraStart:pos]

	// Nonce (compact)
	nonceVal, nonceLen, err := DecodeCompact(body[pos:])
	if err != nil {
		return nil, fmt.Errorf("failed to decode nonce: %w", err)
	}
	if !nonceVal.BigInt().IsUint64() {
		return nil, fmt.Errorf("nonce exceeds uint64")
	}
	pos += nonceLen

	// Tip (compact)
	tipVal, tipLen, err := DecodeCompact(body[pos:])
	if err != nil {
		return nil, fmt.Errorf("failed to decode tip: %w", err)
	}
	if !tipVal.BigInt().IsUint64() {
		return nil, fmt.Errorf("tip exceeds uint64")
	}
	pos += tipLen

	// ChargeAssetTxPayment (Asset Hub): Option<AssetId> follows the tip.
	// Only None (0x00, native-asset fees) is supported — a Some variant is
	// followed by a variable-length AssetId which cannot be skipped safely,
	// and the vault never signs one.
	if chargeAssetTxPayment {
		if pos >= len(body) {
			return nil, fmt.Errorf("extrinsic too short for asset id option byte")
		}
		if body[pos] != 0x00 {
			return nil, fmt.Errorf("unsupported non-native fee asset id option: %d", body[pos])
		}
		pos++ // skip Option<AssetId>::None byte
	}

	// CheckMetadataHash mode byte (RFC-0078, present in DOT/KSM/TAO runtimes).
	// Chains without this extension would not have this byte
	var metadataMode byte
	if checkMetadataHash {
		if pos >= len(body) {
			return nil, fmt.Errorf("extrinsic too short for metadata mode byte")
		}
		metadataMode = body[pos]
		pos++ // skip metadata mode byte
	}

	// The remainder is call data
	callData := body[pos:]

	return &Extrinsic{
		Signer:               signerPubkey,
		Signature:            signature,
		SignatureType:        sigType,
		Era:                  era,
		Nonce:                nonceVal.Uint64(),
		Tip:                  tipVal.Uint64(),
		MetadataMode:         metadataMode,
		CheckMetadataHash:    checkMetadataHash,
		ChargeAssetTxPayment: chargeAssetTxPayment,
		CallData:             callData,
	}, nil
}
