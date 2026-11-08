package signer

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blang/semver"
	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type failureKeygenFunc func(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error)

func (f failureKeygenFunc) GenerateNewKey(height int64, members common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
	return f(height, members)
}

type failureReportBridge struct {
	thorclient.ThorchainBridge
	t              *testing.T
	height         atomic.Int64
	mimirs         map[string]int64
	mimirErrKey    string
	signer         cosmos.AccAddress
	reports        chan *ttypes.MsgTssPool
	mu             sync.Mutex
	attempts       []*ttypes.MsgTssPool
	failuresLeft   int
	heightSequence []int64
	heightErr      error
}

func (b *failureReportBridge) GetMimir(key string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if key == b.mimirErrKey {
		return 0, errors.New("temporary policy lookup failure")
	}
	return b.mimirs[key], nil
}

func (b *failureReportBridge) GetBlockHeight() (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.heightErr != nil {
		return 0, b.heightErr
	}
	if len(b.heightSequence) > 0 {
		height := b.heightSequence[0]
		b.heightSequence = b.heightSequence[1:]
		return height, nil
	}
	return b.height.Load(), nil
}

func (b *failureReportBridge) GetAsgards() (ttypes.Vaults, error) {
	return nil, nil
}

func (b *failureReportBridge) GetThorchainVersion() (semver.Version, error) {
	return semver.MustParse("3.21.0"), nil
}

func (b *failureReportBridge) GetKeygenStdTx(poolPubKey common.PubKey, signature, backup []byte, blame []ttypes.Blame, members common.PubKeys, keygenType ttypes.KeygenType, chains common.Chains, height, keygenTime int64, eddsa common.PubKey, eddsaBackup []byte, _ ...any) (sdktypes.Msg, error) {
	return ttypes.NewMsgTssPoolV3(members.Strings(), poolPubKey, signature, backup, keygenType, height, blame, chains.Strings(), b.signer, keygenTime, eddsa, eddsaBackup, nil, common.EmptyPubKey, 0)
}

func (b *failureReportBridge) Broadcast(msgs ...sdktypes.Msg) (common.TxID, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, msg := range msgs {
		pool, ok := msg.(*ttypes.MsgTssPool)
		if !ok {
			b.t.Errorf("unexpected message type: %T", msg)
			return common.BlankTxID, nil
		}
		b.attempts = append(b.attempts, pool)
		if err := pool.ValidateBasic(); err != nil {
			// Fail at the bad message, without spending a minute in transport
			// backoff. Errorf also works when a retry worker is broadcasting.
			b.t.Errorf("invalid keygen report: %v", err)
			return common.BlankTxID, nil
		}
		if b.failuresLeft > 0 {
			b.failuresLeft--
			return common.BlankTxID, errors.New("temporary broadcast failure")
		}
		b.reports <- pool
	}
	return common.TxID("keygen-failure-report"), nil
}

func newFailureReportSigner(t *testing.T) (*Signer, *failureReportBridge, ttypes.KeygenBlock) {
	t.Helper()
	members := common.PubKeys{ttypes.GetRandomPubKey(), ttypes.GetRandomPubKey()}
	addr, err := members[0].GetThorAddress()
	if err != nil {
		t.Fatal(err)
	}
	keygen, err := ttypes.NewKeygen(100, members.Strings(), ttypes.KeygenType_AsgardKeygen)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &failureReportBridge{
		t:      t,
		mimirs: map[string]int64{constants.ChurnRetryInterval.String(): 200, constants.KeygenRetryInterval.String(): 0},
		signer: addr, reports: make(chan *ttypes.MsgTssPool, 8),
	}
	bridge.height.Store(100)
	signer := &Signer{
		thorchainBridge: bridge, logger: log.Logger, stopChan: make(chan struct{}),
		pubkeyMgr: pubkeymanager.NewMockPoolAddressValidator(),
		errCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "keygen_failure_test_errors_total", Help: "Keygen reporting test errors.",
		}, []string{"reason", "height"}),
	}
	signer.cfg.Signer.KeygenTimeout = time.Second
	t.Cleanup(func() {
		signer.keygenWorkerMu.Lock()
		if !signer.isStopped() {
			close(signer.stopChan)
		}
		signer.keygenWorkerMu.Unlock()
		signer.keygenWorkerWG.Wait()
	})
	return signer, bridge, singleKeygenBlock(100, keygen)
}

