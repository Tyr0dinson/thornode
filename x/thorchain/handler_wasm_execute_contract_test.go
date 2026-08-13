package thorchain

import (
	"fmt"
	"math"

	storetypes "cosmossdk.io/store/types"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	protov2 "google.golang.org/protobuf/proto"

	. "gopkg.in/check.v1"

	cosmos "gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
)

type HandlerWasmExecuteContractSuite struct{}

var _ = Suite(&HandlerWasmExecuteContractSuite{})

func (s *HandlerWasmExecuteContractSuite) SetUpSuite(c *C) {
	SetupConfigForTest()
}

// mockFeeTx implements sdk.FeeTx for testing
type mockFeeTx struct {
	msgs []cosmos.Msg
	fee  cosmos.Coins
	gas  uint64
}

func (m mockFeeTx) GetMsgs() []cosmos.Msg {
	return m.msgs
}

func (m mockFeeTx) GetMsgsV2() ([]protov2.Message, error) {
	return nil, nil
}

func (m mockFeeTx) GetFee() cosmos.Coins {
	return m.fee
}

func (m mockFeeTx) GetGas() uint64 {
	return m.gas
}

func (m mockFeeTx) FeePayer() []byte {
	return nil
}

func (m mockFeeTx) FeeGranter() []byte {
	return nil
}

// mockNonFeeTx implements sdk.Tx but NOT sdk.FeeTx
type mockNonFeeTx struct {
	msgs []cosmos.Msg
}

func (m mockNonFeeTx) GetMsgs() []cosmos.Msg {
	return m.msgs
}

func (m mockNonFeeTx) GetMsgsV2() ([]protov2.Message, error) {
	return nil, nil
}

func (s *HandlerWasmExecuteContractSuite) TestInvalidMessage(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmExecuteContractHandler(mgr)

	result, err := h.Run(ctx, &MsgMimir{})
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err, Equals, errInvalidMessage)
}

func (s *HandlerWasmExecuteContractSuite) TestChainHalted(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmExecuteContractHandler(mgr)

	mgr.Keeper().SetMimir(ctx, "HaltTHORChain", 1)

	msg := &wasmtypes.MsgExecuteContract{
		Sender:   GetRandomBech32Addr().String(),
		Contract: GetRandomBech32Addr().String(),
		Msg:      []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "unable to use MsgExecuteContract while THORChain is halted")
}

func (s *HandlerWasmExecuteContractSuite) TestInvalidSender(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmExecuteContractHandler(mgr)

	msg := &wasmtypes.MsgExecuteContract{
		Sender:   "invalid-address",
		Contract: GetRandomBech32Addr().String(),
		Msg:      []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmExecuteContractSuite) TestInvalidContract(c *C) {
	ctx, mgr := setupManagerForTest(c)
	h := NewWasmExecuteContractHandler(mgr)

	msg := &wasmtypes.MsgExecuteContract{
		Sender:   GetRandomBech32Addr().String(),
		Contract: "invalid-address",
		Msg:      []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (s *HandlerWasmExecuteContractSuite) TestExecuteContractFails(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = &failingExecuteContractWasmManager{}
	h := NewWasmExecuteContractHandler(mgr)

	msg := &wasmtypes.MsgExecuteContract{
		Sender:   GetRandomBech32Addr().String(),
		Contract: GetRandomBech32Addr().String(),
		Msg:      []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
	c.Assert(err.Error(), Equals, "execute contract failed")
}

func (s *HandlerWasmExecuteContractSuite) TestSuccess(c *C) {
	ctx, mgr := setupManagerForTest(c)
	mgr.wasmManager = NewDummyWasmManager()
	h := NewWasmExecuteContractHandler(mgr)

	msg := &wasmtypes.MsgExecuteContract{
		Sender:   GetRandomBech32Addr().String(),
		Contract: GetRandomBech32Addr().String(),
		Msg:      []byte(`{}`),
	}

	result, err := h.Run(ctx, msg)
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

func (s *HandlerWasmExecuteContractSuite) TestAnteHandle_NonWasmMessage(c *C) {
	ctx, k := setupKeeperForTest(c)
	ad := NewWasmExecuteAnteDecorator(k, nil, nil)

	tx := mockFeeTx{
		msgs: []cosmos.Msg{&MsgMimir{}},
		fee:  cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(1))),
		gas:  100,
	}

	nextCalled := false
	next := func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	}

	_, err := ad.AnteHandle(ctx, tx, false, next)
	c.Assert(err, IsNil)
	c.Assert(nextCalled, Equals, true)
}

func (s *HandlerWasmExecuteContractSuite) TestAnteHandle_WasmMessageTriggersDeductFee(c *C) {
	ctx, k := setupKeeperForTest(c)
	// The AnteHandle with a wasm message will try to deduct fees via
	// ante.NewDeductFeeDecorator which requires ak/bk. Without them,
	// it will panic or fail. We just verify the wasm message triggers
	// the fee deduction path (not the next handler).
	ad := NewWasmExecuteAnteDecorator(k, nil, nil)

	tx := mockFeeTx{
		msgs: []cosmos.Msg{&wasmtypes.MsgStoreCode{}},
		fee:  cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(1))),
		gas:  100,
	}

	nextCalled := false
	next := func(ctx sdk.Context, tx sdk.Tx, simulate bool) (sdk.Context, error) {
		nextCalled = true
		return ctx, nil
	}

	// This will likely panic or error because ak/bk are nil, but the important
	// thing is that next() is NOT called (the wasm path is triggered)
	func() {
		defer func() {
			_ = recover() // recover from nil pointer panic in fee deduction
		}()
		_, _ = ad.AnteHandle(ctx, tx, false, next)
	}()
	c.Assert(nextCalled, Equals, false)
}

