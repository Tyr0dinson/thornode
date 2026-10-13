package common

import (
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcutil/bech32"
)

// encodeWitness returns a bech32m (witness v1+) or bech32 (v0) address for an
// arbitrary program, so malformed forms can be built for negative tests.
func encodeWitness(t *testing.T, hrp string, version byte, program []byte) string {
	t.Helper()
	converted, err := bech32.ConvertBits(program, 8, 5, true)
	if err != nil {
		t.Fatal(err)
	}
	data := append([]byte{version}, converted...)
	var addr string
	if version == 0 {
		addr, err = bech32.Encode(hrp, data)
	} else {
		addr, err = bech32.EncodeM(hrp, data)
	}
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestNewAddressLTCTaprootStrict(t *testing.T) {
	hrp := "ltc"
	if CurrentChainNetwork == MockNet {
		hrp = "rltc"
	}
	program, err := hex.DecodeString(taprootVectorXOnly)
	if err != nil {
		t.Fatal(err)
	}

	valid := expectedTaprootVectorAddress()
	addr, err := NewAddress(valid)
	if err != nil {
		t.Fatalf("valid taproot address rejected on %v: %v", CurrentChainNetwork, err)
	}
	if !addr.IsChain(LTCChain) {
		t.Fatalf("%s not recognised as LTC", valid)
	}
	if !LTCChain.SupportsTaprootVaults() {
		// The strict routing is gated with the rest of the Taproot vault work;
		// other builds keep the generic bech32 acceptance for every form.
		t.Logf("strict LTC taproot validation is not enabled on %v builds", CurrentChainNetwork)
		return
	}

	// Bad checksum: flip the last character.
	last := valid[len(valid)-1]
	flipped := "q"
	if last == 'q' {
		flipped = "p"
	}
	if _, rejectErr := NewAddress(valid[:len(valid)-1] + flipped); rejectErr == nil {
		t.Fatal("taproot address with a bad checksum was accepted")
	}
	// Witness v1 with a 20-byte program is not a valid taproot output.
	if _, rejectErr := NewAddress(encodeWitness(t, hrp, 1, program[:20])); rejectErr == nil {
		t.Fatal("witness v1 address with a 20-byte program was accepted")
	}
	// Witness v2 is not defined.
	if _, rejectErr := NewAddress(encodeWitness(t, hrp, 2, program)); rejectErr == nil {
		t.Fatal("witness v2 address was accepted")
	}
	// Witness v1 encoded with plain bech32 instead of bech32m is invalid.
	converted, err := bech32.ConvertBits(program, 8, 5, true)
	if err != nil {
		t.Fatal(err)
	}
	wrongEncoding, err := bech32.Encode(hrp, append([]byte{1}, converted...))
	if err != nil {
		t.Fatal(err)
	}
	if _, rejectErr := NewAddress(wrongEncoding); rejectErr == nil {
		t.Fatal("witness v1 address with a bech32 checksum was accepted")
	}
}
