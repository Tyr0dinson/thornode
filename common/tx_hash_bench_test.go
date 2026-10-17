package common

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

// BenchmarkTxHash measures the attestation unique-hash input bifrost derives
// for every observed tx it receives over gossip.
func BenchmarkTxHash(b *testing.B) {
	tx := Tx{
		ID:          "B8C6B5E1C5D7F0C1A9E0B6E9F1E2D3C4B5A69788796A5B4C3D2E1F0A9B8C7D6E",
		Chain:       BTCChain,
		FromAddress: "bc1qxy2kgdygjrsqtzq2n0yrf2493p83kkfjhx0wlh",
		ToAddress:   "bc1q5cyxnuxmeuwuvkwfem96lqzszd02n6xdcjrs20",
		Coins:       Coins{NewCoin(BTCAsset, cosmos.NewUint(12345678))},
		Gas:         Gas{NewCoin(BTCAsset, cosmos.NewUint(2500))},
		Memo:        "=:ETH.ETH:0x3fd2d4ce97b082d4bce3f9fee2a3d60668d2f473:0/1/0",
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = tx.Hash(912345)
	}
}
