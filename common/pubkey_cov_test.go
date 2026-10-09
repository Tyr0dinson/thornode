package common

import (
	"encoding/json"
	"strings"

	cmted25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
)

type PubKeyCovSuite struct{}

var _ = Suite(&PubKeyCovSuite{})

func pubKeyCovEd25519(c *C) PubKey {
	pk, err := NewPubKeyFromCrypto(cmted25519.GenPrivKeyFromSecret([]byte("pubkey-cov-ed25519")).PubKey())
	c.Assert(err, IsNil)
	return pk
}

func pubKeyCovSecp(c *C, secret string) PubKey {
	pk, err := NewPubKeyFromCrypto(secp256k1.GenPrivKeySecp256k1([]byte(secret)).PubKey())
	c.Assert(err, IsNil)
	return pk
}

func (s *PubKeyCovSuite) TestNewPubKeyFromCrypto(c *C) {
	priv := secp256k1.GenPrivKeySecp256k1([]byte("from-crypto"))
	pk, err := NewPubKeyFromCrypto(priv.PubKey())
	c.Assert(err, IsNil)
	c.Assert(pk.IsEmpty(), Equals, false)

	// round trips back to the same raw key bytes
	cpk, err := cosmos.GetPubKeyFromBech32(cosmos.Bech32PubKeyTypeAccPub, pk.String())
	c.Assert(err, IsNil)
	c.Assert(cpk.Bytes(), DeepEquals, priv.PubKey().Bytes())

	// a nil crypto key is not convertible
	_, err = NewPubKeyFromCrypto(nil)
	c.Assert(err, NotNil)
}

func (s *PubKeyCovSuite) TestNewPubKeyErrors(c *C) {
	pk, err := NewPubKey("")
	c.Assert(err, IsNil)
	c.Assert(pk, Equals, EmptyPubKey)

	pk, err = NewPubKey("not-a-pubkey")
	c.Assert(err, NotNil)
	c.Assert(pk, Equals, EmptyPubKey)
	c.Assert(strings.Contains(err.Error(), "not bech32 encoded pub key"), Equals, true)

	var p PubKey
	c.Assert(json.Unmarshal([]byte(`"not-a-pubkey"`), &p), NotNil)
	c.Assert(json.Unmarshal([]byte(`123`), &p), NotNil)
	good := pubKeyCovSecp(c, "json")
	buf, err := json.Marshal(good)
	c.Assert(err, IsNil)
	c.Assert(json.Unmarshal(buf, &p), IsNil)
	c.Assert(p, Equals, good)
}

func (s *PubKeyCovSuite) TestSecp256K1AndEVMErrors(c *C) {
	bad := PubKey("bogus")
	_, err := bad.Secp256K1()
	c.Assert(err, NotNil)
	addr, err := bad.EVMPubkeyToAddress()
	c.Assert(err, NotNil)
	c.Assert(addr, Equals, NoAddress)

	// an ed25519 key cannot be parsed as secp256k1
	_, err = pubKeyCovEd25519(c).Secp256K1()
	c.Assert(err, NotNil)
}

func (s *PubKeyCovSuite) TestGetAddressAllChainsSecp(c *C) {
	pk := pubKeyCovSecp(c, "all-chains")
	for _, chain := range AllChains {
		if chain.GetSigningAlgo() != SigningAlgoSecp256k1 {
			continue
		}
		addr, err := pk.GetAddress(chain)
		c.Assert(err, IsNil, Commentf("chain %s", chain))
		c.Assert(addr.IsEmpty(), Equals, false, Commentf("chain %s", chain))
		if !chain.Equals(THORChain) { // THOR prefix depends on the global sdk config
			c.Assert(addr.IsChain(chain), Equals, true, Commentf("chain %s addr %s", chain, addr))
		}

		// cached lookup returns the same value
		again, err := pk.GetAddress(chain)
		c.Assert(err, IsNil)
		c.Assert(again, Equals, addr)
	}

	// EVM chains all share the same address
	eth, err := pk.GetAddress(ETHChain)
	c.Assert(err, IsNil)
	avax, err := pk.GetAddress(AVAXChain)
	c.Assert(err, IsNil)
	c.Assert(eth, Equals, avax)
	evm, err := pk.EVMPubkeyToAddress()
	c.Assert(err, IsNil)
	c.Assert(evm, Equals, eth)

	// unknown chain yields no address and no error
	addr, err := pk.GetAddress(Chain("NOPE"))
	c.Assert(err, IsNil)
	c.Assert(addr, Equals, NoAddress)

	// empty pubkey yields no address
	addr, err = EmptyPubKey.GetAddress(BTCChain)
	c.Assert(err, IsNil)
	c.Assert(addr, Equals, NoAddress)
}

