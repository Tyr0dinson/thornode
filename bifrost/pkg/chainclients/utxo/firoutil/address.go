// Package firoutil implements FIRO's transparent addresses, script templates,
// and signing of ordinary transactions spending exchange vault outputs. It does
// not register Bitcoin network prefixes or implement a general FIRO VM.
package firoutil

import (
	"bytes"
	"fmt"

	"github.com/btcsuite/btcd/btcec"
	"github.com/btcsuite/btcutil"
	"github.com/btcsuite/btcutil/base58"
)

type Network string

const (
	MainNet        Network = "main"
	TestNet        Network = "test"
	RegTest        Network = "regtest"
	OpExchangeAddr byte    = 0xe0
)

type Kind uint8

const (
	PubKeyHash Kind = iota
	ScriptHash
	Exchange
)

// Address retains the script kind: exchange and ordinary P2PKH addresses with
// the same key hash are different destinations, not interchangeable encodings.
type Address struct {
	network Network
	kind    Kind
	hash    [20]byte
}

func prefix(network Network, kind Kind) ([]byte, error) {
	var prefixes [3][]byte
	switch network {
	case MainNet:
		prefixes = [3][]byte{{82}, {7}, {0x01, 0xb9, 0xbb}}
	case TestNet:
		prefixes = [3][]byte{{65}, {178}, {0x01, 0xb9, 0xb1}}
	case RegTest:
		prefixes = [3][]byte{{65}, {178}, {0x01, 0xb9, 0xac}}
	default:
		return nil, fmt.Errorf("unsupported FIRO network %q", network)
	}
	if kind > Exchange {
		return nil, fmt.Errorf("unsupported FIRO address kind %d", kind)
	}
	return prefixes[kind], nil
}

func NewAddress(hash []byte, kind Kind, network Network) (*Address, error) {
	if len(hash) != 20 {
		return nil, fmt.Errorf("FIRO address hash must have 20 bytes")
	}
	_, err := prefix(network, kind)
	if err != nil {
		return nil, err
	}
	a := &Address{network: network, kind: kind}
	copy(a.hash[:], hash)
	return a, nil
}

func NewExchangeAddress(publicKey []byte, network Network) (*Address, error) {
	key, err := btcec.ParsePubKey(publicKey, btcec.S256())
	if err != nil {
		return nil, fmt.Errorf("invalid FIRO public key: %w", err)
	}
	return NewAddress(btcutil.Hash160(key.SerializeCompressed()), Exchange, network)
}

func DecodeAddress(value string, network Network) (*Address, error) {
	// Bound base58 work and reject bare public keys before decoding.
	if len(value) < 26 || len(value) > 40 {
		return nil, fmt.Errorf("invalid FIRO address length")
	}
	payload, version, err := base58.CheckDecode(value)
	if err != nil {
		return nil, fmt.Errorf("invalid FIRO address checksum: %w", err)
	}
	if len(payload) != 20 && len(payload) != 22 {
		return nil, fmt.Errorf("invalid FIRO address payload")
	}
	if base58.CheckEncode(payload, version) != value {
		return nil, fmt.Errorf("invalid FIRO address checksum")
	}
	// Base58Check separates the first prefix byte; restore the full FIRO prefix.
	body := append([]byte{version}, payload...)
	for _, kind := range []Kind{PubKeyHash, ScriptHash, Exchange} {
		p, err := prefix(network, kind)
		if err != nil {
			return nil, err
		}
		if len(body) == len(p)+20 && bytes.Equal(body[:len(p)], p) {
			return NewAddress(body[len(p):], kind, network)
		}
	}
	return nil, fmt.Errorf("FIRO address does not belong to %s", network)
}

func (a Address) String() string {
	p, err := prefix(a.network, a.kind)
	if err != nil {
		return ""
	}
	// Any remaining prefix bytes belong to the Base58Check payload.
	payload := p[1:]
	payload = append(payload, a.hash[:]...)
	return base58.CheckEncode(payload, p[0])
}

func (a Address) Kind() Kind { return a.kind }

func (a Address) Script() []byte {
	if a.kind == ScriptHash {
		return append(append([]byte{0xa9, 0x14}, a.hash[:]...), 0x87)
	}
	script, _ := PayToPubKeyHashScript(a.hash[:])
	if a.kind == Exchange {
		return append([]byte{OpExchangeAddr}, script...)
	}
	return script
}

func PayToPubKeyHashScript(hash []byte) ([]byte, error) {
	if len(hash) != 20 {
		return nil, fmt.Errorf("FIRO P2PKH hash must have 20 bytes")
	}
	return append(append([]byte{0x76, 0xa9, 0x14}, hash...), 0x88, 0xac), nil
}

// AddressFromScript accepts only the exact transparent templates supported by
// the integration. In particular, bare multisig and Spark scripts are rejected.
func AddressFromScript(script []byte, network Network) (*Address, error) {
	kind := PubKeyHash
	if len(script) == 26 && script[0] == OpExchangeAddr {
		kind, script = Exchange, script[1:]
	}
	if len(script) == 25 && bytes.Equal(script[:3], []byte{0x76, 0xa9, 0x14}) && bytes.Equal(script[23:], []byte{0x88, 0xac}) {
		return NewAddress(script[3:23], kind, network)
	}
	if len(script) == 23 && bytes.Equal(script[:2], []byte{0xa9, 0x14}) && script[22] == 0x87 {
		return NewAddress(script[2:22], ScriptHash, network)
	}
	return nil, fmt.Errorf("unsupported FIRO script")
}
