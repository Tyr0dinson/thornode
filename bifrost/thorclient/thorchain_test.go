package thorclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	cometed25519 "github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/hashicorp/go-retryablehttp"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/cmd"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestPackage(t *testing.T) { TestingT(t) }

type ThorchainSuite struct {
	server             *httptest.Server
	cfg                config.BifrostClientConfiguration
	bridge             *thorchainBridge
	authAccountFixture string
	nodeAccountFixture string
}

var _ = Suite(&ThorchainSuite{})

func (s *ThorchainSuite) SetUpTest(c *C) {
	cfg2 := cosmos.GetConfig()
	cfg2.SetBech32PrefixForAccount("tthor", "tthorpub")
	cfg, _, kb := SetupThorchainForTest(c)
	s.cfg = cfg
	s.server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasPrefix(req.RequestURI, AuthAccountEndpoint):
			httpTestHandler(c, rw, s.authAccountFixture)
		case strings.HasPrefix(req.RequestURI, NodeAccountEndpoint):
			httpTestHandler(c, rw, s.nodeAccountFixture)
		case strings.HasPrefix(req.RequestURI, LastBlockEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/lastblock/eth.json")
		case strings.HasPrefix(req.RequestURI, StatusEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/status/status.json")
		case strings.HasPrefix(req.RequestURI, KeysignEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/keysign/template.json")
		case strings.HasPrefix(req.RequestURI, AsgardVault):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/vaults/asgard.json")
		case strings.HasPrefix(req.RequestURI, PubKeysEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/vaults/pubKeys.json")
		case strings.EqualFold(req.RequestURI, BroadcastTxsEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/txs/success.json")
		case strings.HasPrefix(req.RequestURI, ThorchainConstants):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/constants/constants.json")
		case strings.HasPrefix(req.RequestURI, RagnarokEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/ragnarok/ragnarok.json")
		case strings.HasPrefix(req.RequestURI, ChainVersionEndpoint):
			_, err := rw.Write([]byte(`{"current":"` + stypes.GetCurrentVersion().String() + `"}`))
			c.Assert(err, IsNil)
		case strings.HasPrefix(req.RequestURI, MimirEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/mimir/mimir.json")
		case strings.HasPrefix(req.RequestURI, InboundAddressesEndpoint):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/inbound_addresses/inbound_addresses.json")
		case strings.HasPrefix(req.RequestURI, "/thorchain/thorname/"):
			httpTestHandler(c, rw, "../../test/fixtures/endpoints/thorname/thorname.json")
		}
	}))
	s.cfg.ChainHost = s.server.Listener.Addr().String()
	s.cfg.ChainRPC = s.server.Listener.Addr().String()

	var err error
	bridge, err := NewThorchainBridge(s.cfg, GetMetricForTest(c), NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd))
	var ok bool
	s.bridge, ok = bridge.(*thorchainBridge)
	c.Assert(ok, Equals, true)
	s.bridge.httpClient.RetryMax = 1 // fail fast
	c.Assert(err, IsNil)
	c.Assert(s.bridge, NotNil)
}

func (s *ThorchainSuite) TearDownTest(c *C) {
	s.server.Close()
}

func (s *ThorchainSuite) TestGetThorChainURL(c *C) {
	uri := s.bridge.getThorChainURL("")
	c.Assert(uri, Equals, "http://"+s.server.Listener.Addr().String())
}

func TestGetKeysignPartyReadsVaultMembership(t *testing.T) {
	vaultPubKey := stypes.GetRandomPubKey()
	members := common.PubKeys{
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
	}
	var requestedVault bool
	var requestedNodes bool
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/thorchain/vault/" + vaultPubKey.String():
			requestedVault = true
			if err := json.NewEncoder(rw).Encode(map[string]any{
				"block_height": 100,
				"pub_key":      vaultPubKey.String(),
				"coins":        []string{},
				"type":         "AsgardVault",
				"status":       "ActiveVault",
				"membership":   members.Strings(),
			}); err != nil {
				t.Fatalf("failed to encode vault response: %v", err)
			}
		case NodeAccountsEndpoint:
			requestedNodes = true
			nodes := []*stypes.QueryNodeResponse{
				{PubKeySet: common.PubKeySet{Secp256k1: members[0]}},
				{PubKeySet: common.PubKeySet{Secp256k1: members[1]}},
			}
			if err := json.NewEncoder(rw).Encode(nodes); err != nil {
				t.Fatalf("failed to encode nodes response: %v", err)
			}
		default:
			http.NotFound(rw, req)
		}
	}))
	defer server.Close()

	httpClient := retryablehttp.NewClient()
	httpClient.Logger = nil
	bridge := &thorchainBridge{
		cfg:        config.BifrostClientConfiguration{ChainHost: server.Listener.Addr().String()},
		httpClient: httpClient,
	}

	party, err := bridge.GetKeysignPartyAtHeight(vaultPubKey, 100)
	if err != nil {
		t.Fatalf("GetKeysignParty returned error: %v", err)
	}
	if !requestedVault || !requestedNodes {
		t.Fatalf("expected vault and nodes endpoints to be requested")
	}
	if !party.Equals(members) {
		t.Fatalf("expected members %v, got %v", members, party)
	}
}

