package keeperv1

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
	thortypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type VaultSecurity struct {
	Vault      Vault
	TotalBond  cosmos.Uint
	TotalValue cosmos.Uint
	Diff       cosmos.Int
}

func (k KVStore) setVault(ctx cosmos.Context, key []byte, record Vault) {
	store := k.kvStore(ctx)
	buf := k.cdc.MustMarshal(&record)
	if buf == nil {
		store.Delete(key)
	} else {
		store.Set(key, buf)
	}
}

func (k KVStore) getVault(ctx cosmos.Context, key []byte, record *Vault) (bool, error) {
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

// GetVaultIterator only iterate vault pools
func (k KVStore) GetVaultIterator(ctx cosmos.Context) cosmos.Iterator {
	return k.getIterator(ctx, prefixVault)
}

// GetMostSecure with given list of vaults, find the vault that is most secure
func (k KVStore) GetMostSecure(ctx cosmos.Context, vaults Vaults, signingTransPeriod int64) Vault {
	vaults = k.SortBySecurity(ctx, vaults, signingTransPeriod)
	if len(vaults) == 0 {
		return Vault{}
	}
	return vaults[len(vaults)-1]
}

// GetMostSecureStrict given list of vaults, find the most secure.
// if the most secure vault's bond is less than securityBps * the vault's asset
// value in rune, it is considered insecure and no vault is returned
func (k KVStore) GetMostSecureStrict(ctx cosmos.Context, vaults Vaults, signingTransPeriod int64) Vault {
	vaultSecurity := k.getSortedVaultSecurity(ctx, vaults, signingTransPeriod)
	if len(vaults) == 0 {
		return Vault{}
	}

	mostSecure := vaultSecurity[len(vaults)-1]
	securityBps := k.GetConfigInt64(ctx, constants.MigrationVaultSecurityBps)

	if securityBps > 0 {
		bondNeeded := common.GetUncappedShare(
			cosmos.NewUint(uint64(securityBps)),
			cosmos.NewUint(10_000),
			mostSecure.TotalValue,
		)
		if mostSecure.TotalBond.LT(bondNeeded) {
			// vault does not meet security threshold
			// this is the most secure, so no vaults are secure
			return Vault{}
		}
	}

	return mostSecure.Vault
}

// GetLeastSecure with given list of vaults, find the vault that is least secure
func (k KVStore) GetLeastSecure(ctx cosmos.Context, vaults Vaults, signingTransPeriod int64) Vault {
	vaults = k.SortBySecurity(ctx, vaults, signingTransPeriod)
	if len(vaults) == 0 {
		return Vault{}
	}
	return vaults[0]
}

// SortBySecurity sorts a list of vaults in an order by how close the total
// value of the vault is to the total bond of the members of that vault. Sorts
// by least secure to most secure.
func (k KVStore) SortBySecurity(ctx cosmos.Context, vaults Vaults, signingTransPeriod int64) Vaults {
	if len(vaults) <= 1 {
		return vaults
	}

	vaultSecurity := k.getSortedVaultSecurity(ctx, vaults, signingTransPeriod)
	final := make(Vaults, len(vaultSecurity))
	for i, v := range vaultSecurity {
		final[i] = v.Vault
	}

	return final
}

// NodeAccount protobuf field number of the bond, read by peekNodeAccountStatusBond.
const nodeAccountBondField protowire.Number = 5

// nodeAccountWireTypes are the wire types NodeAccount.Unmarshal requires of
// its fields (1-17); it rejects any other wire type for them.
var nodeAccountWireTypes = [...]protowire.Type{
	1: protowire.BytesType, 2: protowire.VarintType, 3: protowire.BytesType,
	4: protowire.BytesType, 5: protowire.BytesType, 6: protowire.VarintType,
	7: protowire.BytesType, 8: protowire.VarintType, 9: protowire.BytesType,
	10: protowire.VarintType, 11: protowire.VarintType, 12: protowire.VarintType,
	13: protowire.BytesType, 14: protowire.BytesType, 15: protowire.VarintType,
	16: protowire.VarintType, 17: protowire.VarintType,
}

// nodeAccountPubKeySetField is the PubKeySet message field of a NodeAccount.
const nodeAccountPubKeySetField protowire.Number = 3

// peekNodeAccountStatusBond returns the status and bond that the codec's
// Unmarshal decodes from a NodeAccount's protobuf bytes, without decoding the
// other fields. Unmarshal resets the record first, so an absent status is
// Unknown and an absent bond is the zero Uint value (nil, not ZeroUint); the
// last occurrence of a field wins and the bond goes through the same
// Uint.Unmarshal. The other fields are only checked for what Unmarshal
// rejects: their framing, wire type, and the framing and wire types of the
// PubKeySet. ok is false if any of that fails or the status or bond do not
// decode, in which case the caller must fully unmarshal to get the error.
func peekNodeAccountStatusBond(bz []byte) (status NodeStatus, bond cosmos.Uint, ok bool) {
	for len(bz) > 0 {
		num, wtype, n := protowire.ConsumeTag(bz)
		if n < 0 {
			return 0, bond, false
		}
		bz = bz[n:]
		if int(num) < len(nodeAccountWireTypes) && wtype != nodeAccountWireTypes[num] {
			return 0, bond, false
		}
		switch num {
		case nodeAccountStatusField:
			v, m := protowire.ConsumeVarint(bz)
			if m < 0 {
				return 0, bond, false
			}
			bz = bz[m:]
			status = NodeStatus(int32(v))
		case nodeAccountBondField:
			v, m := protowire.ConsumeBytes(bz)
			if m < 0 {
				return 0, bond, false
			}
			bz = bz[m:]
			if err := bond.Unmarshal(v); err != nil {
				return 0, bond, false
			}
		case nodeAccountPubKeySetField:
			v, m := protowire.ConsumeBytes(bz)
			if m < 0 || !wellFormedPubKeySet(v) {
				return 0, bond, false
			}
			bz = bz[m:]
		default:
			m := protowire.ConsumeFieldValue(num, wtype, bz)
			if m < 0 {
				return 0, bond, false
			}
			bz = bz[m:]
		}
	}
	return status, bond, true
}

// wellFormedPubKeySet reports whether PubKeySet.Unmarshal accepts bz: its two
// fields are strings and it does not validate their contents.
func wellFormedPubKeySet(bz []byte) bool {
	for len(bz) > 0 {
		num, wtype, n := protowire.ConsumeTag(bz)
		if n < 0 {
			return false
		}
		bz = bz[n:]
		if (num == 1 || num == 2) && wtype != protowire.BytesType {
			return false
		}
		m := protowire.ConsumeFieldValue(num, wtype, bz)
		if m < 0 {
			return false
		}
		bz = bz[m:]
	}
	return true
}

// getNodeStatusBondByPubKey returns the Status and Bond of
// GetNodeAccountByPubKey(ctx, pk), with the same store access (Has, then Get
// of the node account key), without decoding the rest of the node account.
func (k KVStore) getNodeStatusBondByPubKey(ctx cosmos.Context, pk common.PubKey) (NodeStatus, cosmos.Uint, error) {
	addr, err := pk.GetThorAddress()
	if err != nil {
		return NodeUnknown, cosmos.ZeroUint(), err
	}
	key := k.GetKey(prefixNodeAccount, addr.String())
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return NodeUnknown, cosmos.ZeroUint(), nil
	}
	bz := store.Get(key)
	if status, bond, ok := peekNodeAccountStatusBond(bz); ok {
		return status, bond, nil
	}
	emptyPubKeySet := common.PubKeySet{
		Secp256k1: common.EmptyPubKey,
		Ed25519:   common.EmptyPubKey,
	}
	record := NewNodeAccount(addr, NodeUnknown, emptyPubKeySet, "", cosmos.ZeroUint(), "", ctx.BlockHeight())
	if err := k.cdc.Unmarshal(bz, &record); err != nil {
		return record.Status, record.Bond, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", &record, key), err)
	}
	return record.Status, record.Bond, nil
}