func (s *HandlerWasmExecuteContractSuite) TestCheckTxFee_NonFeeTx(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithIsCheckTx(true)

	tx := mockNonFeeTx{msgs: nil}
	_, _, err := checkTxFeeWithValidatorMinGasPrices(ctx, k, tx)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*Tx must be a FeeTx.*")
}

func (s *HandlerWasmExecuteContractSuite) TestCheckTxFee_InsufficientFees(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithIsCheckTx(true)

	// Set a high minimum gas price
	k.SetMimir(ctx, constants.MimirKeyWasmMinGasPrice, 1000000000000000000) // 1 rune per gas unit

	// Provide insufficient fees
	tx := mockFeeTx{
		fee: cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(1))),
		gas: 1000,
	}
	_, _, err := checkTxFeeWithValidatorMinGasPrices(ctx, k, tx)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*insufficient fees.*")
}

func (s *HandlerWasmExecuteContractSuite) TestCheckTxFee_SufficientFees(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithIsCheckTx(true)

	// Set a reasonable minimum gas price
	k.SetMimir(ctx, constants.MimirKeyWasmMinGasPrice, 1000000000000000000) // 1 rune per gas unit

	// Provide sufficient fees (1000 gas * 1 rune/gas = 1000 rune)
	tx := mockFeeTx{
		fee: cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(1000))),
		gas: 1000,
	}
	fees, _, err := checkTxFeeWithValidatorMinGasPrices(ctx, k, tx)
	c.Assert(err, IsNil)
	c.Assert(fees, DeepEquals, tx.fee)
}

func (s *HandlerWasmExecuteContractSuite) TestCheckTxFee_NegativeMinGasPrice(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithIsCheckTx(true)

	// Set a negative minimum gas price (should be clamped to 0)
	k.SetMimir(ctx, constants.MimirKeyWasmMinGasPrice, -100)

	tx := mockFeeTx{
		fee: cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(1))),
		gas: 1000,
	}
	fees, _, err := checkTxFeeWithValidatorMinGasPrices(ctx, k, tx)
	c.Assert(err, IsNil)
	c.Assert(fees, DeepEquals, tx.fee)
}

// gasConsumePanics reports whether consuming amt gas from gm triggers an
// out-of-gas panic (the clean-failure path a wasm cap must produce).
func gasConsumePanics(gm storetypes.GasMeter, amt uint64) (panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			panicked = true
		}
	}()
	gm.ConsumeGas(amt, "test")
	return false
}

