package types

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

func buildEvent(phase byte, extrinsicIdx uint32, palletIdx, eventIdx byte, payload []byte, topicCount int) []byte {
	data := []byte{phase}
	if phase == 0 {
		index := make([]byte, 4)
		binary.LittleEndian.PutUint32(index, extrinsicIdx)
		data = append(data, index...)
	}
	data = append(data, palletIdx, eventIdx)
	data = append(data, payload...)
	data = append(data, EncodeCompact(uint64(topicCount))...)
	data = append(data, make([]byte, topicCount*32)...)
	return data
}

func testDispatchInfo() []byte {
	data := EncodeCompact(261_973_000)
	data = append(data, EncodeCompact(1_493)...)
	return append(data, 0, 0) // DispatchClass::Normal, Pays::Yes
}

func testMetadata(t *testing.T, path string) *Metadata {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read metadata fixture: %v", err)
	}
	metadata, err := ParseMetadata(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse metadata fixture: %v", err)
	}
	return metadata
}

func fillBytes(size int) []byte {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(0x40 + i%0x30)
	}
	return data
}

func assetHubEvents(actualFee uint64) []byte {
	feePayload := make([]byte, 32+16+16)
	binary.LittleEndian.PutUint64(feePayload[32:40], actualFee)

	var events []byte
	events = append(events, buildEvent(2, 0, 0, 2, fillBytes(32), 1)...)           // System::CodeUpdated
	events = append(events, buildEvent(0, 0, 10, 17, fillBytes(16), 0)...)         // Balances::Issued
	events = append(events, buildEvent(2, 0, 10, 24, make([]byte, 1+32+16), 0)...) // Balances::Held with RuntimeHoldReason
	events = append(events, buildEvent(0, 0, 10, 2, fillBytes(80), 0)...)          // Balances::Transfer
	events = append(events, buildEvent(0, 0, 11, 0, feePayload, 0)...)             // TransactionFeePaid
	events = append(events, buildEvent(0, 0, 0, 0, testDispatchInfo(), 0)...)
	return append(EncodeCompact(6), events...)
}

func taoEvents(actualFee uint64) []byte {
	feePayload := make([]byte, 32+8+8)
	binary.LittleEndian.PutUint64(feePayload[32:40], actualFee)

	var events []byte
	events = append(events, buildEvent(2, 0, 4, 1, nil, 0)...) // Grandpa::Paused, previously an unknown pallet
	events = append(events, buildEvent(0, 0, 6, 0, feePayload, 0)...)
	events = append(events, buildEvent(0, 0, 0, 0, testDispatchInfo(), 0)...)
	failedPayload := append([]byte{0}, testDispatchInfo()...) // DispatchError::Other
	events = append(events, buildEvent(0, 1, 0, 1, failedPayload, 0)...)
	return append(EncodeCompact(4), events...)
}

func TestParseExtrinsicResultsAcrossRuntimes(t *testing.T) {
	// Parse both metadata blobs before decoding either stream. This verifies
	// that their runtime type definitions remain isolated in scale.go's global
	// registry and can be used side by side.
	assetHub := testMetadata(t, "testdata/metadata-dot-assethub-2003001.hex")
	tao := testMetadata(t, "testdata/metadata-tao-mainnet-401.hex")

	tests := []struct {
		name     string
		data     []byte
		config   EventPalletConfig
		want     map[uint32]bool
		feeIndex uint32
		wantFee  uint64
	}{
		{
			name:     "Asset Hub",
			data:     assetHubEvents(20_000_000),
			config:   EventPalletConfig{Metadata: assetHub, SystemPalletIndex: 0, TransactionPaymentPalletIndex: 11},
			want:     map[uint32]bool{0: true},
			feeIndex: 0,
			wantFee:  20_000_000,
		},
		{
			name:     "TAO",
			data:     taoEvents(125_000),
			config:   EventPalletConfig{Metadata: tao, SystemPalletIndex: 0, TransactionPaymentPalletIndex: 6},
			want:     map[uint32]bool{0: true, 1: false},
			feeIndex: 0,
			wantFee:  125_000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, fees, err := ParseExtrinsicResults(tt.data, tt.config)
			if err != nil {
				t.Fatalf("parse events: %v", err)
			}
			if len(results) != len(tt.want) {
				t.Fatalf("result count = %d, want %d", len(results), len(tt.want))
			}
			for index, want := range tt.want {
				if got, found := results[index]; !found || got != want {
					t.Errorf("result[%d] = %v (found %v), want %v", index, got, found, want)
				}
			}
			if got := fees[tt.feeIndex]; got != tt.wantFee {
				t.Errorf("fee[%d] = %d, want %d", tt.feeIndex, got, tt.wantFee)
			}
		})
	}
}

func TestDecodeEventsUsesMetadataNamesAndTypes(t *testing.T) {
	metadata := testMetadata(t, "testdata/metadata-dot-assethub-2003001.hex")
	events, err := DecodeEvents(assetHubEvents(20_000_000), metadata)
	if err != nil {
		t.Fatalf("decode events: %v", err)
	}
	if len(events) != 6 {
		t.Fatalf("event count = %d, want 6", len(events))
	}

	checks := []struct {
		index  int
		pallet string
		name   string
	}{
		{index: 0, pallet: "System", name: "CodeUpdated"},
		{index: 1, pallet: "Balances", name: "Issued"},
		{index: 2, pallet: "Balances", name: "Held"},
		{index: 3, pallet: "Balances", name: "Transfer"},
		{index: 4, pallet: "TransactionPayment", name: "TransactionFeePaid"},
		{index: 5, pallet: "System", name: "ExtrinsicSuccess"},
	}
	for _, check := range checks {
		got := events[check.index]
		if got.Pallet != check.pallet || got.Name != check.name {
			t.Errorf("event %d = %s:%s, want %s:%s", check.index, got.Pallet, got.Name, check.pallet, check.name)
		}
	}
	if len(events[0].Topics) != 1 {
		t.Errorf("CodeUpdated topics = %d, want 1", len(events[0].Topics))
	}
	if got := events[2].Params[0].Type; got != "asset_hub_polkadot_runtime:RuntimeHoldReason" {
		t.Errorf("Held reason type = %q, want RuntimeHoldReason", got)
	}
	if got := events[3].Params[2].Type; got != "U128" {
		t.Errorf("Transfer amount type = %q, want U128", got)
	}
}

func TestDecodeEventsRejectsMalformedData(t *testing.T) {
	metadata := testMetadata(t, "testdata/metadata-dot-assethub-2003001.hex")
	valid := assetHubEvents(20_000_000)
	tests := []struct {
		name string
		data []byte
	}{
		{name: "truncated", data: valid[:len(valid)-1]},
		{name: "trailing byte", data: append(append([]byte(nil), valid...), 0)},
		{name: "invalid phase", data: append(EncodeCompact(1), 3, 0, 0, 0)},
		{name: "unknown event", data: append(EncodeCompact(1), buildEvent(2, 0, 255, 255, nil, 0)...)},
		{name: "impossible count", data: append(EncodeCompact(100), 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeEvents(tt.data, metadata); err == nil {
				t.Fatal("expected decode error")
			}
		})
	}
}

func TestParseExtrinsicResultsRequiresMetadata(t *testing.T) {
	if _, _, err := ParseExtrinsicResults([]byte{0}, EventPalletConfig{}); err == nil {
		t.Fatal("expected missing metadata error")
	}
}
