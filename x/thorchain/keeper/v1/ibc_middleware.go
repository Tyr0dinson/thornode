package keeperv1

import (
	"gitlab.com/thorchain/thornode/common/cosmos"
	"gitlab.com/thorchain/thornode/x/thorchain/keeper"

	errorsmod "cosmossdk.io/errors"
	"github.com/cosmos/cosmos-sdk/types/errors"
	capabilitytypes "github.com/cosmos/ibc-go/modules/capability/types"
	clienttypes "github.com/cosmos/ibc-go/v8/modules/core/02-client/types"
	channeltypes "github.com/cosmos/ibc-go/v8/modules/core/04-channel/types"
	porttypes "github.com/cosmos/ibc-go/v8/modules/core/05-port/types"
	ibcexported "github.com/cosmos/ibc-go/v8/modules/core/exported"
)

var (
	_                     porttypes.ICS4Wrapper = (*KVStore)(nil)
	_                     porttypes.IBCModule   = (*IBCModule)(nil)
	MIMIR_IBC_CONNECTION                        = "IBCENABLECONN"
	MIMIR_IBC_PORT                              = "IBCENABLEPORT"
	MIMIR_IBC_GLOBAL_HALT                       = "IBCHALTGLOBAL"
)

type IBCModule struct {
	app    porttypes.IBCModule
	keeper keeper.Keeper
}

func NewIBCModule(
	app porttypes.IBCModule,
	keeper keeper.Keeper,
) IBCModule {
	return IBCModule{
		app:    app,
		keeper: keeper,
	}
}

func (k KVStore) SendPacket(
	ctx cosmos.Context,
	chanCap *capabilitytypes.Capability,
	sourcePort string,
	sourceChannel string,
	timeoutHeight clienttypes.Height,
	timeoutTimestamp uint64,
	data []byte,
) (sequence uint64, err error) {
	if err := checkIBCHalt(k, ctx); err != nil {
		return 0, err
	}
	return k.ics4Wrapper.SendPacket(ctx, chanCap, sourcePort, sourceChannel, timeoutHeight, timeoutTimestamp, data)
}

func (k KVStore) WriteAcknowledgement(
	ctx cosmos.Context,
	chanCap *capabilitytypes.Capability,
	packet ibcexported.PacketI,
	ack ibcexported.Acknowledgement,
) error {
	if err := checkIBCHalt(k, ctx); err != nil {
		return err
	}
	return k.ics4Wrapper.WriteAcknowledgement(ctx, chanCap, packet, ack)
}

func (k KVStore) GetAppVersion(
	ctx cosmos.Context,
	portID,
	channelID string,
) (string, bool) {
	return k.ics4Wrapper.GetAppVersion(ctx, portID, channelID)
}

func (im IBCModule) OnChanOpenInit(
	ctx cosmos.Context,
	channelOrdering channeltypes.Order,
	connectionHops []string,
	portID,
	channelID string,
	channelCap *capabilitytypes.Capability,
	counterparty channeltypes.Counterparty,
	version string,
) (string, error) {
	if err := checkIBCHalt(im.keeper, ctx); err != nil {
		return "", err
	}
	if err := validateIBCChannel(im.keeper, ctx, connectionHops[0], portID); err != nil {
		return "", err
	}
	return im.app.OnChanOpenInit(ctx, channelOrdering, connectionHops, portID, channelID, channelCap, counterparty, version)
}

func (im IBCModule) OnChanOpenTry(
	ctx cosmos.Context,
	channelOrdering channeltypes.Order,
	connectionHops []string,
	portID,
	channelID string,
	channelCap *capabilitytypes.Capability,
	counterparty channeltypes.Counterparty,
	counterpartyVersion string,
) (string, error) {
	if err := checkIBCHalt(im.keeper, ctx); err != nil {
		return "", err
	}
	if err := validateIBCChannel(im.keeper, ctx, connectionHops[0], portID); err != nil {
		return "", err
	}
	return im.app.OnChanOpenTry(ctx, channelOrdering, connectionHops, portID, channelID, channelCap, counterparty, counterpartyVersion)
}

func (im IBCModule) OnChanOpenAck(ctx cosmos.Context, portID, channelID string, counterpartyChannelID string, counterpartyVersion string) error {
	if err := checkIBCHalt(im.keeper, ctx); err != nil {
		return err
	}
	return im.app.OnChanOpenAck(ctx, portID, channelID, counterpartyChannelID, counterpartyVersion)
}

func (im IBCModule) OnChanOpenConfirm(ctx cosmos.Context, portID, channelID string) error {
	if err := checkIBCHalt(im.keeper, ctx); err != nil {
		return err
	}
	return im.app.OnChanOpenConfirm(ctx, portID, channelID)
}

func (im IBCModule) OnChanCloseInit(ctx cosmos.Context, portID, channelID string) error {
	if err := checkIBCHalt(im.keeper, ctx); err != nil {
		return err
	}
	return im.app.OnChanCloseInit(ctx, portID, channelID)
}

func (im IBCModule) OnChanCloseConfirm(ctx cosmos.Context, portID, channelID string) error {
	if err := checkIBCHalt(im.keeper, ctx); err != nil {
		return err
	}
	return im.app.OnChanCloseConfirm(ctx, portID, channelID)
}

func (im IBCModule) OnRecvPacket(ctx cosmos.Context, packet channeltypes.Packet, relayer cosmos.AccAddress) ibcexported.Acknowledgement {
	// if err := checkIBCHalt(im.keeper, ctx); err != nil {
	// 	return err
	// }
	return im.app.OnRecvPacket(ctx, packet, relayer)
}

func (im IBCModule) OnTimeoutPacket(ctx cosmos.Context, packet channeltypes.Packet, relayer cosmos.AccAddress) error {
	if err := checkIBCHalt(im.keeper, ctx); err != nil {
		return err
	}
	return im.app.OnTimeoutPacket(ctx, packet, relayer)
}

func (im IBCModule) OnAcknowledgementPacket(
	ctx cosmos.Context,
	packet channeltypes.Packet,
	acknowledgement []byte,
	relayer cosmos.AccAddress,
) error {
	if err := checkIBCHalt(im.keeper, ctx); err != nil {
		return err
	}
	return im.app.OnAcknowledgementPacket(ctx, packet, acknowledgement, relayer)
}

func checkIBCHalt(k keeper.Keeper, ctx cosmos.Context) error {
	v, err := k.GetMimir(ctx, MIMIR_IBC_GLOBAL_HALT)
	if err != nil {
		return err
	}
	if v != 0 && ctx.BlockHeight() > v {
		return errors.ErrUnauthorized
	}
	return nil
}

func validateIBCChannel(k keeper.Keeper, ctx cosmos.Context, connectionEnd, portID string) error {
	v, err := k.GetMimir(ctx, MIMIR_IBC_CONNECTION+connectionEnd)
	if err != nil {
		return err
	}
	if v != 0 && ctx.BlockHeight() < v {
		return errorsmod.Wrap(errors.ErrUnauthorized, "connection not enabled")
	}

	v, err = k.GetMimir(ctx, MIMIR_IBC_PORT+portID)
	if err != nil {
		return err
	}
	if v != 0 && ctx.BlockHeight() < v {
		return errorsmod.Wrap(errors.ErrUnauthorized, "port not enabled")
	}
	return nil
}
