//go:build !mocknet

package substrate

import (
	"os"
	"strings"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
	"gitlab.com/thorchain/thornode/v3/common"

	. "gopkg.in/check.v1"
)

// ---------------------------------------------------------------------------
// ValidateMetadata — real mainnet fixtures (clean + tampered)
//
// Mainnet-only: uses real DOT mainnet metadata which has a different pallet
// layout than the parity/polkadot --chain dev runtime captured under the
// mocknet build tag.
// ---------------------------------------------------------------------------

func (s *ParamsSuite) TestValidateMetadataAgainstFixture(c *C) {
	tests := []struct {
		name        string
		fixturePath string
		chainID     common.Chain
		tamper      func(*stypes.Metadata)
		wantErrIn   string
	}{
		{
			name:        "DOT Asset Hub — clean",
			fixturePath: "types/testdata/metadata-dot-assethub-2003001.hex",
			chainID:     common.DOTChain,
			tamper:      nil,
			wantErrIn:   "",
		},
		{
			name:        "DOT Asset Hub — Balances pallet renumbered",
			fixturePath: "types/testdata/metadata-dot-assethub-2003001.hex",
			chainID:     common.DOTChain,
			tamper: func(m *stypes.Metadata) {
				bumpPalletIndex(m, "Balances", +1)
			},
			wantErrIn: "Balances",
		},
		{
			// DOT targets Asset Hub since the Asset Hub Migration; validating
			// against relay-chain metadata (Balances at 5, not 10) must fail
			// so a mis-pointed RPC endpoint is caught at startup.
			name:        "DOT params against relay-chain metadata — rejected",
			fixturePath: "types/testdata/metadata-dot-mainnet-2002001.hex",
			chainID:     common.DOTChain,
			tamper:      nil,
			wantErrIn:   "invalid pallet index",
		},
		{
			name:        "TAO mainnet — clean",
			fixturePath: "types/testdata/metadata-tao-mainnet-401.hex",
			chainID:     common.TAOChain,
			tamper:      nil,
			wantErrIn:   "",
		},
		{
			name:        "TAO mainnet — batch_all call renumbered",
			fixturePath: "types/testdata/metadata-tao-mainnet-401.hex",
			chainID:     common.TAOChain,
			tamper: func(m *stypes.Metadata) {
				bumpCallIndex(m, "Utility", "batch_all", +1)
			},
			wantErrIn: "batch_all",
		},
	}

	for _, tt := range tests {
		raw, err := os.ReadFile(tt.fixturePath)
		c.Assert(err, IsNil, Commentf("%s: read fixture", tt.name))

		md, err := stypes.ParseMetadata(strings.TrimSpace(string(raw)))
		c.Assert(err, IsNil, Commentf("%s: ParseMetadata", tt.name))
		c.Assert(md, NotNil, Commentf("%s: nil metadata", tt.name))

		if tt.tamper != nil {
			tt.tamper(md)
		}
		_, err = ChainParams[tt.chainID].ValidateMetadata(md)
		if tt.wantErrIn == "" {
			c.Check(err, IsNil, Commentf("%s: expected nil, got %v", tt.name, err))
			continue
		}
		c.Assert(err, NotNil, Commentf("%s: expected error containing %q", tt.name, tt.wantErrIn))
		c.Check(strings.Contains(err.Error(), tt.wantErrIn), Equals, true,
			Commentf("%s: error %q missing %q", tt.name, err.Error(), tt.wantErrIn))
	}
}

// bumpPalletIndex shifts the named pallet's Index by delta.
func bumpPalletIndex(m *stypes.Metadata, palletName string, delta int) {
	for i := range m.Modules {
		if m.Modules[i].Name == palletName {
			m.Modules[i].Index += delta
			return
		}
	}
}

// bumpCallIndex shifts the named call's LookupIndex within the named pallet by delta.
func bumpCallIndex(m *stypes.Metadata, palletName, callName string, delta int) {
	for i := range m.Modules {
		if m.Modules[i].Name != palletName {
			continue
		}
		for j := range m.Modules[i].Calls {
			if m.Modules[i].Calls[j].Name == callName {
				m.Modules[i].Calls[j].LookupIndex += delta
				return
			}
		}
	}
}
