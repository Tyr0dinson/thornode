package thorchain

import (
	"context"
	"testing"

	"cosmossdk.io/core/comet"
	"github.com/blang/semver"
	abci "github.com/cometbft/cometbft/abci/types"
	sdktestutil "github.com/cosmos/cosmos-sdk/testutil"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/telemetry"
	"gitlab.com/thorchain/thornode/v3/constants"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/keeper"
)

type lifecycleCall struct {
	name      string
	manager   string
	operation string
}

var lifecycleCalls = []lifecycleCall{
	{name: "oracle/begin", manager: "oracle", operation: "begin_block"},
	{name: "gas/begin", manager: "gas", operation: "begin_block"},
	{name: "network/begin", manager: "network", operation: "begin_block"},
	{name: "slasher/begin", manager: "slasher", operation: "begin_block"},
	{name: "validator/begin", manager: "validator", operation: "begin_block"},
	{name: "swap_queue/end", manager: "swap_queue", operation: "end_block"},
	{name: "advanced_swap_queue/end", manager: "advanced_swap_queue", operation: "end_block"},
	{name: "slasher/lack_signing", manager: "slasher", operation: "lack_signing"},
	{name: "pool/end", manager: "pool", operation: "end_block"},
	{name: "observer/end", manager: "observer", operation: "end_block"},
	{name: "network/update", manager: "network", operation: "update_network"},
	{name: "stable_reserve/end", manager: "stable_reserve", operation: "end_block"},
	{name: "network/end", manager: "network", operation: "end_block"},
	{name: "validator/end", manager: "validator", operation: "end_block"},
	{name: "tx_out/end", manager: "tx_out", operation: "end_block"},
	{name: "gas/end", manager: "gas", operation: "end_block"},
	{name: "volume/end", manager: "volume", operation: "end_block"},
	{name: "dynamic_fee/end", manager: "dynamic_fee", operation: "end_block"},
	{name: "scheduled_migration/end", manager: "scheduled_migration", operation: "end_block"},
}

type lifecycleRecorder struct {
	calls   []string
	panicAt string
}

func (r *lifecycleRecorder) record(name string) {
	r.calls = append(r.calls, name)
	if name == r.panicAt {
		panic(name)
	}
}

type lifecycleKeeper struct {
	keeper.KVStoreDummy
	version semver.Version
}

func (k lifecycleKeeper) GetLowestActiveVersion(cosmos.Context) semver.Version {
	return k.version
}

type lifecycleOracle struct{ recorder *lifecycleRecorder }

func (m lifecycleOracle) BeginBlock(cosmos.Context) error {
	m.recorder.record("oracle/begin")
	return nil
}

type lifecycleGas struct {
	GasManager
	recorder *lifecycleRecorder
}

func (m lifecycleGas) BeginBlock() {
	m.recorder.record("gas/begin")
}

func (m lifecycleGas) EndBlock(cosmos.Context, keeper.Keeper, EventManager) {
	m.recorder.record("gas/end")
}

type lifecycleNetwork struct {
	NetworkManager
	recorder *lifecycleRecorder
}

func (m lifecycleNetwork) BeginBlock(cosmos.Context, Manager) error {
	m.recorder.record("network/begin")
	return nil
}

func (m lifecycleNetwork) UpdateNetwork(cosmos.Context, Manager) error {
	m.recorder.record("network/update")
	return nil
}

func (m lifecycleNetwork) EndBlock(cosmos.Context, Manager) error {
	m.recorder.record("network/end")
	return nil
}

type lifecycleSlasher struct {
	Slasher
	recorder *lifecycleRecorder
}

func (m lifecycleSlasher) BeginBlock(cosmos.Context, constants.ConstantValues) {
	m.recorder.record("slasher/begin")
}

func (m lifecycleSlasher) LackSigning(cosmos.Context, Manager) error {
	m.recorder.record("slasher/lack_signing")
	return nil
}

type lifecycleValidator struct {
	ValidatorManager
	recorder *lifecycleRecorder
	updates  []abci.ValidatorUpdate
}

