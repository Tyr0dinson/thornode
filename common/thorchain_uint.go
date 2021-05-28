package common

import (
	"gitlab.com/thorchain/thornode/common/cosmos"
)

type Uint struct {
	cosmos.Uint
}

func (u Uint) IsInitialized() bool {
	if len(u.BigInt().Bytes()) == 0 {
		return true
	}
	return false
}

// MarshalTo implements the gogo proto custom type interface.
func (u *Uint) MarshalTo(data []byte) (int, error) {
	if u == nil {
		return 0, nil
	}
	buf := make([]byte, 0, len(data))
	n, err := u.Uint.MarshalTo(buf)
	if err != nil {
		return 0, err
	}
	if n == 1 && buf[0] == 0x30 {
		return 0, nil
	}
	copy(data, buf)
	return n, nil
}
func ZeroUint() Uint {
	return Uint{
		Uint: cosmos.ZeroUint(),
	}
}
