package types

import (
	"testing"

	"gitlab.com/thorchain/thornode/v3/common"
)

func TestAsgardTssIDBindsFrostPubKey(t *testing.T) {
	signerKey := GetRandomPubKey()
	signer, err := signerKey.GetThorAddress()
	if err != nil {
		t.Fatal(err)
	}
	members := []string{signerKey.String(), GetRandomPubKey().String(), GetRandomPubKey().String()}
	copyMembers := func() []string { return append([]string(nil), members...) }
	poolPk, eddsa, frost := GetRandomPubKey(), GetRandomPubKey(), GetTaprootTestPubKey()
	chains := []string{common.RuneAsset().Chain.String(), common.LTCChain.String()}
	const height = int64(10)

	build := func(frostKey common.PubKey) *MsgTssPool {
		msg, buildErr := NewMsgTssPoolV2(copyMembers(), poolPk, nil, nil, KeygenType_AsgardKeygen, height, nil, chains, signer, 1, eddsa, nil, nil, frostKey)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		return msg
	}

	// Without a FROST key the asgard ID is the legacy ID, so existing keygens
	// are unaffected.
	plain := build(common.EmptyPubKey)
	legacy, err := getTssID(copyMembers(), poolPk, eddsa, height, nil)
	if err != nil || plain.ID != legacy {
		t.Fatalf("plain asgard ID %s, want legacy %s (%v)", plain.ID, legacy, err)
	}

	// With a FROST key the ID binds it, so different keys tally separately.
	withFrost := build(frost)
	if withFrost.ID == plain.ID {
		t.Fatal("FROST key did not change the asgard ID")
	}
	bound, err := getAsgardTssID(copyMembers(), poolPk, eddsa, height, nil, frost)
	if err != nil || withFrost.ID != bound {
		t.Fatalf("asgard ID with frost %s, want %s (%v)", withFrost.ID, bound, err)
	}
	if other := build(GetRandomPubKey()); other.ID == withFrost.ID {
		t.Fatal("different FROST keys produced the same asgard ID")
	}
	if !withFrost.PoolPubKeyFrost.Equals(frost) {
		t.Fatal("FROST key not carried on the message")
	}

	// The V3 constructor bifrost uses agrees with V2 for asgard keygens.
	v3, err := NewMsgTssPoolV3(copyMembers(), poolPk, nil, nil, KeygenType_AsgardKeygen, height, nil, chains, signer, 1, eddsa, nil, nil, frost, 0)
	if err != nil || v3.ID != withFrost.ID {
		t.Fatalf("V3 asgard ID %s, want %s (%v)", v3.ID, withFrost.ID, err)
	}

	// XMR monovault IDs are unchanged by the refactor.
	xmrFrost := GetRandomEd25519PubKey()
	xmr, err := NewMsgTssPoolV3(copyMembers(), poolPk, nil, nil, KeygenType_XMRMonovaultKeygen, height, nil, []string{common.RuneAsset().Chain.String(), common.XMRChain.String()}, signer, 1, common.EmptyPubKey, nil, nil, xmrFrost, 77)
	if err != nil {
		t.Fatal(err)
	}
	xmrWant, err := getXMRTssID(copyMembers(), poolPk, common.EmptyPubKey, height, nil, xmrFrost, 77)
	if err != nil || xmr.ID != xmrWant {
		t.Fatalf("XMR ID %s, want %s (%v)", xmr.ID, xmrWant, err)
	}

	// Basic validation only checks the encoding of the FROST key; policy is
	// the handler's job.
	if err := withFrost.ValidateBasic(); err != nil {
		t.Fatalf("asgard report with FROST key failed basic validation: %v", err)
	}
	bad := build(frost)
	bad.PoolPubKeyFrost = common.PubKey("not-a-pubkey")
	if err := bad.ValidateBasic(); err == nil {
		t.Fatal("undecodable FROST key passed basic validation")
	}
}
