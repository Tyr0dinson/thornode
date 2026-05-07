package features

import (
	"fmt"
	rand "math/rand/v2"
	"strings"
	"time"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	bondYieldAutoClaimBps              = uint64(4000)
	bondYieldFullAutoClaimBps          = uint64(10000)
	bondYieldClaimCapKey               = "BondYieldClaimableBpsCap"
	bondYieldClaimCap                  = int64(bondYieldAutoClaimBps)
	bondYieldMinL1OutboundFeeUSDKey    = "MinimumL1OutboundFeeUSD"
	bondYieldMinL1OutboundFeeUSD       = int64(0)
	bondYieldMinOutboundFeeMultKey     = "MinOutboundFeeMultiplierBasisPoints"
	bondYieldMinOutboundFeeMultBps     = int64(1)
	bondYieldTHORNameFundRune          = uint64(50 * common.One)
	bondYieldPreferredAssetHighMultBps = int64(10000)
	bondYieldPreferredAssetLowMultBps  = int64(1)
	bondYieldPreferredAssetFeeSize     = uint64(1)
	bondYieldPreferredAssetFeeRate     = uint64(1)
	bondYieldPreferredAssetFeeHeight   = int64(1)
)

type BondYieldState struct {
	node                     *User
	owner                    *User
	rewardPrimerNode         *User
	thresholdNode            *User
	nodeAddress              cosmos.AccAddress
	rewardPrimerAddress      cosmos.AccAddress
	thresholdNodeAddress     cosmos.AccAddress
	providerAddress          common.Address
	thresholdProviderAddress common.Address
	payoutAsset              common.Asset
	payoutName               string
	initialCollectorRune     cosmos.Uint
	collectorAfterAutoClaim  cosmos.Uint
	collectorBeforeThreshold cosmos.Uint
	manualActiveClaimable    cosmos.Uint
	manualActiveClaimAmount  cosmos.Uint
	manualActiveBalance      cosmos.Uint
	churnedOutClaimable      cosmos.Uint
	churnedOutClaimAmount    cosmos.Uint
	churnedOutBalance        cosmos.Uint
	unpaidYieldBeforeChurnIn cosmos.Uint
	bondBeforeChurnIn        cosmos.Uint
	rewardPrimerRequested    bool
	skipReason               string
}

func NewBondYieldState() *BondYieldState {
	return &BondYieldState{
		payoutAsset: common.ETHAsset,
	}
}

type BondYieldSetupActor struct {
	Actor
	state *BondYieldState
}

func NewBondYieldSetupActor(state *BondYieldState, rng *rand.Rand) *Actor {
	a := &BondYieldSetupActor{
		Actor: *NewActor("BondYield-Setup", rng),
		state: state,
	}
	a.Timeout = 3 * time.Minute
	a.Ops = append(a.Ops, a.requireMocknetCluster)
	a.Ops = append(a.Ops, a.acquireActiveNodeAndOwner)
	a.Ops = append(a.Ops, a.setClaimableCap)
	a.Ops = append(a.Ops, a.setLowOutboundFeeMimirs)
	a.Ops = append(a.Ops, a.waitBondYieldMimirs)
	a.Ops = append(a.Ops, a.registerPayoutTHORName)
	a.Ops = append(a.Ops, a.waitPayoutTHORName)
	return &a.Actor
}

type BondYieldSelectTargetActor struct {
	Actor
	state *BondYieldState
}

func NewBondYieldSelectTargetActor(state *BondYieldState, rng *rand.Rand) *Actor {
	a := &BondYieldSelectTargetActor{
		Actor: *NewActor("BondYield-SelectTarget", rng),
		state: state,
	}
	a.Timeout = 2 * time.Minute
	a.Ops = append(a.Ops, a.skipIfRequired)
	a.Ops = append(a.Ops, a.selectActiveTargetWithYield)
	return &a.Actor
}

type BondYieldPrimeRewardsActor struct {
	Actor
	state *BondYieldState
}

func NewBondYieldPrimeRewardsActor(state *BondYieldState, rng *rand.Rand) *Actor {
	a := &BondYieldPrimeRewardsActor{
		Actor: *NewActor("BondYield-PrimeRewards", rng),
		state: state,
	}
	a.Timeout = 8 * time.Minute
	a.Ops = append(a.Ops, a.skipIfRequired)
	a.Ops = append(a.Ops, a.selectTargetAndPrimeRewards)
	a.Ops = append(a.Ops, a.waitPrimedTargetClaimable)
	return &a.Actor
}

type BondYieldManualActiveActor struct {
	Actor
	state *BondYieldState
}

func NewBondYieldManualActiveActor(state *BondYieldState, rng *rand.Rand) *Actor {
	a := &BondYieldManualActiveActor{
		Actor: *NewActor("BondYield-ManualActive", rng),
		state: state,
	}
	a.Timeout = 2 * time.Minute
	a.Ops = append(a.Ops, a.skipIfRequired)
	a.Ops = append(a.Ops, a.waitActiveClaimableYield)
	a.Ops = append(a.Ops, a.claimActiveYield)
	a.Ops = append(a.Ops, a.verifyActiveManualClaim)
	return &a.Actor
}

type BondYieldTargetAutoClaimActor struct {
	Actor
	state *BondYieldState
}

func NewBondYieldTargetAutoClaimActor(state *BondYieldState, rng *rand.Rand) *Actor {
	a := &BondYieldTargetAutoClaimActor{
		Actor: *NewActor("BondYield-TargetAutoClaim", rng),
		state: state,
	}
	a.Timeout = 2 * time.Minute
	a.Ops = append(a.Ops, a.skipIfRequired)
	a.Ops = append(a.Ops, a.configureTargetAutoClaim)
	a.Ops = append(a.Ops, a.waitTargetAutoClaimConfigured)
	return &a.Actor
}

type BondYieldChurnOutActor struct {
	Actor
	state *BondYieldState
}

func NewBondYieldChurnOutActor(state *BondYieldState, rng *rand.Rand) *Actor {
	a := &BondYieldChurnOutActor{
		Actor: *NewActor("BondYield-ChurnOut", rng),
		state: state,
	}
	a.Timeout = 10 * time.Minute
	a.Ops = append(a.Ops, a.skipIfRequired)
	a.Ops = append(a.Ops, a.requestTargetLeave)
	a.Ops = append(a.Ops, a.waitTargetChurnedOut)
	return &a.Actor
}