// wasmExecTx is a small helper to build a wasm-execute tx for the cap tests.
func wasmExecTx() mockFeeTx {
	return mockFeeTx{
		msgs: []cosmos.Msg{&wasmtypes.MsgExecuteContract{
			Sender:   GetRandomBech32Addr().String(),
			Contract: GetRandomBech32Addr().String(),
			Msg:      []byte(`{}`),
		}},
		fee: cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(1))),
		gas: 100,
	}
}

// A wasm tx with the mimir set caps the gas meter, and consuming past the cap
// fails cleanly with out-of-gas (the anti-stall property) while at/under the cap
// is unaffected.
func (s *HandlerWasmExecuteContractSuite) TestCapWasmTxGas_CapsAndFailsClosed(c *C) {
	ctx, k := setupKeeperForTest(c)
	const capGas = int64(500_000_000)
	k.SetMimir(ctx, "WasmMaxGasPerTx", capGas)
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())

	nc, isWasm := capWasmTxGas(ctx, k, wasmExecTx())
	c.Assert(isWasm, Equals, true)
	c.Assert(nc.GasMeter().Limit(), Equals, uint64(capGas))

	// under/at the cap: no panic (legitimate contract unaffected).
	nc1, _ := capWasmTxGas(ctx, k, wasmExecTx())
	c.Assert(gasConsumePanics(nc1.GasMeter(), uint64(capGas)), Equals, false)

	// over the cap: clean out-of-gas panic (tx reverts, block does not stall).
	nc2, _ := capWasmTxGas(ctx, k, wasmExecTx())
	c.Assert(gasConsumePanics(nc2.GasMeter(), uint64(capGas)+1), Equals, true)
}

// With the mimir at 0 (disabled), a wasm tx keeps the InfiniteGasMeter — legacy
// unbounded behavior, no regression.
func (s *HandlerWasmExecuteContractSuite) TestCapWasmTxGas_DisabledWhenZero(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "WasmMaxGasPerTx", 0)
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())

	nc, isWasm := capWasmTxGas(ctx, k, wasmExecTx())
	c.Assert(isWasm, Equals, true)
	// still infinite: consuming far past any sane cap must not panic.
	c.Assert(gasConsumePanics(nc.GasMeter(), 10_000_000_000), Equals, false)
}

// A non-wasm tx is never capped, even with the mimir set.
func (s *HandlerWasmExecuteContractSuite) TestCapWasmTxGas_NonWasmUnaffected(c *C) {
	ctx, k := setupKeeperForTest(c)
	k.SetMimir(ctx, "WasmMaxGasPerTx", 500_000_000)
	ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())

	tx := mockFeeTx{msgs: []cosmos.Msg{&MsgMimir{}}}
	nc, isWasm := capWasmTxGas(ctx, k, tx)
	c.Assert(isWasm, Equals, false)
	c.Assert(gasConsumePanics(nc.GasMeter(), 10_000_000_000), Equals, false)
}

// A yevlec-style burst: every tx in the burst is independently bounded to the
// per-tx cap, so no single tx can run unbounded and stall the block.
func (s *HandlerWasmExecuteContractSuite) TestCapWasmTxGas_BurstEachTxBounded(c *C) {
	ctx, k := setupKeeperForTest(c)
	const capGas = int64(500_000_000)
	k.SetMimir(ctx, "WasmMaxGasPerTx", capGas)

	for i := 0; i < 20; i++ {
		// each tx arrives under the tx's InfiniteGasMeter, exactly as in a burst.
		txCtx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		nc, isWasm := capWasmTxGas(txCtx, k, wasmExecTx())
		c.Assert(isWasm, Equals, true)
		c.Assert(nc.GasMeter().Limit(), Equals, uint64(capGas))
		// the ~3-5B-gas yevlec call would trip the cap and revert, not stall.
		c.Assert(gasConsumePanics(nc.GasMeter(), 3_000_000_000), Equals, true)
	}
}

type failingExecuteContractWasmManager struct {
	DummyWasmManager
}

