package signer

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// retryTestBridge isolates retry policy from XMR activation and synchronizes
// reads with worker shutdown. No S00 gates, admission slots or fixtures are used.
type retryTestBridge struct {
	fakeBridge
	mu         sync.Mutex
	height     int64
	mimirs     map[string]int64
	failures   map[string]error
	reads      map[string]int
	broadcasts int
}

func (b *retryTestBridge) GetMimir(key string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reads[key]++
	return b.mimirs[key], b.failures[key]
}

func (b *retryTestBridge) GetBlockHeight() (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.height, b.failures["height"]
}

func (b *retryTestBridge) GetAsgards() (ttypes.Vaults, error) { return nil, nil }

// A terminal retry now constructs a failure report through the real message contract.
func (b *retryTestBridge) GetKeygenStdTx(poolPubKey common.PubKey, signature, backup []byte, blame []ttypes.Blame, members common.PubKeys, keygenType ttypes.KeygenType, chains common.Chains, height, keygenTime int64, eddsa common.PubKey, eddsaBackup []byte, _ ...any) (sdktypes.Msg, error) {
	signer, err := members[0].GetThorAddress()
	if err != nil {
		return nil, err
	}
	return ttypes.NewMsgTssPoolV3(members.Strings(), poolPubKey, signature, backup, keygenType, height, blame, chains.Strings(), signer, keygenTime, eddsa, eddsaBackup, nil, common.EmptyPubKey, 0)
}

func (b *retryTestBridge) Broadcast(...sdktypes.Msg) (common.TxID, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.broadcasts++
	return common.TxID("test-keygen-result"), nil
}

func (b *retryTestBridge) setHeight(height int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.height = height
}

func (b *retryTestBridge) setMimir(key string, value int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mimirs[key] = value
}

func (b *retryTestBridge) fail(key string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures[key] = err
}

// recordingKeygen counts actual ordinary-TSS calls, not indirect policy reads.
// Terminal failures also exercise the bridge message constructor above.
type recordingKeygen struct {
	mu      sync.Mutex
	heights []int64
	members []common.PubKeys
}

func (g *recordingKeygen) GenerateNewKey(height int64, members common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.heights = append(g.heights, height)
	g.members = append(g.members, append(common.PubKeys(nil), members...))
	return common.EmptyPubKeySet, nil, errors.New("test DKG failure")
}

func (g *recordingKeygen) calls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.heights)
}

func newRetryTestSigner(t *testing.T) (*Signer, *retryTestBridge, *recordingKeygen, ttypes.KeygenBlock) {
	t.Helper()
	bridge := &retryTestBridge{
		height:   2800,
		mimirs:   map[string]int64{constants.ChurnRetryInterval.String(): 1000, constants.KeygenRetryInterval.String(): 100},
		failures: make(map[string]error), reads: make(map[string]int),
	}
	generator := &recordingKeygen{}
	signer := &Signer{
		stopChan: make(chan struct{}), logger: log.Logger,
		thorchainBridge: bridge, tssKeygen: generator,
		errCounter: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "retry_test_errors_total", Help: "Test failures"}, []string{"reason", "height"}),
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
	keygen, err := ttypes.NewKeygen(2700, []string{ttypes.GetRandomPubKey().String(), ttypes.GetRandomPubKey().String()}, ttypes.KeygenType_AsgardKeygen)
	if err != nil {
		t.Fatal(err)
	}
	return signer, bridge, generator, singleKeygenBlock(2700, keygen)
}

func registerRetryTestWork(signer *Signer, block ttypes.KeygenBlock, target int64) *keygenRetry {
	retry := &keygenRetry{block: block, keygen: block.Keygens[0], targetHeight: target}
	signer.keygenRetries.Store(retryID(block, retry.keygen), retry)
	return retry
}

