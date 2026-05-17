package testutil

import (
	"math/big"

	bcrypto "github.com/binance-chain/tss-lib/crypto"
	"github.com/binance-chain/tss-lib/crypto/paillier"
	ecdsaKeygen "github.com/binance-chain/tss-lib/ecdsa/keygen"
)

// MakeValidECDSALocalPartySaveData builds the minimal ECDSA local save data
// needed by tests that exercise persisted localstate validation paths.
func MakeValidECDSALocalPartySaveData(ecPoint *bcrypto.ECPoint) ecdsaKeygen.LocalPartySaveData {
	n0 := MakePaillierTestModulus(447)
	n1 := MakePaillierTestModulus(723)

	data := ecdsaKeygen.NewLocalPartySaveData(2)
	data.PaillierSK = &paillier.PrivateKey{
		PublicKey: paillier.PublicKey{N: n0},
		LambdaN:   big.NewInt(1),
		PhiN:      big.NewInt(1),
	}
	data.NTildei = big.NewInt(3)
	data.H1i = big.NewInt(5)
	data.H2i = big.NewInt(7)
	data.Alpha = big.NewInt(11)
	data.Beta = big.NewInt(13)
	data.P = big.NewInt(17)
	data.Q = big.NewInt(19)
	data.Xi = big.NewInt(23)
	data.ShareID = big.NewInt(29)

	data.Ks[0] = new(big.Int).Set(data.ShareID)
	data.Ks[1] = big.NewInt(31)
	data.NTildej[0] = big.NewInt(37)
	data.NTildej[1] = big.NewInt(41)
	data.H1j[0] = big.NewInt(43)
	data.H1j[1] = big.NewInt(47)
	data.H2j[0] = big.NewInt(53)
	data.H2j[1] = big.NewInt(59)
	data.PaillierPKs[0] = &paillier.PublicKey{N: n0}
	data.PaillierPKs[1] = &paillier.PublicKey{N: n1}
	data.ECDSAPub = ecPoint

	return data
}

// MakePaillierTestModulus returns deterministic 2048-bit odd composite values
// that avoid the small-prime divisibility checks used by the validator.
func MakePaillierTestModulus(delta int64) *big.Int {
	return new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 2048), big.NewInt(delta))
}

// MakeSmallFactorPaillierModulus returns a 2048-bit-sized odd composite value
// with the supplied small factor for negative validation tests.
func MakeSmallFactorPaillierModulus(factor int64) *big.Int {
	cofactor := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 2032), big.NewInt(17))
	return new(big.Int).Mul(cofactor, big.NewInt(factor))
}