type BondYieldManualChurnedOutActor struct {
	Actor
	state *BondYieldState
}

func NewBondYieldManualChurnedOutActor(state *BondYieldState, rng *rand.Rand) *Actor {
	a := &BondYieldManualChurnedOutActor{
		Actor: *NewActor("BondYield-ManualChurnedOut", rng),
		state: state,
	}
	a.Timeout = 3 * time.Minute
	a.Ops = append(a.Ops, a.skipIfRequired)
	a.Ops = append(a.Ops, a.disableTargetAutoClaim)
	a.Ops = append(a.Ops, a.waitTargetAutoClaimDisabled)
	a.Ops = append(a.Ops, a.waitChurnedOutClaimableYield)
	a.Ops = append(a.Ops, a.claimChurnedOutYield)
	a.Ops = append(a.Ops, a.verifyChurnedOutManualClaim)
	return &a.Actor
}

type BondYieldThresholdChurnInActor struct {
	Actor
	state *BondYieldState
}

func NewBondYieldThresholdChurnInActor(state *BondYieldState, rng *rand.Rand) *Actor {
	a := &BondYieldThresholdChurnInActor{
		Actor: *NewActor("BondYield-ThresholdChurnIn", rng),
		state: state,
	}
	a.Timeout = 8 * time.Minute
	a.Ops = append(a.Ops, a.skipIfRequired)
	a.Ops = append(a.Ops, a.selectThresholdNode)
	a.Ops = append(a.Ops, a.postLowPreferredAssetNetworkFee)
	a.Ops = append(a.Ops, a.lowerPayoutTHORNameThreshold)
	a.Ops = append(a.Ops, a.waitPayoutTHORNameLowThreshold)
	a.Ops = append(a.Ops, a.configureThresholdNodeAutoClaim)
	a.Ops = append(a.Ops, a.waitThresholdNodeAutoClaimConfigured)
	a.Ops = append(a.Ops, a.requestThresholdNodeLeave)
	a.Ops = append(a.Ops, a.waitTargetChurnedInAndThresholdPaid)
	return &a.Actor
}

type bondYieldNode struct {
	Status            string `json:"status"`
	TotalBond         string `json:"total_bond"`
	RequestedToLeave  bool   `json:"requested_to_leave"`
	CurrentAward      string `json:"current_award"`
	SlashPoints       int64  `json:"slash_points"`
	ActiveBlockHeight int64  `json:"active_block_height"`
	BondProviders     struct {
		Providers []bondYieldProvider `json:"providers"`
	} `json:"bond_providers"`
}

type bondYieldProvider struct {
	BondAddress    string `json:"bond_address"`
	Bond           string `json:"bond"`
	Principal      string `json:"principal"`
	ClaimableYield string `json:"claimable_yield"`
	AutoClaimBps   uint64 `json:"auto_claim_bps"`
	PayoutAddress  string `json:"payout_address"`
}

type bondYieldTHORName struct {
	Owner                               string               `json:"owner"`
	PreferredAsset                      string               `json:"preferred_asset"`
	PreferredAssetSwapThresholdRune     string               `json:"preferred_asset_swap_threshold_rune"`
	PreferredAssetOutboundFeeMultiplier int64                `json:"preferred_asset_outbound_fee_multiplier"`
	AffiliateCollectorRune              string               `json:"affiliate_collector_rune"`
	Aliases                             []bondYieldAliasInfo `json:"aliases"`
}

type bondYieldAliasInfo struct {
	Chain   string `json:"chain"`
	Address string `json:"address"`
}

type bondYieldRewardCandidate struct {
	user            *User
	addr            cosmos.AccAddress
	providerAddress common.Address
	node            bondYieldNode
	provider        bondYieldProvider
	claimable       cosmos.Uint
	currentAward    cosmos.Uint
}

func (a *BondYieldSetupActor) requireMocknetCluster(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}
	if len(nodes) < 2 {
		a.state.skipReason = fmt.Sprintf("mocknet-cluster not detected: node_count=%d", len(nodes))
		a.Log().Info().Str("reason", a.state.skipReason).Msg("skipping bond-yield test")
		return OpResult{Finish: true}
	}
	return OpResult{Continue: true}
}

func (a *BondYieldSelectTargetActor) skipIfRequired(config *OpConfig) OpResult {
	return skipBondYieldIfRequired(a.Actor, a.state)
}

func (a *BondYieldPrimeRewardsActor) skipIfRequired(config *OpConfig) OpResult {
	return skipBondYieldIfRequired(a.Actor, a.state)
}

func (a *BondYieldManualActiveActor) skipIfRequired(config *OpConfig) OpResult {
	return skipBondYieldIfRequired(a.Actor, a.state)
}

func (a *BondYieldTargetAutoClaimActor) skipIfRequired(config *OpConfig) OpResult {
	return skipBondYieldIfRequired(a.Actor, a.state)
}

func (a *BondYieldChurnOutActor) skipIfRequired(config *OpConfig) OpResult {
	return skipBondYieldIfRequired(a.Actor, a.state)
}

func (a *BondYieldManualChurnedOutActor) skipIfRequired(config *OpConfig) OpResult {
	return skipBondYieldIfRequired(a.Actor, a.state)
}

func (a *BondYieldThresholdChurnInActor) skipIfRequired(config *OpConfig) OpResult {
	return skipBondYieldIfRequired(a.Actor, a.state)
}

