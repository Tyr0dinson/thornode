package core

import (
	"errors"
	rand "math/rand/v2"
	"net/http"
	"strings"
	"testing"

	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/rs/zerolog"

	atypes "gitlab.com/thorchain/thornode/v3/api/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

////////////////////////////////////////////////////////////////////////////////////////
// Fork A helpers
////////////////////////////////////////////////////////////////////////////////////////

const (
	covAInboundPath = "/thorchain/inbound_addresses"
	covANodesPath   = "/thorchain/nodes"
	covAVaultsPath  = "/thorchain/vaults/asgard"
	covAPoolPath    = "/thorchain/pool/BTC.BTC"
	covAConstPath   = "/thorchain/constants"
	covANetworkPath = "/thorchain/network"
	covAVaultAddr   = "bcrt1qcovavaultaddress"
	covARefundTxID  = "REFUNDTXID"
	covAStagesPath  = "/thorchain/tx/stages/" + covARefundTxID
	covADetailsPath = "/thorchain/tx/details/" + covARefundTxID
	covAActiveNode  = "Active"
	covAStandbyNode = "Standby"
)

func covAStr(s string) *string { return &s }

func covAInt(i int64) *int64 { return &i }

// covAValidators returns n validator users (mocknet validator mnemonics) with
// their fake bridges.
func covAValidators(t *testing.T, n int) ([]*simtypes.User, []*covBridge) {
	t.Helper()
	users := make([]*simtypes.User, 0, n)
	bridges := make([]*covBridge, 0, n)
	for i := 0; i < n; i++ {
		u, b := newCovUser(t, 12+i, nil)
		users = append(users, u)
		bridges = append(bridges, b)
	}
	return users, bridges
}

func covAThorAddr(t *testing.T, u *simtypes.User) string {
	t.Helper()
	addr, err := u.PubKey(common.THORChain).GetThorAddress()
	if err != nil {
		t.Fatal(err)
	}
	return addr.String()
}

// covANode builds the node account record matching a validator user.
func covANode(t *testing.T, u *simtypes.User, status string) openapi.Node {
	t.Helper()
	pk := u.PubKey(common.THORChain).String()
	return openapi.Node{
		NodeAddress: covAThorAddr(t, u),
		Status:      status,
		PubKeySet:   openapi.NodePubKeySet{Secp256k1: &pk},
		PeerId:      "peer",
		IpAddress:   "10.0.0.1",
	}
}

func covANodes(t *testing.T, users []*simtypes.User, status string) []openapi.Node {
	t.Helper()
	nodes := make([]openapi.Node, 0, len(users))
	for _, u := range users {
		nodes = append(nodes, covANode(t, u, status))
	}
	return nodes
}

func covAMembers(nodes []openapi.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, *n.PubKeySet.Secp256k1)
	}
	return out
}

// covAVaultPubKey is a valid bech32 pubkey used as the active BTC vault.
func covAVaultPubKey(t *testing.T) common.PubKey {
	t.Helper()
	u, _ := newCovUser(t, 0, nil)
	return u.PubKey(common.THORChain)
}

func covAVault(pk common.PubKey, status string, members []string) openapi.Vault {
	return openapi.Vault{
		PubKey:     covAStr(pk.String()),
		Status:     status,
		Membership: members,
		Addresses:  []openapi.VaultAddress{{Chain: "BTC", Address: covAVaultAddr}},
	}
}

func covAInbound(pk common.PubKey) []openapi.InboundAddress {
	return []openapi.InboundAddress{
		{Chain: covAStr("ETH"), PubKey: covAStr(pk.String()), Address: covAStr("0xeth")},
		{Chain: covAStr("BTC"), PubKey: covAStr(pk.String()), Address: covAStr(covAVaultAddr)},
	}
}

func covAActor(name string) simtypes.Actor {
	a := simtypes.NewActor(name, rand.New(rand.NewPCG(1, 2)))
	a.SetLogger(zerolog.Nop())
	return *a
}

func covAAssertPending(t *testing.T, name string, res simtypes.OpResult) {
	t.Helper()
	if res.Continue || res.Finish || res.Error != nil {
		t.Fatalf("%s: expected pending result, got %+v", name, res)
	}
}

func covAAssertContinue(t *testing.T, name string, res simtypes.OpResult) {
	t.Helper()
	if !res.Continue || res.Finish || res.Error != nil {
		t.Fatalf("%s: expected continue, got %+v", name, res)
	}
}

func covAAssertFatal(t *testing.T, name string, res simtypes.OpResult, substr string) {
	t.Helper()
	if !res.Finish || res.Error == nil || !strings.Contains(res.Error.Error(), substr) {
		t.Fatalf("%s: expected finish with error containing %q, got %+v", name, substr, res)
	}
}