func TestProcessKeygenFailureReports(t *testing.T) {
	for _, test := range []struct {
		name       string
		generic    bool
		partial    bool
		crossEntry bool
		err        error
	}{
		{name: "partial keys are discarded", partial: true, err: errors.New("EdDSA failed")},
		{name: "duplicate blamed node", err: errors.New("DKG failed")},
		{name: "duplicate across blame entries", crossEntry: true, err: errors.New("DKG failed")},
		{name: "blame without error"},
		{name: "failure without blamed nodes", generic: true, err: errors.New("DKG failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			signer, bridge, block := newFailureReportSigner(t)
			members := block.Keygens[0].GetMembers()
			makeBlame := func() []ttypes.Blame {
				blame := []ttypes.Blame{
					{FailReason: "keygen failed", Round: "keygen_1", BlameNodes: []ttypes.Node{
						{Pubkey: members[1].String()}, {Pubkey: members[1].String()}, {Pubkey: members[0].String()},
					}},
				}
				if test.crossEntry {
					blame = append(blame, ttypes.Blame{FailReason: "keygen failed", Round: "keygen_2", BlameNodes: []ttypes.Node{{Pubkey: members[1].String()}}})
				}
				return blame
			}
			blame, originalBlame := makeBlame(), makeBlame()
			if test.generic {
				blame = nil
				originalBlame = nil
			}
			signer.tssKeygen = failureKeygenFunc(func(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
				keys := common.EmptyPubKeySet
				if test.partial {
					keys.Secp256k1 = ttypes.GetRandomPubKey()
				}
				return keys, blame, test.err
			})
			signer.processKeygenBlock(block)
			select {
			case report := <-bridge.reports:
				if report.IsSuccess() || report.Height != block.Height || report.KeygenType != ttypes.KeygenType_AsgardKeygen {
					t.Fatalf("incorrect failure report: %+v", report)
				}
				if err := report.ValidateBasic(); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(report.PubKeys, block.Keygens[0].Members) {
					t.Fatal("failure report changed the keygen membership")
				}
				if !report.PoolPubKey.IsEmpty() || !report.PoolPubKeyEddsa.IsEmpty() || len(report.Secp256K1Signature) != 0 || len(report.KeysharesBackup) != 0 || len(report.KeysharesBackupEddsa) != 0 || len(report.KeysharesBackupFrost) != 0 || !report.PoolPubKeyFrost.IsEmpty() || report.ObservedMoneroHeight != 0 {
					t.Fatal("failure report included success-only material")
				}
				if test.generic && (report.Blame[0].FailReason != test.err.Error() || len(report.Blame[0].BlameNodes) != 0) {
					t.Fatal("generic failure lost its reason or invented a blamed node")
				}
				wantEntries := 1
				if test.crossEntry {
					wantEntries = 2
				}
				if !test.generic && (len(report.Blame) != wantEntries || len(report.Blame[0].BlameNodes) != 2 || report.Blame[0].BlameNodes[0].Pubkey != members[1].String() || report.Blame[0].BlameNodes[1].Pubkey != members[0].String()) {
					t.Fatal("blame was not deduplicated while preserving distinct nodes")
				}
				if test.crossEntry && len(report.Blame[1].BlameNodes) != 0 {
					t.Fatal("duplicate survived in a later blame entry")
				}
				if !reflect.DeepEqual(blame, originalBlame) {
					t.Fatal("failure reporting mutated the original blame")
				}
			default:
				t.Fatal("failed keygen was not reported")
			}
		})
	}
}

func TestProcessKeygenFailureSkipsNoopAndXMR(t *testing.T) {
	for _, keygenType := range []ttypes.KeygenType{ttypes.KeygenType_AsgardKeygen, ttypes.KeygenType_XMRMonovaultKeygen} {
		t.Run(keygenType.String(), func(t *testing.T) {
			signer, bridge, block := newFailureReportSigner(t)
			block.Keygens[0].Type = keygenType
			signer.tssKeygen = failureKeygenFunc(func(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
				if keygenType == ttypes.KeygenType_XMRMonovaultKeygen {
					t.Fatal("skipped XMR keygen called the ordinary TSS generator")
				}
				return common.EmptyPubKeySet, nil, nil
			})
			signer.processKeygenBlock(block)
			if len(bridge.reports) != 0 || len(bridge.attempts) != 0 {
				t.Fatal("no-op or skipped XMR keygen produced a failure report")
			}
		})
	}
}