func TestGetKeysignPartyExcludesForcedToLeave(t *testing.T) {
	vaultPubKey := stypes.GetRandomPubKey()
	members := common.PubKeys{
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
	}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/thorchain/vault/" + vaultPubKey.String():
			if err := json.NewEncoder(rw).Encode(map[string]any{
				"block_height": 100,
				"pub_key":      vaultPubKey.String(),
				"coins":        []string{},
				"type":         "AsgardVault",
				"status":       "ActiveVault",
				"membership":   members.Strings(),
			}); err != nil {
				t.Fatalf("failed to encode vault response: %v", err)
			}
		case NodeAccountsEndpoint:
			nodes := []*stypes.QueryNodeResponse{
				{PubKeySet: common.PubKeySet{Secp256k1: members[0]}},
				{PubKeySet: common.PubKeySet{Secp256k1: members[1]}, ForcedToLeave: true},
				{PubKeySet: common.PubKeySet{Secp256k1: members[2]}},
			}
			if err := json.NewEncoder(rw).Encode(nodes); err != nil {
				t.Fatalf("failed to encode nodes response: %v", err)
			}
		default:
			http.NotFound(rw, req)
		}
	}))
	defer server.Close()

	httpClient := retryablehttp.NewClient()
	httpClient.Logger = nil
	bridge := &thorchainBridge{
		cfg:        config.BifrostClientConfiguration{ChainHost: server.Listener.Addr().String()},
		httpClient: httpClient,
	}

	party, err := bridge.GetKeysignPartyAtHeight(vaultPubKey, 100)
	if err != nil {
		t.Fatalf("GetKeysignParty returned error: %v", err)
	}
	expected := common.PubKeys{members[0], members[2]}
	if !party.Equals(expected) {
		t.Fatalf("expected members %v, got %v", expected, party)
	}
}

func TestGetKeysignPartyExcludesMembersMissingNodeState(t *testing.T) {
	vaultPubKey := stypes.GetRandomPubKey()
	members := common.PubKeys{
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
	}
	bridge, server, _ := newKeysignPartyTestBridge(t, vaultPubKey, members, keysignPartyTestNodes(common.PubKeys{members[0], members[1], members[2]}, nil))
	defer server.Close()

	party, err := bridge.GetKeysignPartyAtHeight(vaultPubKey, 100)
	if err != nil {
		t.Fatalf("GetKeysignParty returned error: %v", err)
	}
	expected := common.PubKeys{members[0], members[1], members[2]}
	if !party.Equals(expected) {
		t.Fatalf("expected members %v, got %v", expected, party)
	}
}

