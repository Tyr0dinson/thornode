package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

func TestNativeMempoolEscrowAsset(t *testing.T) {
	cases := []struct {
		target common.Asset
		want   common.Asset
	}{
		{target: common.RuneAsset(), want: common.RuneAsset()},
		{target: common.BTCAsset.GetSyntheticAsset(), want: common.BTCAsset.GetSyntheticAsset()},
		{target: common.BTCAsset.GetSecuredAsset(), want: common.BTCAsset},
	}
	for _, tc := range cases {
		if got := nativeMempoolEscrowAsset(tc.target); !got.Equals(tc.want) {
			t.Fatalf("escrow asset for %s: got %s, want %s", tc.target, got, tc.want)
		}
	}
}

func TestNativeMempoolValidateFinalSettlement(t *testing.T) {
	txID := strings.Repeat("A", 64)
	destination := common.Address("thor1nativecreditrecipient")
	amount := cosmos.NewUint(12345678)

	nonstreamTarget := common.BTCAsset.GetSecuredAsset()
	nonstream := nativeMempoolTestSettlement(txID, destination, nonstreamTarget, amount)
	if err := nativeMempoolValidateFinalSettlement(nonstream, txID, destination, nonstreamTarget, amount, cosmos.ZeroUint()); err != nil {
		t.Fatalf("valid non-streaming settlement rejected: %v", err)
	}

	streamTarget := common.BTCAsset.GetSyntheticAsset()
	stream := nativeMempoolTestSettlement(txID, destination, streamTarget, amount)
	if err := nativeMempoolValidateFinalSettlement(stream, txID, destination, streamTarget, amount, amount); err != nil {
		t.Fatalf("valid streaming settlement rejected: %v", err)
	}
}

func TestNativeMempoolValidateFinalSettlementRejectsWrongOutbound(t *testing.T) {
	txID := strings.Repeat("B", 64)
	destination := common.Address("thor1nativecreditrecipient")
	target := common.BTCAsset.GetSecuredAsset()
	amount := cosmos.NewUint(87654321)

	tests := []struct {
		name   string
		mutate func(*openapi.TxDetailsResponse)
	}{
		{
			name: "additional external out tx",
			mutate: func(details *openapi.TxDetailsResponse) {
				external := details.OutTxs[0]
				external.SetChain(common.BTCChain.String())
				details.OutTxs = append(details.OutTxs, external)
			},
		},
		{
			name:   "out tx memo",
			mutate: func(details *openapi.TxDetailsResponse) { details.OutTxs[0].SetMemo("OUT:WRONG") },
		},
		{
			name: "out tx amount",
			mutate: func(details *openapi.TxDetailsResponse) {
				details.OutTxs[0].Coins[0].Amount = amount.Add(cosmos.OneUint()).String()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			details := nativeMempoolTestSettlement(txID, destination, target, amount)
			tc.mutate(&details)
			if err := nativeMempoolValidateFinalSettlement(details, txID, destination, target, amount, cosmos.ZeroUint()); err == nil {
				t.Fatal("invalid settlement was accepted")
			}
		})
	}
}

func TestNativeMempoolValidateFinalSettlementRequiresStreamingOutTx(t *testing.T) {
	txID := strings.Repeat("D", 64)
	destination := common.Address("thor1nativecreditrecipient")
	amount := cosmos.NewUint(42424242)

	target := common.RuneAsset()
	details := nativeMempoolTestSettlement(txID, destination, target, amount)
	details.OutTxs = nil
	if err := nativeMempoolValidateFinalSettlement(details, txID, destination, target, amount, amount); err == nil {
		t.Fatal("streaming settlement must require an out tx record")
	}
}

func TestNativeMempoolValidateSwapQueueAbsent(t *testing.T) {
	txID := strings.Repeat("C", 64)
	details := openapi.SwapDetailsResponse{}
	details.SetQueueType("advanced")

	if err := nativeMempoolValidateSwapQueueAbsent(txID, details, nil); err == nil {
		t.Fatal("successful queue lookup must prove the swap remains queued")
	}
	if err := nativeMempoolValidateSwapQueueAbsent(txID, openapi.SwapDetailsResponse{}, errors.New("not found in any queue")); err != nil {
		t.Fatalf("exact queue-not-found response should prove cleanup: %v", err)
	}
	if err := nativeMempoolValidateSwapQueueAbsent(txID, openapi.SwapDetailsResponse{}, errors.New("connection reset")); err == nil {
		t.Fatal("transient query error must not be accepted as queue absence")
	}
}

func nativeMempoolTestSettlement(txID string, destination common.Address, target common.Asset, amount cosmos.Uint) openapi.TxDetailsResponse {
	chain := common.THORChain.String()
	blankTxID := common.BlankTxID.String()
	toAddress := destination.String()
	memo := "OUT:" + txID
	emptyMemo := ""
	coin := openapi.Coin{Asset: target.String(), Amount: amount.String()}
	return openapi.TxDetailsResponse{
		Actions: []openapi.TxOutItem{{
			Chain:        chain,
			ToAddress:    toAddress,
			Coin:         coin,
			Memo:         &emptyMemo,
			OriginalMemo: &memo,
		}},
		OutTxs: []openapi.Tx{{
			Id:        &blankTxID,
			Chain:     &chain,
			ToAddress: &toAddress,
			Coins:     []openapi.Coin{coin},
			Memo:      &memo,
		}},
	}
}

func TestNativeMempoolMissedWindowClassification(t *testing.T) {
	txID := strings.Repeat("C", 64)
	before := cosmos.NewUint(101)
	baseline := cosmos.NewUint(202)

	// A missed window stays recognizable through the wrapping every scenario and
	// recheck path adds, and it carries the baselines the retry drains back to.
	wrapped := fmt.Errorf("plain native target finalized before the exclusion could be rechecked: %w",
		nativeMempoolMissedWindowError(txID, "POL inbound %s finalized before the pre-finality assertion", txID))
	wrapped = nativeMempoolCarryBaselines(wrapped, before, baseline)

	var missed *nativeMempoolMissedWindow
	if !errors.As(wrapped, &missed) {
		t.Fatalf("missed window was not recognized: %v", wrapped)
	}
	if missed.txID != txID {
		t.Fatalf("missed window txid: got %s, want %s", missed.txID, txID)
	}
	if !missed.before.Equal(before) || !missed.baselineUsage.Equal(baseline) {
		t.Fatalf("missed window baselines: got (%s,%s), want (%s,%s)",
			missed.before, missed.baselineUsage, before, baseline)
	}
	if !strings.Contains(wrapped.Error(), txID) {
		t.Fatalf("missed window lost its message: %v", wrapped)
	}

	// A policy violation must never be retried as a lost race.
	violation := nativeMempoolCarryBaselines(
		fmt.Errorf("plain native target created staged outbound before finality"), before, baseline)
	if errors.As(violation, &missed) {
		t.Fatalf("policy violation classified as a missed window: %v", violation)
	}
	if nativeMempoolCarryBaselines(nil, before, baseline) != nil {
		t.Fatal("a successful attempt was turned into an error")
	}
}
