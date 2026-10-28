package keeperv1

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unsafe"

	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/telemetry"
	"github.com/hashicorp/go-metrics"
	"google.golang.org/protobuf/encoding/protowire"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

func (k KVStore) setNodeAccount(ctx cosmos.Context, key []byte, record NodeAccount) {
	keeper.ActiveValidatorsCacheFromContext(ctx).Disable()
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getNodeAccount(ctx cosmos.Context, key []byte, record *NodeAccount) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	bz := store.Get(key)
	if k.memoDecodeNodeAccount(key, bz, record) {
		return true, nil
	}
	// Only bytes the memo cannot decode get here. They are decoded into a
	// value of their own and copied over (the codec resets its target first,
	// so the result is the same): handed to the codec interface or to fmt,
	// record itself would move to the heap on every read.
	decoded := new(NodeAccount)
	err := k.cdc.Unmarshal(bz, decoded)
	*record = *decoded
	if err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", decoded, key), err)
	}
	return true, nil
}

// TotalActiveValidators count the number of active node account
func (k KVStore) TotalActiveValidators(ctx cosmos.Context) (int, error) {
	activeNodes, err := k.ListActiveValidators(ctx)
	return len(activeNodes), err
}

// ListValidatorsWithBond - gets a list of all validator node accounts that have bond
// Note: the order of node account in the result is not defined
func (k KVStore) ListValidatorsWithBond(ctx cosmos.Context) (NodeAccounts, error) {
	nodeAccounts := make(NodeAccounts, 0)
	naIterator := k.GetNodeAccountIterator(ctx)
	defer naIterator.Close()
	for ; naIterator.Valid(); naIterator.Next() {
		var na NodeAccount
		if !k.memoDecodeNodeAccount(naIterator.Key(), naIterator.Value(), &na) {
			if err := k.cdc.Unmarshal(naIterator.Value(), &na); err != nil {
				return nodeAccounts, dbError(ctx, "Unmarshal: node account", err)
			}
		}
		if na.Type == NodeTypeValidator && !na.Bond.IsZero() {
			nodeAccounts = append(nodeAccounts, na)
		}
	}
	return nodeAccounts, nil
}

// ListValidatorsByStatus - get a list of validator node accounts with the given status
func (k KVStore) ListValidatorsByStatus(ctx cosmos.Context, status NodeStatus) (NodeAccounts, error) {
	var cache *keeper.ActiveValidatorsCache
	if status == NodeActive {
		cache = keeper.ActiveValidatorsCacheFromContext(ctx)
		if nodeAccounts, ok := cache.Get(); ok {
			return nodeAccounts, nil
		}
	}
	nodeAccounts, err := k.listValidatorsByStatus(ctx, status, -1)
	if err == nil {
		cache.Set(nodeAccounts)
	}
	return nodeAccounts, err
}

// listValidatorsByStatus lists validators with the given status in store
// order, keeping at most limit of them (limit < 0: all). The whole node
// account store is iterated whatever the limit, so store access, and so gas,
// is the same as for an unlimited listing; past the limit, matching records
// are just not copied out.
func (k KVStore) listValidatorsByStatus(ctx cosmos.Context, status NodeStatus, limit int) (NodeAccounts, error) {
	// The matches are gathered first and copied out together, into one
	// exactly sized list (keeper.DetachNodeAccounts); ~100 pointers fit on
	// the stack.
	var buf [128]*NodeAccount
	matches := buf[:0]
	naIterator := k.GetNodeAccountIterator(ctx)
	defer naIterator.Close()
	for ; naIterator.Valid(); naIterator.Next() {
		full := limit >= 0 && len(matches) >= limit
		// Node accounts rarely change, so the decoding of nearly every record,
		// matching or not (e.g. ~100 of ~190 are active), is memoized under its
		// exact bytes: filter on it and copy out only the matches. The
		// iteration, and so the store access, is unchanged.
		if na := nodeAccountDecodeMemo.shared(k, naIterator.Key(), naIterator.Value()); na != nil {
			if !full && na.Type == NodeTypeValidator && na.Status == status {
				matches = append(matches, na)
			}
			continue
		}
		// Bytes that do not decode: as before, any such record, matching or
		// not, fails the listing with its unmarshal error.
		var na NodeAccount
		if err := k.unmarshalNodeAccount(naIterator.Value(), &na); err != nil {
			return copyNodeAccounts(matches), dbError(ctx, "Unmarshal: node account", err)
		}
		if !full && na.Type == NodeTypeValidator && na.Status == status {
			matches = append(matches, &na)
		}
	}
	return copyNodeAccounts(matches), nil
}