// poolMemo is a pool decoded by getPoolMemo, with the bytes it was decoded from.
type poolMemo struct {
	bz   []byte
	pool Pool
}

// getPoolMemo is GetPool with the same store access (Has, then Get of the
// pool key), returning the pool decoded earlier from memo when the stored
// bytes are the same rather than unmarshalling them again. Callers share the
// returned pool's Uints, so they must not modify them.
func (k KVStore) getPoolMemo(ctx cosmos.Context, asset common.Asset, memo map[string]poolMemo) (Pool, error) {
	key := k.GetKey(prefixPool, asset.String())
	store := k.kvStore(ctx)
	if !store.Has(key) {
		return NewPool(), nil
	}
	bz := store.Get(key)
	if m, ok := memo[string(key)]; ok && bytes.Equal(m.bz, bz) {
		return m.pool, nil
	}
	record := NewPool()
	if err := k.cdc.Unmarshal(bz, &record); err != nil {
		return record, dbError(ctx, fmt.Sprintf("Unmarshal kvstore: (%T) %s", &record, key), err)
	}
	memo[string(key)] = poolMemo{bz: bz, pool: record}
	return record, nil
}

func (k KVStore) getSortedVaultSecurity(ctx cosmos.Context, vaults Vaults, signingTransPeriod int64) []VaultSecurity {
	vaultSecurity := make([]VaultSecurity, len(vaults))

	// recent txout items count against every vault; read them once
	var txOuts []*TxOut
	if len(vaults) > 0 {
		h := ctx.BlockHeight() - signingTransPeriod
		if h < 1 {
			h = 1
		}
		for height := h; height <= ctx.BlockHeight(); height += 1 {
			txOut, err := k.GetTxOut(ctx, height)
			if err != nil {
				ctx.Logger().Error("unable to get txout", "error", err)
				continue
			}
			txOuts = append(txOuts, txOut)
		}
	}

	// the vaults hold, and the pending window pays out, the same few dozen
	// pools many times over: decode each stored pool once
	pools := make(map[string]poolMemo)

	for i, vault := range vaults {
		// get total bond
		totalBond := cosmos.ZeroUint()
		for _, pk := range vault.GetMembership() {
			status, bond, err := k.getNodeStatusBondByPubKey(ctx, pk)
			if err != nil {
				ctx.Logger().Error("failed to get node account by pubkey", "error", err)
				continue
			}
			if status == NodeActive {
				totalBond = totalBond.Add(bond)
			}
		}

		// get total value
		totalValue := cosmos.ZeroUint()
		for _, coin := range vault.Coins {
			if coin.IsRune() {
				continue
			} else {
				pool, err := k.getPoolMemo(ctx, coin.Asset, pools)
				if err != nil {
					ctx.Logger().Error("failed to get pool", "error", err)
					continue
				}
				totalValue = totalValue.Add(pool.AssetValueInRune(coin.Amount))
			}
		}

		// add recent unsent txout items to totalValue
		for _, txOut := range txOuts {
			var err error
			for _, item := range txOut.TxArray {
				if item.OutHash.IsEmpty() {
					var toAddress common.Address
					toAddress, err = vault.GetAddress(item.Coin.Asset.GetChain())
					if err != nil {
						ctx.Logger().Error("failed to get address of chain", "error", err)
						continue
					}
					if item.VaultPubKey.Equals(vault.PubKey) {
						if item.Coin.IsRune() {
							totalValue = common.SafeSub(totalValue, item.Coin.Amount)
						} else {
							var pool Pool
							pool, err = k.getPoolMemo(ctx, item.Coin.Asset, pools)
							if err != nil {
								ctx.Logger().Error("failed to get pool", "error", err)
								continue
							}
							totalValue = common.SafeSub(totalValue, pool.AssetValueInRune(item.Coin.Amount))
						}
					} else if item.ToAddress.Equals(toAddress) {
						if item.Coin.IsRune() {
							totalValue = totalValue.Add(item.Coin.Amount)
						} else {
							var pool Pool
							pool, err = k.getPoolMemo(ctx, item.Coin.Asset, pools)
							if err != nil {
								ctx.Logger().Error("failed to get pool", "error", err)
								continue
							}
							totalValue = totalValue.Add(pool.AssetValueInRune(item.Coin.Amount))
						}
					}
				}
			}
		}

		if totalValue.GT(totalBond) {
			diff := totalValue.Sub(totalBond).BigInt()
			vaultSecurity[i] = VaultSecurity{
				Vault:      vault,
				TotalBond:  totalBond,
				TotalValue: totalValue,
				Diff:       cosmos.NewIntFromBigInt(diff).MulRaw(-1),
			}
		} else {
			diff := totalBond.Sub(totalValue).BigInt()
			vaultSecurity[i] = VaultSecurity{
				Vault:      vault,
				TotalBond:  totalBond,
				TotalValue: totalValue,
				Diff:       cosmos.NewIntFromBigInt(diff),
			}
		}
	}

	// sort by how far total bond and total value are from each other
	sort.SliceStable(vaultSecurity, func(i, j int) bool {
		return vaultSecurity[i].Diff.LT(vaultSecurity[j].Diff)
	})

	return vaultSecurity
}

