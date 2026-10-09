package common

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	ckeys "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/types"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type SignCovSuite struct{}

var _ = Suite(&SignCovSuite{})

const (
	signCovSigner   = "signcov"
	signCovPassword = "password1234"
)

// setEnvCov sets an environment variable and returns a restore func.
func setEnvCov(key, value string) func() {
	old, ok := os.LookupEnv(key)
	_ = os.Setenv(key, value)
	return func() {
		if ok {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}

// newSignCovKeyring creates a file keyring in dir holding one secp256k1 key.
func newSignCovKeyring(c *C, dir string) types.PubKey {
	buf := bytes.NewBufferString(signCovPassword + "\n" + signCovPassword + "\n")
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)
	kb, err := ckeys.New(cosmos.KeyringServiceName(), ckeys.BackendFile, dir, buf, cdc)
	c.Assert(err, IsNil)
	rec, _, err := kb.NewMnemonic(signCovSigner, ckeys.English, "m/44'/931'/0'/0/0", "", hd.Secp256k1)
	c.Assert(err, IsNil)
	pk, err := rec.GetPubKey()
	c.Assert(err, IsNil)
	return pk
}

func (s *SignCovSuite) TestSignAndSignBase64(c *C) {
	dir := filepath.Join(c.MkDir(), ".thornode")
	pk := newSignCovKeyring(c, dir)

	defer setEnvCov(cosmos.EnvChainHome, dir)()
	defer setEnvCov(cosmos.EnvSignerName, signCovSigner)()
	defer setEnvCov(cosmos.EnvSignerPassword, signCovPassword)()

	msg := []byte("hello world")
	sig, pub, err := Sign(msg)
	c.Assert(err, IsNil)
	c.Assert(pub, DeepEquals, pk.Bytes())
	c.Assert(pk.VerifySignature(msg, sig), Equals, true)

	sigB64, pubB64, err := SignBase64(msg)
	c.Assert(err, IsNil)
	c.Assert(pubB64, Equals, base64.StdEncoding.EncodeToString(pk.Bytes()))
	rawSig, err := base64.StdEncoding.DecodeString(sigB64)
	c.Assert(err, IsNil)
	c.Assert(pk.VerifySignature(msg, rawSig), Equals, true)
}

func (s *SignCovSuite) TestSignUnknownSigner(c *C) {
	dir := filepath.Join(c.MkDir(), ".thornode")
	newSignCovKeyring(c, dir)

	defer setEnvCov(cosmos.EnvChainHome, dir)()
	defer setEnvCov(cosmos.EnvSignerName, "nobody")()
	defer setEnvCov(cosmos.EnvSignerPassword, signCovPassword)()

	sig, pub, err := Sign([]byte("x"))
	c.Assert(err, NotNil)
	c.Assert(sig, IsNil)
	c.Assert(pub, IsNil)

	sigB64, pubB64, err := SignBase64([]byte("x"))
	c.Assert(err, NotNil)
	c.Assert(sigB64, Equals, "")
	c.Assert(pubB64, Equals, "")
}
