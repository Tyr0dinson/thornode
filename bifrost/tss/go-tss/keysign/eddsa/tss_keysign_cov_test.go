package eddsa

import (
	"errors"
	"sort"
	"sync"
	"time"

	tsslibcommon "gitlab.com/thorchain/tss/tss-lib-private.git/v3/common"
	eddsakg "gitlab.com/thorchain/tss/tss-lib-private.git/v3/eddsa/keygen"
	"gitlab.com/thorchain/tss/tss-lib-private.git/v3/eddsa/signing"
	btss "gitlab.com/thorchain/tss/tss-lib-private.git/v3/tss"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

// mockTssMessageCov implements btss.Message for driving the blame paths.
type mockTssMessageCov struct {
	isBroadcast bool
	msgType     string
	wireErr     error
}

func (m *mockTssMessageCov) Type() string                  { return m.msgType }
func (m *mockTssMessageCov) GetFrom() *btss.PartyID        { return nil }
func (m *mockTssMessageCov) GetTo() []*btss.PartyID        { return nil }
func (m *mockTssMessageCov) IsBroadcast() bool             { return m.isBroadcast }
func (m *mockTssMessageCov) WireMsg() *btss.MessageWrapper { return nil }
func (m *mockTssMessageCov) String() string                { return "mock-" + m.msgType }
func (m *mockTssMessageCov) ValidateBasic() bool           { return true }
func (m *mockTssMessageCov) IsToOldAndNewCommittees() bool { return false }
func (m *mockTssMessageCov) IsToOldCommittee() bool        { return false }

func (m *mockTssMessageCov) WireBytes() ([]byte, *btss.MessageRouting, error) {
	return nil, nil, m.wireErr
}

type EddsaKeysignCovSuite struct{}

var _ = Suite(&EddsaKeysignCovSuite{})

// newBlameReadyKeySignCov builds a keysign instance whose blame manager knows
// a real four-party signing set, so the timeout blame logic can run.
func newBlameReadyKeySignCov(c *C, timeout time.Duration) *EDDSAKeySign {
	conversion.SetupBech32Prefix()
	keys := append([]string(nil), testPubKeys...)
	sort.Strings(keys)
	conf := common.TssConfig{KeySignTimeout: timeout, KeyGenTimeout: timeout}
	ks := NewTssKeySign("peer1", conf, nil, make(chan struct{}), "msgID-cov", nil, nil, nil, 1)

	partiesID, localPartyID, err := conversion.GetParties(keys, keys[0])
	c.Assert(err, IsNil)
	partyIDMap := conversion.SetupPartyIDMap(partiesID)
	c.Assert(conversion.SetupIDMaps(partyIDMap, ks.tssCommonStruct.PartyIDtoP2PID), IsNil)

	threshold, err := conversion.GetThreshold(len(partiesID))
	c.Assert(err, IsNil)
	params := btss.NewParameters(btss.Edwards(), btss.NewPeerContext(partiesID), localPartyID, len(partiesID), threshold)
	party := signing.NewLocalParty(nil, params, eddsakg.LocalPartySaveData{},
		make(chan btss.Message, 10), make(chan *tsslibcommon.SignatureData, 10))
	partyMap := new(sync.Map)
	partyMap.Store("", party)

	blameMgr := ks.tssCommonStruct.GetBlameMgr()
	blameMgr.SetPartyInfo(partyMap, partyIDMap)
	c.Assert(conversion.SetupIDMaps(partyIDMap, blameMgr.PartyIDtoP2PID), IsNil)
	ks.tssCommonStruct.P2PPeers = conversion.GetPeersID(ks.tssCommonStruct.PartyIDtoP2PID, ks.tssCommonStruct.GetLocalPeerID())
	return ks
}

func (s *EddsaKeysignCovSuite) TestProcessKeySignTimeoutBlameBroadcast(c *C) {
	ks := newBlameReadyKeySignCov(c, 20*time.Millisecond)
	blameMgr := ks.tssCommonStruct.GetBlameMgr()
	blameMgr.SetLastMsg(&mockTssMessageCov{isBroadcast: true, msgType: "eddsa-round2"})

	result, err := ks.processKeySign(1, make(chan struct{}),
		make(chan btss.Message), make(chan *tsslibcommon.SignatureData))
	c.Assert(err, Equals, blame.ErrTssTimeOut)
	c.Assert(result, IsNil)
	// nobody sent anything, so every peer is missing the share; with 3 peers
	// above the threshold (2) the missing-share blame is not applied
	c.Assert(blameMgr.GetBlame().BlameNodesCount(), Equals, 0)
}

func (s *EddsaKeysignCovSuite) TestProcessKeySignTimeoutBlameUnicast(c *C) {
	ks := newBlameReadyKeySignCov(c, 20*time.Millisecond)
	blameMgr := ks.tssCommonStruct.GetBlameMgr()
	blameMgr.SetLastMsg(&mockTssMessageCov{isBroadcast: false, msgType: "eddsa-round1"})

	result, err := ks.processKeySign(1, make(chan struct{}),
		make(chan btss.Message), make(chan *tsslibcommon.SignatureData))
	c.Assert(err, Equals, blame.ErrTssTimeOut)
	c.Assert(result, IsNil)
	c.Assert(blameMgr.GetBlame().BlameNodesCount(), Equals, 0)
}

func (s *EddsaKeysignCovSuite) TestProcessKeySignOutChProcessError(c *C) {
	ks := newBlameReadyKeySignCov(c, 5*time.Second)
	outCh := make(chan btss.Message, 1)
	msg := &mockTssMessageCov{isBroadcast: true, msgType: "eddsa-round1", wireErr: errors.New("no wire bytes")}
	outCh <- msg

	// WireBytes fails so ProcessOutCh fails; the error is returned
	result, err := ks.processKeySign(1, make(chan struct{}), outCh, make(chan *tsslibcommon.SignatureData))
	c.Assert(err, ErrorMatches, "fail to get wire bytes: no wire bytes")
	c.Assert(result, IsNil)
	// the outbound message is recorded as the last message for blame
	c.Assert(ks.tssCommonStruct.GetBlameMgr().GetLastMsg(), Equals, btss.Message(msg))
}

func (s *EddsaKeysignCovSuite) TestSignMessageNotInPartyReturnsNil(c *C) {
	conversion.SetupBech32Prefix()
	keys := append([]string(nil), testPubKeys...)
	sort.Strings(keys)
	ks := NewTssKeySign("peer1", common.TssConfig{}, nil, make(chan struct{}), "msgID-cov", nil, nil, nil, 1)
	// local party key is in the local state but not among the signing parties
	localState := storage.KeygenLocalState{LocalPartyKey: keys[3], ParticipantKeys: keys}
	sig, err := ks.SignMessage([][]byte{[]byte("msg")}, localState, keys[:3], "")
	c.Assert(err, NotNil)
	c.Assert(sig, IsNil)
}

func (s *EddsaKeysignCovSuite) TestStartBatchSigningFailure(c *C) {
	ks := NewTssKeySign("peer1", common.TssConfig{}, nil, make(chan struct{}), "msgID-cov", nil, nil, nil, 1)
	conversion.SetupBech32Prefix()
	keys := append([]string(nil), testPubKeys...)
	sort.Strings(keys)
	partiesID, localPartyID, err := conversion.GetParties(keys, keys[0])
	c.Assert(err, IsNil)
	params := btss.NewParameters(btss.Edwards(), btss.NewPeerContext(partiesID), localPartyID, len(partiesID), 2)
	// empty save data makes the party refuse to start
	party := signing.NewLocalParty(nil, params, eddsakg.LocalPartySaveData{},
		make(chan btss.Message, 10), make(chan *tsslibcommon.SignatureData, 10))
	partyMap := new(sync.Map)
	partyMap.Store("m", party)
	c.Assert(ks.startBatchSigning(partyMap, 1), Equals, false)
}
