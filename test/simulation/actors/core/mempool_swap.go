package core

import (
	"crypto/sha256"
	"errors"
	"fmt"
	rand "math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simcommon "gitlab.com/thorchain/thornode/v3/test/simulation/actors/common"
	"gitlab.com/thorchain/thornode/v3/test/simulation/pkg/thornode"
	. "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	mempoolRollbackSecurityMsg = "mempool swap rolled back"

	mempoolCapHigh              = int64(100_000_000_000_000)
	mempoolAffiliateRevShareBPS = int64(2_500)
	mempoolTimeoutSigningPeriod = int64(3)
	mempoolTimeoutAttempts      = int64(3)

	mempoolWaitInterval       = 500 * time.Millisecond
	mempoolObservationTimeout = 25 * time.Second
	mempoolCompletionTimeout  = 2 * time.Minute
)

type mempoolMimirValue struct {
	key   string
	value int64
}

var (
	mempoolSimulationSetupMimirs = []mempoolMimirValue{
		{key: "DollarsPerRune", value: common.One},
		{key: "EnableAdvSwapQueue", value: 1},
		{key: "MempoolSwapCapTOR", value: mempoolCapHigh},
		{key: "MempoolSwapPoolCapBPS", value: 0},
	}
	// Fresh mocknet omits these Mimirs from storage and reports -1. Ordinary
	// voting cannot restore an absent key, so cleanup intentionally normalizes
	// that state to the explicit effective mocknet/default value below.
	mempoolSimulationUnsetMimirDefaults = map[string]int64{
		"DollarsPerRune":           0,
		"EnableAdvSwapQueue":       1,
		"MaxOutboundAttempts":      0,
		"MempoolSwapCapTOR":        0,
		"MempoolSwapPoolCapBPS":    50,
		"SigningTransactionPeriod": 300,
	}

	// Keep early lifecycle coverage on BTC, then use AVAX's lower outbound fee
	// for the cumulative-profile cases that otherwise repeat the same target.
	mempoolEarlyLifecycleTarget    = common.BTCAsset
	mempoolCumulativeProfileTarget = common.AVAXAsset
)

// MempoolSwapSimulationActor exercises the real L1 -> Bifrost -> Thornode
// lifecycle. POL is deliberately used for speculative inbounds: Bifrost waits
// for 15 POL confirmations, providing a stable window in which the transaction
// is mined and observed but not final.
type MempoolSwapSimulationActor struct {
	Actor
}

func NewMempoolSwapSimulationActor(rng *rand.Rand) *Actor {
	a := &MempoolSwapSimulationActor{
		Actor: *NewActor("Mempool Swap Lifecycle", rng),
	}
	a.Timeout = 30 * time.Minute
	a.Ops = append(a.Ops, a.run)
	return &a.Actor
}

func (a *MempoolSwapSimulationActor) run(config *OpConfig) OpResult {
	err := a.runScenarios(config)
	return OpResult{Finish: true, Error: err}
}

func (a *MempoolSwapSimulationActor) runScenarios(config *OpConfig) (err error) {
	if len(config.Users) < 2 {
		return fmt.Errorf("mempool swap simulation requires at least two users")
	}
	scenarios := []struct {
		name string
		run  func(*OpConfig) error
	}{
		{name: "happy-market", run: a.scenarioHappyMarket},
		{name: "affiliate-revshare-finality", run: a.scenarioAffiliateRevShareFinality},
		{name: "stream-settle-before-finality", run: a.scenarioStreamSettleBeforeFinality},
		{name: "stream-finality-before-settlement", run: a.scenarioStreamFinalityBeforeSettlement},
		{name: "tor-cap-fallback", run: a.scenarioTORCapFallback},
		{name: "pool-cap-fallback", run: a.scenarioPoolCapFallback},
		{name: "timeout-rollback", run: a.scenarioTimeoutRollback},
	}
	scenarioNames := make([]string, 0, len(scenarios))
	for _, scenario := range scenarios {
		scenarioNames = append(scenarioNames, scenario.name)
	}

	originalMimirs, err := snapshotMempoolSimulationMimirs(mempoolSimulationSetupMimirs)
	if err != nil {
		return fmt.Errorf("snapshot mempool swap simulation Mimirs: %w", err)
	}
	a.Log().Info().Strs("scenarios", scenarioNames).Msg("starting mempool swap lifecycle simulation")
	defer func() {
		restoreErr := a.restoreMimirs(config, originalMimirs)
		if restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore mempool swap simulation mimirs: %w", restoreErr))
		}
	}()
	if err = a.setMimirs(config, mempoolSimulationSetupMimirs); err != nil {
		return fmt.Errorf("configure mempool swap simulation: %w", err)
	}

	usage, err := thornode.GetMempoolSwapUsageTOR()
	if err != nil {
		return err
	}
	if !usage.IsZero() {
		return fmt.Errorf("mempool swap simulation requires zero initial TOR usage, got %s", usage)
	}

	for _, scenario := range scenarios {
		a.Log().Info().Str("scenario", scenario.name).Msg("starting mempool swap scenario")
		if err = scenario.run(config); err != nil {
			return fmt.Errorf("mempool swap scenario %s: %w", scenario.name, err)
		}
		if err = waitForMempoolCondition("TOR usage cleanup after "+scenario.name, 20*time.Second, func() (bool, error) {
			current, queryErr := thornode.GetMempoolSwapUsageTOR()
			return queryErr == nil && current.IsZero(), queryErr
		}); err != nil {
			return err
		}
		a.Log().Info().Str("scenario", scenario.name).Msg("completed mempool swap scenario")
	}

	for _, state := range config.ExpectedSecurityEventStates() {
		if state.Pending != 0 {
			return fmt.Errorf("security expectation for %s remains pending: %+v", state.ID, state)
		}
	}

	return nil
}

func (a *MempoolSwapSimulationActor) setMimirs(config *OpConfig, values []mempoolMimirValue) error {
	for _, value := range values {
		if err := a.setMimir(config, value.key, value.value); err != nil {
			return err
		}
	}
	return nil
}

func snapshotMempoolSimulationMimirs(settings []mempoolMimirValue) ([]mempoolMimirValue, error) {
	result := make([]mempoolMimirValue, 0, len(settings))
	for _, setting := range settings {
		value, err := getEffectiveMempoolSimulationMimir(setting.key)
		if err != nil {
			return nil, fmt.Errorf("get effective %s: %w", setting.key, err)
		}
		result = append(result, mempoolMimirValue{key: setting.key, value: value})
	}
	return result, nil
}

func getEffectiveMempoolSimulationMimir(key string) (int64, error) {
	raw, err := thornode.GetMimir(key)
	if err != nil {
		return 0, err
	}
	return mempoolSimulationEffectiveMimirValue(key, raw)
}

