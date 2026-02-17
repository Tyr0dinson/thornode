package types

import (
	"bytes"
	"testing"
)

func TestHexEncodeDecodeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"single byte", []byte{0x42}},
		{"multiple bytes", []byte{0xde, 0xad, 0xbe, 0xef}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := HexEncode(tt.data)
			decoded, err := HexDecode(encoded)
			if err != nil {
				t.Fatalf("HexDecode: %v", err)
			}
			if !bytes.Equal(decoded, tt.data) {
				t.Errorf("round trip failed: got %x, want %x", decoded, tt.data)
			}
		})
	}
}

func TestHexDecodeStripsPrefix(t *testing.T) {
	data, err := HexDecode("0xdeadbeef")
	if err != nil {
		t.Fatalf("HexDecode: %v", err)
	}
	expected := []byte{0xde, 0xad, 0xbe, 0xef}
	if !bytes.Equal(data, expected) {
		t.Errorf("got %x, want %x", data, expected)
	}
}

func TestHexDecodeWithoutPrefix(t *testing.T) {
	data, err := HexDecode("deadbeef")
	if err != nil {
		t.Fatalf("HexDecode: %v", err)
	}
	expected := []byte{0xde, 0xad, 0xbe, 0xef}
	if !bytes.Equal(data, expected) {
		t.Errorf("got %x, want %x", data, expected)
	}
}

func TestHexDecodeInvalid(t *testing.T) {
	_, err := HexDecode("0xZZZZ")
	if err == nil {
		t.Error("expected error for invalid hex")
	}
}

func TestImmortalEra(t *testing.T) {
	era := ImmortalEra()
	if len(era) != 1 || era[0] != 0x00 {
		t.Errorf("ImmortalEra: got %x, want [00]", era)
	}
}

func TestMortalEra(t *testing.T) {
	tests := []struct {
		name     string
		period   uint64
		block    uint64
		expected []byte
	}{
		// Substrate encodes the low nibble as trailing_zeros(period) - 1.
		// Decoding: period = 2 << low_nibble, phase = (encoded >> 4) * quantize_factor.
		{"period64_block0", 64, 0, []byte{0x05, 0x00}},
		{"period64_block42", 64, 42, []byte{0xa5, 0x02}},
		{"period64_block63", 64, 63, []byte{0xf5, 0x03}},
		{"period64_block64", 64, 64, []byte{0x05, 0x00}}, // wraps around
		{"period4_block0", 4, 0, []byte{0x01, 0x00}},
		{"period4_block3", 4, 3, []byte{0x31, 0x00}},
		// MortalEra(256, 100): trailing_zeros(256)=8, low nibble=7
		// encoded = 7 | (100<<4) = 0x0647
		{"period256_block100", 256, 100, []byte{0x47, 0x06}},
		// MortalEra(4096, 1000): trailing_zeros(4096)=12, low nibble=11
		// encoded = 11 | (1000<<4) = 0x3E8B
		{"period4096_block1000", 4096, 1000, []byte{0x8b, 0x3e}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			era := MortalEra(tt.period, tt.block)
			if !bytes.Equal(era, tt.expected) {
				t.Errorf("MortalEra(%d, %d): got %x, want %x", tt.period, tt.block, era, tt.expected)
			}
		})
	}
}

