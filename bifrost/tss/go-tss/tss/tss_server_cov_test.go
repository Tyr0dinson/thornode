package tss

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p-core/peer"
	"github.com/libp2p/go-libp2p-peerstore/addr"
	tsslibcommon "gitlab.com/thorchain/tss/tss-lib-private.git/v3/common"
	btsskeygen "gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/storage"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keygen"
	"gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/keysign"
	tcommon "gitlab.com/thorchain/thornode/v3/common"
)

// stateMgrCov is an in-memory LocalStateManager with injectable failures.
type stateMgrCov struct {
	mu         sync.Mutex
	state      storage.KeygenLocalState
	getErr     error
	saveBookFn func() error
	books      int
	saved      []storage.KeygenLocalState
}

func (m *stateMgrCov) SaveLocalState(state storage.KeygenLocalState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved = append(m.saved, state)
	return nil
}

func (m *stateMgrCov) GetLocalState(string) (storage.KeygenLocalState, error) {
	return m.state, m.getErr
}

func (m *stateMgrCov) SaveAddressBook(map[peer.ID]addr.AddrList) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.books++
	if m.saveBookFn != nil {
		return m.saveBookFn()
	}
	return nil
}

func (m *stateMgrCov) RetrieveP2PAddresses() (addr.AddrList, error) {
	return nil, os.ErrNotExist
}

func (m *stateMgrCov) bookCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.books
}