// copyNodeAccounts returns copies of *nas, in order, sharing nothing mutable
// with them: matches of a listing may be memoized values.
func copyNodeAccounts(nas []*NodeAccount) NodeAccounts {
	out := make(NodeAccounts, len(nas))
	for i, na := range nas {
		out[i] = *na
	}
	keeper.DetachNodeAccounts(out)
	return out
}

// CountActiveValidators is len(ListActiveValidators(ctx)) without copying the
// node accounts out: served from the same cache when one is set, else from
// the same iteration as the listing, so store access and gas are identical.
func (k KVStore) CountActiveValidators(ctx cosmos.Context) (int, error) {
	if nodeAccounts, ok := keeper.ActiveValidatorsCacheFromContext(ctx).Get(); ok {
		return len(nodeAccounts), nil
	}
	return k.countValidatorsByStatus(ctx, NodeActive)
}

// countValidatorsByStatus counts what listValidatorsByStatus(ctx, status, -1)
// would list, reading the store in the same calls and order: matching records
// are counted on their memoized decoding instead of being cloned.
func (k KVStore) countValidatorsByStatus(ctx cosmos.Context, status NodeStatus) (int, error) {
	count := 0
	naIterator := k.GetNodeAccountIterator(ctx)
	defer naIterator.Close()
	for ; naIterator.Valid(); naIterator.Next() {
		na := nodeAccountDecodeMemo.shared(k, naIterator.Key(), naIterator.Value())
		if na == nil {
			// as the listing: any record that does not decode fails it
			var decoded NodeAccount
			if err := k.unmarshalNodeAccount(naIterator.Value(), &decoded); err != nil {
				return count, dbError(ctx, "Unmarshal: node account", err)
			}
			na = &decoded
		}
		if na.Type == NodeTypeValidator && na.Status == status {
			count++
		}
	}
	return count, nil
}

const nodeAccountSignerMembershipField protowire.Number = 9

// unmarshalNodeAccount is k.cdc.Unmarshal(bz, na) with signer_membership
// decoded here. Nodes keep every vault they were ever a member of (on mainnet
// ~100 each), so Unmarshal made ~100 string allocations per node; here the
// entries share one buffer, in wire order as Unmarshal appends them. The buffer
// is never written after the strings are made. The other fields go through
// Unmarshal unchanged; bytes it cannot split are left to Unmarshal whole.
func (k KVStore) unmarshalNodeAccount(bz []byte, na *NodeAccount) error {
	return k.decodeNodeAccount(bz, na, false)
}

