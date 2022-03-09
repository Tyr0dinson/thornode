package app

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/simapp"
	"github.com/cosmos/cosmos-sdk/store"
	cosmostypes "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/x/auth/ante"
	authkeeper "github.com/cosmos/cosmos-sdk/x/auth/keeper"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	bankkeeper "github.com/cosmos/cosmos-sdk/x/bank/keeper"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	paramskeeper "github.com/cosmos/cosmos-sdk/x/params/keeper"
	paramstypes "github.com/cosmos/cosmos-sdk/x/params/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tendermint/tendermint/libs/log"
	tmproto "github.com/tendermint/tendermint/proto/tendermint/types"
	dbm "github.com/tendermint/tm-db"

	"gitlab.com/thorchain/thornode/cmd"
	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain"
	"gitlab.com/thorchain/thornode/x/thorchain/types"
)

func setupForAnteTest(t *testing.T) (cosmos.Context, ante.AccountKeeper, bankkeeper.Keeper) {
	cosmostypes.SetCoinDenomRegex(func() string {
		return cmd.DenomRegex
	})
	keyAcc := cosmos.NewKVStoreKey(authtypes.StoreKey)
	keyBank := cosmos.NewKVStoreKey(banktypes.StoreKey)
	keyParams := cosmos.NewKVStoreKey(paramstypes.StoreKey)
	tkeyParams := cosmos.NewTransientStoreKey(paramstypes.TStoreKey)
	keyThorchain := cosmos.NewKVStoreKey(thorchain.StoreKey)
	db := dbm.NewMemDB()
	ms := store.NewCommitMultiStore(db)
	ms.MountStoreWithDB(keyAcc, cosmos.StoreTypeIAVL, db)
	ms.MountStoreWithDB(keyParams, cosmos.StoreTypeIAVL, db)
	ms.MountStoreWithDB(keyThorchain, cosmos.StoreTypeIAVL, db)
	ms.MountStoreWithDB(keyBank, cosmos.StoreTypeIAVL, db)
	ms.MountStoreWithDB(tkeyParams, cosmos.StoreTypeTransient, db)
	err := ms.LoadLatestVersion()
	assert.Nil(t, err)

	ctx := cosmos.NewContext(ms, tmproto.Header{ChainID: "thorchain"}, false, log.NewNopLogger())
	ctx = ctx.WithBlockHeight(18)
	legacyCodec := types.MakeTestCodec()
	marshaler := simapp.MakeTestEncodingConfig().Marshaler

	pk := paramskeeper.NewKeeper(marshaler, legacyCodec, keyParams, tkeyParams)
	ak := authkeeper.NewAccountKeeper(marshaler, keyAcc, pk.Subspace(authtypes.ModuleName), authtypes.ProtoBaseAccount, map[string][]string{
		thorchain.ModuleName:  {authtypes.Minter, authtypes.Burner},
		thorchain.AsgardName:  {},
		thorchain.BondName:    {},
		thorchain.ReserveName: {},
	})

	bk := bankkeeper.NewBaseKeeper(marshaler, keyBank, ak, pk.Subspace(banktypes.ModuleName), nil)
	bk.SetSupply(ctx, banktypes.NewSupply(cosmos.Coins{
		cosmos.NewCoin(common.RuneAsset().Native(), cosmos.NewInt(200_000_000_00000000)),
	}))
	return ctx, ak, bk
}

func TestTHORChainFeeDecorator_AnteHandle(t *testing.T) {
	types.SetupConfigForTest()
	ctx, ak, bk := setupForAnteTest(t)
	decorator := NewTHORChainFeeDecorator(ak, bk)
	anteHandler := cosmostypes.ChainAnteDecorators(decorator)
	cfg := MakeEncodingConfig()
	builder := cfg.TxConfig.NewTxBuilder()
	signer := types.GetRandomBech32Addr()
	observeMsg := types.NewMsgObservedTxIn(types.ObservedTxs{types.GetRandomObservedTx()}, signer)
	require.NoError(t, builder.SetMsgs(observeMsg))
	// observedTx should not be rejected , regardless how much account balance
	_, err := anteHandler(ctx, builder.GetTx(), false)
	require.NoError(t, err)

	// MsgSend should check whether sender has enough balance to cover NativeTransactionFee
	builder = cfg.TxConfig.NewTxBuilder()
	msgSend := types.NewMsgSend(signer, types.GetRandomBech32Addr(), cosmos.NewCoins(
		cosmos.NewCoin("rune", cosmos.NewInt(1024)),
	))

	require.NoError(t, builder.SetMsgs(msgSend))
	_, err = anteHandler(ctx, builder.GetTx(), false)
	require.ErrorIs(t, err, sdkerrors.ErrInsufficientFee)

	// MsgDeposit should check whether sender has enough balance to cover NativeTransactionFee
	builder = cfg.TxConfig.NewTxBuilder()
	msgDeposit := types.NewMsgDeposit(common.NewCoins(
		common.NewCoin(common.RuneAsset(), cosmos.NewUint(1024)),
	), "whatever", signer)
	require.NoError(t, builder.SetMsgs(msgDeposit))
	_, err = anteHandler(ctx, builder.GetTx(), false)
	require.ErrorIs(t, err, sdkerrors.ErrInsufficientFee)

	// fund the sender account , the transaction should be accepted
	require.NoError(t, bk.MintCoins(ctx, thorchain.ModuleName, cosmostypes.Coins{
		cosmostypes.NewCoin("rune", cosmostypes.NewInt(100_00_000_000)),
	}))

	require.NoError(t, bk.SendCoinsFromModuleToAccount(ctx, thorchain.ModuleName, signer, cosmostypes.Coins{
		cosmostypes.NewCoin("rune", cosmostypes.NewInt(100_00_000_000)),
	}))
	_, err = anteHandler(ctx, builder.GetTx(), false)
	require.NoError(t, err)
}
