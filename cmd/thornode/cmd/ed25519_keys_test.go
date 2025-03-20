package cmd

import (
	"bytes"
	stded25519 "crypto/ed25519"
	"testing"

	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cosmos/cosmos-sdk/crypto/codec"
	bech32 "github.com/cosmos/cosmos-sdk/types/bech32/legacybech32" // nolint SA1019 deprecated
	"github.com/decred/dcrd/dcrec/edwards"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/x/thorchain"
)

func TestPackage(t *testing.T) { TestingT(t) }

type ED25519TestSuite struct{}

var _ = Suite(&ED25519TestSuite{})

func (s *ED25519TestSuite) SetUpTest(c *C) {
	thorchain.SetupConfigForTest()
}

func (*ED25519TestSuite) TestGetEd25519Keys(c *C) {
	thorchain.SetupConfigForTest()
	mnemonic := `grape safe sound obtain bachelor festival profit iron meat moon exit garbage chapter promote noble grocery blood letter junk click mesh arm shop decorate`
	result, err := mnemonicToEddKey(mnemonic, "")
	c.Assert(err, IsNil)
	// now we test the ed25519 key can sign and verify
	_, pk, err := edwards.PrivKeyFromScalar(edwards.Edwards(), result)
	c.Assert(err, IsNil)
	pkey := ed25519.PubKey(pk.Serialize())
	tmp, err := codec.FromCmtPubKeyInterface(pkey)
	c.Assert(err, IsNil)
	// nolint
	pubKey, err := bech32.MarshalPubKey(bech32.AccPK, tmp)
	c.Assert(err, IsNil)
	c.Assert(pubKey, Equals, "tthorpub1zcjduepqkh2q3agpupf9w3kqpgqfe0n3crtn8jljzds777x4x9tw9tngmk6s4vfcz5")

	mnemonic = `invalid grape safe sound obtain bachelor festival profit iron meat moon exit garbage chapter promote noble grocery blood letter junk click mesh arm shop decorate`
	result, err = mnemonicToEddKey(mnemonic, "")
	c.Assert(err, NotNil)
	c.Assert(result, IsNil)
}

func (*ED25519TestSuite) TestGetEd25519KeysWithPassphrase(c *C) {
	thorchain.SetupConfigForTest()
	mnemonic := `grape safe sound obtain bachelor festival profit iron meat moon exit garbage chapter promote noble grocery blood letter junk click mesh arm shop decorate`
	result, err := mnemonicToEddKey(mnemonic, "")
	c.Assert(err, IsNil)
	priv, pub, err := edwards.PrivKeyFromScalar(edwards.Edwards(), result)
	privBz := priv.Serialize()
	pubBz := pub.Serialize()
	c.Assert(err, IsNil)
	c.Assert(len(pubBz), Equals, ed25519.PubKeySize)
	c.Assert(len(privBz), Equals, 32)
	c.Assert(bytes.Equal(privBz, result), Equals, true)

	stdPriv := stded25519.NewKeyFromSeed(privBz)
	c.Assert(bytes.Equal(stdPriv.Seed(), privBz), Equals, true)

	stdPub := stdPriv.Public().(stded25519.PublicKey)
	c.Assert(bytes.Equal(stdPub, pubBz), Equals, true)
}