func TestCommonRetryRetainsLookupFailures(t *testing.T) {
	for _, lookup := range []string{constants.ChurnRetryInterval.String(), constants.KeygenRetryInterval.String(), "height"} {
		for _, outcome := range []string{"resume", "disabled", "expired", "shutdown"} {
			t.Run(lookup+"/"+outcome, func(t *testing.T) {
				signer, bridge, generator, block := newRetryTestSigner(t)
				retry := registerRetryTestWork(signer, block, 2800)
				bridge.fail(lookup, errors.New("temporary API outage"))
				for i := 0; i < 3; i++ {
					if signer.tryKeygenRetry(retry) || generator.calls() != 0 {
						t.Fatal("unknown policy discarded or executed work")
					}
				}
				bridge.fail(lookup, nil)
				bridge.setHeight(3500) // Rejoin a later shared retry boundary.
				want := 0
				switch outcome {
				case "resume":
					want = 1
				case "disabled":
					bridge.setMimir(constants.KeygenRetryInterval.String(), 0)
				case "expired":
					bridge.setHeight(3700)
				case "shutdown":
					close(signer.stopChan)
				}
				if !signer.tryKeygenRetry(retry) || generator.calls() != want {
					t.Fatalf("calls=%d want=%d", generator.calls(), want)
				}
				if !signer.tryKeygenRetry(retry) || generator.calls() != want {
					t.Fatal("consumed token executed twice")
				}
			})
		}
	}
}