// decodeNodeAccount is unmarshalNodeAccount; if immutable, the caller
// guarantees bz is never written, and the signer_membership strings point
// into bz instead of a copy.
func (k KVStore) decodeNodeAccount(bz []byte, na *NodeAccount, immutable bool) error {
	count, size := 0, 0
	for rest := bz; len(rest) > 0; {
		num, wtype, n := protowire.ConsumeTag(rest)
		if n < 0 {
			return k.cdc.Unmarshal(bz, na)
		}
		m := protowire.ConsumeFieldValue(num, wtype, rest[n:])
		if m < 0 {
			return k.cdc.Unmarshal(bz, na)
		}
		if num == nodeAccountSignerMembershipField {
			if wtype != protowire.BytesType {
				return k.cdc.Unmarshal(bz, na)
			}
			count++
			size += m
		}
		rest = rest[n+m:]
	}
	if count == 0 {
		return k.cdc.Unmarshal(bz, na)
	}

	other := make([]byte, 0, len(bz)-size)
	var buf []byte
	if !immutable {
		buf = make([]byte, 0, size)
	}
	members := make([]string, 0, count)
	for rest := bz; len(rest) > 0; {
		num, wtype, n := protowire.ConsumeTag(rest)
		m := protowire.ConsumeFieldValue(num, wtype, rest[n:])
		if num == nodeAccountSignerMembershipField {
			v, _ := protowire.ConsumeBytes(rest[n:])
			if !immutable {
				start := len(buf)
				buf = append(buf, v...)
				v = buf[start:]
			}
			members = append(members, unsafe.String(unsafe.SliceData(v), len(v)))
		} else {
			other = append(other, rest[:n+m]...)
		}
		rest = rest[n+m:]
	}
	if err := k.cdc.Unmarshal(other, na); err != nil {
		return err
	}
	na.SignerMembership = members
	return nil
}

// ListActiveValidators - get a list of active validator node accounts
func (k KVStore) ListActiveValidators(ctx cosmos.Context) (NodeAccounts, error) {
	return k.ListValidatorsByStatus(ctx, NodeActive)
}

// ListActiveValidatorsUpTo returns the first n active validators of
// ListActiveValidators, with the same store access (the whole node account
// store is iterated), without unmarshalling the rest.
func (k KVStore) ListActiveValidatorsUpTo(ctx cosmos.Context, n int) (NodeAccounts, error) {
	return k.listValidatorsByStatus(ctx, NodeActive, n)
}

func (k KVStore) RemoveLowBondValidatorAccounts(ctx cosmos.Context) error {
	var events cosmos.Events
	lowBondValidators := make([][]byte, 0)
	naIterator := k.GetNodeAccountIterator(ctx)
	defer naIterator.Close()
	for ; naIterator.Valid(); naIterator.Next() {
		var na NodeAccount
		if !k.memoDecodeNodeAccount(naIterator.Key(), naIterator.Value(), &na) {
			if err := k.cdc.Unmarshal(naIterator.Value(), &na); err != nil {
				return dbError(ctx, "Unmarshal: node account", err)
			}
		}
		if na.Type == NodeTypeVault || na.Status == NodeActive {
			continue
		}
		if na.Type == NodeTypeValidator && na.Bond.LTE(cosmos.NewUint(common.One)) {
			if na.Bond.IsZero() {
				lowBondValidators = append(lowBondValidators, naIterator.Key())
				continue
			}
			bps, err := k.GetBondProviders(ctx, na.NodeAddress)
			if err != nil {
				return err
			}
			to, err := na.BondAddress.AccAddress()
			if err != nil {
				return dbError(ctx, "", fmt.Errorf("fail to parse bond address(%s)", na.BondAddress))
			}

			// No bond providers
			if len(bps.Providers) == 0 {
				coin := common.NewCoin(common.RuneAsset(), na.Bond)
				if err = k.SendFromModuleToAccount(ctx, BondName, to, common.NewCoins(coin)); err != nil {
					ctx.Logger().Error("failed to return bond pool coins", "error", err)
					continue
				}
				bondEvent := NewEventBond(na.Bond, BondReturned, common.Tx{}, &na, to)
				if events, err = bondEvent.Events(); err != nil {
					ctx.Logger().Error("fail to emit bond event", "error", err)
				} else {
					ctx.EventManager().EmitEvents(events)
				}
				lowBondValidators = append(lowBondValidators, naIterator.Key())
				continue
			}

			bps.Adjust(na.Bond)
			totalSent := cosmos.ZeroUint()
			sendFailed := false
			for _, provider := range bps.Providers {
				if provider.Bond.IsZero() || provider.BondAddress.Empty() {
					continue
				}
				coin := common.NewCoin(common.RuneAsset(), provider.Bond)
				if err = k.SendFromModuleToAccount(ctx, BondName, provider.BondAddress, common.NewCoins(coin)); err != nil {
					ctx.Logger().Error("failed to return bond pool coins", "error", err)
					sendFailed = true
					continue
				}
				bondEvent := NewEventBond(provider.Bond, BondReturned, common.Tx{}, &na, provider.BondAddress)
				if events, err = bondEvent.Events(); err != nil {
					ctx.Logger().Error("fail to emit bond event", "error", err)
				} else {
					ctx.EventManager().EmitEvents(events)
				}
				totalSent = totalSent.Add(provider.Bond)
			}
			// sanity check
			if totalSent.GT(na.Bond) {
				return dbError(ctx, "", fmt.Errorf("total bond returned greater than node bond"))
			}

			// If any send failed, preserve node account and bond providers
			// so funds remain recoverable in a future attempt.
			if sendFailed {
				na.Bond = na.Bond.Sub(totalSent)
				if err = k.SetNodeAccount(ctx, na); err != nil {
					return dbError(ctx, "", fmt.Errorf("fail to save node account: %w", err))
				}
				continue
			}

			// remove bond providers
			k.del(ctx, k.GetKey(prefixBondProviders, na.NodeAddress.String()))
			lowBondValidators = append(lowBondValidators, naIterator.Key())
		}
	}
	for _, naKey := range lowBondValidators {
		k.del(ctx, naKey)
	}
	return nil
}

