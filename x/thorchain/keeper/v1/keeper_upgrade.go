package keeperv1

import (
	"bytes"
	"fmt"
	"strings"

	upgradetypes "github.com/cosmos/cosmos-sdk/x/upgrade/types"

	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

// GetUpgradePlan proxies through to the upgrade keeper
func (k KVStore) GetUpgradePlan(ctx cosmos.Context) (upgradetypes.Plan, bool) {
	return k.upgradeKeeper.GetUpgradePlan(ctx)
}

// ScheduleUpgrade proxies through to the upgrade keeper
func (k KVStore) ScheduleUpgrade(ctx cosmos.Context, plan upgradetypes.Plan) error {
	return k.upgradeKeeper.ScheduleUpgrade(ctx, plan)
}

// ClearUpgradePlan proxies through to the upgrade keeper
func (k KVStore) ClearUpgradePlan(ctx cosmos.Context) {
	k.upgradeKeeper.ClearUpgradePlan(ctx)
}

// ProposeUpgrade proposes an upgrade by name
func (k KVStore) ProposeUpgrade(ctx cosmos.Context, name string, upgrade types.Upgrade) error {
	key := fmt.Sprintf("%s%s", prefixUpgradeProposals, name)
	store := ctx.KVStore(k.storeKey)

	v, err := k.cdc.Marshal(&upgrade)
	if err != nil {
		return fmt.Errorf("failed to marshal proposed upgrade: %w", err)
	}

	store.Set([]byte(key), v)

	return nil
}

// GetProposedUpgrade retrieves a proposed upgrade
func (k KVStore) GetProposedUpgrade(ctx cosmos.Context, name string) (types.Upgrade, error) {
	key := fmt.Sprintf("%s%s", prefixUpgradeProposals, name)
	store := ctx.KVStore(k.storeKey)

	v := store.Get([]byte(key))
	if v == nil {
		return types.Upgrade{}, fmt.Errorf("no proposed upgrade for name: %s", name)
	}

	var upgrade types.Upgrade
	if err := k.cdc.Unmarshal(v, &upgrade); err != nil {
		return types.Upgrade{}, fmt.Errorf("failed to unmarshal proposed upgrade: %w", err)
	}

	return upgrade, nil
}

// ApproveUpgrade approves an upgrade as a validator
func (k KVStore) ApproveUpgrade(ctx cosmos.Context, addr cosmos.AccAddress, name string) error {
	key := fmt.Sprintf("%s%s", VotePrefix(name), addr.String())
	store := ctx.KVStore(k.storeKey)

	store.Set(append([]byte(key), addr...), []byte{0x1})
	return nil
}

// RejectUpgrade rejects an upgrade as a validator
func (k KVStore) RejectUpgrade(ctx cosmos.Context, addr cosmos.AccAddress, name string) error {
	key := fmt.Sprintf("%s%s", VotePrefix(name), addr.String())
	store := ctx.KVStore(k.storeKey)

	store.Set(append([]byte(key), addr...), []byte{0xFF})
	return nil
}

// RemoveExpiredUpgradeProposals removes an upgrade proposal and all votes
// after the proposal height has passed.
func (k KVStore) RemoveExpiredUpgradeProposals(ctx cosmos.Context) error {
	iter := k.GetUpgradeProposalIterator(ctx)
	defer iter.Close()

	store := ctx.KVStore(k.storeKey)

	for ; iter.Valid(); iter.Next() {
		key, value := iter.Key(), iter.Value()

		nameSplit := strings.Split(string(key), "/")
		name := nameSplit[len(nameSplit)-1]

		var upgrade types.Upgrade
		if err := k.cdc.Unmarshal(value, &upgrade); err != nil {
			return fmt.Errorf("failed to unmarshal proposed upgrade: %w", err)
		}

		if ctx.BlockHeight() <= upgrade.Height {
			continue
		}

		ctx.Logger().Info(
			"Deleting expired upgrade proposal",
			"name", name,
		)

		k.removeExpiredUpgradeProposalVotes(ctx, name)
		store.Delete(key)
	}

	return nil
}

func (k KVStore) removeExpiredUpgradeProposalVotes(ctx cosmos.Context, name string) {
	store := ctx.KVStore(k.storeKey)

	iter := k.GetUpgradeVoteIterator(ctx, name)
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		store.Delete(iter.Key())
	}
}

// UpgradeApprovedByMajority returns true and no error if the upgrade is approved by 2/3 of Validators.
// it additionally returns the current approval percentage and the bond needed to reach quorum, if not approved.
func (k KVStore) UpgradeApprovedByMajority(ctx cosmos.Context, name string) (bool, string, cosmos.Uint, error) {
	iterA := k.GetNodeAccountIterator(ctx)
	defer iterA.Close()

	totalActiveBond := cosmos.ZeroUint()
	bonds := make(map[string]cosmos.Uint)

	for ; iterA.Valid(); iterA.Next() {
		var na NodeAccount
		if err := k.Cdc().Unmarshal(iterA.Value(), &na); err != nil {
			return false, "", cosmos.Uint{}, fmt.Errorf("failed to unmarshal: %w", err)
		}

		if na.IsEmpty() || na.Status != NodeActive || na.Type != NodeTypeValidator {
			continue
		}

		totalActiveBond = totalActiveBond.Add(na.Bond)
		bonds[na.NodeAddress.String()] = na.Bond
	}

	scalingFactor := cosmos.OneUint()
	if totalActiveBond.LTE(cosmos.NewUint(1000000000000000)) {
		// scale for precision with small bond amounts
		scalingFactor = cosmos.NewUint(10000)
	}

	// 2/3 of active validators
	bondThreshold := totalActiveBond.Mul(scalingFactor).MulUint64(2).QuoUint64(3)

	iterV := k.GetUpgradeVoteIterator(ctx, name)
	defer iterV.Close()

	totalApprovedBond := cosmos.ZeroUint()

	for ; iterV.Valid(); iterV.Next() {
		key, vote := iterV.Key(), iterV.Value()
		if !bytes.Equal(vote, []byte{0x1}) {
			continue
		}

		prefix := []byte(VotePrefix(name))
		addr := cosmos.AccAddress(bytes.TrimPrefix(key, prefix))

		bond, ok := bonds[addr.String()]
		if !ok {
			// this could happen if a validator votes and then becomes inactive
			continue
		}

		totalApprovedBond = totalApprovedBond.Add(bond.Mul(scalingFactor))
	}

	currentApproval := cosmos.NewDecFromBigInt(totalApprovedBond.BigInt()).Quo(cosmos.NewDecFromBigInt(totalActiveBond.BigInt())).Quo(cosmos.NewDecFromBigInt(scalingFactor.BigInt())).String()

	if totalApprovedBond.GTE(bondThreshold) {
		return true, currentApproval, cosmos.ZeroUint(), nil
	}

	bondToQuorum := bondThreshold.Sub(totalApprovedBond).Quo(scalingFactor)

	return false, currentApproval, bondToQuorum, nil
}
