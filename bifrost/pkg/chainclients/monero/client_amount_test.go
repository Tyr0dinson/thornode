package monero

import (
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestXMRThorchainAmountToAtomicBoundary(t *testing.T) {
	t.Parallel()

	maxThorAmount := ^uint64(0) / moneroAtomicToThorchain
	atomic, err := xmrThorchainAmountToAtomic(cosmos.NewUint(maxThorAmount))
	if err != nil {
		t.Fatalf("unexpected conversion error: %v", err)
	}

	expected := maxThorAmount * moneroAtomicToThorchain
	if atomic != expected {
		t.Fatalf("expected atomic amount %d, got %d", expected, atomic)
	}
}

func TestXMRThorchainAmountToAtomicOverflow(t *testing.T) {
	t.Parallel()

	maxThorAmount := ^uint64(0) / moneroAtomicToThorchain
	_, err := xmrThorchainAmountToAtomic(cosmos.NewUint(maxThorAmount + 1))
	if err == nil {
		t.Fatal("expected overflow conversion error")
	}
	if !strings.Contains(err.Error(), "overflows uint64") {
		t.Fatalf("expected overflow error, got %v", err)
	}
}

func TestXMRThorchainAmountToAtomicNil(t *testing.T) {
	t.Parallel()

	var amount cosmos.Uint
	_, err := xmrThorchainAmountToAtomic(amount)
	if err == nil {
		t.Fatal("expected nil amount conversion error")
	}
	if !strings.Contains(err.Error(), "is nil") {
		t.Fatalf("expected nil amount error, got %v", err)
	}
}

func TestXMRCoinbaseThorchainConversion(t *testing.T) {
	t.Parallel()

	expected := uint64(defaultCoinbaseValue / moneroAtomicToThorchain)
	if expected == 0 {
		expected = 1
	}
	if got := xmrCoinbaseThorchain(); got != expected {
		t.Fatalf("expected coinbase thorchain %d, got %d", expected, got)
	}
}

func TestXMRConfirmationRequiredUsesThorchainUnits(t *testing.T) {
	t.Parallel()

	coinbaseThorchain := xmrCoinbaseThorchain()
	if coinbaseThorchain == 0 {
		t.Fatal("coinbase thorchain must be non-zero")
	}
	confValue := cosmos.NewUint(coinbaseThorchain)

	if got := xmrConfirmationRequired(cosmos.NewUint(coinbaseThorchain), confValue); got != 1 {
		t.Fatalf("expected one confirmation at coinbase-sized value, got %d", got)
	}
	if got := xmrConfirmationRequired(cosmos.NewUint(coinbaseThorchain-1), confValue); got != 1 {
		t.Fatalf("expected one confirmation below coinbase-sized value after rounding up, got %d", got)
	}
	if got := xmrConfirmationRequired(cosmos.NewUint((coinbaseThorchain*3)/2), confValue); got != 2 {
		t.Fatalf("expected non-divisible confirmation counts to round up, got %d", got)
	}
	if got := xmrConfirmationRequired(cosmos.ZeroUint(), confValue); got != 0 {
		t.Fatalf("expected zero confirmations for zero value, got %d", got)
	}
	if got := xmrConfirmationRequired(cosmos.NewUint(coinbaseThorchain), cosmos.ZeroUint()); got != 0 {
		t.Fatalf("expected zero confirmations when confValue is zero, got %d", got)
	}
}

func TestXMRConfirmationRequiredWithConfMultiplier(t *testing.T) {
	t.Parallel()

	coinbaseThorchain := xmrCoinbaseThorchain()
	if coinbaseThorchain == 0 {
		t.Fatal("coinbase thorchain must be non-zero")
	}

	// 2x multiplier (confMul=20000 bps) doubles confValue, halving confirmations.
	doubledConfValue := cosmos.NewUint(coinbaseThorchain * 2)
	totalValue := cosmos.NewUint(coinbaseThorchain * 10) // would normally need 10 confirmations
	if got := xmrConfirmationRequired(totalValue, doubledConfValue); got != 5 {
		t.Fatalf("expected 5 confirmations with 2x multiplier, got %d", got)
	}

	// 0.5x multiplier (confMul=5000 bps) halves confValue, doubling confirmations.
	halvedConfValue := cosmos.NewUint(coinbaseThorchain / 2)
	if got := xmrConfirmationRequired(totalValue, halvedConfValue); got != 20 {
		t.Fatalf("expected 20 confirmations with 0.5x multiplier, got %d", got)
	}
}
