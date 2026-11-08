package tss

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	cometsecp "github.com/cometbft/cometbft/crypto/secp256k1"
	cossecp "github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	legacybech32 "github.com/cosmos/cosmos-sdk/types/bech32/legacybech32"

	"gitlab.com/thorchain/thornode/v3/bifrost/p2p/conversion"
	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

// A five-member vault signs with a minimal four-member party. Three members
// combine; the fourth prepared and released its share but its combine failed.
// Without adopting their result it never attests, and the source-vault
// certificate (four of five members) can never form.
func TestFrostWaitForResultAdoptsResultSpendingPreparedInputs(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	result := mustSuccessResult(t, "msg-id", testExpectedPlanHash, testHashOf('b'), "deadbeef", 10)
	prepared := []FrostSpentOutputRef{{TxHash: testHashOf('c'), OutputIndex: 1, KeyImage: testHashOf('d')}}
	foreign := []FrostSpentOutputRef{{TxHash: testHashOf('e'), OutputIndex: 1, KeyImage: testHashOf('d')}}

	for _, tc := range []struct {
		name     string
		prepared []FrostSpentOutputRef
		senders  int
		adopt    bool
	}{
		{name: "two results spending prepared inputs", prepared: prepared, senders: 2, adopt: true},
		{name: "three combiners", prepared: prepared, senders: 3, adopt: true},
		{name: "one result is not enough", prepared: prepared, senders: 1, adopt: false},
		{name: "result spends other inputs", prepared: foreign, senders: 3, adopt: false},
		{name: "no local prepare", prepared: nil, senders: 3, adopt: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgChan := newFrostSignInbox()
			coord := &FrostSignCoordinator{
				localPubKey:      testPubKeys[0],
				msgID:            "msg-id",
				expectedPlanHash: testExpectedPlanHash,
				inbox:            msgChan,
				timeout:          time.Second,
				sidecar:          &knownTxSidecar{},
				preparedInputs:   tc.prepared,
				verifyAdopted: func(FrostKeySignRequest, []FrostSpentOutputRef, FrostKeySignResponse) error {
					return nil
				},
			}
			for _, sender := range party[1 : 1+tc.senders] {
				enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{
					SenderPubKey: sender,
					Response:     result,
				})
			}
			// The full threshold+1 quorum is four; at most three results exist.
			resp, err := coord.WaitForResult(party, 4, 150*time.Millisecond)
			if !tc.adopt {
				if err == nil {
					t.Fatalf("adopted %+v", resp)
				}
				return
			}
			if err != nil {
				t.Fatalf("wait for result: %v", err)
			}
			if resp.Status != tcommon.Success || resp.TxHash != testHashOf('b') || len(resp.SpentOutputRefs) != 1 {
				t.Fatalf("unexpected response: %+v", resp)
			}
		})
	}
}

// goldenResult is the gossip result two peers report for the 832c migration,
// with an eventuality consistent with its (possibly tampered) hex.
func goldenResult(t *testing.T, prepared []FrostSpentOutputRef, resp FrostKeySignResponse) FrostResult {
	t.Helper()
	eventuality, err := BuildFrostEventuality(testExpectedPlanHash, resp.TxHash, resp.TxHex, resp.TxKey, resp.FeeAtomic)
	if err != nil {
		t.Fatalf("build eventuality: %v", err)
	}
	refs := make([]FrostSpentOutputRef, len(prepared))
	for i, ref := range prepared {
		ref.SpendTxHash = resp.TxHash
		refs[i] = ref
	}
	return FrostResult{
		MsgID:           "msg-id",
		TxHash:          resp.TxHash,
		TxHex:           resp.TxHex,
		TxKey:           resp.TxKey,
		FeeAtomic:       resp.FeeAtomic,
		Eventuality:     eventuality,
		SpentOutputRefs: refs,
		Status:          uint8(tcommon.Success),
	}
}

