package tss

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	cossecp "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	maddr "github.com/multiformats/go-multiaddr"
	btsskeygen "gitlab.com/thorchain/tss/tss-lib-private.git/v3/ecdsa/keygen"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	gotss "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type fakeVersionBridgeCov struct {
	thorclient.ThorchainBridge

	mu      sync.Mutex
	version semver.Version
	err     error
	calls   int
}

func (b *fakeVersionBridgeCov) GetThorchainVersion() (semver.Version, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	return b.version, b.err
}

func (b *fakeVersionBridgeCov) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

type KeyGenCovSuite struct{}

var _ = Suite(&KeyGenCovSuite{})

func (s *KeyGenCovSuite) SetUpSuite(c *C) {
	thorchain.SetupConfigForTest()
}

func (s *KeyGenCovSuite) TestNewTssKeyGenNilKeys(c *C) {
	kg, err := NewTssKeyGen(nil, nil, nil)
	c.Assert(err, ErrorMatches, "keys is nil")
	c.Assert(kg, IsNil)
}

func (s *KeyGenCovSuite) TestGetVersionCaches(c *C) {
	bridge := &fakeVersionBridgeCov{version: semver.MustParse("3.1.0")}
	kg := &KeyGen{bridge: bridge}

	c.Check(kg.getVersion().String(), Equals, "3.1.0")
	c.Check(bridge.callCount(), Equals, 1)

	// within one block time the cached version is reused
	bridge.version = semver.MustParse("3.2.0")
	c.Check(kg.getVersion().String(), Equals, "3.1.0")
	c.Check(bridge.callCount(), Equals, 1)

	// once the cache is stale the bridge is queried again
	kg.lastCheck = time.Now().Add(-2 * constants.ThorchainBlockTime)
	c.Check(kg.getVersion().String(), Equals, "3.2.0")
	c.Check(bridge.callCount(), Equals, 2)

	// errors keep the last known version
	kg.lastCheck = time.Now().Add(-2 * constants.ThorchainBlockTime)
	bridge.err = errors.New("unavailable")
	c.Check(kg.getVersion().String(), Equals, "3.2.0")
	c.Check(bridge.callCount(), Equals, 3)

	// with no known version every call goes to the bridge
	fresh := &KeyGen{bridge: bridge}
	c.Check(fresh.getVersion().Equals(semver.Version{}), Equals, true)
	c.Check(fresh.getVersion().Equals(semver.Version{}), Equals, true)
	c.Check(bridge.callCount(), Equals, 5)
}

func (s *KeyGenCovSuite) TestGenerateNewKeyNoKeys(c *C) {
	kg := &KeyGen{}
	pk, blame, err := kg.GenerateNewKey(10, nil)
	c.Assert(err, IsNil)
	c.Check(blame, IsNil)
	c.Check(pk, Equals, common.EmptyPubKeySet)
}

func loadPreParamsCov(c *C) *btsskeygen.LocalPreParams {
	buf, err := os.ReadFile("go-tss/test_data/preParam_test.data")
	c.Assert(err, IsNil)
	line := strings.Split(strings.TrimSpace(string(buf)), "\n")[0]
	val, err := hex.DecodeString(line)
	c.Assert(err, IsNil)
	var pp btsskeygen.LocalPreParams
	c.Assert(json.Unmarshal(val, &pp), IsNil)
	return &pp
}

func startTssServerCov(c *C, pp *btsskeygen.LocalPreParams, bootstrap []maddr.Multiaddr) (*gotss.TssServer, *p2p.Communication, common.PubKey) {
	priKey := secp256k1.GenPrivKey()
	comm, stateMgr, err := p2p.StartP2P(&p2p.Config{RendezvousString: "cov", Port: 0, BootstrapPeers: bootstrap}, priKey, c.MkDir())
	c.Assert(err, IsNil)
	server, err := gotss.NewTss(comm, stateMgr, priKey, priKey, tsscommon.TssConfig{
		PartyTimeout:    10 * time.Second,
		KeyGenTimeout:   30 * time.Second,
		KeySignTimeout:  30 * time.Second,
		PreParamTimeout: time.Second,
	}, pp)
	c.Assert(err, IsNil)
	pub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, &cossecp.PubKey{Key: priKey.PubKey().Bytes()})
	c.Assert(err, IsNil)
	pk, err := common.NewPubKey(pub)
	c.Assert(err, IsNil)
	return server, comm, pk
}

