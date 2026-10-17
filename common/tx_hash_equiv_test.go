package common

import (
	"crypto/sha256"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// referenceTxHash is the original fmt-based Tx.Hash.
func referenceTxHash(tx Tx, blockHeight int64) string {
	str := fmt.Sprintf("%s|%s|%s|%s|%d", tx.Chain, tx.FromAddress, tx.Coins, tx.ToAddress, blockHeight)
	return fmt.Sprintf("%X", sha256.Sum256([]byte(str)))
}

func randomHashString(rng *rand.Rand) string {
	pieces := []string{"", "btc", "BTC", "eth", "thor1", "0x", "|", "%d", "%!s", " ", "ü", "K", "\x00", "bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh"}
	var s string
	for n := rng.Intn(4); n > 0; n-- {
		s += pieces[rng.Intn(len(pieces))]
	}
	return s
}

func randomHashCoin(rng *rand.Rand) Coin {
	assets := []Asset{BTCAsset, ETHAsset, RuneNative, {}, {Chain: "eth", Symbol: "usdc-0xa0b8", Ticker: "usdc", Synth: true}, {Chain: BTCChain, Symbol: "BTC", Ticker: "BTC", Trade: true}, {Chain: ETHChain, Symbol: "ETH", Ticker: "ETH", Secured: true}}
	c := Coin{Asset: assets[rng.Intn(len(assets))], Decimals: rng.Int63n(19)}
	switch rng.Intn(4) {
	case 0: // zero-value Uint, nil inside
	case 1:
		c.Amount = cosmos.ZeroUint()
	case 2:
		c.Amount = cosmos.NewUint(rng.Uint64())
	default: // up to 63+189 bits; Uint is capped at 256
		c.Amount = cosmos.NewUintFromBigInt(new(big.Int).Lsh(big.NewInt(rng.Int63()), uint(rng.Intn(190))))
	}
	return c
}

func TestTxHashMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(3)) //nolint:gosec // fixed seed: a reproducible differential test
	heights := []int64{0, 1, -1, 912345, math.MaxInt64, math.MinInt64}
	for i := 0; i < 50000; i++ {
		tx := Tx{
			ID:          TxID(randomHashString(rng)),
			Chain:       Chain(randomHashString(rng)),
			FromAddress: Address(randomHashString(rng)),
			ToAddress:   Address(randomHashString(rng)),
			Memo:        randomHashString(rng),
		}
		if rng.Intn(5) > 0 {
			tx.Coins = Coins{}
			for n := rng.Intn(4); n > 0; n-- {
				tx.Coins = append(tx.Coins, randomHashCoin(rng))
			}
		}
		for n := rng.Intn(3); n > 0; n-- {
			tx.Gas = append(tx.Gas, randomHashCoin(rng))
		}
		h := heights[rng.Intn(len(heights))]
		if rng.Intn(2) == 0 {
			h = rng.Int63() - rng.Int63()
		}
		if got, want := tx.Hash(h), referenceTxHash(tx, h); got != want {
			t.Fatalf("Hash(%d) of %#v = %s, want %s", h, tx, got, want)
		}
	}
}
