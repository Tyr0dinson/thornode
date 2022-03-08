package app

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// SetupInfiniteGasMeterDecorator apply an infinite gas meter
type SetupInfiniteGasMeterDecorator struct{}

func NewSetupInfiniteGasMeterDecorator() SetupInfiniteGasMeterDecorator {
	return SetupInfiniteGasMeterDecorator{}
}

func (sud SetupInfiniteGasMeterDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (newCtx sdk.Context, err error) {
	newCtx = ctx.WithGasMeter(sdk.NewInfiniteGasMeter())
	return next(newCtx, tx, simulate)
}
