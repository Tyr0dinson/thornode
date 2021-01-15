package app

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	tsigning "github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/signing"
)

type signModeHandlerWrapper struct {
	origin signing.SignModeHandler
}

func (w signModeHandlerWrapper) DefaultMode() tsigning.SignMode {
	return w.origin.DefaultMode()
}

// Modes is the list of modes supporting by this handler
func (w signModeHandlerWrapper) Modes() []tsigning.SignMode {
	return w.origin.Modes()
}

// GetSignBytes returns the sign bytes for the provided SignMode, SignerData and Tx,
// or an error
func (w signModeHandlerWrapper) GetSignBytes(mode tsigning.SignMode, data signing.SignerData, tx sdk.Tx) ([]byte, error) {
	buf, err := w.origin.GetSignBytes(mode, data, tx)
	fmt.Printf("signing bytes:%s\n", string(buf))
	return buf, err
}
