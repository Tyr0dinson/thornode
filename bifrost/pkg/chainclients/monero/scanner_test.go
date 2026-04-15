package monero

import (
	"errors"
	"testing"
)

func TestAverageNetworkFeeFromBlock(t *testing.T) {
	// defaultCoinbaseValue must be in piconero (Monero atomic units, 1e12 per XMR).
	// The Monero daemon RPC get_block returns reward in piconero, and this constant
	// is subtracted directly from that value. On mainnet the tail emission is ~0.6 XMR
	// = 600,000,000,000 piconero.
	if defaultCoinbaseValue < 100000000000 { // sanity: at least 0.1 XMR in piconero
		t.Fatalf("defaultCoinbaseValue %d appears too small; must be in piconero (1e12 per XMR)", defaultCoinbaseValue)
	}

	// 200,000 piconero total fees across 2 txs -> 100,000 piconero per tx.
	reward := defaultCoinbaseValue + uint64(200000)
	fee := averageNetworkFeeFromBlock(reward, 2)
	if fee != 100000 {
		t.Fatalf("expected average fee 100000, got %d", fee)
	}

	// Reward equal to coinbase → zero fees.
	fee = averageNetworkFeeFromBlock(defaultCoinbaseValue, 2)
	if fee != 0 {
		t.Fatalf("expected zero fee when reward equals coinbase, got %d", fee)
	}

	// Zero tx count → zero fees.
	fee = averageNetworkFeeFromBlock(defaultCoinbaseValue+200000, 0)
	if fee != 0 {
		t.Fatalf("expected zero fee when tx count is zero, got %d", fee)
	}

	// Reward below coinbase → zero fees (no underflow).
	fee = averageNetworkFeeFromBlock(defaultCoinbaseValue-1, 5)
	if fee != 0 {
		t.Fatalf("expected zero fee when reward is below coinbase, got %d", fee)
	}
}

func TestIsUnavailableBlockErr(t *testing.T) {
	if !IsUnavailableBlockErr(ErrFrostUnavailableBlock) {
		t.Fatal("expected unavailable block error to be detected")
	}
	if !IsUnavailableBlockErr(errors.Join(errString("wrapper"), ErrFrostUnavailableBlock)) {
		t.Fatal("expected wrapped unavailable block error to be detected")
	}
	if IsUnavailableBlockErr(errString("some other rpc error")) {
		t.Fatal("expected non-block-height errors to be ignored")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