// Two colluding party members report a transaction whose refs name this node's
// prepared inputs but whose bytes pay elsewhere. The node decodes the
// transaction itself and does not adopt it; the genuine transaction is adopted.
func TestFrostWaitForResultRejectsTamperedTxFromTwoPeers(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	req, prepared, genuine := golden832c(t)
	tx, err := parseXMRTx(mustHex(t, genuine.TxHex))
	if err != nil {
		t.Fatal(err)
	}
	tampered := genuine
	tampered.TxHex = replaceOnce(t, genuine.TxHex, hex.EncodeToString(tx.outputs[0].key[:]), strings.Repeat("11", 32))
	tampered.TxHash = retxHash(t, tampered.TxHex)

	// Prepare reports no key image for outputs the vault has not spent before.
	firstSpend := make([]FrostSpentOutputRef, len(prepared))
	for i, ref := range prepared {
		firstSpend[i] = ref
		firstSpend[i].KeyImage = ""
	}

	for _, tc := range []struct {
		name     string
		resp     FrostKeySignResponse
		prepared []FrostSpentOutputRef
		adopt    bool
	}{
		{name: "genuine transaction", resp: genuine, prepared: prepared, adopt: true},
		{name: "genuine transaction, first-spend inputs", resp: genuine, prepared: firstSpend, adopt: true},
		{name: "tampered transaction", resp: tampered, prepared: prepared, adopt: false},
		{name: "tampered transaction, first-spend inputs", resp: tampered, prepared: firstSpend, adopt: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgChan := newFrostSignInbox()
			coord := &FrostSignCoordinator{
				localPubKey:      testPubKeys[0],
				msgID:            "msg-id",
				expectedPlanHash: testExpectedPlanHash,
				inbox:            msgChan,
				timeout:          time.Second,
				sidecar:          &knownTxSidecar{},
				preparedInputs:   tc.prepared,
				preparedRequest:  req,
				verifyAdopted: func(req FrostKeySignRequest, prepared []FrostSpentOutputRef, resp FrostKeySignResponse) error {
					return verifyFrostTxForVault(req, prepared, resp, golden832cVaultAddress)
				},
			}
			result := goldenResult(t, prepared, tc.resp)
			for _, sender := range party[1:3] {
				enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{
					SenderPubKey: sender,
					Response:     result,
				})
			}
			resp, err := coord.WaitForResult(party, 4, 150*time.Millisecond)
			if !tc.adopt {
				if err == nil {
					t.Fatalf("adopted tampered result %s", resp.TxHash)
				}
				return
			}
			if err != nil {
				t.Fatalf("wait for result: %v", err)
			}
			if resp.TxHash != genuine.TxHash {
				t.Fatalf("adopted %s", resp.TxHash)
			}
		})
	}
}

// testNonPartyPubKey is a source-vault member outside the four-member party.
const testNonPartyPubKey = "thorpub1addwnpepqfz9hs6zl3xcsd2wrwqzxf8kdmw5xqwnjt3xspghlclpd2xcxyznycxcvlr"

