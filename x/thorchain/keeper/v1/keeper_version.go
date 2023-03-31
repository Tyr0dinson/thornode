package keeperv1

import (
	"github.com/blang/semver"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

// GetVersionWithCtx returns the version with the given context,
// and returns true if the version was found in the store
func (k KVStore) GetVersionWithCtx(ctx cosmos.Context) (semver.Version, bool) {
	// InfiniteGasMeter allows calls without affecting gas and consensus
	ctx = ctx.WithGasMeter(sdk.NewInfiniteGasMeter())
	key := k.GetKey(ctx, prefixVersion, "")
	store := ctx.KVStore(k.storeKey)
	val := store.Get([]byte(key))
	if val == nil {
		return semver.Version{}, false
	}
	return semver.MustParse(string(val)), true
}

// SetVersionWithCtx stores the version
func (k KVStore) SetVersionWithCtx(ctx cosmos.Context, v semver.Version) {
	key := k.GetKey(ctx, prefixVersion, "")
	store := ctx.KVStore(k.storeKey)
	store.Set([]byte(key), []byte(v.String()))
}

// getMinJoinLast returns the last stored MinJoinVersion
func (k KVStore) getMinJoinLast(ctx cosmos.Context) semver.Version {
	key := k.GetKey(ctx, prefixMinJoinLast, "")
	store := ctx.KVStore(k.storeKey)
	versionByte := store.Get([]byte(key))
	if versionByte == nil {
		return semver.Version{}
	}
	return semver.MustParse(string(versionByte))
}

// setMinJoinLast stores the MinJoinVersion
func (k KVStore) setMinJoinLast(ctx cosmos.Context, version semver.Version) {
	key := k.GetKey(ctx, prefixMinJoinLast, "")
	store := ctx.KVStore(k.storeKey)
	store.Set([]byte(key), []byte(version.String()))
}

// getMinJoinLastHeight gets the height when the MinJoinLast version was last changed
func (k KVStore) getMinJoinLastHeight(ctx cosmos.Context) int64 {
	key := k.GetKey(ctx, prefixMinJoinLastHeight, "")
	store := ctx.KVStore(k.storeKey)
	if !store.Has([]byte(key)) {
		// Before heights with a stored MinJoinLast, return 0.
		return 0
	}
	var protoLastHeight ProtoInt64
	buf := store.Get([]byte(key))
	k.cdc.MustUnmarshal(buf, &protoLastHeight)
	return protoLastHeight.Value
}

// setMinJoinLastHeight stores the height when the MinJoinLast version was last changed
func (k KVStore) setMinJoinLastHeight(ctx cosmos.Context, height int64) {
	key := k.GetKey(ctx, prefixMinJoinLastHeight, "")
	store := ctx.KVStore(k.storeKey)
	protoHeight := ProtoInt64{Value: height}
	store.Set([]byte(key), k.cdc.MustMarshal(&protoHeight))
}

// GetMinJoinLast returns the last stored MinJoinVersion and its last stored height,
// or else (at worse performance) the current MinJoinVersion and 0.
func (k KVStore) GetMinJoinLast(ctx cosmos.Context) (semver.Version, int64) {
	version := k.getMinJoinLast(ctx)
	lastHeight := k.getMinJoinLastHeight(ctx)

	// MinJoinLast is intended to be kept updated to always equal the MinJoinVersion.
	// If either the stored version or height indicates that unstored/unset, fall back to GetMinJoinVersion.
	if version.Equals(semver.Version{}) || lastHeight <= 0 {
		return k.GetMinJoinVersion(ctx), 0
	}

	return version, lastHeight
}

// SetMinJoinLast updates-if-changed the MinJoinVersion and its height when changed.
func (k KVStore) SetMinJoinLast(ctx cosmos.Context) {
	minJoinVersion := k.GetMinJoinVersion(ctx)
	minJoinLast, lastHeight := k.GetMinJoinLast(ctx)

	// GetMinJoinLast will fall back to GetMinJoinVersion if unset (returning a height of 0),
	// so check both the version and the last changed height.
	if !minJoinVersion.Equals(minJoinLast) || lastHeight <= 0 {
		// Since different (or unset), update it.
		// First updating that the stored version has been modified, then the stored version itself.
		if lastHeight != ctx.BlockHeight() {
			k.setMinJoinLastHeight(ctx, ctx.BlockHeight())
		}
		k.setMinJoinLast(ctx, minJoinVersion)
	}
}
