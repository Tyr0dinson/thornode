package thorchain

import (
	"fmt"

	"gitlab.com/thorchain/thornode/common/cosmos"
)

type MsgType = interface{}

type TypedHandler[M MsgType] interface {
	MsgHandler

	validate(ctx cosmos.Context, msg M) error
	handle(ctx cosmos.Context, msg M) (*cosmos.Result, error)
}

type BaseHandler[M MsgType] struct {
	TypedHandler[M]

	mgr        Manager
	converter  func(msg cosmos.Msg) (*M, bool)
	validators LadderDispatch[func(cosmos.Context, Manager, M) error]
	handlers   LadderDispatch[func(cosmos.Context, Manager, M) error]
}

func NewValidators[M MsgType]() LadderDispatch[func(cosmos.Context, Manager, M) error] {
	return LadderDispatch[func(cosmos.Context, Manager, M) error]{}
}
func NewHandlers[M MsgType]() LadderDispatch[func(cosmos.Context, Manager, M) error] {
	return LadderDispatch[func(cosmos.Context, Manager, M) error]{}
}

// Sadly Go's type system is still primitive and does not allow for generics in
// type aliases, so the following cleaner convenience type does not work:
//
//   type Validators[M MsgType] = LadderDispatch[func(cosmos.Context, Manager, M) error]
//
// See https://github.com/golang/go/issues/46477 for ongoing discussion.

func typeof(v interface{}) string {
	return fmt.Sprintf("%T", v)
}

func (h BaseHandler[M]) Run(ctx cosmos.Context, m cosmos.Msg) (*cosmos.Result, error) {
	msg, ok := h.converter(m)
	if !ok {
		return nil, errInvalidMessage
	}
	ctx.Logger().Info("received " + typeof(m))
	if err := h.validate(ctx, *msg); err != nil {
		ctx.Logger().Error(typeof(m) + " failed validation", "error", err)
		return nil, err
	}
	result, err := h.handle(ctx, *msg)
	if err != nil {
		ctx.Logger().Error(typeof(m) + " failed handler", "error", err)
	}
	return result, err
}

func (h BaseHandler[M]) validate(ctx cosmos.Context, msg M) error {
	version := h.mgr.GetVersion()
	handler := h.validators.Get(version)
	if handler != nil {
		return handler(ctx, h.mgr, msg)
	}
	return errBadVersion
}

func (h BaseHandler[M]) handle(ctx cosmos.Context, msg M) (*cosmos.Result, error) {
	version := h.mgr.GetVersion()
	handler := h.handlers.Get(version)
	if handler != nil {
		err := handler(ctx, h.mgr, msg)
		if err != nil {
			return nil, err
		}
		return &cosmos.Result{}, nil
	}
	// handlerNoCosmos := h.handlersNC.Get(version)
	// if handlerNoCosmos != nil {
	// 	err := handlerNoCosmos(ctx, h.mgr, msg)
	// 	if err != nil {
	// 		return nil, err
	// 	} else {
	// 		return &cosmos.Result{}, nil
	// 	}
	// }
	return nil, errBadVersion
}