// Stagenet 7969: three of a four-member party combined and the vault's fifth
// member sat outside the party. It holds the scheduled txout but no prepare, so
// it verifies the transaction against the txout, binds the reported funding
// refs to the vault outputs its own sidecar tracks, and adopts it from a third
// of the party (two of four). Colluders cannot sign alone but can rebroadcast
// the genuine transaction with another funding ref; that must not complete the
// source-vault certificate through this member.
func TestFrostWaitForResultNonPartyMemberAdoptsScheduledTxOut(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	members := append([]string{testNonPartyPubKey}, party...)
	req, genuine := golden7969(t)
	req.SignerPubKeys = members
	tracked := golden7969Outputs(t)
	tx, err := parseXMRTx(mustHex(t, genuine.TxHex))
	if err != nil {
		t.Fatal(err)
	}
	tampered := genuine
	tampered.TxHex = replaceOnce(t, genuine.TxHex, hex.EncodeToString(tx.outputs[0].key[:]), strings.Repeat("11", 32))
	tampered.TxHash = retxHash(t, tampered.TxHex)
	// The real transaction and key image, but a funding ref naming another
	// output the vault holds.
	misattributed := genuine
	misattributed.SpentOutputRefs = []FrostSpentOutputRef{genuine.SpentOutputRefs[0]}
	misattributed.SpentOutputRefs[0].TxHash = tracked[1].TxHash
	misattributed.SpentOutputRefs[0].OutputIndex = tracked[1].OutputIndex

	verifyGolden := func(req FrostKeySignRequest, resp FrostKeySignResponse, outputs []FrostVaultOutput) error {
		return verifyScheduledFrostTxForVault(req, resp, outputs, golden7969VaultAddress)
	}
	withOutputs := func(outputs []FrostVaultOutput, err error) FrostSidecarClient {
		return &vaultOutputsSidecar{outputs: outputs, err: err}
	}

	for _, tc := range []struct {
		name    string
		resp    FrostKeySignResponse
		senders int
		member  bool
		sidecar FrostSidecarClient
		adopt   bool
	}{
		{name: "three genuine results", resp: genuine, senders: 3, member: true, sidecar: withOutputs(tracked, nil), adopt: true},
		{name: "two genuine results are a third of the party", resp: genuine, senders: 2, member: true, sidecar: withOutputs(tracked, nil), adopt: true},
		{name: "two colluding senders, tampered transaction", resp: tampered, senders: 2, member: true, sidecar: withOutputs(tracked, nil), adopt: false},
		{name: "two colluding senders misattributing the funding ref", resp: misattributed, senders: 2, member: true, sidecar: withOutputs(tracked, nil), adopt: false},
		{name: "one result is not enough", resp: genuine, senders: 1, member: true, sidecar: withOutputs(tracked, nil), adopt: false},
		{name: "three colluding senders misattributing the funding ref", resp: misattributed, senders: 3, member: true, sidecar: withOutputs(tracked, nil), adopt: false},
		{name: "three colluding senders, tampered transaction", resp: tampered, senders: 3, member: true, sidecar: withOutputs(tracked, nil), adopt: false},
		{name: "funding output not scanned yet", resp: genuine, senders: 3, member: true, sidecar: withOutputs(tracked[1:], nil), adopt: false},
		{name: "sidecar fails to list outputs", resp: genuine, senders: 3, member: true, sidecar: withOutputs(nil, errors.New("down")), adopt: false},
		{name: "sidecar cannot list outputs", resp: genuine, senders: 3, member: true, sidecar: &mockFrostSidecarSign{}, adopt: false},
		{name: "no sidecar", resp: genuine, senders: 3, member: true, adopt: false},
		{name: "not a source-vault member", resp: genuine, senders: 3, member: false, sidecar: withOutputs(tracked, nil), adopt: false},
		{name: "threshold+1 results adopt without verification", resp: misattributed, senders: 4, member: true, adopt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgChan := newFrostSignInbox()
			coord := &FrostSignCoordinator{
				sidecar:          tc.sidecar,
				localPubKey:      testNonPartyPubKey,
				msgID:            "msg-id",
				expectedPlanHash: testExpectedPlanHash,
				inbox:            msgChan,
				timeout:          time.Second,
				verifyScheduled:  verifyGolden,
			}
			scheduled := req
			if !tc.member {
				scheduled.SignerPubKeys = party[1:]
			}
			if got := coord.adoptScheduledTxOut(scheduled); got != tc.member {
				t.Fatalf("adoptScheduledTxOut = %v", got)
			}
			result := goldenResult(t, tc.resp.SpentOutputRefs, tc.resp)
			for _, sender := range party[:tc.senders] {
				enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{
					SenderPubKey: sender,
					Response:     result,
				})
			}
			resp, err := coord.WaitForResult(party, 4, 150*time.Millisecond)
			if !tc.adopt {
				if err == nil {
					t.Fatalf("adopted %s", resp.TxHash)
				}
				return
			}
			if err != nil {
				t.Fatalf("wait for result: %v", err)
			}
			if resp.Status != tcommon.Success || resp.TxHash != tc.resp.TxHash {
				t.Fatalf("adopted %+v", resp)
			}
		})
	}
}

// vaultOutputsSidecar is a sidecar that lists the vault outputs it tracks.
type vaultOutputsSidecar struct {
	knownTxSidecar
	outputs []FrostVaultOutput
	err     error
}

func (s *vaultOutputsSidecar) FrostVaultOutputs(context.Context, string) ([]FrostVaultOutput, error) {
	return s.outputs, s.err
}

// bindingLaterSidecar lists the vault outputs unbound for the first calls, then
// bound, as after an authenticated spent-ref import during the wait.
type bindingLaterSidecar struct {
	knownTxSidecar
	unbound, bound []FrostVaultOutput
	unboundFor     int
	listed         int
}

func (s *bindingLaterSidecar) FrostVaultOutputs(context.Context, string) ([]FrostVaultOutput, error) {
	s.listed++
	if s.listed <= s.unboundFor {
		return s.unbound, nil
	}
	return s.bound, nil
}

