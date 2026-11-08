package signer

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	sdktypes "github.com/cosmos/cosmos-sdk/types"
	"github.com/rs/zerolog/log"
	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients"
	chainclienttypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type xmrAbsenceBridge struct {
	mu     sync.Mutex
	height int64
	*retryAwareKeygenBridge
	reports         []*ttypes.MsgTssPool
	beforeBroadcast func()
}

func (b *xmrAbsenceBridge) GetBlockHeight() (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.height == 0 {
		return 100, nil
	}
	return b.height, nil
}

func (b *xmrAbsenceBridge) GetMimir(key string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.retryAwareKeygenBridge.GetMimir(key)
}

func (b *xmrAbsenceBridge) Broadcast(msgs ...sdktypes.Msg) (common.TxID, error) {
	for _, msg := range msgs {
		report, ok := msg.(*ttypes.MsgTssPool)
		if !ok {
			return common.BlankTxID, errors.New("unexpected keygen message type")
		}
		b.mu.Lock()
		b.reports = append(b.reports, report)
		b.mu.Unlock()
	}
	if b.beforeBroadcast != nil {
		b.beforeBroadcast()
	}
	return common.TxID("absence-report"), nil
}

func (b *xmrAbsenceBridge) GetKeygenStdTx(
	pk common.PubKey, sig, backup []byte, blame []ttypes.Blame,
	members common.PubKeys, kind ttypes.KeygenType, chains common.Chains,
	height, elapsed int64, ed common.PubKey, edBackup []byte, extras ...any,
) (sdktypes.Msg, error) {
	signer, err := members[0].GetThorAddress()
	if err != nil {
		return nil, err
	}
	return ttypes.NewMsgTssPoolV3(members.Strings(), pk, sig, backup, kind, height, blame, chains.Strings(), signer, elapsed, ed, edBackup, nil, common.EmptyPubKey, 0)
}

func (s *SignSuite) TestXMRKeygenAccountableFailureTerminalOnly(c *C) {
	for _, scenario := range []string{"terminal", "retry", "shutdown", "diagnostic", "equivocation"} {
		a, b := ttypes.GetRandomPubKey(), ttypes.GetRandomPubKey()
		keygen, err := ttypes.NewKeygen(100, []string{a.String(), b.String()}, ttypes.KeygenType_XMRMonovaultKeygen)
		c.Assert(err, IsNil)
		bridge := &xmrAbsenceBridge{retryAwareKeygenBridge: &retryAwareKeygenBridge{
			mimirs: map[string]int64{constants.ChurnRetryInterval.String(): 1000, constants.KeygenRetryInterval.String(): 0},
		}}
		absent := []string{strings.ToUpper(b.String()), b.String()}
		if scenario == "diagnostic" || scenario == "equivocation" {
			absent = nil
		}
		custom := &MockCustomKeygenChainClient{
			MockChainClient: &MockChainClient{},
			results: []chainclienttypes.CustomKeygenResult{{
				AbsentMembers: absent,
				Blame:         []ttypes.Blame{{FailReason: scenario, BlameNodes: []ttypes.Node{{Pubkey: b.String()}}}},
			}},
			errs: []error{errors.New("failed XMR keygen")},
		}
		sign := &Signer{
			chains:          map[common.Chain]chainclients.ChainClient{common.XMRChain: custom},
			thorchainBridge: bridge, stopChan: make(chan struct{}),
			logger: log.Logger, errCounter: s.metrics.GetCounterVec(metrics.SignerError),
		}
		block := ttypes.KeygenBlock{Height: 100, Keygens: []ttypes.Keygen{keygen}}
		if scenario == "retry" {
			bridge.mimirs[constants.KeygenRetryInterval.String()] = 100
		}
		if scenario == "shutdown" {
			close(sign.stopChan)
		}
		sign.runKeygenRequest(block, keygen)
		if scenario == "terminal" {
			c.Assert(bridge.reports, HasLen, 1)
			report := bridge.reports[0]
			c.Assert(report.ValidateBasic(), IsNil)
			c.Assert(report.Blame[0].BlameNodes, DeepEquals, []ttypes.Node{{Pubkey: b.String()}})
			c.Assert(report.PoolPubKey.IsEmpty(), Equals, true)
			c.Assert(report.PoolPubKeyFrost.IsEmpty(), Equals, true)
			c.Assert(report.ObservedMoneroHeight, Equals, int64(0))
		} else {
			c.Assert(bridge.reports, HasLen, 0, Commentf("scenario %s", scenario))
		}
		if scenario == "retry" {
			c.Assert(sign.keygenRetries.CompareAndDelete(retryID(block, keygen), nil), Equals, false)
			close(sign.stopChan)
			sign.keygenWorkerWG.Wait()
			c.Assert(bridge.reports, HasLen, 0)
		}
	}
}