func mempoolSimulationEffectiveMimirValue(key string, raw int64) (int64, error) {
	if raw >= 0 {
		return raw, nil
	}
	if raw != -1 {
		return 0, fmt.Errorf("effective %s has invalid negative value %d", key, raw)
	}
	value, ok := mempoolSimulationUnsetMimirDefaults[key]
	if !ok {
		return 0, fmt.Errorf("effective %s is unset and has no documented fallback", key)
	}
	return value, nil
}

// restoreMimirs attempts every restoration even if an earlier Mimir vote
// fails, so one failure cannot strand an unrelated temporary test setting.
func (a *MempoolSwapSimulationActor) restoreMimirs(config *OpConfig, values []mempoolMimirValue) error {
	var result error
	for i := len(values) - 1; i >= 0; i-- {
		value := values[i]
		if err := a.setMimir(config, value.key, value.value); err != nil {
			result = errors.Join(result, fmt.Errorf("restore %s=%d: %w", value.key, value.value, err))
		}
	}
	return result
}

func (a *MempoolSwapSimulationActor) setMimir(config *OpConfig, key string, value int64) error {
	current, err := getEffectiveMempoolSimulationMimir(key)
	if err == nil && current == value {
		return nil
	}

	active, err := activeSimulationNodeUsers(config)
	if err != nil {
		return err
	}
	if len(active) == 0 {
		return fmt.Errorf("no active node users available for mimir %s", key)
	}

	for _, node := range active {
		if !node.Acquire() {
			return fmt.Errorf("active node %s is busy while setting mimir %s", node.Name(), key)
		}
		accAddr, addrErr := node.PubKey(common.THORChain).GetThorAddress()
		if addrErr != nil {
			node.Release()
			return addrErr
		}
		txID, broadcastErr := node.Thorchain.Broadcast(thortypes.NewMsgMimir(key, value, accAddr))
		node.Release()
		if broadcastErr != nil {
			return fmt.Errorf("broadcast mimir %s=%d from %s: %w", key, value, accAddr, broadcastErr)
		}
		a.Log().Debug().Stringer("txid", txID).Str("key", key).Int64("value", value).Msg("broadcasted mimir vote")
	}

	return waitForMempoolCondition(fmt.Sprintf("mimir %s=%d", strings.ToUpper(key), value), 45*time.Second, func() (bool, error) {
		current, queryErr := getEffectiveMempoolSimulationMimir(key)
		return queryErr == nil && current == value, queryErr
	})
}

func activeSimulationNodeUsers(config *OpConfig) ([]*User, error) {
	nodes, err := thornode.GetNodes()
	if err != nil {
		return nil, err
	}
	activeAddresses := make(map[string]bool)
	for _, node := range nodes {
		if node.Status == "Active" {
			activeAddresses[node.NodeAddress] = true
		}
	}

	result := make([]*User, 0, len(activeAddresses))
	for _, user := range config.NodeUsers {
		addr, addrErr := user.PubKey(common.THORChain).GetThorAddress()
		if addrErr != nil {
			return nil, addrErr
		}
		if activeAddresses[addr.String()] {
			result = append(result, user)
		}
	}
	if len(result) != len(activeAddresses) {
		return nil, fmt.Errorf("found %d simulation signers for %d active validators", len(result), len(activeAddresses))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name() < result[j].Name() })
	return result, nil
}

type mempoolLiveSwap struct {
	name string
	user *User

	from   common.Asset
	to     common.Asset
	amount cosmos.Uint
	memo   string
	txID   string

	destination      common.Address
	destinationStart cosmos.Uint
	fromPoolStart    cosmos.Uint
	toPoolStart      cosmos.Uint
	vaultPubKey      common.PubKey
	vaultStart       cosmos.Uint
}

func (s *mempoolLiveSwap) release() {
	if s != nil && s.user != nil {
		s.user.Release()
		s.user = nil
	}
}

func (a *MempoolSwapSimulationActor) startSwap(
	config *OpConfig,
	name string,
	from, to common.Asset,
	divisor uint64,
	stream string,
) (*mempoolLiveSwap, error) {
	swap, err := a.prepareSwap(config, name, from, to, divisor, stream)
	if err != nil {
		return nil, err
	}
	if err = a.broadcastPreparedSwap(swap); err != nil {
		swap.release()
		return nil, err
	}
	return swap, nil
}

func (a *MempoolSwapSimulationActor) prepareSwap(
	config *OpConfig,
	name string,
	from, to common.Asset,
	divisor uint64,
	stream string,
) (*mempoolLiveSwap, error) {
	if divisor == 0 {
		return nil, fmt.Errorf("swap divisor cannot be zero")
	}
	fromPool, err := thornode.GetPool(from.GetLayer1Asset())
	if err != nil {
		return nil, err
	}
	amount, err := cosmos.ParseUint(fromPool.BalanceAsset)
	if err != nil {
		return nil, err
	}
	amount = amount.QuoUint64(divisor)
	if amount.IsZero() {
		return nil, fmt.Errorf("computed zero %s swap amount", from)
	}

	var (
		user             *User
		destination      common.Address
		destinationStart cosmos.Uint
		lastSizingErr    error
	)
	for {
		user, destination, destinationStart, err = acquireMempoolSwapUser(config, from, to, amount)
		if err != nil {
			if lastSizingErr != nil {
				return nil, fmt.Errorf("size %s swap above the recommended minimum: %w", name, errors.Join(lastSizingErr, err))
			}
			return nil, err
		}
		fromAccount, accountErr := user.ChainClients[from.Chain].GetAccount(nil)
		if accountErr != nil {
			user.Release()
			return nil, fmt.Errorf("get %s account for quote sizing: %w", from, accountErr)
		}
		spendable := swapSpendableBalance(from, accountAssetAmount(fromAccount, from))
		_, sizedAmount, sizingErr := getSwapQuoteWithRecommendedMin(from, to, amount, spendable, destination, false)
		if sizingErr == nil {
			amount = sizedAmount
			break
		}
		user.Release()
		if !mempoolSwapQuoteNeedsRicherUser(sizedAmount, spendable, sizingErr) {
			return nil, fmt.Errorf("size %s swap above the recommended minimum: %w", name, sizingErr)
		}

		// The quote helper returns the required retry amount when its recommended
		// minimum exceeds this user's spendable balance. Reacquire at that amount
		// so the depleted user is skipped and a richer unlocked user can be used.
		amount = sizedAmount
		lastSizingErr = sizingErr
	}
	swap := &mempoolLiveSwap{
		name:             name,
		user:             user,
		from:             from,
		to:               to,
		amount:           amount,
		destination:      destination,
		destinationStart: destinationStart,
	}
	failed := true
	defer func() {
		if failed {
			swap.release()
		}
	}()

	swap.fromPoolStart, err = mempoolPoolAssetBalance(from)
	if err != nil {
		return nil, err
	}
	swap.toPoolStart, err = mempoolPoolAssetBalance(to)
	if err != nil {
		return nil, err
	}
	swap.vaultPubKey, err = thornode.GetInboundAddressPubKey(from.Chain)
	if err != nil {
		return nil, err
	}
	swap.vaultStart, err = mempoolVaultAssetBalance(swap.vaultPubKey, from)
	if err != nil {
		return nil, err
	}

	swap.memo = fmt.Sprintf("=:%s:%s", to, destination)
	if stream != "" {
		swap.memo += ":" + stream
	}
	failed = false
	return swap, nil
}