// TestMortalEraSubstrateDecodeRoundTrip verifies the encoded mortal era can be
// decoded back to the correct period and phase using Substrate's decode formula:
//
//	period = 2 << (encoded & 0x0F)
//	quantize = max(period >> 12, 1)
//	phase = (encoded >> 4) * quantize
func TestMortalEraSubstrateDecodeRoundTrip(t *testing.T) {
	tests := []struct {
		period uint64
		block  uint64
		wantP  uint64 // expected decoded period
		wantPh uint64 // expected decoded phase
	}{
		{64, 0, 64, 0},
		{64, 42, 64, 42},
		{64, 63, 64, 63},
		{64, 64, 64, 0}, // wraps
		{4, 0, 4, 0},
		{4, 3, 4, 3},
		{256, 100, 256, 100},
		{4096, 1000, 4096, 1000},
	}
	for _, tt := range tests {
		era := MortalEra(tt.period, tt.block)
		encoded := uint16(era[0]) | uint16(era[1])<<8

		// Substrate decode formula
		period := uint64(2) << (encoded & 0x0F)
		quantize := period >> 12
		if quantize < 1 {
			quantize = 1
		}
		phase := uint64(encoded>>4) * quantize

		if period != tt.wantP {
			t.Errorf("MortalEra(%d,%d) decode period: got %d, want %d (encoded=%04x)", tt.period, tt.block, period, tt.wantP, encoded)
		}
		if phase != tt.wantPh {
			t.Errorf("MortalEra(%d,%d) decode phase: got %d, want %d (encoded=%04x)", tt.period, tt.block, phase, tt.wantPh, encoded)
		}
		if phase >= period {
			t.Errorf("MortalEra(%d,%d) invalid: phase(%d) >= period(%d) (encoded=%04x)", tt.period, tt.block, phase, period, encoded)
		}
	}
}

func TestMortalEraIsTwoBytes(t *testing.T) {
	era := MortalEra(64, 10)
	if len(era) != 2 {
		t.Errorf("mortal era should be 2 bytes, got %d", len(era))
	}
}

func TestExtrinsicHashDeterministic(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03}
	hash1 := ExtrinsicHash(data)
	hash2 := ExtrinsicHash(data)
	if hash1 != hash2 {
		t.Error("ExtrinsicHash should be deterministic")
	}
	// 0x prefix + 64 hex chars
	if len(hash1) != 66 {
		t.Errorf("hash length: got %d, want 66", len(hash1))
	}
	if hash1[:2] != "0x" {
		t.Error("hash should start with 0x")
	}
}

func TestExtrinsicHashDifferentInput(t *testing.T) {
	hash1 := ExtrinsicHash([]byte{0x01})
	hash2 := ExtrinsicHash([]byte{0x02})
	if hash1 == hash2 {
		t.Error("different inputs should produce different hashes")
	}
}

func TestBalanceTransferKeepAlive(t *testing.T) {
	dest := make([]byte, 32)
	dest[0] = 0xAA
	call := BalanceTransferKeepAlive(5, 3, dest, 1_000_000)

	// pallet index
	if call[0] != 5 {
		t.Errorf("pallet index: got %d, want 5", call[0])
	}
	// call index
	if call[1] != 3 {
		t.Errorf("call index: got %d, want 3", call[1])
	}
	// MultiAddress::Id variant
	if call[2] != MultiAddressID {
		t.Errorf("address type: got %d, want %d", call[2], MultiAddressID)
	}
	// first byte of destination
	if call[3] != 0xAA {
		t.Errorf("first byte of dest: got 0x%02x, want 0xAA", call[3])
	}
	// decode compact amount at the end
	amountBytes := call[3+32:]
	decoded, _, err := DecodeCompact(amountBytes)
	if err != nil {
		t.Fatalf("DecodeCompact: %v", err)
	}
	if decoded != 1_000_000 {
		t.Errorf("amount: got %d, want 1000000", decoded)
	}
}

func TestSystemRemark(t *testing.T) {
	memo := []byte("SWAP:BTC.BTC:bc1qxyz")
	call := SystemRemark(0, 0, memo)

	if call[0] != 0 || call[1] != 0 {
		t.Errorf("pallet/call: got %d/%d, want 0/0", call[0], call[1])
	}

	// decode compact length
	remarkLen, n, err := DecodeCompact(call[2:])
	if err != nil {
		t.Fatalf("DecodeCompact: %v", err)
	}
	if remarkLen != uint64(len(memo)) {
		t.Errorf("remark length: got %d, want %d", remarkLen, len(memo))
	}
	actualMemo := string(call[2+n:])
	if actualMemo != string(memo) {
		t.Errorf("memo: got %q, want %q", actualMemo, string(memo))
	}
}

