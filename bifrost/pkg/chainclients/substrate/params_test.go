package substrate

import (
	"strings"
	"testing"

	scaletypes "github.com/itering/scale.go/types"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
	"gitlab.com/thorchain/thornode/v3/common"

	. "gopkg.in/check.v1"
)

func TestParamsSuite(t *testing.T) { TestingT(t) }

type ParamsSuite struct{}

var _ = Suite(&ParamsSuite{})

// buildTAOMetadata returns a synthetic *Metadata whose pallet/call/event
// indices align with ChainParams[common.TAOChain].Indexes. Each test row
// mutates a copy of this baseline to simulate drift.
func buildTAOMetadata() *stypes.Metadata {
	return &stypes.Metadata{
		Modules: []scaletypes.MetadataModules{
			{Name: "System", Index: 0, Calls: []scaletypes.MetadataCalls{
				{Name: "remark", LookupIndex: 0},
			}},
			{Name: "Balances", Index: 5, Calls: []scaletypes.MetadataCalls{
				{Name: "transfer_allow_death", LookupIndex: 0},
				{Name: "transfer_keep_alive", LookupIndex: 3},
			}, Events: []scaletypes.MetadataEvents{
				{Name: "Transfer", LookupIndex: 2},
			}},
			{Name: "TransactionPayment", Index: 6},
			{Name: "Utility", Index: 11, Calls: []scaletypes.MetadataCalls{
				{Name: "batch_all", LookupIndex: 2},
			}},
		},
	}
}

// ---------------------------------------------------------------------------
// ValidateMetadata — synthetic baseline + per-row mutations
// ---------------------------------------------------------------------------

func (s *ParamsSuite) TestValidateMetadata(c *C) {
	tests := []struct {
		name      string
		mutate    func(*stypes.Metadata)
		wantErrIn string // "" means expect nil
	}{
		{
			name:      "clean baseline",
			mutate:    nil,
			wantErrIn: "",
		},
		{
			name: "Balances pallet renumbered",
			mutate: func(m *stypes.Metadata) {
				m.Modules[1].Index = 6
			},
			wantErrIn: "Balances",
		},
		{
			name: "Utility pallet renumbered",
			mutate: func(m *stypes.Metadata) {
				m.Modules[3].Index = 12
			},
			wantErrIn: "Utility",
		},
		{
			name: "transfer_keep_alive call moved",
			mutate: func(m *stypes.Metadata) {
				m.Modules[1].Calls[1].LookupIndex = 4
			},
			wantErrIn: "transfer_keep_alive",
		},
		{
			name: "batch_all call moved",
			mutate: func(m *stypes.Metadata) {
				m.Modules[3].Calls[0].LookupIndex = 3
			},
			wantErrIn: "batch_all",
		},
		{
			name: "Transfer event moved",
			mutate: func(m *stypes.Metadata) {
				m.Modules[1].Events[0].LookupIndex = 3
			},
			wantErrIn: "Transfer",
		},
		{
			// Pallet not in Indexes — silently skipped, no error.
			name: "unknown pallet present",
			mutate: func(m *stypes.Metadata) {
				m.Modules = append(m.Modules, scaletypes.MetadataModules{
					Name: "Foo", Index: 99,
				})
			},
			wantErrIn: "",
		},
		{
			// Current behavior: ValidateMetadata only checks pallets PRESENT
			// in the metadata. A configured-but-missing pallet does not
			// raise an error. This row pins that behavior; flip the
			// assertion if the validator is later tightened.
			name: "required pallet missing",
			mutate: func(m *stypes.Metadata) {
				m.Modules = append(m.Modules[:1], m.Modules[2:]...) // drop Balances
			},
			wantErrIn: "",
		},
	}

	for _, tt := range tests {
		md := buildTAOMetadata()
		if tt.mutate != nil {
			tt.mutate(md)
		}
		err := ChainParams[common.TAOChain].ValidateMetadata(md)
		if tt.wantErrIn == "" {
			c.Check(err, IsNil, Commentf("%s: expected nil, got %v", tt.name, err))
			continue
		}
		c.Assert(err, NotNil, Commentf("%s: expected error containing %q", tt.name, tt.wantErrIn))
		c.Check(strings.Contains(err.Error(), tt.wantErrIn), Equals, true,
			Commentf("%s: error %q missing %q", tt.name, err.Error(), tt.wantErrIn))
	}
}