func (a *BondYieldSetupActor) acquireActiveNodeAndOwner(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}

	activeNodes := make(map[string]bool)
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes[node.NodeAddress] = true
		}
	}
	if len(activeNodes) != 1 {
		a.Log().Info().Int("active_nodes", len(activeNodes)).Msg("waiting for single active node before bond-yield churn test")
		return OpResult{Continue: false}
	}

	for _, node := range config.NodeUsers {
		accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get node THOR address")
			return OpResult{Continue: false}
		}
		if !activeNodes[accAddr.String()] {
			continue
		}
		a.state.node = node
		a.state.nodeAddress = accAddr
		a.state.providerAddress = common.Address(accAddr.String())
		break
	}
	if a.state.node == nil {
		a.Log().Info().Msg("waiting for active node user")
		return OpResult{Continue: false}
	}

	for _, user := range config.Users {
		if user.IsLocked() {
			continue
		}
		balances, err := thornode.GetBalances(user.Address(common.THORChain))
		if err != nil {
			a.Log().Error().Err(err).Str("user", user.Name()).Msg("failed to get user balance")
			continue
		}
		if balances.GetCoin(common.RuneAsset()).Amount.LT(cosmos.NewUint(bondYieldTHORNameFundRune)) {
			continue
		}

		a.state.owner = user
		a.state.payoutName = fmt.Sprintf("simyield%s", user.Name())
		a.Log().Info().
			Stringer("node", a.state.nodeAddress).
			Str("owner", user.Name()).
			Str("thorname", a.state.payoutName).
			Msg("selected active node bond provider and THORName owner")
		return OpResult{Continue: true}
	}

	a.Log().Info().Msg("waiting for THORName owner user")
	return OpResult{Continue: false}
}

func (a *BondYieldSetupActor) setClaimableCap(config *OpConfig) OpResult {
	return a.broadcastMimir(bondYieldClaimCapKey, bondYieldClaimCap)
}

func (a *BondYieldSetupActor) setLowOutboundFeeMimirs(config *OpConfig) OpResult {
	for key, value := range map[string]int64{
		bondYieldMinL1OutboundFeeUSDKey: bondYieldMinL1OutboundFeeUSD,
		bondYieldMinOutboundFeeMultKey:  bondYieldMinOutboundFeeMultBps,
	} {
		result := a.broadcastMimir(key, value)
		if !result.Continue && !result.Finish {
			return result
		}
	}
	return OpResult{Continue: true}
}

func (a *BondYieldSetupActor) waitBondYieldMimirs(config *OpConfig) OpResult {
	mimirs, err := thornode.GetMimirs()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get mimirs")
		return OpResult{Continue: false}
	}

	expected := map[string]int64{
		bondYieldClaimCapKey:            bondYieldClaimCap,
		bondYieldMinL1OutboundFeeUSDKey: bondYieldMinL1OutboundFeeUSD,
		bondYieldMinOutboundFeeMultKey:  bondYieldMinOutboundFeeMultBps,
	}
	for key, want := range expected {
		got, ok := getMimirCaseInsensitive(mimirs, key)
		if !ok || got != want {
			a.Log().Info().Str("key", key).Int64("want", want).Int64("got", got).Bool("found", ok).Msg("waiting for bond-yield mimir")
			return OpResult{Continue: false}
		}
	}

	return OpResult{Continue: true}
}

func (a *BondYieldSetupActor) registerPayoutTHORName(config *OpConfig) OpResult {
	txid, memo, err := broadcastPayoutTHORName(a.state, cosmos.NewUint(bondYieldTHORNameFundRune), bondYieldPreferredAssetHighMultBps)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to register payout THORName")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("memo", memo).Msg("broadcasted payout THORName registration")
	return OpResult{Continue: true}
}

func (a *BondYieldSetupActor) waitPayoutTHORName(config *OpConfig) OpResult {
	tn, err := getBondYieldTHORName(a.state.payoutName)
	if err != nil {
		a.Log().Info().Err(err).Str("thorname", a.state.payoutName).Msg("waiting for payout THORName")
		return OpResult{Continue: false}
	}
	if !hasAlias(tn, a.state.payoutAsset.GetChain(), a.state.owner.Address(a.state.payoutAsset.GetChain())) {
		a.Log().Info().Str("thorname", a.state.payoutName).Msg("waiting for payout THORName asset alias")
		return OpResult{Continue: false}
	}
	if tn.PreferredAssetOutboundFeeMultiplier != bondYieldPreferredAssetHighMultBps {
		a.Log().Info().Int64("multiplier", tn.PreferredAssetOutboundFeeMultiplier).Msg("waiting for high THORName payout multiplier")
		return OpResult{Continue: false}
	}

	a.state.initialCollectorRune = bondYieldUint(tn.AffiliateCollectorRune)
	a.Log().Info().Str("initial_collector_rune", a.state.initialCollectorRune.String()).Msg("payout THORName ready")
	return OpResult{Continue: true}
}

func (a *BondYieldSelectTargetActor) selectActiveTargetWithYield(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}

	activeNodes := make(map[string]bool)
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes[node.NodeAddress] = true
		}
	}

	for _, node := range config.NodeUsers {
		accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get node THOR address")
			return OpResult{Continue: false}
		}
		if !activeNodes[accAddr.String()] {
			continue
		}

		providerAddress := common.Address(accAddr.String())
		_, provider, ok, err := getBondYieldNodeProvider(accAddr, providerAddress.String())
		if err != nil {
			a.Log().Error().Err(err).Stringer("node", accAddr).Msg("failed to query active target provider")
			continue
		}
		claimable := bondYieldUint(provider.ClaimableYield)
		if !ok || claimable.LTE(cosmos.NewUint(1)) {
			continue
		}

		a.state.node = node
		a.state.nodeAddress = accAddr
		a.state.providerAddress = providerAddress
		a.Log().Info().
			Stringer("node", a.state.nodeAddress).
			Str("claimable_yield", claimable.String()).
			Msg("selected active node for bond-yield payout checks")
		return OpResult{Continue: true}
	}

	a.Log().Info().Int("active_nodes", len(activeNodes)).Msg("waiting for active node with claimable yield")
	return OpResult{Continue: false}
}