func (m lifecycleValidator) BeginBlock(cosmos.Context, Manager, []string) error {
	m.recorder.record("validator/begin")
	return nil
}

func (m lifecycleValidator) EndBlock(cosmos.Context, Manager) []abci.ValidatorUpdate {
	m.recorder.record("validator/end")
	return m.updates
}

type lifecycleSwapQueue struct{ recorder *lifecycleRecorder }

func (m lifecycleSwapQueue) EndBlock(cosmos.Context, Manager) error {
	m.recorder.record("swap_queue/end")
	return nil
}

type lifecycleAdvSwapQueue struct {
	AdvSwapQueue
	recorder *lifecycleRecorder
}

func (m lifecycleAdvSwapQueue) EndBlock(cosmos.Context, Manager, bool) error {
	m.recorder.record("advanced_swap_queue/end")
	return nil
}

type lifecyclePool struct{ recorder *lifecycleRecorder }

func (m lifecyclePool) EndBlock(cosmos.Context, Manager) error {
	m.recorder.record("pool/end")
	return nil
}

type lifecycleObserver struct {
	ObserverManager
	recorder *lifecycleRecorder
}

func (m lifecycleObserver) EndBlock(cosmos.Context, keeper.Keeper) {
	m.recorder.record("observer/end")
}

type lifecycleStableReserve struct {
	StableReserveManager
	recorder *lifecycleRecorder
}

func (m lifecycleStableReserve) EndBlock(cosmos.Context, Manager) error {
	m.recorder.record("stable_reserve/end")
	return nil
}

type lifecycleTxOut struct {
	TxOutStore
	recorder *lifecycleRecorder
}

func (m lifecycleTxOut) EndBlock(cosmos.Context, Manager) error {
	m.recorder.record("tx_out/end")
	return nil
}

type lifecycleVolume struct{ recorder *lifecycleRecorder }

func (m lifecycleVolume) EndBlock(cosmos.Context) error {
	m.recorder.record("volume/end")
	return nil
}

type lifecycleDynamicFee struct {
	DynamicFeeManager
	recorder *lifecycleRecorder
}

func (m lifecycleDynamicFee) EndBlock(cosmos.Context) error {
	m.recorder.record("dynamic_fee/end")
	return nil
}

type lifecycleScheduledMigration struct{ recorder *lifecycleRecorder }

func (m lifecycleScheduledMigration) EndBlock(cosmos.Context, Manager) error {
	m.recorder.record("scheduled_migration/end")
	return nil
}

type emptyBlockInfo struct{}

func (emptyBlockInfo) GetEvidence() comet.EvidenceList { return emptyEvidenceList{} }
func (emptyBlockInfo) GetValidatorsHash() []byte       { return nil }
func (emptyBlockInfo) GetProposerAddress() []byte      { return nil }
func (emptyBlockInfo) GetLastCommit() comet.CommitInfo { return emptyCommitInfo{} }

type emptyEvidenceList struct{}

func (emptyEvidenceList) Len() int               { return 0 }
func (emptyEvidenceList) Get(int) comet.Evidence { return nil }

type emptyCommitInfo struct{}

func (emptyCommitInfo) Round() int32           { return 0 }
func (emptyCommitInfo) Votes() comet.VoteInfos { return emptyVoteInfos{} }

type emptyVoteInfos struct{}

func (emptyVoteInfos) Len() int               { return 0 }
func (emptyVoteInfos) Get(int) comet.VoteInfo { return nil }

