package types

import (
	"context"
	fmt "fmt"
	"strings"

	"cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
)

const (
	ModuleDenomPrefix = "factory"
	delimiter         = "/"
)

// GetTokenDenom constructs a denom string for tokens created by denom
// based on an input creator address and a id
// The denom constructed is factory/{creator}/{id}
func GetTokenDenom(creator, id string) (string, error) {
	if strings.Contains(creator, delimiter) {
		return "", ErrInvalidCreator
	}
	denom := strings.Join([]string{ModuleDenomPrefix, creator, id}, delimiter)
	return denom, sdk.ValidateDenom(denom)
}

// DeconstructDenom takes a token denom string and verifies that it is a valid
// denom of the denom module, and is of the form `factory/{creator}/{id}`
// If valid, it returns the creator address and id
func DeconstructDenom(denom string) (creator string, id string, err error) {
	err = sdk.ValidateDenom(denom)
	if err != nil {
		return "", "", err
	}

	strParts := strings.Split(denom, delimiter)
	if len(strParts) < 3 {
		return "", "", errors.Wrapf(ErrInvalidDenom, "not enough parts of denom %s", denom)
	}

	if strParts[0] != ModuleDenomPrefix {
		return "", "", errors.Wrapf(ErrInvalidDenom, "denom prefix is incorrect. Is: %s.  Should be: %s", strParts[0], ModuleDenomPrefix)
	}

	creator = strParts[1]
	_, err = sdk.AccAddressFromBech32(creator)
	if err != nil {
		return "", "", errors.Wrapf(ErrInvalidDenom, "Invalid creator address (%s)", err)
	}

	// Handle the case where a denom has the delimiter in its id. For example,
	// when we did the split, we'd turn factory/sunnyaddr/atomderivative/sikka into ["factory", "sunnyaddr", "atomderivative", "sikka"]
	// So we have to join [2:] with a delimiter to get back the correct id which should be "atomderivative/sikka"
	id = strings.Join(strParts[2:], delimiter)

	return creator, id, nil
}

// NewdenomDenomMintCoinsRestriction creates and returns a BankMintingRestrictionFn that only allows minting of
// valid denom denoms
func NewdenomDenomMintCoinsRestriction() banktypes.MintingRestrictionFn {
	return func(_ context.Context, coinsToMint sdk.Coins) error {
		for _, coin := range coinsToMint {
			_, _, err := DeconstructDenom(coin.Denom)
			if err != nil {
				return fmt.Errorf("does not have permission to mint %s", coin.Denom)
			}
		}
		return nil
	}
}