func TestProcessKeygenFailureSkipsReportOnShutdown(t *testing.T) {
	signer, bridge, block := newFailureReportSigner(t)
	signer.stopChan = make(chan struct{})
	signer.tssKeygen = failureKeygenFunc(func(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
		close(signer.stopChan) // Shutdown starts while the ceremony is running.
		return common.EmptyPubKeySet, nil, errors.New("DKG interrupted")
	})
	signer.processKeygenBlock(block)
	if len(bridge.reports) != 0 || len(bridge.attempts) != 0 {
		t.Fatal("shutdown produced a terminal failure report")
	}
}

func TestBuildKeygenBackupsSkipsEmptyMaterial(t *testing.T) {
	t.Setenv("SIGNER_SEED_PHRASE_FILE", "")
	t.Setenv("SIGNER_SEED_PHRASE", "")
	var logs bytes.Buffer
	signer := &Signer{logger: zerolog.New(&logs)}
	signer.cfg.Signer.BackupKeyshares = true
	backups := signer.buildKeygenBackups("", "", "")
	if len(backups.keyshares)+len(backups.keysharesEddsa)+len(backups.keysharesFrost) != 0 || logs.Len() != 0 {
		t.Fatal("empty key material attempted a backup or logged a seed error")
	}
}

// Exercise the real worker across an outage. A later attempt replaces the stored
// failure; a result that has passed THORNode's acceptance deadline stays silent.
func TestProcessKeygenFailurePolicyUnavailable(t *testing.T) {
	for _, expires := range []bool{false, true} {
		name := "recovers"
		if expires {
			name = "expires"
		}
		t.Run(name, func(t *testing.T) {
			signer, bridge, block := newFailureReportSigner(t)
			bridge.mimirs[constants.KeygenRetryInterval.String()] = 50
			bridge.mimirErrKey = constants.KeygenRetryInterval.String()
			var calls atomic.Int64
			firstNode, lastNode := block.Keygens[0].Members[0], block.Keygens[0].Members[1]
			signer.tssKeygen = failureKeygenFunc(func(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
				node := firstNode
				if calls.Add(1) > 1 {
					node = lastNode
					bridge.height.Store(250) // No successor fits after this attempt.
				}
				return common.EmptyPubKeySet, []ttypes.Blame{{FailReason: "DKG failed", BlameNodes: []ttypes.Node{{Pubkey: node}}}}, errors.New("DKG failed")
			})
			signer.processKeygenBlock(block)
			bridge.mu.Lock()
			if len(bridge.attempts) != 0 {
				t.Error("unknown policy produced a report")
			}
			bridge.mimirErrKey = ""
			if expires {
				bridge.height.Store(301)
			} else {
				// Recovery prepares at 150; the subsequent fire-time lookup sees 200.
				bridge.heightSequence = []int64{150, 200}
				bridge.height.Store(200)
			}
			bridge.mu.Unlock()
			done := make(chan struct{})
			go func() { signer.keygenWorkerWG.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * constants.ThorchainBlockTime):
				t.Fatal("retry worker did not finish")
			}
			if expires {
				if calls.Load() != 1 || len(bridge.attempts) != 0 {
					t.Fatal("expired policy recovery executed or reported stale failure")
				}
				return
			}
			if calls.Load() != 2 || len(bridge.attempts) != 1 {
				t.Fatalf("calls=%d reports=%d", calls.Load(), len(bridge.attempts))
			}
			report := bridge.attempts[0]
			if report.IsSuccess() || report.Height != block.Height || len(report.Blame) != 1 || len(report.Blame[0].BlameNodes) != 1 || report.Blame[0].BlameNodes[0].Pubkey != lastNode {
				t.Fatal("report did not preserve the final attempt's blame and original height")
			}
		})
	}
}