func (s *PubKeyCovSuite) TestGetAddressAllChainsEd25519(c *C) {
	pk := pubKeyCovEd25519(c)
	for _, chain := range []Chain{SOLChain, TAOChain, DOTChain, SUIChain, ADAChain, XMRChain} {
		addr, err := pk.GetAddress(chain)
		c.Assert(err, IsNil, Commentf("chain %s", chain))
		c.Assert(addr.IsEmpty(), Equals, false, Commentf("chain %s", chain))
		c.Assert(addr.IsChain(chain), Equals, true, Commentf("chain %s addr %s", chain, addr))
	}
	sui, err := pk.GetAddress(SUIChain)
	c.Assert(err, IsNil)
	c.Assert(strings.HasPrefix(sui.String(), "0x"), Equals, true)
	c.Assert(len(sui.String()), Equals, 66)
}

func (s *PubKeyCovSuite) TestGetAddressInvalidPubKey(c *C) {
	// every chain-specific branch rejects a pubkey that is not bech32 encoded
	bad := PubKey("thorpub1invalid")
	for _, chain := range []Chain{
		XRPChain, GAIAChain, THORChain, NOBLEChain, BTCChain, LTCChain, DOGEChain,
		BCHChain, SOLChain, TAOChain, DOTChain, TRONChain, XMRChain, ZECChain,
		SUIChain, ADAChain, ETHChain,
	} {
		addr, err := bad.GetAddress(chain)
		c.Assert(err, NotNil, Commentf("chain %s", chain))
		c.Assert(addr, Equals, NoAddress, Commentf("chain %s", chain))
	}

	// 32 byte ed25519 chains reject 33 byte secp256k1 keys
	secp := pubKeyCovSecp(c, "wrong-len")
	for _, chain := range []Chain{SOLChain, TAOChain, DOTChain} {
		_, err := secp.GetAddress(chain)
		c.Assert(err, NotNil, Commentf("chain %s", chain))
		c.Assert(strings.Contains(err.Error(), "invalid pubkey length"), Equals, true)
	}
	_, err := secp.GetAddress(XMRChain)
	c.Assert(err, NotNil)

	// ed25519 keys cannot produce secp256k1-derived addresses
	ed := pubKeyCovEd25519(c)
	for _, chain := range []Chain{XRPChain, TRONChain, ETHChain} {
		_, err := ed.GetAddress(chain)
		c.Assert(err, NotNil, Commentf("chain %s", chain))
	}
}

func (s *PubKeyCovSuite) TestGetThorAddressAndAddresses(c *C) {
	pk1 := pubKeyCovSecp(c, "thor-1")
	pk2 := pubKeyCovSecp(c, "thor-2")

	acc, err := pk1.GetThorAddress()
	c.Assert(err, IsNil)
	thorAddr, err := pk1.GetAddress(THORChain)
	c.Assert(err, IsNil)
	c.Assert(acc.String(), Equals, thorAddr.String())

	_, err = PubKey("bogus").GetThorAddress()
	c.Assert(err, NotNil)

	pks := PubKeys{pk1, pk2}
	addrs, err := pks.Addresses()
	c.Assert(err, IsNil)
	c.Assert(addrs, HasLen, 2)
	c.Assert(addrs[0].String(), Equals, acc.String())

	_, err = PubKeys{pk1, PubKey("bogus")}.Addresses()
	c.Assert(err, NotNil)
}