// The keysign policy leaves a jailed source-vault member out of the eligible
// signers, so it never joins the party, yet it still attests the outbound. The
// coordinator FrostKeySign builds must let it adopt a verified result below
// threshold+1, as for a member that party formation left out.
func TestFrostKeySignCoordinatorLetsPolicyExcludedMemberAdopt(t *testing.T) {
	ensureBech32Prefix()
	eligible := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	req, genuine := golden7969(t)
	req.SignerPubKeys = append([]string{testNonPartyPubKey}, eligible...)
	req.ActiveSignerPubKeys = eligible

	msgChan := newFrostSignInbox()
	server := &TssServer{localNodePubKey: testNonPartyPubKey}
	coord := server.newFrostKeySignCoordinator(req, &vaultOutputsSidecar{outputs: golden7969Outputs(t)}, "msg-id", testExpectedPlanHash, msgChan)
	coord.verifyScheduled = func(req FrostKeySignRequest, resp FrostKeySignResponse, outputs []FrostVaultOutput) error {
		return verifyScheduledFrostTxForVault(req, resp, outputs, golden7969VaultAddress)
	}
	result := goldenResult(t, genuine.SpentOutputRefs, genuine)
	for _, sender := range eligible[:3] {
		enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{
			SenderPubKey: sender,
			Response:     result,
		})
	}
	resp, err := coord.WaitForResult(eligible, 4, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("policy-excluded member did not adopt: %v", err)
	}
	if resp.Status != tcommon.Success || resp.TxHash != genuine.TxHash {
		t.Fatalf("adopted %+v", resp)
	}
}

// flakyOutputsSidecar fails to list the vault outputs, or lists them without
// the funding output, for its first calls, as a busy or lagging sidecar does.
type flakyOutputsSidecar struct {
	knownTxSidecar
	outputs  []FrostVaultOutput
	failures int
	lagging  int
	calls    int
}

func (s *flakyOutputsSidecar) FrostVaultOutputs(context.Context, string) ([]FrostVaultOutput, error) {
	s.calls++
	switch {
	case s.calls <= s.failures:
		return nil, errors.New("sidecar busy")
	case s.calls <= s.failures+s.lagging:
		return s.outputs[1:], nil
	}
	return s.outputs, nil
}

// Threshold members send their result once, so no further vote arrives after
// it. A sidecar that cannot list the funding output at that moment must not
// cost the member its adoption: the result is checked again until the timer
// runs out. A result that fails verification is not checked again.
func TestFrostWaitForResultRechecksScheduledTxOut(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	req, genuine := golden7969(t)
	req.SignerPubKeys = append([]string{testNonPartyPubKey}, party...)
	tx, err := parseXMRTx(mustHex(t, genuine.TxHex))
	if err != nil {
		t.Fatal(err)
	}
	tampered := genuine
	tampered.TxHex = replaceOnce(t, genuine.TxHex, hex.EncodeToString(tx.outputs[0].key[:]), strings.Repeat("11", 32))
	tampered.TxHash = retxHash(t, tampered.TxHex)

	for _, tc := range []struct {
		name      string
		resp      FrostKeySignResponse
		failures  int
		lagging   int
		adopt     bool
		wantCalls int
	}{
		{name: "sidecar busy at the threshold vote", resp: genuine, failures: 2, adopt: true, wantCalls: 3},
		{name: "funding output scanned after the threshold vote", resp: genuine, lagging: 2, adopt: true, wantCalls: 3},
		{name: "tampered transaction is not checked again", resp: tampered, adopt: false, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sidecar := &flakyOutputsSidecar{outputs: golden7969Outputs(t), failures: tc.failures, lagging: tc.lagging}
			msgChan := newFrostSignInbox()
			coord := &FrostSignCoordinator{
				sidecar:          sidecar,
				localPubKey:      testNonPartyPubKey,
				msgID:            "msg-id",
				expectedPlanHash: testExpectedPlanHash,
				inbox:            msgChan,
				timeout:          time.Second,
				recheckInterval:  10 * time.Millisecond,
				verifyScheduled: func(req FrostKeySignRequest, resp FrostKeySignResponse, outputs []FrostVaultOutput) error {
					return verifyScheduledFrostTxForVault(req, resp, outputs, golden7969VaultAddress)
				},
			}
			coord.adoptScheduledTxOut(req)
			result := goldenResult(t, tc.resp.SpentOutputRefs, tc.resp)
			for _, sender := range party[:3] {
				enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{
					SenderPubKey: sender,
					Response:     result,
				})
			}
			resp, err := coord.WaitForResult(party, 4, 300*time.Millisecond)
			if sidecar.calls != tc.wantCalls {
				t.Fatalf("listed vault outputs %d times, want %d", sidecar.calls, tc.wantCalls)
			}
			if !tc.adopt {
				if err == nil {
					t.Fatalf("adopted %s", resp.TxHash)
				}
				return
			}
			if err != nil {
				t.Fatalf("wait for result: %v", err)
			}
			if resp.Status != tcommon.Success || resp.TxHash != genuine.TxHash {
				t.Fatalf("adopted %+v", resp)
			}
		})
	}
}