func mempoolSwapQuoteNeedsRicherUser(quotedAmount, spendable cosmos.Uint, quoteErr error) bool {
	// getSwapQuoteWithRecommendedMin preserves the requested amount for ordinary
	// quote errors; only its max-balance failure returns an amount above max.
	return quoteErr != nil && quotedAmount.GT(spendable)
}

func (a *MempoolSwapSimulationActor) broadcastPreparedSwap(swap *mempoolLiveSwap) error {
	var err error
	swap.txID, err = simcommon.DepositL1(a.Log(), swap.user.ChainClients[swap.from.Chain], swap.from, swap.memo, swap.amount)
	if err != nil {
		return err
	}
	a.Log().Info().Str("scenario", swap.name).Str("txid", swap.txID).Stringer("amount", swap.amount).Str("memo", swap.memo).Msg("broadcasted lifecycle swap")
	return nil
}

func acquireMempoolSwapUser(config *OpConfig, from, to common.Asset, amount cosmos.Uint) (*User, common.Address, cosmos.Uint, error) {
	for _, user := range config.Users {
		if !user.Acquire() {
			continue
		}
		fromClient, ok := user.ChainClients[from.Chain]
		if !ok {
			user.Release()
			continue
		}
		toClient, ok := user.ChainClients[to.Chain]
		if !ok {
			user.Release()
			continue
		}
		fromAccount, err := fromClient.GetAccount(nil)
		if err != nil || swapSpendableBalance(from, accountAssetAmount(fromAccount, from)).LT(amount) {
			user.Release()
			continue
		}
		destination, err := user.PubKey(to.Chain).GetAddress(to.Chain)
		if err != nil {
			user.Release()
			continue
		}
		toAccount, err := toClient.GetAccount(nil)
		if err != nil {
			user.Release()
			continue
		}
		return user, destination, accountAssetAmount(toAccount, to), nil
	}
	return nil, common.NoAddress, cosmos.ZeroUint(), fmt.Errorf("no unlocked user can swap %s %s to %s", amount, from, to)
}

func mempoolPoolAssetBalance(asset common.Asset) (cosmos.Uint, error) {
	pool, err := thornode.GetPool(asset.GetLayer1Asset())
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	return cosmos.ParseUint(pool.BalanceAsset)
}

func mempoolVaultAssetBalance(pubKey common.PubKey, asset common.Asset) (cosmos.Uint, error) {
	vault, err := thornode.GetVault(pubKey.String())
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	for _, coin := range vault.Coins {
		if strings.EqualFold(coin.Asset, asset.String()) {
			return cosmos.ParseUint(coin.Amount)
		}
	}
	return cosmos.ZeroUint(), nil
}

func mempoolDestinationBalance(swap *mempoolLiveSwap) (cosmos.Uint, error) {
	account, err := swap.user.ChainClients[swap.to.Chain].GetAccount(nil)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	return accountAssetAmount(account, swap.to), nil
}

func (a *MempoolSwapSimulationActor) scenarioHappyMarket(config *OpConfig) error {
	swap, err := a.startSwap(config, "happy-market", common.POLAsset, mempoolEarlyLifecycleTarget, 2_000, "")
	if err != nil {
		return err
	}
	defer swap.release()
	if err = a.waitForSpeculativeSettlement(swap, cosmos.ZeroUint()); err != nil {
		return err
	}
	return a.waitForL1Completion(swap, cosmos.ZeroUint())
}

func (a *MempoolSwapSimulationActor) scenarioAffiliateRevShareFinality(config *OpConfig) (err error) {
	thorname, err := a.ensureMempoolAffiliateTHORName(config)
	if err != nil {
		return err
	}

	revShareKey := "REVSHARE-" + thorname
	// This THORName is unique to the simulation. Restoring to an explicit zero
	// preserves the fresh cluster's disabled behavior even though an absent
	// operational Mimir cannot be recreated through ordinary validator votes.
	defer func() {
		if restoreErr := a.setMimir(config, revShareKey, 0); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore %s=0: %w", revShareKey, restoreErr))
		}
	}()
	if err = a.setMimir(config, revShareKey, mempoolAffiliateRevShareBPS); err != nil {
		return err
	}

	collectorStart, err := mempoolAffiliateCollectorBalance(thorname)
	if err != nil {
		return err
	}
	swap, err := a.prepareSwap(
		config,
		"affiliate-revshare-finality",
		common.POLAsset,
		mempoolCumulativeProfileTarget,
		2_000,
		"",
	)
	if err != nil {
		return err
	}
	defer swap.release()
	// Zero ordinary affiliate bps keeps the THORName in the parsed affiliate
	// list for liquidity-fee attribution without creating a user-side affiliate
	// outbound. Any collector growth therefore comes only from REVSHARE.
	swap.memo = fmt.Sprintf("=:%s:%s::%s:0", swap.to, swap.destination, thorname)
	if err = a.broadcastPreparedSwap(swap); err != nil {
		return err
	}
	if err = a.waitForSpeculativeSettlement(swap, cosmos.ZeroUint()); err != nil {
		return err
	}
	if err = requireMempoolSwapUnfinalized(swap.txID); err != nil {
		return err
	}
	collectorBeforeFinality, err := mempoolAffiliateCollectorBalance(thorname)
	if err != nil {
		return err
	}
	if !collectorBeforeFinality.Equal(collectorStart) {
		return fmt.Errorf(
			"affiliate collector changed before inbound finality: expected %s, got %s",
			collectorStart,
			collectorBeforeFinality,
		)
	}
	if err = requireMempoolSwapUnfinalized(swap.txID); err != nil {
		return err
	}

	if err = a.waitForL1Completion(swap, cosmos.ZeroUint()); err != nil {
		return err
	}
	return waitForMempoolCondition("finalized affiliate REVSHARE payout "+swap.txID, 30*time.Second, func() (bool, error) {
		collector, queryErr := mempoolAffiliateCollectorBalance(thorname)
		return queryErr == nil && collector.GT(collectorStart), queryErr
	})
}

func (a *MempoolSwapSimulationActor) ensureMempoolAffiliateTHORName(config *OpConfig) (string, error) {
	for _, user := range config.Users {
		if !user.Acquire() {
			continue
		}
		name, err := a.ensureMempoolAffiliateTHORNameForUser(user)
		user.Release()
		return name, err
	}
	return "", errors.New("no unlocked simulation user available to own affiliate THORName")
}

