package app

import (
	"fmt"
	gogogrpc "github.com/gogo/protobuf/grpc"
	"github.com/cosmos/cosmos-sdk/baseapp"

	"google.golang.org/grpc"

	keeper "gitlab.com/thorchain/thornode/x/thorchain/keeper"

)

type MsgServiceRouter struct {
	*baseapp.MsgServiceRouter
	k keeper.Keeper
}

var _ gogogrpc.Server = &MsgServiceRouter{}

func NewMsgServiceRouter(tcKeeper keeper.Keeper) *MsgServiceRouter {
	return &MsgServiceRouter{
		MsgServiceRouter: baseapp.NewMsgServiceRouter(),
		k: tcKeeper,
	}
}

func (msr *MsgServiceRouter) RegisterService(sd *grpc.ServiceDesc, handler interface{}) {
	if sd.ServiceName != "cosmos.bank.v1beta1.Msg" {
		msr.MsgServiceRouter.RegisterService(sd, handler)
	} else {
		fmt.Println("Handler:", handler)
		fmt.Println("Keeper:", msr.k)
		msr.MsgServiceRouter.RegisterService(sd, msr.k)
	}
}
// func (msr *MsgServiceRouter) Handler(msg sdk.Msg) baseapp.MsgServiceHandler {
// 	fmt.Println("Handler():", sdk.MsgTypeURL(msg))
// 	if newRoute := reRoute(sdk.MsgTypeURL(msg)); newRoute != nil {
// 		return newRoute
// 	}
// 	return msr.MsgServiceRouter.Handler(msg)
// }

// func (msr *MsgServiceRouter) HandlerByTypeURL(typeURL string) baseapp.MsgServiceHandler {
// 	fmt.Println("HandlerByTypeURL():", typeURL)
// 	if newRoute := reRoute(typeURL); newRoute != nil {
// 		return newRoute
// 	}
// 	return msr.MsgServiceRouter.HandlerByTypeURL(typeURL)
// }

// func reRoute(typeURL string) (baseapp.MsgServiceHandler) {
// 	if typeURL == "/cosmos.bank.v1beta1.Msg/Send" {
// 		return thorchain.MsgBankSendHandle
// 	}

// 	return nil
// }