func (a *BondYieldPrimeRewardsActor) selectTargetAndPrimeRewards(config *OpConfig) OpResult {
	candidates, err := a.activeRewardCandidates(config)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query active reward candidates")
		return OpResult{Continue: false}
	}

	var target *bondYieldRewardCandidate
	for i := range candidates {
		if candidates[i].claimable.GT(cosmos.NewUint(1)) {
			target = &candidates[i]
			break
		}
	}
	if target != nil {
		a.setTarget(*target)
		a.Log().Info().
			Stringer("node", a.state.nodeAddress).
			Str("claimable_yield", target.claimable.String()).
			Msg("active target already has claimable bond yield")
		return OpResult{Continue: true}
	}

	for i := range candidates {
		if candidates[i].currentAward.GT(cosmos.NewUint(1)) {
			target = &candidates[i]
			break
		}
	}
	if target == nil {
		a.Log().Info().Int("active_nodes", len(candidates)).Msg("waiting for active node with current bond award")
		return OpResult{Continue: false}
	}

	var primer *bondYieldRewardCandidate
	for i := range candidates {
		if !candidates[i].addr.Equals(target.addr) {
			primer = &candidates[i]
			break
		}
	}
	if primer == nil {
		a.Log().Info().Msg("waiting for separate active node to prime reward distribution")
		return OpResult{Continue: false}
	}

	a.setTarget(*target)
	a.state.rewardPrimerNode = primer.user
	a.state.rewardPrimerAddress = primer.addr

	txid, err := broadcastLeave(a.state.rewardPrimerNode, a.state.rewardPrimerAddress)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast reward-primer leave")
		return OpResult{Continue: false}
	}
	a.state.rewardPrimerRequested = true
	a.Log().Info().
		Str("txid", txid).
		Stringer("target", a.state.nodeAddress).
		Str("target_current_award", target.currentAward.String()).
		Int64("target_slash_points", target.node.SlashPoints).
		Stringer("primer", a.state.rewardPrimerAddress).
		Msg("broadcasted reward-primer leave to persist target claimable yield on next churn")
	return OpResult{Continue: true}
}

func (a *BondYieldPrimeRewardsActor) waitPrimedTargetClaimable(config *OpConfig) OpResult {
	node, provider, ok, err := getBondYieldNodeProvider(a.state.nodeAddress, a.state.providerAddress.String())
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query primed target node")
		return OpResult{Continue: false}
	}
	claimable := bondYieldUint(provider.ClaimableYield)
	if ok && node.Status == atypes.NodeStatus_Active.String() && claimable.GT(cosmos.NewUint(1)) {
		a.Log().Info().
			Stringer("node", a.state.nodeAddress).
			Str("claimable_yield", claimable.String()).
			Msg("primed active target has claimable bond yield")
		return OpResult{Continue: true}
	}

	candidates, err := a.activeRewardCandidates(config)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query fallback active reward candidates")
		return OpResult{Continue: false}
	}
	for i := range candidates {
		if candidates[i].claimable.GT(cosmos.NewUint(1)) {
			a.setTarget(candidates[i])
			a.Log().Info().
				Stringer("node", a.state.nodeAddress).
				Str("claimable_yield", candidates[i].claimable.String()).
				Msg("selected fallback active target with claimable bond yield")
			return OpResult{Continue: true}
		}
	}

	a.Log().Info().
		Stringer("target", a.state.nodeAddress).
		Str("status", node.Status).
		Bool("primer_requested", a.state.rewardPrimerRequested).
		Str("claimable_yield", claimable.String()).
		Msg("waiting for reward-primer churn to persist target claimable yield")
	return OpResult{Continue: false}
}

func (a *BondYieldPrimeRewardsActor) activeRewardCandidates(config *OpConfig) ([]bondYieldRewardCandidate, error) {
	nodes, err := thornode.GetNodes()
	if err != nil {
		return nil, err
	}

	activeNodes := make(map[string]bool)
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes[node.NodeAddress] = true
		}
	}

	candidates := make([]bondYieldRewardCandidate, 0, len(activeNodes))
	for _, user := range config.NodeUsers {
		accAddr, err := user.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			return nil, err
		}
		if !activeNodes[accAddr.String()] {
			continue
		}

		providerAddress := common.Address(accAddr.String())
		node, provider, ok, err := getBondYieldNodeProvider(accAddr, providerAddress.String())
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		candidates = append(candidates, bondYieldRewardCandidate{
			user:            user,
			addr:            accAddr,
			providerAddress: providerAddress,
			node:            node,
			provider:        provider,
			claimable:       bondYieldUint(provider.ClaimableYield),
			currentAward:    bondYieldUint(node.CurrentAward),
		})
	}

	return candidates, nil
}

func (a *BondYieldPrimeRewardsActor) setTarget(candidate bondYieldRewardCandidate) {
	a.state.node = candidate.user
	a.state.nodeAddress = candidate.addr
	a.state.providerAddress = candidate.providerAddress
}

func (a *BondYieldManualActiveActor) waitActiveClaimableYield(config *OpConfig) OpResult {
	node, provider, ok, err := getBondYieldNodeProvider(a.state.nodeAddress, a.state.providerAddress.String())
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query target bond provider")
		return OpResult{Continue: false}
	}
	if !ok {
		a.Log().Info().Stringer("provider", a.state.providerAddress).Msg("waiting for target bond provider")
		return OpResult{Continue: false}
	}
	if node.Status != atypes.NodeStatus_Active.String() {
		a.Log().Info().Str("status", node.Status).Msg("waiting for target node to remain active after first churn")
		return OpResult{Continue: false}
	}

	claimable := bondYieldUint(provider.ClaimableYield)
	if claimable.LTE(cosmos.NewUint(1)) {
		a.Log().Info().Str("claimable_yield", claimable.String()).Msg("waiting for active target claimable yield")
		return OpResult{Continue: false}
	}

	balance, err := getRuneBalance(a.state.providerAddress)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get target provider balance")
		return OpResult{Continue: false}
	}

	a.state.manualActiveClaimable = claimable
	a.state.manualActiveClaimAmount = claimable.QuoUint64(2)
	if a.state.manualActiveClaimAmount.IsZero() {
		a.state.manualActiveClaimAmount = cosmos.NewUint(1)
	}
	a.state.manualActiveBalance = balance
	a.Log().Info().
		Str("claimable_yield", claimable.String()).
		Str("claim_amount", a.state.manualActiveClaimAmount.String()).
		Msg("active target has claimable bond yield")
	return OpResult{Continue: true}
}

func (a *BondYieldManualActiveActor) claimActiveYield(config *OpConfig) OpResult {
	txid, err := broadcastBondYieldClaim(a.state.node, a.state.nodeAddress, a.state.manualActiveClaimAmount)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast active manual bond-yield claim")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("claim_amount", a.state.manualActiveClaimAmount.String()).Msg("broadcasted active manual bond-yield claim")
	return OpResult{Continue: true}
}