// knownTxSidecar is a sidecar whose daemon holds every transaction once it has
// been asked about it more than unknownFor times.
type knownTxSidecar struct {
	mockFrostSidecarSign
	unknownFor int
	err        error
	asked      int
}

func (s *knownTxSidecar) FrostTxExists(context.Context, string) (bool, error) {
	s.asked++
	if s.err != nil {
		return false, s.err
	}
	return s.asked > s.unknownFor, nil
}

// Below threshold+1 the node does not verify signatures or range proofs, and a
// member without prepared inputs takes key images from the reported refs. So
// colluders below the signing threshold could hand it a transaction they never
// signed, with key images of their choosing, and its attestation would mark
// those spent for an outbound that never mines. Refs must name outputs whose key
// image is bound locally, and the node adopts only once its daemon holds the
// transaction; the signers' broadcast may reach the daemon after the results.
func TestFrostWaitForResultAdoptsOnlyWhatTheDaemonHolds(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	req, genuine := golden7969(t)
	req.SignerPubKeys = append([]string{testNonPartyPubKey}, party...)
	tracked := golden7969Outputs(t)

	// A forged transaction: the genuine bytes with a key image nobody can sign
	// for, its ref naming a never-spent vault output of the funding amount.
	forgedKeyImage := strings.Repeat("5a", 32)
	forged := genuine
	forged.TxHex = replaceOnce(t, genuine.TxHex, genuine.SpentOutputRefs[0].KeyImage, forgedKeyImage)
	forged.TxHash = retxHash(t, forged.TxHex)
	never := FrostVaultOutput{TxHash: strings.Repeat("cd", 32), OutputIndex: 1, Amount: tracked[0].Amount, State: "unspent"}
	forged.SpentOutputRefs = []FrostSpentOutputRef{{
		TxHash: never.TxHash, OutputIndex: never.OutputIndex, KeyImage: forgedKeyImage, SpendTxHash: forged.TxHash,
	}}
	withNever := append(append([]FrostVaultOutput{}, tracked...), never)
	// The never-spent output has no key image bound locally, so the local
	// checks already reject it; the daemon check below is a second barrier.
	if err := verifyScheduledFrostTxForVault(req, forged, withNever, golden7969VaultAddress); err == nil {
		t.Fatal("forged transaction bound a key image to an output with none of its own")
	}

	for _, tc := range []struct {
		name    string
		resp    FrostKeySignResponse
		outputs []FrostVaultOutput
		daemon  knownTxSidecar
		adopt   bool
	}{
		{name: "daemon holds the genuine transaction", resp: genuine, outputs: tracked, adopt: true},
		{name: "broadcast reaches the daemon after the results", resp: genuine, outputs: tracked, daemon: knownTxSidecar{unknownFor: 3}, adopt: true},
		{name: "daemon never holds the forged transaction", resp: forged, outputs: withNever, daemon: knownTxSidecar{unknownFor: 1 << 30}, adopt: false},
		{name: "daemon unreachable", resp: genuine, outputs: tracked, daemon: knownTxSidecar{err: errors.New("down")}, adopt: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sidecar := &vaultOutputsSidecar{knownTxSidecar: tc.daemon, outputs: tc.outputs}
			msgChan := newFrostSignInbox()
			coord := &FrostSignCoordinator{
				sidecar:          sidecar,
				localPubKey:      testNonPartyPubKey,
				msgID:            "msg-id",
				expectedPlanHash: testExpectedPlanHash,
				inbox:            msgChan,
				timeout:          time.Second,
				recheckInterval:  10 * time.Millisecond,
				verifyScheduled: func(req FrostKeySignRequest, resp FrostKeySignResponse, outputs []FrostVaultOutput) error {
					return verifyScheduledFrostTxForVault(req, resp, outputs, golden7969VaultAddress)
				},
			}
			coord.adoptScheduledTxOut(req)
			result := goldenResult(t, tc.resp.SpentOutputRefs, tc.resp)
			for _, sender := range party[:3] {
				enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{
					SenderPubKey: sender,
					Response:     result,
				})
			}
			resp, err := coord.WaitForResult(party, 4, 300*time.Millisecond)
			if !tc.adopt {
				if err == nil {
					t.Fatalf("adopted %s", resp.TxHash)
				}
				// The forged refs fail the local binding before the daemon is asked.
				if tc.resp.TxHash == forged.TxHash {
					if sidecar.asked != 0 {
						t.Fatalf("asked the daemon %d times about a result the local checks reject", sidecar.asked)
					}
					return
				}
				if sidecar.asked < 2 {
					t.Fatalf("asked the daemon %d times, want it asked again", sidecar.asked)
				}
				return
			}
			if err != nil {
				t.Fatalf("wait for result: %v", err)
			}
			if resp.Status != tcommon.Success || resp.TxHash != tc.resp.TxHash {
				t.Fatalf("adopted %+v", resp)
			}
		})
	}
}

