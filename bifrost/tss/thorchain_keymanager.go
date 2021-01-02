package tss

import (
	"github.com/tendermint/tendermint/crypto"
	bcommon "gitlab.com/thorchain/thornode/bifrost/common"
)

// ThorchainKeyManager it is a composite of binance chain keymanager
type ThorchainKeyManager interface {
	Sign(bcommon.StdSignMsg) ([]byte, error)
	GetPrivKey() crypto.PrivKey
	RemoteSign(msg []byte, poolPubKey string) ([]byte, error)
}
