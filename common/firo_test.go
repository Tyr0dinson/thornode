package common

import (
	"bytes"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/utxo/firoutil"
)

func TestFiroRegistrationAndNetwork(t *testing.T) {
	if !FIROChain.IsUTXO() || !FIROChain.GetGasAsset().Equals(FIROAsset) || FIROAsset.ShortCode() != "fi" {
		t.Fatal("FIRO registration mismatch")
	}
	for _, kind := range []firoutil.Kind{firoutil.PubKeyHash, firoutil.ScriptHash, firoutil.Exchange} {
		a, err := firoutil.NewAddress(bytes.Repeat([]byte{1}, 20), kind, FIRONetwork())
		if err != nil {
			t.Fatal(err)
		}
		address, err := NewAddress(a.String())
		if err != nil {
			t.Fatal(err)
		}
		if !address.IsChain(FIROChain) || !address.IsCurrentNetwork(FIROChain) || !FIROChain.IsValidAddress(address) {
			t.Fatalf("rejected supported FIRO destination %s", address)
		}
		if AddressEquals(FIROChain, address, Address(strings.ToLower(address.String()))) {
			t.Fatal("FIRO address comparison folded case")
		}
	}
	for _, tc := range []struct {
		network     ChainNetwork
		prefix      string
		firoNetwork firoutil.Network
	}{
		{MainNet, "EXX", firoutil.MainNet},
		{StageNet, "EXX", firoutil.MainNet},
		{ChainNet, "EXX", firoutil.MainNet},
		{MockNet, "EXR", firoutil.RegTest},
		{TestNet, "EXT", firoutil.TestNet},
	} {
		if FIROChain.AddressPrefix(tc.network) != tc.prefix || firoNetwork(tc.network) != tc.firoNetwork {
			t.Fatalf("wrong FIRO network mapping: %v", tc.network)
		}
		a, _ := firoutil.NewAddress(make([]byte, 20), firoutil.Exchange, tc.firoNetwork)
		if Address(a.String()).IsCurrentNetwork(FIROChain) != (FIRONetwork() == tc.firoNetwork) {
			t.Fatal("accepted wrong exchange network")
		}
	}
}
