package tss

import (
	"gitlab.com/thorchain/binance-sdk/types/tx"

	"gitlab.com/thorchain/thornode/common"
)

type ThorchainKeyManager interface {
	Sign(tx.StdSignMsg) ([]byte, error)
	SignWithPool(msg tx.StdSignMsg, poolPubKey common.PubKey) ([]byte, error)
	RemoteSign(msg []byte, poolPubKey string) ([]byte, []byte, error)
	Start()
	Stop()
}
