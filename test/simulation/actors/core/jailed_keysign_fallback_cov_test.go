package core

import (
	"errors"
	rand "math/rand/v2"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	simtypes "gitlab.com/thorchain/thornode/v3/test/simulation/pkg/types"
	stypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func newCovAJailed() *JailedKeysignFallbackActor {
	return &JailedKeysignFallbackActor{
		SwapActor:   SwapActor{Actor: covAActor("jailed"), from: common.BTCAsset, to: common.BTCAsset},
		jailVotes:   map[string]map[string]bool{},
		jailHeights: map[string]int64{},
	}
}

func covAPubKeys(users []*simtypes.User) common.PubKeys {
	out := make(common.PubKeys, 0, len(users))
	for _, u := range users {
		out = append(out, u.PubKey(common.THORChain))
	}
	return out
}

func covAJailedNode(t *testing.T, u *simtypes.User, release int64) openapi.Node {
	t.Helper()
	n := covANode(t, u, covAActiveNode)
	n.Jail = openapi.NodeJail{ReleaseHeight: covAInt(release)}
	return n
}

func TestNewJailedKeysignFallbackActorCovA(t *testing.T) {
	a := NewJailedKeysignFallbackActor(rand.New(rand.NewPCG(1, 2)))
	if a.Name != "JailedKeysignFallback BTC.BTC refund" || len(a.Ops) != 8 || a.Timeout.Minutes() != 35 {
		t.Fatalf("unexpected actor %s ops=%d timeout=%s", a.Name, len(a.Ops), a.Timeout)
	}
	if !strings.Contains(a.Ops[0].String(), "selectJailedFallbackVault") || !strings.Contains(a.Ops[7].String(), "verifyRefundOutbound") {
		t.Fatalf("unexpected op order %s..%s", a.Ops[0], a.Ops[7])
	}
}

func TestJailedFallbackVaultSetupFromPolicyCovA(t *testing.T) {
	members := []string{"a", "b", "c", "d"}
	signers := common.PubKeys{"a", "b", "c", "d"}
	for _, tc := range []struct {
		name    string
		members []string
		policy  thorclient.KeysignPolicy
		reason  string
		jail    int
	}{
		{"small vault", members[:3], thorclient.KeysignPolicy{RequiredSigners: 2}, "cannot exercise fallback promotion", 2},
		{"one jail", members, thorclient.KeysignPolicy{RequiredSigners: 4}, "cannot exercise fallback promotion", 1},
		{"no threshold", members, thorclient.KeysignPolicy{RequiredSigners: 0}, "no required signer threshold", 5},
		{"never signers", members, thorclient.KeysignPolicy{RequiredSigners: 3, NeverSigners: common.PubKeys{"a"}}, "never-signers", 2},
		{"jailed fallbacks", members, thorclient.KeysignPolicy{RequiredSigners: 3, JailedFallbackSigners: common.PubKeys{"a"}}, "jailed fallback signers", 2},
		{"filtered", members, thorclient.KeysignPolicy{RequiredSigners: 3, Signers: signers[:3]}, "already filtered", 2},
		{"ok", members, thorclient.KeysignPolicy{RequiredSigners: 3, Signers: signers}, "", 2},
	} {
		setup, reason := jailedFallbackVaultSetupFromPolicy(openapi.Vault{Membership: tc.members}, tc.policy)
		if (tc.reason == "" && reason != "") || !strings.Contains(reason, tc.reason) {
			t.Fatalf("%s: reason %q, want %q", tc.name, reason, tc.reason)
		}
		if setup.jailCount != tc.jail || setup.memberCount != len(tc.members) || setup.requiredSigners != tc.policy.RequiredSigners {
			t.Fatalf("%s: unexpected setup %+v", tc.name, setup)
		}
	}
}

func TestJailedSelectFallbackVaultCovA(t *testing.T) {
	api := newCovThornode(t)
	users, bridges := covAValidators(t, 4)
	cfg := &simtypes.OpConfig{NodeUsers: users}
	vpk := covAVaultPubKey(t)
	vaultPath := "/thorchain/vault/" + vpk.String()
	nodes := covANodes(t, users, covAActiveNode)
	a := newCovAJailed()

	covAAssertPending(t, "no vault", a.selectJailedFallbackVault(cfg))

	api.JSON(covAInboundPath, covAInbound(vpk))
	api.JSON(vaultPath, covAVault(vpk, "ActiveVault", covAMembers(nodes)))
	bridges[0].HeightFn = func() (int64, error) { return 0, errors.New("down") }
	covAAssertPending(t, "height error", a.selectJailedFallbackVault(cfg))

	bridges[0].HeightFn = func() (int64, error) { return 100, nil }
	covAAssertPending(t, "policy error", a.selectJailedFallbackVault(cfg))

	policy := thorclient.KeysignPolicy{RequiredSigners: 3, Signers: covAPubKeys(users), NeverSigners: common.PubKeys{"x"}}
	bridges[0].KeysignPolicyFn = func(common.PubKey, int64) (thorclient.KeysignPolicy, error) { return policy, nil }
	res := a.selectJailedFallbackVault(cfg)
	if !res.Finish || res.Error != nil {
		t.Fatalf("unusable vault must skip, got %+v", res)
	}

	policy.NeverSigners = nil
	covAAssertPending(t, "nodes error", a.selectJailedFallbackVault(cfg))

	// Two of four members are already jailed, so only two candidates remain
	// for the two required jails, but a jailed node still votes.
	jailed := append([]openapi.Node{}, nodes...)
	jailed[0] = covAJailedNode(t, users[0], 500)
	jailed[1] = covAJailedNode(t, users[1], 500)
	jailed[2].Status = covAStandbyNode
	api.JSON(covANodesPath, jailed)
	covAAssertPending(t, "not enough candidates", a.selectJailedFallbackVault(cfg))

	// Only two local voters: excluding a target leaves one vote of four.
	api.JSON(covANodesPath, nodes)
	covAAssertPending(t, "no consensus", a.selectJailedFallbackVault(&simtypes.OpConfig{NodeUsers: users[:2]}))
	if !a.vaultPubKey.IsEmpty() {
		t.Fatal("vault selected without vote consensus")
	}

	covAAssertContinue(t, "select", a.selectJailedFallbackVault(cfg))
	if !a.vaultPubKey.Equals(vpk) || !a.expectedOutboundVault.Equals(vpk) || a.memberCount != 4 {
		t.Fatalf("unexpected selection vault=%s members=%d", a.vaultPubKey, a.memberCount)
	}
	if len(a.targets) != 2 || len(a.voters) != 4 || !a.expectPreferredExclusion {
		t.Fatalf("targets=%d voters=%d preferred=%v", len(a.targets), len(a.voters), a.expectPreferredExclusion)
	}
}

func TestBroadcastKeysignFailureUntilJailedCovA(t *testing.T) {
	api := newCovThornode(t)
	users, bridges := covAValidators(t, 4)
	cfg := &simtypes.OpConfig{NodeUsers: users}
	nodes := covANodes(t, users, covAActiveNode)
	byAddr, err := nodeUsersByThorAddress(cfg)
	if err != nil {
		t.Fatal(err)
	}
	targets, voters := jailableVaultMembers(covAVault("v", "ActiveVault", covAMembers(nodes)), nodes, byAddr, 100)
	if len(targets) != 4 || len(voters) != 4 {
		t.Fatalf("targets=%d voters=%d", len(targets), len(voters))
	}
	a := newCovAJailed()
	a.vaultPubKey = covAVaultPubKey(t)
	a.memberCount = 4
	a.targets = targets[:2]
	a.voters = voters
	target := a.targets[0]
	targetIdx := -1
	for i, n := range nodes {
		if n.NodeAddress == target.node.NodeAddress {
			targetIdx = i
		}
	}

	bridges[0].HeightFn = func() (int64, error) { return 0, errors.New("down") }
	covAAssertPending(t, "height error", a.jailPreferredExcludedMember(cfg))
	bridges[0].HeightFn = func() (int64, error) { return 100, nil }
	covAAssertPending(t, "nodes error", a.jailPreferredExcludedMember(cfg))

	others := append([]openapi.Node{}, nodes[:targetIdx]...)
	others = append(others, nodes[targetIdx+1:]...)
	api.JSON(covANodesPath, others)
	covAAssertFatal(t, "target missing", a.jailPreferredExcludedMember(cfg), "not found")

	// Votes from every unlocked non-target member at the first failure height.
	api.JSON(covANodesPath, nodes)
	locked := users[(targetIdx+1)%4]
	if !locked.Acquire() {
		t.Fatal("acquire")
	}
	var heights []int64
	var memos []string
	for _, b := range bridges {
		b.PostKeysignFailureFn = func(blame stypes.Blame, h int64, memo string, coins common.Coins, pk common.PubKey) (common.TxID, error) {
			heights = append(heights, h)
			memos = append(memos, memo)
			if !pk.Equals(a.vaultPubKey) || len(blame.BlameNodes) != 1 || blame.BlameNodes[0].Pubkey != target.pubKey.String() {
				t.Errorf("unexpected keysign failure for %s: %+v", pk, blame)
			}
			if !coins[0].Asset.Equals(common.BTCAsset) || blame.FailReason != jailedKeysignFallbackReason {
				t.Errorf("unexpected coins %v / reason %q", coins, blame.FailReason)
			}
			return "TX", nil
		}
	}
	covAAssertPending(t, "votes", a.jailPreferredExcludedMember(cfg))
	if len(heights) != 2 {
		t.Fatalf("expected two votes (one voter locked, target excluded), got %d", len(heights))
	}
	locked.Release()

	// Later rounds reuse the original failure height and skip prior voters.
	bridges[0].HeightFn = func() (int64, error) { return 105, nil }
	covAAssertPending(t, "remaining vote", a.jailPreferredExcludedMember(cfg))
	if len(heights) != 3 || heights[2] != 100 {
		t.Fatalf("expected third vote at original height 100, got %v", heights)
	}
	if !strings.HasPrefix(memos[0], "SIM-JAILED-FALLBACK:") {
		t.Fatalf("unexpected memo %q", memos[0])
	}

	// Post failures are fatal.
	a.jailVotes = map[string]map[string]bool{}
	for _, b := range bridges {
		b.PostKeysignFailureFn = func(stypes.Blame, int64, string, common.Coins, common.PubKey) (common.TxID, error) {
			return "", errors.New("rejected")
		}
	}
	covAAssertFatal(t, "post error", a.jailPreferredExcludedMember(cfg), "rejected")

	// Jailed past the current height: done.
	jailed := append([]openapi.Node{}, nodes...)
	jailed[targetIdx] = covAJailedNode(t, users[targetIdx], 500)
	api.JSON(covANodesPath, jailed)
	covAAssertContinue(t, "jailed", a.jailPreferredExcludedMember(cfg))

	// jailFallbackPromotionMembers: the second target is not yet jailed, then
	// fails, then is jailed.
	covAAssertFatal(t, "promotion error", a.jailFallbackPromotionMembers(cfg), "rejected")
	for _, b := range bridges {
		b.PostKeysignFailureFn = nil
	}
	covAAssertPending(t, "promotion pending", a.jailFallbackPromotionMembers(cfg))
	for i, n := range nodes {
		if n.NodeAddress == a.targets[1].node.NodeAddress {
			jailed[i] = covAJailedNode(t, users[i], 500)
		}
	}
	api.JSON(covANodesPath, jailed)
	covAAssertContinue(t, "promotion jailed", a.jailFallbackPromotionMembers(cfg))
}

func TestJailedVerifyPoliciesCovA(t *testing.T) {
	users, bridges := covAValidators(t, 4)
	cfg := &simtypes.OpConfig{NodeUsers: users}
	nodes := covANodes(t, users, covAActiveNode)
	t0, err := newForcedToLeaveNode(nodes[0])
	if err != nil {
		t.Fatal(err)
	}
	t1, err := newForcedToLeaveNode(nodes[1])
	if err != nil {
		t.Fatal(err)
	}
	a := newCovAJailed()
	a.vaultPubKey = covAVaultPubKey(t)
	a.targets = []forcedToLeaveNode{t0, t1}
	a.expectPreferredExclusion = true
	all := covAPubKeys(users)
	clean := all[1:]

	var policy thorclient.KeysignPolicy
	setPolicy := func(p thorclient.KeysignPolicy) {
		policy = p
		bridges[0].KeysignPolicyFn = func(common.PubKey, int64) (thorclient.KeysignPolicy, error) { return policy, nil }
	}

	// verifyJailedPreferredExclusion
	bridges[0].HeightFn = func() (int64, error) { return 0, errors.New("down") }
	covAAssertPending(t, "height error", a.verifyJailedPreferredExclusion(cfg))
	bridges[0].HeightFn = nil
	covAAssertPending(t, "policy error", a.verifyJailedPreferredExclusion(cfg))

	setPolicy(thorclient.KeysignPolicy{Signers: all, RequiredSigners: 3})
	covAAssertPending(t, "not fallback yet", a.verifyJailedPreferredExclusion(cfg))

	setPolicy(thorclient.KeysignPolicy{Signers: all, JailedFallbackSigners: all[:1], RequiredSigners: 3})
	covAAssertFatal(t, "selected while clean", a.verifyJailedPreferredExclusion(cfg), "was selected while clean signers satisfy threshold")
	a.expectPreferredExclusion = false
	covAAssertContinue(t, "promoted immediately", a.verifyJailedPreferredExclusion(cfg))
	a.expectPreferredExclusion = true

	setPolicy(thorclient.KeysignPolicy{Signers: clean[:2], JailedFallbackSigners: all[:1], RequiredSigners: 3})
	covAAssertFatal(t, "below threshold", a.verifyJailedPreferredExclusion(cfg), "preferred signer policy below threshold")

	setPolicy(thorclient.KeysignPolicy{Signers: clean, JailedFallbackSigners: all[:1], RequiredSigners: 3})
	covAAssertContinue(t, "excluded", a.verifyJailedPreferredExclusion(cfg))

	// verifyJailedFallbackPromotion
	bridges[0].KeysignPolicyFn = nil
	covAAssertPending(t, "promotion policy error", a.verifyJailedFallbackPromotion(cfg))

	setPolicy(thorclient.KeysignPolicy{Signers: clean, JailedFallbackSigners: all[:1], RequiredSigners: 3})
	covAAssertPending(t, "second target not fallback", a.verifyJailedFallbackPromotion(cfg))

	setPolicy(thorclient.KeysignPolicy{Signers: all[2:], JailedFallbackSigners: all[:2], RequiredSigners: 3})
	covAAssertFatal(t, "promotion below threshold", a.verifyJailedFallbackPromotion(cfg), "fallback signer policy below threshold")

	setPolicy(thorclient.KeysignPolicy{Signers: all[2:], JailedFallbackSigners: all[:2], RequiredSigners: 2})
	covAAssertFatal(t, "nothing promoted", a.verifyJailedFallbackPromotion(cfg), "no jailed fallback signer was promoted")

	setPolicy(thorclient.KeysignPolicy{Signers: all[1:], JailedFallbackSigners: all[:2], RequiredSigners: 3})
	covAAssertContinue(t, "promoted", a.verifyJailedFallbackPromotion(cfg))
}

func TestJailedRefundOpsCovA(t *testing.T) {
	a := newCovAJailed()
	a.vaultPubKey = covAVaultPubKey(t)
	covARunRefundOps(t, covARefundOps{
		swap:        &a.SwapActor,
		vaultPubKey: a.vaultPubKey,
		acquire:     a.acquireRefundUser,
		send:        a.sendRefundInbound,
		verify:      a.verifyRefundOutbound,
		txID:        func() string { return a.refundTxID },
		memo:        "TEST-JAILED-FALLBACK-REFUND",
	})
}

func TestJailedHelpersCovA(t *testing.T) {
	users, _ := covAValidators(t, 3)
	cfg := &simtypes.OpConfig{NodeUsers: users[:2]}
	byAddr, err := nodeUsersByThorAddress(cfg)
	if err != nil {
		t.Fatal(err)
	}
	active := covANode(t, users[0], covAActiveNode)
	jailed := covAJailedNode(t, users[1], 150)
	noUser := covANode(t, users[2], covAActiveNode)
	forced := covANode(t, users[0], covAActiveNode)
	forced.ForcedToLeave = true
	bad := covANode(t, users[1], covAActiveNode)
	bad.NodeAddress = "bad"

	vault := openapi.Vault{Membership: []string{*active.PubKeySet.Secp256k1, *jailed.PubKeySet.Secp256k1, *noUser.PubKeySet.Secp256k1, "unknown"}}
	candidates, voters := jailableVaultMembers(vault, []openapi.Node{active, jailed, noUser}, byAddr, 100)
	if len(candidates) != 1 || candidates[0].node.NodeAddress != active.NodeAddress || len(voters) != 2 {
		t.Fatalf("candidates=%d voters=%d", len(candidates), len(voters))
	}
	// Forced nodes and malformed addresses are neither candidates nor voters.
	byAddr["bad"] = users[1]
	c2, v2 := jailableVaultMembers(openapi.Vault{Membership: []string{*forced.PubKeySet.Secp256k1, *bad.PubKeySet.Secp256k1}}, []openapi.Node{forced, bad}, byAddr, 100)
	if len(c2) != 0 || len(v2) != 0 {
		t.Fatalf("forced/bad nodes leaked: %d %d", len(c2), len(v2))
	}
	// After the release height the node is no longer jailed.
	if !nodeIsJailedAt(jailed, 100) || nodeIsJailedAt(jailed, 150) || nodeIsJailedAt(active, 0) {
		t.Fatal("nodeIsJailedAt mismatch")
	}
	if keysignFailureVotesCanReachConsensus(voters, candidates, 4) {
		t.Fatal("one remaining voter of four cannot reach simple majority")
	}
	if !keysignFailureVotesCanReachConsensus(voters, candidates, 2) {
		t.Fatal("one voter of two is a simple majority")
	}
}

func TestXMRHistoricalSignerGateCovA(t *testing.T) {
	api := newCovThornode(t)
	users, bridges := covAValidators(t, 5)
	cfg := &simtypes.OpConfig{NodeUsers: users}
	nodes := covANodes(t, users, covAActiveNode)
	frost := covAVaultPubKey(t).String()
	vault := openapi.Vault{
		PubKeyFrost: &frost,
		Status:      "RetiringVault",
		Membership:  covAMembers(append(nodes[:3:3], covANode(t, users[3], covAStandbyNode))),
	}
	// member 3 is standby, so exactly three active members can vote.
	nodes[3].Status = covAStandbyNode

	res := NewXMRHistoricalSignerGate(vault)(&simtypes.OpConfig{NodeUsers: users[:4]})
	covAAssertFatal(t, "too few validators", res, "five running validators")

	gate := NewXMRHistoricalSignerGate(vault)
	if res := gate(cfg); res.Error == nil || res.Finish || res.Continue {
		t.Fatalf("node lookup error should retry with error, got %+v", res)
	}
	api.JSON(covANodesPath, nodes)
	bridges[0].HeightFn = func() (int64, error) { return 0, errors.New("down") }
	if res := gate(cfg); res.Error == nil || res.Finish {
		t.Fatalf("height error should retry with error, got %+v", res)
	}
	bridges[0].HeightFn = nil

	wrong := vault
	wrong.Membership = vault.Membership[:3]
	covAAssertFatal(t, "wrong vault", NewXMRHistoricalSignerGate(wrong)(cfg), "four-member vault")

	var posted []common.PubKey
	for _, b := range bridges {
		b.PostKeysignFailureFn = func(_ stypes.Blame, _ int64, _ string, coins common.Coins, pk common.PubKey) (common.TxID, error) {
			posted = append(posted, pk)
			if !coins[0].Asset.Equals(common.XMRAsset) {
				t.Errorf("expected XMR coins, got %v", coins)
			}
			return "TX", nil
		}
	}
	covAAssertPending(t, "voting", gate(cfg))
	if len(posted) != 2 || posted[0].String() != frost {
		t.Fatalf("expected two votes against the frost key, got %v", posted)
	}

	// Find the selected target (lowest address among active members) and jail
	// it with too little remaining jail time.
	var targetIdx int
	for i := 0; i < 3; i++ {
		if nodes[i].NodeAddress < nodes[targetIdx].NodeAddress {
			targetIdx = i
		}
	}
	jailed := append([]openapi.Node{}, nodes...)
	jailed[targetIdx] = covAJailedNode(t, users[targetIdx], 150)
	api.JSON(covANodesPath, jailed)
	covAAssertFatal(t, "short jail", gate(cfg), "at least 120 jail blocks")

	jailed[targetIdx] = covAJailedNode(t, users[targetIdx], 700)
	api.JSON(covANodesPath, jailed)
	covAAssertContinue(t, "gate ready", gate(cfg))
}