func newLifecycleModule(
	t *testing.T,
	trace bool,
	panicAt string,
) (AppModule, context.Context, *lifecycleRecorder, *Tracer) {
	t.Helper()
	tracer := &Tracer{}
	if trace {
		var err error
		tracer, err = NewTracer(
			TraceConfig{TraceManagers: true},
			true,
			prometheus.NewRegistry(),
		)
		require.NoError(t, err)
	}

	recorder := &lifecycleRecorder{panicAt: panicAt}
	updates := []abci.ValidatorUpdate{{Power: 7}}
	version := constants.SWVersion
	k := lifecycleKeeper{version: version}
	mgr := &Mgrs{
		currentVersion:            version,
		constAccessor:             constants.GetConstantValues(version),
		gasMgr:                    lifecycleGas{GasManager: NewDummyGasManager(), recorder: recorder},
		eventMgr:                  NewDummyEventMgr(),
		txOutStore:                lifecycleTxOut{TxOutStore: NewTxStoreDummy(), recorder: recorder},
		networkMgr:                lifecycleNetwork{NetworkManager: NewNetworkMgrDummy(), recorder: recorder},
		validatorMgr:              lifecycleValidator{ValidatorManager: NewValidatorDummyMgr(), recorder: recorder, updates: updates},
		obMgr:                     lifecycleObserver{ObserverManager: NewDummyObserverManager(), recorder: recorder},
		poolMgr:                   lifecyclePool{recorder: recorder},
		swapQ:                     lifecycleSwapQueue{recorder: recorder},
		advSwapQueue:              lifecycleAdvSwapQueue{AdvSwapQueue: NewDummyAdvSwapQueue(), recorder: recorder},
		slasher:                   lifecycleSlasher{Slasher: NewDummySlasher(), recorder: recorder},
		stableReserveManager:      lifecycleStableReserve{StableReserveManager: NewDummyStableReserveManager(), recorder: recorder},
		scheduledMigrationManager: lifecycleScheduledMigration{recorder: recorder},
		oracleManager:             lifecycleOracle{recorder: recorder},
		volumeManager:             lifecycleVolume{recorder: recorder},
		dynamicFeeManager:         lifecycleDynamicFee{recorder: recorder},
		K:                         k,
		tracer:                    tracer,
	}
	// BeginBlock also checks InstantSend in a cached store context.
	store := sdktestutil.DefaultContextWithDB(t, cosmos.NewKVStoreKey("lifecycle"), cosmos.NewTransientStoreKey("transient"))
	t.Cleanup(func() { require.NoError(t, store.DB.Close()) })
	ctx := store.Ctx.WithCometInfo(emptyBlockInfo{})
	return AppModule{mgr: mgr}, ctx, recorder, tracer
}

func runLifecycle(t *testing.T, trace bool) ([]string, []abci.ValidatorUpdate, *Tracer) {
	t.Helper()
	module, ctx, recorder, tracer := newLifecycleModule(t, trace, "")
	require.NoError(t, module.BeginBlock(ctx))
	updates, err := module.EndBlock(ctx)
	require.NoError(t, err)
	return recorder.calls, updates, tracer
}

func TestManagerLifecycleTracing(t *testing.T) {
	enabledCalls, enabledUpdates, tracer := runLifecycle(t, true)
	disabledCalls, disabledUpdates, _ := runLifecycle(t, false)

	expectedCalls := make([]string, 0, len(lifecycleCalls))
	for _, call := range lifecycleCalls {
		expectedCalls = append(expectedCalls, call.name)
	}
	require.Equal(t, expectedCalls, enabledCalls)
	require.Equal(t, disabledCalls, enabledCalls)
	require.Equal(t, disabledUpdates, enabledUpdates)
	require.Equal(t, len(lifecycleCalls), testutil.CollectAndCount(tracer.managerRuns))
	for _, call := range lifecycleCalls {
		require.Equal(t, float64(1), testutil.ToFloat64(tracer.managerRuns.WithLabelValues(
			call.manager,
			call.operation,
			telemetry.OutcomeSuccess,
		)))
	}
}

func TestManagerLifecyclePanicLabels(t *testing.T) {
	for _, call := range lifecycleCalls {
		t.Run(call.name, func(t *testing.T) {
			module, ctx, recorder, tracer := newLifecycleModule(t, true, call.name)
			require.PanicsWithValue(t, call.name, func() {
				if err := module.BeginBlock(ctx); err != nil {
					panic(err)
				}
				_, _ = module.EndBlock(ctx)
			})
			require.Equal(t, call.name, recorder.calls[len(recorder.calls)-1])
			require.Equal(t, float64(1), testutil.ToFloat64(tracer.managerRuns.WithLabelValues(
				call.manager,
				call.operation,
				telemetry.OutcomePanic,
			)))
		})
	}
}