func (s *PubKeyCovSuite) TestPubKeysHelpers(c *C) {
	pk1 := pubKeyCovSecp(c, "keys-1")
	pk2 := pubKeyCovSecp(c, "keys-2")
	pk3 := pubKeyCovSecp(c, "keys-3")
	pks := PubKeys{pk1, pk2}

	c.Assert(pks.Valid(), IsNil)
	c.Assert(PubKeys{pk1, PubKey("bogus")}.Valid(), NotNil)

	c.Assert(pks.Contains(pk1), Equals, true)
	c.Assert(pks.Contains(pk3), Equals, false)

	c.Assert(pks.String(), Equals, pk1.String()+", "+pk2.String())
	c.Assert(pks.Strings(), DeepEquals, []string{pk1.String(), pk2.String()})
	c.Assert(PubKeys{}.String(), Equals, "")

	// Equals is order insensitive but length and content sensitive
	c.Assert(pks.Equals(PubKeys{pk2, pk1}), Equals, true)
	c.Assert(pks.Equals(PubKeys{pk1}), Equals, false)
	c.Assert(pks.Equals(PubKeys{pk1, pk3}), Equals, false)
	// Equals must not reorder the receiver
	c.Assert(pks[0], Equals, pk1)
}

func (s *PubKeyCovSuite) TestConvertAndEncode(c *C) {
	out, err := ConvertAndEncode("thor", []byte{1, 2, 3})
	c.Assert(err, IsNil)
	c.Assert(strings.HasPrefix(out, "thor1"), Equals, true)
	other, err := ConvertAndEncode("thor", []byte{1, 2, 4})
	c.Assert(err, IsNil)
	c.Assert(other, Not(Equals), out)
}

func (s *PubKeyCovSuite) TestPubKeySet(c *C) {
	secp := pubKeyCovSecp(c, "set")
	ed := pubKeyCovEd25519(c)

	set := NewPubKeySet(secp, ed)
	c.Assert(set.Secp256k1, Equals, secp)
	c.Assert(set.Ed25519, Equals, ed)
	c.Assert(set.IsEmpty(), Equals, false)
	c.Assert(NewPubKeySet(secp, EmptyPubKey).IsEmpty(), Equals, true)
	c.Assert(NewPubKeySet(EmptyPubKey, ed).IsEmpty(), Equals, true)

	c.Assert(set.Equals(NewPubKeySet(secp, ed)), Equals, true)
	c.Assert(set.Equals(NewPubKeySet(secp, EmptyPubKey)), Equals, false)
	c.Assert(set.Contains(secp), Equals, true)
	c.Assert(set.Contains(ed), Equals, true)
	c.Assert(set.Contains(pubKeyCovSecp(c, "other")), Equals, false)

	str := set.String()
	c.Assert(strings.Contains(str, "secp256k1: "+secp.String()), Equals, true)
	c.Assert(strings.Contains(str, "ed25519: "+ed.String()), Equals, true)

	btc, err := set.GetAddress(BTCChain)
	c.Assert(err, IsNil)
	want, err := secp.GetAddress(BTCChain)
	c.Assert(err, IsNil)
	c.Assert(btc, Equals, want)

	sol, err := set.GetAddress(SOLChain)
	c.Assert(err, IsNil)
	want, err = ed.GetAddress(SOLChain)
	c.Assert(err, IsNil)
	c.Assert(sol, Equals, want)

	// unknown chains default to secp256k1 and resolve to no address
	addr, err := set.GetAddress(Chain("NOPE"))
	c.Assert(err, IsNil)
	c.Assert(addr, Equals, NoAddress)
}