func TestGetKeysignPartyErrorsWhenAllMembersMissingNodeState(t *testing.T) {
	vaultPubKey := stypes.GetRandomPubKey()
	members := common.PubKeys{
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
	}
	bridge, server, _ := newKeysignPartyTestBridge(t, vaultPubKey, members, nil)
	defer server.Close()

	_, err := bridge.GetKeysignPartyAtHeight(vaultPubKey, 100)
	if err == nil {
		t.Fatal("expected no eligible signer membership error")
	}
	if !strings.Contains(err.Error(), "not enough eligible signers") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetKeysignPartySkipsJailedMemberWhenCleanMembersMeetThreshold(t *testing.T) {
	vaultPubKey := stypes.GetRandomPubKey()
	members := common.PubKeys{
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
	}
	bridge, server, _ := newKeysignPartyTestBridge(t, vaultPubKey, members, keysignPartyTestNodesWithState(members, map[string]keysignPartyNodeState{
		members[1].String(): {jail: &stypes.NodeJail{ReleaseHeight: 200, Reason: "test jail"}},
	}))
	defer server.Close()

	party, err := bridge.GetKeysignPartyAtHeight(vaultPubKey, 100)
	if err != nil {
		t.Fatalf("GetKeysignParty returned error: %v", err)
	}
	expected := common.PubKeys{members[0], members[2], members[3]}
	if !party.Equals(expected) {
		t.Fatalf("expected members %v, got %v", expected, party)
	}
}

func TestGetKeysignPartyUsesJailedFallbackWhenNeeded(t *testing.T) {
	vaultPubKey := stypes.GetRandomPubKey()
	members := common.PubKeys{
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
	}
	bridge, server, _ := newKeysignPartyTestBridge(t, vaultPubKey, members, keysignPartyTestNodesWithState(members, map[string]keysignPartyNodeState{
		members[1].String(): {jail: &stypes.NodeJail{ReleaseHeight: 200, Reason: "test jail"}},
		members[2].String(): {jail: &stypes.NodeJail{ReleaseHeight: 200, Reason: "test jail"}},
	}))
	defer server.Close()

	party, err := bridge.GetKeysignPartyAtHeight(vaultPubKey, 100)
	if err != nil {
		t.Fatalf("GetKeysignParty returned error: %v", err)
	}
	expected := common.PubKeys{members[0], members[1], members[3]}
	if !party.Equals(expected) {
		t.Fatalf("expected members %v, got %v", expected, party)
	}
}

func TestGetKeysignPartyTreatsReleasedJailAsPreferred(t *testing.T) {
	vaultPubKey := stypes.GetRandomPubKey()
	members := common.PubKeys{
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
	}
	bridge, server, _ := newKeysignPartyTestBridge(t, vaultPubKey, members, keysignPartyTestNodesWithState(members, map[string]keysignPartyNodeState{
		members[1].String(): {jail: &stypes.NodeJail{ReleaseHeight: 80, Reason: "test jail"}},
	}))
	defer server.Close()

	party, err := bridge.GetKeysignPartyAtHeight(vaultPubKey, 100)
	if err != nil {
		t.Fatalf("GetKeysignParty returned error: %v", err)
	}
	if !party.Equals(members) {
		t.Fatalf("expected members %v, got %v", members, party)
	}
}

func TestGetKeysignPartyUsesCurrentRoundedHeight(t *testing.T) {
	vaultPubKey := stypes.GetRandomPubKey()
	members := common.PubKeys{
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
		stypes.GetRandomPubKey(),
	}
	bridge, server, requestedPaths := newKeysignPartyTestBridge(t, vaultPubKey, members, keysignPartyTestNodesWithState(members, map[string]keysignPartyNodeState{
		members[1].String(): {jail: &stypes.NodeJail{ReleaseHeight: 80, Reason: "test jail"}},
	}))
	defer server.Close()

	party, err := bridge.GetKeysignParty(vaultPubKey)
	if err != nil {
		t.Fatalf("GetKeysignParty returned error: %v", err)
	}
	if !containsTestPath(*requestedPaths, LastBlockEndpoint) {
		t.Fatalf("expected last block endpoint, got %v", *requestedPaths)
	}
	if !party.Equals(members) {
		t.Fatalf("expected released jailed member to be preferred at current rounded height, got %v", party)
	}
}

// newKeysignPartyTestBridge creates a minimal thornode HTTP fixture for the
// vault and node-account endpoints used by GetKeysignParty.
func newKeysignPartyTestBridge(t *testing.T, vaultPubKey common.PubKey, members common.PubKeys, nodes []*stypes.QueryNodeResponse) (*thorchainBridge, *httptest.Server, *[]string) {
	t.Helper()

	requestedPaths := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		switch req.URL.Path {
		case "/thorchain/vault/" + vaultPubKey.String():
			if err := json.NewEncoder(rw).Encode(map[string]any{
				"block_height": 100,
				"pub_key":      vaultPubKey.String(),
				"coins":        []string{},
				"type":         "AsgardVault",
				"status":       "ActiveVault",
				"membership":   members.Strings(),
			}); err != nil {
				t.Fatalf("failed to encode vault response: %v", err)
			}
		case NodeAccountsEndpoint:
			nodes := []*stypes.QueryNodeResponse{
				{PubKeySet: common.PubKeySet{Secp256k1: members[0]}},
			}
			if err := json.NewEncoder(rw).Encode(nodes); err != nil {
				t.Fatalf("failed to encode nodes response: %v", err)
			}
		case LastBlockEndpoint:
			if err := json.NewEncoder(rw).Encode([]map[string]any{
				{
					"chain":     "THOR",
					"thorchain": int64(127),
				},
			}); err != nil {
				t.Fatalf("failed to encode last block response: %v", err)
			}
		case LastBlockEndpoint:
			if err := json.NewEncoder(rw).Encode([]map[string]any{
				{
					"chain":     "THOR",
					"thorchain": int64(127),
				},
			}); err != nil {
				t.Fatalf("failed to encode last block response: %v", err)
			}
		default:
			http.NotFound(rw, req)
		}
	}))
	defer server.Close()

	httpClient := retryablehttp.NewClient()
	httpClient.Logger = nil
	bridge := &thorchainBridge{
		cfg:        config.BifrostClientConfiguration{ChainHost: server.Listener.Addr().String()},
		httpClient: httpClient,
	}
	return bridge, server, &requestedPaths
}

