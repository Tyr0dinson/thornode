package thorchain

import (
	"context"
	"errors"

	errorsmod "cosmossdk.io/errors"
	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	se "github.com/cosmos/cosmos-sdk/types/errors"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type MsgServerCovCSuite struct{}

var _ = Suite(&MsgServerCovCSuite{})

type covCFuncHandler func(ctx cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error)

func (f covCFuncHandler) Run(ctx cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error) {
	return f(ctx, msg)
}

func (s *MsgServerCovCSuite) TestExternalHandlerCovC(c *C) {
	ctx, _ := setupManagerForTest(c)
	msg := &types.MsgBan{}

	// success returns an empty response
	res, err := externalHandler(ctx, covCFuncHandler(func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		return &cosmos.Result{}, nil
	}), msg)
	c.Assert(err, IsNil)
	c.Check(res, DeepEquals, &types.MsgEmpty{})

	// nil result is fine too
	res, err = externalHandler(ctx, covCFuncHandler(func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		return nil, nil
	}), msg)
	c.Assert(err, IsNil)
	c.Check(res, NotNil)

	// non-empty results are rejected
	res, err = externalHandler(ctx, covCFuncHandler(func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		return &cosmos.Result{Log: "something"}, nil
	}), msg)
	c.Check(err, ErrorMatches, "external handler, handler returned non-empty result.*")
	c.Check(res, IsNil)

	// plain (code 1, redacted) errors are wrapped as internal errors
	res, err = externalHandler(ctx, covCFuncHandler(func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		return nil, errKaboom
	}), msg)
	c.Check(res, IsNil)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, errInternal), Equals, true)
	c.Check(err.Error(), Matches, "kaboom.*")

	// registered sdk errors pass through untouched
	res, err = externalHandler(ctx, covCFuncHandler(func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		return nil, errorsmod.Wrap(se.ErrUnauthorized, "nope")
	}), msg)
	c.Check(res, IsNil)
	c.Check(errors.Is(err, se.ErrUnauthorized), Equals, true)
	c.Check(errors.Is(err, errInternal), Equals, false)

	// panics are recovered and returned as errors
	res, err = externalHandler(ctx, covCFuncHandler(func(cosmos.Context, cosmos.Msg) (*cosmos.Result, error) {
		panic("boom")
	}), msg)
	c.Check(res, IsNil)
	c.Check(err, ErrorMatches, "panic: boom")
}

func (s *MsgServerCovCSuite) TestMsgServerRejectsUnsignedCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ms := NewMsgServerImpl(mgr)
	var goCtx context.Context = ctx

	type call struct {
		name string
		fn   func() (*types.MsgEmpty, error)
	}
	calls := []call{
		{"Ban", func() (*types.MsgEmpty, error) { return ms.Ban(goCtx, &types.MsgBan{}) }},
		{"Deposit", func() (*types.MsgEmpty, error) { return ms.Deposit(goCtx, &types.MsgDeposit{}) }},
		{"ErrataTx", func() (*types.MsgEmpty, error) { return ms.ErrataTx(goCtx, &types.MsgErrataTx{}) }},
		{"ErrataTxQuorum", func() (*types.MsgEmpty, error) { return ms.ErrataTxQuorum(goCtx, &types.MsgErrataTxQuorum{}) }},
		{"Mimir", func() (*types.MsgEmpty, error) { return ms.Mimir(goCtx, &types.MsgMimir{}) }},
		{"NetworkFee", func() (*types.MsgEmpty, error) { return ms.NetworkFee(goCtx, &types.MsgNetworkFee{}) }},
		{"NetworkFeeQuorum", func() (*types.MsgEmpty, error) {
			return ms.NetworkFeeQuorum(goCtx, &types.MsgNetworkFeeQuorum{})
		}},
		{"NodePauseChain", func() (*types.MsgEmpty, error) { return ms.NodePauseChain(goCtx, &types.MsgNodePauseChain{}) }},
		{"ObservedTxIn", func() (*types.MsgEmpty, error) { return ms.ObservedTxIn(goCtx, &types.MsgObservedTxIn{}) }},
		{"ObservedTxOut", func() (*types.MsgEmpty, error) { return ms.ObservedTxOut(goCtx, &types.MsgObservedTxOut{}) }},
		{"ObservedTxQuorum", func() (*types.MsgEmpty, error) {
			return ms.ObservedTxQuorum(goCtx, &types.MsgObservedTxQuorum{})
		}},
		{"PriceFeedQuorumBatch", func() (*types.MsgEmpty, error) {
			return ms.PriceFeedQuorumBatch(goCtx, &types.MsgPriceFeedQuorumBatch{})
		}},
		{"ThorSend", func() (*types.MsgEmpty, error) { return ms.ThorSend(goCtx, &types.MsgSend{}) }},
		{"SetIPAddress", func() (*types.MsgEmpty, error) { return ms.SetIPAddress(goCtx, &types.MsgSetIPAddress{}) }},
		{"SetNodeKeys", func() (*types.MsgEmpty, error) { return ms.SetNodeKeys(goCtx, &types.MsgSetNodeKeys{}) }},
		{"Solvency", func() (*types.MsgEmpty, error) { return ms.Solvency(goCtx, &types.MsgSolvency{}) }},
		{"SolNonceAttest", func() (*types.MsgEmpty, error) { return ms.SolNonceAttest(goCtx, &types.MsgSolNonceAttest{}) }},
		{"SolvencyQuorum", func() (*types.MsgEmpty, error) { return ms.SolvencyQuorum(goCtx, &types.MsgSolvencyQuorum{}) }},
		{"TssKeysignFail", func() (*types.MsgEmpty, error) { return ms.TssKeysignFail(goCtx, &types.MsgTssKeysignFail{}) }},
		{"TssPool", func() (*types.MsgEmpty, error) { return ms.TssPool(goCtx, &types.MsgTssPool{}) }},
		{"SetVersion", func() (*types.MsgEmpty, error) { return ms.SetVersion(goCtx, &types.MsgSetVersion{}) }},
		{"ProposeUpgrade", func() (*types.MsgEmpty, error) { return ms.ProposeUpgrade(goCtx, &types.MsgProposeUpgrade{}) }},
		{"ApproveUpgrade", func() (*types.MsgEmpty, error) { return ms.ApproveUpgrade(goCtx, &types.MsgApproveUpgrade{}) }},
		{"RejectUpgrade", func() (*types.MsgEmpty, error) { return ms.RejectUpgrade(goCtx, &types.MsgRejectUpgrade{}) }},
	}
	for _, cl := range calls {
		res, err := cl.fn()
		c.Check(err, NotNil, Commentf("%s should reject an empty message", cl.name))
		c.Check(res, IsNil, Commentf("%s", cl.name))
		if err != nil {
			c.Check(err.Error(), Not(Matches), "panic:.*", Commentf("%s", cl.name))
		}
	}
}

