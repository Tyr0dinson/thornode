package firoutil

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/btcsuite/btcutil/base58"
)

func TestAddressVectors(t *testing.T) {
	// secp256k1 generator, compressed. Encodings independently calculated
	// from FIRO Core's chainparams prefixes and double-SHA256 checksums.
	key, _ := hex.DecodeString("0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	hash, _ := hex.DecodeString("751e76e8199196d454941c45d1b3a323f1433bd6")
	for _, tc := range []struct {
		network Network
		kind    Kind
		address string
	}{
		{MainNet, PubKeyHash, "aBPjJ4LEarrcCrd6EBRTb8jVjUZuHQFVnD"},
		{MainNet, ScriptHash, "413VSuydLL6tsMAbPiSZDjKVXeFA1YR19z"},
		{MainNet, Exchange, "EXXR93ZYdE5bWcSpkEu9vW5Usv5wUqgFqiSi"},
		{TestNet, PubKeyHash, "TLeUZDGLWnyiJVFcp3m3M1782uBsGWa8uf"},
		{TestNet, ScriptHash, "2EoqdpUwtkBLiiU1QaTQ49As29uJTaepbNx"},
		{TestNet, Exchange, "EXTPm1iTeLynsrGVMNexbKEEA3ntuQZyccPo"},
		{RegTest, PubKeyHash, "TLeUZDGLWnyiJVFcp3m3M1782uBsGWa8uf"},
		{RegTest, ScriptHash, "2EoqdpUwtkBLiiU1QaTQ49As29uJTaepbNx"},
		{RegTest, Exchange, "EXRP4znv9uRtZUBKewXrviobo7eNcgwUb87C"},
	} {
		t.Run(tc.address, func(t *testing.T) {
			a, err := NewAddress(hash, tc.kind, tc.network)
			if err != nil || a.String() != tc.address {
				t.Fatalf("encoding: %v, %v", a, err)
			}
			decoded, err := DecodeAddress(tc.address, tc.network)
			if err != nil || decoded.Kind() != tc.kind || !bytes.Equal(decoded.Script(), a.Script()) {
				t.Fatalf("decoding: %v, %v", decoded, err)
			}
			fromScript, err := AddressFromScript(a.Script(), tc.network)
			if err != nil || fromScript.String() != tc.address {
				t.Fatalf("script round trip: %v, %v", fromScript, err)
			}
			if tc.kind == Exchange {
				fromKey, err := NewExchangeAddress(key, tc.network)
				if err != nil || fromKey.String() != tc.address {
					t.Fatalf("key derivation: %v, %v", fromKey, err)
				}
				for _, wrong := range []Network{MainNet, TestNet, RegTest} {
					if wrong != tc.network {
						_, err := DecodeAddress(tc.address, wrong)
						if err == nil {
							t.Fatal("accepted wrong network")
						}
					}
				}
			}
			badChecksum := base58.Decode(tc.address)
			badChecksum[len(badChecksum)-1] ^= 1
			for _, bad := range []string{strings.ToLower(tc.address), tc.address + "1", "1" + tc.address, tc.address[:len(tc.address)-1] + "0", base58.Encode(badChecksum)} {
				_, err := DecodeAddress(bad, tc.network)
				if err == nil {
					t.Fatalf("accepted %q", bad)
				}
			}
		})
	}
}

func TestRejectInvalidAddressPayloads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prefix  []byte
		hashLen int
	}{
		{"short ordinary hash", []byte{82}, 19},
		{"long ordinary hash", []byte{82}, 21},
		{"ordinary prefix with exchange payload length", []byte{82}, 22},
		{"short exchange hash", []byte{0x01, 0xb9, 0xbb}, 19},
		{"long exchange hash", []byte{0x01, 0xb9, 0xbb}, 21},
		{"unknown exchange prefix", []byte{0x01, 0xb9, 0xbc}, 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A valid checksum must not make an invalid prefix or hash length acceptable.
			body := append([]byte(nil), tc.prefix...)
			body = append(body, bytes.Repeat([]byte{1}, tc.hashLen)...)
			value := base58.CheckEncode(body[1:], body[0])
			_, err := DecodeAddress(value, MainNet)
			if err == nil {
				t.Fatalf("accepted %q", value)
			}
		})
	}
}

func TestRejectUnsupportedScripts(t *testing.T) {
	a, _ := NewAddress(make([]byte, 20), Exchange, MainNet)
	for _, script := range [][]byte{nil, {0xe0}, append(a.Script(), 0x61), append([]byte{0xe0}, a.Script()...), append([]byte{0, 20}, make([]byte, 20)...)} {
		_, err := AddressFromScript(script, MainNet)
		if err == nil {
			t.Fatalf("accepted %x", script)
		}
	}
}