// keysignPartyTestNodes builds node-account responses keyed by the provided
// secp256k1 vault members, with optional forced-to-leave flags.
func keysignPartyTestNodes(members common.PubKeys, forced map[string]bool) []*stypes.QueryNodeResponse {
	states := make(map[string]keysignPartyNodeState, len(forced))
	for member, isForced := range forced {
		states[member] = keysignPartyNodeState{forced: isForced}
	}
	return keysignPartyTestNodesWithState(members, states)
}

type keysignPartyNodeState struct {
	forced bool
	jail   *stypes.NodeJail
}

func keysignPartyTestNodesWithState(members common.PubKeys, states map[string]keysignPartyNodeState) []*stypes.QueryNodeResponse {
	nodes := make([]*stypes.QueryNodeResponse, 0, len(members))
	for _, member := range members {
		state := states[member.String()]
		nodes = append(nodes, &stypes.QueryNodeResponse{
			PubKeySet: common.PubKeySet{
				Secp256k1: member,
			},
			ForcedToLeave: state.forced,
			Jail:          state.jail,
		})
	}
	return nodes
}

// containsTestPath reports whether the fixture server received the expected
// endpoint request.
func containsTestPath(paths []string, path string) bool {
	for _, item := range paths {
		if item == path {
			return true
		}
	}
	return false
}

func httpTestHandler(c *C, rw http.ResponseWriter, fixture string) {
	var content []byte
	var err error

	switch fixture {
	case "500":
		rw.WriteHeader(http.StatusInternalServerError)
	default:
		content, err = os.ReadFile(fixture)
		if err != nil {
			c.Fatal(err)
		}
	}

	rw.Header().Set("Content-Type", "application/json")
	if _, err = rw.Write(content); err != nil {
		c.Fatal(err)
	}
}