func TestSystemRemarkEmpty(t *testing.T) {
	call := SystemRemark(0, 1, []byte{})
	// pallet(1) + call(1) + compact_len(1 for 0)
	if len(call) != 3 {
		t.Errorf("empty remark call length: got %d, want 3", len(call))
	}
	remarkLen, _, err := DecodeCompact(call[2:])
	if err != nil {
		t.Fatalf("DecodeCompact: %v", err)
	}
	if remarkLen != 0 {
		t.Errorf("remark length: got %d, want 0", remarkLen)
	}
}

func TestBatchAll(t *testing.T) {
	call1 := []byte{5, 3, 0x00}
	call2 := []byte{0, 0, 0x04}
	batch := BatchAll(11, 2, [][]byte{call1, call2})

	if batch[0] != 11 || batch[1] != 2 {
		t.Errorf("pallet/call: got %d/%d, want 11/2", batch[0], batch[1])
	}

	// decode compact count
	count, n, err := DecodeCompact(batch[2:])
	if err != nil {
		t.Fatalf("DecodeCompact: %v", err)
	}
	if count != 2 {
		t.Errorf("call count: got %d, want 2", count)
	}

	remaining := batch[2+n:]
	expected := append(call1, call2...)
	if !bytes.Equal(remaining, expected) {
		t.Errorf("batch calls: got %x, want %x", remaining, expected)
	}
}

func TestEncodeSignedExtrinsicFormat(t *testing.T) {
	signer := make([]byte, 32)
	signer[0] = 0x01
	sig := make([]byte, 64)
	sig[0] = 0xFF

	ext := &Extrinsic{
		Signer:        signer,
		SignatureType: SignatureTypeEd25519,
		Signature:     sig,
		Era:           MortalEra(64, 10),
		Nonce:         5,
		Tip:           0,
		MetadataMode:  MetadataModeDisabled,
		CallData:      []byte{5, 3, 0x00},
	}

	encoded := EncodeSignedExtrinsic(ext)

	// should start with compact length prefix
	_, consumed, err := DecodeCompact(encoded)
	if err != nil {
		t.Fatalf("length prefix: %v", err)
	}
	body := encoded[consumed:]

	// version byte 0x84 (signed, version 4)
	if body[0] != ExtrinsicFormatVersion {
		t.Errorf("version byte: got 0x%02x, want 0x%02x", body[0], ExtrinsicFormatVersion)
	}

	// MultiAddress::Id
	if body[1] != MultiAddressID {
		t.Errorf("address type: got 0x%02x, want 0x%02x", body[1], MultiAddressID)
	}

	// signer at offset 2
	if body[2] != 0x01 {
		t.Errorf("first signer byte: got 0x%02x, want 0x01", body[2])
	}

	// signature type at offset 2+32=34
	if body[34] != SignatureTypeEd25519 {
		t.Errorf("signature type: got 0x%02x, want 0x%02x", body[34], SignatureTypeEd25519)
	}

	// first byte of signature at offset 35
	if body[35] != 0xFF {
		t.Errorf("first signature byte: got 0x%02x, want 0xFF", body[35])
	}
}

func TestSigningPayloadContainsAllFields(t *testing.T) {
	callData := []byte{5, 3}
	era := MortalEra(64, 10)
	genesisHash := make([]byte, 32)
	genesisHash[0] = 0xAA
	blockHash := make([]byte, 32)
	blockHash[0] = 0xBB

	payload := SigningPayload(callData, era, 1, 0, MetadataModeDisabled, 100, 1, genesisHash, blockHash)

	// starts with call data
	if payload[0] != 5 || payload[1] != 3 {
		t.Error("payload should start with call data")
	}

	// ends with Option<Hash>::None (0x00) from CheckMetadataHash implicit
	if payload[len(payload)-1] != 0x00 {
		t.Error("payload should end with metadata hash None byte")
	}

	// blockHash is before the trailing None byte
	if payload[len(payload)-33] != 0xBB {
		t.Error("block hash should be before metadata hash None byte")
	}

	// genesisHash is before blockHash
	if payload[len(payload)-65] != 0xAA {
		t.Error("genesis hash should be before block hash")
	}
}
