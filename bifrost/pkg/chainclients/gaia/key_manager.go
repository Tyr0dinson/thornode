package gaia

import (
	ctypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/types"

	"gitlab.com/thorchain/thornode/common"
	"gitlab.com/thorchain/thornode/common/cosmos"
)

type keyManager struct {
	privKey ctypes.PrivKey
	addr    types.AccAddress
	pubkey  common.PubKey
}

func (m *keyManager) Pubkey() common.PubKey {
	return m.pubkey
}

func (m *keyManager) Sign(msg []byte) ([]byte, error) {
	return m.privKey.Sign(msg)
}

func (m *keyManager) GetPrivKey() ctypes.PrivKey {
	return m.privKey
}

func (m *keyManager) GetAddr() cosmos.AccAddress {
	return m.addr
}
