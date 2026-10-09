package keeper

import (
	"errors"
	"reflect"
	"testing"

	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/testutil"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	kvTypes "gitlab.com/thorchain/thornode/v3/x/thorchain/keeper/types"
)

func newDummyCtxCov() cosmos.Context {
	key := storetypes.NewKVStoreKey("dummycov")
	tkey := storetypes.NewTransientStoreKey("dummycov_t")
	return testutil.DefaultContext(key, tkey)
}

var (
	ctxTypeCov   = reflect.TypeOf(cosmos.Context{})
	errorTypeCov = reflect.TypeOf((*error)(nil)).Elem()
)

// callDummyMethodCov invokes method m with zero-valued arguments (and a real
// context for every cosmos.Context parameter). It returns the results and any
// value the method panicked with.
func callDummyMethodCov(ctx cosmos.Context, m reflect.Value) (out []reflect.Value, panicked any) {
	defer func() {
		panicked = recover()
	}()
	mt := m.Type()
	args := make([]reflect.Value, mt.NumIn())
	for i := 0; i < mt.NumIn(); i++ {
		in := mt.In(i)
		if in == ctxTypeCov {
			args[i] = reflect.ValueOf(ctx)
			continue
		}
		args[i] = reflect.Zero(in)
	}
	if mt.IsVariadic() {
		return m.CallSlice(args), nil
	}
	return m.Call(args), nil
}

// TestKVStoreDummyContractCov checks the documented contract of the dummy
// keeper: every method either returns a nil/kaboom error, or panics with
// kaboom. It never fails with any other error or panic.
func TestKVStoreDummyContractCov(t *testing.T) {
	ctx := newDummyCtxCov()
	k := KVStoreDummy{}
	v := reflect.ValueOf(k)
	vt := v.Type()

	var kaboomErrs, panics int
	for i := 0; i < v.NumMethod(); i++ {
		name := vt.Method(i).Name
		out, p := callDummyMethodCov(ctx, v.Method(i))
		if p != nil {
			err, ok := p.(error)
			require.Truef(t, ok, "%s panicked with non-error %v", name, p)
			require.Truef(t, errors.Is(err, kaboom), "%s panicked with %v", name, err)
			panics++
			continue
		}
		for _, o := range out {
			if o.Type() != errorTypeCov {
				continue
			}
			if o.IsNil() {
				continue
			}
			err, ok := o.Interface().(error)
			require.True(t, ok)
			require.Truef(t, errors.Is(err, kaboom), "%s returned unexpected error %v", name, err)
			kaboomErrs++
		}
	}
	// sanity: the dummy really does fail most write paths and panics on
	// upgrade votes.
	require.Greater(t, kaboomErrs, 100)
	require.Equal(t, 2, panics)
}

// The keeper interface must be satisfied by the dummy so it can be embedded
// in handler test doubles.
func TestKVStoreDummyImplementsKeeperCov(t *testing.T) {
	var k Keeper = KVStoreDummy{}
	require.NotNil(t, k)
}

func TestKVStoreDummySpecificValuesCov(t *testing.T) {
	ctx := newDummyCtxCov()
	k := KVStoreDummy{}

	require.NotNil(t, k.Cdc())
	require.Equal(t, "9999999.0.0", k.GetVersion().String())
	v, ok := k.GetVersionWithCtx(ctx)
	require.True(t, ok)
	require.Equal(t, "9999999.0.0", v.String())
	_, last := k.GetMinJoinLast(ctx)
	require.Equal(t, int64(0), last)
	require.NotNil(t, k.Logger(ctx))

	// GetKey concatenates the extra parts.
	require.Equal(t, "foo/1/bar", string(k.GetKey(kvTypes.DbPrefix("foo"), "bar")))
	require.Equal(t, "foo/1/barbazqux", string(k.GetKey(kvTypes.DbPrefix("foo"), "bar", "baz", "qux")))

	// only the reserve module has a known address.
	addr, err := k.GetModuleAddress(ReserveName)
	require.NoError(t, err)
	require.Equal(t, common.Address("tthor1dheycdevq39qlkxs2a6wuuzyn4aqxhve3hhmlw"), addr)
	addr, err = k.GetModuleAddress(AsgardName)
	require.ErrorIs(t, err, kaboom)
	require.True(t, addr.IsEmpty())

	require.True(t, k.GetRuneBalanceOfModule(ctx, AsgardName).IsZero())
	require.True(t, k.GetBalanceOfModule(ctx, AsgardName, "rune").IsZero())
	require.ErrorIs(t, k.MintToModule(ctx, ModuleName, common.NewCoin(common.RuneNative, cosmos.NewUint(1))), kaboom)
	amt, err := k.BurnEntireSynthSupply(ctx, common.BTCAsset)
	require.ErrorIs(t, err, kaboom)
	require.True(t, amt.IsZero())

	plan, err := k.GetUpgradePlan(ctx)
	require.NoError(t, err)
	require.Empty(t, plan.Name)
	require.NoError(t, k.ClearUpgradePlan(ctx))
	require.NoError(t, k.RemoveExpiredUpgradeProposals(ctx))

	require.PanicsWithValue(t, kaboom, func() { k.ApproveUpgrade(ctx, nil, "x") })
	require.PanicsWithValue(t, kaboom, func() { k.RejectUpgrade(ctx, nil, "x") })
}

func TestDummyIteratorCov(t *testing.T) {
	iter := NewDummyIterator()
	require.False(t, iter.Valid())

	iter.AddItem([]byte("k1"), []byte("v1"))
	iter.AddItem([]byte("k2"), []byte("v2"))

	var keys, values []string
	for ; iter.Valid(); iter.Next() {
		keys = append(keys, string(iter.Key()))
		values = append(values, string(iter.Value()))
	}
	require.Equal(t, []string{"k1", "k2"}, keys)
	require.Equal(t, []string{"v1", "v2"}, values)
	require.NoError(t, iter.Error())

	start, end := iter.Domain()
	require.Nil(t, start)
	require.Nil(t, end)

	// Close rewinds the iterator.
	require.NoError(t, iter.Close())
	require.True(t, iter.Valid())
	require.Equal(t, "k1", string(iter.Key()))

	iter.err = kaboom
	require.ErrorIs(t, iter.Error(), kaboom)
}
