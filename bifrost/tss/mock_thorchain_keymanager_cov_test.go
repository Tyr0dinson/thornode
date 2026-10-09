package tss

import (
	"encoding/base64"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common"
)

type MockKeyManagerCovSuite struct{}

var _ = Suite(&MockKeyManagerCovSuite{})

func (s *MockKeyManagerCovSuite) TestTrivialMethods(c *C) {
	k := &MockThorchainKeyManager{}
	c.Check(k.GetPrivKey(), IsNil)
	c.Check(k.GetAddr(), IsNil)
	m, err := k.ExportAsMnemonic()
	c.Check(err, IsNil)
	c.Check(m, Equals, "")
	p, err := k.ExportAsPrivateKey()
	c.Check(err, IsNil)
	c.Check(p, Equals, "")
	ks, err := k.ExportAsKeyStore("password")
	c.Check(err, IsNil)
	c.Check(ks, IsNil)
	k.Start()
	k.Stop()
}

func (s *MockKeyManagerCovSuite) TestRemoteSign(c *C) {
	k := &MockThorchainKeyManager{}
	decode := func(b64 string) []byte {
		b, err := base64.StdEncoding.DecodeString(b64)
		c.Assert(err, IsNil)
		return b
	}
	expectSig := func(pool, msgB64, r, sVal string) {
		sig, recovery, err := k.RemoteSign(decode(msgB64), common.SigningAlgoSecp256k1, pool, 1)
		c.Assert(err, IsNil)
		c.Check(recovery, IsNil)
		want, err := getSignature(r, sVal)
		c.Assert(err, IsNil)
		c.Check(sig, DeepEquals, want)
		c.Check(sig, HasLen, 64)
	}

	const pool1 = "tthorpub1addwnpepqwznsrgk2t5vn2cszr6ku6zned6tqxknugzw3vhdcjza284d7djp5rql6vn"
	expectSig(pool1, "wqYuqkdeLjxtkKjmeAK0fOZygdw8zZgsDaJX7mrqWRE=",
		"ku/n0D18euwqkgM0kZn0OVX9+D7wfDBIWBMya1SGxWg=", "fw0sE6osjVN6vQtr9WxFrOpdxizPz9etSTOKGdjDY9A=")
	expectSig(pool1, base64.StdEncoding.EncodeToString([]byte("other")),
		"256CpfiML7BDP1nXqKRc3Fq01PALeKwpXYv9P/H3Xhk=", "LoX6cVND0JN8bbZSTsoJcwLCysAKhyYtB2BFM3sdP98=")

	const pool2 = "tthorpub1addwnpepqw2k68efthm08f0f5akhjs6fk5j2pze4wkwt4fmnymf9yd463puruhh0lyz"
	expectSig(pool2, "BMxXf+K+1dYu3qGgvH59GXoxwwFfTnLjB7hHf3qflPk=",
		"WGSFUPPCN0kTcXcylAIQXyAxO7OUC5YRjDRz9wmzpkk=", "RUIoqdza5Od9nMfU2teqbZJAeC+pTyHIbKq+72jJMfM=")
	expectSig(pool2, "7zpXFp0KDBebXPNc2ZGim8NQAY7GMwS7iwr4hl2tFZQ=",
		"tCR9TWnSxn/HPr0T3I9XeneJ0dRmi2DqbOkcFPWIkNs=", "VAxipOj6ogfBci+WwJy4n9QfAjjhJk6WhQ1I8n6xEo4=")
	expectSig(pool2, "isIqvmEs/otDI3NC2C8zFr1DGu3k/p8g/1RdlE0KzBI=",
		"Nkb9ZFkPpSi1i/GaJe6FkMZmx1IH2oDtnr0jGsycBF8=", "ZAQ0qbPtPtdAin5HVOMmMO6oJxwWT4T0GvqpeyGG168=")
	expectSig(pool2, base64.StdEncoding.EncodeToString([]byte("other")),
		"gVxKdVgWR+4OZLxFAu5uWWOPCxhGPqFtQAyVujqSuh8=", "JXPU4Li4spnonssxJS52r/hEBwt1iPFlvjwu8ZOe+F0=")

	const pool3 = "tthorpub1addwnpepqtvzm6wa6ezgjj9l4sdvzcf64wf0wzs8x9mgjfhjp6tkzcvkyfyqg9a9p8e"
	sig, _, err := k.RemoteSign(decode("PIZUt687khEYQizRpYbLyQgDw1Ou+xzbSrLQ8fTKiaw="), common.SigningAlgoSecp256k1, pool3, 1)
	c.Assert(err, IsNil)
	c.Check(sig, DeepEquals, decode("HxT9xOyBYuhHfK8iLSbPniJq6u6KYfJVmq28iO+/Sa44ocAuckpzs3g6zBelr4pUaxatoKixAaPt2UtlgPP2sA=="))

	// unknown message for pool3 and unknown pools yield nothing
	sig, recovery, err := k.RemoteSign([]byte("x"), common.SigningAlgoSecp256k1, pool3, 1)
	c.Check(err, IsNil)
	c.Check(sig, IsNil)
	c.Check(recovery, IsNil)
	sig, _, err = k.RemoteSign([]byte("x"), common.SigningAlgoSecp256k1, "unknown", 1)
	c.Check(err, IsNil)
	c.Check(sig, IsNil)
}