// GetPendingOutbounds selects txouts in the outbound and scheduled outbound queues (for deduction to leave only 'available' balances),
// as the amounts of both types of txout items are yet to be deducted from the vault balances.
func (k KVStore) GetPendingOutbounds(ctx cosmos.Context, asset common.Asset) []TxOutItem {
	signingPeriod := k.GetConfigInt64(ctx, constants.SigningTransactionPeriod)
	standardStartHeight := ctx.BlockHeight() - signingPeriod
	if standardStartHeight < 1 {
		standardStartHeight = 1
	}
	// Use the longest period the requested asset's underlying chain can contain.
	// GetChain reports THOR for synth, trade, and secured assets, so use the L1
	// chain to retain any EVM/XMR-backed liabilities conservatively.
	scanPeriod := thortypes.MaxTxOutSigningTransactionPeriodForChain(asset.GetLayer1Asset().Chain, signingPeriod)
	startHeight := ctx.BlockHeight() - scanPeriod
	if startHeight < 1 {
		startHeight = 1
	}
	txOutDelayMax := k.GetConfigInt64(ctx, constants.TxOutDelayMax)
	maxTxOutOffset := k.GetConfigInt64(ctx, constants.MaxTxOutOffset)
	var outbounds []TxOutItem
	seen := make(map[string]struct{})
	// The rolling window spans the longest timing class, while each item retains
	// only its own signing period. Durable stale migrate markers are added below.
	appendPendingOutbounds := func(blockOut *TxOut) {
		for i, txOutItem := range blockOut.TxArray {
			// only need to look at outbounds for the same asset
			if !txOutItem.Coin.Asset.Equals(asset) {
				continue
			}
			// only still outstanding txout will be considered
			if !txOutItem.OutHash.IsEmpty() {
				continue
			}
			if ctx.BlockHeight()-blockOut.Height > txOutItem.SigningTransactionPeriod(signingPeriod) {
				continue
			}
			itemKey := strconv.FormatInt(blockOut.Height, 10) + "/" + strconv.Itoa(i)
			seen[itemKey] = struct{}{}
			outbounds = append(outbounds, txOutItem)
		}
	}
	for height := startHeight; height <= ctx.BlockHeight()+txOutDelayMax; height++ {
		blockOut, err := k.GetTxOut(ctx, height)
		if err != nil {
			ctx.Logger().Error("fail to get block tx out", "error", err)
		}
		if height > ctx.BlockHeight()+maxTxOutOffset && len(blockOut.TxArray) == 0 {
			// we've hit our max offset, and an empty block, we can assume the
			// rest will be empty as well
			break
		}
		appendPendingOutbounds(blockOut)
	}

	if signingPeriod <= 0 {
		return outbounds
	}

	staleMigratePendingBlocks := signingPeriod * constants.StaleMigratePendingSigningPeriodMultiplier
	// Build a unique set of old pending migrate heights before loading txouts.
	// The same height can appear multiple times for multi-asset migration, but
	// processing a txout block more than once would double-deduct availability.
	stalePendingVaultsByHeight := make(map[int64]map[common.PubKey]struct{})
	stalePendingHeights := make([]int64, 0)
	retiring, err := k.GetAsgardVaultsByStatus(ctx, RetiringVault)
	if err != nil {
		ctx.Logger().Error("fail to get retiring vaults for pending outbounds", "error", err)
		return outbounds
	}
	for _, vault := range retiring {
		for _, height := range vault.PendingTxBlockHeights {
			if height < 1 || height >= standardStartHeight {
				continue
			}
			// Pending migrate heights are durable references beyond the rolling
			// signing window, but expired ones are retry-eligible in migrateFunds
			// and must not reserve funds forever.
			if ctx.BlockHeight()-height > staleMigratePendingBlocks {
				continue
			}
			eligibleVaults, ok := stalePendingVaultsByHeight[height]
			if !ok {
				eligibleVaults = make(map[common.PubKey]struct{})
				stalePendingVaultsByHeight[height] = eligibleVaults
				stalePendingHeights = append(stalePendingHeights, height)
			}
			eligibleVaults[vault.PubKey] = struct{}{}
		}
	}
	// Sort for deterministic accounting. This helper feeds vault-selection logic,
	// so consensus-sensitive callers must not depend on map iteration order.
	sort.Slice(stalePendingHeights, func(i, j int) bool {
		return stalePendingHeights[i] < stalePendingHeights[j]
	})
	for _, height := range stalePendingHeights {
		blockOut, err := k.GetTxOut(ctx, height)
		if err != nil {
			ctx.Logger().Error("fail to get stale pending migrate tx out", "error", err, "height", height)
			continue
		}
		eligibleVaults := stalePendingVaultsByHeight[height]
		for i, txOutItem := range blockOut.TxArray {
			if !txOutItem.Coin.Asset.Equals(asset) || !txOutItem.OutHash.IsEmpty() {
				continue
			}
			if _, ok := eligibleVaults[txOutItem.VaultPubKey]; !ok {
				continue
			}
			// Only count the exact migrate item referenced by the durable marker.
			// Other old txouts at the same height should not reserve this vault's funds.
			if !isMigrateMemoForHeight(txOutItem.Memo, height) {
				continue
			}
			itemKey := strconv.FormatInt(blockOut.Height, 10) + "/" + strconv.Itoa(i)
			if _, ok := seen[itemKey]; ok {
				continue
			}
			seen[itemKey] = struct{}{}
			outbounds = append(outbounds, txOutItem)
		}
	}
	return outbounds
}