// GetMinJoinVersion - get min version to join. Min version is the most popular version
func (k KVStore) GetMinJoinVersion(ctx cosmos.Context) semver.Version {
	type tmpVersionInfo struct {
		version semver.Version
		count   int
	}
	var vCount []tmpVersionInfo
	nodes, err := k.ListActiveValidators(ctx)
	if err != nil {
		_ = dbError(ctx, "Unable to list active node accounts", err)
		return semver.Version{}
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		return nodes[i].GetVersion().LT(nodes[j].GetVersion())
	})
	for _, na := range nodes {
		exist := false
		for _, item := range vCount {
			if item.version.String() == na.Version {
				exist = true
				break
			}
		}
		if !exist {
			vCount = append(vCount, tmpVersionInfo{
				version: na.GetVersion(),
				count:   0,
			})
		}

		// assume all versions are backward compatible
		for k, v := range vCount {
			if v.version.LTE(na.GetVersion()) {
				v.count++
				vCount[k] = v
			}
		}
	}
	totalCount := len(nodes)
	version := semver.Version{}
	// sort it by version descending
	sort.SliceStable(vCount, func(i, j int) bool {
		return vCount[i].version.GT(vCount[j].version)
	})

	for _, info := range vCount {
		// skip those version that doesn't have majority
		if !HasSuperMajority(info.count, totalCount) {
			continue
		}
		if info.version.GT(version) {
			version = info.version
		}

	}
	return version
}

// GetLowestActiveVersion - get version number of lowest active node
func (k KVStore) GetLowestActiveVersion(ctx cosmos.Context) semver.Version {
	nodes, err := k.ListActiveValidators(ctx)
	if err != nil {
		_ = dbError(ctx, "Unable to list active node accounts", err)
		return constants.SWVersion
	}
	if len(nodes) > 0 {
		version := nodes[0].GetVersion()
		for _, na := range nodes {
			if na.GetVersion().LT(version) {
				version = na.GetVersion()
			}
		}
		return version
	}
	return constants.SWVersion
}

// GetNodeAccount try to get node account with the given address from db
func (k KVStore) GetNodeAccount(ctx cosmos.Context, addr cosmos.AccAddress) (NodeAccount, error) {
	// A stored record replaces the whole default (the memo copies over it, the
	// codec resets it first), so the default is built only when there is none.
	var record NodeAccount
	found, err := k.getNodeAccount(ctx, k.GetKey(prefixNodeAccount, addr.String()), &record)
	if !found {
		emptyPubKeySet := common.PubKeySet{
			Secp256k1: common.EmptyPubKey,
			Ed25519:   common.EmptyPubKey,
		}
		return NewNodeAccount(addr, NodeUnknown, emptyPubKeySet, "", cosmos.ZeroUint(), "", ctx.BlockHeight()), err
	}
	return record, err
}

