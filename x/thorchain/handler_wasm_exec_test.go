package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	. "gopkg.in/check.v1"
)

type HandlerWasmExecSuite struct{}

var _ = Suite(&HandlerWasmExecSuite{})

func (s *HandlerWasmExecSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

func (s *HandlerWasmExecSuite) TestWasmExecHandler_InvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmExecHandler(mgr)

	// Passing wrong message type should fail
	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerWasmExecSuite) TestWasmExecHandler_ValidateBasicFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmExecHandler(mgr)

	// Empty asset should fail validation
	msg := &MsgWasmExec{
		Asset:    common.EmptyAsset,
		Amount:   cosmos.NewUint(100),
		Contract: GetRandomBech32Addr(),
		Sender:   GetRandomBech32Addr(),
		Signer:   GetRandomBech32Addr(),
		Msg:      []byte(`{"action":"test"}`),
		Tx:       common.Tx{ID: GetRandomTxHash()},
	}
	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// Zero amount should fail validation
	msg2 := NewMsgWasmExec(
		common.BTCAsset,
		cosmos.ZeroUint(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{ID: GetRandomTxHash()},
	)
	result2, err2 := h.Run(ctx, msg2)
	c.Assert(err2, NotNil)
	c.Assert(result2, IsNil)

	// Empty contract address should fail
	msg3 := NewMsgWasmExec(
		common.BTCAsset,
		cosmos.NewUint(100),
		cosmos.AccAddress{},
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{ID: GetRandomTxHash()},
	)
	result3, err3 := h.Run(ctx, msg3)
	c.Assert(err3, NotNil)
	c.Assert(result3, IsNil)

	// Empty sender should fail
	msg4 := NewMsgWasmExec(
		common.BTCAsset,
		cosmos.NewUint(100),
		GetRandomBech32Addr(),
		cosmos.AccAddress{},
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{ID: GetRandomTxHash()},
	)
	result4, err4 := h.Run(ctx, msg4)
	c.Assert(err4, NotNil)
	c.Assert(result4, IsNil)

	// Empty signer should fail
	msg5 := NewMsgWasmExec(
		common.BTCAsset,
		cosmos.NewUint(100),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		cosmos.AccAddress{},
		[]byte(`{"action":"test"}`),
		common.Tx{ID: GetRandomTxHash()},
	)
	result5, err5 := h.Run(ctx, msg5)
	c.Assert(err5, NotNil)
	c.Assert(result5, IsNil)

	// Empty tx ID should fail
	msg6 := NewMsgWasmExec(
		common.BTCAsset,
		cosmos.NewUint(100),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{},
	)
	result6, err6 := h.Run(ctx, msg6)
	c.Assert(err6, NotNil)
	c.Assert(result6, IsNil)
}

func (s *HandlerWasmExecSuite) TestWasmExecHandler_ChainHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmExecHandler(mgr)

	// Halt THORChain (key format is "Halt%sChain" where chain is "THOR")
	mgr.Keeper().SetMimir(ctx, "HaltTHORChain", 1)

	msg := NewMsgWasmExec(
		common.BTCAsset,
		cosmos.NewUint(100),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{ID: GetRandomTxHash()},
	)
	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "unable to use MsgWasmExec while THORChain is halted")
}