func (a *MempoolSwapSimulationActor) ensureMempoolAffiliateTHORNameForUser(user *User) (string, error) {
	owner, err := user.PubKey(common.THORChain).GetAddress(common.THORChain)
	if err != nil {
		return "", err
	}
	name := mempoolAffiliateTHORName(owner.String())
	if existing, queryErr := thornode.GetTHORName(name); queryErr == nil {
		if !strings.EqualFold(existing.GetOwner(), owner.String()) {
			return "", fmt.Errorf("THORName %s is owned by %s, expected %s", name, existing.GetOwner(), owner)
		}
		if existing.GetPreferredAsset() != common.EmptyAsset.String() {
			return "", fmt.Errorf("THORName %s has preferred asset %s; expected no preferred asset", name, existing.GetPreferredAsset())
		}
		return name, nil
	}

	ownerAcc, err := owner.AccAddress()
	if err != nil {
		return "", err
	}
	network, err := thornode.GetNetwork()
	if err != nil {
		return "", fmt.Errorf("query THORName registration fee: %w", err)
	}
	registrationFee, err := cosmos.ParseUint(network.GetTnsRegisterFeeRune())
	if err != nil {
		return "", fmt.Errorf("parse THORName registration fee %q: %w", network.GetTnsRegisterFeeRune(), err)
	}
	registerMemo := fmt.Sprintf("~:%s:THOR:%s:%s", name, owner, owner)
	registerCoin := common.NewCoin(common.RuneNative, registrationFee.Add(cosmos.NewUint(common.One)))
	deposit := thortypes.NewMsgDeposit(common.NewCoins(registerCoin), registerMemo, ownerAcc)
	txID, err := user.Thorchain.BroadcastWithBlocking(deposit)
	if err != nil {
		return "", fmt.Errorf("register affiliate THORName %s: %w", name, err)
	}
	a.Log().Info().Stringer("txid", txID).Str("thorname", name).Str("owner", owner.String()).Msg("registered mempool affiliate THORName")

	if err = waitForMempoolCondition("affiliate THORName registration "+name, 30*time.Second, func() (bool, error) {
		registered, queryErr := thornode.GetTHORName(name)
		if queryErr != nil {
			return false, queryErr
		}
		if !strings.EqualFold(registered.GetOwner(), owner.String()) {
			return false, stopMempoolWait(fmt.Errorf("THORName %s registered to %s, expected %s", name, registered.GetOwner(), owner))
		}
		return registered.GetPreferredAsset() == common.EmptyAsset.String(), nil
	}); err != nil {
		return "", err
	}
	return name, nil
}

func mempoolAffiliateTHORName(owner string) string {
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(owner))))
	return fmt.Sprintf("mpsim%x", digest[:5])
}

func mempoolAffiliateCollectorBalance(thorname string) (cosmos.Uint, error) {
	value, err := thornode.GetTHORName(thorname)
	if err != nil {
		return cosmos.ZeroUint(), err
	}
	return parseMempoolAffiliateCollectorBalance(value.GetAffiliateCollectorRune())
}

func parseMempoolAffiliateCollectorBalance(raw string) (cosmos.Uint, error) {
	if strings.TrimSpace(raw) == "" {
		return cosmos.ZeroUint(), errors.New("affiliate_collector_rune is missing")
	}
	amount, err := cosmos.ParseUint(raw)
	if err != nil {
		return cosmos.ZeroUint(), fmt.Errorf("invalid affiliate_collector_rune %q: %w", raw, err)
	}
	return amount, nil
}

func (a *MempoolSwapSimulationActor) scenarioStreamSettleBeforeFinality(config *OpConfig) error {
	swap, err := a.startSwap(config, "stream-settle-before-finality", common.POLAsset, mempoolEarlyLifecycleTarget, 2_000, "0/1/3")
	if err != nil {
		return err
	}
	defer swap.release()
	if err = a.waitForSpeculativeSettlement(swap, cosmos.ZeroUint()); err != nil {
		return err
	}
	return a.waitForL1Completion(swap, cosmos.ZeroUint())
}

func (a *MempoolSwapSimulationActor) scenarioStreamFinalityBeforeSettlement(config *OpConfig) error {
	swap, err := a.startSwap(config, "stream-finality-before-settlement", common.POLAsset, mempoolEarlyLifecycleTarget, 2_000, "0/10/6")
	if err != nil {
		return err
	}
	defer swap.release()

	if err = waitForMempoolCondition("partial unfinalized streaming swap "+swap.txID, mempoolObservationTimeout, func() (bool, error) {
		if err := requireMempoolSwapUnfinalized(swap.txID); err != nil {
			return false, err
		}
		usage, queryErr := thornode.GetMempoolSwapUsageTOR()
		if queryErr != nil || usage.IsZero() {
			return false, queryErr
		}
		details, queryErr := thornode.GetSwapDetails(swap.txID)
		if queryErr != nil || details.Swap == nil || details.Swap.State == nil {
			return false, queryErr
		}
		state := details.Swap.State
		if state.GetCount() <= 0 || state.GetCount() >= state.GetQuantity() {
			return false, nil
		}
		if has, queryErr := mempoolHasStagedOutbound(swap.txID); queryErr != nil || has {
			return false, queryErr
		}
		if has, queryErr := mempoolHasRealOutbound(swap.txID); queryErr != nil || has {
			return false, queryErr
		}
		balance, queryErr := mempoolDestinationBalance(swap)
		if queryErr != nil || !balance.Equal(swap.destinationStart) {
			return false, queryErr
		}
		return true, requireMempoolSwapUnfinalized(swap.txID)
	}); err != nil {
		return err
	}

	// Inbound finality retires the speculative exposure even though the advanced
	// queue still owns the remaining stream: the credited source is now backed by
	// a finalized deposit, so the capacity reservation is released immediately and
	// the leftover queue item settles as an ordinary streaming swap.
	if err = waitForMempoolCondition("released streaming reservation at finality "+swap.txID, mempoolCompletionTimeout, func() (bool, error) {
		details, queryErr := thornode.GetTxDetails(swap.txID)
		if queryErr != nil || details.GetFinalisedHeight() == 0 {
			return false, queryErr
		}
		usage, queryErr := thornode.GetMempoolSwapUsageTOR()
		if queryErr != nil || !usage.IsZero() {
			return false, queryErr
		}
		queued, queryErr := thornode.GetSwapDetails(swap.txID)
		if queryErr != nil || queued.Swap == nil || queued.Swap.State == nil {
			return false, queryErr
		}
		state := queued.Swap.State
		if state.GetCount() <= 0 || state.GetCount() >= state.GetQuantity() {
			return false, nil
		}
		if has, queryErr := mempoolHasStagedOutbound(swap.txID); queryErr != nil || has {
			return false, queryErr
		}
		balance, queryErr := mempoolDestinationBalance(swap)
		if queryErr != nil || !balance.Equal(swap.destinationStart) {
			return false, queryErr
		}
		vaultBalance, queryErr := mempoolVaultAssetBalance(swap.vaultPubKey, swap.from)
		if queryErr != nil || !vaultBalance.Equal(swap.vaultStart.Add(swap.amount)) {
			return false, queryErr
		}
		return true, nil
	}); err != nil {
		return err
	}

	return a.waitForL1Completion(swap, cosmos.ZeroUint())
}

