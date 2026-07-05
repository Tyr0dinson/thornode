package core

import (
	"fmt"
	rand "math/rand/v2"
	"strings"
	"time"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

// XMRKeygenJailActor reproduces the chainnet XMR monovault keygen
// mass-jailing in the mocknet cluster. It requires the fault-injection
// environment prepared by `make test-simulation-keygen-jail`:
//
//   - symmetric netem latency on every bifrost's p2p port, so DKG transcript
//     verifications lag round payloads by one network hop, and
//   - XMR_FROST_KEYGEN_TEST_ROUND3_TIMEOUT_MS on one bifrost (the victim),
//     placing its round-3 wait inside that gap.
//
// The victim then abandons the DKG after sending everything its peers need,
// the peers reach 2/3 success consensus, and judgeLateSigner jails the victim
// for a failure it could never repair: its keygen failure vote carries a
// different tss ID than the success, so no same-ID release can ever fire (see
// TestXMRMonovaultKeygenFailureSubmitterStaysJailedAfterSuccessConsensus).
//
// The actor asserts the CURRENT (buggy) network behavior: the new XMR vault
// is created AND at least one member sits in keygen jail. The bifrost half of
// the fix (AdoptQuorumSuccess) already lets the victim adopt the quorum
// success, but at 4 nodes the 3 finishers reach on-chain supermajority before
// the victim's adopted vote can land (adoption costs netem-delayed p2p
// round-trips), so the victim is still jailed at consensus — and the
// MajorityConsensusBlockHeight clobber in x/thorchain (defect 2 of
// gitlab.com/thorchain/thornode issue #3092) then blocks its release.
//
// This assertion pins a KNOWN-OPEN bug on purpose and is tied to #3092: when
// the defect-2 consensus-lane fix (persist MajorityConsensusBlockHeight
// correctly) lands, flip it to "vault created and nobody stays jailed". Until
// then, note the assertion succeeds on "at least one victim"; if adoption
// timing shifts it could pass for the wrong reason (vault created but a
// different/extra member jailed), so the `len(jailed) > 1` case is logged
// loudly below rather than silently accepted.
type XMRKeygenJailActor struct {
	Actor

	vaultsBefore map[string]bool
}

// xmrKeygenJailReason is the jail reason set by judgeLateSigner in
// x/thorchain/handler_tss.go for members that did not sign the consensus
// keygen success.
const xmrKeygenJailReason = "failed to vote keygen in time"

func NewXMRKeygenJailActor(rng *rand.Rand) *Actor {
	a := &XMRKeygenJailActor{
		Actor: *NewActor("XMRKeygenJail", rng),
	}
	a.Timeout = 20 * time.Minute

	a.Ops = append(a.Ops, a.capturePreChurnVaults)
	a.Ops = append(a.Ops, a.startChurn)
	a.Ops = append(a.Ops, a.waitForKeygenJailSplit)

	return &a.Actor
}

func (a *XMRKeygenJailActor) capturePreChurnVaults(config *OpConfig) OpResult {
	vaults, err := thornode.GetVaults()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get vaults")
		return OpResult{Continue: false}
	}

	a.vaultsBefore = make(map[string]bool, len(vaults))
	for _, vault := range vaults {
		a.vaultsBefore[vault.GetPubKey()] = true
	}
	a.Log().Info().Int("vaults", len(a.vaultsBefore)).Msg("captured pre-churn vault set")
	return OpResult{Continue: true}
}

func (a *XMRKeygenJailActor) startChurn(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}

	activeNodes := 0
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes++
		}
	}
	if activeNodes > 1 {
		// The repro needs the 1->4 churn to schedule the monovault keygen; an
		// already-churned cluster cannot run it again deterministically.
		return OpResult{
			Finish: true,
			Error:  fmt.Errorf("cluster already churned (%d active nodes); reset the cluster to run the keygen-jail repro", activeNodes),
		}
	}

	if result := waitForXMRFrostChurnReadiness(a.Log(), config); !result.Continue {
		return result
	}

	node := config.NodeUsers[0]
	if !node.Acquire() {
		a.Log().Warn().Msg("failed to acquire node lock")
		return OpResult{Continue: false}
	}
	defer node.Release()

	accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get node thor address")
		return OpResult{Continue: false}
	}

	// Mirror the xmr-post-churn churn setup: expand 1->4 and keep the churn
	// retry beyond the FROST keygen timeout so a missed keygen gets retried.
	for _, mimir := range []struct {
		key   string
		value int64
	}{
		{"NUMBEROFNEWNODESPERCHURN", xmrPostChurnNewNodesPerChurn},
		{"CHURNRETRYINTERVAL", xmrPostChurnChurnRetryInterval},
		{"HALTCHURNING", 0},
	} {
		msg := thorchain.NewMsgMimir(mimir.key, mimir.value, accAddr)
		txid, broadcastErr := node.Thorchain.Broadcast(msg)
		if broadcastErr != nil {
			a.Log().Error().Err(broadcastErr).Str("key", mimir.key).Msg("failed to broadcast mimir")
			return OpResult{Continue: false}
		}
		a.Log().Info().
			Str("key", mimir.key).
			Int64("value", mimir.value).
			Str("txid", txid.String()).
			Msg("broadcasted mimir")
	}

	return OpResult{Continue: true}
}

func (a *XMRKeygenJailActor) waitForKeygenJailSplit(config *OpConfig) OpResult {
	vaults, err := thornode.GetVaults()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get vaults")
		return OpResult{Continue: false}
	}

	newXMRVault := ""
	for _, vault := range vaults {
		if a.vaultsBefore[vault.GetPubKey()] {
			continue
		}
		for _, address := range vault.Addresses {
			if address.Chain == common.XMRChain.String() && strings.TrimSpace(address.Address) != "" {
				newXMRVault = vault.GetPubKey()
				break
			}
		}
		if newXMRVault != "" {
			break
		}
	}

	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}

	var jailed []string
	for _, node := range nodes {
		if node.Jail.GetReason() == xmrKeygenJailReason {
			jailed = append(jailed, node.NodeAddress)
		}
	}

	if newXMRVault == "" || len(jailed) == 0 {
		a.Log().Info().
			Str("new_xmr_vault", newXMRVault).
			Strs("keygen_jailed", jailed).
			Msg("waiting for keygen success consensus and keygen jail")
		return OpResult{Continue: false}
	}

	if len(jailed) > 1 {
		a.Log().Warn().
			Strs("keygen_jailed", jailed).
			Msg("more than one member in keygen jail; expected a single victim")
	}

	a.Log().Info().
		Str("new_xmr_vault", newXMRVault).
		Strs("keygen_jailed", jailed).
		Msg("reproduced keygen mass-jailing split: success consensus reached while a participating member is jailed")
	return OpResult{Continue: true}
}