func TestCommonRetryReportingHaltDoesNotDisableRetries(t *testing.T) {
	signer, bridge, generator, block := newRetryTestSigner(t)
	bridge.setMimir(constants.MimirKeyHaltKeygenFailureReporting, 1)
	bridge.fail(constants.MimirKeyHaltKeygenFailureReporting, errors.New("halt lookup unavailable"))
	retry := registerRetryTestWork(signer, block, 2800)
	if !signer.tryKeygenRetry(retry) || generator.calls() != 1 {
		t.Fatal("reporting policy blocked the scheduled keygen")
	}
	bridge.mu.Lock()
	haltReads := bridge.reads[constants.MimirKeyHaltKeygenFailureReporting]
	bridge.mu.Unlock()
	if haltReads != 0 {
		t.Fatal("retry consulted reporting policy before it was terminal")
	}
	current, ok := signer.keygenRetries.Load(retryID(block, retry.keygen))
	if !ok || current == retry {
		t.Fatal("failed attempt did not schedule its successor")
	}
	bridge.fail(constants.MimirKeyHaltKeygenFailureReporting, nil)
	bridge.setHeight(3603) // The final shared round's join window has closed.
	successor, ok := current.(*keygenRetry)
	if !ok {
		t.Fatalf("unexpected retry token: %T", current)
	}
	if !signer.tryKeygenRetry(successor) {
		t.Fatal("terminal successor did not retire")
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if bridge.broadcasts != 0 {
		t.Fatal("halted terminal failure was reported")
	}
}

func TestCommonRetryInitialFailureDeduplicatesAndRendezvous(t *testing.T) {
	for _, lookup := range []string{constants.ChurnRetryInterval.String(), constants.KeygenRetryInterval.String(), "height"} {
		t.Run(lookup, func(t *testing.T) {
			signer, bridge, generator, block := newRetryTestSigner(t)
			bridge.fail(lookup, errors.New("temporary outage"))
			var callers sync.WaitGroup
			for i := 0; i < 10; i++ {
				callers.Add(1)
				go func() {
					defer callers.Done()
					if !signer.scheduleKeygenRetry(block, block.Keygens[0], nil, nil) {
						t.Error("lost retry intent")
					}
				}()
			}
			callers.Wait()
			bridge.mu.Lock()
			reads := bridge.reads[constants.ChurnRetryInterval.String()]
			bridge.mu.Unlock()
			if reads != 1 || generator.calls() != 0 {
				t.Fatal("duplicate worker or premature execution")
			}
			// Exercise the worker's preparation step separately from its owned
			// timer state; recovery rejoins the ceremony's shared retry grid.
			bridge.fail(lookup, nil)
			bridge.setHeight(2817)
			retained := &keygenRetry{block: block, keygen: block.Keygens[0]}
			if done, err := signer.prepareKeygenRetry(retained); done || err != nil || retained.targetHeight != 2900 {
				t.Fatalf("preparation: done=%t err=%v target=%d", done, err, retained.targetHeight)
			}
			close(signer.stopChan)
			signer.keygenWorkerWG.Wait()
			if _, ok := signer.keygenRetries.Load(retryID(block, block.Keygens[0])); ok {
				t.Fatal("shutdown leaked token")
			}
		})
	}
}

func TestCommonRetryLaggingHeightAndSuccessorOwnership(t *testing.T) {
	signer, bridge, generator, block := newRetryTestSigner(t)
	retry := registerRetryTestWork(signer, block, 0)
	bridge.setHeight(2699)
	if done, err := signer.prepareKeygenRetry(retry); done || err == nil {
		t.Fatal("lagging height dropped work")
	}
	bridge.setHeight(2700)
	if done, err := signer.prepareKeygenRetry(retry); done || err != nil || retry.targetHeight != 2800 {
		t.Fatal("wrong retry boundary")
	}
	if signer.tryKeygenRetry(retry) {
		t.Fatal("fired before target")
	}
	bridge.setHeight(2800)
	if !signer.tryKeygenRetry(retry) || generator.calls() != 1 {
		t.Fatal("recovered retry did not execute once")
	}
	id := retryID(block, block.Keygens[0])
	successor, ok := signer.keygenRetries.Load(id)
	if !ok || successor == retry {
		t.Fatal("failed attempt did not register successor")
	}
	if signer.keygenRetries.CompareAndDelete(id, retry) {
		t.Fatal("old cleanup removed successor")
	}
	if !signer.tryKeygenRetry(retry) || generator.calls() != 1 {
		t.Fatal("stale token executed")
	}
	if current, _ := signer.keygenRetries.Load(id); current != successor {
		t.Fatal("stale worker changed successor")
	}
}

func TestCommonRetryDispatchesOnlyFailedAsgard(t *testing.T) {
	signer, bridge, generator, block := newRetryTestSigner(t)
	companion, err := ttypes.NewKeygen(2700, []string{ttypes.GetRandomPubKey().String()}, ttypes.KeygenType_AsgardKeygen)
	if err != nil {
		t.Fatal(err)
	}
	block.Keygens = append(block.Keygens, companion)
	retry := registerRetryTestWork(signer, block, 2800)
	bridge.setHeight(3500)
	if !signer.tryKeygenRetry(retry) || generator.calls() != 1 {
		t.Fatal("retry replayed successful companion")
	}
	if generator.heights[0] != block.Height || !generator.members[0].Equals(block.Keygens[0].GetMembers()) {
		t.Fatal("wrong ceremony executed")
	}
	if bridge.reads["EnableMonovaultKeygen"] != 0 || bridge.reads["HaltMonovaultKeygen"] != 0 {
		t.Fatal("Asgard consulted XMR gate")
	}
}

// S00 deliberately replaces Common's unconditional brake with an explicit
// opt-in. Every execution entry must still honor a disabled XMR gate.
func TestCommonRetryHonorsXMRGate(t *testing.T) {
	for _, entry := range []string{"delivery", "request", "retry"} {
		t.Run(entry, func(t *testing.T) {
			signer, bridge, generator, block := newRetryTestSigner(t)
			custom := &MockCustomKeygenChainClient{MockChainClient: &MockChainClient{}}
			signer.chains = map[common.Chain]chainclients.ChainClient{common.XMRChain: custom}
			bridge.setMimir("EnableMonovaultKeygen", 0)
			bridge.setMimir("HaltMonovaultKeygen", 0)
			block.Keygens[0].Type = ttypes.KeygenType_XMRMonovaultKeygen
			switch entry {
			case "delivery":
				signer.processKeygenBlock(block)
			case "request":
				signer.processKeygenRequest(block, block.Keygens[0])
			case "retry":
				if !signer.tryKeygenRetry(registerRetryTestWork(signer, block, 2800)) {
					t.Fatal("retry did not finish")
				}
			}
			if custom.callCount != 0 || generator.calls() != 0 || bridge.broadcasts != 0 {
				t.Fatal("XMR brake bypassed")
			}
			if _, ok := signer.keygenRetries.Load(retryID(block, block.Keygens[0])); ok {
				t.Fatal("skipped XMR scheduled another retry")
			}
		})
	}
}

func TestCommonRetryWindowAndScheduleBoundaries(t *testing.T) {
	asgard := ttypes.Keygen{Type: ttypes.KeygenType_AsgardKeygen}
	xmr := ttypes.Keygen{Type: ttypes.KeygenType_XMRMonovaultKeygen}
	minimum := int64((6*time.Minute + constants.ThorchainBlockTime - 1) / constants.ThorchainBlockTime)
	for _, test := range []struct {
		value   int64
		keygens []ttypes.Keygen
		want    int64
	}{
		// Validity ends one block before the retry tick (mirror of THORNode's
		// keygenResultValidityBlocks returning N-1). churn==0 keeps the disabled
		// sentinel (returns 0, no subtraction).
		{0, []ttypes.Keygen{asgard}, 0},
		{0, []ttypes.Keygen{xmr}, 0},
		{-1, []ttypes.Keygen{asgard}, constants.NewConstantValue().GetInt64Value(constants.ChurnRetryInterval) - 1},
		{1, []ttypes.Keygen{asgard}, 0},
		{1, []ttypes.Keygen{xmr}, minimum - 1},
		{1, []ttypes.Keygen{asgard, xmr}, minimum - 1},
		{1000, []ttypes.Keygen{xmr}, 999},
	} {
		if got := keygenRetryValidityBlocks(test.value, ttypes.KeygenBlock{Keygens: test.keygens}); got != test.want {
			t.Fatalf("validity=%d want=%d", got, test.want)
		}
	}
	for _, test := range []struct {
		height int64
		done   bool
		target int64
		// Scheduling keeps one interval of completion headroom inside the window.
		// With validity churn-1 (999) and interval 100, the last height that can
		// still schedule is 3499 (target 3500); 3500 itself no longer fits.
	}{{2700, false, 2800}, {2817, false, 2900}, {3400, false, 3500}, {3499, false, 3500}, {3500, true, 0}, {3600, true, 0}, {3701, true, 0}, {math.MaxInt64, true, 0}} {
		signer, bridge, _, block := newRetryTestSigner(t)
		bridge.setHeight(test.height)
		retry := &keygenRetry{block: block, keygen: block.Keygens[0]}
		if done, err := signer.prepareKeygenRetry(retry); err != nil || done != test.done || retry.targetHeight != test.target {
			t.Fatalf("height=%d done=%t target=%d err=%v", test.height, done, retry.targetHeight, err)
		}
	}
	if !keygenHeightInWindow(2700, 2700, 0) || keygenHeightInWindow(2701, 2700, 0) {
		t.Fatal("zero validity must preserve only the initial block")
	}
}

func TestCommonRetryDelayedFiring(t *testing.T) {
	for _, test := range []struct {
		name     string
		height   int64
		interval int64
		target   int64
		calls    int
	}{
		{"on time", 2800, 100, 0, 1},
		{"cached poll skips target", 2801, 100, 0, 1},
		{"last tolerated block", 2802, 100, 0, 1},
		{"too late to join", 2803, 100, 2900, 0},
		{"missed boundary", 2817, 100, 2900, 0},
		{"next round after outage", 2901, 100, 0, 1},
		{"last possible boundary ahead", 3499, 100, 3500, 0},
		{"final boundary ahead", 3599, 100, 3600, 0},
		{"final round on time", 3600, 100, 0, 1},
		{"final round one block late", 3601, 100, 0, 1},
		{"final round two blocks late", 3602, 100, 0, 1},
		{"final join window missed", 3603, 100, 0, 0},
		{"acceptance boundary", 3700, 100, 0, 0},
		{"interval changed", 2800, 150, 2850, 0},
		{"late on changed grid", 2851, 150, 0, 1},
		{"larger interval needs more headroom", 3500, 300, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			signer, bridge, generator, block := newRetryTestSigner(t)
			retry := registerRetryTestWork(signer, block, 2800)
			// Keep a 1000-block result window (ChurnRetryInterval-1).
			bridge.setMimir(constants.ChurnRetryInterval.String(), 1001)
			bridge.setHeight(test.height)
			bridge.setMimir(constants.KeygenRetryInterval.String(), test.interval)
			finished := signer.tryKeygenRetry(retry)
			if generator.calls() != test.calls || finished != (test.target == 0) {
				t.Fatalf("late retry: calls=%d want=%d finished=%t", generator.calls(), test.calls, finished)
			}
			wantCalls := test.calls
			if test.target != 0 {
				if retry.targetHeight != test.target {
					t.Fatalf("target=%d want=%d", retry.targetHeight, test.target)
				}
				bridge.setHeight(test.target)
				wantCalls++
				if !signer.tryKeygenRetry(retry) || generator.calls() != wantCalls {
					t.Fatal("realigned retry did not execute at the next shared round")
				}
			}
			if !signer.tryKeygenRetry(retry) || generator.calls() != wantCalls {
				t.Fatal("old worker executed more than once")
			}
			if current, ok := signer.keygenRetries.Load(retryID(block, retry.keygen)); ok && current == retry {
				t.Fatal("retired retry left its token behind")
			}
		})
	}
}