func (s *HandlerWasmExecSuite) TestWasmExecHandler_SecuredAsset(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Replace real wasm manager with dummy (real one needs a deployed contract)
	mgr.wasmManager = NewDummyWasmManager()

	h := NewWasmExecHandler(mgr)

	// Use a secured asset - this path skips the SecuredAssetManager.Deposit call
	// and directly creates the coin
	securedAsset := common.BTCAsset.GetSecuredAsset()

	msg := NewMsgWasmExec(
		securedAsset,
		cosmos.NewUint(100),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{ID: GetRandomTxHash()},
	)

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

func (s *HandlerWasmExecSuite) TestWasmExecHandler_NonSecuredAsset_DepositFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmExecHandler(mgr)

	// Use a non-secured, non-native asset (e.g. BTC)
	// This will call SecuredAssetManager().Deposit() which requires a pool
	// Without a pool set up, the deposit should fail
	msg := NewMsgWasmExec(
		common.BTCAsset,
		cosmos.NewUint(100),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{
			ID:          GetRandomTxHash(),
			FromAddress: GetRandomBTCAddress(),
		},
	)

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmExecSuite) TestWasmExecHandler_NonSecuredAsset_DepositSuccess(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Replace real wasm manager with dummy (real one needs a deployed contract)
	mgr.wasmManager = NewDummyWasmManager()

	h := NewWasmExecHandler(mgr)

	// Set up a BTC pool so SecuredAssetManager.Deposit can succeed
	c.Assert(mgr.Keeper().SetPool(ctx, availableBTCPool()), IsNil)

	msg := NewMsgWasmExec(
		common.BTCAsset,
		cosmos.NewUint(100),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{
			ID:          GetRandomTxHash(),
			FromAddress: GetRandomBTCAddress(),
		},
	)

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

func (s *HandlerWasmExecSuite) TestWasmExecHandler_ExecuteContractFails(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// Replace WasmManager with one that returns an error on ExecuteContract
	mgr.wasmManager = &failingWasmManager{}

	h := NewWasmExecHandler(mgr)

	securedAsset := common.BTCAsset.GetSecuredAsset()
	msg := NewMsgWasmExec(
		securedAsset,
		cosmos.NewUint(100),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{ID: GetRandomTxHash()},
	)

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "execute contract failed")
}

// TestWasmExecHandler_DepositRolledBackOnExecuteFailure ensures that when a
// non-secured asset is deposited (minting secured shares and increasing pool
// depth) but the contract execution subsequently fails, the deposit is fully
// rolled back so no shares are left minted without a completed execution (#3032).
func (s *HandlerWasmExecSuite) TestWasmExecHandler_DepositRolledBackOnExecuteFailure(c *C) {
	ctx, mgr := setupManagerForTest(c)

	// WasmManager fails on ExecuteContract, after the deposit has been performed.
	mgr.wasmManager = &failingWasmManager{}

	h := NewWasmExecHandler(mgr)

	// Set up a BTC pool so SecuredAssetManager.Deposit can succeed.
	c.Assert(mgr.Keeper().SetPool(ctx, availableBTCPool()), IsNil)

	sender := GetRandomBech32Addr()
	securedAsset := common.BTCAsset.GetSecuredAsset()

	// No secured shares and no secured-asset pool depth before the call.
	c.Assert(mgr.Keeper().GetBalanceOf(ctx, sender, securedAsset).Amount.IsZero(), Equals, true)

	msg := NewMsgWasmExec(
		common.BTCAsset,
		cosmos.NewUint(100),
		GetRandomBech32Addr(),
		sender,
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{
			ID:          GetRandomTxHash(),
			FromAddress: GetRandomBTCAddress(),
		},
	)

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)

	// The secured-asset mint must have been rolled back with the failed execution.
	c.Assert(mgr.Keeper().GetBalanceOf(ctx, sender, securedAsset).Amount.IsZero(), Equals, true)

	// The secured-asset pool depth increase must have been rolled back too.
	securedPool, err := mgr.Keeper().GetSecuredAsset(ctx, securedAsset)
	c.Assert(err, IsNil)
	c.Assert(securedPool.Depth.IsZero(), Equals, true)
}

// failingWasmManager is a test double that returns errors on ExecuteContract
type failingWasmManager struct {
	DummyWasmManager
}

func (f *failingWasmManager) ExecuteContract(ctx cosmos.Context,
	contractAddr, senderAddr cosmos.AccAddress,
	msg []byte,
	coins cosmos.Coins,
) ([]byte, error) {
	return nil, fmt.Errorf("execute contract failed")
}

