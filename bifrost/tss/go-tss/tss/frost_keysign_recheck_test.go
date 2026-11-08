package tss

import (
	"context"
	"errors"
	"testing"
	"time"

	tcommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
)

// Once a result has passed the checks against this node's plan or the
// scheduled txout, waiting for the signers' broadcast to reach the daemon only
// asks the daemon again: the transaction is not decoded and the vault outputs
// are not listed on every tick.
func TestFrostWaitForResultRechecksOnlyTheDaemonOnceVerified(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	const unknownFor = 3

	t.Run("scheduled txout", func(t *testing.T) {
		req, genuine := golden7969(t)
		req.SignerPubKeys = append([]string{testNonPartyPubKey}, party...)
		sidecar := &flakyOutputsSidecar{knownTxSidecar: knownTxSidecar{unknownFor: unknownFor}, outputs: golden7969Outputs(t)}
		verified := 0
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
				verified++
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
		if resp.Status != tcommon.Success || resp.TxHash != genuine.TxHash {
			t.Fatalf("adopted %+v", resp)
		}
		if sidecar.asked != unknownFor+1 {
			t.Fatalf("asked the daemon %d times, want %d", sidecar.asked, unknownFor+1)
		}
		if sidecar.calls != 1 || verified != 1 {
			t.Fatalf("listed vault outputs %d times and verified %d times, want once each", sidecar.calls, verified)
		}
	})

	t.Run("prepared inputs", func(t *testing.T) {
		req, prepared, genuine := golden832c(t)
		sidecar := &knownTxSidecar{unknownFor: unknownFor}
		verified := 0
		msgChan := newFrostSignInbox()
		coord := &FrostSignCoordinator{
			sidecar:          sidecar,
			localPubKey:      party[0],
			msgID:            "msg-id",
			expectedPlanHash: testExpectedPlanHash,
			inbox:            msgChan,
			timeout:          time.Second,
			recheckInterval:  10 * time.Millisecond,
			preparedInputs:   prepared,
			preparedRequest:  req,
			verifyAdopted: func(req FrostKeySignRequest, prepared []FrostSpentOutputRef, resp FrostKeySignResponse) error {
				verified++
				return verifyFrostTxForVault(req, prepared, resp, golden832cVaultAddress)
			},
		}
		result := goldenResult(t, prepared, genuine)
		for _, sender := range party[1:3] {
			enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{SenderPubKey: sender, Response: result})
		}
		resp, err := coord.WaitForResult(party, 4, 500*time.Millisecond)
		if err != nil {
			t.Fatalf("wait for result: %v", err)
		}
		if resp.TxHash != genuine.TxHash {
			t.Fatalf("adopted %s", resp.TxHash)
		}
		if sidecar.asked != unknownFor+1 || verified != 1 {
			t.Fatalf("asked the daemon %d times and verified %d times, want %d and 1", sidecar.asked, verified, unknownFor+1)
		}
	})
}

// hungSidecar is a sidecar whose daemon and output calls hang until their
// context ends, or for a minute without one.
type hungSidecar struct {
	mockFrostSidecarSign
	hangOutputs bool
	deadlines   []time.Time
}

func (s *hungSidecar) hang(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Time{}
	}
	s.deadlines = append(s.deadlines, deadline)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Minute):
		return errors.New("sidecar timed out")
	}
}

func (s *hungSidecar) FrostTxExists(ctx context.Context, _ string) (bool, error) {
	return false, s.hang(ctx)
}

func (s *hungSidecar) FrostVaultOutputs(ctx context.Context, _ string) ([]FrostVaultOutput, error) {
	if s.hangOutputs {
		return nil, s.hang(ctx)
	}
	return nil, errors.New("not reached")
}