// Exercise actual ceremonies and terminal reports, rather than only the grid
// helper. Preserve develop's retry counts, including the final exact-fit round
// for members that first observe it up to two blocks late.
func TestCommonRetryConfigurationRounds(t *testing.T) {
	for _, test := range []struct {
		interval int64
		rounds   []int64
	}{
		{360, []int64{360}},
		{240, []int64{240, 480}},
		{120, []int64{120, 240, 360, 480, 600}},
		{100, []int64{100, 200, 300, 400, 500, 600}},
	} {
		for _, lateness := range []int64{0, 2} {
			t.Run(fmt.Sprintf("720/%d/late=%d", test.interval, lateness), func(t *testing.T) {
				signer, bridge, block := newFailureReportSigner(t)
				// Keep a 720-block result window (ChurnRetryInterval-1).
				bridge.mimirs[constants.ChurnRetryInterval.String()] = 721
				bridge.mimirs[constants.KeygenRetryInterval.String()] = test.interval
				bridge.height.Store(block.Height + 50)
				calls := 0
				signer.tssKeygen = failureKeygenFunc(func(int64, common.PubKeys) (common.PubKeySet, []ttypes.Blame, error) {
					calls++
					return common.EmptyPubKeySet, []ttypes.Blame{{FailReason: fmt.Sprintf("attempt %d failed", calls)}}, errors.New("DKG failed")
				})
				// The original ceremony fails after 50 blocks. Drive the worker's
				// polls explicitly so the test does not wait hundreds of blocks.
				signer.processKeygenBlock(block)
				id := retryID(block, block.Keygens[0])
				for _, boundary := range test.rounds {
					current, ok := signer.keygenRetries.Load(id)
					if !ok {
						t.Fatalf("round %d was not scheduled", boundary)
					}
					retry, ok := current.(*keygenRetry)
					if !ok || retry.targetHeight != block.Height+boundary {
						t.Fatalf("round %d has unexpected token: %+v", boundary, current)
					}
					bridge.height.Store(block.Height + boundary + lateness)
					if !signer.tryKeygenRetry(retry) {
						t.Fatalf("round %d did not execute", boundary)
					}
					// A consumed worker cannot execute or retire its successor.
					before := calls
					if !signer.tryKeygenRetry(retry) || calls != before {
						t.Fatal("consumed worker executed twice")
					}
				}
				if calls != 1+len(test.rounds) {
					t.Fatalf("ceremonies=%d want=%d", calls, 1+len(test.rounds))
				}
				if _, ok := signer.keygenRetries.Load(id); ok {
					t.Fatal("scheduled a round without enough completion time")
				}
				if len(bridge.attempts) != 1 || bridge.attempts[0].Blame[0].FailReason != fmt.Sprintf("attempt %d failed", calls) {
					t.Fatal("did not report the final completed attempt exactly once")
				}

				// A delayed worker that already owned the next boundary must
				// reach the same decision as the scheduler and skip that round.
				nextBoundary := int64(len(test.rounds)+1) * test.interval
				bridge.height.Store(block.Height + nextBoundary + lateness)
				retry := registerRetryTestWork(signer, block, block.Height+nextBoundary)
				if !signer.tryKeygenRetry(retry) || calls != 1+len(test.rounds) {
					t.Fatal("fire-time gate admitted the excluded round")
				}
				if _, ok := signer.keygenRetries.Load(id); ok {
					t.Fatal("excluded round retained its token")
				}
			})
		}
	}
}