func isMigrateMemoForHeight(memo string, height int64) bool {
	base, _, _ := strings.Cut(memo, "|")
	parts := strings.SplitN(base, ":", 3)
	if len(parts) < 2 || !strings.EqualFold(parts[0], "migrate") {
		return false
	}
	memoHeight, err := strconv.ParseInt(parts[1], 10, 64)
	return err == nil && memoHeight == height
}

// SetVault save the Vault object to store
func (k KVStore) SetVault(ctx cosmos.Context, vault Vault) error {
	if vault.IsAsgard() {
		if err := k.addAsgardIndex(ctx, vault.PubKey); err != nil {
			return err
		}
		if !vault.PubKeyEddsa.IsEmpty() {
			if err := k.addAsgardEDDSAIndex(ctx, vault.PubKeyEddsa, vault.PubKey); err != nil {
				return err
			}
		}
		if !vault.PubKeyFrost.IsEmpty() {
			if err := k.addAsgardFrostIndex(ctx, vault.PubKeyFrost, vault.PubKey); err != nil {
				return err
			}
		}
	}

	k.setVault(ctx, k.GetKey(prefixVault, vault.PubKey.String()), vault)
	return nil
}

// VaultExists check whether the given pubkey is associated with a vault
func (k KVStore) VaultExists(ctx cosmos.Context, pk common.PubKey) bool {
	if k.has(ctx, k.GetKey(prefixVault, pk.String())) {
		return true
	}
	eddsaPubKey, err := k.getAsgardEDDSAIndex(ctx, pk)
	if err != nil {
		ctx.Logger().Error("fail to getAsgardEDDSAIndex", err)
		return false
	}
	if !eddsaPubKey.IsEmpty() {
		return true
	}
	frostPubKey, err := k.getAsgardFrostIndex(ctx, pk)
	if err != nil {
		ctx.Logger().Error("fail to getAsgardFrostIndex", err)
		return false
	}
	return !frostPubKey.IsEmpty()
}