// A sidecar call made while verifying a result is bounded by the time left to
// wait for one: a hung sidecar must not hold WaitForResult past its timeout.
func TestFrostWaitForResultBoundsSidecarCallsByItsTimeout(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	const timeout = 150 * time.Millisecond

	for _, tc := range []struct {
		name      string
		scheduled bool
	}{
		{name: "daemon hangs, prepared inputs"},
		{name: "vault outputs hang, scheduled txout", scheduled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sidecar := &hungSidecar{hangOutputs: tc.scheduled}
			msgChan := newFrostSignInbox()
			coord := &FrostSignCoordinator{
				sidecar:          sidecar,
				msgID:            "msg-id",
				expectedPlanHash: testExpectedPlanHash,
				inbox:            msgChan,
				timeout:          time.Second,
				recheckInterval:  10 * time.Millisecond,
			}
			var result FrostResult
			senders := party[1:3]
			if tc.scheduled {
				req, genuine := golden7969(t)
				req.SignerPubKeys = append([]string{testNonPartyPubKey}, party...)
				coord.localPubKey = testNonPartyPubKey
				coord.adoptScheduledTxOut(req)
				result = goldenResult(t, genuine.SpentOutputRefs, genuine)
				senders = party[:3]
			} else {
				req, prepared, genuine := golden832c(t)
				coord.localPubKey = party[0]
				coord.preparedInputs = prepared
				coord.preparedRequest = req
				coord.verifyAdopted = func(req FrostKeySignRequest, prepared []FrostSpentOutputRef, resp FrostKeySignResponse) error {
					return verifyFrostTxForVault(req, prepared, resp, golden832cVaultAddress)
				}
				result = goldenResult(t, prepared, genuine)
			}
			for _, sender := range senders {
				enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{SenderPubKey: sender, Response: result})
			}
			start := time.Now()
			_, err := coord.WaitForResult(party, 4, timeout)
			elapsed := time.Since(start)
			if !errors.Is(err, errFrostResultTimeout) {
				t.Fatalf("got %v, want the result timeout", err)
			}
			if elapsed > timeout+500*time.Millisecond {
				t.Fatalf("WaitForResult took %s with a %s timeout", elapsed, timeout)
			}
			if len(sidecar.deadlines) == 0 {
				t.Fatal("sidecar was not called")
			}
			for _, deadline := range sidecar.deadlines {
				if deadline.IsZero() || deadline.After(start.Add(timeout+50*time.Millisecond)) {
					t.Fatalf("sidecar call deadline %v is not bounded by the wait ending at %v", deadline, start.Add(timeout))
				}
			}
		})
	}
}

// A stalled sidecar call for a below-quorum result must not keep WaitForResult
// from reading the results that complete the full quorum behind it.
func TestFrostWaitForResultReadsFullQuorumPastStalledSidecar(t *testing.T) {
	ensureBech32Prefix()
	party := []string{testPubKeys[0], testPubKeys[1], testPubKeys[2], testPubKeys[3]}
	sidecar := &hungSidecar{}
	msgChan := newFrostSignInbox()
	coord := &FrostSignCoordinator{
		sidecar:          sidecar,
		msgID:            "msg-id",
		expectedPlanHash: testExpectedPlanHash,
		inbox:            msgChan,
		timeout:          time.Second,
		recheckInterval:  20 * time.Millisecond,
	}
	req, prepared, genuine := golden832c(t)
	coord.localPubKey = party[0]
	coord.preparedInputs = prepared
	coord.preparedRequest = req
	coord.verifyAdopted = func(req FrostKeySignRequest, prepared []FrostSpentOutputRef, resp FrostKeySignResponse) error {
		return verifyFrostTxForVault(req, prepared, resp, golden832cVaultAddress)
	}
	result := goldenResult(t, prepared, genuine)
	// Two results reach the prepared-input quorum and start a daemon call that
	// stalls; the other two complete the full quorum.
	for _, sender := range party {
		enqueueResultMessage(t, msgChan, "msg-id", sender, sender, FrostResultPayload{SenderPubKey: sender, Response: result})
	}
	const timeout = 5 * time.Second
	start := time.Now()
	resp, err := coord.WaitForResult(party, 4, timeout)
	if err != nil {
		t.Fatalf("got %v, want the full-quorum result", err)
	}
	if resp.TxHash != result.TxHash {
		t.Fatalf("adopted %s, want %s", resp.TxHash, result.TxHash)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("WaitForResult took %s; a stalled sidecar call held the loop", elapsed)
	}
}
