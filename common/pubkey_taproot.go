package common

import (
	"bytes"
	"errors"
	"fmt"

	sdkbech32 "github.com/cosmos/cosmos-sdk/types/bech32"
	ltcchaincfg "github.com/ltcsuite/ltcd/chaincfg"

	ltctxscript "gitlab.com/thorchain/thornode/v3/bifrost/txscript/ltcd-txscript"
)

// Amino type prefixes of the legacy bech32 pubkey encoding (type tag followed
// by the key length), used to tell the curve of a key from its bytes alone.
var (
	aminoSecp256k1PubKeyPrefix = []byte{0xeb, 0x5a, 0xe9, 0x87, 0x21}
	aminoEd25519PubKeyPrefix   = []byte{0x16, 0x24, 0xde, 0x64, 0x20}
)

// KeyAlgo reports the curve of the key: SigningAlgoSecp256k1 or
// SigningAlgoEd25519. It does not say how the key is used; a secp256k1 vault
// key may be an ECDSA key or a Taproot (FROST) key, which is what the
// SigningAlgoSecp256k1Taproot tag carried next to the key records.
//
// The curve is read from the bech32 payload's amino type prefix, so this never
// touches the SDK's global bech32 configuration. It runs on every pubkey
// refresh in bifrost and on observed-transaction validation, and must not
// share mutable state with code that configures prefixes.
func (p PubKey) KeyAlgo() (SigningAlgo, error) {
	if p.IsEmpty() {
		return "", errors.New("empty pubkey")
	}
	_, payload, err := sdkbech32.DecodeAndConvert(string(p))
	if err != nil {
		return "", err
	}
	switch {
	case bytes.HasPrefix(payload, aminoSecp256k1PubKeyPrefix) && len(payload) == len(aminoSecp256k1PubKeyPrefix)+33:
		return SigningAlgoSecp256k1, nil
	case bytes.HasPrefix(payload, aminoEd25519PubKeyPrefix) && len(payload) == len(aminoEd25519PubKeyPrefix)+32:
		return SigningAlgoEd25519, nil
	default:
		return "", fmt.Errorf("unsupported pubkey encoding (%d bytes)", len(payload))
	}
}

// GetAddressForAlgo returns the address of this key on chain when the key is
// used under algo. For SigningAlgoSecp256k1 and SigningAlgoEd25519 it is
// GetAddress. For SigningAlgoSecp256k1Taproot the key must be a FROST vault
// key, which is the already-tweaked BIP-341 output key in compressed form, and
// the address is the chain's P2TR encoding of its x-only form. The key is used
// as is and is never tweaked again.
func (p PubKey) GetAddressForAlgo(chain Chain, algo SigningAlgo) (Address, error) {
	if p.IsEmpty() {
		return NoAddress, nil
	}
	switch algo {
	case SigningAlgoSecp256k1, SigningAlgoEd25519:
		return p.GetAddress(chain)
	case SigningAlgoSecp256k1Taproot:
		return p.getTaprootAddress(chain)
	default:
		return NoAddress, fmt.Errorf("unsupported signing algorithm %q", algo)
	}
}

// getTaprootAddress encodes the key's x coordinate as the chain's witness v1
// (bech32m) address. The key must already be a Taproot output key.
func (p PubKey) getTaprootAddress(chain Chain) (Address, error) {
	if !chain.SupportsTaprootVaults() {
		return NoAddress, fmt.Errorf("taproot vault addresses are not supported for %s in this build", chain)
	}

	key := fmt.Sprintf("%s-%s-%s", chain.String(), SigningAlgoSecp256k1Taproot, p.String())
	pubkeyToAddressCacheMu.RLock()
	if v, ok := pubkeyToAddressCache[key]; ok {
		pubkeyToAddressCacheMu.RUnlock()
		return v, nil
	}
	pubkeyToAddressCacheMu.RUnlock()

	pk, err := p.Secp256K1()
	if err != nil {
		return NoAddress, fmt.Errorf("taproot vault key is not a secp256k1 key: %w", err)
	}
	// The x-only output key is the compressed encoding without its parity byte.
	outputKeyXOnly := pk.SerializeCompressed()[1:]

	var net *ltcchaincfg.Params
	switch CurrentChainNetwork {
	case MockNet:
		net = &ltcchaincfg.RegressionNetParams
	case MainNet, StageNet, ChainNet:
		net = &ltcchaincfg.MainNetParams
	default:
		return NoAddress, fmt.Errorf("unsupported network %v for taproot addresses", CurrentChainNetwork)
	}
	addr, err := ltctxscript.NewAddressTaproot(outputKeyXOnly, net)
	if err != nil {
		return NoAddress, fmt.Errorf("fail to encode taproot address: %w", err)
	}
	address, err := NewAddress(addr.String())
	if err != nil {
		return address, fmt.Errorf("NewAddress, addressString %s, %w", addr.String(), err)
	}

	pubkeyToAddressCacheMu.Lock()
	pubkeyToAddressCache[key] = address
	pubkeyToAddressCacheMu.Unlock()
	return address, nil
}

// GetAddresses returns every address this key can own on chain in this build:
// the GetAddress form and, where the chain supports Taproot vaults and the key
// is secp256k1, the Taproot form. Callers that know the key's algorithm should
// use GetAddressForAlgo instead; this is for validation sites that only hold a
// bare key, such as observed-transaction checks.
func (p PubKey) GetAddresses(chain Chain) ([]Address, error) {
	if p.IsEmpty() {
		return nil, nil
	}
	legacy, err := p.GetAddress(chain)
	if err != nil {
		return nil, err
	}
	addresses := []Address{legacy}
	if !chain.SupportsTaprootVaults() {
		return addresses, nil
	}
	if algo, algoErr := p.KeyAlgo(); algoErr != nil || algo != SigningAlgoSecp256k1 {
		return addresses, nil
	}
	taproot, err := p.getTaprootAddress(chain)
	if err != nil {
		return nil, err
	}
	return append(addresses, taproot), nil
}

// HasAddress reports whether addr is one of the key's addresses on chain (see
// GetAddresses), compared with the chain's address identity rules.
func (p PubKey) HasAddress(chain Chain, addr Address) bool {
	addresses, err := p.GetAddresses(chain)
	if err != nil {
		return false
	}
	for _, candidate := range addresses {
		if AddressEquals(chain, candidate, addr) {
			return true
		}
	}
	return false
}
