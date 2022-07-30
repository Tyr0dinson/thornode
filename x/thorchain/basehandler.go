package thorchain

import (
	//"fmt"

	//"github.com/blang/semver"
	//se "github.com/cosmos/cosmos-sdk/types/errors"

	//"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
	//"gitlab.com/thorchain/thornode/constants"
)

type BaseHandlerI interface {
	Run(ctx cosmos.Context, msg cosmos.Msg) (*cosmos.Result, error)
}

type TypedHandler[M interface{}] interface {
	BaseHandlerI

	validate(ctx cosmos.Context, msg M) error
	handle(ctx cosmos.Context, msg M) (*cosmos.Result, error)
}

type BaseHandler[M interface{}] struct {
	TypedHandler[M]

	mgr        Manager
	validators LadderDispatchT[func(Manager, cosmos.Context, M) error]
	handlers   LadderDispatchT[func(Manager, cosmos.Context, M) (*cosmos.Result, error)]
}

func NewValidators[M interface{}]() LadderDispatchT[func(Manager, cosmos.Context, M) error] {
	return LadderDispatchT[func(Manager, cosmos.Context, M) error]{}
}
func NewHandlers[M interface{}]() LadderDispatchT[func(Manager, cosmos.Context, M) (*cosmos.Result, error)] {
	return LadderDispatchT[func(Manager, cosmos.Context, M) (*cosmos.Result, error)]{}
}

// func NewHandlerX[M interface{}](mgr Manager, validators, handlers LadderDispatch) BaseHandler[M] {
// 	return BaseHandler[M]{
// 		mgr: mgr,
// 		validators: validators,
// 		handlers: handlers,
// 	}
// }

func (h BaseHandler[M]) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := m.(M)
	if !ok {
		return nil, errInvalidMessage
	}
	//ctx.Logger().Info("receive MsgUnBond",
	//	"node address", msg.NodeAddress,
	//	"request hash", msg.TxIn.ID,
	//	"amount", msg.Amount)
	if err := h.validate(ctx, msg); err != nil {
		ctx.Logger().Error("MsgType failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, msg)
	if err != nil {
		ctx.Logger().Error("MsgType failed handler", "error", err)
	}
	return result, nil
}

func (h BaseHandler[M]) validate(ctx cosmos.Context, msg M) error {
	version := h.mgr.GetVersion()
	handler := h.validators.Get(version)
	if handler != nil {
		return handler(h.mgr, ctx, msg)
	}
	return errBadVersion
}

func (h BaseHandler[M]) handle(ctx cosmos.Context, msg M) (*cosmos.Result, error) {
	version := h.mgr.GetVersion()
	handler := h.handlers.Get(version)
	if handler != nil {
		return handler(h.mgr, ctx, msg)
	}
	return nil, errBadVersion
}