// GetNodeAccountByPubKey try to get node account with the given pubkey from db
func (k KVStore) GetNodeAccountByPubKey(ctx cosmos.Context, pk common.PubKey) (NodeAccount, error) {
	addr, err := pk.GetThorAddress()
	if err != nil {
		return NodeAccount{}, err
	}
	return k.GetNodeAccount(ctx, addr)
}

// SetNodeAccount save the given node account into data store
func (k KVStore) SetNodeAccount(ctx cosmos.Context, na NodeAccount) error {
	if na.IsEmpty() {
		return nil
	}
	if na.Status == NodeActive {
		if na.ActiveBlockHeight == 0 {
			// the na is active, and does not have a block height when they
			// became active. This must be the first block they are active, so
			// THORNode will set it now.
			na.ActiveBlockHeight = ctx.BlockHeight()
			k.ResetNodeAccountSlashPoints(ctx, na.NodeAddress) // reset slash points
		}
	}

	addr := na.NodeAddress.String()
	key := k.GetKey(prefixNodeAccount, addr)
	// before the write, so that the stored account still holds the bond before it
	k.syncNodeAgeStart(ctx, na, addr, key)
	k.setNodeAccount(ctx, key, na)
	return nil
}

// EnsureNodeKeysUnique check the given consensus pubkey and pubkey set against all the the node account
// return an error when it is overlap with any existing account
func (k KVStore) EnsureNodeKeysUnique(ctx cosmos.Context, consensusPubKey string, pubKeys common.PubKeySet) error {
	if strings.TrimSpace(consensusPubKey) == "" {
		return dbError(ctx, "", errors.New("Validator Consensus Key cannot be empty"))
	}
	if pubKeys.IsEmpty() {
		return dbError(ctx, "", errors.New("PubKeySet cannot be empty"))
	}

	iter := k.GetNodeAccountIterator(ctx)
	defer iter.Close()
	for ; iter.Valid(); iter.Next() {
		var na NodeAccount
		if !k.memoDecodeNodeAccount(iter.Key(), iter.Value(), &na) {
			if err := k.cdc.Unmarshal(iter.Value(), &na); err != nil {
				return dbError(ctx, "Unmarshal: node account", err)
			}
		}
		if na.ValidatorConsPubKey == consensusPubKey {
			return dbError(ctx, "", fmt.Errorf("%s already exist", na.ValidatorConsPubKey))
		}
		if na.PubKeySet.Contains(pubKeys.Secp256k1) {
			return dbError(ctx, "", fmt.Errorf("%s already exist", pubKeys))
		}
		if na.PubKeySet.Contains(pubKeys.Ed25519) {
			return dbError(ctx, "", fmt.Errorf("%s already exist", pubKeys))
		}
	}

	return nil
}

// GetNodeAccountIterator iterate node account
func (k KVStore) GetNodeAccountIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixNodeAccount)
}

// GetUpgradeProposalIterator to iterate upgrade proposals.
func (k KVStore) GetUpgradeProposalIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixUpgradeProposals)
}

// GetUpgradeVoteIterator to iterate upgrade votes for a named proposal.
func (k KVStore) GetUpgradeVoteIterator(ctx cosmos.Context, name string) cosmos.Iterator {
	return k.getIterator(ctx, types.DbPrefix(VotePrefix(name)))
}

func VotePrefix(name string) string {
	return fmt.Sprintf("%s%s/", prefixUpgradeVotes, name)
}

// nodeAgeStartBelowMinBond is the age start recorded for a node whose bond is below
// MinimumBondInRune: it has no age until its bond reaches the minimum again.
const nodeAgeStartBelowMinBond int64 = -1

// GetNodeAgeStart returns the height from which the given node has held at least
// MinimumBondInRune without a break, and false when its bond is below the minimum or
// no height is recorded for it.
func (k KVStore) GetNodeAgeStart(ctx cosmos.Context, addr cosmos.AccAddress) (int64, bool, error) {
	start := int64(0)
	ok, err := k.getInt64(ctx, k.GetKey(prefixNodeAgeStart, addr.String()), &start)
	if err != nil || !ok || start < 0 {
		return 0, false, err
	}
	return start, true, nil
}

