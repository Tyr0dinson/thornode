package common

import (
	"net/http"
	"testing"

	"github.com/rs/zerolog"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

func TestSetXMRChainEnabledTogglesSimChainsCov(t *testing.T) {
	t.Cleanup(func() { SetXMRChainEnabled(true) })

	SetXMRChainEnabled(false)
	if XMRChainEnabled() {
		t.Fatal("XMR should be disabled")
	}
	if len(SimChains) != len(allSimChains)-1 {
		t.Fatalf("expected %d chains, got %d", len(allSimChains)-1, len(SimChains))
	}
	for _, c := range SimChains {
		if c.Equals(common.XMRChain) {
			t.Fatal("XMR must be removed")
		}
	}

	SetXMRChainEnabled(true)
	if !XMRChainEnabled() {
		t.Fatal("XMR should be enabled")
	}
	if len(SimChains) != len(allSimChains) {
		t.Fatalf("expected %d chains, got %d", len(allSimChains), len(SimChains))
	}
}

func TestDepositL1InboundErrorCov(t *testing.T) {
	setThornodeCov(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/thorchain/inbound_addresses" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	l := zerolog.Nop()
	_, err := DepositL1(&l, nil, common.BTCAsset, "memo", cosmos.NewUint(1))
	if err == nil {
		t.Fatal("expected inbound address error")
	}
}