// GetVault get Vault with the given pubkey from data store
func (k KVStore) GetVault(ctx cosmos.Context, pk common.PubKey) (Vault, error) {
	record := Vault{
		BlockHeight: ctx.BlockHeight(),
		PubKey:      pk,
	}
	ok, err := k.getVault(ctx, k.GetKey(prefixVault, pk.String()), &record)
	if !ok {
		ecdsaPubKey, eddsaErr := k.getAsgardEDDSAIndex(ctx, pk)
		if eddsaErr != nil {
			return record, fmt.Errorf("unable to getAsgardEDDSAIndex for %s: %w", pk, kvTypes.ErrVaultNotFound)
		}
		if !ecdsaPubKey.IsEmpty() {
			return k.GetVault(ctx, ecdsaPubKey)
		}
		frostPubKey, frostErr := k.getAsgardFrostIndex(ctx, pk)
		if frostErr != nil {
			return record, fmt.Errorf("unable to getAsgardFrostIndex for %s: %w", pk, kvTypes.ErrVaultNotFound)
		}
		if !frostPubKey.IsEmpty() {
			return k.GetVault(ctx, frostPubKey)
		}
		return record, fmt.Errorf("vault with pubkey(%s) doesn't exist: %w", pk, kvTypes.ErrVaultNotFound)
	}
	if record.PubKey.IsEmpty() {
		record.PubKey = pk
	}
	// Maintains pre-sdk v0.50 behavior where the current block height is set if the vault's block height is 0
	if record.BlockHeight == 0 {
		record.BlockHeight = ctx.BlockHeight()
	}
	return record, err
}

