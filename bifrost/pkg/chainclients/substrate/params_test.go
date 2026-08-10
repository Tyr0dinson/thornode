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
			}, Events: []scaletypes.MetadataEvents{
				{Name: "ExtrinsicSuccess", LookupIndex: 0, Args: []string{"frame_system:DispatchEventInfo"}},
				{Name: "ExtrinsicFailed", LookupIndex: 1, Args: []string{"sp_runtime:DispatchError", "frame_system:DispatchEventInfo"}},
			}},
			{Name: "Balances", Index: 5, Calls: []scaletypes.MetadataCalls{
				{Name: "transfer_allow_death", LookupIndex: 0},
				{Name: "transfer_keep_alive", LookupIndex: 3},
			}, Events: []scaletypes.MetadataEvents{
				{Name: "Transfer", LookupIndex: 2, Args: []string{"AccountId", "AccountId", "U64"}},
			}},
			{Name: "TransactionPayment", Index: 6, Events: []scaletypes.MetadataEvents{
				{Name: "TransactionFeePaid", LookupIndex: 0, Args: []string{"AccountId", "U64", "U64"}, ArgsName: []string{"who", "actual_fee", "tip"}},
			}},
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
			// The unmodified TAO fixture aligns with every key in
			// ChainParams[TAOChain].Indexes. Pins that ValidateMetadata
			// returns nil when chain metadata matches configuration.
			name:      "clean baseline",
			mutate:    nil,
			wantErrIn: "",
		},
		{
			// A configured pallet's on-chain index drifts from what Indexes
			// declares. ValidateMetadata short-circuits with an error naming
			// the drifted pallet before walking the rest of the module.
			name: "Balances pallet renumbered",
			mutate: func(m *stypes.Metadata) {
				m.Modules[1].Index = 6
			},
			wantErrIn: "Balances",
		},
		{
			// A configured call's on-chain LookupIndex no longer matches what
			// Indexes declares. isValidIndex returns (configured: true,
			// valid: false) and ValidateMetadata errors with the call name.
			name: "transfer_keep_alive call moved",
			mutate: func(m *stypes.Metadata) {
				m.Modules[1].Calls[1].LookupIndex = 4
			},
			wantErrIn: "transfer_keep_alive",
		},
		{
			name: "System success event missing",
			mutate: func(m *stypes.Metadata) {
				m.Modules[0].Events[0].Name = "RenamedSuccess"
			},
			wantErrIn: "ExtrinsicSuccess",
		},
		{
			name: "fee parameter missing",
			mutate: func(m *stypes.Metadata) {
				m.Modules[2].Events[0].ArgsName[1] = "renamed_fee"
			},
			wantErrIn: "actual_fee",
		},
		{
			// A pallet present on-chain but not in Indexes is silently
			// skipped. Pins the pallet-level "not configured = not validated"
			// rule — the chain may carry arbitrary extra pallets we don't
			// care about, and they must not produce false errors.
			name: "unknown pallet present",
			mutate: func(m *stypes.Metadata) {
				m.Modules = append(m.Modules, scaletypes.MetadataModules{
					Name: "Foo", Index: 99,
				})
			},
			wantErrIn: "",
		},
		{
			// A configured pallet missing from chain metadata leaves its
			// call/event keys in Indexes unresolved during the walk, so the
			// forward check at the end of ValidateMetadata flags them as
			// "not found in metadata".
			name: "required pallet missing",
			mutate: func(m *stypes.Metadata) {
				m.Modules = append(m.Modules[:1], m.Modules[2:]...) // drop Balances
			},
			wantErrIn: "Balances",
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
