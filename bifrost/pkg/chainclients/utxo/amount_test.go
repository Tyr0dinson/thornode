package utxo

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestToUint64(t *testing.T) {
	for _, tc := range []struct {
		name       string
		amount     cosmos.Uint
		want       uint64
		wantErrSub string
	}{
		{name: "nil", amount: cosmos.Uint{}, wantErrSub: "amount is nil"},
		{name: "ordinary", amount: cosmos.NewUint(42), want: 42},
		{name: "maximum", amount: cosmos.NewUint(math.MaxUint64), want: math.MaxUint64},
		{
			name:       "overflow",
			amount:     cosmos.NewUintFromString("18446744073709551616"),
			wantErrSub: "amount does not fit uint64",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := toUint64(tc.amount)
			if tc.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErrSub, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestNativeGasCoinPreservesUTXODecimalCompatibility(t *testing.T) {
	btcClient := &Client{}
	btcClient.cfg.ChainID = common.BTCChain
	btcCoin := btcClient.toThorCoin(cosmos.NewUint(50_000_000))
	btcGas := btcClient.toThorCoin(cosmos.NewUint(3_000))
	if btcCoin.Decimals != 0 || btcGas.Decimals != 0 {
		t.Fatalf("BTC decimal metadata changed: coin=%d gas=%d", btcCoin.Decimals, btcGas.Decimals)
	}

	xecClient := &Client{}
	xecClient.cfg.ChainID = common.XECChain
	xecCoin := xecClient.toThorCoin(cosmos.NewUint(100))
	if xecCoin.Decimals != 2 || !xecCoin.Amount.Equal(cosmos.NewUint(common.One)) {
		t.Fatalf("unexpected XEC coin: amount=%s decimals=%d", xecCoin.Amount, xecCoin.Decimals)
	}

	legacyCoin := common.NewCoin(common.BTCAsset, btcCoin.Amount)
	legacyGas := common.NewCoin(common.BTCAsset, btcGas.Amount)
	makeObservedTx := func(coin, gas common.Coin) common.ObservedTx {
		return common.ObservedTx{
			Tx: common.Tx{
				ID:    "6D3C642AE0FF0A3E2ACB8CA2F1B0F9C4E44D7A2599277F03E9B598FE0B1AD9C1",
				Chain: common.BTCChain,
				Coins: common.Coins{coin},
				Gas:   common.Gas{gas},
				Memo:  "=:ETH.ETH:0x90f2b1ae50e6018230e90a33f98c7844a0ab635a",
			},
			BlockHeight:    850_000,
			FinaliseHeight: 850_000,
		}
	}

	legacyObservedTx := makeObservedTx(legacyCoin, legacyGas)
	legacyPayload, err := legacyObservedTx.GetSignablePayloadWithInbound(true)
	if err != nil {
		t.Fatalf("marshal legacy observation: %v", err)
	}
	currentObservedTx := makeObservedTx(btcCoin, btcGas)
	currentPayload, err := currentObservedTx.GetSignablePayloadWithInbound(true)
	if err != nil {
		t.Fatalf("marshal current observation: %v", err)
	}
	if !bytes.Equal(legacyPayload, currentPayload) {
		t.Fatal("BTC observation sign bytes differ from the legacy payload")
	}
}