// ResetNodeAgeStart records the given node as below the minimum bond, so that its age
// starts when its bond next reaches MinimumBondInRune.
func (k KVStore) ResetNodeAgeStart(ctx cosmos.Context, addr cosmos.AccAddress) {
	k.setInt64(ctx, k.GetKey(prefixNodeAgeStart, addr.String()), nodeAgeStartBelowMinBond)
}

// SyncNodeAgeStart brings the age start of the given node in line with its bond: a
// bond below MinimumBondInRune clears it, and a bond that reaches the minimum starts it
// at the current height. SetNodeAccount calls it on every write, before the account is
// stored, so the stored account holds the bond before the write. A change of
// MinimumBondInRune writes no node account, so the churn calls it as well.
func (k KVStore) SyncNodeAgeStart(ctx cosmos.Context, na NodeAccount) {
	addr := na.NodeAddress.String()
	k.syncNodeAgeStart(ctx, na, addr, k.GetKey(prefixNodeAccount, addr))
}

// syncNodeAgeStart is SyncNodeAgeStart for the node at addr, whose account is stored at
// naKey.
func (k KVStore) syncNodeAgeStart(ctx cosmos.Context, na NodeAccount, addr string, naKey []byte) {
	minBond := cosmos.NewUint(uint64(k.GetConfigInt64(ctx, constants.MinimumBondInRune)))
	held := holdsMinBond(na.Bond, minBond)
	key := k.GetKey(prefixNodeAgeStart, addr)
	start := int64(0)
	ok, err := k.getInt64(ctx, key, &start)
	switch {
	case err != nil:
		// restarted, which can only make the node younger
		ctx.Logger().Error("fail to get node age start, restarting it", "node_address", addr, "error", err)
		start = nodeAgeStartBelowMinBond
		if held {
			start = ctx.BlockHeight()
		}
		k.setInt64(ctx, key, start)
	case !ok:
		if first, judged := k.firstNodeAgeStart(ctx, addr, naKey, held, minBond); judged {
			k.setInt64(ctx, key, first)
		}
	case !held:
		if start >= 0 {
			k.setInt64(ctx, key, nodeAgeStartBelowMinBond)
		}
	case start < 0:
		k.setInt64(ctx, key, ctx.BlockHeight())
	case k.storedBelowMinBond(ctx, addr, naKey, minBond):
		// MinimumBondInRune rose above the stored bond since the last sync, and this
		// write reaches it
		k.setInt64(ctx, key, ctx.BlockHeight())
	}
}

// storedBelowMinBond reports whether the node account stored at naKey holds less than
// minBond. It runs on most node account writes, so it reads the bond from the
// shared decode memo (the write's caller has usually just read the same bytes) instead
// of copying the account out. An account that cannot be decoded counts as below the
// minimum, which can only make the node younger.
func (k KVStore) storedBelowMinBond(ctx cosmos.Context, addr string, naKey []byte, minBond cosmos.Uint) bool {
	bz := k.kvStore(ctx).Get(naKey)
	if bz == nil {
		return false
	}
	stored := nodeAccountDecodeMemo.shared(k, naKey, bz) // read only
	if stored == nil {
		ctx.Logger().Error("fail to decode node account, restarting its age", "node_address", addr)
		return true
	}
	return !holdsMinBond(stored.Bond, minBond)
}