// covARefundOps drives the shared refund-outbound op trio used by both the
// forced-to-leave and the jailed-fallback keysign actors.
type covARefundOps struct {
	swap        *SwapActor
	vaultPubKey common.PubKey
	acquire     func(*simtypes.OpConfig) simtypes.OpResult
	send        func(*simtypes.OpConfig) simtypes.OpResult
	verify      func(*simtypes.OpConfig) simtypes.OpResult
	txID        func() string
	memo        string
}

func covARunRefundOps(t *testing.T, ops covARefundOps) {
	t.Helper()
	api := newCovThornode(t)
	vpk := ops.vaultPubKey

	// acquireRefundUser: pool failure keeps waiting.
	api.Status(covAPoolPath, http.StatusBadRequest, "no pool")
	covAAssertPending(t, "pool error", ops.acquire(&simtypes.OpConfig{}))

	// Empty pool falls back to 10x the BTC dust threshold.
	btc := newCovChainClient(common.NewCoin(common.BTCAsset, sdkmath.NewUint(common.One)))
	user, _ := newCovUser(t, 1, map[common.Chain]simtypes.LiteChainClient{common.BTCChain: btc})
	cfg := &simtypes.OpConfig{Users: []*simtypes.User{user}}
	api.JSON(covAPoolPath, openapi.Pool{BalanceAsset: "0"})
	covAAssertContinue(t, "acquire dust fallback", ops.acquire(cfg))
	if want := common.BTCChain.DustThreshold().MulUint64(10); !ops.swap.swapAmount.Equal(want) {
		t.Fatalf("swap amount %s, want dust fallback %s", ops.swap.swapAmount, want)
	}
	if ops.swap.user != user || !user.IsLocked() {
		t.Fatal("refund user was not selected and locked")
	}

	// Large pools target 0.1% of the pool depth.
	ops.swap.releaseUserSelection()
	api.JSON(covAPoolPath, openapi.Pool{BalanceAsset: "5000000000"})
	covAAssertContinue(t, "acquire pool share", ops.acquire(cfg))
	if !ops.swap.swapAmount.Equal(sdkmath.NewUint(5000000)) {
		t.Fatalf("swap amount %s, want 5000000", ops.swap.swapAmount)
	}

	// sendRefundInbound
	vaultPath := "/thorchain/vault/" + vpk.String()
	api.Status(vaultPath, http.StatusNotFound, "missing")
	covAAssertPending(t, "vault error", ops.send(cfg))

	noBTC := covAVault(vpk, "ActiveVault", nil)
	noBTC.Addresses = []openapi.VaultAddress{{Chain: "ETH", Address: "0xeth"}, {Chain: "BTC"}}
	api.JSON(vaultPath, noBTC)
	covAAssertFatal(t, "vault without BTC", ops.send(cfg), "has no BTC address")

	api.JSON(vaultPath, covAVault(vpk, "ActiveVault", nil))
	btc.BroadcastErr = errors.New("rpc unavailable")
	covAAssertPending(t, "broadcast error", ops.send(cfg))
	if ops.txID() != "" {
		t.Fatal("txid recorded for failed broadcast")
	}

	btc.BroadcastErr = nil
	btc.TxID = covARefundTxID
	covAAssertContinue(t, "broadcast", ops.send(cfg))
	if ops.txID() != covARefundTxID {
		t.Fatalf("refund txid %q", ops.txID())
	}
	last := btc.Signed[len(btc.Signed)-1]
	if last.Memo != ops.memo || last.ToAddress != covAVaultAddr || !last.Coin.Amount.Equal(ops.swap.swapAmount) {
		t.Fatalf("unexpected refund inbound %+v", last)
	}

	// verifyRefundOutbound
	api.Status(covAStagesPath, http.StatusNotFound, "missing")
	covAAssertPending(t, "stages error", ops.verify(cfg))
	api.JSON(covAStagesPath, `{"inbound_observed":{"completed":true}}`)
	covAAssertPending(t, "not signed", ops.verify(cfg))
	api.JSON(covAStagesPath, `{"inbound_observed":{"completed":true},"outbound_signed":{"completed":false}}`)
	covAAssertPending(t, "signing incomplete", ops.verify(cfg))
	api.JSON(covAStagesPath, `{"inbound_observed":{"completed":true},"outbound_signed":{"completed":true}}`)
	api.Status(covADetailsPath, http.StatusNotFound, "missing")
	covAAssertPending(t, "details error", ops.verify(cfg))

	refund := "REFUND:" + covARefundTxID
	action := func(vault *string, memo, original *string) openapi.TxOutItem {
		return openapi.TxOutItem{Chain: "BTC", VaultPubKey: vault, Memo: memo, OriginalMemo: original}
	}
	details := func(outs int, actions ...openapi.TxOutItem) openapi.TxDetailsResponse {
		d := openapi.TxDetailsResponse{Actions: actions}
		for i := 0; i < outs; i++ {
			d.OutTxs = append(d.OutTxs, openapi.Tx{})
		}
		return d
	}
	api.JSON(covADetailsPath, details(2, action(covAStr(vpk.String()), &refund, nil)))
	covAAssertFatal(t, "two outs", ops.verify(cfg), "exactly one refund out")
	api.JSON(covADetailsPath, details(1))
	covAAssertFatal(t, "no actions", ops.verify(cfg), "exactly one refund action")
	api.JSON(covADetailsPath, details(1, action(nil, &refund, nil)))
	covAAssertFatal(t, "nil vault", ops.verify(cfg), "empty vault pubkey")
	api.JSON(covADetailsPath, details(1, action(covAStr("other"), &refund, nil)))
	covAAssertFatal(t, "other vault", ops.verify(cfg), "got other")
	api.JSON(covADetailsPath, details(1, action(covAStr(vpk.String()), nil, covAStr("SWAP:BTC.BTC"))))
	covAAssertFatal(t, "non-refund original memo", ops.verify(cfg), `"SWAP:BTC.BTC"`)
	api.JSON(covADetailsPath, details(1, action(covAStr(vpk.String()), nil, nil)))
	covAAssertFatal(t, "empty memo", ops.verify(cfg), `got ""`)

	// Successful verification releases the refund user.
	if ops.swap.user == nil {
		if !user.Acquire() {
			t.Fatal("acquire")
		}
		ops.swap.user = user
	}
	api.JSON(covADetailsPath, details(1, action(covAStr(vpk.String()), nil, &refund)))
	res := ops.verify(cfg)
	if !res.Finish || res.Error != nil {
		t.Fatalf("expected verified refund, got %+v", res)
	}
	if ops.swap.user != nil || user.IsLocked() {
		t.Fatal("refund user was not released after verification")
	}

	// A funding failure after selection releases the user and skips the actor.
	if !user.Acquire() {
		t.Fatal("acquire")
	}
	ops.swap.user = user
	btc.BroadcastErr = errors.New("insufficient funds for gas")
	res = ops.send(cfg)
	if !res.Finish || res.Error != nil {
		t.Fatalf("funding failure should skip actor, got %+v", res)
	}
	if user.IsLocked() || ops.swap.user != nil {
		t.Fatal("funding failure must release the user")
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// ForcedToLeaveKeysignActor
////////////////////////////////////////////////////////////////////////////////////////

func newCovAForcedKeysign() *ForcedToLeaveKeysignActor {
	return &ForcedToLeaveKeysignActor{
		SwapActor: SwapActor{Actor: covAActor("ftl"), from: common.BTCAsset, to: common.BTCAsset},
		banVotes:  map[string]bool{},
	}
}

func TestNewForcedToLeaveActorsWireOpsCovA(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	ks := NewForcedToLeaveKeysignActor(rng)
	if ks.Name != "ForcedToLeaveKeysign BTC.BTC refund" || len(ks.Ops) != 6 || ks.Timeout.Minutes() != 25 {
		t.Fatalf("unexpected keysign actor %s ops=%d timeout=%s", ks.Name, len(ks.Ops), ks.Timeout)
	}
	if !strings.Contains(ks.Ops[0].String(), "selectActiveVaultMember") || !strings.Contains(ks.Ops[5].String(), "verifyRefundOutbound") {
		t.Fatalf("unexpected keysign op order %s..%s", ks.Ops[0], ks.Ops[5])
	}
	kg := NewForcedToLeaveKeygenActor(rng)
	if kg.Name != "ForcedToLeaveKeygen BTC.BTC => ETH.ETH" || len(kg.Ops) != 8 || kg.Timeout.Minutes() != 35 {
		t.Fatalf("unexpected keygen actor %s ops=%d timeout=%s", kg.Name, len(kg.Ops), kg.Timeout)
	}
	if !strings.Contains(kg.Ops[0].String(), "selectKeygenCandidate") || !strings.Contains(kg.Ops[3].String(), "waitForNewVault") {
		t.Fatalf("unexpected keygen op order %s..%s", kg.Ops[0], kg.Ops[3])
	}
}

func TestForcedToLeaveSelectActiveVaultMemberCovA(t *testing.T) {
	api := newCovThornode(t)
	users, _ := covAValidators(t, 4)
	cfg := &simtypes.OpConfig{NodeUsers: users}
	vpk := covAVaultPubKey(t)
	vaultPath := "/thorchain/vault/" + vpk.String()
	nodes := covANodes(t, users, covAActiveNode)
	a := newCovAForcedKeysign()

	// No inbound addresses yet.
	covAAssertPending(t, "inbound missing", a.selectActiveVaultMember(cfg))

	// Inbound vault exists but is not active.
	api.JSON(covAInboundPath, covAInbound(vpk))
	api.JSON(vaultPath, covAVault(vpk, "RetiringVault", covAMembers(nodes)))
	covAAssertPending(t, "retiring vault", a.selectActiveVaultMember(cfg))

	// Inbound vault lookup fails.
	api.Status(vaultPath, http.StatusNotFound, "missing")
	covAAssertPending(t, "vault lookup", a.selectActiveVaultMember(cfg))

	// Nodes lookup fails.
	api.JSON(vaultPath, covAVault(vpk, "ActiveVault", covAMembers(nodes)))
	covAAssertPending(t, "nodes error", a.selectActiveVaultMember(cfg))

	// Fewer than three active validators skips the actor.
	small := []openapi.Node{nodes[0], nodes[1], covANode(t, users[2], covAStandbyNode)}
	api.JSON(covANodesPath, small)
	res := a.selectActiveVaultMember(cfg)
	if !res.Finish || res.Error != nil {
		t.Fatalf("small topology should skip, got %+v", res)
	}

	// Vault too small to lose a signer.
	api.JSON(covANodesPath, nodes)
	api.JSON(vaultPath, covAVault(vpk, "ActiveVault", covAMembers(nodes[:2])))
	covAAssertPending(t, "small vault", a.selectActiveVaultMember(cfg))

	// Members whose node address is malformed abort the actor.
	bad := append([]openapi.Node{}, nodes...)
	bad[3].NodeAddress = "not-a-bech32"
	api.JSON(covANodesPath, bad)
	api.JSON(vaultPath, covAVault(vpk, "ActiveVault", covAMembers(nodes)))
	covAAssertFatal(t, "bad node address", a.selectActiveVaultMember(cfg), "invalid node address")

	// Without simulation users for the voters, no ban can reach consensus.
	api.JSON(covANodesPath, nodes)
	covAAssertPending(t, "no voters", a.selectActiveVaultMember(&simtypes.OpConfig{NodeUsers: users[:1]}))
	if !a.vaultPubKey.IsEmpty() {
		t.Fatal("vault selected without a removable member")
	}

	// Forced, non-active and unknown members are skipped; the lowest address of
	// the remaining active members is chosen deterministically.
	withSkips := append([]openapi.Node{}, nodes...)
	withSkips[0].ForcedToLeave = true
	members := append(covAMembers(nodes), "thorpub-unknown-member")
	api.JSON(covANodesPath, withSkips)
	api.JSON(vaultPath, covAVault(vpk, "ActiveVault", members))
	covAAssertContinue(t, "select", a.selectActiveVaultMember(cfg))
	want := ""
	for _, n := range withSkips[1:] {
		if want == "" || n.NodeAddress < want {
			want = n.NodeAddress
		}
	}
	if a.flagged.node.NodeAddress != want {
		t.Fatalf("flagged %s, want lowest eligible %s", a.flagged.node.NodeAddress, want)
	}
	if !a.vaultPubKey.Equals(vpk) || !a.expectedOutboundVault.Equals(vpk) {
		t.Fatal("selected vault not pinned for outbound verification")
	}
}

func TestBroadcastBanUntilForcedCovA(t *testing.T) {
	api := newCovThornode(t)
	users, bridges := covAValidators(t, 4)
	cfg := &simtypes.OpConfig{NodeUsers: users}
	nodes := covANodes(t, users, covAActiveNode)
	target, err := newForcedToLeaveNode(nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	a := newCovAForcedKeysign()
	a.flagged = target

	// Node lookup failures keep waiting.
	covAAssertPending(t, "nodes error", a.forceLeaveSelectedMember(cfg))

	// Missing target is fatal.
	api.JSON(covANodesPath, nodes[1:])
	covAAssertFatal(t, "missing target", a.forceLeaveSelectedMember(cfg), "not found")

	// Not enough local voters is fatal.
	api.JSON(covANodesPath, nodes)
	covAAssertFatal(t, "no consensus", a.forceLeaveSelectedMember(&simtypes.OpConfig{NodeUsers: users[:2]}), "not enough active simulation node users")

	// A locked voter is skipped and the remaining votes are below the
	// supermajority, so the op keeps waiting.
	if !users[1].Acquire() {
		t.Fatal("acquire")
	}
	covAAssertPending(t, "partial votes", a.forceLeaveSelectedMember(cfg))
	if bridges[1].MsgCount() != 0 || bridges[2].MsgCount() != 1 || bridges[3].MsgCount() != 1 {
		t.Fatal("expected ban votes only from unlocked non-target validators")
	}
	ban, ok := bridges[2].Msgs[0].(*stypes.MsgBan)
	if !ok || !ban.NodeAddress.Equals(target.address) || ban.Signer.String() != covAThorAddr(t, users[2]) {
		t.Fatalf("unexpected ban msg %#v", bridges[2].Msgs[0])
	}
	users[1].Release()

	// Already-sent votes are not repeated; the newly unlocked voter votes.
	covAAssertPending(t, "all votes sent", a.forceLeaveSelectedMember(cfg))
	if bridges[1].MsgCount() != 1 || bridges[2].MsgCount() != 1 || bridges[0].MsgCount() != 0 {
		t.Fatal("ban votes repeated or target voted for itself")
	}
	if len(a.banVotes) != 3 {
		t.Fatalf("expected 3 recorded votes, got %d", len(a.banVotes))
	}

	// Broadcast failures are fatal.
	a.banVotes = map[string]bool{}
	bridges[1].BroadcastFn = func(...sdk.Msg) (common.TxID, error) { return "", errors.New("rejected") }
	covAAssertFatal(t, "broadcast error", a.forceLeaveSelectedMember(cfg), "rejected")
	if users[1].IsLocked() {
		t.Fatal("voter lock leaked after broadcast failure")
	}

	// Once forced_to_leave is reported, the op continues.
	forced := append([]openapi.Node{}, nodes...)
	forced[0].ForcedToLeave = true
	api.JSON(covANodesPath, forced)
	covAAssertContinue(t, "forced", a.forceLeaveSelectedMember(cfg))
}

func TestForcedToLeaveVerifyFilteredKeysignPartyCovA(t *testing.T) {
	users, bridges := covAValidators(t, 4)
	cfg := &simtypes.OpConfig{NodeUsers: users}
	nodes := covANodes(t, users, covAActiveNode)
	flagged, err := newForcedToLeaveNode(nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	a := newCovAForcedKeysign()
	a.flagged = flagged
	a.vaultPubKey = covAVaultPubKey(t)
	others := common.PubKeys{users[1].PubKey(common.THORChain), users[2].PubKey(common.THORChain), users[3].PubKey(common.THORChain)}

	bridges[0].HeightFn = func() (int64, error) { return 0, errors.New("down") }
	covAAssertPending(t, "height error", a.verifyFilteredKeysignParty(cfg))

	var gotHeight int64
	bridges[0].HeightFn = func() (int64, error) { return 77, nil }
	covAAssertPending(t, "policy error", a.verifyFilteredKeysignParty(cfg))

	bridges[0].KeysignPolicyFn = func(vault common.PubKey, h int64) (thorclient.KeysignPolicy, error) {
		gotHeight = h
		return thorclient.KeysignPolicy{Signers: append(others, flagged.pubKey), RequiredSigners: 3}, nil
	}
	covAAssertFatal(t, "flagged present", a.verifyFilteredKeysignParty(cfg), "still contains forced-to-leave")
	if gotHeight != 77 {
		t.Fatalf("policy queried at %d, want current height 77", gotHeight)
	}

	bridges[0].KeysignPolicyFn = func(common.PubKey, int64) (thorclient.KeysignPolicy, error) {
		return thorclient.KeysignPolicy{Signers: others[:2], RequiredSigners: 3}, nil
	}
	covAAssertFatal(t, "below threshold", a.verifyFilteredKeysignParty(cfg), "below threshold: got 2 need 3")

	bridges[0].KeysignPolicyFn = func(common.PubKey, int64) (thorclient.KeysignPolicy, error) {
		return thorclient.KeysignPolicy{Signers: others, RequiredSigners: 3}, nil
	}
	covAAssertContinue(t, "filtered", a.verifyFilteredKeysignParty(cfg))
}

func TestForcedToLeaveRefundOpsCovA(t *testing.T) {
	a := newCovAForcedKeysign()
	a.vaultPubKey = covAVaultPubKey(t)
	covARunRefundOps(t, covARefundOps{
		swap:        &a.SwapActor,
		vaultPubKey: a.vaultPubKey,
		acquire:     a.acquireRefundUser,
		send:        a.sendRefundInbound,
		verify:      a.verifyRefundOutbound,
		txID:        func() string { return a.refundTxID },
		memo:        "TEST-FORCED-TO-LEAVE-REFUND",
	})
}

////////////////////////////////////////////////////////////////////////////////////////
// ForcedToLeaveKeygenActor
////////////////////////////////////////////////////////////////////////////////////////

func newCovAForcedKeygen() *ForcedToLeaveKeygenActor {
	return &ForcedToLeaveKeygenActor{
		SwapActor: SwapActor{Actor: covAActor("ftl-keygen"), from: common.BTCAsset, to: common.ETHAsset},
		banVotes:  map[string]bool{},
	}
}

func TestForcedToLeaveSelectKeygenCandidateCovA(t *testing.T) {
	api := newCovThornode(t)
	users, _ := covAValidators(t, 5)
	cfg := &simtypes.OpConfig{NodeUsers: users}
	a := newCovAForcedKeygen()
	vpk := covAVaultPubKey(t)

	covAAssertPending(t, "vaults error", a.selectKeygenCandidate(cfg))

	api.JSON(covAVaultsPath, []openapi.Vault{
		covAVault(vpk, "ActiveVault", nil),
		covAVault(common.PubKey("retiring"), "RetiringVault", nil),
		{Status: "ActiveVault"},
	})
	covAAssertPending(t, "nodes error", a.selectKeygenCandidate(cfg))
	if len(a.initialVaults) != 1 || !a.initialVaults[vpk.String()] {
		t.Fatalf("unexpected initial vault snapshot %v", a.initialVaults)
	}

	// Constants missing: falls back to 4 BFT members; 4 eligible nodes leave
	// no spare participant, so the actor skips.
	nodes := covANodes(t, users[:4], covAActiveNode)
	api.JSON(covANodesPath, nodes)
	res := a.selectKeygenCandidate(cfg)
	if !res.Finish || res.Error != nil {
		t.Fatalf("no spare keygen participant should skip, got %+v", res)
	}

	// With MinimumNodesForBFT=3 and 4 eligible nodes, but no local voters,
	// nobody can be banned.
	api.JSON(covAConstPath, `{"int_64_values":{"MinimumNodesForBFT":3}}`)
	covAAssertPending(t, "no candidate", a.selectKeygenCandidate(&simtypes.OpConfig{NodeUsers: users[:1]}))

	// A standby node is preferred over active ones.
	withStandby := append(covANodes(t, users[:4], covAActiveNode), covANode(t, users[4], covAStandbyNode))
	api.JSON(covANodesPath, withStandby)
	covAAssertContinue(t, "select standby", a.selectKeygenCandidate(cfg))
	if a.flagged.node.NodeAddress != withStandby[4].NodeAddress {
		t.Fatalf("expected standby node to be flagged, got %s", a.flagged.node.Status)
	}
}

func TestMinimumNodesForBFTCovA(t *testing.T) {
	api := newCovThornode(t)
	if _, err := minimumNodesForBFT(); err == nil {
		t.Fatal("expected error for missing constants")
	}
	api.JSON(covAConstPath, `{"int_64_values":{"MinimumNodesForBFT":0}}`)
	if _, err := minimumNodesForBFT(); err == nil || !strings.Contains(err.Error(), "unset") {
		t.Fatalf("expected unset error, got %v", err)
	}
	api.JSON(covAConstPath, `{"int_64_values":{"MinimumNodesForBFT":4}}`)
	if n, err := minimumNodesForBFT(); err != nil || n != 4 {
		t.Fatalf("got %d %v", n, err)
	}
}

func TestForcedToLeaveKeygenBanAndChurnCovA(t *testing.T) {
	api := newCovThornode(t)
	users, bridges := covAValidators(t, 4)
	cfg := &simtypes.OpConfig{NodeUsers: users}
	nodes := covANodes(t, users, covAActiveNode)
	a := newCovAForcedKeygen()
	flagged, err := newForcedToLeaveNode(nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	a.flagged = flagged

	// forceLeaveKeygenCandidate: error path and success path.
	api.JSON(covANodesPath, nodes[1:])
	covAAssertFatal(t, "missing target", a.forceLeaveKeygenCandidate(cfg), "not found")
	forced := append([]openapi.Node{}, nodes...)
	forced[0].ForcedToLeave = true
	api.JSON(covANodesPath, forced)
	covAAssertContinue(t, "forced", a.forceLeaveKeygenCandidate(cfg))

	// triggerChurn: node lookup failure.
	api.Remove(covANodesPath)
	covAAssertPending(t, "nodes error", a.triggerChurn(cfg))

	// Only the flagged node is active: no signer.
	onlyFlagged := []openapi.Node{forced[0], covANode(t, users[1], covAStandbyNode)}
	api.JSON(covANodesPath, onlyFlagged)
	covAAssertPending(t, "no signer", a.triggerChurn(cfg))

	// Selected signer is locked.
	api.JSON(covANodesPath, forced)
	if !users[1].Acquire() {
		t.Fatal("acquire")
	}
	covAAssertPending(t, "signer locked", a.triggerChurn(cfg))
	users[1].Release()

	// Broadcast failure is fatal and releases the signer.
	bridges[1].BroadcastFn = func(...sdk.Msg) (common.TxID, error) { return "", errors.New("nope") }
	covAAssertFatal(t, "broadcast", a.triggerChurn(cfg), "broadcast NUMBEROFNEWNODESPERCHURN=4")
	if users[1].IsLocked() || a.churnStarted {
		t.Fatal("failed churn trigger must release lock and not mark churn started")
	}

	bridges[1].BroadcastFn = nil
	bridges[1].Msgs = nil
	covAAssertContinue(t, "trigger", a.triggerChurn(cfg))
	if !a.churnStarted || len(bridges[1].Msgs) != 3 {
		t.Fatalf("expected 3 mimir votes, got %d", len(bridges[1].Msgs))
	}
	wantKeys := []string{"NUMBEROFNEWNODESPERCHURN", "CHURNRETRYINTERVAL", "HALTCHURNING"}
	wantVals := []int64{4, xmrPostChurnRetryInterval, 0}
	for i, msg := range bridges[1].Msgs {
		m := msg.(*stypes.MsgMimir)
		if m.Key != wantKeys[i] || m.Value != wantVals[i] {
			t.Fatalf("mimir %d = %s=%d", i, m.Key, m.Value)
		}
	}

	// Already started: no further broadcasts.
	covAAssertContinue(t, "already started", a.triggerChurn(cfg))
	if len(bridges[1].Msgs) != 3 {
		t.Fatal("churn mimirs re-broadcast")
	}
}

func TestForcedToLeaveWaitForNewVaultCovA(t *testing.T) {
	api := newCovThornode(t)
	users, _ := covAValidators(t, 4)
	nodes := covANodes(t, users, covAActiveNode)
	a := newCovAForcedKeygen()
	flagged, err := newForcedToLeaveNode(nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	a.flagged = flagged
	oldPK := covAVaultPubKey(t)
	a.initialVaults = map[string]bool{oldPK.String(): true}
	newPK := users[3].PubKey(common.THORChain)

	covAAssertPending(t, "network error", a.waitForNewVault(nil))
	api.JSON(covANetworkPath, `{"vaults_migrating":false}`)
	covAAssertPending(t, "vaults error", a.waitForNewVault(nil))

	noETH := covAVault(newPK, "ActiveVault", covAMembers(nodes[1:]))
	api.JSON(covAVaultsPath, []openapi.Vault{
		covAVault(newPK, "RetiringVault", nil),
		{Status: "ActiveVault"},
		covAVault(oldPK, "ActiveVault", nil),
		noETH,
	})
	covAAssertPending(t, "no new eth vault", a.waitForNewVault(nil))

	withFlagged := covAVault(newPK, "ActiveVault", covAMembers(nodes))
	withFlagged.Chains = []string{"BTC", "ETH"}
	api.JSON(covAVaultsPath, []openapi.Vault{withFlagged})
	covAAssertFatal(t, "flagged member", a.waitForNewVault(nil), "includes forced-to-leave pubkey")

	clean := covAVault(newPK, "ActiveVault", covAMembers(nodes[1:]))
	clean.Addresses = append(clean.Addresses, openapi.VaultAddress{Chain: "ETH", Address: "0xvault"})
	api.JSON(covAVaultsPath, []openapi.Vault{clean})
	api.JSON(covANetworkPath, `{"vaults_migrating":true}`)
	covAAssertPending(t, "migrating", a.waitForNewVault(nil))
	if !a.newVault.IsEmpty() {
		t.Fatal("vault pinned while migrating")
	}

	api.JSON(covANetworkPath, `{"vaults_migrating":false}`)
	covAAssertContinue(t, "new vault", a.waitForNewVault(nil))
	if !a.newVault.Equals(newPK) || !a.expectedOutboundVault.Equals(newPK) {
		t.Fatal("new vault was not pinned for the swap")
	}
}

////////////////////////////////////////////////////////////////////////////////////////
// Shared helpers
////////////////////////////////////////////////////////////////////////////////////////

func TestForcedToLeaveHelpersCovA(t *testing.T) {
	users, _ := covAValidators(t, 3)

	// newForcedToLeaveNode
	if _, err := newForcedToLeaveNode(openapi.Node{NodeAddress: "x"}); err == nil || !strings.Contains(err.Error(), "empty secp256k1") {
		t.Fatalf("expected empty pubkey error, got %v", err)
	}
	n := covANode(t, users[0], covAActiveNode)
	got, err := newForcedToLeaveNode(n)
	if err != nil || got.address.String() != n.NodeAddress || got.pubKey.String() != *n.PubKeySet.Secp256k1 {
		t.Fatalf("unexpected node %+v %v", got, err)
	}

	// threshold helpers
	for _, tc := range []struct{ n, tss, super int }{{0, 0, 0}, {1, 0, 1}, {3, 1, 2}, {4, 2, 3}, {6, 3, 4}} {
		if tssThreshold(tc.n) != tc.tss || requiredSuperMajority(tc.n) != tc.super {
			t.Fatalf("n=%d tss=%d super=%d", tc.n, tssThreshold(tc.n), requiredSuperMajority(tc.n))
		}
	}
	if vaultRemovalKeepsThreshold(openapi.Vault{Membership: []string{"a"}}) {
		t.Fatal("single member vault cannot lose a signer")
	}
	if !vaultRemovalKeepsThreshold(openapi.Vault{Membership: []string{"a", "b", "c", "d"}}) {
		t.Fatal("4 member vault can lose a signer")
	}

	// vault helpers
	if _, ok := vaultPubKey(openapi.Vault{PubKey: covAStr("")}); ok {
		t.Fatal("empty vault pubkey accepted")
	}
	v := openapi.Vault{Chains: []string{"ETH"}, Addresses: []openapi.VaultAddress{{Chain: "BTC", Address: "addr"}}}
	if !vaultHasChain(v, common.ETHChain) || !vaultHasChain(v, common.BTCChain) || vaultHasChain(v, common.DOGEChain) {
		t.Fatal("vaultHasChain mismatch")
	}
	if addr, ok := vaultChainAddress(v, common.BTCChain); !ok || addr != "addr" {
		t.Fatal("vaultChainAddress mismatch")
	}
	if _, ok := vaultChainAddress(v, common.ETHChain); ok {
		t.Fatal("ETH address should be missing")
	}

	// node indexing skips nodes without a secp256k1 key
	nodes := []openapi.Node{n, {NodeAddress: "nokey", Status: covAActiveNode}}
	if idx := nodesBySecp256k1(nodes); len(idx) != 1 {
		t.Fatalf("expected 1 indexed node, got %d", len(idx))
	}
	if activeValidatorCount(nodes) != 2 {
		t.Fatal("active count mismatch")
	}
	if _, ok := findNodeByAddress(nodes, "missing"); ok {
		t.Fatal("found missing node")
	}

	// banCanReachConsensus with no active nodes
	if banCanReachConsensus(nil, nil, "x") {
		t.Fatal("no active nodes cannot reach consensus")
	}

	// sort orders by status desc then address
	sorted := []forcedToLeaveNode{
		{node: openapi.Node{Status: "Active", NodeAddress: "b"}},
		{node: openapi.Node{Status: "Standby", NodeAddress: "z"}},
		{node: openapi.Node{Status: "Active", NodeAddress: "a"}},
	}
	sortForcedToLeaveNodes(sorted)
	if sorted[0].node.Status != "Standby" || sorted[1].node.NodeAddress != "a" || sorted[2].node.NodeAddress != "b" {
		t.Fatalf("unexpected order %+v", sorted)
	}

	// keygen eligibility filters forced, non-active/standby and keyless nodes
	forced := covANode(t, users[1], covAActiveNode)
	forced.ForcedToLeave = true
	disabled := covANode(t, users[2], atypes.NodeStatus_Disabled.String())
	eligible := keygenEligibleNodes([]openapi.Node{n, forced, disabled, {Status: covAActiveNode}})
	if len(eligible) != 1 || eligible[0].NodeAddress != n.NodeAddress {
		t.Fatalf("unexpected eligible nodes %+v", eligible)
	}
	if c := keygenForcedToLeaveCandidates([]openapi.Node{n}, nil, 1); len(c) != 0 {
		t.Fatal("candidates returned below minimum members")
	}

	// keygen candidates skip invalid addresses even when consensus is possible.
	cfg := &simtypes.OpConfig{NodeUsers: users}
	byAddr, err := nodeUsersByThorAddress(cfg)
	if err != nil || len(byAddr) != 3 {
		t.Fatalf("users by address %v %v", byAddr, err)
	}
	all := covANodes(t, users, covAActiveNode)
	badAddr := covANode(t, users[0], covAStandbyNode)
	badAddr.NodeAddress = "bad"
	cands := keygenForcedToLeaveCandidates(append(all, badAddr), byAddr, 2)
	for _, c := range cands {
		if c.node.NodeAddress == "bad" {
			t.Fatal("invalid node address became a candidate")
		}
	}
	if len(cands) != 3 {
		t.Fatalf("expected 3 active candidates, got %d", len(cands))
	}

	// firstActiveNodeUser skips excluded/forced/userless nodes
	stranger := covANode(t, users[0], covAActiveNode)
	stranger.NodeAddress = "thor1stranger"
	u, addr, ok := firstActiveNodeUser(cfg, []openapi.Node{stranger, all[0], all[1]}, all[0].NodeAddress)
	if !ok || u != users[1] || addr.String() != all[1].NodeAddress {
		t.Fatalf("unexpected first active node user %v %v %v", u, addr, ok)
	}
	if _, _, ok := firstActiveNodeUser(cfg, []openapi.Node{forced}, ""); ok {
		t.Fatal("forced node returned as signer")
	}

	if !stringSliceContains([]string{"a", "b"}, "b") || stringSliceContains(nil, "a") {
		t.Fatal("stringSliceContains mismatch")
	}
	pk := users[0].PubKey(common.THORChain)
	if !pubKeysContain(common.PubKeys{pk}, pk) || pubKeysContain(nil, pk) {
		t.Fatal("pubKeysContain mismatch")
	}
	set := activeVaultPubKeySet([]openapi.Vault{{Status: "ActiveVault", PubKey: covAStr("x")}, {Status: "InactiveVault", PubKey: covAStr("y")}})
	if len(set) != 1 || !set["x"] {
		t.Fatalf("unexpected active set %v", set)
	}
	if getActiveVaultForChainCovA(t) {
		t.Fatal("unexpected active vault")
	}
}

// getActiveVaultForChainCovA checks the inbound pubkey error path.
func getActiveVaultForChainCovA(t *testing.T) bool {
	t.Helper()
	api := newCovThornode(t)
	api.JSON(covAInboundPath, []openapi.InboundAddress{{Chain: covAStr("BTC")}})
	_, _, err := getActiveVaultForChain(common.BTCChain)
	return err == nil
}
