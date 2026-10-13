package common

import (
	"encoding/hex"
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	sdkbech32 "github.com/cosmos/cosmos-sdk/types/bech32"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

const (
	// BIP-341 scriptPubKey vector 0 output key (x-only), the same key used by
	// bifrost/txscript/ltcd-txscript/taproot_test.go.
	taprootVectorXOnly       = "53a1f6e454df1aa2776a2814a721372d6258050de330b3c6d10ee8f4e0dda343"
	taprootVectorAddrMainnet = "ltc1p2wsldez5mud2yam29q22wgfh9439spgduvct83k3pm50fcxa5dpshpe9c3"
	taprootVectorAddrRegtest = "rltc1p2wsldez5mud2yam29q22wgfh9439spgduvct83k3pm50fcxa5dpsxhprzv"
)

// taprootVectorPubKey returns the vector output key as a THORChain pubkey,
// the way a FROST vault key is stored: compressed, already tweaked.
func taprootVectorPubKey(t *testing.T) PubKey {
	t.Helper()
	x, err := hex.DecodeString(taprootVectorXOnly)
	if err != nil {
		t.Fatal(err)
	}
	pk := &secp256k1.PubKey{Key: append([]byte{0x02}, x...)}
	bech, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, pk)
	if err != nil {
		t.Fatal(err)
	}
	return PubKey(bech)
}

func randomEd25519PubKey(t *testing.T) PubKey {
	t.Helper()
	bech, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, ed25519.GenPrivKey().PubKey())
	if err != nil {
		t.Fatal(err)
	}
	return PubKey(bech)
}

func randomSecp256k1PubKey(t *testing.T) PubKey {
	t.Helper()
	bech, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, secp256k1.GenPrivKey().PubKey())
	if err != nil {
		t.Fatal(err)
	}
	return PubKey(bech)
}

func expectedTaprootVectorAddress() string {
	if CurrentChainNetwork == MockNet {
		return taprootVectorAddrRegtest
	}
	return taprootVectorAddrMainnet
}

func TestSupportsTaprootVaults(t *testing.T) {
	want := CurrentChainNetwork == StageNet || CurrentChainNetwork == MockNet
	if got := LTCChain.SupportsTaprootVaults(); got != want {
		t.Fatalf("LTC SupportsTaprootVaults = %v on %v, want %v", got, CurrentChainNetwork, want)
	}
	for _, chain := range []Chain{BTCChain, BCHChain, DOGEChain, ETHChain, XMRChain, SOLChain, THORChain, EmptyChain} {
		if chain.SupportsTaprootVaults() {
			t.Fatalf("%s must not support taproot vaults", chain)
		}
	}
}

func TestAcceptsSigningAlgo(t *testing.T) {
	cases := []struct {
		chain Chain
		algo  SigningAlgo
		want  bool
	}{
		{LTCChain, SigningAlgoSecp256k1, true},
		{LTCChain, SigningAlgoSecp256k1Taproot, LTCChain.SupportsTaprootVaults()},
		{LTCChain, SigningAlgoEd25519, false},
		{BTCChain, SigningAlgoSecp256k1, true},
		{BTCChain, SigningAlgoSecp256k1Taproot, false},
		{XMRChain, SigningAlgoEd25519, true},
		{XMRChain, SigningAlgoSecp256k1Taproot, false},
		{SOLChain, SigningAlgoEd25519, true},
		{SOLChain, SigningAlgoSecp256k1, false},
		{ETHChain, SigningAlgo("bogus"), false},
	}
	for _, tc := range cases {
		if got := tc.chain.AcceptsSigningAlgo(tc.algo); got != tc.want {
			t.Fatalf("%s accepts %s = %v, want %v", tc.chain, tc.algo, got, tc.want)
		}
	}
}