// The result a member without prepared inputs adopts is what signTx persists
// and what its observation later imports as spent outputs. Senders below
// threshold+1 reporting the genuine, daemon-held transaction with its ref moved
// to an equal-amount output whose key image is not bound locally must not be
// adopted, so those refs never reach the spent-output store. A full quorum
// still decides.
func TestFrostWaitForResultDoesNotAdoptUnboundRefsBelowQuorum(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	req, genuine := golden7969(t)
	req.SignerPubKeys = append([]string{testNonPartyPubKey}, party...)
	tracked := golden7969Outputs(t)
	twin := FrostVaultOutput{TxHash: strings.Repeat("ef", 32), OutputIndex: 2, Amount: tracked[0].Amount, State: "unspent"}
	swapped := genuine
	ref := genuine.SpentOutputRefs[0]
	ref.TxHash, ref.OutputIndex = twin.TxHash, twin.OutputIndex
	swapped.SpentOutputRefs = []FrostSpentOutputRef{ref}

	for _, tc := range []struct {
		name    string
		senders []string
		adopt   bool
	}{
		{name: "below threshold+1", senders: party[:3]},
		{name: "full quorum", senders: party, adopt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sidecar := &vaultOutputsSidecar{outputs: append(append([]FrostVaultOutput{}, tracked...), twin)}
			msgChan := newFrostSignInbox()
			coord := &FrostSignCoordinator{
				sidecar:          sidecar,
				localPubKey:      testNonPartyPubKey,
				msgID:            "msg-id",
				expectedPlanHash: testExpectedPlanHash,
				inbox:            msgChan,
				timeout:          time.Second,
				recheckInterval:  10 * time.Millisecond,
				verifyScheduled: func(req FrostKeySignRequest, resp FrostKeySignResponse, outputs []FrostVaultOutput) error {
					return verifyScheduledFrostTxForVault(req, resp, outputs, golden7969VaultAddress)
				},
			}
			coord.adoptScheduledTxOut(req)
			result := goldenResult(t, swapped.SpentOutputRefs, swapped)
			for _, sender := range tc.senders {
				enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{SenderPubKey: sender, Response: result})
			}
			resp, err := coord.WaitForResult(party, 4, 300*time.Millisecond)
			if !tc.adopt {
				if err == nil {
					t.Fatalf("adopted refs %+v from below threshold+1", resp.SpentOutputRefs)
				}
				return
			}
			if err != nil {
				t.Fatalf("wait for result: %v", err)
			}
		})
	}
}

// A funding output whose key image the sidecar binds only during the wait is
// checked again, not rejected for the session.
func TestFrostWaitForResultAdoptsOnceTheKeyImageIsBound(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	req, genuine := golden7969(t)
	req.SignerPubKeys = append([]string{testNonPartyPubKey}, party...)
	bound := golden7969Outputs(t)
	unbound := append([]FrostVaultOutput(nil), bound...)
	unbound[0].KeyImage = ""
	sidecar := &bindingLaterSidecar{unbound: unbound, bound: bound, unboundFor: 3}
	msgChan := newFrostSignInbox()
	coord := &FrostSignCoordinator{
		sidecar:          sidecar,
		localPubKey:      testNonPartyPubKey,
		msgID:            "msg-id",
		expectedPlanHash: testExpectedPlanHash,
		inbox:            msgChan,
		timeout:          time.Second,
		recheckInterval:  10 * time.Millisecond,
		verifyScheduled: func(req FrostKeySignRequest, resp FrostKeySignResponse, outputs []FrostVaultOutput) error {
			return verifyScheduledFrostTxForVault(req, resp, outputs, golden7969VaultAddress)
		},
	}
	coord.adoptScheduledTxOut(req)
	result := goldenResult(t, genuine.SpentOutputRefs, genuine)
	for _, sender := range party[:3] {
		enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{SenderPubKey: sender, Response: result})
	}
	resp, err := coord.WaitForResult(party, 4, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("wait for result: %v", err)
	}
	if resp.TxHash != genuine.TxHash || sidecar.listed <= sidecar.unboundFor {
		t.Fatalf("adopted %s after %d listings", resp.TxHash, sidecar.listed)
	}
}