// HasValidVaultPools check the data store to see whether we have a valid vault
func (k KVStore) HasValidVaultPools(ctx cosmos.Context) (bool, error) {
	iterator := k.GetVaultIterator(ctx)
	defer iterator.Close()
	for ; iterator.Valid(); iterator.Next() {
		var vault Vault
		if err := k.cdc.Unmarshal(iterator.Value(), &vault); err != nil {
			return false, dbError(ctx, "fail to unmarshal vault", err)
		}
		if vault.HasFundsOrDeficit() {
			return true, nil
		}
	}
	return false, nil
}

func (k KVStore) getAsgardIndex(ctx cosmos.Context) (common.PubKeys, error) {
	record := make([]string, 0)
	_, err := k.getStrings(ctx, k.GetKey(prefixVaultAsgardIndex, ""), &record)
	if err != nil {
		return nil, err
	}
	pks := make(common.PubKeys, len(record))
	for i, s := range record {
		pks[i], err = common.NewPubKey(s)
		if err != nil {
			return nil, err
		}
	}
	return pks, nil
}

func (k KVStore) addAsgardIndex(ctx cosmos.Context, pubkey common.PubKey) error {
	pks, err := k.getAsgardIndex(ctx)
	if err != nil {
		return err
	}
	for _, pk := range pks {
		if pk.Equals(pubkey) {
			return nil
		}
	}
	pks = append(pks, pubkey)
	k.setStrings(ctx, k.GetKey(prefixVaultAsgardIndex, ""), pks.Strings())
	return nil
}