func (s *ThorchainSuite) TestGet(c *C) {
	buf, status, err := s.bridge.getWithPath("")
	c.Check(status, Equals, http.StatusOK)
	c.Assert(err, IsNil)
	c.Assert(buf, NotNil)
}

func (s *ThorchainSuite) TestGetObservationsStdTx_OutboundShouldHaveConfirmationCounting(c *C) {
	pk := stypes.GetRandomPubKey()
	vaultAddr, err := pk.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(
		common.Tx{
			Coins: common.Coins{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(123400000)),
			},
			Memo:        "This is my memo!",
			FromAddress: vaultAddr,
			ToAddress:   "0xd58610f89265a2fb637ac40edf59141ff873b266",
		},
		1,
		pk,
		100,
	)

	_, out, err := s.bridge.GetInboundOutbound(common.ObservedTxs{tx})
	c.Assert(out, NotNil)
	c.Assert(err, IsNil)
	c.Assert(out[0].FinaliseHeight > out[0].BlockHeight, Equals, true)
}

func (s *ThorchainSuite) TestSign(c *C) {
	pk := stypes.GetRandomPubKey()
	vaultAddr, err := pk.GetAddress(common.ETHChain)
	c.Assert(err, IsNil)
	tx := common.NewObservedTx(
		common.Tx{
			Coins: common.Coins{
				common.NewCoin(common.ETHAsset, cosmos.NewUint(123400000)),
			},
			Memo:        "This is my memo!",
			FromAddress: vaultAddr,
			ToAddress:   common.Address("0xd58610f89265a2fb637ac40edf59141ff873b266"),
		},
		1,
		pk,
		1,
	)

	_, out, err := s.bridge.GetInboundOutbound(common.ObservedTxs{tx})
	c.Assert(out, NotNil)
	c.Assert(err, IsNil)
}

func (s *ThorchainSuite) TestGetInboundOutboundXMRRequiresDerivableObservedAddress(c *C) {
	pk := stypes.GetRandomPubKey()
	tx := common.NewObservedTx(
		common.Tx{
			Chain: common.XMRChain,
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(123400000)),
			},
			FromAddress: common.Address(common.XMRChecksummedDummyAddress('8')),
			ToAddress:   common.Address(common.XMRChecksummedDummyAddress('4')),
		},
		1,
		pk,
		1,
	)

	_, _, err := s.bridge.GetInboundOutbound(common.ObservedTxs{tx})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "xmr observed vault pubkey"), Equals, true)
}

func (s *ThorchainSuite) TestGetInboundOutboundXMRInboundWithEddsaObservedPubKey(c *C) {
	pk, err := common.NewPubKeyFromCrypto(cometed25519.GenPrivKey().PubKey())
	c.Assert(err, IsNil)
	vaultAddr, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	tx := common.NewObservedTx(
		common.Tx{
			Chain: common.XMRChain,
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(123400000)),
			},
			Memo:        "SWAP:XMR.XMR:thor1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq0l5m4g",
			FromAddress: common.Address(common.XMRChecksummedDummyAddress('8')),
			ToAddress:   vaultAddr,
		},
		1,
		pk,
		1,
	)

	in, out, err := s.bridge.GetInboundOutbound(common.ObservedTxs{tx})
	c.Assert(err, IsNil)
	c.Assert(in, HasLen, 1)
	c.Assert(out, HasLen, 0)
}

func (s *ThorchainSuite) TestGetInboundOutboundXMROutboundWithEddsaObservedPubKey(c *C) {
	pk, err := common.NewPubKeyFromCrypto(cometed25519.GenPrivKey().PubKey())
	c.Assert(err, IsNil)
	vaultAddr, err := pk.GetAddress(common.XMRChain)
	c.Assert(err, IsNil)

	tx := common.NewObservedTx(
		common.Tx{
			Chain: common.XMRChain,
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(123400000)),
			},
			Memo:        "OUT:ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890",
			FromAddress: vaultAddr,
			ToAddress:   common.Address(common.XMRChecksummedDummyAddress('8')),
		},
		1,
		pk,
		100,
	)

	in, out, err := s.bridge.GetInboundOutbound(common.ObservedTxs{tx})
	c.Assert(err, IsNil)
	c.Assert(in, HasLen, 0)
	c.Assert(out, HasLen, 1)
	c.Assert(out[0].FinaliseHeight > out[0].BlockHeight, Equals, true)
}