// A party member whose own combine failed adopts its prepared inputs' result
// from two senders only once its daemon holds the transaction, and not at all
// from a sidecar that cannot tell.
func TestFrostWaitForResultPreparedAdoptionNeedsTheDaemon(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	req, prepared, genuine := golden832c(t)
	for _, tc := range []struct {
		name    string
		sidecar FrostSidecarClient
		adopt   bool
	}{
		{name: "daemon holds it", sidecar: &knownTxSidecar{}, adopt: true},
		{name: "daemon gets it later", sidecar: &knownTxSidecar{unknownFor: 2}, adopt: true},
		{name: "daemon never gets it", sidecar: &knownTxSidecar{unknownFor: 1 << 30}, adopt: false},
		{name: "sidecar cannot tell", sidecar: &mockFrostSidecarSign{}, adopt: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgChan := newFrostSignInbox()
			coord := &FrostSignCoordinator{
				sidecar:          tc.sidecar,
				localPubKey:      testPubKeys[0],
				msgID:            "msg-id",
				expectedPlanHash: testExpectedPlanHash,
				inbox:            msgChan,
				timeout:          time.Second,
				recheckInterval:  10 * time.Millisecond,
				preparedInputs:   prepared,
				preparedRequest:  req,
				verifyAdopted: func(req FrostKeySignRequest, prepared []FrostSpentOutputRef, resp FrostKeySignResponse) error {
					return verifyFrostTxForVault(req, prepared, resp, golden832cVaultAddress)
				},
			}
			result := goldenResult(t, prepared, genuine)
			for _, sender := range party[1:3] {
				enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{
					SenderPubKey: sender,
					Response:     result,
				})
			}
			resp, err := coord.WaitForResult(party, 4, 200*time.Millisecond)
			if !tc.adopt {
				if err == nil {
					t.Fatalf("adopted %s", resp.TxHash)
				}
				return
			}
			if err != nil {
				t.Fatalf("wait for result: %v", err)
			}
			if resp.TxHash != genuine.TxHash {
				t.Fatalf("adopted %s", resp.TxHash)
			}
		})
	}
}

// A party member whose own prepare failed holds no prepared inputs, yet it
// still attests the outbound: it keeps the scheduled txout armed and adopts
// from a third of the party (frostScheduledResultQuorum), as a member outside
// the party does.
func TestFrostPartyMemberWhosePrepareFailedAdoptsScheduledTxOut(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	req, genuine := golden7969(t)
	req.SignerPubKeys = append([]string{testNonPartyPubKey}, party...)
	req.ActiveSignerPubKeys = party
	if req.VaultPubKey == "" {
		req.VaultPubKey = "thorpub1test-vault"
	}
	peerIDs, err := conversion.GetPeerIDsFromPubKeys(party)
	if err != nil {
		t.Fatal(err)
	}
	quorum := 4
	if got := frostScheduledResultQuorum(quorum); got != 2 {
		t.Fatalf("frostScheduledResultQuorum(%d) = %d", quorum, got)
	}

	for _, tc := range []struct {
		name    string
		senders int
		adopt   bool
	}{
		{name: "a third of the party", senders: frostScheduledResultQuorum(quorum), adopt: true},
		{name: "one sender is not enough", senders: frostScheduledResultQuorum(quorum) - 1, adopt: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgChan := newFrostSignInbox()
			sidecar := &vaultOutputsSidecar{outputs: golden7969Outputs(t)}
			sidecar.prepareErr = errors.New("prepare unavailable")
			server := &TssServer{localNodePubKey: party[0]}
			coord := server.newFrostKeySignCoordinator(req, sidecar, "msg-id", testExpectedPlanHash, msgChan)
			coord.recheckInterval = 10 * time.Millisecond
			coord.verifyScheduled = func(req FrostKeySignRequest, resp FrostKeySignResponse, outputs []FrostVaultOutput) error {
				return verifyScheduledFrostTxForVault(req, resp, outputs, golden7969VaultAddress)
			}
			if resp := coord.Execute(req, party, peerIDs); resp.Status != tcommon.Fail || !strings.Contains(resp.Blame.FailReason, "frost prepare failed") {
				t.Fatalf("prepare failure did not fail the ceremony: %+v", resp)
			}
			if !coord.scheduledTxOut || coord.preparedInputs != nil {
				t.Fatalf("prepare failure left scheduledTxOut=%v preparedInputs=%v", coord.scheduledTxOut, coord.preparedInputs)
			}
			result := goldenResult(t, genuine.SpentOutputRefs, genuine)
			for _, sender := range party[1 : 1+tc.senders] {
				enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{SenderPubKey: sender, Response: result})
			}
			resp, err := coord.WaitForResult(party, quorum, 150*time.Millisecond)
			if !tc.adopt {
				if err == nil {
					t.Fatalf("adopted %s from %d senders", resp.TxHash, tc.senders)
				}
				return
			}
			if err != nil {
				t.Fatalf("wait for result: %v", err)
			}
			if resp.Status != tcommon.Success || resp.TxHash != genuine.TxHash {
				t.Fatalf("adopted %+v", resp)
			}
		})
	}
}

