package ecdsa

import (
	"errors"
	"sort"
	"sync"
	"time"

	bkg "gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"
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

type TssECDSAKeygenCovSuite struct{}

var _ = Suite(&TssECDSAKeygenCovSuite{})

// newBlameReadyKeyGenCov builds a keygen instance whose blame manager knows a
// real four-party keygen set, so the timeout blame logic can run.
func newBlameReadyKeyGenCov(c *C, timeout time.Duration) *TssKeyGen {
	conversion.SetupBech32Prefix()
	keys := append([]string(nil), testPubKeys...)
	sort.Strings(keys)
	conf := common.TssConfig{KeyGenTimeout: timeout, KeySignTimeout: timeout}
	instance := NewTssKeyGen("peer1", conf, keys[0], nil, nil, nil, "msg-cov", &storage.MockLocalStateManager{}, nil, nil)

	partiesID, localPartyID, err := conversion.GetParties(keys, keys[0])
	c.Assert(err, IsNil)
	partyIDMap := conversion.SetupPartyIDMap(partiesID)
	c.Assert(conversion.SetupIDMaps(partyIDMap, instance.tssCommonStruct.PartyIDtoP2PID), IsNil)
	threshold, err := conversion.GetThreshold(len(partiesID))
	c.Assert(err, IsNil)
	params := btss.NewParameters(btss.S256(), btss.NewPeerContext(partiesID), localPartyID, len(partiesID), threshold)
	party := bkg.NewLocalParty(params, make(chan btss.Message, 10), make(chan *bkg.LocalPartySaveData, 10))
	partyMap := new(sync.Map)
	partyMap.Store("", party)

	blameMgr := instance.tssCommonStruct.GetBlameMgr()
	blameMgr.SetPartyInfo(partyMap, partyIDMap)
	c.Assert(conversion.SetupIDMaps(partyIDMap, blameMgr.PartyIDtoP2PID), IsNil)
	instance.tssCommonStruct.P2PPeers = conversion.GetPeersID(instance.tssCommonStruct.PartyIDtoP2PID, instance.tssCommonStruct.GetLocalPeerID())
	return instance
}

func (s *TssECDSAKeygenCovSuite) TestProcessKeyGenTimeoutWithLastMsg(c *C) {
	for _, broadcast := range []bool{true, false} {
		instance := newBlameReadyKeyGenCov(c, 20*time.Millisecond)
		blameMgr := instance.tssCommonStruct.GetBlameMgr()
		blameMgr.SetLastMsg(&mockTssMessageCov{isBroadcast: broadcast, msgType: "KGRound2Message1"})

		result, err := instance.processKeyGen(make(chan struct{}), make(chan btss.Message),
			make(chan *bkg.LocalPartySaveData), storage.KeygenLocalState{})
		c.Assert(err, Equals, blame.ErrTssTimeOut, Commentf("broadcast=%v", broadcast))
		c.Assert(result, IsNil)
		// no peer sent anything; three missing peers exceed the threshold, so
		// nobody is blamed rather than blaming a supermajority
		c.Assert(blameMgr.GetBlame().BlameNodesCount(), Equals, 0)
	}
}

func (s *TssECDSAKeygenCovSuite) TestProcessKeyGenOutChWireError(c *C) {
	instance := newBlameReadyKeyGenCov(c, 5*time.Second)
	outCh := make(chan btss.Message, 1)
	msg := &mockTssMessageCov{isBroadcast: true, msgType: "KGRound1Message", wireErr: errors.New("no wire")}
	outCh <- msg

	result, err := instance.processKeyGen(make(chan struct{}), outCh,
		make(chan *bkg.LocalPartySaveData), storage.KeygenLocalState{})
	c.Assert(err, ErrorMatches, "fail to get wire bytes: no wire")
	c.Assert(result, IsNil)
	c.Assert(instance.tssCommonStruct.GetBlameMgr().GetLastMsg(), Equals, btss.Message(msg))
}
