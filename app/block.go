package app

import (
	"errors"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// BlockMsgDecorator checks for specific message types and blocks them
type BlockMsgDecorator struct {
	blockedMsgTypes map[string]bool
}

// NewBlockMsgDecorator creates a new BlockMsgDecorator with the specified message types
func NewBlockMsgDecorator(msgTypes ...string) BlockMsgDecorator {
	blockedTypes := make(map[string]bool)
	for _, msgType := range msgTypes {
		blockedTypes[msgType] = true
	}
	return BlockMsgDecorator{blockedMsgTypes: blockedTypes}
}

func (d BlockMsgDecorator) AnteHandle(
	ctx sdk.Context,
	tx sdk.Tx,
	simulate bool,
	next sdk.AnteHandler,
) (newCtx sdk.Context, err error) {
	for _, msg := range tx.GetMsgs() {
		if d.blockedMsgTypes[sdk.MsgTypeURL(msg)] {
			return ctx, errors.New("transaction contains a blocked message type")
		}
	}
	return next(ctx, tx, simulate)
}
