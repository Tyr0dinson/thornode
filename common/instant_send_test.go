package common

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstantSendMetadata(t *testing.T) {
	early := ObservedTx{Tx: Tx{Chain: DASHChain}, BlockHeight: 100, FinaliseHeight: 100, InstantSend: &InstantSend{}}
	block := early
	block.InstantSend = nil
	require.NotEqual(t, early.AttestationUniqueHash(), block.AttestationUniqueHash())
	first, err := early.GetSignablePayloadWithInbound(true)
	require.NoError(t, err)
	second, err := block.GetSignablePayloadWithInbound(true)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	var restored ObservedTx
	require.NoError(t, restored.Unmarshal(first[:len(first)-1]))
	require.NotNil(t, restored.InstantSend)
	require.True(t, early.InstantSend.Equals(restored.InstantSend))
	require.False(t, early.InstantSend.Equals(nil))
	other := early
	other.Tx.Chain = Chain("BASH")
	require.NotEqual(t, early.AttestationUniqueHash(), other.AttestationUniqueHash())
}

func TestInstantSendStateValidationAndCopy(t *testing.T) {
	state := InstantSendState{Chain: DASHChain, PubKey: PubKey("vault"), Pending: []PendingInbound{{Id: TxID(strings.Repeat("A", 64)), Amount: 10, Height: 1}}}
	require.NoError(t, state.Validate())
	copy := state.Copy()
	copy.Pending[0].Amount = 0
	require.Error(t, copy.Validate())
	require.EqualValues(t, 10, state.Pending[0].Amount)
	copy = state.Copy()
	copy.Pending = append(copy.Pending, copy.Pending[0])
	require.Error(t, copy.Validate())
	copy = state.Copy()
	copy.Pending[0].Id = TxID(strings.Repeat("a", 64))
	require.Error(t, copy.Validate())
	copy = state.Copy()
	copy.Pending = make([]PendingInbound, InstantSendMaxPending+1)
	require.Error(t, copy.Validate())
}

func TestInstantSendPendingLimit(t *testing.T) {
	state := InstantSendState{Chain: DASHChain, PubKey: PubKey("vault")}
	for i := 1; i <= InstantSendMaxPending; i++ {
		state.Pending = append(state.Pending, PendingInbound{Id: TxID(fmt.Sprintf("%064X", i)), Amount: 1, Height: 1})
	}
	require.NoError(t, state.Validate())
	state.Pending = append(state.Pending, PendingInbound{Id: TxID(fmt.Sprintf("%064X", InstantSendMaxPending+1)), Amount: 1, Height: 1})
	require.EqualError(t, state.Validate(), "too many pending InstantSend inbounds")
}

func TestDashInputsAreStrictConsensusMetadata(t *testing.T) {
	first := OutputRef{TxHash: strings.Repeat("A", 64), OutputIndex: 1}
	second := first
	second.OutputIndex = 2
	require.NoError(t, ValidateDashInputs([]OutputRef{first, second}))
	for _, refs := range [][]OutputRef{nil, {second, first}, {first, first}, {{TxHash: strings.Repeat("a", 64)}}} {
		require.Error(t, ValidateDashInputs(refs))
	}
	a := ObservedTx{Tx: Tx{Chain: DASHChain}, SpentOutputRefs: []OutputRef{first}}
	b := a
	b.SpentOutputRefs = []OutputRef{second}
	require.False(t, a.HasEquivalentSpentOutputRefs(b))
	require.NotEqual(t, a.AttestationUniqueHash(), b.AttestationUniqueHash())
	b.SpentOutputRefs = nil
	require.False(t, a.HasEquivalentSpentOutputRefs(b))
	a.Tx.Chain = XMRChain
	b.Tx.Chain = XMRChain
	require.True(t, a.HasEquivalentSpentOutputRefs(b))
}

func TestInstantSendAnchorSteps(t *testing.T) {
	for _, tc := range []struct{ height, anchor int64 }{
		{-1, 0}, {0, 0}, {1, 1}, {11, 1}, {12, 12}, {23, 12}, {24, 24}, {95, 84}, {96, 96}, {107, 96}, {108, 108},
	} {
		require.Equal(t, tc.anchor, InstantSendAnchor(tc.height))
		if tc.height > 0 {
			require.LessOrEqual(t, tc.height-InstantSendAnchor(tc.height), InstantSendAnchorWindow)
		}
	}
}

func TestInstantSendOverdueAllowanceKeepsExposure(t *testing.T) {
	s := InstantSendState{Pending: []PendingInbound{{Id: "A", Amount: 3}, {Id: "B", Amount: 7}}}
	require.Equal(t, uint64(10), s.PendingAmount().Uint64())
	require.Equal(t, uint64(10), s.SolvencyAllowance(nil).Uint64())
	require.Equal(t, uint64(7), s.SolvencyAllowance([]TxID{"A"}).Uint64())
	require.Equal(t, uint64(0), s.SolvencyAllowance([]TxID{"A", "B"}).Uint64())
	require.Equal(t, uint64(10), s.PendingAmount().Uint64())
}