func loadPreParamsCov(t *testing.T, idx int) *btsskeygen.LocalPreParams {
	t.Helper()
	buf, err := os.ReadFile(path.Join(testFileLocation, preParamTestFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(buf), "\n")
	raw, err := hex.DecodeString(lines[idx])
	if err != nil {
		t.Fatal(err)
	}
	var pp btsskeygen.LocalPreParams
	if err := json.Unmarshal(raw, &pp); err != nil {
		t.Fatal(err)
	}
	return &pp
}

// loadKeysignStateCov returns the ECDSA keysign fixture whose local party is
// testPubKeys[idx].
func loadKeysignStateCov(t *testing.T, idx int) storage.KeygenLocalState {
	t.Helper()
	for i := 0; i < 4; i++ {
		buf, err := os.ReadFile(path.Join(testFileLocation, "keysign_data", "ecdsa", fmt.Sprintf("%d.json", i)))
		if err != nil {
			t.Fatal(err)
		}
		var state storage.KeygenLocalState
		if err := json.Unmarshal(buf, &state); err != nil {
			t.Fatal(err)
		}
		if state.LocalPartyKey == testPubKeys[idx] {
			return state
		}
	}
	t.Fatalf("no keysign fixture for %s", testPubKeys[idx])
	return storage.KeygenLocalState{}
}

// newTssServerCov builds a real TssServer (loopback p2p on an ephemeral port)
// through NewTss with the committed pre-params.
func newTssServerCov(t *testing.T, idx int, conf common.TssConfig, sm storage.LocalStateManager) *TssServer {
	t.Helper()
	ensureBech32Prefix()
	priv := frostPrivKeyCov(t, idx)
	raw, err := conversion.GetPriKeyRawBytes(priv)
	if err != nil {
		t.Fatal(err)
	}
	comm, err := p2p.NewCommunication(&p2p.Config{Port: 0, RendezvousString: "tss-cov-test"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := comm.Start(raw); err != nil {
		t.Fatal(err)
	}
	server, err := NewTss(comm, sm, priv, priv, conf, loadPreParamsCov(t, idx))
	if err != nil {
		_ = comm.Stop()
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)
	return server
}

func shortConfCov() common.TssConfig {
	return common.TssConfig{
		KeyGenTimeout:   time.Second,
		KeySignTimeout:  200 * time.Millisecond,
		PartyTimeout:    150 * time.Millisecond,
		PreParamTimeout: time.Second,
		EnableMonitor:   false, // Enable registers global collectors; only once per process
	}
}

// blockHeightForLeaderCov finds a block height at which the deterministic
// join-party leader for msgID is (or is not) the given peer.
func blockHeightForLeaderCov(t *testing.T, msgID string, keys []string, wantLeader string, wantIsLeader bool) int64 {
	t.Helper()
	ids, err := conversion.GetPeerIDsFromPubKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	for h := int64(1); h < 500; h++ {
		leader, err := p2p.LeaderNode(msgID, h, peerIDsToStrings(ids))
		if err != nil {
			t.Fatal(err)
		}
		if (leader == wantLeader) == wantIsLeader {
			return h
		}
	}
	t.Fatal("no block height selects the wanted leader")
	return 0
}

func TestTssServerCovBasics(t *testing.T) {
	sm := &stateMgrCov{}
	server := newTssServerCov(t, 0, shortConfCov(), sm)
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	wantPeer, err := conversion.GetPeerIDFromPubKey(testPubKeys[0])
	if err != nil {
		t.Fatal(err)
	}
	if server.GetLocalPeerID() != wantPeer.String() {
		t.Fatalf("local peer %s want %s", server.GetLocalPeerID(), wantPeer)
	}
	if peers := server.GetKnownPeers(); len(peers) != 0 {
		t.Fatalf("isolated server has peers: %+v", peers)
	}
	// NewTss derives the bech32 node pubkey from the private key
	if server.localNodePubKey != testPubKeys[0] {
		t.Fatalf("local node pubkey %s", server.localNodePubKey)
	}

	server.persistPeerAddressBook()
	if sm.bookCount() != 1 {
		t.Fatalf("address book saved %d times", sm.bookCount())
	}
	sm.saveBookFn = func() error { return errors.New("disk full") }
	server.persistPeerAddressBook() // logged, not fatal
	if sm.bookCount() != 2 {
		t.Fatalf("address book saved %d times", sm.bookCount())
	}
	var nilServer *TssServer
	nilServer.persistPeerAddressBook()
	(&TssServer{}).persistPeerAddressBook()

	if _, err := server.requestToMsgId(42); err == nil {
		t.Fatal("unknown request type accepted")
	}

	ch := make(chan struct{}, 1)
	server.setJoinPartyChan(ch)
	server.notifyJoinPartyChan()
	select {
	case <-ch:
	default:
		t.Fatal("join party channel not notified")
	}
	server.unsetJoinPartyChan()
	server.notifyJoinPartyChan() // no-op without a channel
}

func TestTssServerCovJoinPartyErrors(t *testing.T) {
	t.Parallel()
	server := newTssServerCov(t, 0, shortConfCov(), &stateMgrCov{})
	if _, _, err := server.joinParty("m", "bad", 1, testPubKeys, 2, nil); err == nil ||
		!strings.Contains(err.Error(), "fail to parse the version") {
		t.Fatalf("bad version: %v", err)
	}
	peers, leader, err := server.joinParty("m", "0.13.0", 1, []string{"bogus"}, 2, nil)
	if err == nil || leader != "NONE" || peers != nil {
		t.Fatalf("legacy bad keys: peers=%v leader=%s err=%v", peers, leader, err)
	}
	if _, _, err := server.joinParty("m", newJoinPartyVersion, 1, nil, 2, nil); err == nil ||
		!strings.Contains(err.Error(), "no participants") {
		t.Fatalf("no participants: %v", err)
	}
	if _, _, err := server.joinParty("m", newJoinPartyVersion, 1, []string{"bogus"}, 2, nil); err == nil ||
		!strings.Contains(err.Error(), "fail to convert the public key to peer ID") {
		t.Fatalf("bad keys: %v", err)
	}
}

func TestTssServerCovKeygenInvalidAlgo(t *testing.T) {
	server := newTssServerCov(t, 0, shortConfCov(), &stateMgrCov{})
	_, err := server.Keygen(keygen.NewRequest(copyTestPubKeys(), 1, newJoinPartyVersion, "bogus"))
	if err == nil || err.Error() != "invalid keygen algo" {
		t.Fatalf("invalid algo: %v", err)
	}
}

func TestTssServerCovKeygenJoinFailures(t *testing.T) {
	t.Parallel()
	server := newTssServerCov(t, 0, shortConfCov(), &stateMgrCov{})
	keys := copyTestPubKeys()
	self := server.GetLocalPeerID()

	for _, algo := range []tcommon.SigningAlgo{tcommon.SigningAlgoSecp256k1, tcommon.SigningAlgoEd25519} {
		// legacy join with unresolvable keys fails before the party starts
		resp, err := server.Keygen(keygen.NewRequest([]string{"bogus"}, 1, "0.13.0", algo))
		if err != nil || resp.Status != common.Fail || resp.Blame.FailReason != blame.InternalError {
			t.Fatalf("%s legacy bad keys: resp=%+v err=%v", algo, resp, err)
		}

		// legacy join with all peers offline blames the missing peers
		resp, err = server.Keygen(keygen.NewRequest(keys, 1, "0.13.0", algo))
		if err != nil || resp.Status != common.Fail || len(resp.Blame.BlameNodes) != 3 {
			t.Fatalf("%s legacy offline: resp=%+v err=%v", algo, resp, err)
		}
		for _, n := range resp.Blame.BlameNodes {
			if n.Pubkey == testPubKeys[0] {
				t.Fatalf("%s legacy offline blamed the local node", algo)
			}
		}

		msgID, err := server.requestToMsgId(keygen.NewRequest(keys, 0, newJoinPartyVersion, algo))
		if err != nil {
			t.Fatal(err)
		}
		// a remote (offline) leader is blamed
		h := blockHeightForLeaderCov(t, msgID, keys, self, false)
		resp, err = server.Keygen(keygen.NewRequest(keys, h, newJoinPartyVersion, algo))
		if err != nil || resp.Status != common.Fail || len(resp.Blame.BlameNodes) == 0 {
			t.Fatalf("%s remote leader: resp=%+v err=%v", algo, resp, err)
		}
		// as leader with nobody joining the party cannot be formed
		h = blockHeightForLeaderCov(t, msgID, keys, self, true)
		resp, err = server.Keygen(keygen.NewRequest(keys, h, newJoinPartyVersion, algo))
		if err != nil || resp.Status != common.Fail || resp.PubKey != "" {
			t.Fatalf("%s local leader: resp=%+v err=%v", algo, resp, err)
		}
	}
}

func TestTssServerCovKeygenAllAlgoJoinFailures(t *testing.T) {
	t.Parallel()
	server := newTssServerCov(t, 0, shortConfCov(), &stateMgrCov{})
	keys := copyTestPubKeys()
	self := server.GetLocalPeerID()

	resps, err := server.KeygenAllAlgo(keygen.NewRequest([]string{"bogus"}, 1, "0.13.0", ""))
	if err != nil || len(resps) != 1 || resps[0].Status != common.Fail || resps[0].Blame.FailReason != blame.InternalError {
		t.Fatalf("legacy bad keys: %+v err=%v", resps, err)
	}
	resps, err = server.KeygenAllAlgo(keygen.NewRequest(keys, 1, "0.13.0", ""))
	if err != nil || len(resps) != 1 || resps[0].Status != common.Fail || len(resps[0].Blame.BlameNodes) != 3 {
		t.Fatalf("legacy offline: %+v err=%v", resps, err)
	}

	msgID, err := server.requestToMsgId(keygen.NewRequest(keys, 0, newJoinPartyVersion, ""))
	if err != nil {
		t.Fatal(err)
	}
	h := blockHeightForLeaderCov(t, msgID, keys, self, false)
	resps, err = server.KeygenAllAlgo(keygen.NewRequest(keys, h, newJoinPartyVersion, ""))
	if err != nil || len(resps) != 1 || resps[0].Status != common.Fail || len(resps[0].Blame.BlameNodes) == 0 {
		t.Fatalf("remote leader: %+v err=%v", resps, err)
	}
	h = blockHeightForLeaderCov(t, msgID, keys, self, true)
	resps, err = server.KeygenAllAlgo(keygen.NewRequest(keys, h, newJoinPartyVersion, ""))
	if err != nil || len(resps) != 1 || resps[0].Status != common.Fail {
		t.Fatalf("local leader: %+v err=%v", resps, err)
	}
}

func TestTssServerCovKeySignValidation(t *testing.T) {
	state := loadKeysignStateCov(t, 0)
	sm := &stateMgrCov{state: state}
	server := newTssServerCov(t, 0, shortConfCov(), sm)
	msg := base64.StdEncoding.EncodeToString(hash([]byte("cov")))
	others := []string{}
	for _, k := range state.ParticipantKeys {
		if k != testPubKeys[0] {
			others = append(others, k)
		}
	}

	sm.getErr = errors.New("no state")
	if _, err := server.KeySign(keysign.NewRequest(state.PubKey, tcommon.SigningAlgoSecp256k1, []string{msg}, 1, nil, newJoinPartyVersion)); err == nil ||
		!strings.Contains(err.Error(), "fail to get local keygen state") {
		t.Fatalf("state error: %v", err)
	}
	sm.getErr = nil

	if _, err := server.KeySign(keysign.NewRequest(state.PubKey, tcommon.SigningAlgoSecp256k1, []string{msg}, 1, []string{testPubKeys[0], "not-a-member"}, newJoinPartyVersion)); err == nil ||
		!strings.Contains(err.Error(), "is not in local keygen state") {
		t.Fatalf("foreign signer: %v", err)
	}
	if _, err := server.KeySign(keysign.NewRequest(state.PubKey, tcommon.SigningAlgoSecp256k1, []string{msg}, 1, []string{testPubKeys[0], others[0]}, newJoinPartyVersion)); err == nil ||
		err.Error() != "not enough signers" {
		t.Fatalf("too few signers: %v", err)
	}
	// local node is not in the selected signing party: nothing to do
	resp, err := server.KeySign(keysign.NewRequest(state.PubKey, tcommon.SigningAlgoSecp256k1, []string{msg}, 1, others, newJoinPartyVersion))
	if err != nil || resp.Status != 0 || len(resp.Signatures) != 0 {
		t.Fatalf("non-member: resp=%+v err=%v", resp, err)
	}
	if _, err := server.KeySign(keysign.NewRequest(state.PubKey, "bogus", []string{msg}, 1, nil, newJoinPartyVersion)); err == nil ||
		!strings.Contains(err.Error(), "unsupported signing algo") {
		t.Fatalf("bad algo: %v", err)
	}
	if _, err := server.KeySign(keysign.NewRequest(state.PubKey, tcommon.SigningAlgoSecp256k1, []string{"%%%"}, 1, nil, newJoinPartyVersion)); err == nil ||
		!strings.Contains(err.Error(), "fail to decode message") {
		t.Fatalf("bad message: %v", err)
	}
	resp, err = server.KeySign(keysign.NewRequest(state.PubKey, tcommon.SigningAlgoEd25519, []string{msg}, 1, nil, "bad-version"))
	if err == nil || resp.Status != common.Fail || resp.Blame.FailReason != blame.InternalError {
		t.Fatalf("bad version: resp=%+v err=%v", resp, err)
	}

	sm.state = storage.KeygenLocalState{}
	if _, err := server.KeySign(keysign.NewRequest(state.PubKey, tcommon.SigningAlgoSecp256k1, []string{msg}, 1, nil, newJoinPartyVersion)); err == nil ||
		!strings.Contains(err.Error(), "local signer membership is empty") {
		t.Fatalf("empty membership: %v", err)
	}
}

func TestTssServerCovKeySignJoinFailure(t *testing.T) {
	t.Parallel()
	state := loadKeysignStateCov(t, 0)
	server := newTssServerCov(t, 0, shortConfCov(), &stateMgrCov{state: state})
	msg := base64.StdEncoding.EncodeToString(hash([]byte("cov-join")))

	for _, version := range []string{"0.13.0", newJoinPartyVersion} {
		resp, err := server.KeySign(keysign.NewRequest(state.PubKey, tcommon.SigningAlgoSecp256k1, []string{msg}, 7, nil, version))
		if err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		if resp.Status != common.Fail || len(resp.Signatures) != 0 {
			t.Fatalf("%s: expected join failure, got %+v", version, resp)
		}
		if version == newJoinPartyVersion && resp.Blame.FailReason != blame.TssSyncFail {
			t.Fatalf("%s: leader join failure must be a sync failure: %+v", version, resp.Blame)
		}
	}
}

func TestTssServerCovGenerateSignatureBadParticipants(t *testing.T) {
	server := newTssServerCov(t, 0, shortConfCov(), &stateMgrCov{})
	resp, err := server.generateSignature("m", nil, keysign.Request{}, 1, []string{"bogus"},
		storage.KeygenLocalState{}, nil, nil, make(chan string, 2))
	if err != nil || resp.Status != common.Fail || resp.Blame.FailReason != blame.InternalError {
		t.Fatalf("bad participants: resp=%+v err=%v", resp, err)
	}
}

func TestTssServerCovBatchSignatures(t *testing.T) {
	server := newTssServerCov(t, 0, shortConfCov(), &stateMgrCov{})
	m1 := hash([]byte("one"))
	m2 := hash([]byte("two"))
	sigs := []*tsslibcommon.SignatureData{
		{M: m2, R: []byte{2}, S: []byte{3}, SignatureRecovery: []byte{1}, Signature: []byte{9}},
		{M: m1, R: []byte{4}, S: []byte{5}, SignatureRecovery: []byte{0}, Signature: []byte{8}},
	}
	// pairing is by content, not position
	resp := server.batchSignatures(tcommon.SigningAlgoSecp256k1, sigs, [][]byte{m1, m2})
	if resp.Status != common.Success || len(resp.Signatures) != 2 {
		t.Fatalf("batch: %+v", resp)
	}
	if resp.Signatures[0].Msg != base64.StdEncoding.EncodeToString(m2) || resp.Signatures[0].R != base64.StdEncoding.EncodeToString([]byte{2}) {
		t.Fatalf("signature not paired with its message: %+v", resp.Signatures[0])
	}
	resp = server.batchSignatures(tcommon.SigningAlgoEd25519, sigs[:1], [][]byte{m2})
	if resp.Status != common.Success || resp.Signatures[0].R != "" || resp.Signatures[0].S != base64.StdEncoding.EncodeToString([]byte{9}) {
		t.Fatalf("eddsa batch: %+v", resp)
	}
	// a signature for a message that was not requested fails the batch
	resp = server.batchSignatures(tcommon.SigningAlgoSecp256k1, sigs, [][]byte{m1})
	if resp.Status != common.Fail || resp.Blame.FailReason != blame.InternalError {
		t.Fatalf("unpaired signature: %+v", resp)
	}
}

func TestTssServerCovNewTssRejectsBadPubKey(t *testing.T) {
	ensureBech32Prefix()
	priv := frostPrivKeyCov(t, 1)
	raw, err := conversion.GetPriKeyRawBytes(priv)
	if err != nil {
		t.Fatal(err)
	}
	comm, err := p2p.NewCommunication(&p2p.Config{Port: 0, RendezvousString: "tss-cov-test"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := comm.Start(raw); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = comm.Stop() }()
	// a valid pre-param set is used as-is (no regeneration)
	pp := loadPreParamsCov(t, 1)
	server, err := NewTss(comm, &stateMgrCov{}, priv, priv, common.TssConfig{PartyTimeout: time.Second}, pp)
	if err != nil {
		t.Fatal(err)
	}
	if server.preParams != pp || server.localNodePubKey != testPubKeys[1] || server.tssMetrics == nil {
		t.Fatalf("unexpected server state")
	}
	server.partyCoordinator.Stop()
}

func TestTssServerCovKeygenSingleNodeFailsToStart(t *testing.T) {
	server := newTssServerCov(t, 0, shortConfCov(), &stateMgrCov{})
	single := []string{testPubKeys[0]}

	// a one-party ECDSA keygen cannot start; the cause is surfaced on the
	// blame fail reason and the error is returned
	resps, err := server.KeygenAllAlgo(keygen.NewRequest(single, 1, newJoinPartyVersion, ""))
	if err == nil || !strings.Contains(err.Error(), "fail to start local party") {
		t.Fatalf("expected start failure, got %v", err)
	}
	if len(resps) != 1 || resps[0].Status != common.Fail || resps[0].Blame.FailReason != err.Error() {
		t.Fatalf("unexpected responses: %+v", resps)
	}

	for _, algo := range []tcommon.SigningAlgo{tcommon.SigningAlgoSecp256k1, tcommon.SigningAlgoEd25519} {
		resp, err := server.Keygen(keygen.NewRequest(single, 1, newJoinPartyVersion, algo))
		if err == nil || !strings.Contains(err.Error(), "fail to start local party") {
			t.Fatalf("%s: expected start failure, got %v", algo, err)
		}
		if resp.Status != common.Fail || resp.PubKey != "" || resp.Algo != algo {
			t.Fatalf("%s: unexpected response %+v", algo, resp)
		}
	}
}
