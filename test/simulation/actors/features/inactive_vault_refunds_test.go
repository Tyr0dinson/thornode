package features

import (
	"errors"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

func TestInactiveVaultRefundSelectsEligibleChainAndSigningKey(t *testing.T) {
	for _, asset := range []common.Asset{common.XMRAsset, common.BTCAsset} {
		t.Run(asset.String(), func(t *testing.T) {
			a := InactiveVaultRefundActor{
				asset: asset,
				getInactiveVaults: func() ([]openapi.Vault, error) {
					return []openapi.Vault{
						{PubKey: openapi.PtrString("active"), Status: "ActiveVault", Addresses: []openapi.VaultAddress{{Chain: asset.Chain.String(), Address: "active-address"}}},
						{PubKey: openapi.PtrString("retired-primary"), PubKeyFrost: openapi.PtrString("retired-frost"), Status: "InactiveVault", Addresses: []openapi.VaultAddress{{Chain: asset.Chain.String(), Address: "retired-address"}}},
					}, nil
				},
			}
			result := a.selectInactiveVault(nil)
			expected := "retired-primary"
			if asset.Equals(common.XMRAsset) {
				expected = "retired-frost"
			}
			if !result.Continue || result.Error != nil || a.inboundVault != expected || a.inboundAddress != "retired-address" {
				t.Fatalf("wrong retired vault selected: %+v, %+v", result, a)
			}
		})
	}
}

func TestInactiveVaultRefundCannotSilentlySkip(t *testing.T) {
	a := InactiveVaultRefundActor{asset: common.XMRAsset, getInactiveVaults: func() ([]openapi.Vault, error) { return nil, nil }}
	result := a.selectInactiveVault(nil)
	if !result.Finish || result.Error == nil || result.Continue {
		t.Fatalf("missing coverage must fail: %+v", result)
	}
	failure := errors.New("query unavailable")
	a.getInactiveVaults = func() ([]openapi.Vault, error) { return nil, failure }
	result = a.selectInactiveVault(nil)
	if result.Finish || result.Continue || !errors.Is(result.Error, failure) {
		t.Fatalf("query error should retry: %+v", result)
	}
}

func TestInactiveVaultRefundRejectsMissingFrostKey(t *testing.T) {
	a := InactiveVaultRefundActor{asset: common.XMRAsset, getInactiveVaults: func() ([]openapi.Vault, error) {
		return []openapi.Vault{
			{PubKey: openapi.PtrString("primary"), Status: "InactiveVault", Addresses: []openapi.VaultAddress{{Chain: "XMR", Address: "retired"}}},
		}, nil
	}}
	if _, _, err := a.findInactiveVaultAddress(); err == nil {
		t.Fatal("XMR must not substitute primary key for missing FROST key")
	}
}

func TestInactiveRefundReferenceMatchesAssetAndMemo(t *testing.T) {
	expected := "+:XMR.XMR:thor-address/refund-address"
	good := openapi.ReferenceMemoResponse{Asset: "XMR.XMR", Memo: expected, Reference: "00042"}
	if memo, err := inactiveRefundReference(good, common.XMRAsset, expected); err != nil || memo != "r:42" {
		t.Fatalf("reference = %q, %v", memo, err)
	}
	for _, change := range []func(*openapi.ReferenceMemoResponse){
		func(r *openapi.ReferenceMemoResponse) { r.Memo = "+:XMR.XMR:another-recipient" },
		func(r *openapi.ReferenceMemoResponse) { r.Asset = "BTC.BTC" },
		func(r *openapi.ReferenceMemoResponse) { r.Reference = "invalid" },
	} {
		bad := good
		change(&bad)
		if _, err := inactiveRefundReference(bad, common.XMRAsset, expected); err == nil {
			t.Fatal("mismatched reference accepted")
		}
	}
}

func TestInactiveVaultRefundVerificationRejectsUnrelatedOutbounds(t *testing.T) {
	fixture := func() openapi.TxDetailsResponse {
		return openapi.TxDetailsResponse{
			Tx:      openapi.ObservedTx{ObservedPubKey: openapi.PtrString("retired-frost")},
			Actions: []openapi.TxOutItem{{Chain: "XMR", ToAddress: "refund-address", InHash: openapi.PtrString("inbound"), Memo: openapi.PtrString("REFUND:inbound"), Coin: openapi.Coin{Asset: "XMR.XMR", Amount: "90"}}},
			OutTxs:  []openapi.Tx{{Id: openapi.PtrString("outbound"), Chain: openapi.PtrString("XMR"), ToAddress: openapi.PtrString("refund-address"), Coins: []openapi.Coin{{Asset: "XMR.XMR", Amount: "90"}}}},
		}
	}
	check := func(d openapi.TxDetailsResponse) error {
		return validateInactiveVaultRefund(d, common.XMRAsset, "inbound", "retired-frost", common.Address("refund-address"), cosmos.NewUint(100))
	}
	if err := check(fixture()); err != nil {
		t.Fatalf("valid refund rejected: %v", err)
	}
	memoless := fixture()
	memoless.Actions[0].OriginalMemo = openapi.PtrString("REFUND:inbound")
	memoless.Actions[0].Memo = openapi.PtrString("r:42")
	if err := check(memoless); err != nil {
		t.Fatalf("valid memoless refund rejected: %v", err)
	}
	for _, tc := range []struct {
		name   string
		change func(*openapi.TxDetailsResponse)
	}{
		{"wrong observed vault", func(d *openapi.TxDetailsResponse) { d.Tx.ObservedPubKey = openapi.PtrString("active-vault") }},
		{"swap instead of refund", func(d *openapi.TxDetailsResponse) { d.Actions[0].Memo = openapi.PtrString("OUT:inbound") }},
		{"wrong inbound", func(d *openapi.TxDetailsResponse) { d.Actions[0].InHash = openapi.PtrString("different") }},
		{"wrong recipient", func(d *openapi.TxDetailsResponse) { d.OutTxs[0].ToAddress = openapi.PtrString("different") }},
		{"wrong asset", func(d *openapi.TxDetailsResponse) { d.OutTxs[0].Coins[0].Asset = "BTC.BTC" }},
		{"zero refund", func(d *openapi.TxDetailsResponse) { d.OutTxs[0].Coins[0].Amount = "0" }},
		{"malformed amount", func(d *openapi.TxDetailsResponse) { d.OutTxs[0].Coins[0].Amount = "not-an-amount" }},
		{"over-refund", func(d *openapi.TxDetailsResponse) { d.OutTxs[0].Coins[0].Amount = "101" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := fixture()
			tc.change(&d)
			if err := check(d); err == nil {
				t.Fatal("invalid refund accepted")
			}
		})
	}
}