func (ThorchainSuite) TestNewThorchainBridge(c *C) {
	testFunc := func(cfg config.BifrostClientConfiguration, errChecker, sbChecker Checker) {
		registry := codectypes.NewInterfaceRegistry()
		cryptocodec.RegisterInterfaces(registry)
		cdc := codec.NewProtoCodec(registry)
		kb := keyring.NewInMemory(cdc)
		_, _, err := kb.NewMnemonic(cfg.SignerName, keyring.English, cmd.THORChainHDPath, cfg.SignerPasswd, hd.Secp256k1)
		c.Assert(err, IsNil)
		sb, err := NewThorchainBridge(cfg, m, NewKeysWithKeybase(kb, cfg.SignerName, cfg.SignerPasswd))
		c.Assert(err, errChecker)
		c.Assert(sb, sbChecker)
	}
	testFunc(config.BifrostClientConfiguration{
		ChainID:         "",
		ChainHost:       "localhost",
		ChainHomeFolder: "~/.thorcli",
		SignerName:      "signer",
		SignerPasswd:    "signerpassword",
	}, NotNil, IsNil)
	testFunc(config.BifrostClientConfiguration{
		ChainID:         "chainid",
		ChainHost:       "",
		ChainHomeFolder: "~/.thorcli",
		SignerName:      "signer",
		SignerPasswd:    "signerpassword",
	}, NotNil, IsNil)
}

func (s *ThorchainSuite) TestGetAccountNumberAndSequenceNumber_Success(c *C) {
	s.nodeAccountFixture = "../../test/fixtures/endpoints/nodeaccount/template.json"
	s.authAccountFixture = "../../test/fixtures/endpoints/auth/accounts/template.json"
	accNumber, sequence, err := s.bridge.getAccountNumberAndSequenceNumber()
	c.Assert(err, IsNil)
	c.Assert(accNumber, Equals, uint64(3))
	c.Assert(sequence, Equals, uint64(5))
}

func (s *ThorchainSuite) TestGetAccountNumberAndSequenceNumber_Fail(c *C) {
	s.nodeAccountFixture = "../../test/fixtures/endpoints/nodeaccount/template.json"
	s.authAccountFixture = ""
	accNumber, sequence, err := s.bridge.getAccountNumberAndSequenceNumber()
	c.Assert(err, NotNil)
	c.Assert(accNumber, Equals, uint64(0))
	c.Assert(sequence, Equals, uint64(0))
}

func (s *ThorchainSuite) TestGetAccountNumberAndSequenceNumber_Fail_500(c *C) {
	s.nodeAccountFixture = "../../test/fixtures/endpoints/nodeaccount/template.json"
	s.authAccountFixture = "500"
	accNumber, sequence, err := s.bridge.getAccountNumberAndSequenceNumber()
	c.Assert(err, NotNil)
	c.Assert(accNumber, Equals, uint64(0))
	c.Assert(sequence, Equals, uint64(0))
}

func (s *ThorchainSuite) TestGetAccountNumberAndSequenceNumber_Fail_Unmarshal(c *C) {
	s.nodeAccountFixture = "../../test/fixtures/endpoints/nodeaccount/template.json"
	s.authAccountFixture = "../../test/fixtures/endpoints/auth/accounts/malformed.json"
	accNumber, sequence, err := s.bridge.getAccountNumberAndSequenceNumber()
	c.Assert(err, NotNil)
	c.Assert(true, Equals, strings.HasPrefix(err.Error(), "failed to unmarshal account resp"))
	c.Assert(accNumber, Equals, uint64(0))
	c.Assert(sequence, Equals, uint64(0))
}

