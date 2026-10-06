package signer

import (
	"testing"
	"time"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// Catch-up delivers old keygen blocks only once. Even a first admission must
// reject a result window that has already closed, before starting any DKG.
func TestXMRKeygenInitialValidity(t *testing.T) {
	minimum := int64((6*time.Minute + constants.ThorchainBlockTime - 1) / constants.ThorchainBlockTime)
	for _, test := range []struct {
		name           string
		churn, elapsed int64
		wantRuns       int
	}{
		{"current", 1000, 0, 1},
		{"inclusive expiry", 1000, 1000, 1},
		{"expired first delivery", 1000, 1001, 0},
		{"unset default boundary", -1, constants.NewConstantValue().GetInt64Value(constants.ChurnRetryInterval), 1},
		{"unset default expired", -1, constants.NewConstantValue().GetInt64Value(constants.ChurnRetryInterval) + 1, 0},
		{"XMR minimum boundary", 1, minimum, 1},
		{"XMR minimum expired", 1, minimum + 1, 0},
		{"zero window same block", 0, 0, 1},
		{"zero window next block", 0, 1, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			signer, bridge, client := replacementTestSigner(t, nil)
			client.releaseFirst()
			block := newSingleFlightXMRKeygen(t, 2700)
			bridge.setMimir(constants.ChurnRetryInterval.String(), test.churn)
			bridge.setMimir(constants.KeygenRetryInterval.String(), 0)
			bridge.setHeight(block.Height + test.elapsed)
			signer.processKeygenBlock(block)
			if client.callCount() != test.wantRuns || bridge.broadcastCount() != test.wantRuns {
				t.Fatalf("DKG/result counts %d/%d, want %d", client.callCount(), bridge.broadcastCount(), test.wantRuns)
			}
			if signer.xmrKeygen.active != nil || signer.xmrKeygen.pending != nil {
				t.Fatal("initial admission retained completed or expired work")
			}
			if bridge.mimirReadCount(constants.KeygenRetryInterval.String()) != 0 {
				t.Fatal("initial admission depended on retry enablement")
			}
			if test.wantRuns == 0 {
				fresh := newSingleFlightXMRKeygen(t, block.Height+test.elapsed)
				signer.processKeygenBlock(fresh)
				if client.callCount() != 1 || bridge.broadcastCount() != 1 {
					t.Fatal("expired initial request prevented a later fresh ceremony")
				}
			}
		})
	}
}

// Rejecting an expired XMR request must let the serial consumer reach its
// same-block Asgard companion, without starting XMR or changing Asgard policy.
func TestExpiredInitialXMRDoesNotDelayAsgard(t *testing.T) {
	signer, bridge, client := replacementTestSigner(t, nil)
	signer.tssKeygen = &tss.KeyGen{}
	bridge.setHeight(3701)
	block := newSingleFlightXMRKeygen(t, 2700)
	block.Keygens = append(block.Keygens, ttypes.Keygen{Type: ttypes.KeygenType_AsgardKeygen})
	done := make(chan struct{})
	go func() {
		defer close(done)
		signer.processKeygenBlock(block)
	}()
	waitForSingleFlightReturn(t, done, "Asgard after expired initial XMR")
	// The empty Asgard key takes its existing failure/retry path. One window
	// read for XMR admission and one for Asgard retries prove both decisions.
	if client.callCount() != 0 || bridge.mimirReadCount(constants.ChurnRetryInterval.String()) != 2 {
		t.Fatal("expired XMR started DKG or prevented ordinary Asgard processing")
	}
}
