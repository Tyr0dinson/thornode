package core

import (
	rand "math/rand/v2"
	"testing"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/solana/types"
	"gitlab.com/thorchain/thornode/v3/common"
)

func TestFreshSolanaAddressIsValidAndReproducible(t *testing.T) {
	a := freshSolanaAddress(rand.New(rand.NewPCG(1, 2)))
	b := freshSolanaAddress(rand.New(rand.NewPCG(1, 2)))
	c := freshSolanaAddress(rand.New(rand.NewPCG(3, 4)))
	if a != b {
		t.Fatalf("same seed gave %s and %s", a, b)
	}
	if a == c {
		t.Fatal("different seeds gave the same address")
	}
	if _, err := stypes.PublicKeyFromStringStrict(a.String()); err != nil {
		t.Fatalf("invalid address %s: %v", a, err)
	}
	if !a.IsChain(common.SOLChain) {
		t.Fatalf("%s is not a SOL address", a)
	}
}
