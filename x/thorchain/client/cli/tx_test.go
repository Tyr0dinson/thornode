package cli

import (
	"testing"

	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

// validTxForExtract returns an openapi ObservedTx with only the required fields
// populated; every optional pointer field is left nil.
func validTxForExtract() openapi.ObservedTx {
	return openapi.ObservedTx{
		Tx: openapi.Tx{
			Id:          openapi.PtrString("0000000000000000000000000000000000000000000000000000000000000001"),
			Chain:       openapi.PtrString("ETH"),
			FromAddress: openapi.PtrString("0x90f2b1ae50e6018230e90a33f98c7844a0ab635a"),
			ToAddress:   openapi.PtrString("0x8d3f74b8a1f8d6e2c7f0e8b7a6d5c4b3a2918f7e"),
			Coins: []openapi.Coin{{
				Asset:  "ETH.ETH",
				Amount: "1000",
			}},
			// Memo intentionally nil.
		},
		// ObservedPubKey, ExternalObservedHeight, ExternalConfirmationDelayHeight
		// all intentionally nil.
	}
}

// TestExtractOpenAPIObservedTxOptionalFieldsAbsent is a regression test for the
// nil pointer dereference panic hit by `thornode tx thorchain observe-tx-ins
// --txids ...`. When an observation from the thornode API omitted any of the
// optional fields (Memo, ExternalObservedHeight, ExternalConfirmationDelayHeight,
// ObservedPubKey), extractOpenAPIObservedTx dereferenced the nil pointer and
// panicked. They must now fall back to their zero values.
func TestExtractOpenAPIObservedTxOptionalFieldsAbsent(t *testing.T) {
	got, err := extractOpenAPIObservedTx(validTxForExtract())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Tx.Memo != "" {
		t.Errorf("expected empty memo, got %q", got.Tx.Memo)
	}
	if got.BlockHeight != 0 {
		t.Errorf("expected zero block height, got %d", got.BlockHeight)
	}
	if got.FinaliseHeight != 0 {
		t.Errorf("expected zero finalise height, got %d", got.FinaliseHeight)
	}
	if !got.ObservedPubKey.IsEmpty() {
		t.Errorf("expected empty observed pub key, got %q", got.ObservedPubKey)
	}
}

// TestExtractOpenAPIObservedTxMissingRequiredField ensures a missing required
// field returns a clean error rather than panicking on a nil dereference.
func TestExtractOpenAPIObservedTxMissingRequiredField(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*openapi.ObservedTx)
	}{
		{"id", func(o *openapi.ObservedTx) { o.Tx.Id = nil }},
		{"chain", func(o *openapi.ObservedTx) { o.Tx.Chain = nil }},
		{"from_address", func(o *openapi.ObservedTx) { o.Tx.FromAddress = nil }},
		{"to_address", func(o *openapi.ObservedTx) { o.Tx.ToAddress = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			otx := validTxForExtract()
			tc.mutate(&otx)
			if _, err := extractOpenAPIObservedTx(otx); err == nil {
				t.Errorf("expected error for missing %s, got nil", tc.name)
			}
		})
	}
}