func (a *BondYieldManualActiveActor) verifyActiveManualClaim(config *OpConfig) OpResult {
	_, provider, ok, err := getBondYieldNodeProvider(a.state.nodeAddress, a.state.providerAddress.String())
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query target bond provider")
		return OpResult{Continue: false}
	}
	if !ok {
		a.Log().Info().Stringer("provider", a.state.providerAddress).Msg("waiting for target bond provider")
		return OpResult{Continue: false}
	}
	balance, err := getRuneBalance(a.state.providerAddress)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get target provider balance")
		return OpResult{Continue: false}
	}

	claimable := bondYieldUint(provider.ClaimableYield)
	expectedClaimable := a.state.manualActiveClaimable.Sub(a.state.manualActiveClaimAmount)
	if claimable.LTE(expectedClaimable) {
		a.Log().Info().
			Str("balance_before", a.state.manualActiveBalance.String()).
			Str("balance_after", balance.String()).
			Str("claimable_before", a.state.manualActiveClaimable.String()).
			Str("claimable_after", claimable.String()).
			Msg("verified manual bond-yield payout while node is active")
		return OpResult{Continue: true}
	}

	a.Log().Info().
		Str("balance_before", a.state.manualActiveBalance.String()).
		Str("balance_after", balance.String()).
		Str("claimable_before", a.state.manualActiveClaimable.String()).
		Str("claimable_after", claimable.String()).
		Msg("waiting for active manual bond-yield payout")
	return OpResult{Continue: false}
}

func (a *BondYieldTargetAutoClaimActor) configureTargetAutoClaim(config *OpConfig) OpResult {
	txid, memo, err := broadcastBondYieldAutoClaim(a.state.node, a.state.nodeAddress, bondYieldAutoClaimBps, a.state.payoutName)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to configure target bond-yield auto-claim")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("memo", memo).Msg("broadcasted target bond-yield auto-claim")
	return OpResult{Continue: true}
}

func (a *BondYieldTargetAutoClaimActor) waitTargetAutoClaimConfigured(config *OpConfig) OpResult {
	return waitBondYieldAutoClaim(a.Actor, a.state.nodeAddress, a.state.providerAddress.String(), bondYieldAutoClaimBps, a.state.payoutName)
}

func (a *BondYieldChurnOutActor) requestTargetLeave(config *OpConfig) OpResult {
	txid, err := broadcastLeave(a.state.node, a.state.nodeAddress)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast target leave")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Stringer("node", a.state.nodeAddress).Msg("broadcasted target leave")
	return OpResult{Continue: true}
}

func (a *BondYieldChurnOutActor) waitTargetChurnedOut(config *OpConfig) OpResult {
	node, provider, ok, err := getBondYieldNodeProvider(a.state.nodeAddress, a.state.providerAddress.String())
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query target node")
		return OpResult{Continue: false}
	}
	if !ok {
		a.Log().Info().Stringer("provider", a.state.providerAddress).Msg("waiting for target bond provider")
		return OpResult{Continue: false}
	}
	if node.Status != atypes.NodeStatus_Standby.String() {
		a.Log().Info().Str("status", node.Status).Bool("requested_to_leave", node.RequestedToLeave).Msg("waiting for target node to churn out")
		return OpResult{Continue: false}
	}

	network, err := thornode.GetNetwork()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get network")
		return OpResult{Continue: false}
	}
	if network.VaultsMigrating {
		a.Log().Info().Msg("waiting for churn-out migration to finish")
		return OpResult{Continue: false}
	}

	tn, err := getBondYieldTHORName(a.state.payoutName)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query payout THORName")
		return OpResult{Continue: false}
	}
	currentRune := bondYieldUint(tn.AffiliateCollectorRune)
	claimable := bondYieldUint(provider.ClaimableYield)
	if currentRune.GT(a.state.initialCollectorRune) && claimable.GT(cosmos.NewUint(1)) {
		a.state.collectorAfterAutoClaim = currentRune
		a.Log().Info().
			Str("collector_rune", currentRune.String()).
			Str("claimable_yield", claimable.String()).
			Msg("verified automatic bond-yield payout to affiliate collector during target churn-out")
		return OpResult{Continue: true}
	}

	a.Log().Info().
		Str("collector_rune", currentRune.String()).
		Str("initial_collector_rune", a.state.initialCollectorRune.String()).
		Str("claimable_yield", claimable.String()).
		Msg("waiting for target churn-out auto-claim and remaining claimable yield")
	return OpResult{Continue: false}
}

func (a *BondYieldManualChurnedOutActor) disableTargetAutoClaim(config *OpConfig) OpResult {
	txid, memo, err := broadcastBondYieldAutoClaim(a.state.node, a.state.nodeAddress, 0, "")
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to disable target bond-yield auto-claim")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("memo", memo).Msg("broadcasted target bond-yield auto-claim disable")
	return OpResult{Continue: true}
}

func (a *BondYieldManualChurnedOutActor) waitTargetAutoClaimDisabled(config *OpConfig) OpResult {
	return waitBondYieldAutoClaim(a.Actor, a.state.nodeAddress, a.state.providerAddress.String(), 0, "")
}

func (a *BondYieldManualChurnedOutActor) waitChurnedOutClaimableYield(config *OpConfig) OpResult {
	node, provider, ok, err := getBondYieldNodeProvider(a.state.nodeAddress, a.state.providerAddress.String())
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query target bond provider")
		return OpResult{Continue: false}
	}
	if !ok {
		a.Log().Info().Stringer("provider", a.state.providerAddress).Msg("waiting for target bond provider")
		return OpResult{Continue: false}
	}
	if node.Status == atypes.NodeStatus_Active.String() {
		a.Log().Info().Msg("waiting for target node to be churned out before manual claim")
		return OpResult{Continue: false}
	}

	claimable := bondYieldUint(provider.ClaimableYield)
	if claimable.LTE(cosmos.NewUint(1)) {
		a.Log().Info().Str("claimable_yield", claimable.String()).Msg("waiting for churned-out target claimable yield")
		return OpResult{Continue: false}
	}

	balance, err := getRuneBalance(a.state.providerAddress)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get target provider balance")
		return OpResult{Continue: false}
	}

	a.state.churnedOutClaimable = claimable
	a.state.churnedOutClaimAmount = claimable.QuoUint64(2)
	if a.state.churnedOutClaimAmount.IsZero() {
		a.state.churnedOutClaimAmount = cosmos.NewUint(1)
	}
	a.state.churnedOutBalance = balance
	a.Log().Info().
		Str("claimable_yield", claimable.String()).
		Str("claim_amount", a.state.churnedOutClaimAmount.String()).
		Msg("churned-out target has claimable bond yield")
	return OpResult{Continue: true}
}

