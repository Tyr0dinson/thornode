package generic

import (
	"crypto/ecdsa"
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/codec"
	ecrypto "github.com/ethereum/go-ethereum/crypto"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/evm"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
)

type KeyManagerSecp256k1 struct {
	privKey *ecdsa.PrivateKey
	pubKey  common.PubKey
	address common.Address
}

func NewKeyManagerSecp256k1(
	keys *thorclient.Keys,
	chain common.Chain,
) (*KeyManagerSecp256k1, error) {
	priv, err := keys.GetPrivateKey()
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

	evmPrivateKey, err := evm.GetPrivateKey(priv)
	if err != nil {
		return nil, err
	}

	address, err := pub.GetAddress(chain)
	if err != nil {
		return nil, fmt.Errorf("failed to create address: %w", err)
	}

	return &KeyManagerSecp256k1{
		privKey: evmPrivateKey,
		pubKey:  pub,
		address: address,
	}, nil
}

func (m *KeyManagerSecp256k1) Sign(msg []byte) ([]byte, error) {
	return ecrypto.Sign(msg, m.privKey)
}

func (m *KeyManagerSecp256k1) PubKey() common.PubKey {
	return m.pubKey
}

func (m *KeyManagerSecp256k1) Address() common.Address {
	return m.address
}