func (s *HandlerWasmExecSuite) TestWasmExecHandler_THORChainAssetRejected(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmExecHandler(mgr)

	// THORChain native asset (RUNE) that is not a secured asset should be rejected
	// by ValidateBasic: "asset cannot be THORChain asset"
	msg := NewMsgWasmExec(
		common.RuneAsset(),
		cosmos.NewUint(100),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{ID: GetRandomTxHash()},
	)

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

// availableBTCPool returns a funded, available BTC pool for the exec tests.
func availableBTCPool() Pool {
	pool := NewPool()
	pool.Asset = common.BTCAsset
	pool.BalanceAsset = cosmos.NewUint(1000 * common.One)
	pool.BalanceRune = cosmos.NewUint(10000 * common.One)
	pool.Status = PoolAvailable
	return pool
}

// execMsgForBTC builds a layer-1 BTC exec message minting secured shares for sender.
func execMsgForBTC(sender cosmos.AccAddress) *MsgWasmExec {
	return NewMsgWasmExec(
		common.BTCAsset,
		cosmos.NewUint(100),
		GetRandomBech32Addr(),
		sender,
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{
			ID:          GetRandomTxHash(),
			FromAddress: GetRandomBTCAddress(),
		},
	)
}

// TestWasmExecHandler_PerChainDepositHalt ensures an exec memo cannot mint
// secured-asset shares while that chain's secured deposits are halted by
// governance, which the dedicated secured-asset deposit handler already
// enforces (#2702).
func (s *HandlerWasmExecSuite) TestWasmExecHandler_PerChainDepositHalt(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmExecHandler(mgr)

	c.Assert(mgr.Keeper().SetPool(ctx, availableBTCPool()), IsNil)
	mgr.Keeper().SetMimir(ctx, fmt.Sprintf(constants.MimirTemplateSecuredAssetHaltDeposit, common.BTCChain), 1)

	sender := GetRandomBech32Addr()
	result, err := h.Run(ctx, execMsgForBTC(sender))
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Check(err.Error(), Matches, ".*secured asset deposits are disabled.*")

	// nothing was minted for the sender
	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, common.BTCAsset, sender).IsZero(), Equals, true)
}

// TestWasmExecHandler_SecuredAssetNotHalted ensures the per-chain deposit halt
// only gates exec calls that mint new shares; moving already minted secured
// assets into a contract is unaffected.
func (s *HandlerWasmExecSuite) TestWasmExecHandler_SecuredAssetNotHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmExecHandler(mgr)

	mgr.Keeper().SetMimir(ctx, fmt.Sprintf(constants.MimirTemplateSecuredAssetHaltDeposit, common.BTCChain), 1)

	msg := NewMsgWasmExec(
		common.BTCAsset.GetSecuredAsset(),
		cosmos.NewUint(100),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		GetRandomBech32Addr(),
		[]byte(`{"action":"test"}`),
		common.Tx{ID: GetRandomTxHash()},
	)

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

// TestWasmExecHandler_PoolUnavailable ensures an exec memo cannot mint secured
// shares for an asset whose layer-1 pool is not available (#2702).
func (s *HandlerWasmExecSuite) TestWasmExecHandler_PoolUnavailable(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmExecHandler(mgr)

	pool := availableBTCPool()
	pool.Status = PoolStaged
	c.Assert(mgr.Keeper().SetPool(ctx, pool), IsNil)

	sender := GetRandomBech32Addr()
	result, err := h.Run(ctx, execMsgForBTC(sender))
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Check(err.Error(), Matches, ".*pool.*unavailable.*")

	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, common.BTCAsset, sender).IsZero(), Equals, true)
}

// TestWasmExecHandler_TVLCap ensures an exec memo cannot mint secured shares
// beyond the bond-backed TVL cap (#2702).
func (s *HandlerWasmExecSuite) TestWasmExecHandler_TVLCap(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmExecHandler(mgr)

	c.Assert(mgr.Keeper().SetPool(ctx, availableBTCPool()), IsNil)

	// mocknet disables StrictBondLiquidityRatio, so enable it to exercise the cap
	mgr.constAccessor = constants.NewDummyConstants(map[constants.ConstantName]int64{
		constants.MaximumLiquidityRune: 1,
	}, map[constants.ConstantName]bool{
		constants.StrictBondLiquidityRatio: true,
	}, map[constants.ConstantName]string{})

	// an asgard vault holding far more value than the (zero) active bond
	vault := GetRandomVault()
	vault.Coins = common.Coins{
		common.NewCoin(common.BTCAsset, cosmos.NewUint(10000*common.One)),
	}
	c.Assert(mgr.Keeper().SetVault(ctx, vault), IsNil)

	sender := GetRandomBech32Addr()
	result, err := h.Run(ctx, execMsgForBTC(sender))
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Check(err.Error(), Matches, ".*secured deposits more than bond.*")

	c.Check(mgr.SecuredAssetManager().BalanceOf(ctx, common.BTCAsset, sender).IsZero(), Equals, true)
}
