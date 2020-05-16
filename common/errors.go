package common

import se "github.com/cosmos/cosmos-sdk/types/errors"

func ErrUnknownRequest(msg string) error {
	return se.Wrap(se.ErrUnknownRequest, msg)
}
func ErrInvalidAddress(addr string) error {
	return se.Wrap(se.ErrInvalidAddress, addr)
}
func ErrInvalidCoins(msg string) error {
	return se.Wrap(se.ErrInvalidCoins, msg)
}
