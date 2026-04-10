package generic

import (
	"fmt"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
)

type LocalKeyManager interface {
	Sign([]byte) ([]byte, error)
	PubKey() common.PubKey
	Address() common.Address
}

func NewLocalKeyManager(
	keys *thorclient.Keys,
	chain common.Chain,
) (LocalKeyManager, error) {
	algo := chain.GetSigningAlgo()
	switch algo {
	case common.SigningAlgoSecp256k1:
		return NewKeyManagerSecp256k1(keys, chain)
	case common.SigningAlgoEd25519:
		return NewKeyManagerEd25519(keys, chain)
	}

	return nil, fmt.Errorf("unsupported signing algorithm: %s", algo)
}