func (s *MsgServerCovCSuite) TestMsgServerSuccessCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ms := NewMsgServerImpl(mgr)

	// an active node can set its IP address through the msg server
	na := GetRandomValidatorNode(NodeActive)
	c.Assert(mgr.Keeper().SetNodeAccount(ctx, na), IsNil)
	FundModule(c, ctx, mgr.Keeper(), BondName, 10*common.One)
	res, err := ms.SetIPAddress(ctx, types.NewMsgSetIPAddress("192.168.0.1", na.NodeAddress))
	c.Assert(err, IsNil)
	c.Check(res, DeepEquals, &types.MsgEmpty{})
	got, err := mgr.Keeper().GetNodeAccount(ctx, na.NodeAddress)
	c.Assert(err, IsNil)
	c.Check(got.IPAddress, Equals, "192.168.0.1")
}

func (s *MsgServerCovCSuite) TestMsgServerWasmsCovC(c *C) {
	ctx, mgr := setupManagerForTest(c)
	ms, ok := NewMsgServerImpl(mgr).(*msgServer)
	c.Assert(ok, Equals, true)

	// empty messages fail validation in each wasm handler
	_, err := ms.StoreCode(ctx, &wasmtypes.MsgStoreCode{})
	c.Check(err, NotNil)
	_, err = ms.InstantiateContract(ctx, &wasmtypes.MsgInstantiateContract{})
	c.Check(err, NotNil)
	_, err = ms.InstantiateContract2(ctx, &wasmtypes.MsgInstantiateContract2{})
	c.Check(err, NotNil)
	_, err = ms.ExecuteContract(ctx, &wasmtypes.MsgExecuteContract{})
	c.Check(err, NotNil)
	_, err = ms.MigrateContract(ctx, &wasmtypes.MsgMigrateContract{})
	c.Check(err, NotNil)
	_, err = ms.SudoContract(ctx, &wasmtypes.MsgSudoContract{})
	c.Check(err, NotNil)
	_, err = ms.UpdateAdmin(ctx, &wasmtypes.MsgUpdateAdmin{})
	c.Check(err, NotNil)
	_, err = ms.ClearAdmin(ctx, &wasmtypes.MsgClearAdmin{})
	c.Check(err, NotNil)

	// executing against a non-existent contract with a valid sender fails
	sender := GetRandomBech32Addr()
	contract := GetRandomBech32Addr()
	_, err = ms.ExecuteContract(ctx, &wasmtypes.MsgExecuteContract{
		Sender:   sender.String(),
		Contract: contract.String(),
		Msg:      []byte(`{}`),
	})
	c.Check(err, NotNil)
}