func TestProcessKeygenFailureBroadcastRetry(t *testing.T) {
	signer, bridge, block := newFailureReportSigner(t)
	bridge.failuresLeft = 1
	signer.tssKeygen = failureKeygenFunc(func(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
		return common.EmptyPubKeySet, nil, errors.New("DKG failed")
	})
	signer.processKeygenBlock(block)
	if len(bridge.reports) != 1 || len(bridge.attempts) != 2 {
		t.Fatalf("expected one report and one broadcast retry, got %d reports and %d attempts", len(bridge.reports), len(bridge.attempts))
	}
	if bridge.attempts[0] != bridge.attempts[1] || bridge.attempts[0].ID != bridge.attempts[1].ID {
		t.Fatal("broadcast retry changed the report or identity")
	}
}

func TestProcessKeygenFailureReportsAfterPolicyRecoveryWithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name     string
		interval int64
	}{{"disabled retries", 0}, {"no successor fits", 50}} {
		t.Run(test.name, func(t *testing.T) {
			signer, bridge, block := newFailureReportSigner(t)
			bridge.mimirs[constants.KeygenRetryInterval.String()] = test.interval
			bridge.mimirErrKey = constants.KeygenRetryInterval.String()
			blame := []ttypes.Blame{{FailReason: "last attempt failed", BlameNodes: []ttypes.Node{{
				Pubkey: block.Keygens[0].Members[0], BlameData: []byte{1}, BlameSignature: []byte{2},
			}}}}
			signer.tssKeygen = failureKeygenFunc(func(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
				return common.EmptyPubKeySet, blame, errors.New("last attempt failed")
			})
			signer.processKeygenBlock(block)
			stored, ok := signer.keygenRetries.Load(retryID(block, block.Keygens[0]))
			if !ok {
				t.Fatal("outage did not retain the failure")
			}
			retained, ok := stored.(*keygenRetry)
			if !ok || retained.failure == nil {
				t.Fatal("retry did not retain an ordinary failure")
			}
			elapsed := retained.failure.keygenTime
			// A later TSS result may reuse its slices. The retained result must
			// preserve the failed attempt's evidence and duration across the wait.
			blame[0].FailReason = "changed"
			blame[0].BlameNodes[0].BlameData[0] = 9
			blame[0].BlameNodes[0].BlameSignature[0] = 9
			bridge.mu.Lock()
			if len(bridge.attempts) != 0 {
				t.Error("unknown retry policy produced a report")
			}
			bridge.height.Store(250) // Too late to retry, but the report is valid through 300.
			bridge.mimirErrKey = ""
			bridge.mu.Unlock()
			done := make(chan struct{})
			go func() { signer.keygenWorkerWG.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * constants.ThorchainBlockTime):
				t.Fatal("retry worker did not finish")
			}
			if len(bridge.attempts) != 1 {
				t.Fatalf("reports=%d want=1", len(bridge.attempts))
			}
			report := bridge.attempts[0]
			if report.Height != block.Height || report.KeygenTime != elapsed || len(report.Blame) != 1 || report.Blame[0].FailReason != "last attempt failed" {
				t.Fatal("lost the original failure")
			}
			if len(report.Blame[0].BlameNodes) != 1 || !bytes.Equal(report.Blame[0].BlameNodes[0].BlameData, []byte{1}) || !bytes.Equal(report.Blame[0].BlameNodes[0].BlameSignature, []byte{2}) {
				t.Fatal("retained failure evidence changed")
			}
		})
	}
}

