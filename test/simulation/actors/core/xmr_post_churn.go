package core

import (
	"fmt"
	rand "math/rand/v2"
	"time"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

type XMRPostChurnActor struct {
	SwapActor

	inboundBefore common.Address
	inboundAfter  common.Address
}

const (
	xmrPostChurnNewNodesPerChurn = int64(3)
	// XMR/FROST keygen routinely takes longer than the mocknet churn retry window.
	// Expand the retry interval before triggering churn so THORNode still accepts
	// the resulting keygen tx instead of treating it as stale.
	xmrPostChurnChurnRetryInterval = int64(90)
)

func NewXMRPostChurnActor(rng *rand.Rand) *Actor {
	a := &XMRPostChurnActor{
		SwapActor: SwapActor{
			Actor: *NewActor("XMRPostChurn BTC.BTC => XMR.XMR", rng),
			from:  common.BTCAsset,
			to:    common.XMRAsset,
		},
	}
	a.Timeout = 15 * time.Minute

	a.Ops = append(a.Ops, a.captureActiveInbound)
	a.Ops = append(a.Ops, a.startChurn)
	a.Ops = append(a.Ops, a.waitForVaultRotation)
	a.Ops = append(a.Ops, a.acquireUser)
	a.Ops = append(a.Ops, a.getQuote)
	a.Ops = append(a.Ops, a.sendSwap)
	a.Ops = append(a.Ops, a.verifyOutbound)

	return &a.Actor
}

func (a *XMRPostChurnActor) captureActiveInbound(config *OpConfig) OpResult {
	inboundAddress, _, err := thornode.GetInboundAddress(common.XMRChain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get active XMR inbound address")
		return OpResult{Continue: false}
	}

	a.inboundBefore = inboundAddress
	a.Log().Info().Stringer("inbound_before", inboundAddress).Msg("captured pre-churn XMR inbound address")
	return OpResult{Continue: true}
}

func (a *XMRPostChurnActor) startChurn(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}

	activeNodes := map[string]bool{}
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes[node.NodeAddress] = true
		}
	}
	if len(activeNodes) > 1 {
		a.Log().Info().Msg("cluster already churned; waiting for XMR vault rotation")
		return OpResult{Continue: true}
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

	txid, err := a.broadcastMimir(node, accAddr, "NUMBEROFNEWNODESPERCHURN", xmrPostChurnNewNodesPerChurn)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast node expansion mimir")
		return OpResult{Continue: false}
	}
	a.Log().Info().
		Int64("value", xmrPostChurnNewNodesPerChurn).
		Str("txid", txid.String()).
		Msg("broadcasted node expansion mimir")

	txid, err = a.broadcastMimir(node, accAddr, "CHURNRETRYINTERVAL", xmrPostChurnChurnRetryInterval)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast churn retry mimir")
		return OpResult{Continue: false}
	}
	a.Log().Info().
		Int64("value", xmrPostChurnChurnRetryInterval).
		Str("txid", txid.String()).
		Msg("broadcasted churn retry mimir")

	txid, err = a.broadcastMimir(node, accAddr, "HALTCHURNING", 0)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast churn mimir")
		return OpResult{Continue: false}
	}

	a.Log().Info().Str("txid", txid.String()).Msg("broadcasted churn mimir")
	return OpResult{Continue: true}
}

func (a *XMRPostChurnActor) waitForVaultRotation(config *OpConfig) OpResult {
	network, err := thornode.GetNetwork()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get network")
		return OpResult{Continue: false}
	}

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
	if activeNodes <= 1 {
		a.Log().Info().Msg("waiting for churn to activate additional nodes")
		return OpResult{Continue: false}
	}

	inboundAddress, _, err := thornode.GetInboundAddress(common.XMRChain)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get XMR inbound address")
		return OpResult{Continue: false}
	}
	if inboundAddress.Equals(a.inboundBefore) {
		a.Log().Info().Stringer("inbound_address", inboundAddress).Msg("waiting for XMR inbound address rotation")
		return OpResult{Continue: false}
	}

	if !xmrVaultRotationReady(network, a.inboundBefore, inboundAddress, activeNodes) {
		a.Log().Info().Stringer("inbound_address", inboundAddress).Msg("waiting for active XMR vault funding")
		return OpResult{Continue: false}
	}
	if network.VaultsMigrating {
		a.Log().Info().Stringer("inbound_address", inboundAddress).Msg("XMR active vault rotated and funded; continuing despite pending migrations")
	}

	a.inboundAfter = inboundAddress
	a.Log().Info().
		Stringer("inbound_before", a.inboundBefore).
		Stringer("inbound_after", a.inboundAfter).
		Msg("XMR active vault rotated")
	return OpResult{Continue: true}
}

func xmrVaultRotationReady(network openapi.NetworkResponse, inboundBefore, inboundAfter common.Address, activeNodes int) bool {
	if activeNodes <= 1 {
		return false
	}
	if inboundAfter.IsEmpty() || inboundAfter.Equals(inboundBefore) {
		return false
	}
	return network.XmrActiveVaultReady
}

func (a *XMRPostChurnActor) broadcastMimir(node *User, accAddr cosmos.AccAddress, key string, value int64) (common.TxID, error) {
	mimir := thorchain.NewMsgMimir(key, value, accAddr)
	txid, err := node.Thorchain.Broadcast(mimir)
	if err != nil {
		return common.TxID(""), fmt.Errorf("broadcast %s=%d: %w", key, value, err)
	}
	return txid, nil
}
