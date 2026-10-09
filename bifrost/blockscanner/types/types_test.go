package types

import (
	"errors"
	"fmt"
	"testing"
)

func TestRewindRequiredErrorCov(t *testing.T) {
	err := RewindRequiredError{NextHeight: 42}
	if got, want := err.Error(), "block scanner rewind required: next height 42"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	var target RewindRequiredError
	wrapped := fmt.Errorf("wrap: %w", err)
	if !errors.As(wrapped, &target) || target.NextHeight != 42 {
		t.Fatalf("errors.As failed: %+v", target)
	}
}

func TestScannerRecoveryRequiredErrorCov(t *testing.T) {
	err := ScannerRecoveryRequiredError{CurrentHeight: 10, NextHeight: 3, Reason: "boom"}
	want := "block scanner operator recovery required at height 10 (chain client next height 3): boom"
	if got := err.Error(); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if MaxCursorRewindBlocks != 730 {
		t.Fatalf("unexpected MaxCursorRewindBlocks %d", MaxCursorRewindBlocks)
	}
}