func (s *ThorchainSuite) TestEnsureNodeWhitelisted_Success(c *C) {
	s.authAccountFixture = "../../test/fixtures/endpoints/auth/accounts/template.json"
	s.nodeAccountFixture = "../../test/fixtures/endpoints/nodeaccount/template.json"
	err := s.bridge.EnsureNodeWhitelisted()
	c.Assert(err, IsNil)
}

func (s *ThorchainSuite) TestEnsureNodeWhitelisted_Fail(c *C) {
	s.authAccountFixture = "../../test/fixtures/endpoints/auth/accounts/template.json"
	s.nodeAccountFixture = "../../test/fixtures/endpoints/nodeaccount/disabled.json"
	err := s.bridge.EnsureNodeWhitelisted()
	c.Assert(err, NotNil)
}

func (s *ThorchainSuite) TestIsCatchingUp(c *C) {
	ok, err := s.bridge.IsCatchingUp()
	c.Assert(err, IsNil)
	c.Assert(ok, Equals, false)
}

func (s *ThorchainSuite) TestGetAsgards(c *C) {
	vaults, err := s.bridge.GetAsgards()
	c.Assert(err, IsNil)
	c.Assert(vaults, NotNil)
}

func (s *ThorchainSuite) TestGetPubKeys(c *C) {
	pks, err := s.bridge.GetPubKeys()
	c.Assert(err, IsNil)
	c.Assert(pks, HasLen, 8)
	foundFrost := false
	for _, pk := range pks {
		if pk.Algo == common.SigningAlgoEd25519 && pk.PubKey.String() == "tthorpub1zcjduepqfrostvaultpubkeyforxmrtest000000000000000000" {
			foundFrost = true
			break
		}
	}
	c.Assert(foundFrost, Equals, true)
}

func (s *ThorchainSuite) TestPostNetworkFee(c *C) {
	s.authAccountFixture = "../../test/fixtures/endpoints/auth/accounts/template.json"
	txid, err := s.bridge.PostNetworkFee(1024, common.ETHChain, 100, 100)
	c.Assert(err, IsNil)
	c.Assert(txid.IsEmpty(), Equals, false)
}

func (s *ThorchainSuite) TestGetConstants(c *C) {
	result, err := s.bridge.GetConstants()
	c.Assert(err, IsNil)
	c.Assert(result, NotNil)
}

func (s *ThorchainSuite) TestGetRagnarok(c *C) {
	result, err := s.bridge.RagnarokInProgress()
	c.Assert(err, IsNil)
	c.Assert(result, Equals, false)
}

func (s *ThorchainSuite) TestGetThorchainVersion(c *C) {
	result, err := s.bridge.GetThorchainVersion()
	c.Assert(err, IsNil)
	c.Assert(result.EQ(stypes.GetCurrentVersion()), Equals, true)
}

func (s *ThorchainSuite) TestGetMimir(c *C) {
	result, err := s.bridge.GetMimir("HaltETHChain")
	c.Assert(err, IsNil)
	c.Assert(result, Equals, int64(10))
}

func (s *ThorchainSuite) TestGetContractAddress(c *C) {
	result, err := s.bridge.GetContractAddress()
	c.Assert(err, IsNil)
	c.Assert(result[0].Contracts[common.ETHChain].String(), Equals, "0xE65e9d372F8cAcc7b6dfcd4af6507851Ed31bb44")
}

func (s *ThorchainSuite) TestTHORName(c *C) {
	result, err := s.bridge.GetTHORName("test1")
	c.Assert(err, IsNil)
	c.Assert(result.Name, Equals, "test1")
	c.Assert(result.ExpireBlockHeight, Equals, int64(10000))
	c.Assert(result.Aliases, HasLen, 1)
	c.Assert(result.Aliases[0].Chain, Equals, common.THORChain)
	c.Assert(result.Aliases[0].Address, Equals, common.Address("tthor1tdfqy34uptx207scymqsy4k5uzfmry5sf7z3dw"))
}