func (a *MempoolSwapSimulationActor) scenarioTORCapFallback(config *OpConfig) (err error) {
	if err = a.setMimir(config, "MempoolSwapCapTOR", 1); err != nil {
		return err
	}
	defer func() {
		if restoreErr := a.setMimir(config, "MempoolSwapCapTOR", mempoolCapHigh); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore MempoolSwapCapTOR: %w", restoreErr))
		}
	}()

	swap, err := a.startSwap(config, "tor-cap-fallback", common.POLAsset, mempoolCumulativeProfileTarget, 2_000, "")
	if err != nil {
		return err
	}
	defer swap.release()
	if err = a.assertNoSpeculativeWindow(swap, cosmos.ZeroUint(), 2); err != nil {
		return err
	}
	return a.waitForL1Completion(swap, cosmos.ZeroUint())
}

func (a *MempoolSwapSimulationActor) scenarioPoolCapFallback(config *OpConfig) (err error) {
	if err = a.setMimir(config, "MempoolSwapPoolCapBPS", 1); err != nil {
		return err
	}
	defer func() {
		if restoreErr := a.setMimir(config, "MempoolSwapPoolCapBPS", 0); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore MempoolSwapPoolCapBPS: %w", restoreErr))
		}
	}()

	swap, err := a.startSwap(config, "pool-cap-fallback", common.POLAsset, mempoolCumulativeProfileTarget, 2_000, "")
	if err != nil {
		return err
	}
	defer swap.release()
	if err = a.assertNoSpeculativeWindow(swap, cosmos.ZeroUint(), 2); err != nil {
		return err
	}
	return a.waitForL1Completion(swap, cosmos.ZeroUint())
}

func (a *MempoolSwapSimulationActor) scenarioTimeoutRollback(config *OpConfig) (err error) {
	originalSigningPeriod, err := getEffectiveMempoolSimulationMimir("SigningTransactionPeriod")
	if err != nil {
		return fmt.Errorf("snapshot SigningTransactionPeriod: %w", err)
	}
	if originalSigningPeriod <= 0 {
		return fmt.Errorf("invalid effective SigningTransactionPeriod %d", originalSigningPeriod)
	}
	originalMaxOutboundAttempts, err := getEffectiveMempoolSimulationMimir("MaxOutboundAttempts")
	if err != nil {
		return fmt.Errorf("snapshot MaxOutboundAttempts: %w", err)
	}

	// Register each value before changing it: setMimir can fail after enough
	// votes have already landed to alter the effective value.
	restoreValues := make([]mempoolMimirValue, 0, 2)
	restored := false
	defer func() {
		if restored {
			return
		}
		if restoreErr := a.restoreMimirs(config, restoreValues); restoreErr != nil {
			err = errors.Join(err, fmt.Errorf("restore timeout rollback mimirs: %w", restoreErr))
		}
	}()

	// Any non-positive MaxOutboundAttempts already selects the production
	// fallback of three. Avoid persisting zero over an unset (-1) Mimir, which
	// cannot be faithfully restored through ordinary node votes.
	if originalMaxOutboundAttempts > 0 {
		restoreValues = append(restoreValues, mempoolMimirValue{key: "MaxOutboundAttempts", value: originalMaxOutboundAttempts})
		if err = a.setMimir(config, "MaxOutboundAttempts", 0); err != nil {
			return err
		}
	}
	if originalSigningPeriod != mempoolTimeoutSigningPeriod {
		restoreValues = append(restoreValues, mempoolMimirValue{key: "SigningTransactionPeriod", value: originalSigningPeriod})
		if err = a.setMimir(config, "SigningTransactionPeriod", mempoolTimeoutSigningPeriod); err != nil {
			return err
		}
	}

	startHeight, err := thornode.GetBlockHeight()
	if err != nil {
		return err
	}
	swap, err := a.startSwap(config, "timeout-rollback", common.POLAsset, mempoolCumulativeProfileTarget, 2_000, "")
	if err != nil {
		return err
	}
	defer swap.release()
	if err = config.RegisterExpectedSecurityEvent(swap.txID, mempoolRollbackSecurityMsg, 1); err != nil {
		return err
	}
	if err = a.waitForSpeculativeSettlement(swap, cosmos.ZeroUint()); err != nil {
		return err
	}
	stagedHeight, err := mempoolStagedOutboundHeight(swap.txID)
	if err != nil {
		return err
	}
	// v1 performs the staged-outbound scan only on SigningTransactionPeriod
	// cadence blocks. A timeout threshold between cadence blocks is recovered on
	// the next cadence, rather than at the exact age threshold used by v2's due
	// lifecycle index.
	expectedRecoveryHeight := mempoolTimeoutRecoveryHeight(
		stagedHeight,
		mempoolTimeoutSigningPeriod,
		mempoolTimeoutAttempts,
	)
	if err = requireMempoolTimeoutPending(swap.txID, expectedRecoveryHeight); err != nil {
		return err
	}
	if err = a.waitForRollbackCleanup(swap, false); err != nil {
		return err
	}
	// Restore both temporary timeout controls as soon as the rollback state is
	// observed; watcher/event verification and later POL finality run under the
	// caller's original signing configuration.
	if err = a.restoreMimirs(config, restoreValues); err != nil {
		return fmt.Errorf("restore timeout rollback mimirs before verification: %w", err)
	}
	restored = true
	if err = waitForExpectedSecurityEvent(config, swap.txID); err != nil {
		return err
	}
	expectation := mempoolRollbackEventExpectation{
		timeout:                true,
		expectedRollbackHeight: expectedRecoveryHeight,
	}
	details, err := thornode.GetTxDetails(swap.txID)
	if err != nil {
		return err
	}
	// Finality may arrive while restoring the original Mimirs. If so, use its
	// actual height when auditing any outbound that was legitimately released
	// during restoration.
	expectation.finalisedHeight = details.GetFinalisedHeight()
	if err = requireRollbackEvents(startHeight, swap.txID, expectation); err != nil {
		return err
	}
	if err = a.waitForL1Completion(swap, cosmos.ZeroUint()); err != nil {
		return err
	}
	details, err = thornode.GetTxDetails(swap.txID)
	if err != nil {
		return err
	}
	if details.GetFinalisedHeight() <= 0 {
		return fmt.Errorf("timeout recovery %s completed without an actual finalised height", swap.txID)
	}
	expectation.finalisedHeight = details.GetFinalisedHeight()
	expectation.requireOutboundHistory = true
	return requireRollbackEvents(startHeight, swap.txID, expectation)
}