func (a *BondYieldManualChurnedOutActor) claimChurnedOutYield(config *OpConfig) OpResult {
	txid, err := broadcastBondYieldClaim(a.state.node, a.state.nodeAddress, a.state.churnedOutClaimAmount)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast churned-out manual bond-yield claim")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("claim_amount", a.state.churnedOutClaimAmount.String()).Msg("broadcasted churned-out manual bond-yield claim")
	return OpResult{Continue: true}
}

func (a *BondYieldManualChurnedOutActor) verifyChurnedOutManualClaim(config *OpConfig) OpResult {
	node, provider, ok, err := getBondYieldNodeProvider(a.state.nodeAddress, a.state.providerAddress.String())
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query target bond provider")
		return OpResult{Continue: false}
	}
	if !ok {
		a.Log().Info().Stringer("provider", a.state.providerAddress).Msg("waiting for target bond provider")
		return OpResult{Continue: false}
	}
	if node.Status == atypes.NodeStatus_Active.String() {
		a.Log().Info().Msg("waiting for target node to remain churned out")
		return OpResult{Continue: false}
	}
	balance, err := getRuneBalance(a.state.providerAddress)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get target provider balance")
		return OpResult{Continue: false}
	}

	claimable := bondYieldUint(provider.ClaimableYield)
	expectedClaimable := a.state.churnedOutClaimable.Sub(a.state.churnedOutClaimAmount)
	if claimable.Equal(expectedClaimable) && claimable.GT(cosmos.ZeroUint()) {
		a.state.unpaidYieldBeforeChurnIn = claimable
		a.state.bondBeforeChurnIn = bondYieldUint(provider.Bond)
		a.Log().Info().
			Str("balance_before", a.state.churnedOutBalance.String()).
			Str("balance_after", balance.String()).
			Str("claimable_before", a.state.churnedOutClaimable.String()).
			Str("claimable_after", claimable.String()).
			Str("bond_before_churn_in", a.state.bondBeforeChurnIn.String()).
			Msg("verified manual bond-yield payout while node is churned out")
		return OpResult{Continue: true}
	}

	a.Log().Info().
		Str("balance_before", a.state.churnedOutBalance.String()).
		Str("balance_after", balance.String()).
		Str("claimable_before", a.state.churnedOutClaimable.String()).
		Str("claimable_after", claimable.String()).
		Msg("waiting for churned-out manual bond-yield payout")
	return OpResult{Continue: false}
}

func (a *BondYieldThresholdChurnInActor) selectThresholdNode(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}

	activeNodes := make(map[string]bool)
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes[node.NodeAddress] = true
		}
	}

	for _, node := range config.NodeUsers {
		accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get node THOR address")
			return OpResult{Continue: false}
		}
		if accAddr.Equals(a.state.nodeAddress) || !activeNodes[accAddr.String()] {
			continue
		}
		providerAddress := common.Address(accAddr.String())
		_, provider, ok, err := getBondYieldNodeProvider(accAddr, providerAddress.String())
		if err != nil {
			a.Log().Error().Err(err).Stringer("node", accAddr).Msg("failed to query threshold-node provider")
			continue
		}
		if !ok || !bondYieldUint(provider.ClaimableYield).GT(cosmos.NewUint(1)) {
			continue
		}

		a.state.thresholdNode = node
		a.state.thresholdNodeAddress = accAddr
		a.state.thresholdProviderAddress = providerAddress
		a.Log().Info().
			Stringer("threshold_node", a.state.thresholdNodeAddress).
			Str("claimable_yield", provider.ClaimableYield).
			Msg("selected active node for affiliate collector threshold trigger")
		return OpResult{Continue: true}
	}

	a.Log().Info().Int("active_nodes", len(activeNodes)).Msg("waiting for active threshold node with claimable yield")
	return OpResult{Continue: false}
}

func (a *BondYieldThresholdChurnInActor) lowerPayoutTHORNameThreshold(config *OpConfig) OpResult {
	txid, memo, err := broadcastPayoutTHORName(a.state, cosmos.ZeroUint(), bondYieldPreferredAssetLowMultBps)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to lower payout THORName threshold")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("memo", memo).Msg("broadcasted payout THORName low-threshold update")
	return OpResult{Continue: true}
}

func (a *BondYieldThresholdChurnInActor) postLowPreferredAssetNetworkFee(config *OpConfig) OpResult {
	nodes, err := thornode.GetNodes()
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to get nodes")
		return OpResult{Continue: false}
	}

	activeNodes := make(map[string]bool)
	for _, node := range nodes {
		if node.Status == atypes.NodeStatus_Active.String() {
			activeNodes[node.NodeAddress] = true
		}
	}
	required := superMajority(len(activeNodes))
	if required == 0 {
		a.Log().Info().Msg("waiting for active nodes before posting network fee")
		return OpResult{Continue: false}
	}

	var sent int
	for _, node := range config.NodeUsers {
		accAddr, err := node.PubKey(common.THORChain).GetThorAddress()
		if err != nil {
			a.Log().Error().Err(err).Msg("failed to get node THOR address")
			return OpResult{Continue: false}
		}
		if !activeNodes[accAddr.String()] {
			continue
		}
		if !node.Acquire() {
			a.Log().Info().Stringer("node", accAddr).Msg("waiting for node account lock")
			continue
		}
		txid, err := node.Thorchain.PostNetworkFee(
			bondYieldPreferredAssetFeeHeight,
			a.state.payoutAsset.GetChain(),
			bondYieldPreferredAssetFeeSize,
			bondYieldPreferredAssetFeeRate,
		)
		node.Release()
		if err != nil {
			a.Log().Error().Err(err).Stringer("node", accAddr).Msg("failed to post preferred-asset network fee")
			return OpResult{Continue: false}
		}
		if txid.IsEmpty() {
			a.Log().Info().Stringer("node", accAddr).Msg("node skipped preferred-asset network fee post")
			continue
		}
		sent++
		a.Log().Info().Str("txid", txid.String()).Stringer("node", accAddr).Msg("posted preferred-asset network fee")
	}
	if sent < required {
		a.Log().Info().Int("sent", sent).Int("required", required).Msg("waiting to post preferred-asset network fee quorum")
		return OpResult{Continue: false}
	}
	return OpResult{Continue: true}
}

