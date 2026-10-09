package standard

import (
	"context"
	"errors"
	"testing"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/app/upgrades"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// keeperCov is a minimal thorchain keeper recording the calls made by the
// upgrade handler.
type keeperCov struct {
	keeper.KVStoreDummy
	active     types.NodeAccounts
	listErr    error
	setErr     error
	saved      types.NodeAccounts
	minJoinSet bool
}

func (k *keeperCov) ListActiveValidators(_ cosmos.Context) (types.NodeAccounts, error) {
	return k.active, k.listErr
}

func (k *keeperCov) SetNodeAccount(_ cosmos.Context, na types.NodeAccount) error {
	if k.setErr != nil {
		return k.setErr
	}
	k.saved = append(k.saved, na)
	return nil
}

func (k *keeperCov) SetMinJoinLast(_ cosmos.Context) { k.minJoinSet = true }

// moduleManagerCov records RunMigrations calls.
type moduleManagerCov struct {
	called bool
	fromVM module.VersionMap
	err    error
}

func (m *moduleManagerCov) RunMigrations(_ context.Context, _ module.Configurator, fromVM module.VersionMap) (module.VersionMap, error) {
	m.called = true
	m.fromVM = fromVM
	if m.err != nil {
		return nil, m.err
	}
	out := module.VersionMap{}
	for k, v := range fromVM {
		out[k] = v + 1
	}
	return out, nil
}

func (m *moduleManagerCov) GetVersionMap() module.VersionMap { return module.VersionMap{} }

func newCtxCov() sdk.Context {
	return sdk.Context{}.WithContext(context.Background()).WithEventManager(sdk.NewEventManager())
}

func TestNewUpgradeCov(t *testing.T) {
	u := NewUpgrade("9.9.9")
	require.Equal(t, "9.9.9", u.UpgradeName)
	require.NotNil(t, u.CreateUpgradeHandler)
	require.Empty(t, u.StoreUpgrades.Added)
	require.Empty(t, u.StoreUpgrades.Deleted)
	require.NotNil(t, u.StoreUpgrades.Added)
	require.NotNil(t, u.StoreUpgrades.Deleted)
}

func TestCreateUpgradeHandlerCov(t *testing.T) {
	na1 := types.GetRandomValidatorNode(types.NodeStatus_Active)
	na2 := types.GetRandomValidatorNode(types.NodeStatus_Active)
	k := &keeperCov{active: types.NodeAccounts{na1, na2}}
	mm := &moduleManagerCov{}

	h := NewUpgrade("3.99.0").CreateUpgradeHandler(mm, nil, &upgrades.AppKeepers{ThorchainKeeper: k})
	ctx := newCtxCov()
	vm, err := h(ctx, upgradetypes.Plan{Name: "3.99.0"}, module.VersionMap{"thorchain": 1})
	require.NoError(t, err)
	require.Equal(t, module.VersionMap{"thorchain": 2}, vm)
	require.True(t, mm.called)
	require.Equal(t, module.VersionMap{"thorchain": 1}, mm.fromVM)

	// every active validator was saved with the plan version
	require.Len(t, k.saved, 2)
	for _, na := range k.saved {
		require.Equal(t, "3.99.0", na.Version)
	}
	require.True(t, k.minJoinSet)

	var setVersion int
	for _, ev := range ctx.EventManager().Events() {
		if ev.Type == "set_version" {
			setVersion++
		}
	}
	require.Equal(t, 2, setVersion)
}

func TestCreateUpgradeHandlerErrorsCov(t *testing.T) {
	// listing validators fails: migrations do not run
	k := &keeperCov{listErr: errors.New("boom")}
	mm := &moduleManagerCov{}
	h := CreateUpgradeHandler(mm, nil, &upgrades.AppKeepers{ThorchainKeeper: k})
	_, err := h(newCtxCov(), upgradetypes.Plan{Name: "x"}, module.VersionMap{})
	require.ErrorContains(t, err, "failed to update active validator versions")
	require.False(t, mm.called)

	// saving a node account fails
	k = &keeperCov{active: types.NodeAccounts{types.GetRandomValidatorNode(types.NodeStatus_Active)}, setErr: errors.New("nope")}
	h = CreateUpgradeHandler(mm, nil, &upgrades.AppKeepers{ThorchainKeeper: k})
	_, err = h(newCtxCov(), upgradetypes.Plan{Name: "x"}, module.VersionMap{})
	require.ErrorContains(t, err, "failed to update active validator versions")
	require.False(t, mm.called)

	// migration errors propagate
	k = &keeperCov{}
	mm = &moduleManagerCov{err: errors.New("migrate")}
	h = CreateUpgradeHandler(mm, nil, &upgrades.AppKeepers{ThorchainKeeper: k})
	_, err = h(newCtxCov(), upgradetypes.Plan{Name: "x"}, module.VersionMap{})
	require.ErrorContains(t, err, "migrate")
	require.True(t, mm.called)
}