// firstNodeAgeStart returns the age start of a node that has none recorded: an account
// stored before the age start existed, or imported at genesis. Its bond history is not
// stored. If the stored account already held the minimum and still does, the age counts
// from the earliest height that account holds, StatusSince or an earlier
// ActiveBlockHeight, which can only make the node look younger than its registration.
// If its bond reaches the minimum with this write, the age starts now. It returns false
// for an account not stored yet, as during the genesis import, before the mimirs are:
// that one is judged at its next write or at the churn.
func (k KVStore) firstNodeAgeStart(ctx cosmos.Context, addr string, naKey []byte, held bool, minBond cosmos.Uint) (int64, bool) {
	var stored NodeAccount
	found, err := k.getNodeAccount(ctx, naKey, &stored)
	switch {
	case err != nil:
		ctx.Logger().Error("fail to get node account, judging its age by this write", "node_address", addr, "error", err)
		if held {
			return ctx.BlockHeight(), true
		}
		return nodeAgeStartBelowMinBond, true
	case !found:
		return 0, false
	case !held:
		return nodeAgeStartBelowMinBond, true
	case !holdsMinBond(stored.Bond, minBond):
		return ctx.BlockHeight(), true
	}
	start := stored.StatusSince
	if stored.ActiveBlockHeight > 0 && stored.ActiveBlockHeight < start {
		start = stored.ActiveBlockHeight
	}
	return start, true
}

// holdsMinBond reports whether bond is at least minBond. A nil bond is stored as 0.
func holdsMinBond(bond cosmos.Uint, minBond cosmos.Uint) bool {
	if bond.IsNil() {
		bond = cosmos.ZeroUint()
	}
	return !bond.LT(minBond)
}

// GetNodeAccountSlashPoints - get the slash points associated with the given
// node address
func (k KVStore) GetNodeAccountSlashPoints(ctx cosmos.Context, addr cosmos.AccAddress) (int64, error) {
	record := int64(0)
	_, err := k.getInt64(ctx, k.GetKey(prefixNodeSlashPoints, addr.String()), &record)
	return record, err
}

// SetNodeAccountSlashPoints - set the slash points associated with the given
// node address and uint
func (k KVStore) SetNodeAccountSlashPoints(ctx cosmos.Context, addr cosmos.AccAddress, pts int64) {
	// make sure slash point doesn't go to negative
	if pts < 0 {
		pts = 0
	}
	k.setInt64(ctx, k.GetKey(prefixNodeSlashPoints, addr.String()), pts)
}

// ResetNodeAccountSlashPoints - reset the slash points to zero for associated
// with the given node address
func (k KVStore) ResetNodeAccountSlashPoints(ctx cosmos.Context, addr cosmos.AccAddress) {
	k.del(ctx, k.GetKey(prefixNodeSlashPoints, addr.String()))
}

// IncNodeAccountSlashPoints - increments the slash points associated with the
// given node address and uint
func (k KVStore) IncNodeAccountSlashPoints(ctx cosmos.Context, addr cosmos.AccAddress, pts int64) error {
	current, err := k.GetNodeAccountSlashPoints(ctx, addr)
	if err != nil {
		return err
	}
	k.SetNodeAccountSlashPoints(ctx, addr, current+pts)

	metricLabels, _ := ctx.Context().Value(constants.CtxMetricLabels).([]metrics.Label)
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "point_slash"},
		float32(pts),
		append(
			metricLabels,
			telemetry.NewLabel("address", addr.String()),
		),
	)

	if config.GetThornode().Telemetry.SlashPoints {
		slashTelemetry(ctx, pts, addr, "IncSlashPoints")
	}

	return nil
}

// DecNodeAccountSlashPoints - decrements the slash points associated with the
// given node address and uint
func (k KVStore) DecNodeAccountSlashPoints(ctx cosmos.Context, addr cosmos.AccAddress, pts int64) error {
	current, err := k.GetNodeAccountSlashPoints(ctx, addr)
	if err != nil {
		return err
	}
	k.SetNodeAccountSlashPoints(ctx, addr, current-pts)

	dec := pts
	if dec > current {
		dec = current
	}

	metricLabels, _ := ctx.Context().Value(constants.CtxMetricLabels).([]metrics.Label)
	telemetry.IncrCounterWithLabels(
		[]string{"thornode", "point_slash_refund"},
		float32(dec),
		append(
			metricLabels,
			telemetry.NewLabel("address", addr.String()),
		),
	)

	if config.GetThornode().Telemetry.SlashPoints {
		slashTelemetry(ctx, -pts, addr, "DecSlashPoints")
	}

	return nil
}