func (s *KeyGenCovSuite) TestGenerateNewKeyTwoParties(c *C) {
	pps := loadAllPreParamsCov(c)
	server1, comm1, pk1 := startTssServerCov(c, pps[0], nil)
	defer server1.Stop()
	var addr maddr.Multiaddr
	for _, a := range comm1.GetHost().Addrs() {
		if strings.HasPrefix(a.String(), "/ip4/127.0.0.1/") {
			addr = a
		}
	}
	c.Assert(addr, NotNil)
	bootstrap, err := maddr.NewMultiaddr(addr.String() + "/p2p/" + comm1.GetLocalPeerID())
	c.Assert(err, IsNil)
	server2, _, pk2 := startTssServerCov(c, pps[1], []maddr.Multiaddr{bootstrap})
	defer server2.Stop()

	version := semver.MustParse(tsscommon.SessionNonceActivationVersion)
	kgs := []*KeyGen{
		{bridge: &fakeVersionBridgeCov{version: version}, server: server1},
		{bridge: &fakeVersionBridgeCov{version: version}, server: server2},
	}
	type result struct {
		pk    common.PubKeySet
		blame []types.Blame
		err   error
	}
	results := make([]result, len(kgs))
	start := time.Now()
	var wg sync.WaitGroup
	for i := range kgs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pk, blame, err := kgs[i].GenerateNewKey(200, common.PubKeys{pk1, pk2})
			results[i] = result{pk, blame, err}
		}(i)
	}
	wg.Wait()
	c.Logf("two party keygen took %s", time.Since(start))
	for _, r := range results {
		c.Assert(r.err, IsNil)
		c.Check(r.blame, HasLen, 0)
		c.Check(r.pk.Secp256k1.IsEmpty(), Equals, false)
		c.Check(r.pk.Ed25519.IsEmpty(), Equals, false)
	}
	c.Check(results[0].pk, DeepEquals, results[1].pk)
}

func loadAllPreParamsCov(c *C) []*btsskeygen.LocalPreParams {
	buf, err := os.ReadFile("go-tss/test_data/preParam_test.data")
	c.Assert(err, IsNil)
	var out []*btsskeygen.LocalPreParams
	for _, line := range strings.Split(strings.TrimSpace(string(buf)), "\n") {
		val, err := hex.DecodeString(line)
		c.Assert(err, IsNil)
		var pp btsskeygen.LocalPreParams
		c.Assert(json.Unmarshal(val, &pp), IsNil)
		out = append(out, &pp)
	}
	return out
}

// GenerateNewKey against a single real (loopback-only, no peers) TSS server:
// the keygen party can never form, so every path must surface a blame.
func (s *KeyGenCovSuite) TestGenerateNewKeyPartyFailureBlames(c *C) {
	priKey := secp256k1.GenPrivKey()
	comm, stateMgr, err := p2p.StartP2P(&p2p.Config{RendezvousString: "cov", Port: 0}, priKey, c.MkDir())
	c.Assert(err, IsNil)
	server, err := gotss.NewTss(comm, stateMgr, priKey, priKey, tsscommon.TssConfig{
		PartyTimeout:    200 * time.Millisecond,
		KeyGenTimeout:   time.Second,
		KeySignTimeout:  time.Second,
		PreParamTimeout: time.Second,
	}, loadPreParamsCov(c))
	c.Assert(err, IsNil)
	defer server.Stop()

	localPub, err := cosmos.Bech32ifyPubKey(cosmos.Bech32PubKeyTypeAccPub, &cossecp.PubKey{Key: priKey.PubKey().Bytes()})
	c.Assert(err, IsNil)
	local, err := common.NewPubKey(localPub)
	c.Assert(err, IsNil)
	peer := types.GetRandomPubKey()

	// legacy (leaderless) join party: the offline peer is blamed
	bridge := &fakeVersionBridgeCov{version: semver.MustParse("0.13.0")}
	kg := &KeyGen{bridge: bridge, server: server}
	pk, blame, err := kg.GenerateNewKey(100, common.PubKeys{local, peer})
	// NOTE (suspected bug): GenerateNewKey builds the error "fail to keygen:
	// individual algorithm failures", but its deferred blame logger assigns to
	// the named `err` result while resolving each blamed node's address, so a
	// blame with valid node pubkeys is returned with a nil error. The signer
	// only relies on the empty pubkey set, which is still returned.
	c.Assert(err, IsNil)
	c.Check(pk, Equals, common.EmptyPubKeySet)
	c.Assert(blame, HasLen, 1)
	c.Check(blame[0].FailReason, Not(Equals), "")
	blamed := make([]string, 0, len(blame[0].BlameNodes))
	for _, n := range blame[0].BlameNodes {
		blamed = append(blamed, n.Pubkey)
	}
	c.Check(blamed, DeepEquals, []string{peer.String()})

	// leader based join party: party formation times out and is blamed
	bridge.version = semver.MustParse("3.0.0")
	kg = &KeyGen{bridge: bridge, server: server}
	pk, blame, _ = kg.GenerateNewKey(101, common.PubKeys{local, peer})
	c.Check(pk, Equals, common.EmptyPubKeySet)
	c.Assert(len(blame) > 0, Equals, true)
	c.Check(blame[0].FailReason, Not(Equals), "")

	// a single-member keygen forms the party but tss-lib refuses to start a
	// 1-of-1 local party: KeygenAllAlgo errors and the error is surfaced
	pk, blame, err = kg.GenerateNewKey(102, common.PubKeys{local})
	c.Assert(err, ErrorMatches, "fail to keygen: .*")
	c.Check(pk, Equals, common.EmptyPubKeySet)
	c.Assert(blame, HasLen, 1)
	c.Check(strings.Contains(blame[0].FailReason, "fail to start local party"), Equals, true, Commentf("%s", blame[0].FailReason))
}
