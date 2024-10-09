package thorchain

import (
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"

	"github.com/cosmos/cosmos-sdk/types/errors"
	capabilitytypes "github.com/cosmos/ibc-go/modules/capability/types"
	channeltypes "github.com/cosmos/ibc-go/v8/modules/core/04-channel/types"
	porttypes "github.com/cosmos/ibc-go/v8/modules/core/05-port/types"
	ibcexported "github.com/cosmos/ibc-go/v8/modules/core/exported"
)

var (
	_                     porttypes.IBCModule = (*IBCMiddleware)(nil)
	MIMIR_IBC_CONNECTION                      = "IBCENABLECONN"
	MIMIR_IBC_PORT                            = "IBCENABLEPORT"
	MIMIR_IBC_GLOBAL_HALT                     = "IBCHALTGLOBAL"
)

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
	if err := im.checkHalt(ctx); err != nil {
		return "", err
	}
	if err := im.validateChannel(ctx, connectionHops[0], portID); err != nil {
		return "", err
	}
	return im.app.OnChanOpenInit(ctx, channelOrdering, connectionHops, portID, channelID, channelCap, counterparty, version)
}

func (im IBCMiddleware) OnChanOpenTry(
	ctx cosmos.Context,
	channelOrdering channeltypes.Order,
	connectionHops []string,
	portID,
	channelID string,
	channelCap *capabilitytypes.Capability,
	counterparty channeltypes.Counterparty,
	counterpartyVersion string,
) (string, error) {
	if err := im.checkHalt(ctx); err != nil {
		return "", err
	}
	if err := im.validateChannel(ctx, connectionHops[0], portID); err != nil {
		return "", err
	}
	return im.app.OnChanOpenTry(ctx, channelOrdering, connectionHops, portID, channelID, channelCap, counterparty, counterpartyVersion)
}

func (im IBCMiddleware) OnChanOpenAck(ctx cosmos.Context, portID, channelID string, counterpartyChannelID string, counterpartyVersion string) error {
	if err := im.checkHalt(ctx); err != nil {
		return err
	}
	return im.app.OnChanOpenAck(ctx, portID, channelID, counterpartyChannelID, counterpartyVersion)
}

func (im IBCMiddleware) OnChanOpenConfirm(ctx cosmos.Context, portID, channelID string) error {
	if err := im.checkHalt(ctx); err != nil {
		return err
	}
	return im.app.OnChanOpenConfirm(ctx, portID, channelID)
}

func (im IBCMiddleware) OnChanCloseInit(ctx cosmos.Context, portID, channelID string) error {
	if err := im.checkHalt(ctx); err != nil {
		return err
	}
	return im.app.OnChanCloseInit(ctx, portID, channelID)
}

func (im IBCMiddleware) OnChanCloseConfirm(ctx cosmos.Context, portID, channelID string) error {
	if err := im.checkHalt(ctx); err != nil {
		return err
	}
	return im.app.OnChanCloseConfirm(ctx, portID, channelID)
}

func (im IBCMiddleware) OnRecvPacket(ctx cosmos.Context, packet channeltypes.Packet, relayer cosmos.AccAddress) ibcexported.Acknowledgement {
	// if err := im.checkHalt(ctx); err != nil {
	// 	return err
	// }
	return im.app.OnRecvPacket(ctx, packet, relayer)
}

func (im IBCMiddleware) OnTimeoutPacket(ctx cosmos.Context, packet channeltypes.Packet, relayer cosmos.AccAddress) error {
	if err := im.checkHalt(ctx); err != nil {
		return err
	}
	return im.app.OnTimeoutPacket(ctx, packet, relayer)
}

func (im IBCMiddleware) OnAcknowledgementPacket(
	ctx cosmos.Context,
	packet channeltypes.Packet,
	acknowledgement []byte,
	relayer cosmos.AccAddress,
) error {
	if err := im.checkHalt(ctx); err != nil {
		return err
	}
	return im.app.OnAcknowledgementPacket(ctx, packet, acknowledgement, relayer)
}

func (im IBCMiddleware) checkHalt(ctx cosmos.Context) error {
	v, err := im.keeper.GetMimir(ctx, MIMIR_IBC_GLOBAL_HALT)
	if err != nil {
		return err
	}
	if v != 0 && ctx.BlockHeight() > v {
		return errors.ErrUnauthorized
	}
	return nil
}

func (im IBCMiddleware) validateChannel(ctx cosmos.Context, connectionEnd, portID string) error {
	v, err := im.keeper.GetMimir(ctx, MIMIR_IBC_CONNECTION+connectionEnd)
	if err != nil {
		return err
	}
	if v != 0 && ctx.BlockHeight() < v {
		return wrapError(ctx, errors.ErrUnauthorized, "connection not enabled")
	}

	v, err = im.keeper.GetMimir(ctx, MIMIR_IBC_PORT+portID)
	if err != nil {
		return err
	}
	if v != 0 && ctx.BlockHeight() < v {
		return wrapError(ctx, errors.ErrUnauthorized, "port not enabled")
	}
	return nil
}
