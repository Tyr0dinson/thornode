package monero

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// runOutboundHaltConsolidation runs one consolidation pass with mimirs set and
// a single vault of vaultType, and returns the number of FrostKeySign calls.
func runOutboundHaltConsolidation(t *testing.T, mimirs map[string]int64, vaultType ttypes.VaultType) int {
	t.Helper()
	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		PubKeyFrost: vaultPubKey,
		Type:        vaultType,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}
	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
		mimir:       mimirs,
		constants: map[string]int64{
			constants.ChurnInterval.String(): 100,
		},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{{
			resp: tssp.FrostKeySignResponse{
				TxHash:    "xmr-sign-hash",
				TxHex:     "deadbeef",
				FeeAtomic: 1_000,
				MsgID:     "consolidation-msg-1",
				Status:    tsscommon.Success,
			},
		}},
	}
	sc := &consolidationSidecar{
		balanceByVault: map[string]sidecar.Balance{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]sidecar.Output{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sc, localNodePubKey, vaultPubKey, vaultAddr)
	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	return tssServer.callCount()
}

// XMR consolidation is a CONSOLIDATE self-send, so it keeps signing under
// HALTOUTBOUND / HaltOutboundXMR (HaltSigning still stops it).
func TestRunConsolidationPassSignsUnderHaltOutbound(t *testing.T) {
	t.Parallel()
	for _, mimirs := range []map[string]int64{
		nil,
		{constants.MimirKeyHaltOutbound: 1},
		{"HaltOutboundXMR": 1},
	} {
		if got := runOutboundHaltConsolidation(t, mimirs, ttypes.VaultType_AsgardVault); got != 1 {
			t.Fatalf("mimirs %v: expected one FrostKeySign call, got %d", mimirs, got)
		}
	}
	if got := runOutboundHaltConsolidation(t, map[string]int64{"HaltSigningXMR": 1}, ttypes.VaultType_AsgardVault); got != 0 {
		t.Fatalf("HaltSigningXMR must stop consolidation, got %d keysigns", got)
	}
}

// Fail closed: a destination that is not an Asgard vault address is external,
// so the pass signs nothing under HaltOutbound.
func TestRunConsolidationPassBlockedUnderHaltOutboundWhenNotAsgard(t *testing.T) {
	t.Parallel()
	if got := runOutboundHaltConsolidation(t, map[string]int64{"HaltOutboundXMR": 1}, ttypes.VaultType_UnknownVault); got != 0 {
		t.Fatalf("expected no FrostKeySign call, got %d", got)
	}
}
