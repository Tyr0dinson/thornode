package types

import (
	"errors"
	"fmt"
)

type LiquidityProviders []LiquidityProvider

// Valid check whether lp represent valid information
func (lp LiquidityProvider) Valid() error {
	if lp.LastAddHeight == 0 {
		return errors.New("last add liquidity height cannot be empty")
	}
	if lp.AssetAddress.IsEmpty() && lp.RuneAddress.IsEmpty() {
		return errors.New("asset address and rune address cannot be empty")
	}
	return nil
}

// Key return a string which can be used to identify lp
func (lp LiquidityProvider) Key() string {
	return fmt.Sprintf("%s/%s", lp.Asset.String(), lp.RuneAddress.String())
}