func TestKeygenRetryRetainedFailure(t *testing.T) {
	for _, test := range []struct {
		name     string
		height   int64
		validity int64
		interval int64
		halt     int64
		reports  int
	}{
		{"final join window missed", 253, 200, 50, 0, 1},
		{"retry disabled", 200, 200, 0, 0, 1},
		{"retry interval overflow", 200, 200, math.MaxInt64, 0, 1},
		{"last block with inclusion time", 299, 200, 50, 0, 1},
		{"no inclusion time left", 300, 200, 50, 0, 0},
		{"expired", 301, 200, 50, 0, 0},
		{"zero validity leaves no inclusion block", 100, 0, 0, 0, 0},
		{"reporting halt unset", 200, 200, 0, -1, 1},
		{"reporting halted", 200, 200, 0, 1, 0},
		{"positive halt is not an activation height", 200, 200, 0, math.MaxInt64, 0},
	} {
		for _, prepare := range []bool{false, true} {
			path := "fire"
			if prepare {
				path = "prepare"
			}
			t.Run(test.name+"/"+path, func(t *testing.T) {
				signer, bridge, block := newFailureReportSigner(t)
				bridge.height.Store(test.height)
				// THORNode's result window is ChurnRetryInterval-1 blocks.
				bridge.mimirs[constants.ChurnRetryInterval.String()] = test.validity + 1
				bridge.mimirs[constants.KeygenRetryInterval.String()] = test.interval
				bridge.mimirs[constants.MimirKeyHaltKeygenFailureReporting] = test.halt
				retry := registerRetryTestWork(signer, block, 150)
				retry.failure = &keygenFailure{blame: []ttypes.Blame{{FailReason: "last attempt failed"}}, keygenTime: 1234}
				signer.tssKeygen = failureKeygenFunc(func(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
					t.Error("terminal retry started a ceremony")
					return common.EmptyPubKeySet, nil, nil
				})
				for i := 0; i < 2; i++ {
					if prepare {
						if done, err := signer.prepareKeygenRetry(retry); !done || err != nil {
							t.Fatalf("done=%t err=%v", done, err)
						}
					} else if !signer.tryKeygenRetry(retry) {
						t.Fatal("worker did not finish")
					}
				}
				if len(bridge.attempts) != test.reports {
					t.Fatalf("reports=%d want=%d", len(bridge.attempts), test.reports)
				}
				if test.reports > 0 && bridge.attempts[0].KeygenTime != 1234 {
					t.Fatal("waiting changed the ceremony duration")
				}
				if _, ok := signer.keygenRetries.Load(retryID(block, retry.keygen)); ok {
					t.Fatal("terminal worker retained its token")
				}
			})
		}
	}
}

func TestKeygenRetryReportsBeforeTargetWhenValidityShrinks(t *testing.T) {
	signer, bridge, block := newFailureReportSigner(t)
	bridge.mimirs[constants.ChurnRetryInterval.String()] = 202
	bridge.mimirs[constants.KeygenRetryInterval.String()] = 100
	retry := registerRetryTestWork(signer, block, 0)
	retry.failure = &keygenFailure{blame: []ttypes.Blame{{FailReason: "last attempt failed"}}}
	signer.tssKeygen = failureKeygenFunc(func(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
		t.Error("terminal retry started a ceremony")
		return common.EmptyPubKeySet, nil, nil
	})
	if done, err := signer.prepareKeygenRetry(retry); err != nil || done || retry.targetHeight != 200 {
		t.Fatalf("prepare: done=%t err=%v target=%d", done, err, retry.targetHeight)
	}

	bridge.height.Store(150)
	if signer.tryKeygenRetry(retry) || len(bridge.attempts) != 0 {
		t.Fatal("worker did not wait for its scheduled retry")
	}
	// The failure is now valid only through 180, before the old target of 200.
	bridge.mimirs[constants.ChurnRetryInterval.String()] = 80
	if !signer.tryKeygenRetry(retry) || len(bridge.attempts) != 1 {
		t.Fatal("worker did not report the retained failure before the shortened window expired")
	}
	if _, ok := signer.keygenRetries.Load(retryID(block, retry.keygen)); ok {
		t.Fatal("terminal worker retained its token")
	}
	// Repeated polling, including at the obsolete target, must not report twice.
	for _, height := range []int64{150, 200} {
		bridge.height.Store(height)
		if !signer.tryKeygenRetry(retry) || len(bridge.attempts) != 1 {
			t.Fatal("retired worker changed its report count")
		}
	}
}

