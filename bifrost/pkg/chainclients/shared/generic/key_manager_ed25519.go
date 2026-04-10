package generic

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
)

type KeyManagerEd25519 struct {
	privKey *ed25519.PrivKey
	pubKey  common.PubKey
	address common.Address
}

func NewKeyManagerEd25519(
	keys *thorclient.Keys,
	chain common.Chain,
) (*KeyManagerEd25519, error) {
	priv, err := keys.GetPrivateKeyEDDSA()
	if err != nil {
		return nil, fmt.Errorf("failed to get private key: %w", err)
	}

	temp, err := codec.ToCmtPubKeyInterface(priv.PubKey())
	if err != nil {
		return nil, fmt.Errorf("failed to get tm pub key: %w", err)
	}

	pub, err := common.NewPubKeyFromCrypto(temp)
	if err != nil {
		return nil, fmt.Errorf("failed to get pub key: %w", err)
	}

	address, err := pub.GetAddress(chain)
	if err != nil {
		return nil, fmt.Errorf("failed to create address: %w", err)
	}

	return &KeyManagerEd25519{
		privKey: priv,
		pubKey:  pub,
		address: address,
	}, nil
}

func (m *KeyManagerEd25519) Sign(msg []byte) ([]byte, error) {
	return m.privKey.Sign(msg)
}

func (m *KeyManagerEd25519) PubKey() common.PubKey {
	return m.pubKey
}

func (m *KeyManagerEd25519) Address() common.Address {
	return m.address
}
