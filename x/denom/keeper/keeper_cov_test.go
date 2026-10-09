package keeper_test

import (
	"context"
	"errors"
	"testing"

	"cosmossdk.io/log"
	"cosmossdk.io/math"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	moduletestutil "github.com/cosmos/cosmos-sdk/types/module/testutil"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/x/denom/keeper"
	"gitlab.com/thorchain/thornode/v3/x/denom/types"
)

var errBankCov = errors.New("bank failure")

type fakeAccountKeeperCov struct {
	set []sdk.ModuleAccountI
}

func (f *fakeAccountKeeperCov) SetModuleAccount(_ context.Context, macc sdk.ModuleAccountI) {
	f.set = append(f.set, macc)
}

// fakeBankKeeperCov fails mint/burn operations on demand.
type fakeBankKeeperCov struct {
	mintErr, burnErr, toAccErr, toModErr error
	minted, burned                       sdk.Coins
}

func (f *fakeBankKeeperCov) GetDenomMetaData(context.Context, string) (banktypes.Metadata, bool) {
	return banktypes.Metadata{}, false
}
func (f *fakeBankKeeperCov) SetDenomMetaData(context.Context, banktypes.Metadata) {}
func (f *fakeBankKeeperCov) SendCoinsFromModuleToAccount(context.Context, string, sdk.AccAddress, sdk.Coins) error {
	return f.toAccErr
}

func (f *fakeBankKeeperCov) SendCoinsFromAccountToModule(context.Context, sdk.AccAddress, string, sdk.Coins) error {
	return f.toModErr
}

func (f *fakeBankKeeperCov) MintCoins(_ context.Context, _ string, amt sdk.Coins) error {
	if f.mintErr != nil {
		return f.mintErr
	}
	f.minted = f.minted.Add(amt...)
	return nil
}

func (f *fakeBankKeeperCov) BurnCoins(_ context.Context, _ string, amt sdk.Coins) error {
	if f.burnErr != nil {
		return f.burnErr
	}
	f.burned = f.burned.Add(amt...)
	return nil
}

func newFakeKeeperCov(t *testing.T, bk *fakeBankKeeperCov, ak *fakeAccountKeeperCov) (sdk.Context, keeper.Keeper) {
	t.Helper()
	key := storetypes.NewKVStoreKey(types.StoreKey)
	ctx := testutil.DefaultContextWithKeys(
		map[string]*storetypes.KVStoreKey{types.StoreKey: key},
		storetypes.NewTransientStoreKeys(),
		storetypes.NewMemoryStoreKeys(),
	)
	ctx = ctx.WithLogger(log.NewNopLogger())
	encCfg := moduletestutil.MakeTestEncodingConfig()
	k := keeper.NewKeeper(encCfg.Codec, runtime.NewKVStoreService(key), ak, bk, nil, authAcc.String())
	return ctx, k
}

func TestKeeperLoggerAndModuleAccountCov(t *testing.T) {
	ak := &fakeAccountKeeperCov{}
	ctx, k := newFakeKeeperCov(t, &fakeBankKeeperCov{}, ak)
	require.NotNil(t, k.Logger(ctx))

	k.CreateModuleAccount(ctx)
	require.Len(t, ak.set, 1)
	macc := ak.set[0]
	require.Equal(t, types.ModuleName, macc.GetName())
	require.True(t, macc.HasPermission(authtypes.Minter))
	require.True(t, macc.HasPermission(authtypes.Burner))
	require.False(t, macc.HasPermission(authtypes.Staking))
}

func TestKeeperMintToCov(t *testing.T) {
	bk := &fakeBankKeeperCov{}
	ctx, k := newFakeKeeperCov(t, bk, &fakeAccountKeeperCov{})
	good := sdk.NewCoin("x/btc", math.NewInt(10))

	// non x/denom denoms are refused before minting
	err := k.MintToCov(ctx, sdk.NewCoin("rune", math.NewInt(10)), accAddrs[0].String())
	require.ErrorIs(t, err, types.ErrInvalidDenom)
	require.True(t, bk.minted.IsZero())

	// mint failure propagates
	bk.mintErr = errBankCov
	require.ErrorIs(t, k.MintToCov(ctx, good, accAddrs[0].String()), errBankCov)
	bk.mintErr = nil

	// invalid recipient: note the coins are already minted at this point
	err = k.MintToCov(ctx, good, "not-an-address")
	require.Error(t, err)
	require.Equal(t, "10x/btc", bk.minted.String())

	// send failure propagates
	bk.toAccErr = errBankCov
	require.ErrorIs(t, k.MintToCov(ctx, good, accAddrs[0].String()), errBankCov)
	bk.toAccErr = nil

	require.NoError(t, k.MintToCov(ctx, good, accAddrs[0].String()))
	require.Equal(t, "30x/btc", bk.minted.String())
}

func TestKeeperBurnFromCov(t *testing.T) {
	bk := &fakeBankKeeperCov{}
	ctx, k := newFakeKeeperCov(t, bk, &fakeAccountKeeperCov{})
	good := sdk.NewCoin("x/btc", math.NewInt(4))

	require.ErrorIs(t, k.BurnFromCov(ctx, sdk.NewCoin("rune", math.NewInt(4)), accAddrs[0].String()), types.ErrInvalidDenom)
	require.Error(t, k.BurnFromCov(ctx, good, "not-an-address"))

	bk.toModErr = errBankCov
	require.ErrorIs(t, k.BurnFromCov(ctx, good, accAddrs[0].String()), errBankCov)
	bk.toModErr = nil

	bk.burnErr = errBankCov
	require.ErrorIs(t, k.BurnFromCov(ctx, good, accAddrs[0].String()), errBankCov)
	bk.burnErr = nil
	require.True(t, bk.burned.IsZero())

	require.NoError(t, k.BurnFromCov(ctx, good, accAddrs[0].String()))
	require.Equal(t, "4x/btc", bk.burned.String())
}

