package common

import (
	"math/big"

	"github.com/binance-chain/tss-lib/crypto/paillier"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common/testutil"
)

// PaillierValidateSuite groups regression coverage for ECDSA localstate
// validation without running a full TSS keygen.
type PaillierValidateSuite struct{}

var _ = Suite(&PaillierValidateSuite{})

// TestValidateECDSALocalPartySaveDataValid confirms the baseline fixture passes
// all validator checks before individual tests mutate it into invalid states.
func (s *PaillierValidateSuite) TestValidateECDSALocalPartySaveDataValid(c *C) {
	c.Assert(ValidateECDSALocalPartySaveData(testutil.MakeValidECDSALocalPartySaveData(nil)), IsNil)
}

// TestValidateECDSALocalPartySaveDataRejectsSmallFactor verifies the full
// localstate validator rejects Paillier moduli containing a known small factor.
func (s *PaillierValidateSuite) TestValidateECDSALocalPartySaveDataRejectsSmallFactor(c *C) {
	data := testutil.MakeValidECDSALocalPartySaveData(nil)
	cofactor := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), paillierExpectedModulusBits-16), big.NewInt(17))
	data.PaillierPKs[1].N = new(big.Int).Mul(cofactor, big.NewInt(65537))

	err := ValidateECDSALocalPartySaveData(data)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*small factor 65537.*")
}

// TestValidatePaillierPublicKeyRejectsIncidentModulus locks in the concrete
// public Paillier modulus observed in the incident localstate artifact.
func (s *PaillierValidateSuite) TestValidatePaillierPublicKeyRejectsIncidentModulus(c *C) {
	// This is public key material only; no private share data is copied here.
	incidentN := mustParseBigInt(c,
		"24237754553483255475536157516502463970333077002286613024097759070643491354150918"+
			"16989790105864218509776653847069142351462058966936286819082131611428983621234047"+
			"31580254757470688126483127922390800970432287880506762075941639819648171213785707"+
			"53857127852936234389027228782165681918655799356455664299342947789141435750042251"+
			"25681551373108992342712114213202649474154355835424449826964453134523561269394464"+
			"81328375444577027888463930181987633642262344283027732099249654234203442635331151"+
			"57293867826295198492602258906935492523451284497951263348710738281654446776237074"+
			"088020620235910529662357721898290528008211173578839181893")

	err := validatePaillierPublicKey(&paillier.PublicKey{N: incidentN}, 0)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*small factor 65537.*")
}

// TestValidateECDSALocalPartySaveDataRejectsLocalKeyMismatch ensures the local
// private Paillier key maps to the public key at the local ShareID index.
func (s *PaillierValidateSuite) TestValidateECDSALocalPartySaveDataRejectsLocalKeyMismatch(c *C) {
	data := testutil.MakeValidECDSALocalPartySaveData(nil)
	data.PaillierSK.N = new(big.Int).Set(data.PaillierPKs[1].N)

	err := ValidateECDSALocalPartySaveData(data)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*does not match public key.*")
}

// TestValidateECDSALocalPartySaveDataRejectsSharedFactor catches duplicated or
// otherwise factor-related Paillier keys across different participants.
func (s *PaillierValidateSuite) TestValidateECDSALocalPartySaveDataRejectsSharedFactor(c *C) {
	data := testutil.MakeValidECDSALocalPartySaveData(nil)
	data.PaillierPKs[1].N = new(big.Int).Set(data.PaillierPKs[0].N)

	err := ValidateECDSALocalPartySaveData(data)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Matches, ".*share a factor.*")
}

// TestValidateECDSALocalPartySaveDataRejectsMissingShareID covers localstate
// that cannot map its local party to any participant slot.
func (s *PaillierValidateSuite) TestValidateECDSALocalPartySaveDataRejectsMissingShareID(c *C) {
	data := testutil.MakeValidECDSALocalPartySaveData(nil)
	data.ShareID = nil

	err := ValidateECDSALocalPartySaveData(data)
	c.Assert(err, NotNil)
	c.Assert(err.Error(), Equals, "missing local share ID")
}

// mustParseBigInt keeps long decimal modulus fixtures readable while failing
// the test immediately if the fixture string is malformed.
func mustParseBigInt(c *C, value string) *big.Int {
	n, ok := new(big.Int).SetString(value, 10)
	c.Assert(ok, Equals, true)
	return n
}