func (a *MempoolSwapSimulationActor) waitForSpeculativeSettlement(swap *mempoolLiveSwap, baselineUsage cosmos.Uint) error {
	return waitForMempoolCondition("speculative settlement "+swap.txID, mempoolObservationTimeout, func() (bool, error) {
		if err := requireMempoolSwapUnfinalized(swap.txID); err != nil {
			return false, err
		}
		usage, err := thornode.GetMempoolSwapUsageTOR()
		if err != nil || !usage.GT(baselineUsage) {
			return false, err
		}
		hasStaged, err := mempoolHasStagedOutbound(swap.txID)
		if err != nil || !hasStaged {
			return false, err
		}
		hasOutbound, err := mempoolHasRealOutbound(swap.txID)
		if err != nil || hasOutbound {
			return false, err
		}
		destination, err := mempoolDestinationBalance(swap)
		if err != nil || !destination.Equal(swap.destinationStart) {
			return false, err
		}
		vault, err := mempoolVaultAssetBalance(swap.vaultPubKey, swap.from)
		if err != nil || !vault.Equal(swap.vaultStart) {
			return false, err
		}
		fromBalance, err := mempoolPoolAssetBalance(swap.from)
		if err != nil || !fromBalance.GT(swap.fromPoolStart) {
			return false, err
		}
		toBalance, err := mempoolPoolAssetBalance(swap.to)
		if err != nil || !toBalance.LT(swap.toPoolStart) {
			return false, err
		}
		return true, requireMempoolSwapUnfinalized(swap.txID)
	})
}

func (a *MempoolSwapSimulationActor) assertNoSpeculativeWindow(swap *mempoolLiveSwap, expectedUsage cosmos.Uint, blocks int64) error {
	if err := waitForMempoolCondition("unfinalized observation without lifecycle "+swap.txID, mempoolObservationTimeout, func() (bool, error) {
		return mempoolSwapObservedUnfinalized(swap.txID)
	}); err != nil {
		return err
	}
	startHeight, err := thornode.GetBlockHeight()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out proving excluded swap %s stayed inert for %d blocks", swap.txID, blocks)
		}
		if err = requireMempoolSwapUnfinalized(swap.txID); err != nil {
			return err
		}
		usage, queryErr := thornode.GetMempoolSwapUsageTOR()
		if queryErr != nil {
			return queryErr
		}
		if !usage.Equal(expectedUsage) {
			return fmt.Errorf("excluded swap %s changed TOR usage: expected %s, got %s", swap.txID, expectedUsage, usage)
		}
		if has, queryErr := mempoolHasStagedOutbound(swap.txID); queryErr != nil || has {
			if queryErr != nil {
				return queryErr
			}
			return fmt.Errorf("excluded swap %s created a staged outbound", swap.txID)
		}
		if has, queryErr := mempoolHasRealOutbound(swap.txID); queryErr != nil || has {
			if queryErr != nil {
				return queryErr
			}
			return fmt.Errorf("excluded swap %s released an outbound", swap.txID)
		}
		if exists, queryErr := mempoolSwapQueueExists(swap.txID); queryErr != nil || exists {
			if queryErr != nil {
				return queryErr
			}
			return fmt.Errorf("excluded swap %s entered a swap queue", swap.txID)
		}
		destination, queryErr := mempoolDestinationBalance(swap)
		if queryErr != nil {
			return queryErr
		}
		if !destination.Equal(swap.destinationStart) {
			return fmt.Errorf("excluded swap %s credited destination before finality", swap.txID)
		}
		if err = requireMempoolPoolBalances(map[common.Asset]cosmos.Uint{
			swap.from: swap.fromPoolStart,
			swap.to:   swap.toPoolStart,
		}); err != nil {
			return err
		}
		height, queryErr := thornode.GetBlockHeight()
		if queryErr != nil {
			return queryErr
		}
		if height >= startHeight+blocks {
			return nil
		}
		time.Sleep(mempoolWaitInterval)
	}
}

func (a *MempoolSwapSimulationActor) waitForL1Completion(swap *mempoolLiveSwap, expectedUsage cosmos.Uint) error {
	return a.waitForL1CompletionWithVaultMode(swap, expectedUsage, true)
}

func (a *MempoolSwapSimulationActor) waitForL1CompletionWithVaultMode(swap *mempoolLiveSwap, expectedUsage cosmos.Uint, exactVault bool) error {
	return waitForMempoolCondition("final swap completion "+swap.txID, mempoolCompletionTimeout, func() (bool, error) {
		stages, err := thornode.GetTxStages(swap.txID)
		if err != nil || stages.OutboundSigned == nil || !stages.OutboundSigned.Completed {
			return false, err
		}
		details, err := thornode.GetTxDetails(swap.txID)
		if err != nil {
			return false, err
		}
		if details.GetFinalisedHeight() == 0 || details.GetReverted() {
			return false, nil
		}
		if len(details.Actions) != 1 || len(details.OutTxs) != 1 {
			return false, stopMempoolWait(fmt.Errorf("swap %s completed with %d actions and %d out txs", swap.txID, len(details.Actions), len(details.OutTxs)))
		}
		usage, err := thornode.GetMempoolSwapUsageTOR()
		if err != nil || !usage.Equal(expectedUsage) {
			return false, err
		}
		if has, queryErr := mempoolHasStagedOutbound(swap.txID); queryErr != nil || has {
			return false, queryErr
		}
		if exists, queryErr := mempoolSwapQueueExists(swap.txID); queryErr != nil || exists {
			return false, queryErr
		}
		destination, err := mempoolDestinationBalance(swap)
		if err != nil || !destination.GT(swap.destinationStart) {
			return false, err
		}
		vault, err := mempoolVaultAssetBalance(swap.vaultPubKey, swap.from)
		if err != nil {
			return false, err
		}
		expectedVault := swap.vaultStart.Add(swap.amount)
		if (exactVault && !vault.Equal(expectedVault)) || (!exactVault && vault.LT(expectedVault)) {
			return false, nil
		}
		return true, nil
	})
}

func (a *MempoolSwapSimulationActor) waitForRollbackCleanup(swap *mempoolLiveSwap, reverted bool) error {
	return waitForMempoolCondition("rollback cleanup "+swap.txID, 45*time.Second, func() (bool, error) {
		if err := a.requireRolledBackState(swap, reverted); err != nil {
			return false, err
		}
		return true, nil
	})
}