func (k KVStore) setJail(ctx cosmos.Context, key []byte, record Jail) {
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getJail(ctx cosmos.Context, key []byte, record *Jail) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return true, nil
}

// GetNodeAccountJail - gets jail details for a given node address
func (k KVStore) GetNodeAccountJail(ctx cosmos.Context, addr cosmos.AccAddress) (Jail, error) {
	record := NewJail(addr)
	_, err := k.getJail(ctx, k.GetKey(prefixNodeJail, addr.String()), &record)
	return record, err
}

// SetNodeAccountJail - update the jail details of a node account
func (k KVStore) SetNodeAccountJail(ctx cosmos.Context, addr cosmos.AccAddress, height int64, reason string) error {
	jail, err := k.GetNodeAccountJail(ctx, addr)
	if err != nil {
		return err
	}
	// never reduce sentence
	if jail.ReleaseHeight > height {
		return nil
	}
	jail.ReleaseHeight = height
	jail.Reason = reason

	k.setJail(ctx, k.GetKey(prefixNodeJail, addr.String()), jail)
	return nil
}

// ReleaseNodeAccountFromJail - update the jail details of a node account
func (k KVStore) ReleaseNodeAccountFromJail(ctx cosmos.Context, addr cosmos.AccAddress) error {
	jail, err := k.GetNodeAccountJail(ctx, addr)
	if err != nil {
		return err
	}
	jail.ReleaseHeight = ctx.BlockHeight()
	jail.Reason = ""
	k.setJail(ctx, k.GetKey(prefixNodeJail, addr.String()), jail)
	return nil
}

func (k KVStore) setBondProviders(ctx cosmos.Context, key []byte, record BondProviders) {
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getBondProviders(ctx cosmos.Context, key []byte, record *BondProviders) (bool, error) {
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return false, nil
	}

	bz := store.Get(key)
	if err := k.cdc.Unmarshal(bz, record); err != nil {
		return true, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", record, key), err)
	}
	return true, nil
}

// GetBondProviders - gets bond providers for a node account
func (k KVStore) GetBondProviders(ctx cosmos.Context, addr cosmos.AccAddress) (BondProviders, error) {
	record := NewBondProviders(addr)
	_, err := k.getBondProviders(ctx, k.GetKey(prefixBondProviders, addr.String()), &record)
	return record, err
}

// SetBondProviders - update the bond providers of a node account
func (k KVStore) SetBondProviders(ctx cosmos.Context, record BondProviders) error {
	k.setBondProviders(ctx, k.GetKey(prefixBondProviders, record.NodeAddress.String()), record)
	return nil
}

func (k KVStore) DeductNativeTxFeeFromBond(ctx cosmos.Context, nodeAddr cosmos.AccAddress) error {
	fee := k.GetNativeTxFee(ctx)
	if fee.IsZero() {
		return nil // no fee
	}

	// deduct fee from node account's bond
	na, err := k.GetNodeAccount(ctx, nodeAddr)
	if err != nil {
		return err
	}
	if na.Bond.LT(fee) {
		return fmt.Errorf("not enound bond for fee")
	}
	na.Bond = common.SafeSub(na.Bond, fee)
	if err = k.SetNodeAccount(ctx, na); err != nil {
		return fmt.Errorf("fail to save node account: %w", err)
	}

	// transfer fee from bond module to reserve
	coins := common.NewCoins(common.NewCoin(common.RuneAsset(), fee))
	if err = k.SendFromModuleToModule(ctx, BondName, ReserveName, coins); err != nil {
		return err
	}

	// emit bond cost event
	bondEvent := NewEventBond(fee, BondCost, common.Tx{}, &na, nil)
	events, err := bondEvent.Events()
	if err != nil {
		return fmt.Errorf("fail to get events: %w", err)
	}
	ctx.EventManager().EmitEvents(events)

	return nil
}
