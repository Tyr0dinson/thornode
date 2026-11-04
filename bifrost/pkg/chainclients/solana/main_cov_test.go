package solana

import (
	"math/big"
	"os"
	"testing"

	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/crypto/paramcheck"
)

// TestMain waits for the primorial build tss-lib starts in init. Otherwise it takes
// CPU from the first tests, whose in-process RPC fakes can miss their 1s timeout on
// a busy runner. ValidateModulus is the only exported way to block on that build:
// this 2048-bit odd multiple of 3 reaches the small-factor check, so its expected
// rejection is ignored.
func TestMain(m *testing.M) {
	modulus := new(big.Int).Add(new(big.Int).Lsh(big.NewInt(1), 2047), big.NewInt(1))
	_ = paramcheck.ValidateModulus(modulus)
	os.Exit(m.Run())
}