func (k KVStore) addAsgardEDDSAIndex(ctx cosmos.Context, pubKeyEDDSA, pubkeyECDSA common.PubKey) error {
	k.setStrings(ctx, k.GetKey(prefixVaultAsgardEDDSAIndex, pubKeyEDDSA.String()), []string{pubkeyECDSA.String()})
	return nil
}

func (k KVStore) getAsgardEDDSAIndex(ctx cosmos.Context, pubKeyEDDSA common.PubKey) (common.PubKey, error) {
	record := make([]string, 0)
	exists, err := k.getStrings(ctx, k.GetKey(prefixVaultAsgardEDDSAIndex, pubKeyEDDSA.String()), &record)
	if err != nil || !exists {
		return common.EmptyPubKey, err
	}
	return common.PubKey(record[0]), nil
}

func (k KVStore) addAsgardFrostIndex(ctx cosmos.Context, pubKeyFrost, pubkeyECDSA common.PubKey) error {
	k.setStrings(ctx, k.GetKey(prefixVaultAsgardFrostIndex, pubKeyFrost.String()), []string{pubkeyECDSA.String()})
	return nil
}

func (k KVStore) getAsgardFrostIndex(ctx cosmos.Context, pubKeyFrost common.PubKey) (common.PubKey, error) {
	record := make([]string, 0)
	exists, err := k.getStrings(ctx, k.GetKey(prefixVaultAsgardFrostIndex, pubKeyFrost.String()), &record)
	if err != nil || !exists {
		return common.EmptyPubKey, err
	}
	return common.PubKey(record[0]), nil
}

func (k KVStore) RemoveFromAsgardIndex(ctx cosmos.Context, pubkey common.PubKey) error {
	pks, err := k.getAsgardIndex(ctx)
	if err != nil {
		return err
	}

	newPks := common.PubKeys{}
	for _, pk := range pks {
		if !pk.Equals(pubkey) {
			newPks = append(newPks, pk)
		}
	}

	k.setStrings(ctx, k.GetKey(prefixVaultAsgardIndex, ""), newPks.Strings())
	return nil
}

// GetAsgardVaults return all asgard vaults
func (k KVStore) GetAsgardVaults(ctx cosmos.Context) (Vaults, error) {
	pks, err := k.getAsgardIndex(ctx)
	if err != nil {
		return nil, err
	}

	var asgards Vaults
	for _, pk := range pks {
		var vault Vault
		vault, err = k.GetVault(ctx, pk)
		if err != nil {
			return nil, err
		}
		if vault.IsAsgard() {
			asgards = append(asgards, vault)
		}
	}

	return asgards, nil
}

// GetAsgardVaultsByStatus get all the asgard vault that have the given status
func (k KVStore) GetAsgardVaultsByStatus(ctx cosmos.Context, status VaultStatus) (Vaults, error) {
	all, err := k.GetAsgardVaults(ctx)
	if err != nil {
		return nil, err
	}

	var asgards Vaults
	for _, vault := range all {
		if vault.Status == status {
			asgards = append(asgards, vault)
		}
	}

	return asgards, nil
}

// DeleteVault remove the given vault from data store
func (k KVStore) DeleteVault(ctx cosmos.Context, pubkey common.PubKey) error {
	vault, err := k.GetVault(ctx, pubkey)
	if err != nil {
		if errors.Is(err, kvTypes.ErrVaultNotFound) {
			return nil
		}
		return err
	}

	if vault.HasFundsOrDeficit() {
		return errors.New("unable to delete vault: it still contains funds or deficit")
	}

	if vault.IsAsgard() {
		if err = k.RemoveFromAsgardIndex(ctx, pubkey); err != nil {
			ctx.Logger().Error("fail to remove vault from asgard index", "error", err)
		}
	}
	// delete the actual vault
	k.del(ctx, k.GetKey(prefixVault, vault.PubKey.String()))
	return nil
}