func TestKeygenRetryLastRoundIncludesLateMembers(t *testing.T) {
	for _, test := range []struct {
		name             string
		validity         int64
		calls            int
		reason           string
		completionHeight int64
		reports          int
	}{
		{"extra completion time", 202, 1, "latest attempt", 0, 1},
		{"final exact-fit round", 200, 1, "latest attempt", 0, 1},
		{"completes with one inclusion block left", 200, 1, "latest attempt", 299, 1},
		{"completes at the acceptance deadline", 200, 1, "latest attempt", 300, 0},
		{"less than one interval", 199, 0, "retained attempt", 0, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Different members can first observe different heights for one round.
			for _, height := range []int64{200, 201, 202} {
				signer, bridge, block := newFailureReportSigner(t)
				// THORNode's result window is ChurnRetryInterval-1 blocks.
				bridge.mimirs[constants.ChurnRetryInterval.String()] = test.validity + 1
				bridge.mimirs[constants.KeygenRetryInterval.String()] = 100
				bridge.height.Store(height)
				retry := registerRetryTestWork(signer, block, 200)
				retry.failure = &keygenFailure{blame: []ttypes.Blame{{FailReason: "retained attempt"}}}
				calls := 0
				signer.tssKeygen = failureKeygenFunc(func(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
					calls++
					if test.completionHeight > 0 {
						bridge.height.Store(test.completionHeight)
					}
					return common.EmptyPubKeySet, []ttypes.Blame{{FailReason: "latest attempt"}}, errors.New("DKG failed")
				})
				if !signer.tryKeygenRetry(retry) || calls != test.calls {
					t.Fatalf("height=%d calls=%d want=%d", height, calls, test.calls)
				}
				if len(bridge.attempts) != test.reports {
					t.Fatalf("height=%d reports=%d want=%d", height, len(bridge.attempts), test.reports)
				}
				if test.reports > 0 && (len(bridge.attempts[0].Blame) != 1 || bridge.attempts[0].Blame[0].FailReason != test.reason) {
					t.Fatalf("height=%d did not report the expected attempt", height)
				}
				if _, ok := signer.keygenRetries.Load(retryID(block, retry.keygen)); ok {
					t.Fatalf("height=%d left a retry behind", height)
				}
			}
		})
	}
}

func TestProcessKeygenFailureReportingHaltRecovery(t *testing.T) {
	signer, bridge, block := newFailureReportSigner(t)
	bridge.height.Store(250)
	bridge.mimirErrKey = constants.MimirKeyHaltKeygenFailureReporting
	signer.tssKeygen = failureKeygenFunc(func(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
		return common.EmptyPubKeySet, nil, errors.New("last attempt failed")
	})
	signer.processKeygenBlock(block)
	id := retryID(block, block.Keygens[0])
	if _, ok := signer.keygenRetries.Load(id); !ok {
		t.Fatal("halt lookup outage lost the pending failure")
	}
	bridge.mu.Lock()
	if len(bridge.attempts) != 0 {
		t.Error("unknown halt policy allowed reporting")
	}
	bridge.mimirs[constants.MimirKeyHaltKeygenFailureReporting] = 1
	bridge.mimirErrKey = ""
	bridge.mu.Unlock()
	done := make(chan struct{})
	go func() { signer.keygenWorkerWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * constants.ThorchainBlockTime):
		t.Fatal("worker did not retire after reporting was halted")
	}
	if len(bridge.attempts) != 0 {
		t.Fatal("halted report was submitted")
	}
	if _, ok := signer.keygenRetries.Load(id); ok {
		t.Fatal("halted worker retained its token")
	}
}

func TestKeygenRetryFailureReportGuards(t *testing.T) {
	for _, test := range []string{"shutdown", "no failure", "XMR", "stale worker", "disabled churn", "shortened validity"} {
		t.Run(test, func(t *testing.T) {
			signer, bridge, block := newFailureReportSigner(t)
			bridge.mimirErrKey = constants.MimirKeyHaltKeygenFailureReporting
			bridge.height.Store(250)
			retry := registerRetryTestWork(signer, block, 150)
			retry.failure = &keygenFailure{blame: []ttypes.Blame{{FailReason: "last attempt failed"}}}
			switch test {
			case "shutdown":
				close(signer.stopChan)
			case "no failure":
				retry.failure = nil
			case "XMR":
				signer.keygenRetries.Delete(retryID(block, retry.keygen))
				retry.keygen.Type = ttypes.KeygenType_XMRMonovaultKeygen
				signer.keygenRetries.Store(retryID(block, retry.keygen), retry)
			case "stale worker":
				signer.keygenRetries.Store(retryID(block, retry.keygen), &keygenRetry{block: block, keygen: retry.keygen})
			case "disabled churn":
				bridge.mimirs[constants.ChurnRetryInterval.String()] = 0
			case "shortened validity":
				bridge.mimirs[constants.ChurnRetryInterval.String()] = 100
			}
			if !signer.tryKeygenRetry(retry) || len(bridge.attempts) != 0 {
				t.Fatal("ineligible worker reported a failure")
			}
			if test == "stale worker" {
				if current, ok := signer.keygenRetries.Load(retryID(block, retry.keygen)); !ok || current == retry {
					t.Fatal("stale worker removed its successor")
				}
			}
		})
	}
}