func TestXMRKeygenAbsenceNeverReappearsAcrossRetries(t *testing.T) {
	var slot xmrKeygenSlot
	keygen := ttypes.Keygen{ID: common.TxID("ceremony"), Members: []string{"first", "second", "third"}}
	failure := func(absent ...string) error { return &xmrKeygenFailure{cause: errors.New("absence"), absent: absent} }
	_ = slot.recordAbsence(100, keygen, failure("second", "third"))
	err := slot.recordAbsence(100, keygen, failure("first", "third"))
	var retained *xmrKeygenFailure
	if !errors.As(err, &retained) || len(retained.absent) != 1 || retained.absent[0] != "third" {
		t.Fatalf("previous participant became absent: %+v", retained)
	}
	_ = slot.recordAbsence(100, keygen, errors.New("diagnostic later-round failure"))
	err = slot.recordAbsence(100, keygen, failure("first", "second", "third"))
	if !errors.As(err, &retained) || len(retained.absent) != 0 {
		t.Fatal("diagnostic attempt acquired absence blame")
	}
	err = slot.recordAbsence(101, keygen, failure("first"))
	if !errors.As(err, &retained) || len(retained.absent) != 1 {
		t.Fatal("new ceremony retained old presence")
	}
}

func TestXMRKeygenRetryRetirementReportsRetainedFailure(t *testing.T) {
	a, b := ttypes.GetRandomPubKey(), ttypes.GetRandomPubKey()
	keygen, err := ttypes.NewKeygen(100, []string{a.String(), b.String()}, ttypes.KeygenType_XMRMonovaultKeygen)
	if err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		done := make(chan struct{})
		bridge := &xmrAbsenceBridge{
			height: 100, beforeBroadcast: func() { close(done) },
			retryAwareKeygenBridge: &retryAwareKeygenBridge{
				mimirs: map[string]int64{constants.ChurnRetryInterval.String(): 1000, constants.KeygenRetryInterval.String(): 10},
			},
		}
		sign := &Signer{thorchainBridge: bridge, stopChan: make(chan struct{}), logger: log.Logger}
		block := ttypes.KeygenBlock{Height: 100, Keygens: []ttypes.Keygen{keygen}}
		failure := &xmrKeygenFailure{cause: errors.New("presence timeout"), absent: []string{b.String()}}
		_ = sign.xmrKeygen.recordAbsence(block.Height, keygen, failure)
		if !sign.scheduleKeygenRetry(block, keygen, nil, failure) {
			t.Fatal("retry not scheduled")
		}
		synctest.Wait()
		bridge.mu.Lock()
		if len(bridge.reports) != 0 {
			t.Fatal("reported while retry remained")
		}
		// Policy disables the scheduled retry. The real waiting worker retires and
		// reports its saved failure without executing a new attempt.
		bridge.height = 110
		bridge.mimirs[constants.KeygenRetryInterval.String()] = 0
		bridge.mu.Unlock()
		<-done
		sign.keygenWorkerWG.Wait()
		if len(bridge.reports) != 1 {
			t.Fatalf("retired retry reports = %d", len(bridge.reports))
		}
		close(sign.stopChan)
		sign.reportXMRKeygenFailure(block, keygen, failure)
		if len(bridge.reports) != 1 {
			t.Fatal("shutdown reported another failure")
		}
	})
}

func (s *SignSuite) TestXMRKeygenFailureBroadcastStopsBetweenTransportRetries(c *C) {
	// The guard applies inside backoff, so shutdown after building the transaction
	// cannot slip through the lower-level broadcast retry path.
	sign := &Signer{stopChan: make(chan struct{}), logger: log.Logger}
	close(sign.stopChan)
	_, err := sign.broadcastKeygenMsgs(100, &ttypes.MsgTssPool{
		KeygenType: ttypes.KeygenType_XMRMonovaultKeygen, Blame: []ttypes.Blame{{FailReason: "absence"}},
	})
	c.Assert(err, NotNil)
	c.Assert(strings.Contains(err.Error(), "stopped"), Equals, true)
}

func (s *SignSuite) TestXMRKeygenRetiredRetryCannotReportOverNewAttempt(c *C) {
	a, b := ttypes.GetRandomPubKey(), ttypes.GetRandomPubKey()
	keygen, err := ttypes.NewKeygen(100, []string{a.String(), b.String()}, ttypes.KeygenType_XMRMonovaultKeygen)
	c.Assert(err, IsNil)
	bridge := &xmrAbsenceBridge{retryAwareKeygenBridge: &retryAwareKeygenBridge{
		mimirs: map[string]int64{constants.ChurnRetryInterval.String(): 1000},
	}}
	sign := &Signer{thorchainBridge: bridge, stopChan: make(chan struct{}), logger: log.Logger}
	failure := &xmrKeygenFailure{cause: errors.New("old absence"), absent: []string{b.String()}}
	block := ttypes.KeygenBlock{Height: 100, Keygens: []ttypes.Keygen{keygen}}
	retry := &keygenRetry{block: block, keygen: keygen, xmrFailure: failure}
	_ = sign.xmrKeygen.recordAbsence(100, keygen, failure)
	sign.xmrKeygen.admit(100, keygen, false)
	sign.reportRetiredXMRKeygenFailure(retry)
	c.Assert(bridge.reports, HasLen, 0)
	// The new attempt saw every member, invalidating the old absence.
	_ = sign.xmrKeygen.recordAbsence(100, keygen, errors.New("later-round failure"))
	sign.xmrKeygen.finishAndPromote()
	sign.reportRetiredXMRKeygenFailure(retry)
	c.Assert(bridge.reports, HasLen, 0)
	// Even valid old evidence may not be reused after a replacement ceremony.
	_ = sign.xmrKeygen.recordAbsence(101, keygen, failure)
	sign.reportRetiredXMRKeygenFailure(retry)
	c.Assert(bridge.reports, HasLen, 0)
}