func TestKeyAlgo(t *testing.T) {
	if algo, err := randomSecp256k1PubKey(t).KeyAlgo(); err != nil || algo != SigningAlgoSecp256k1 {
		t.Fatalf("secp256k1 key algo = %s, %v", algo, err)
	}
	if algo, err := taprootVectorPubKey(t).KeyAlgo(); err != nil || algo != SigningAlgoSecp256k1 {
		t.Fatalf("taproot vault key algo = %s, %v", algo, err)
	}
	if algo, err := randomEd25519PubKey(t).KeyAlgo(); err != nil || algo != SigningAlgoEd25519 {
		t.Fatalf("ed25519 key algo = %s, %v", algo, err)
	}
	if _, err := PubKey("not a pubkey").KeyAlgo(); err == nil {
		t.Fatal("expected error for garbage key")
	}
	if _, err := EmptyPubKey.KeyAlgo(); err == nil {
		t.Fatal("expected error for empty key")
	}

	// The curve comes from the key bytes, not from the configured bech32
	// prefix: the same payload under another prefix decodes the same way.
	for _, tc := range []struct {
		key  PubKey
		want SigningAlgo
	}{{randomSecp256k1PubKey(t), SigningAlgoSecp256k1}, {randomEd25519PubKey(t), SigningAlgoEd25519}} {
		_, payload, err := sdkbech32.DecodeAndConvert(string(tc.key))
		if err != nil {
			t.Fatal(err)
		}
		foreign, err := sdkbech32.ConvertAndEncode("cosmospub", payload)
		if err != nil {
			t.Fatal(err)
		}
		if algo, algoErr := PubKey(foreign).KeyAlgo(); algoErr != nil || algo != tc.want {
			t.Fatalf("foreign-prefix key algo = %s, %v, want %s", algo, algoErr, tc.want)
		}
		// A truncated payload is rejected rather than misclassified.
		truncated, err := sdkbech32.ConvertAndEncode("thorpub", payload[:len(payload)-1])
		if err != nil {
			t.Fatal(err)
		}
		if _, algoErr := PubKey(truncated).KeyAlgo(); algoErr == nil {
			t.Fatal("expected error for truncated key payload")
		}
	}
}

func TestGetAddressForAlgoTaproot(t *testing.T) {
	pk := taprootVectorPubKey(t)

	legacy, err := pk.GetAddress(LTCChain)
	if err != nil {
		t.Fatal(err)
	}
	if got, legacyErr := pk.GetAddressForAlgo(LTCChain, SigningAlgoSecp256k1); legacyErr != nil || !got.Equals(legacy) {
		t.Fatalf("secp256k1 form = %s (%v), want %s", got, legacyErr, legacy)
	}

	taproot, err := pk.GetAddressForAlgo(LTCChain, SigningAlgoSecp256k1Taproot)
	if !LTCChain.SupportsTaprootVaults() {
		if err == nil {
			t.Fatalf("taproot address derived on %v build: %s", CurrentChainNetwork, taproot)
		}
		return
	}
	if err != nil {
		t.Fatalf("taproot form: %v", err)
	}
	if taproot.String() != expectedTaprootVectorAddress() {
		t.Fatalf("taproot form = %s, want %s", taproot, expectedTaprootVectorAddress())
	}
	if taproot.Equals(legacy) {
		t.Fatal("taproot and legacy forms must differ")
	}
	if !taproot.IsChain(LTCChain) {
		t.Fatalf("%s is not recognised as an LTC address", taproot)
	}
	// Second call is served from the cache and must agree.
	if again, err := pk.GetAddressForAlgo(LTCChain, SigningAlgoSecp256k1Taproot); err != nil || !again.Equals(taproot) {
		t.Fatalf("cached taproot form = %s (%v)", again, err)
	}

	if _, err := pk.GetAddressForAlgo(BTCChain, SigningAlgoSecp256k1Taproot); err == nil {
		t.Fatal("BTC must not derive a taproot vault address")
	}
	if _, err := randomEd25519PubKey(t).GetAddressForAlgo(LTCChain, SigningAlgoSecp256k1Taproot); err == nil {
		t.Fatal("an ed25519 key must not derive a taproot address")
	}
	if _, err := pk.GetAddressForAlgo(LTCChain, SigningAlgo("bogus")); err == nil {
		t.Fatal("expected error for unknown algo")
	}
	if got, err := EmptyPubKey.GetAddressForAlgo(LTCChain, SigningAlgoSecp256k1Taproot); err != nil || !got.IsEmpty() {
		t.Fatalf("empty key = %s (%v)", got, err)
	}
}

