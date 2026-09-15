package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

func TestFormatMempoolMemolessReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		reference uint64
		count     int64
		expected  string
		wantErr   bool
	}{
		{name: "zero padded", reference: 7, count: 99_999, expected: "r:00007"},
		{name: "upper bound", reference: 99_999, count: 99_999, expected: "r:99999"},
		{name: "single digit range", reference: 3, count: 9, expected: "r:3"},
		{name: "reference exceeds range", reference: 10, count: 9, wantErr: true},
		{name: "invalid range", reference: 0, count: 0, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			actual, err := formatMempoolMemolessReference(test.reference, test.count)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if actual != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, actual)
			}
		})
	}
}

func TestMempoolMemolessQueueScanners(t *testing.T) {
	t.Parallel()

	txOut := openapi.TxOutItem{}
	txOut.SetInHash("0xabc")
	staged := openapi.StagedOutboundResponse{StagedOutboundItems: []openapi.StagedOutboundItem{{TxOutItem: &txOut}}}
	if !mempoolMemolessStagedContains(staged, "ABC") || !mempoolMemolessOutboundsContain([]openapi.TxOutItem{txOut}, "ABC") {
		t.Fatal("matching queue items were not found")
	}
	if mempoolMemolessStagedContains(staged, "DEF") || mempoolMemolessOutboundsContain([]openapi.TxOutItem{txOut}, "DEF") {
		t.Fatal("unrelated queue items matched")
	}
}

func TestValidateMempoolMemolessExclusion(t *testing.T) {
	t.Parallel()

	baselineUsage := sdkmath.NewUint(10)
	baselineRecipient := sdkmath.NewUint(20)
	clean := mempoolMemolessExclusionSnapshot{
		usage:     baselineUsage,
		recipient: baselineRecipient,
	}
	if err := validateMempoolMemolessExclusion(clean, baselineUsage, baselineRecipient); err != nil {
		t.Fatalf("clean exclusion snapshot failed: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*mempoolMemolessExclusionSnapshot)
	}{
		{name: "usage", mutate: func(s *mempoolMemolessExclusionSnapshot) { s.usage = sdkmath.NewUint(11) }},
		{name: "staged", mutate: func(s *mempoolMemolessExclusionSnapshot) { s.staged = true }},
		{name: "outbound", mutate: func(s *mempoolMemolessExclusionSnapshot) { s.outbound = true }},
		{name: "queued", mutate: func(s *mempoolMemolessExclusionSnapshot) { s.queued = true }},
		{name: "recipient", mutate: func(s *mempoolMemolessExclusionSnapshot) { s.recipient = sdkmath.NewUint(21) }},
		{name: "actions", mutate: func(s *mempoolMemolessExclusionSnapshot) { s.actions = 1 }},
		{name: "out txs", mutate: func(s *mempoolMemolessExclusionSnapshot) { s.outTxs = 1 }},
		{name: "reference", mutate: func(s *mempoolMemolessExclusionSnapshot) { s.referenceUses = 1 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot := clean
			test.mutate(&snapshot)
			if err := validateMempoolMemolessExclusion(snapshot, baselineUsage, baselineRecipient); err == nil {
				t.Fatal("invalid exclusion snapshot was accepted")
			}
		})
	}
}

func TestMempoolMemolessMissedWindowClassification(t *testing.T) {
	t.Parallel()

	missedErr := &mempoolMemolessMissedWindow{
		err: fmt.Errorf("POL inbound finalized before %d pre-finality exclusion samples (got %d)",
			mempoolMemolessPreFinalitySamples, 0),
	}

	var missed *mempoolMemolessMissedWindow
	if !errors.As(fmt.Errorf("attempt: %w", missedErr), &missed) {
		t.Fatal("wrapped missed window was not recognized")
	}
	if !strings.Contains(missedErr.Error(), "got 0") {
		t.Fatalf("missed window lost its message: %v", missedErr)
	}

	// Every other pre-finality failure is a real exclusion breach, not a race.
	breach := fmt.Errorf("pre-finality exclusion sample at height 10: %w",
		fmt.Errorf("reference swap reserved mempool TOR"))
	if errors.As(breach, &missed) {
		t.Fatalf("exclusion breach classified as a missed window: %v", breach)
	}
}