// testPartyPubKeys returns n fresh account pubkeys for a party larger than the
// four fixed test keys.
func testPartyPubKeys(t *testing.T, n int) []string {
	t.Helper()
	keys := make([]string, n)
	for i := range keys {
		pk, err := legacybech32.MarshalPubKey(legacybech32.AccPK, &cossecp.PubKey{Key: cometsecp.GenPrivKey().PubKey().Bytes()}) //nolint:staticcheck
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = pk
	}
	return keys
}

// A member without prepared inputs adopts a scheduled-txout result from exactly
// a third of the party (rounded up): with a seven-member party, two senders are
// one short and three are enough. Colluders at that quorum still cannot get a
// tampered or misattributed transaction adopted.
func TestFrostScheduledAdoptionAtAThirdOfTheParty(t *testing.T) {
	ensureBech32Prefix()
	party := testPartyPubKeys(t, 7)
	quorum := len(party)
	third := frostScheduledResultQuorum(quorum)
	if third != 3 {
		t.Fatalf("a third of a party of %d is %d", quorum, third)
	}
	req, genuine := golden7969(t)
	req.SignerPubKeys = append([]string{testNonPartyPubKey}, party...)
	tracked := golden7969Outputs(t)
	tx, err := parseXMRTx(mustHex(t, genuine.TxHex))
	if err != nil {
		t.Fatal(err)
	}
	tampered := genuine
	tampered.TxHex = replaceOnce(t, genuine.TxHex, hex.EncodeToString(tx.outputs[0].key[:]), strings.Repeat("11", 32))
	tampered.TxHash = retxHash(t, tampered.TxHex)
	misattributed := genuine
	misattributed.SpentOutputRefs = []FrostSpentOutputRef{genuine.SpentOutputRefs[0]}
	misattributed.SpentOutputRefs[0].TxHash = tracked[1].TxHash
	misattributed.SpentOutputRefs[0].OutputIndex = tracked[1].OutputIndex

	for _, tc := range []struct {
		name    string
		resp    FrostKeySignResponse
		senders int
		adopt   bool
	}{
		{name: "one short of a third", resp: genuine, senders: third - 1, adopt: false},
		{name: "a third", resp: genuine, senders: third, adopt: true},
		{name: "two colluders, tampered transaction", resp: tampered, senders: 2, adopt: false},
		{name: "a third colluding, tampered transaction", resp: tampered, senders: third, adopt: false},
		{name: "a third colluding, misattributed funding ref", resp: misattributed, senders: third, adopt: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgChan := newFrostSignInbox()
			coord := &FrostSignCoordinator{
				sidecar:          &vaultOutputsSidecar{outputs: tracked},
				localPubKey:      testNonPartyPubKey,
				msgID:            "msg-id",
				expectedPlanHash: testExpectedPlanHash,
				inbox:            msgChan,
				timeout:          time.Second,
				verifyScheduled: func(req FrostKeySignRequest, resp FrostKeySignResponse, outputs []FrostVaultOutput) error {
					return verifyScheduledFrostTxForVault(req, resp, outputs, golden7969VaultAddress)
				},
			}
			if !coord.adoptScheduledTxOut(req) {
				t.Fatal("source-vault member not armed for scheduled adoption")
			}
			result := goldenResult(t, tc.resp.SpentOutputRefs, tc.resp)
			for _, sender := range party[:tc.senders] {
				enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{
					SenderPubKey: sender,
					Response:     result,
				})
			}
			resp, err := coord.WaitForResult(party, quorum, 150*time.Millisecond)
			if !tc.adopt {
				if err == nil {
					t.Fatalf("adopted %s from %d senders", resp.TxHash, tc.senders)
				}
				return
			}
			if err != nil {
				t.Fatalf("wait for result: %v", err)
			}
			if resp.Status != tcommon.Success || resp.TxHash != genuine.TxHash {
				t.Fatalf("adopted %+v", resp)
			}
		})
	}
}
