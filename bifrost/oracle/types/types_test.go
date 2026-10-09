package types

import (
	"math/big"
	"testing"
	"time"
)

func TestCurrencyPairCov(t *testing.T) {
	cp, err := NewCurrencyPair("BTC/USDT")
	if err != nil {
		t.Fatal(err)
	}
	if cp.Base != "BTC" || cp.Quote != "USDT" {
		t.Fatalf("unexpected pair %+v", cp)
	}
	if cp.String() != "BTC/USDT" {
		t.Fatalf("String: %s", cp.String())
	}
	if cp.Join("-") != "BTC-USDT" || cp.Join("") != "BTCUSDT" {
		t.Fatalf("Join failed")
	}
	sw := cp.Swap()
	if sw.Base != "USDT" || sw.Quote != "BTC" {
		t.Fatalf("Swap: %+v", sw)
	}

	for _, bad := range []string{"BTC", "BTC/USDT/ETH", ""} {
		if _, err := NewCurrencyPair(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestTickerStringCov(t *testing.T) {
	tk := Ticker{
		Time:   time.Unix(0, 0),
		Pair:   CurrencyPair{Base: "ETH", Quote: "USD"},
		Price:  big.NewFloat(1234.5),
		Volume: big.NewFloat(10),
	}
	if got, want := tk.String(), "s=ETH/USD p=1234.5, v=10"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