func (a *BondYieldThresholdChurnInActor) waitPayoutTHORNameLowThreshold(config *OpConfig) OpResult {
	tn, err := getBondYieldTHORName(a.state.payoutName)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query payout THORName")
		return OpResult{Continue: false}
	}
	if tn.PreferredAssetOutboundFeeMultiplier != bondYieldPreferredAssetLowMultBps {
		a.Log().Info().Int64("multiplier", tn.PreferredAssetOutboundFeeMultiplier).Msg("waiting for low THORName payout threshold")
		return OpResult{Continue: false}
	}

	collectorRune := bondYieldUint(tn.AffiliateCollectorRune)
	if collectorRune.IsZero() {
		a.Log().Info().Msg("waiting for pre-threshold affiliate collector RUNE")
		return OpResult{Continue: false}
	}
	thresholdRune := bondYieldUint(tn.PreferredAssetSwapThresholdRune)
	if !thresholdRune.IsZero() && !collectorRune.GT(thresholdRune) {
		a.Log().Info().
			Str("collector_rune", collectorRune.String()).
			Str("threshold_rune", thresholdRune.String()).
			Msg("waiting for affiliate collector to exceed preferred-asset threshold")
		return OpResult{Continue: false}
	}
	a.state.collectorBeforeThreshold = collectorRune
	a.Log().Info().
		Str("collector_rune", collectorRune.String()).
		Str("threshold_rune", thresholdRune.String()).
		Msg("payout THORName low threshold ready")
	return OpResult{Continue: true}
}

func (a *BondYieldThresholdChurnInActor) configureThresholdNodeAutoClaim(config *OpConfig) OpResult {
	txid, memo, err := broadcastBondYieldAutoClaim(a.state.thresholdNode, a.state.thresholdNodeAddress, bondYieldFullAutoClaimBps, a.state.payoutName)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to configure threshold-node bond-yield auto-claim")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Str("memo", memo).Msg("broadcasted threshold-node bond-yield auto-claim")
	return OpResult{Continue: true}
}

func (a *BondYieldThresholdChurnInActor) waitThresholdNodeAutoClaimConfigured(config *OpConfig) OpResult {
	return waitBondYieldAutoClaim(a.Actor, a.state.thresholdNodeAddress, a.state.thresholdProviderAddress.String(), bondYieldFullAutoClaimBps, a.state.payoutName)
}

func (a *BondYieldThresholdChurnInActor) requestThresholdNodeLeave(config *OpConfig) OpResult {
	txid, err := broadcastLeave(a.state.thresholdNode, a.state.thresholdNodeAddress)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to broadcast threshold-node leave")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid).Stringer("node", a.state.thresholdNodeAddress).Msg("broadcasted threshold-node leave")
	return OpResult{Continue: true}
}

func (a *BondYieldThresholdChurnInActor) waitTargetChurnedInAndThresholdPaid(config *OpConfig) OpResult {
	node, provider, ok, err := getBondYieldNodeProvider(a.state.nodeAddress, a.state.providerAddress.String())
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query target node")
		return OpResult{Continue: false}
	}
	if !ok {
		a.Log().Info().Stringer("provider", a.state.providerAddress).Msg("waiting for target bond provider")
		return OpResult{Continue: false}
	}
	if node.Status != atypes.NodeStatus_Active.String() {
		a.Log().Info().Str("status", node.Status).Msg("waiting for target node to churn back in")
		return OpResult{Continue: false}
	}

	tn, err := getBondYieldTHORName(a.state.payoutName)
	if err != nil {
		a.Log().Error().Err(err).Msg("failed to query payout THORName")
		return OpResult{Continue: false}
	}

	collectorRune := bondYieldUint(tn.AffiliateCollectorRune)
	bond := bondYieldUint(provider.Bond)
	principal := bondYieldUint(provider.Principal)
	claimable := bondYieldUint(provider.ClaimableYield)
	collectorDrained := collectorRune.IsZero() || collectorRune.LT(a.state.collectorBeforeThreshold)
	unpaidYieldRebonded := !a.state.unpaidYieldBeforeChurnIn.IsZero() &&
		claimable.LT(a.state.unpaidYieldBeforeChurnIn) &&
		bond.GTE(a.state.bondBeforeChurnIn)

	if collectorDrained && unpaidYieldRebonded {
		a.Log().Info().
			Str("collector_before_threshold", a.state.collectorBeforeThreshold.String()).
			Str("collector_after_threshold", collectorRune.String()).
			Str("unpaid_yield_before_churn_in", a.state.unpaidYieldBeforeChurnIn.String()).
			Str("bond_before_churn_in", a.state.bondBeforeChurnIn.String()).
			Str("bond_after_churn_in", bond.String()).
			Str("principal_after_churn_in", principal.String()).
			Str("claimable_after_churn_in", claimable.String()).
			Msg("verified affiliate collector threshold payout and unpaid yield rebond on churn-in")
		return OpResult{Finish: true}
	}

	a.Log().Info().
		Str("collector_before_threshold", a.state.collectorBeforeThreshold.String()).
		Str("collector_after_threshold", collectorRune.String()).
		Str("unpaid_yield_before_churn_in", a.state.unpaidYieldBeforeChurnIn.String()).
		Str("bond_before_churn_in", a.state.bondBeforeChurnIn.String()).
		Str("bond_after_churn_in", bond.String()).
		Str("principal_after_churn_in", principal.String()).
		Str("claimable_after_churn_in", claimable.String()).
		Msg("waiting for affiliate collector threshold payout and unpaid-yield rebond")
	return OpResult{Continue: false}
}

func (a *BondYieldSetupActor) broadcastMimir(key string, value int64) OpResult {
	if !a.state.node.Acquire() {
		a.Log().Info().Msg("waiting for node account lock")
		return OpResult{Continue: false}
	}
	defer a.state.node.Release()

	mimir := thorchain.NewMsgMimir(key, value, a.state.nodeAddress)
	txid, err := a.state.node.Thorchain.Broadcast(mimir)
	if err != nil {
		a.Log().Error().Err(err).Str("key", key).Int64("value", value).Msg("failed to broadcast mimir")
		return OpResult{Continue: false}
	}
	a.Log().Info().Str("txid", txid.String()).Str("key", key).Int64("value", value).Msg("broadcasted mimir")
	return OpResult{Continue: true}
}