func (a *MempoolSwapSimulationActor) requireRolledBackState(swap *mempoolLiveSwap, reverted bool) error {
	usage, err := thornode.GetMempoolSwapUsageTOR()
	if err != nil {
		return err
	}
	if !usage.IsZero() {
		return fmt.Errorf("rollback %s retains TOR usage %s", swap.txID, usage)
	}
	if has, err := mempoolHasStagedOutbound(swap.txID); err != nil {
		return err
	} else if has {
		return fmt.Errorf("rollback %s retains staged outbound", swap.txID)
	}
	if has, err := mempoolHasRealOutbound(swap.txID); err != nil {
		return err
	} else if has {
		return fmt.Errorf("rollback %s released a real outbound", swap.txID)
	}
	if exists, err := mempoolSwapQueueExists(swap.txID); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("rollback %s retains swap queue work", swap.txID)
	}
	details, err := thornode.GetTxDetails(swap.txID)
	if err != nil {
		return err
	}
	if details.GetReverted() != reverted {
		return fmt.Errorf("rollback %s reverted=%t, expected %t", swap.txID, details.GetReverted(), reverted)
	}
	if len(details.Actions) != 0 || len(details.OutTxs) != 0 {
		return fmt.Errorf("rollback %s retained actions or out txs", swap.txID)
	}
	if err = requireMempoolPoolBalances(map[common.Asset]cosmos.Uint{
		swap.from: swap.fromPoolStart,
		swap.to:   swap.toPoolStart,
	}); err != nil {
		return err
	}
	vault, err := mempoolVaultAssetBalance(swap.vaultPubKey, swap.from)
	if err != nil {
		return err
	}
	if !vault.Equal(swap.vaultStart) {
		return fmt.Errorf("rollback %s changed source vault: expected %s, got %s", swap.txID, swap.vaultStart, vault)
	}
	destination, err := mempoolDestinationBalance(swap)
	if err != nil {
		return err
	}
	if !destination.Equal(swap.destinationStart) {
		return fmt.Errorf("rollback %s changed destination balance", swap.txID)
	}
	return nil
}

func waitForExpectedSecurityEvent(config *OpConfig, txID string) error {
	return waitForMempoolCondition("expected rollback security event "+txID, 30*time.Second, func() (bool, error) {
		state, ok := config.ExpectedSecurityEventState(txID, mempoolRollbackSecurityMsg)
		return ok && state.Observed == 1 && state.Pending == 0, nil
	})
}

type mempoolRollbackEventExpectation struct {
	timeout                bool
	expectedRollbackHeight int64
	finalisedHeight        int64
	requireOutboundHistory bool
}

type mempoolRollbackEventSummary struct {
	securityCount       int
	rollbackCount       int
	ordinaryErrataCount int
	rollbackHeight      int64
	scheduledOutboundAt []int64
	observedOutboundAt  []int64
}

func requireRollbackEvents(startHeight int64, txID string, expectation mempoolRollbackEventExpectation) error {
	endHeight, err := thornode.GetBlockHeight()
	if err != nil {
		return err
	}
	// Couple the finality snapshot to the history boundary. If finality lands
	// between the actor's earlier state check and this audit, an outbound in the
	// scanned range is legitimate only from that finalised height onward.
	if expectation.timeout && expectation.finalisedHeight == 0 {
		details, queryErr := thornode.GetTxDetails(txID)
		if queryErr != nil {
			return queryErr
		}
		if finalisedHeight := details.GetFinalisedHeight(); finalisedHeight > 0 && finalisedHeight <= endHeight {
			expectation.finalisedHeight = finalisedHeight
		}
	}
	var summary mempoolRollbackEventSummary
	for height := startHeight; height <= endHeight; height++ {
		block, queryErr := thornode.GetBlock(height)
		if queryErr != nil {
			return queryErr
		}
		if queryErr = auditMempoolRollbackBlock(block, txID, &summary); queryErr != nil {
			return queryErr
		}
	}
	return validateMempoolRollbackEventSummary(txID, expectation, summary)
}

func validateMempoolRollbackEventSummary(txID string, expectation mempoolRollbackEventExpectation, summary mempoolRollbackEventSummary) error {
	if summary.securityCount != 1 || summary.rollbackCount != 1 {
		return fmt.Errorf("rollback %s emitted security=%d rollback=%d, expected one each", txID, summary.securityCount, summary.rollbackCount)
	}
	if summary.ordinaryErrataCount != 0 {
		return fmt.Errorf("lifecycle rollback %s fell through to %d ordinary errata events", txID, summary.ordinaryErrataCount)
	}
	if expectation.expectedRollbackHeight > 0 && summary.rollbackHeight != expectation.expectedRollbackHeight {
		return fmt.Errorf("rollback %s occurred at height %d, expected recovery height %d", txID, summary.rollbackHeight, expectation.expectedRollbackHeight)
	}

	if !expectation.timeout || expectation.finalisedHeight == 0 {
		if len(summary.scheduledOutboundAt) != 0 || len(summary.observedOutboundAt) != 0 {
			return fmt.Errorf("rollback %s released outbound history before allowed finality: scheduled=%v outbound=%v", txID, summary.scheduledOutboundAt, summary.observedOutboundAt)
		}
		return nil
	}
	if expectation.requireOutboundHistory && (len(summary.scheduledOutboundAt) == 0 || len(summary.observedOutboundAt) == 0) {
		return fmt.Errorf("timeout recovery %s lacks post-finality outbound history: scheduled=%v outbound=%v", txID, summary.scheduledOutboundAt, summary.observedOutboundAt)
	}
	for _, height := range append(append([]int64(nil), summary.scheduledOutboundAt...), summary.observedOutboundAt...) {
		if height < expectation.finalisedHeight {
			return fmt.Errorf("timeout recovery %s released outbound at height %d before finality height %d", txID, height, expectation.finalisedHeight)
		}
	}
	return nil
}

func auditMempoolRollbackBlock(block openapi.BlockResponse, txID string, summary *mempoolRollbackEventSummary) error {
	if summary == nil {
		return errors.New("nil mempool rollback event summary")
	}
	for _, event := range mempoolBlockEvents(block) {
		switch event["type"] {
		case "security":
			if mempoolTxIDsEqual(event["id"], txID) && event["msg"] == mempoolRollbackSecurityMsg {
				summary.securityCount++
			}
		case "mempool_swap_reverted":
			if !mempoolTxIDsEqual(event["tx_id"], txID) {
				continue
			}
			reportedHeight, err := strconv.ParseInt(event["block_height"], 10, 64)
			if err != nil {
				return fmt.Errorf("rollback %s has invalid block_height %q at block %d: %w", txID, event["block_height"], block.Header.Height, err)
			}
			if reportedHeight != block.Header.Height {
				return fmt.Errorf("rollback %s reports block_height %d in block %d", txID, reportedHeight, block.Header.Height)
			}
			summary.rollbackCount++
			summary.rollbackHeight = block.Header.Height
		case "errata":
			if mempoolTxIDsEqual(event["in_tx_id"], txID) {
				summary.ordinaryErrataCount++
			}
		case "scheduled_outbound":
			if mempoolTxIDsEqual(event["in_hash"], txID) {
				summary.scheduledOutboundAt = append(summary.scheduledOutboundAt, block.Header.Height)
			}
		case "outbound":
			if mempoolTxIDsEqual(event["in_tx_id"], txID) {
				summary.observedOutboundAt = append(summary.observedOutboundAt, block.Header.Height)
			}
		}
	}
	return nil
}

