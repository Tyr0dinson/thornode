package thorchain

import (
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"

	capabilitytypes "github.com/cosmos/ibc-go/modules/capability/types"
	channeltypes "github.com/cosmos/ibc-go/v8/modules/core/04-channel/types"
	porttypes "github.com/cosmos/ibc-go/v8/modules/core/05-port/types"
	ibcexported "github.com/cosmos/ibc-go/v8/modules/core/exported"
)

var _ porttypes.IBCModule = (*IBCMiddleware)(nil)

type IBCMiddleware struct {
	app    porttypes.IBCModule
	keeper keeper.Keeper
}

func NewIBCMiddleware(
	app porttypes.IBCModule,
	keeper keeper.Keeper,
) IBCMiddleware {
	return IBCMiddleware{
		app:    app,
		keeper: keeper,
	}
}

func (im IBCMiddleware) OnAcknowledgementPacket(
	ctx cosmos.Context,
	packet channeltypes.Packet,
	acknowledgement []byte,
	relayer cosmos.AccAddress,
) error {
	return im.app.OnAcknowledgementPacket(ctx, packet, acknowledgement, relayer)
}

func (im IBCMiddleware) OnChanOpenInit(
	ctx cosmos.Context,
	channelOrdering channeltypes.Order,
	connectionHops []string,
	portID,
	channelID string,
	channelCap *capabilitytypes.Capability,
	counterparty channeltypes.Counterparty,
	version string,
) (string, error) {
	return im.app.OnChanOpenInit(ctx, channelOrdering, connectionHops, portID, channelID, channelCap, counterparty, version)
}

func (im IBCMiddleware) OnChanOpenTry(
	ctx cosmos.Context,
	channelOrdering channeltypes.Order,
	connectionHops []string, portID,
	channelID string,
	channelCap *capabilitytypes.Capability,
	counterparty channeltypes.Counterparty,
	counterpartyVersion string,
) (string, error) {
	return im.app.OnChanOpenTry(ctx, channelOrdering, connectionHops, portID, channelID, channelCap, counterparty, counterpartyVersion)
}

func (im IBCMiddleware) OnChanOpenAck(ctx cosmos.Context, portID, channelID string, counterpartyChannelID string, counterpartyVersion string) error {
	return im.app.OnChanOpenAck(ctx, portID, channelID, counterpartyChannelID, counterpartyVersion)
}

func (im IBCMiddleware) OnChanOpenConfirm(ctx cosmos.Context, portID, channelID string) error {
	return im.app.OnChanOpenConfirm(ctx, portID, channelID)
}

func (im IBCMiddleware) OnChanCloseInit(ctx cosmos.Context, portID, channelID string) error {
	return im.app.OnChanCloseInit(ctx, portID, channelID)
}

func (im IBCMiddleware) OnChanCloseConfirm(ctx cosmos.Context, portID, channelID string) error {
	return im.app.OnChanCloseConfirm(ctx, portID, channelID)
}

func (im IBCMiddleware) OnRecvPacket(ctx cosmos.Context, packet channeltypes.Packet, relayer cosmos.AccAddress) ibcexported.Acknowledgement {
	return im.app.OnRecvPacket(ctx, packet, relayer)
}

func (im IBCMiddleware) OnTimeoutPacket(ctx cosmos.Context, packet channeltypes.Packet, relayer cosmos.AccAddress) error {
	return im.app.OnTimeoutPacket(ctx, packet, relayer)
}