func skipBondYieldIfRequired(actor Actor, state *BondYieldState) OpResult {
	if state.skipReason == "" {
		return OpResult{Continue: true}
	}
	actor.Log().Info().Str("reason", state.skipReason).Msg("skipping bond-yield actor")
	return OpResult{Finish: true}
}

func waitBondYieldAutoClaim(actor Actor, nodeAddress cosmos.AccAddress, providerAddress string, bps uint64, payoutName string) OpResult {
	provider, ok, err := getBondYieldProvider(nodeAddress, providerAddress)
	if err != nil {
		actor.Log().Error().Err(err).Msg("failed to query bond provider")
		return OpResult{Continue: false}
	}
	if !ok {
		actor.Log().Info().Str("provider", providerAddress).Msg("waiting for bond provider")
		return OpResult{Continue: false}
	}
	if provider.AutoClaimBps == bps && provider.PayoutAddress == payoutName {
		actor.Log().Info().Uint64("auto_claim_bps", bps).Str("payout_address", payoutName).Msg("bond-yield auto-claim configuration ready")
		return OpResult{Continue: true}
	}

	actor.Log().Info().
		Uint64("auto_claim_bps", provider.AutoClaimBps).
		Str("payout_address", provider.PayoutAddress).
		Uint64("want_auto_claim_bps", bps).
		Str("want_payout_address", payoutName).
		Msg("waiting for bond-yield auto-claim config")
	return OpResult{Continue: false}
}

func broadcastPayoutTHORName(state *BondYieldState, coinAmount cosmos.Uint, multiplier int64) (string, string, error) {
	ownerAddress := state.owner.Address(common.THORChain)
	payoutAddress := state.owner.Address(state.payoutAsset.GetChain())
	memo := fmt.Sprintf(
		"~:%s:%s:%s:%s:%s:0:%d",
		state.payoutName,
		state.payoutAsset.GetChain(),
		payoutAddress,
		ownerAddress,
		state.payoutAsset,
		multiplier,
	)
	coin := common.NewCoin(common.RuneAsset(), coinAmount)
	txid, err := broadcastDeposit(state.owner, common.NewCoins(coin), memo)
	return txid, memo, err
}

func broadcastBondYieldAutoClaim(user *User, nodeAddress cosmos.AccAddress, bps uint64, payoutName string) (string, string, error) {
	memo := fmt.Sprintf("bondyield:%s:AUTO:%d", nodeAddress.String(), bps)
	if payoutName != "" {
		memo = fmt.Sprintf("%s:%s", memo, payoutName)
	}
	txid, err := broadcastDeposit(user, common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.ZeroUint())), memo)
	return txid, memo, err
}

func broadcastBondYieldClaim(user *User, nodeAddress cosmos.AccAddress, amount cosmos.Uint) (string, error) {
	memo := fmt.Sprintf("bondyield:%s:%s", nodeAddress.String(), amount.String())
	return broadcastDeposit(user, common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.ZeroUint())), memo)
}

func broadcastLeave(user *User, nodeAddress cosmos.AccAddress) (string, error) {
	memo := fmt.Sprintf("leave:%s", nodeAddress.String())
	return broadcastDeposit(user, common.NewCoins(common.NewCoin(common.RuneAsset(), cosmos.ZeroUint())), memo)
}

func broadcastDeposit(user *User, coins common.Coins, memo string) (string, error) {
	if !user.Acquire() {
		return "", fmt.Errorf("failed to acquire user lock")
	}
	defer user.Release()

	accAddr, err := user.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		return "", err
	}
	deposit := ttypes.NewMsgDeposit(coins, memo, accAddr)
	txid, err := user.Thorchain.Broadcast(deposit)
	if err != nil {
		return "", err
	}
	return txid.String(), nil
}

func getBondYieldProvider(nodeAddress cosmos.AccAddress, providerAddress string) (bondYieldProvider, bool, error) {
	_, provider, ok, err := getBondYieldNodeProvider(nodeAddress, providerAddress)
	return provider, ok, err
}

func getBondYieldNodeProvider(nodeAddress cosmos.AccAddress, providerAddress string) (bondYieldNode, bondYieldProvider, bool, error) {
	node, err := getBondYieldNode(nodeAddress)
	if err != nil {
		return bondYieldNode{}, bondYieldProvider{}, false, err
	}
	for _, provider := range node.BondProviders.Providers {
		if provider.BondAddress == providerAddress {
			return node, provider, true, nil
		}
	}
	return node, bondYieldProvider{}, false, nil
}

func getBondYieldNode(nodeAddress cosmos.AccAddress) (bondYieldNode, error) {
	var node bondYieldNode
	url := fmt.Sprintf("%s/thorchain/node/%s", thornode.BaseURL(), nodeAddress.String())
	if err := thornode.Get(url, &node); err != nil {
		return bondYieldNode{}, err
	}
	return node, nil
}

func getBondYieldTHORName(name string) (bondYieldTHORName, error) {
	var tn bondYieldTHORName
	url := fmt.Sprintf("%s/thorchain/thorname/%s", thornode.BaseURL(), name)
	if err := thornode.Get(url, &tn); err != nil {
		return bondYieldTHORName{}, err
	}
	return tn, nil
}

func getRuneBalance(address common.Address) (cosmos.Uint, error) {
	balances, err := thornode.GetBalances(address)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	return balances.GetCoin(common.RuneAsset()).Amount, nil
}

func bondYieldUint(value string) cosmos.Uint {
	if value == "" {
		return cosmos.ZeroUint()
	}
	return cosmos.NewUintFromString(value)
}

func getMimirCaseInsensitive(mimirs map[string]int64, key string) (int64, bool) {
	for k, v := range mimirs {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return 0, false
}

func hasAlias(tn bondYieldTHORName, chain common.Chain, address common.Address) bool {
	for _, alias := range tn.Aliases {
		if alias.Chain == chain.String() && alias.Address == address.String() {
			return true
		}
	}
	return false
}

func superMajority(total int) int {
	if total <= 0 {
		return 0
	}
	required := total * 2 / 3
	if (total*2)%3 > 0 {
		required++
	}
	return required
}