func mempoolBlockEvents(block openapi.BlockResponse) []map[string]string {
	events := make([]map[string]string, 0)
	events = append(events, block.FinalizeBlockEvents...)
	events = append(events, block.BeginBlockEvents...)
	events = append(events, block.EndBlockEvents...)
	for _, tx := range block.Txs {
		events = append(events, tx.Result.Events...)
	}
	return events
}

func mempoolInboundFinalised(stages openapi.TxStagesResponse) bool {
	return stages.InboundFinalised != nil && stages.InboundFinalised.Completed
}

func requireMempoolSwapUnfinalized(txID string) error {
	observed, err := mempoolSwapObservedUnfinalized(txID)
	if err != nil {
		return err
	}
	if !observed {
		stages, queryErr := thornode.GetTxStages(txID)
		if queryErr == nil && stages.InboundFinalised != nil && stages.InboundFinalised.Completed {
			return stopMempoolWait(fmt.Errorf("missed required pre-finality window for %s", txID))
		}
	}
	return nil
}

func mempoolSwapObservedUnfinalized(txID string) (bool, error) {
	stages, err := thornode.GetTxStages(txID)
	if err != nil {
		return false, err
	}
	if stages.InboundFinalised != nil && stages.InboundFinalised.Completed {
		return false, stopMempoolWait(fmt.Errorf("missed required pre-finality window for %s", txID))
	}
	return stages.InboundObserved.Completed, nil
}

func mempoolHasStagedOutbound(txID string) (bool, error) {
	staged, err := thornode.GetStagedOutbounds()
	if err != nil {
		return false, err
	}
	for _, item := range staged.StagedOutboundItems {
		if item.TxOutItem != nil && item.TxOutItem.InHash != nil && mempoolTxIDsEqual(*item.TxOutItem.InHash, txID) {
			return true, nil
		}
	}
	return false, nil
}

func mempoolStagedOutboundHeight(txID string) (int64, error) {
	staged, err := thornode.GetStagedOutbounds()
	if err != nil {
		return 0, err
	}
	var height int64
	var matches int
	for _, item := range staged.StagedOutboundItems {
		if item.TxOutItem == nil || item.TxOutItem.InHash == nil || !mempoolTxIDsEqual(*item.TxOutItem.InHash, txID) {
			continue
		}
		matches++
		if item.StagedHeight == nil || item.GetStagedHeight() <= 0 {
			return 0, fmt.Errorf("staged outbound for %s has no valid staged height", txID)
		}
		if height == 0 {
			height = item.GetStagedHeight()
		} else if height != item.GetStagedHeight() {
			return 0, fmt.Errorf("staged outbounds for %s span heights %d and %d", txID, height, item.GetStagedHeight())
		}
	}
	if matches == 0 {
		return 0, fmt.Errorf("no staged outbound found for %s", txID)
	}
	return height, nil
}

// mempoolTimeoutRecoveryHeight returns the first LackSigning recovery cadence
// at or after the staged outbound reaches its configured timeout age.
func mempoolTimeoutRecoveryHeight(stagedHeight, signingPeriod, attempts int64) int64 {
	threshold := stagedHeight + signingPeriod*attempts
	if remainder := threshold % signingPeriod; remainder != 0 {
		threshold += signingPeriod - remainder
	}
	return threshold
}

func requireMempoolTimeoutPending(txID string, expectedRecoveryHeight int64) error {
	height, err := thornode.GetBlockHeight()
	if err != nil {
		return err
	}
	if height >= expectedRecoveryHeight {
		return fmt.Errorf("observed timeout swap %s at height %d after expected recovery height %d", txID, height, expectedRecoveryHeight)
	}
	if err = requireMempoolSwapUnfinalized(txID); err != nil {
		return err
	}
	hasStaged, err := mempoolHasStagedOutbound(txID)
	if err != nil {
		return err
	}
	if !hasStaged {
		return fmt.Errorf("timeout swap %s lost staged outbound before recovery height %d", txID, expectedRecoveryHeight)
	}
	return nil
}

func mempoolHasRealOutbound(txID string) (bool, error) {
	outbounds, err := thornode.GetOutboundQueue()
	if err != nil {
		return false, err
	}
	for _, item := range outbounds {
		if item.InHash != nil && mempoolTxIDsEqual(*item.InHash, txID) {
			return true, nil
		}
	}
	scheduled, err := thornode.GetScheduledOutboundQueue()
	if err != nil {
		return false, err
	}
	for _, item := range scheduled {
		if item.InHash != nil && mempoolTxIDsEqual(*item.InHash, txID) {
			return true, nil
		}
	}
	return false, nil
}

func mempoolSwapQueueExists(txID string) (bool, error) {
	_, err := thornode.GetSwapDetails(txID)
	if err == nil {
		return true, nil
	}
	if mempoolQueueNotFound(err) {
		return false, nil
	}
	return false, err
}

func mempoolQueueNotFound(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found in any queue")
}

func requireMempoolPoolBalances(expected map[common.Asset]cosmos.Uint) error {
	for asset, amount := range expected {
		current, err := mempoolPoolAssetBalance(asset)
		if err != nil {
			return err
		}
		if !current.Equal(amount) {
			return fmt.Errorf("pool %s asset balance changed: expected %s, got %s", asset.GetLayer1Asset(), amount, current)
		}
	}
	return nil
}

func mempoolTxIDsEqual(a, b string) bool {
	a = strings.TrimPrefix(strings.TrimSpace(a), "0x")
	b = strings.TrimPrefix(strings.TrimSpace(b), "0x")
	return strings.EqualFold(a, b)
}

type mempoolStopWaitError struct {
	err error
}

func (e mempoolStopWaitError) Error() string { return e.err.Error() }
func (e mempoolStopWaitError) Unwrap() error { return e.err }

func stopMempoolWait(err error) error {
	return mempoolStopWaitError{err: err}
}

func waitForMempoolCondition(description string, timeout time.Duration, check func() (bool, error)) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		ready, err := check()
		if ready && err == nil {
			return nil
		}
		if err != nil {
			var terminal mempoolStopWaitError
			if errors.As(err, &terminal) {
				return terminal.err
			}
			lastErr = err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("timed out waiting for %s: %w", description, lastErr)
			}
			return fmt.Errorf("timed out waiting for %s", description)
		}
		time.Sleep(mempoolWaitInterval)
	}
}