func (f *failingExecuteContractWasmManager) ExecuteContract(ctx cosmos.Context, contractAddr, senderAddr cosmos.AccAddress, msg []byte, coins cosmos.Coins) ([]byte, error) {
	return nil, fmt.Errorf("execute contract failed")
}

func (s *HandlerWasmExecuteContractSuite) TestCheckTxFeeWithValidatorMinGasPrices_GasOverflow(c *C) {
	ctx, k := setupKeeperForTest(c)
	// Enable check tx mode to trigger the gas price validation
	ctx = ctx.WithIsCheckTx(true)

	// Set a minimum gas price via Mimir
	k.SetMimir(ctx, constants.MimirKeyWasmMinGasPrice, 1000000000000000000) // 1 rune per gas unit

	// Test case 1: Normal gas value should work
	normalTx := mockFeeTx{
		fee: cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(1000000000))),
		gas: 1000,
	}
	_, _, err := checkTxFeeWithValidatorMinGasPrices(ctx, k, normalTx)
	// This may fail due to insufficient fees, but should not fail due to overflow
	// The point is it should not panic or have overflow issues
	if err != nil {
		c.Assert(err.Error(), Not(Matches), ".*exceeds maximum.*")
	}

	// Test case 2: Gas value at MaxInt64 boundary should work
	maxInt64Tx := mockFeeTx{
		fee: cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(1000000000000000000))),
		gas: uint64(math.MaxInt64),
	}
	_, _, err = checkTxFeeWithValidatorMinGasPrices(ctx, k, maxInt64Tx)
	// Should not fail with overflow error
	if err != nil {
		c.Assert(err.Error(), Not(Matches), ".*exceeds maximum.*")
	}

	// Test case 3: Gas value exceeding MaxInt64 should return error
	overflowTx := mockFeeTx{
		fee: cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(1000000000000000000))),
		gas: uint64(math.MaxInt64) + 1,
	}
	_, _, err = checkTxFeeWithValidatorMinGasPrices(ctx, k, overflowTx)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*exceeds maximum allowed value.*")

	// Test case 4: Maximum uint64 gas value should return error
	maxUint64Tx := mockFeeTx{
		fee: cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(1000000000000000000))),
		gas: math.MaxUint64,
	}
	_, _, err = checkTxFeeWithValidatorMinGasPrices(ctx, k, maxUint64Tx)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*exceeds maximum allowed value.*")
}

func (s *HandlerWasmExecuteContractSuite) TestCheckTxFeeWithValidatorMinGasPrices_NoCheckTx(c *C) {
	ctx, k := setupKeeperForTest(c)
	// Not in check tx mode - should skip gas price validation
	ctx = ctx.WithIsCheckTx(false)

	// Set a minimum gas price via Mimir
	k.SetMimir(ctx, constants.MimirKeyWasmMinGasPrice, 1000000000000000000)

	// Even with overflow gas, non-CheckTx should pass (no validation done)
	overflowTx := mockFeeTx{
		fee: cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(1))),
		gas: math.MaxUint64,
	}
	fees, _, err := checkTxFeeWithValidatorMinGasPrices(ctx, k, overflowTx)
	c.Assert(err, IsNil)
	c.Assert(fees, DeepEquals, overflowTx.fee)
}

func (s *HandlerWasmExecuteContractSuite) TestCheckTxFeeWithValidatorMinGasPrices_ZeroMinGasPrice(c *C) {
	ctx, k := setupKeeperForTest(c)
	ctx = ctx.WithIsCheckTx(true)

	// Set zero minimum gas price
	k.SetMimir(ctx, constants.MimirKeyWasmMinGasPrice, 0)

	// With zero min gas price, overflow check should not be triggered since minGasPrices.IsZero() is true
	overflowTx := mockFeeTx{
		fee: cosmos.NewCoins(cosmos.NewCoin("rune", cosmos.NewInt(1))),
		gas: math.MaxUint64,
	}
	fees, _, err := checkTxFeeWithValidatorMinGasPrices(ctx, k, overflowTx)
	c.Assert(err, IsNil)
	c.Assert(fees, DeepEquals, overflowTx.fee)
}