func TestKeygenRetryFailureWaitsForKnownPolicyAndHeight(t *testing.T) {
	for _, lookup := range []string{constants.ChurnRetryInterval.String(), constants.KeygenRetryInterval.String(), constants.MimirKeyHaltKeygenFailureReporting, "height"} {
		for _, prepare := range []bool{false, true} {
			path := "fire"
			if prepare {
				path = "prepare"
			}
			t.Run(lookup+"/"+path, func(t *testing.T) {
				signer, bridge, block := newFailureReportSigner(t)
				bridge.height.Store(250)
				retry := registerRetryTestWork(signer, block, 150)
				retry.failure = &keygenFailure{blame: []ttypes.Blame{{FailReason: "last attempt failed"}}}
				bridge.mimirErrKey = lookup
				if lookup == "height" {
					bridge.heightErr = errors.New("height unavailable")
				}
				step := func() bool {
					if prepare {
						done, _ := signer.prepareKeygenRetry(retry)
						return done
					}
					return signer.tryKeygenRetry(retry)
				}
				if step() || len(bridge.attempts) != 0 {
					t.Fatal("unknown policy or height produced a report or dropped work")
				}
				if current, ok := signer.keygenRetries.Load(retryID(block, retry.keygen)); !ok || current != retry {
					t.Fatal("lookup outage lost the retained failure")
				}
				bridge.mimirErrKey = ""
				bridge.heightErr = nil
				if !step() || len(bridge.attempts) != 1 {
					t.Fatal("policy recovery did not report the retained failure")
				}
			})
		}
	}
}

// Bypass only the current network-wide brake to keep the deferred custom-result
// contract covered when S00 later enables XMR. Custom blame must never reach TSS penalties.
func TestProcessKeygenFailureDoesNotReportCustomBlame(t *testing.T) {
	signer, bridge, block := newFailureReportSigner(t)
	block.Keygens[0].Type = ttypes.KeygenType_XMRMonovaultKeygen
	custom := &MockCustomKeygenChainClient{
		MockChainClient: &MockChainClient{},
		retErr:          errors.New("FROST failed"),
		retBlame:        []ttypes.Blame{{FailReason: "FROST failed", BlameNodes: []ttypes.Node{{Pubkey: block.Keygens[0].Members[0]}}}},
	}
	signer.chains = map[common.Chain]chainclients.ChainClient{common.XMRChain: custom}
	signer.runKeygenRequest(block, block.Keygens[0])
	if custom.callCount != 1 || len(bridge.attempts) != 0 {
		t.Fatal("custom blame reached on-chain reporting or hook was not exercised")
	}
}

// Stop waits for in-flight ceremonies before closing TSS. A successful result
// must still deliver its vote and backups, including through transport retries.
func TestKeygenSuccessBroadcastDuringShutdown(t *testing.T) {
	signer, bridge, block := newFailureReportSigner(t)
	// Reporting policy must not block successful votes or keyshare backups.
	bridge.mimirErrKey = constants.MimirKeyHaltKeygenFailureReporting
	backup := make([]byte, 2048)
	for i := range backup {
		backup[i] = byte(i)
	}
	keys := common.NewPubKeySet(ttypes.GetRandomPubKey(), ttypes.GetRandomPubKey())
	msg, err := bridge.GetKeygenStdTx(keys.Secp256k1, nil, backup, nil, block.Keygens[0].GetMembers(), ttypes.KeygenType_AsgardKeygen, common.Chains{common.THORChain}, block.Height, 1234, keys.Ed25519, backup)
	if err != nil {
		t.Fatal(err)
	}
	close(signer.stopChan)
	bridge.failuresLeft = 1
	if _, err = signer.broadcastKeygenMsgs(block.Height, msg); err != nil {
		t.Fatal(err)
	}
	if len(bridge.attempts) != 2 || len(bridge.reports) != 1 || bridge.attempts[0] != bridge.attempts[1] {
		t.Fatal("shutdown dropped or rebuilt the successful result")
	}
	report := <-bridge.reports
	if !report.IsSuccess() || report.PoolPubKey != keys.Secp256k1 || report.PoolPubKeyEddsa != keys.Ed25519 || !bytes.Equal(report.KeysharesBackup, backup) || !bytes.Equal(report.KeysharesBackupEddsa, backup) {
		t.Fatal("shutdown lost successful keygen material")
	}
}