func TestGetAddressesAndHasAddress(t *testing.T) {
	pk := taprootVectorPubKey(t)
	legacy, err := pk.GetAddress(LTCChain)
	if err != nil {
		t.Fatal(err)
	}
	addresses, err := pk.GetAddresses(LTCChain)
	if err != nil {
		t.Fatal(err)
	}
	if !pk.HasAddress(LTCChain, legacy) {
		t.Fatal("legacy form not recognised")
	}
	other, err := randomSecp256k1PubKey(t).GetAddress(LTCChain)
	if err != nil {
		t.Fatal(err)
	}
	if pk.HasAddress(LTCChain, other) {
		t.Fatal("another key's address recognised")
	}

	if !LTCChain.SupportsTaprootVaults() {
		if len(addresses) != 1 {
			t.Fatalf("expected only the legacy form on %v, got %v", CurrentChainNetwork, addresses)
		}
		return
	}
	if len(addresses) != 2 || !addresses[0].Equals(legacy) || addresses[1].String() != expectedTaprootVectorAddress() {
		t.Fatalf("addresses = %v", addresses)
	}
	if !pk.HasAddress(LTCChain, Address(expectedTaprootVectorAddress())) {
		t.Fatal("taproot form not recognised")
	}

	// Other chains and ed25519 keys keep a single form.
	if btc, err := pk.GetAddresses(BTCChain); err != nil || len(btc) != 1 {
		t.Fatalf("BTC addresses = %v (%v)", btc, err)
	}
	if ed, err := randomEd25519PubKey(t).GetAddresses(LTCChain); err != nil || len(ed) != 1 {
		t.Fatalf("ed25519 LTC addresses = %v (%v)", ed, err)
	}
	if empty, err := EmptyPubKey.GetAddresses(LTCChain); err != nil || len(empty) != 0 {
		t.Fatalf("empty key addresses = %v (%v)", empty, err)
	}
}

func TestNewChainPoolInfoForAlgo(t *testing.T) {
	pk := taprootVectorPubKey(t)
	legacy, err := NewChainPoolInfoForAlgo(LTCChain, pk, SigningAlgoSecp256k1)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := NewChainPoolInfo(LTCChain, pk)
	if err != nil || !plain.PoolAddress.Equals(legacy.PoolAddress) {
		t.Fatalf("legacy pool info mismatch: %v %v", plain, err)
	}
	taproot, err := NewChainPoolInfoForAlgo(LTCChain, pk, SigningAlgoSecp256k1Taproot)
	if !LTCChain.SupportsTaprootVaults() {
		if err == nil {
			t.Fatal("expected error on a build without taproot vaults")
		}
		return
	}
	if err != nil || taproot.PoolAddress.String() != expectedTaprootVectorAddress() || !taproot.PubKey.Equals(pk) {
		t.Fatalf("taproot pool info = %+v (%v)", taproot, err)
	}
	if _, err := NewChainPoolInfoForAlgo(EmptyChain, pk, SigningAlgoSecp256k1Taproot); err == nil {
		t.Fatal("expected error for empty chain")
	}
	if _, err := NewChainPoolInfoForAlgo(LTCChain, EmptyPubKey, SigningAlgoSecp256k1Taproot); err == nil {
		t.Fatal("expected error for empty key")
	}
}