func TestKeeperCreateDenomCov(t *testing.T) {
	ctx, k := newFakeKeeperCov(t, &fakeBankKeeperCov{}, &fakeAccountKeeperCov{})

	// invalid id makes an invalid denom
	_, err := k.CreateDenom(ctx, "!", accAddrs[0].String())
	require.Error(t, err)

	// invalid admin address
	_, err = k.CreateDenom(ctx, "btc", "bogus")
	require.Error(t, err)
	admin, err := k.GetAdmin(ctx, "x/btc")
	require.NoError(t, err)
	require.Nil(t, admin)

	denom, err := k.CreateDenom(ctx, "btc", accAddrs[0].String())
	require.NoError(t, err)
	require.Equal(t, "x/btc", denom)
	admin, err = k.GetAdmin(ctx, denom)
	require.NoError(t, err)
	require.Equal(t, accAddrs[0], admin)
}

func TestMsgServerNoContractKeeperCov(t *testing.T) {
	ctx, k := newFakeKeeperCov(t, &fakeBankKeeperCov{}, &fakeAccountKeeperCov{})
	srv := keeper.NewMsgServerImpl(k)

	// without a contract keeper every sender is unauthorized
	_, err := srv.CreateDenom(ctx, types.NewMsgCreateDenom(accAddrs[0].String(), "btc"))
	require.ErrorIs(t, err, types.ErrUnauthorized)

	// invalid sender address is rejected before the contract check
	_, err = srv.CreateDenom(ctx, types.NewMsgCreateDenom("bogus", "btc"))
	require.Error(t, err)
	require.NotErrorIs(t, err, types.ErrUnauthorized)
}

func (s *KeeperTestSuite) TestMsgServerErrorPathsCov() {
	s.SetupTest()
	sdkCtx := sdk.UnwrapSDKContext(s.ctx)

	res, err := s.msgServer.CreateDenom(s.ctx, types.NewMsgCreateDenom(accAddrs[0].String(), "cov"))
	s.Require().NoError(err)
	denom := res.GetNewTokenDenom()

	// invalid id for CreateDenom
	_, err = s.msgServer.CreateDenom(s.ctx, types.NewMsgCreateDenom(accAddrs[0].String(), "!"))
	s.Require().Error(err)

	// non-admin contract cannot mint / burn / change admin
	_, err = s.msgServer.MintTokens(s.ctx, types.NewMsgMintTokens(accAddrs[1].String(), sdk.NewCoin(denom, math.NewInt(1)), accAddrs[1].String()))
	s.Require().ErrorIs(err, types.ErrUnauthorized)
	_, err = s.msgServer.BurnTokens(s.ctx, types.NewMsgBurnTokens(accAddrs[1].String(), sdk.NewCoin(denom, math.NewInt(1))))
	s.Require().ErrorIs(err, types.ErrUnauthorized)
	_, err = s.msgServer.ChangeDenomAdmin(s.ctx, types.NewMsgChangeDenomAdmin(accAddrs[1].String(), denom, accAddrs[1].String()))
	s.Require().ErrorIs(err, types.ErrUnauthorized)

	// EOA senders are rejected even if they would be admin
	_, err = s.msgServer.MintTokens(s.ctx, types.NewMsgMintTokens(eoaAddr.String(), sdk.NewCoin(denom, math.NewInt(1)), eoaAddr.String()))
	s.Require().ErrorIs(err, types.ErrUnauthorized)

	// admin minting to an invalid recipient fails
	_, err = s.msgServer.MintTokens(s.ctx, types.NewMsgMintTokens(accAddrs[0].String(), sdk.NewCoin(denom, math.NewInt(1)), "bogus"))
	s.Require().Error(err)

	// admin burning more than held fails
	_, err = s.msgServer.BurnTokens(s.ctx, types.NewMsgBurnTokens(accAddrs[0].String(), sdk.NewCoin(denom, math.NewInt(1000))))
	s.Require().Error(err)

	// invalid new admin
	_, err = s.msgServer.ChangeDenomAdmin(s.ctx, types.NewMsgChangeDenomAdmin(accAddrs[0].String(), denom, "bogus"))
	s.Require().Error(err)
	admin, err := s.keeper.GetAdmin(sdkCtx, denom)
	s.Require().NoError(err)
	s.Require().Equal(accAddrs[0], admin)

	// renouncing admin (empty) removes it entirely
	_, err = s.msgServer.ChangeDenomAdmin(s.ctx, types.NewMsgChangeDenomAdmin(accAddrs[0].String(), denom, ""))
	s.Require().NoError(err)
	admin, err = s.keeper.GetAdmin(sdkCtx, denom)
	s.Require().NoError(err)
	s.Require().Nil(admin)
	q, err := s.queryClient.DenomAdmin(s.ctx, &types.QueryDenomAdminRequest{Denom: denom})
	s.Require().NoError(err)
	s.Require().Empty(q.Admin)
}
