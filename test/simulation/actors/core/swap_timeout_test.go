package core

import (
	"fmt"
	"testing"
	"time"

	rand "math/rand/v2"

	sdkmath "cosmossdk.io/math"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestNewSwapActorTimeouts(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(1, 2))

	ethToBtc := NewSwapActor(common.ETHAsset, common.BTCAsset, rng)
	if ethToBtc.Timeout != 2*time.Minute {
		t.Fatalf("expected standard swap timeout to be 2m, got %s", ethToBtc.Timeout)
	}

	ethToXmr := NewSwapActor(common.ETHAsset, common.XMRAsset, rng)
	if ethToXmr.Timeout != 4*time.Minute {
		t.Fatalf("expected XMR swap timeout to be 4m, got %s", ethToXmr.Timeout)
	}
}

func TestNewSwapMemolessActorTimeouts(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewPCG(3, 4))

	ethToBtc := NewSwapMemolessActor(common.ETHAsset, common.BTCAsset, MemolessTypeRef, rng)
	if ethToBtc.Timeout != 5*time.Minute {
		t.Fatalf("expected standard memoless swap timeout to be 5m, got %s", ethToBtc.Timeout)
	}

	ethToXmr := NewSwapMemolessActor(common.ETHAsset, common.XMRAsset, MemolessTypeRef, rng)
	if ethToXmr.Timeout != 7*time.Minute {
		t.Fatalf("expected XMR memoless swap timeout to be 7m, got %s", ethToXmr.Timeout)
	}
}

func TestGetRecommendedMinAmountInFromError(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf(`(http://localhost:1317/thorchain/quote/swap) HTTP: 500 => {"code":2,"message":"amount less than min swap amount (recommended_min_amount_in: 941178450)","details":[]}`)
	got, ok := getRecommendedMinAmountInFromError(err)
	if !ok {
		t.Fatal("expected to extract recommended minimum amount")
	}

	want := sdkmath.NewUint(941178450)
	if !got.Equal(want) {
		t.Fatalf("expected recommended minimum %s, got %s", want, got)
	}
}

func TestAmountWithEncodedRefAtOrAbove(t *testing.T) {
	t.Parallel()

	asset, err := common.NewAsset("AVAX.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	if err != nil {
		t.Fatalf("failed to parse asset: %v", err)
	}

	actor := &SwapMemolessActor{
		SwapActor: SwapActor{
			from: asset,
			to:   common.BTCAsset,
		},
		memolessRef: 2,
	}

	got := actor.amountWithEncodedRefAtOrAbove(sdkmath.NewUint(941178450))
	want := sdkmath.NewUint(941200002)
	if !got.Equal(want) {
		t.Fatalf("expected encoded amount %s, got %s", want, got)
	}
}

func TestMaxAmountWithEncodedRefAtOrAbove(t *testing.T) {
	t.Parallel()

	asset, err := common.NewAsset("AVAX.TKN-0X17AB05351FC94A1A67BF3F56DDBB941AE6C63E25")
	if err != nil {
		t.Fatalf("failed to parse asset: %v", err)
	}

	actor := &SwapMemolessActor{
		SwapActor: SwapActor{
			from: asset,
			to:   common.BTCAsset,
		},
	}

	got := actor.maxAmountWithEncodedRefAtOrAbove(sdkmath.NewUint(941178450))
	want := sdkmath.NewUint(941199999)
	if !got.Equal(want) {
		t.Fatalf("expected max encoded amount %s, got %s", want, got)
	}
}
