package app

import (
	gogogrpc "github.com/gogo/protobuf/grpc"
	"github.com/cosmos/cosmos-sdk/baseapp"

	"google.golang.org/grpc"
)

// MsgServiceRouter is the THORChainApp version of MsgServiceRouter from baseapp
// This MsgServiceRouter is used to register each service.
// Each service registered is also registered in baseapp's MsgServiceRouter
// Baseapp's MsgServiceRouter is populated for baseapp's version of DeliverTx()->runTx()->runMsgs(),
//   reducing the need to implement DeliverTx() in THORChainApp.
// Custom routes can be set for each service using the service name and providing the new handler
//   (i.e. msgServer/keeper). The new handler must define the all the service's methods to be valid.
// Example: the "cosmos.bank.v1beta1.Msg" service can be routed to thorchain's keeper instead of
//   the bank's msgServer. The Send and MultiSend messages will be received by the thorchain module's keeper.
type MsgServiceRouter struct {
	*baseapp.MsgServiceRouter

	bAppMsr      *baseapp.MsgServiceRouter
	customRoutes map[string]interface{}
}

var _ gogogrpc.Server = &MsgServiceRouter{}

func NewMsgServiceRouter(bAppMsr *baseapp.MsgServiceRouter) *MsgServiceRouter {
	return &MsgServiceRouter{
		MsgServiceRouter: baseapp.NewMsgServiceRouter(),
		bAppMsr:          bAppMsr,
		customRoutes:     make(map[string]interface{}),
	}
}

func (msr *MsgServiceRouter) RegisterService(sd *grpc.ServiceDesc, handler interface{}) {
	customRoute := msr.customRoutes[sd.ServiceName]
	if customRoute == nil {
		msr.MsgServiceRouter.RegisterService(sd, handler)
		msr.bAppMsr.RegisterService(sd, handler)
	} else {
		msr.MsgServiceRouter.RegisterService(sd, customRoute)
		msr.bAppMsr.RegisterService(sd, customRoute)
	}
}

func (msr *MsgServiceRouter) AddCustomRoute(serviceName string, handler interface{}) {
	msr.customRoutes[serviceName] = handler
}