package monero

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blang/semver"
	"github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/hashicorp/go-multierror"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

	"gitlab.com/thorchain/thornode/v3/app"
	"gitlab.com/thorchain/thornode/v3/bifrost/blockscanner"
	"gitlab.com/thorchain/thornode/v3/bifrost/metrics"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/runners"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/signercache"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/shared/utxo"
	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	bifrosttss "gitlab.com/thorchain/thornode/v3/bifrost/tss"
	gotssblame "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/blame"
	gotsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	memo "gitlab.com/thorchain/thornode/v3/x/thorchain/memo"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	maxAsgardAddresses = 100

	mimirXMRConsolidationEnabled             = "XMRConsolidationEnabled"
	mimirXMRConsolidationCheckIntervalBlocks = "XMRConsolidationCheckIntervalBlocks"
	mimirXMRConsolidationCooldownBlocks      = "XMRConsolidationCooldownBlocks"
	mimirXMRConsolidationMinUnlockedOutputs  = "XMRConsolidationMinUnlockedOutputs"
	mimirXMRConsolidationTargetOutputs       = "XMRConsolidationTargetOutputs"
	mimirXMRConsolidationReserveBps          = "XMRConsolidationReserveBps"
	mimirXMRConsolidationChurnLookahead      = "XMRConsolidationChurnLookaheadBlocks"
	mimirXMRConsolidationMaxRetries          = "XMRConsolidationMaxRetries"

	defaultXMRConsolidationCheckIntervalBlocks = int64(10)
	defaultXMRConsolidationCooldownBlocks      = int64(30)
	defaultXMRConsolidationMinUnlockedOutputs  = int64(40)
	defaultXMRConsolidationTargetOutputs       = int64(15)
	defaultXMRConsolidationReserveBps          = int64(50) // 0.50%
	defaultXMRConsolidationChurnLookahead      = int64(720)
	defaultXMRConsolidationMaxRetries          = int64(3)
	defaultXMRConsolidationMinReserveAtomic    = uint64(50_000_000) // 0.00005 XMR (piconero)
)

var ErrFrostNotConfigured = errors.New("xmr frost signing requires signer_endpoint or api_host configuration")

var xmrNecessaryFeePattern = regexp.MustCompile(`necessary_fee Some\((\d+)\)`)

type xmrKeyImagePeerReadiness interface {
	MissingPeerAddresses(pubKeys []string) ([]string, error)
}

type signedTxPayload struct {
	TxHash       string                `json:"tx_hash"`
	TxMetadata   string                `json:"tx_metadata"`
	AmountAtomic uint64                `json:"amount_atomic,omitempty"`
	FeeAtomic    uint64                `json:"fee_atomic"`
	MsgID        string                `json:"msg_id,omitempty"`
	Eventuality  tssp.FrostEventuality `json:"eventuality,omitempty"`
	Broadcasted  bool                  `json:"broadcasted,omitempty"`
	TxKeys       []string              `json:"tx_keys,omitempty"`
}

type xmrValidatedSignRequest struct {
	vaultPubKey  common.PubKey
	vaultStatus  ttypes.VaultStatus
	fromAddress  string
	toAddress    string
	amountAtomic uint64
	sweepIfFinal bool
}

type lockedOutputsError struct {
	available                   uint64
	required                    uint64
	total                       uint64
	locked                      uint64
	confirmationsRemaining      uint64
	minConfirmationsRemaining   uint64
	maxConfirmationsRemaining   uint64
	lockedOutputCount           int
	confirmationDetailsObserved bool
}

type keyImageBacklogError struct {
	queued     uint64
	active     bool
	activeWork string
}

type xmrSyncingToTipError struct {
	reason string
}

type xmrTransientFrostKeySignError struct {
	reason string
}

const xmrDefaultMaxConfirmationCap uint64 = 30 // ~1 hour at 2-minute XMR blocks
const xmrSpendableConfirmations uint64 = 10

func (e *lockedOutputsError) Error() string {
	if e.confirmationDetailsObserved {
		return fmt.Sprintf(
			"insufficient xmr balance for signing: available=%d required=%d (total=%d locked=%d, locked_outputs=%d, confirmations_remaining=%d min_remaining=%d max_remaining=%d)",
			e.available, e.required, e.total, e.locked, e.lockedOutputCount, e.confirmationsRemaining, e.minConfirmationsRemaining, e.maxConfirmationsRemaining,
		)
	}
	return fmt.Sprintf(
		"insufficient xmr balance for signing: available=%d required=%d (total=%d locked=%d, confirmations_remaining=unknown)",
		e.available, e.required, e.total, e.locked,
	)
}

func (e *lockedOutputsError) RetryAfterNextBlock() bool { return true }

func (e *lockedOutputsError) RetryAfterBlocks() uint64 {
	if !e.confirmationDetailsObserved || e.confirmationsRemaining == 0 {
		return 1
	}
	return e.confirmationsRemaining
}

func (e *keyImageBacklogError) Error() string {
	if strings.TrimSpace(e.activeWork) != "" {
		return fmt.Sprintf("pending xmr key-image backlog: queued=%d active=%t active_work=%s", e.queued, e.active, e.activeWork)
	}
	return fmt.Sprintf("pending xmr key-image backlog: queued=%d active=%t", e.queued, e.active)
}

func (e *keyImageBacklogError) RetryAfterNextBlock() bool { return true }

func (e *xmrSyncingToTipError) Error() string {
	if strings.TrimSpace(e.reason) == "" {
		return "xmr node is still syncing_to_tip"
	}
	return fmt.Sprintf("xmr node is still syncing_to_tip: %s", e.reason)
}

func (e *xmrSyncingToTipError) RetryAfterNextBlock() bool { return true }

func (e *xmrTransientFrostKeySignError) Error() string {
	if strings.TrimSpace(e.reason) == "" {
		return "transient xmr frost keysign failure"
	}
	return fmt.Sprintf("transient xmr frost keysign failure: %s", e.reason)
}

func (e *xmrTransientFrostKeySignError) RetryAfterNextBlock() bool { return true }

type xmrConsolidationSettings struct {
	Enabled             bool
	CheckIntervalBlocks int64
	CooldownBlocks      int64
	MinUnlockedOutputs  int64
	TargetOutputs       int64
	ReserveBps          int64
	ChurnLookahead      int64
	MaxRetries          int64
}

type xmrConsolidationVaultState struct {
	LastAttemptHeight int64
	LastSuccessHeight int64
	LastTxHash        string
	ConsecutiveFails  int64
	LastSkipHeight    int64
	LastSkipReason    string
}

type xmrKeyImageCatchupState struct {
	refs    []xmrFrostApplyKeyImageRef
	pending bool
}

type xmrKeyImageCeremonyGate struct {
	mu            sync.Mutex
	cond          *sync.Cond
	nextTicket    uint64
	servingTicket uint64
	queued        uint64
	active        bool
}

type xmrKeyImageProofCacheEntry struct {
	proofs   []*common.XmrKeyImageProof
	cachedAt time.Time
}

// Client implements the chain client interface for Monero.
type Client struct {
	logger zerolog.Logger
	cfg    config.BifrostChainConfiguration
	chain  common.Chain

	thorchainBridge     thorclient.ThorchainBridge
	localNodePubKey     common.PubKey
	localVaultBootstrap *frostVaultBootstrap
	storage             *blockscanner.BlockScannerStorage
	blockScanner        *blockscanner.BlockScanner
	scanner             *Scanner
	frostSigner         *FrostSigner
	tssServer           tssp.Server
	signerCacheManager  *signercache.CacheManager
	vaultAddressStore   *VaultAddressStore
	vaultAddressMap     map[string]common.Address
	vaultAddrToKey      map[string]string
	vaultAddressMu      sync.RWMutex
	globalTxsQueue      chan stypes.TxIn
	globalSolvencyQueue chan stypes.Solvency

	asgardMu                sync.RWMutex
	asgardAddresses         []common.Address
	lastAsgard              time.Time
	consolidationInitOnce   sync.Once
	consolidationSettingsMu sync.RWMutex
	consolidationSettings   xmrConsolidationSettings
	lastConsolidationConfig time.Time
	hasConsolidationConfig  bool
	vaultLockMu             sync.Mutex
	vaultLocks              map[string]*sync.Mutex
	emptyVaultLock          sync.Mutex
	consolidationStateMu    sync.Mutex
	consolidationState      map[string]xmrConsolidationVaultState
	stateSyncMu             sync.Mutex
	trackedVaultSyncMu      sync.Mutex
	trackedVaultSyncState   map[string]string
	keyImageCatchupMu       sync.Mutex
	keyImageCatchupState    map[string]xmrKeyImageCatchupState
	keyImageCeremonyGateMu  sync.Mutex
	keyImageCeremonyGate    *xmrKeyImageCeremonyGate
	keyImageProofCacheMu    sync.Mutex
	keyImageProofCache      map[string]xmrKeyImageProofCacheEntry
	keyImageWorkStore       *xmrKeyImageWorkStateStore
	activeXMRWorkMu         sync.Mutex
	activeXMRWorkItem       *xmrWorkItem
	consolidateInProgress   *atomic.Bool
	lastConsolidationHeight *atomic.Int64
	lastSolvencyCheckHeight *atomic.Int64
	solvencyCheckAfterIdle  *atomic.Bool
	wg                      *sync.WaitGroup
	stopchan                chan struct{}
}

// NewClient creates a monero chain client backed by the shared block scanner.
func NewClient(
	thorKeys *thorclient.Keys,
	cfg config.BifrostChainConfiguration,
	tssServer *tssp.TssServer,
	bridge thorclient.ThorchainBridge,
	m *metrics.Metrics,
) (types.ChainClient, error) {
	if !cfg.ChainID.Equals(common.XMRChain) {
		return nil, fmt.Errorf("monero client requires chain id %s, got %s", common.XMRChain, cfg.ChainID)
	}
	if bridge == nil {
		return nil, errors.New("thorchain bridge is nil")
	}
	if m == nil {
		return nil, errors.New("metrics instance is nil")
	}
	if tssServer == nil {
		return nil, errors.New("tss server is nil")
	}
	logger := log.With().Str("module", cfg.ChainID.String()).Logger()
	frostSigner, err := NewFrostSigner(cfg)
	if err != nil {
		return nil, fmt.Errorf("fail to create FROST signer: %w", err)
	}
	// Bifrost owns the sidecar's in-memory session lifecycle: every time we
	// start fresh, drop any half-finished FROST sign/key-image sessions the
	// sidecar inherited from a previous bifrost lifecycle. Without this, a
	// bifrost recreate without a sidecar restart leaves stale sessions that
	// cause "leader not reachable" / "missing blame nodes" failures.
	// Best-effort — log and continue if the sidecar isn't reachable yet
	// (the chain client startup will retry signing later anyway).
	if cleared, clearErr := frostSigner.ClearSessions(); clearErr != nil {
		logger.Warn().Err(clearErr).Msg("fail to clear FROST sidecar sessions on bifrost startup; sidecar may carry stale state")
	} else if cleared > 0 {
		logger.Info().Int("cleared", cleared).Msg("cleared stale FROST sidecar sessions on bifrost startup")
	}

	localNodePubKey := common.EmptyPubKey
	if thorKeys != nil {
		if privKey, keyErr := thorKeys.GetPrivateKey(); keyErr != nil {
			logger.Debug().Err(keyErr).Msg("fail to get local node private key for monero vault mapping")
		} else if tmPubKey, convErr := codec.ToCmtPubKeyInterface(privKey.PubKey()); convErr != nil {
			logger.Debug().Err(convErr).Msg("fail to convert local node pubkey for monero vault mapping")
		} else if pk, pubErr := common.NewPubKeyFromCrypto(tmPubKey); pubErr != nil {
			logger.Debug().Err(pubErr).Msg("fail to encode local node pubkey for monero vault mapping")
		} else {
			localNodePubKey = pk
		}
	}

	localVaultBootstrap, bootstrapErr := deriveLocalFrostVaultBootstrap(thorKeys)
	if bootstrapErr != nil {
		logger.Debug().Err(bootstrapErr).Msg("fail to derive local monero frost vault bootstrap material")
	}
	scanner := NewScanner(logger, frostSigner, bridge, cfg.ChainID, localNodePubKey, cfg.BlockScanner.MaxReorgRescanBlocks)
	scanner.SetLocalVaultBootstrap(localVaultBootstrap)

	c := &Client{
		logger:                  logger,
		cfg:                     cfg,
		chain:                   cfg.ChainID,
		thorchainBridge:         bridge,
		localNodePubKey:         localNodePubKey,
		localVaultBootstrap:     localVaultBootstrap,
		scanner:                 scanner,
		frostSigner:             frostSigner,
		tssServer:               tssServer,
		asgardAddresses:         make([]common.Address, 0),
		vaultLocks:              make(map[string]*sync.Mutex),
		consolidationState:      make(map[string]xmrConsolidationVaultState),
		trackedVaultSyncState:   make(map[string]string),
		keyImageCatchupState:    make(map[string]xmrKeyImageCatchupState),
		consolidateInProgress:   atomic.NewBool(false),
		lastConsolidationHeight: atomic.NewInt64(0),
		signerCacheManager:      nil,
		vaultAddressMap:         make(map[string]common.Address),
		vaultAddrToKey:          make(map[string]string),
		lastSolvencyCheckHeight: atomic.NewInt64(0),
		solvencyCheckAfterIdle:  atomic.NewBool(false),
		wg:                      &sync.WaitGroup{},
		stopchan:                make(chan struct{}),
	}

	var path string
	if len(c.cfg.BlockScanner.DBPath) > 0 {
		path = fmt.Sprintf("%s/%s", c.cfg.BlockScanner.DBPath, c.cfg.BlockScanner.ChainID)
	}

	c.storage, err = blockscanner.NewBlockScannerStorage(path, c.cfg.ScannerLevelDB)
	if err != nil {
		return nil, fmt.Errorf("fail to create scanner storage: %w", err)
	}
	c.keyImageWorkStore, err = newXMRKeyImageWorkStateStore(c.storage.GetInternalDb())
	if err != nil {
		return nil, fmt.Errorf("fail to create xmr key-image work state store: %w", err)
	}

	c.blockScanner, err = blockscanner.NewBlockScanner(c.cfg.BlockScanner, c.storage, m, c.thorchainBridge, c.scanner)
	if err != nil {
		return nil, fmt.Errorf("fail to create block scanner: %w", err)
	}

	c.signerCacheManager, err = signercache.NewSignerCacheManager(c.storage.GetInternalDb())
	if err != nil {
		return nil, fmt.Errorf("fail to create signer cache manager: %w", err)
	}
	c.vaultAddressStore = NewVaultAddressStore(c.storage.GetInternalDb())
	c.scanner.SetReorgStore(NewReorgStore(c.storage.GetInternalDb()))
	// Wire solvency reporting to the per-block scan loop. Without this, the
	// chain client's ReportSolvency only fires from the auto-unhalt
	// SolvencyCheckRunner — which itself is gated on the chain being already
	// halted, so vault drift is never detected during normal operation.
	// Match the XRP / Solana wiring pattern (their scanners take a
	// SolvencyReporter callback in their constructors).
	c.scanner.SetSolvencyReporter(c.ReportSolvency)
	if err := c.loadVaultAddressMap(); err != nil {
		c.logger.Warn().Err(err).Msg("fail to load persisted monero vault addresses")
	}

	return c, nil
}

func newXMRKeyImageCeremonyGate() *xmrKeyImageCeremonyGate {
	g := &xmrKeyImageCeremonyGate{}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (c *Client) getXMRKeyImageCeremonyGate() *xmrKeyImageCeremonyGate {
	c.keyImageCeremonyGateMu.Lock()
	defer c.keyImageCeremonyGateMu.Unlock()
	if c.keyImageCeremonyGate == nil {
		c.keyImageCeremonyGate = newXMRKeyImageCeremonyGate()
	}
	return c.keyImageCeremonyGate
}

func (c *Client) acquireXMRKeyImageCeremonySlot() func() {
	gate := c.getXMRKeyImageCeremonyGate()
	gate.mu.Lock()
	myTicket := gate.nextTicket
	gate.nextTicket++
	gate.queued++
	for gate.active || myTicket != gate.servingTicket {
		gate.cond.Wait()
	}
	gate.queued--
	gate.active = true
	gate.mu.Unlock()
	return func() {
		gate.mu.Lock()
		gate.active = false
		gate.servingTicket++
		gate.cond.Broadcast()
		gate.mu.Unlock()
	}
}

func (c *Client) pendingXMRKeyImageBacklog() (queued uint64, active bool) {
	gate := c.getXMRKeyImageCeremonyGate()
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.queued, gate.active
}

func (c *Client) pendingXMRKeyImageBacklogDetails() (queued uint64, active bool, activeWork *xmrWorkItem) {
	queued, active = c.pendingXMRKeyImageBacklog()
	if active {
		activeWork = c.getActiveXMRWorkItem()
	}
	return queued, active, activeWork
}

func (c *Client) signalXMRKeyImageBacklogChanged() {
	gate := c.getXMRKeyImageCeremonyGate()
	gate.mu.Lock()
	gate.cond.Broadcast()
	gate.mu.Unlock()
}

func xmrKeyImageBacklogConflictsWithSign(activeWork *xmrWorkItem, vaultPubKey common.PubKey) bool {
	if activeWork == nil || vaultPubKey.IsEmpty() {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(activeWork.VaultPubKey), vaultPubKey.String())
}

func (c *Client) setActiveXMRWorkItem(item *xmrWorkItem) {
	c.activeXMRWorkMu.Lock()
	defer c.activeXMRWorkMu.Unlock()
	if item == nil {
		c.activeXMRWorkItem = nil
		return
	}
	cp := *item
	c.activeXMRWorkItem = &cp
}

func (c *Client) getActiveXMRWorkItem() *xmrWorkItem {
	c.activeXMRWorkMu.Lock()
	defer c.activeXMRWorkMu.Unlock()
	if c.activeXMRWorkItem == nil {
		return nil
	}
	cp := *c.activeXMRWorkItem
	return &cp
}

func xmrVaultAllowsStandbyOrigination(status ttypes.VaultStatus) bool {
	return status == ttypes.VaultStatus_RetiringVault || status == ttypes.VaultStatus_InactiveVault
}

func (c *Client) xmrLocalCapability(members []string, syncState xmrSyncState, vaultStatus ttypes.VaultStatus) (xmrLocalCapability, error) {
	capability := xmrLocalCapability{
		NodeStatus: ttypes.NodeStatus_Unknown,
		Role:       xmrNodeRoleUnknown,
		SyncState:  syncState,
	}
	if c.thorchainBridge == nil {
		return capability, nil
	}

	nodeStatus, err := c.thorchainBridge.FetchNodeStatus()
	if err != nil {
		return capability, fmt.Errorf("fail to resolve local thor node status: %w", err)
	}
	capability.NodeStatus = nodeStatus
	capability.IsVaultSigner = c.localNodeIsVaultSigner(members)

	switch nodeStatus {
	case ttypes.NodeStatus_Active:
		capability.CanAttest = true
		if capability.IsVaultSigner {
			capability.Role = xmrNodeRoleActiveSigner
			capability.CanOriginate = syncState == xmrSyncStateAtTip
		} else {
			capability.Role = xmrNodeRoleActiveWatchOnly
		}
	case ttypes.NodeStatus_Standby:
		if capability.IsVaultSigner && xmrVaultAllowsStandbyOrigination(vaultStatus) {
			capability.Role = xmrNodeRoleStandbyVaultSigner
			capability.CanOriginate = syncState == xmrSyncStateAtTip
		} else {
			capability.Role = xmrNodeRoleStandbyWatchOnly
		}
	default:
		if capability.IsVaultSigner {
			capability.Role = xmrNodeRoleActiveSigner
		} else {
			capability.Role = xmrNodeRoleStandbyWatchOnly
		}
	}

	return capability, nil
}

func (c *Client) xmrLocalSyncState(states []xmrFrostVaultStateEntry) (xmrSyncState, string, error) {
	return c.xmrLocalSyncStateForVault(states, common.EmptyPubKey)
}

func (c *Client) xmrLocalSyncStateForVault(states []xmrFrostVaultStateEntry, target common.PubKey) (xmrSyncState, string, error) {
	if c.frostSigner == nil {
		return xmrSyncStateSyncingToTip, "xmr frost signer is not initialized", nil
	}
	if states == nil {
		stateResp, err := c.frostSigner.ListVaultStates()
		if err != nil && !isFrostHTTPStatus(err, http.StatusNotFound) {
			return xmrSyncStateSyncingToTip, "failed to load xmr vault states", err
		}
		states = stateResp.Vaults
	}
	targetReady := false
	for _, state := range states {
		if !state.Rescan {
			if !target.IsEmpty() && strings.TrimSpace(state.VaultPubKey) == target.String() {
				targetReady = true
			}
			continue
		}
		if !target.IsEmpty() && strings.TrimSpace(state.VaultPubKey) != target.String() {
			continue
		}
		return xmrSyncStateSyncingToTip, fmt.Sprintf("vault %s is syncing_to_tip", strings.TrimSpace(state.VaultPubKey)), nil
	}
	if targetReady {
		if pending, reason, err := c.xmrHasPendingReplayWorkForVault(target); pending || err != nil {
			if err != nil {
				return xmrSyncStateSyncingToTip, "failed to inspect xmr replay work", err
			}
			return xmrSyncStateSyncingToTip, reason, nil
		}
		return xmrSyncStateAtTip, "", nil
	}
	if c.scanner == nil || c.scanner.frostSigner == nil || c.scanner.bridge == nil {
		if pending, reason, err := c.xmrHasPendingReplayWorkForVault(target); pending || err != nil {
			if err != nil {
				return xmrSyncStateSyncingToTip, "failed to inspect xmr replay work", err
			}
			return xmrSyncStateSyncingToTip, reason, nil
		}
		return xmrSyncStateAtTip, "", nil
	}
	if c.blockScanner != nil && !c.blockScanner.IsHealthy() {
		return xmrSyncStateSyncingToTip, "xmr scanner is unhealthy or catching up", nil
	}

	scannerHeight := c.scanner.CurrentHeight()
	chainHeight, err := c.GetHeight()
	if err != nil {
		return xmrSyncStateSyncingToTip, "failed to query xmr chain tip", err
	}
	const xmrScannerTipToleranceBlocks = 3
	if scannerHeight <= 0 || chainHeight <= 0 || chainHeight-scannerHeight > xmrScannerTipToleranceBlocks {
		return xmrSyncStateSyncingToTip, fmt.Sprintf("xmr scanner is not at tip (scan=%d tip=%d)", scannerHeight, chainHeight), nil
	}

	if pending, reason, err := c.xmrHasPendingReplayWorkForVault(target); pending || err != nil {
		if err != nil {
			return xmrSyncStateSyncingToTip, "failed to inspect xmr replay work", err
		}
		return xmrSyncStateSyncingToTip, reason, nil
	}

	return xmrSyncStateAtTip, "", nil
}

func xmrKeyImageProofCacheKey(vaultPubKey common.PubKey, txHash string, outputs []xmrFrostOutputEntry) string {
	var b strings.Builder
	b.WriteString(vaultPubKey.String())
	b.WriteString("|")
	b.WriteString(normalizeTxID(txHash))
	for _, output := range outputs {
		b.WriteString("|")
		b.WriteString(strconv.FormatUint(uint64(output.OutputIndex), 10))
	}
	return b.String()
}

func cloneXMRKeyImageProofs(proofs []*common.XmrKeyImageProof) []*common.XmrKeyImageProof {
	if len(proofs) == 0 {
		return nil
	}
	cloned := make([]*common.XmrKeyImageProof, 0, len(proofs))
	for _, proof := range proofs {
		if proof == nil {
			continue
		}
		dup := *proof
		cloned = append(cloned, &dup)
	}
	return cloned
}

func (c *Client) getCachedXMRKeyImageProofs(vaultPubKey common.PubKey, txHash string, outputs []xmrFrostOutputEntry) ([]*common.XmrKeyImageProof, bool) {
	cacheKey := xmrKeyImageProofCacheKey(vaultPubKey, txHash, outputs)
	c.keyImageProofCacheMu.Lock()
	defer c.keyImageProofCacheMu.Unlock()
	if c.keyImageProofCache == nil {
		return nil, false
	}
	entry, ok := c.keyImageProofCache[cacheKey]
	if !ok {
		return nil, false
	}
	if time.Since(entry.cachedAt) > 15*time.Minute {
		delete(c.keyImageProofCache, cacheKey)
		return nil, false
	}
	return cloneXMRKeyImageProofs(entry.proofs), true
}

func (c *Client) putCachedXMRKeyImageProofs(vaultPubKey common.PubKey, txHash string, outputs []xmrFrostOutputEntry, proofs []*common.XmrKeyImageProof) {
	cacheKey := xmrKeyImageProofCacheKey(vaultPubKey, txHash, outputs)
	c.keyImageProofCacheMu.Lock()
	defer c.keyImageProofCacheMu.Unlock()
	if c.keyImageProofCache == nil {
		c.keyImageProofCache = make(map[string]xmrKeyImageProofCacheEntry)
	}
	c.keyImageProofCache[cacheKey] = xmrKeyImageProofCacheEntry{
		proofs:   cloneXMRKeyImageProofs(proofs),
		cachedAt: time.Now(),
	}
}

func isTransientXMRKeyImageCeremonyError(err error) bool {
	if err == nil {
		return false
	}
	return isTransientXMRKeyImageCeremonyText(err.Error())
}

func isTransientXMRKeyImageCeremonyText(errText string) bool {
	return isTransientXMRFrostCoordinationText(errText)
}

func isTransientXMRFrostCoordinationText(errText string) bool {
	return strings.Contains(errText, "leader not reachable") ||
		strings.Contains(errText, "fail to join party, timeout")
}

var errXMRKeyImageNotActiveSigner = errors.New("xmr key-image local signer not active")
var errXMROutgoingAuditIncomplete = errors.New("xmr outgoing audit incomplete")
var errXMRFinalizeWorkRetryBackoff = errors.New("xmr finalized observation work retry backoff active")

const xmrWorkRetryMinBackoff = 60 * time.Second
const xmrMissingOutboundTxRetryLimit = 3

var xmrMissingOutboundTxRetryBackoff = []time.Duration{
	10 * time.Second,
	30 * time.Second,
}

func isTransientXMROutboundValidationError(err error) bool {
	if err == nil {
		return false
	}
	errText := strings.ToLower(err.Error())
	if isFrostHTTPStatus(err, http.StatusNotFound) {
		return false
	}
	switch {
	case isFrostHTTPStatus(err, http.StatusRequestTimeout),
		isFrostHTTPStatus(err, http.StatusTooManyRequests),
		isFrostHTTPStatus(err, http.StatusBadGateway),
		isFrostHTTPStatus(err, http.StatusServiceUnavailable),
		isFrostHTTPStatus(err, http.StatusGatewayTimeout):
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	return isTransientXMROutboundValidationText(errText)
}

func isTransientXMROutboundValidationText(errText string) bool {
	errText = strings.ToLower(strings.TrimSpace(errText))
	return (strings.Contains(errText, "transaction") && strings.Contains(errText, "not found")) ||
		strings.Contains(errText, "connection refused") ||
		strings.Contains(errText, "connection reset") ||
		strings.Contains(errText, "i/o timeout") ||
		strings.Contains(errText, "context deadline exceeded") ||
		strings.Contains(errText, "server closed idle connection") ||
		strings.Contains(errText, "unexpected eof")
}

func isMissingXMROutboundValidationTx(err error) bool {
	if err == nil || !isFrostHTTPStatus(err, http.StatusNotFound) {
		return false
	}
	return isMissingXMROutboundValidationText(err.Error())
}

func isMissingXMROutboundValidationText(errText string) bool {
	errText = strings.ToLower(errText)
	return strings.Contains(errText, "transaction") && strings.Contains(errText, "not found")
}

func (c *Client) Start(
	globalTxsQueue chan stypes.TxIn,
	globalErrataQueue chan stypes.ErrataBlock,
	globalSolvencyQueue chan stypes.Solvency,
	globalNetworkFeeQueue chan common.NetworkFee,
) {
	c.ensureConsolidationState()
	if err := c.syncXMRState(); err != nil {
		c.logger.Warn().Err(err).Msg("xmr startup state sync failed; continuing and retrying in background")
	}
	c.globalTxsQueue = globalTxsQueue
	c.globalSolvencyQueue = globalSolvencyQueue
	c.scanner.SetErrataQueue(globalErrataQueue)
	c.blockScanner.Start(globalTxsQueue, globalNetworkFeeQueue)
	c.wg.Add(1)
	go runners.SolvencyCheckRunner(c.GetChain(), c, c.thorchainBridge, c.stopchan, c.wg, constants.ThorchainBlockTime)
	c.wg.Add(1)
	go c.consolidationRunner()
	c.wg.Add(1)
	go c.xmrStateSyncRunner()
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.restoreFrostKeysFromRecoveryFiles()
	}()
}

func (c *Client) restoreFrostKeysFromRecoveryFiles() {
	if c.frostSigner == nil {
		return
	}
	pattern := filepath.Join(app.DefaultNodeHome, "frost-recovery-*.hex")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return
	}
	for _, path := range matches {
		base := filepath.Base(path)
		vaultPubKeyStr := strings.TrimPrefix(base, "frost-recovery-")
		vaultPubKeyStr = strings.TrimSuffix(vaultPubKeyStr, ".hex")

		vaultPubKey, parseErr := common.NewPubKey(vaultPubKeyStr)
		if parseErr != nil {
			c.logger.Err(parseErr).Str("file", base).Msg("xmr recovery: invalid vault pubkey in filename")
			continue
		}

		keyHex, readErr := os.ReadFile(path)
		if readErr != nil {
			c.logger.Err(readErr).Str("file", base).Msg("xmr recovery: failed to read recovery file")
			continue
		}

		restoreErr := c.RestoreFrostKeyshare(vaultPubKey, string(keyHex))
		if restoreErr != nil {
			c.logger.Err(restoreErr).Str("vault", vaultPubKeyStr).Msg("xmr recovery: failed to restore frost keyshare")
			continue
		}

		_ = os.Remove(path)
		c.logger.Info().Str("vault", vaultPubKeyStr).Msg("xmr recovery: frost keyshare restored and recovery file removed")
	}
}

func (c *Client) xmrStateSyncRunner() {
	defer c.wg.Done()

	if err := c.syncXMRState(); err != nil {
		c.logger.Warn().Err(err).Msg("xmr state sync failed; retrying on next tick")
	}

	ticker := time.NewTicker(constants.ThorchainBlockTime * 10)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopchan:
			return
		case <-ticker.C:
			if err := c.syncXMRState(); err != nil {
				c.logger.Warn().Err(err).Msg("xmr state sync failed; retrying on next tick")
			}
		}
	}
}

func (c *Client) syncXMRState() error {
	if c.thorchainBridge == nil || c.frostSigner == nil {
		return nil
	}

	c.stateSyncMu.Lock()
	defer c.stateSyncMu.Unlock()

	vaults, err := c.trackedXMRVaultsForKeyImageCatchup()
	if err != nil {
		return fmt.Errorf("failed to get tracked xmr vaults: %w", err)
	}

	vaultStates, err := c.syncTrackedVaultRegistrations(vaults)
	if err != nil {
		return err
	}
	if hasPendingVaultRescans(vaultStates) {
		updated, progressErr := c.drivePendingScannerRescans(vaultStates)
		if progressErr != nil {
			return progressErr
		}
		vaultStates = updated
		if hasPendingVaultRescans(vaultStates) {
			c.logger.Info().
				Int("pending_vaults", countPendingVaultRescans(vaultStates)).
				Msg("xmr state sync: scanner rescans still pending; deferring metadata catchup until next tick")
			return nil
		}
	}

	c.catchupKeyImagesForVaults(vaults)
	c.driveLocalKeyImageBacklogForVaults(vaults)
	c.logXMRVaultLocalStatuses(vaultStates)
	return nil
}

func (c *Client) catchupKeyImages() {
	if c.thorchainBridge == nil || c.frostSigner == nil {
		return
	}
	vaults, err := c.trackedXMRVaultsForKeyImageCatchup()
	if err != nil {
		c.logger.Err(err).Msg("xmr key-image catchup: failed to get tracked xmr vaults")
		return
	}
	c.catchupKeyImagesForVaults(vaults)
}

func (c *Client) catchupKeyImagesForVaults(vaults ttypes.Vaults) {
	for _, vault := range vaults {
		vaultPubKey := keyImageCatchupVaultPubKey(vault)
		if vaultPubKey.IsEmpty() {
			c.clearKeyImageCatchupState(vaultPubKey.String())
			continue
		}

		refs := makeApplyKeyImageRefs(vault.KeyImagesXmr)
		batch, shouldApply := c.nextKeyImageCatchupBatch(vaultPubKey.String(), refs)
		if !shouldApply {
			needsReplay, verifyErr := c.keyImageCatchupNeedsReplay(vaultPubKey.String(), refs)
			if verifyErr != nil {
				c.logger.Debug().
					Err(verifyErr).
					Str("vault", vaultPubKey.String()).
					Msg("xmr key-image catchup: failed to verify signer state, forcing replay")
				needsReplay = true
			}
			if needsReplay {
				c.clearKeyImageCatchupState(vaultPubKey.String())
				batch = cloneApplyKeyImageRefs(refs)
				shouldApply = true
			}
		}
		if !shouldApply {
			continue
		}

		result, applyErr := c.frostSigner.ApplyKeyImages(vaultPubKey.String(), batch, true)
		if applyErr != nil {
			c.logger.Err(applyErr).
				Str("vault", vaultPubKey.String()).
				Int("key_images", len(batch)).
				Msg("xmr key-image catchup: failed to apply key-images")
			continue
		}
		c.recordKeyImageCatchup(vaultPubKey.String(), refs, result.NotFound > 0)
		c.logger.Info().
			Str("vault", vaultPubKey.String()).
			Int("applied", result.Applied).
			Int("not_found", result.NotFound).
			Int("sent", len(batch)).
			Int("total", len(refs)).
			Msg("xmr key-image catchup: applied key-images from thorchain")
		c.signalXMRKeyImageBacklogChanged()
	}
}

func (c *Client) driveLocalKeyImageBacklogForVaults(vaults ttypes.Vaults) {
	if c.frostSigner == nil || c.tssServer == nil || c.thorchainBridge == nil {
		return
	}
	syncState, syncReason, err := c.xmrLocalSyncState(nil)
	if err != nil {
		c.logger.Debug().Err(err).Msg("xmr key-image backlog: failed to resolve local sync state")
		return
	}
	if syncState != xmrSyncStateAtTip {
		c.logger.Info().
			Str("sync_state", string(syncState)).
			Str("reason", syncReason).
			Msg("xmr key-image backlog: deferring origination while node is syncing_to_tip")
		return
	}

	version, err := c.thorchainBridge.GetThorchainVersion()
	if err != nil {
		c.logger.Debug().Err(err).Msg("xmr key-image backlog: failed to resolve thornode version")
		return
	}

	for _, vault := range vaults {
		c.driveLocalKeyImageBacklogForVault(vault, version.String(), syncState, syncReason)
	}
}

func (c *Client) driveLocalKeyImageBacklogForVault(vault ttypes.Vault, version string, syncState xmrSyncState, syncReason string) {
	vaultPubKey := keyImageCatchupVaultPubKey(vault)
	if vaultPubKey.IsEmpty() {
		return
	}

	ceremonyCtx, err := c.resolveXMRKeyImageCeremonyContext(vaultPubKey)
	if err != nil {
		c.logger.Debug().
			Err(err).
			Str("vault", vaultPubKey.String()).
			Msg("xmr key-image backlog: failed to resolve ceremony context")
		return
	}

	capability, capErr := c.xmrLocalCapability(ceremonyCtx.members, syncState, ceremonyCtx.vaultStatus)
	if capErr != nil {
		c.logger.Debug().
			Err(capErr).
			Str("vault", ceremonyCtx.vaultPubKey.String()).
			Msg("xmr key-image backlog: failed to resolve local capability")
		return
	}
	if !capability.CanOriginate {
		c.logger.Debug().
			Str("vault", ceremonyCtx.vaultPubKey.String()).
			Str("role", string(capability.Role)).
			Str("sync_state", string(capability.SyncState)).
			Str("reason", syncReason).
			Msg("xmr key-image backlog: local node is not allowed to originate work")
		return
	}

	if readiness, ok := c.tssServer.(xmrKeyImagePeerReadiness); ok {
		missingPeers, readinessErr := readiness.MissingPeerAddresses(ceremonyCtx.members)
		if readinessErr != nil {
			c.logger.Debug().
				Err(readinessErr).
				Str("vault", ceremonyCtx.vaultPubKey.String()).
				Msg("xmr key-image backlog: failed to inspect signer peer readiness")
			return
		}
		if len(missingPeers) > 0 {
			c.logger.Info().
				Str("vault", ceremonyCtx.vaultPubKey.String()).
				Strs("missing_signer_pub_keys", missingPeers).
				Msg("xmr key-image backlog: waiting for signer peer discovery")
			return
		}
	}

	allOutputs, err := c.frostSigner.GetAllOutputs(ceremonyCtx.vaultPubKey.String())
	if err != nil {
		c.logger.Debug().
			Err(err).
			Str("vault", ceremonyCtx.vaultPubKey.String()).
			Msg("xmr key-image backlog: failed to query tracked outputs")
		return
	}

	backlogOutputs := xmrOutputsMissingKeyImages(allOutputs)
	if len(backlogOutputs) == 0 {
		backlogOutputs = xmrOutputsWithLocalKeyImagesMissingFromVault(allOutputs, vault.KeyImagesXmr)
	}
	if len(backlogOutputs) == 0 {
		return
	}

	backlogHeadTxHash := normalizeTxID(backlogOutputs[0].TxHash)
	txOutputs := filterVaultOutputsForTx(allOutputs, backlogHeadTxHash)
	keyImageOutputs := xmrOutputsRequiringKeyImages(txOutputs)
	pendingOutputs := xmrOutputsMissingKeyImages(keyImageOutputs)
	if len(txOutputs) == 0 {
		return
	}
	if len(keyImageOutputs) == 0 {
		return
	}
	if len(pendingOutputs) == 0 && !xmrTxOutputsNeedKeyImageAttestation(keyImageOutputs, vault.KeyImagesXmr) {
		return
	}
	if len(pendingOutputs) != 0 && len(pendingOutputs) != len(keyImageOutputs) {
		c.logger.Info().
			Str("vault", ceremonyCtx.vaultPubKey.String()).
			Str("tx_hash", backlogHeadTxHash).
			Int("tracked_output_count", len(txOutputs)).
			Int("key_image_output_count", len(keyImageOutputs)).
			Int("missing_key_image_output_count", len(pendingOutputs)).
			Msg("xmr key-image backlog: waiting for coherent local proof state")
		return
	}

	if cachedProofs, ok := c.getCachedXMRKeyImageProofs(ceremonyCtx.vaultPubKey, backlogHeadTxHash, keyImageOutputs); ok && len(cachedProofs) == len(keyImageOutputs) {
		c.queueXMRKeyImageBacklogObservation(ceremonyCtx.vaultPubKey, backlogHeadTxHash, int64(backlogOutputs[0].Height), cachedProofs)
		return
	}

	finaliseHeight := c.resolveXMRKeyImageAnchorHeight(ceremonyCtx.vaultPubKey, backlogHeadTxHash, int64(backlogOutputs[0].Height))
	c.logger.Info().
		Str("vault", ceremonyCtx.vaultPubKey.String()).
		Str("tx_hash", backlogHeadTxHash).
		Int("pending_key_image_output_count", len(pendingOutputs)).
		Int64("requested_finalise_height", finaliseHeight).
		Msg("driving local xmr key-image backlog head")

	ceremonyOutputs := pendingOutputs
	if len(ceremonyOutputs) == 0 {
		ceremonyOutputs = keyImageOutputs
	}
	proofs, err := c.generateObservedTxKeyImages(
		ceremonyCtx.vaultPubKey,
		backlogHeadTxHash,
		ceremonyOutputs,
		ceremonyCtx.members,
		finaliseHeight,
		xmrWorkKindReplayKeyImage,
		xmrFinalizePhaseKeyImage,
		version,
	)
	if err != nil {
		switch {
		case errors.Is(err, errXMRFinalizeWorkRetryBackoff):
			c.logger.Debug().
				Err(err).
				Str("vault", ceremonyCtx.vaultPubKey.String()).
				Str("tx_hash", backlogHeadTxHash).
				Msg("xmr key-image backlog: waiting for retry backoff")
		case errors.Is(err, errXMRKeyImageNotActiveSigner):
			c.logger.Info().
				Err(err).
				Str("vault", ceremonyCtx.vaultPubKey.String()).
				Str("tx_hash", backlogHeadTxHash).
				Msg("xmr key-image backlog: waiting for active signer subset")
		case isTransientXMRKeyImageCeremonyError(err):
			c.logger.Info().
				Err(err).
				Str("vault", ceremonyCtx.vaultPubKey.String()).
				Str("tx_hash", backlogHeadTxHash).
				Msg("xmr key-image backlog: transient ceremony failure; will retry")
		default:
			c.logger.Warn().
				Err(err).
				Str("vault", ceremonyCtx.vaultPubKey.String()).
				Str("tx_hash", backlogHeadTxHash).
				Msg("xmr key-image backlog: failed to generate local proof set")
		}
		return
	}

	c.putCachedXMRKeyImageProofs(ceremonyCtx.vaultPubKey, backlogHeadTxHash, keyImageOutputs, proofs)
	c.queueXMRKeyImageBacklogObservation(ceremonyCtx.vaultPubKey, backlogHeadTxHash, int64(backlogOutputs[0].Height), proofs)
	c.logger.Info().
		Str("vault", ceremonyCtx.vaultPubKey.String()).
		Str("tx_hash", backlogHeadTxHash).
		Int("proof_count", len(proofs)).
		Msg("xmr key-image backlog: cached locally generated proof set")
}

func (c *Client) queueXMRKeyImageBacklogObservation(vaultPubKey common.PubKey, txHash string, fallbackHeight int64, proofs []*common.XmrKeyImageProof) {
	item, err := c.xmrObservedTxInItemFromVoter(vaultPubKey, txHash, fallbackHeight)
	if err != nil {
		c.logger.Debug().
			Err(err).
			Str("vault", vaultPubKey.String()).
			Str("tx_hash", normalizeTxID(txHash)).
			Msg("xmr key-image backlog: cannot replay observation for local proof set")
		return
	}
	item.XmrKeyImages = cloneXMRKeyImageProofs(proofs)
	replay := stypes.TxIn{
		Chain:   common.XMRChain,
		TxArray: []*stypes.TxInItem{item},
	}
	if c.globalTxsQueue != nil {
		select {
		case c.globalTxsQueue <- replay:
			c.logger.Info().
				Str("vault", vaultPubKey.String()).
				Str("tx_hash", normalizeTxID(txHash)).
				Int("proof_count", len(item.XmrKeyImages)).
				Msg("xmr key-image backlog: submitted observation replay for local proof set")
			return
		default:
			c.logger.Debug().
				Str("vault", vaultPubKey.String()).
				Str("tx_hash", normalizeTxID(txHash)).
				Msg("xmr key-image backlog: observation queue full")
		}
	}
	if c.scanner != nil {
		c.scanner.queueReplayTxInItems(replay.TxArray)
		c.logger.Info().
			Str("vault", vaultPubKey.String()).
			Str("tx_hash", normalizeTxID(txHash)).
			Int("proof_count", len(item.XmrKeyImages)).
			Msg("xmr key-image backlog: queued observation replay for local proof set")
		return
	}
}

func (c *Client) xmrObservedTxInItemFromVoter(vaultPubKey common.PubKey, txHash string, fallbackHeight int64) (*stypes.TxInItem, error) {
	if c.thorchainBridge == nil {
		return nil, fmt.Errorf("thorchain bridge is not configured")
	}
	normalizedTxHash := normalizeTxID(txHash)
	voter, err := c.thorchainBridge.GetObservedTxVoter(normalizedTxHash)
	if err != nil {
		return nil, fmt.Errorf("fail to get observed tx voter: %w", err)
	}
	observed, err := selectObservedXMRForVault(voter, normalizedTxHash, vaultPubKey)
	if err != nil {
		return nil, err
	}
	height := observed.BlockHeight
	if height <= 0 {
		height = fallbackHeight
	}
	if height <= 0 {
		height = voter.FinalisedHeight
	}
	if height <= 0 {
		height = voter.Height
	}

	item := stypes.NewTxInItem(
		height,
		observed.Tx.ID.String(),
		observed.Tx.Memo,
		observed.Tx.FromAddress.String(),
		observed.Tx.ToAddress.String(),
		observed.Tx.Coins,
		observed.Tx.Gas,
		observed.ObservedPubKey,
		observed.Aggregator,
		observed.AggregatorTarget,
		observed.AggregatorTargetLimit,
	)
	if item == nil {
		return nil, fmt.Errorf("failed to build observed tx item")
	}
	return item, nil
}

func (c *Client) refreshTrackedVaultInventory() {
	if c.thorchainBridge == nil || c.frostSigner == nil {
		return
	}
	vaults, err := c.trackedXMRVaultsForKeyImageCatchup()
	if err != nil {
		c.logger.Err(err).Msg("xmr state sync: failed to get tracked xmr vaults")
		return
	}
	if _, err := c.syncTrackedVaultRegistrations(vaults); err != nil {
		c.logger.Err(err).Msg("xmr state sync: failed to refresh signer inventory from thorchain")
	}
}

func (c *Client) outgoingInventoryCanonicalOutboundMeta(vaultPubKey common.PubKey, txHash, memoText string) (xmrFrostOutboundMeta, bool, error) {
	parsedMemo, err := memo.ParseMemo(common.LatestVersion, memoText)
	if err != nil || !parsedMemo.IsOutbound() || parsedMemo.GetTxID().IsEmpty() {
		return xmrFrostOutboundMeta{}, false, nil
	}

	status, err := c.thorchainBridge.GetTxStatus(parsedMemo.GetTxID().String())
	if err != nil {
		return xmrFrostOutboundMeta{}, false, fmt.Errorf(
			"fail to get tx status for outbound parent %s: %w",
			parsedMemo.GetTxID().String(),
			err,
		)
	}

	meta, found, err := c.outgoingInventoryMetaFromTxStatus(vaultPubKey, txHash, status)
	if err != nil {
		return xmrFrostOutboundMeta{}, false, err
	}
	return meta, found, nil
}

func (c *Client) outgoingInventoryMetaFromTxStatus(
	vaultPubKey common.PubKey,
	txHash string,
	status *ttypes.QueryTxStatusResponse,
) (xmrFrostOutboundMeta, bool, error) {
	if status == nil {
		return xmrFrostOutboundMeta{}, false, nil
	}

	vaultAddress := strings.TrimSpace(c.getVaultAddress(vaultPubKey))
	normalizedHash := strings.TrimSpace(txHash)
	for _, outTx := range status.OutTxs {
		if !outTx.Chain.Equals(common.XMRChain) {
			continue
		}
		if !strings.EqualFold(outTx.ID.String(), normalizedHash) {
			continue
		}
		if outTx.ToAddress.IsEmpty() {
			return xmrFrostOutboundMeta{}, false, fmt.Errorf(
				"tx status outbound %s is missing destination address",
				txHash,
			)
		}

		amountAtomic, err := xmrThorchainAmountToAtomic(outTx.Coins.GetCoin(common.XMRAsset).Amount)
		if err != nil {
			return xmrFrostOutboundMeta{}, false, fmt.Errorf(
				"fail to convert xmr tx status outbound amount to atomic for %s: %w",
				txHash,
				err,
			)
		}

		var feeAtomic uint64
		gasCoin := outTx.Gas.ToCoins().GetCoin(common.XMRAsset)
		if !gasCoin.IsEmpty() {
			feeAtomic, err = xmrThorchainAmountToAtomic(gasCoin.Amount)
			if err != nil {
				return xmrFrostOutboundMeta{}, false, fmt.Errorf(
					"fail to convert xmr tx status outbound fee to atomic for %s: %w",
					txHash,
					err,
				)
			}
		}

		fromAddress := outTx.FromAddress.String()
		if vaultAddress != "" {
			fromAddress = vaultAddress
		}
		if fromAddress == "" {
			fromAddress = outTx.FromAddress.String()
		}

		return xmrFrostOutboundMeta{
			FromAddress: fromAddress,
			ToAddress:   outTx.ToAddress.String(),
			Amount:      amountAtomic,
			Fee:         feeAtomic,
			Memo:        outTx.Memo,
		}, true, nil
	}

	return xmrFrostOutboundMeta{}, false, nil
}

func selectObservedOutboundForVault(
	voter *ttypes.QueryObservedTxVoter,
	txHash string,
	vaultPubKey common.PubKey,
) (*ttypes.QueryObservedTx, error) {
	if voter == nil {
		return nil, fmt.Errorf("observed tx voter is nil for %s", txHash)
	}

	candidates := make([]*ttypes.QueryObservedTx, 0, len(voter.Txs)+1)
	if !voter.Tx.Tx.IsEmpty() {
		candidates = append(candidates, &voter.Tx)
	}
	for i := range voter.Txs {
		candidates = append(candidates, &voter.Txs[i])
	}

	var fallback *ttypes.QueryObservedTx
	for _, candidate := range candidates {
		if candidate == nil || candidate.Tx.IsEmpty() {
			continue
		}
		if !candidate.Tx.Chain.Equals(common.XMRChain) {
			continue
		}
		if !strings.EqualFold(candidate.Tx.ID.String(), txHash) {
			continue
		}
		if candidate.ObservedPubKey.Equals(vaultPubKey) {
			return candidate, nil
		}
		if fallback == nil {
			fallback = candidate
		}
	}

	if fallback != nil {
		// The caller only needs tx-level metadata (from/to/amount/fee/memo),
		// which is identical regardless of which vault's scanner recorded
		// the observation. A MIGRATE outbound is typically observed against
		// the RECEIVING vault (as an inbound) rather than the signing vault
		// — rejecting here would abort the entire syncXMRState loop for the
		// signing vault, preventing any inventory push (including unrelated
		// key-images) from ever propagating to sidecars.
		return fallback, nil
	}
	return nil, fmt.Errorf("observed tx voter %s has no xmr outbound observation", txHash)
}

func selectObservedXMRForVault(
	voter *ttypes.QueryObservedTxVoter,
	txHash string,
	vaultPubKey common.PubKey,
) (*ttypes.QueryObservedTx, error) {
	if voter == nil {
		return nil, fmt.Errorf("observed tx voter is nil for %s", txHash)
	}

	normalizedTxHash := normalizeTxID(txHash)
	candidates := make([]*ttypes.QueryObservedTx, 0, len(voter.Txs)+1)
	if !voter.Tx.Tx.IsEmpty() {
		candidates = append(candidates, &voter.Tx)
	}
	for i := range voter.Txs {
		candidates = append(candidates, &voter.Txs[i])
	}

	for _, candidate := range candidates {
		if candidate == nil || candidate.Tx.IsEmpty() {
			continue
		}
		if !candidate.Tx.Chain.Equals(common.XMRChain) {
			continue
		}
		if !strings.EqualFold(candidate.Tx.ID.String(), normalizedTxHash) {
			continue
		}
		if candidate.ObservedPubKey.Equals(vaultPubKey) {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("observed tx voter %s has no xmr observation for vault %s", txHash, vaultPubKey)
}

func (c *Client) trackedXMRVaultsForKeyImageCatchup() (ttypes.Vaults, error) {
	if c.scanner != nil && c.scanner.bridge != nil {
		return c.scanner.trackedXMRVaults()
	}
	if c.thorchainBridge == nil {
		return nil, nil
	}

	return asgardXMRVaults(c.thorchainBridge)
}

func asgardXMRVaults(bridge thorclient.ThorchainBridge) (ttypes.Vaults, error) {
	vaults, err := bridge.GetAsgards()
	if err != nil {
		return nil, fmt.Errorf("fail to get asgards: %w", err)
	}

	lookup := make(map[string]ttypes.Vault)
	for _, vault := range vaults {
		vaultPubKey := keyImageCatchupVaultPubKey(vault)
		if vaultPubKey.IsEmpty() {
			continue
		}
		lookup[vaultPubKey.String()] = vault
	}

	return mapVaults(lookup), nil
}

func keyImageCatchupVaultPubKey(vault ttypes.Vault) common.PubKey {
	vaultPubKey := vault.PubKeyFrost
	if vaultPubKey.IsEmpty() {
		vaultPubKey = vault.PubKeyEddsa
	}
	return vaultPubKey
}

func xmrVaultInventoryFingerprint(vault ttypes.Vault) string {
	var builder strings.Builder
	builder.WriteString(keyImageCatchupVaultPubKey(vault).String())
	builder.WriteString("|")
	builder.WriteString(strconv.FormatInt(vault.VaultCreationHeightXmr, 10))

	keyImages := makeApplyKeyImageRefs(vault.KeyImagesXmr)
	sort.Slice(keyImages, func(i, j int) bool {
		if keyImages[i].TxHash == keyImages[j].TxHash {
			if keyImages[i].OutputIndex == keyImages[j].OutputIndex {
				return keyImages[i].KeyImage < keyImages[j].KeyImage
			}
			return keyImages[i].OutputIndex < keyImages[j].OutputIndex
		}
		return keyImages[i].TxHash < keyImages[j].TxHash
	})
	for _, ref := range keyImages {
		builder.WriteString("|ki:")
		builder.WriteString(strings.ToLower(strings.TrimSpace(ref.TxHash)))
		builder.WriteString(":")
		builder.WriteString(strconv.FormatUint(uint64(ref.OutputIndex), 10))
		builder.WriteString(":")
		builder.WriteString(strings.ToLower(strings.TrimSpace(ref.KeyImage)))
	}

	sum := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("%x", sum[:])
}

func (c *Client) trackedVaultSyncFingerprint(vaultPubKey string) (string, bool) {
	c.trackedVaultSyncMu.Lock()
	defer c.trackedVaultSyncMu.Unlock()
	fingerprint, ok := c.trackedVaultSyncState[vaultPubKey]
	return fingerprint, ok
}

func (c *Client) replaceTrackedVaultSyncFingerprints(next map[string]string) {
	c.trackedVaultSyncMu.Lock()
	defer c.trackedVaultSyncMu.Unlock()
	c.trackedVaultSyncState = next
}

func hasPendingVaultRescans(states []xmrFrostVaultStateEntry) bool {
	return countPendingVaultRescans(states) > 0
}

func countPendingVaultRescans(states []xmrFrostVaultStateEntry) int {
	pending := 0
	for _, state := range states {
		if state.Rescan {
			pending++
		}
	}
	return pending
}

func mapVaultRescanHeights(states []xmrFrostVaultStateEntry) map[string]uint64 {
	out := make(map[string]uint64, len(states))
	for _, state := range states {
		if state.ScanHeight != nil {
			out[state.VaultPubKey] = *state.ScanHeight
			continue
		}
		out[state.VaultPubKey] = 0
	}
	return out
}

func vaultRescanProgressed(before, after []xmrFrostVaultStateEntry) bool {
	beforeHeights := mapVaultRescanHeights(before)
	for _, state := range after {
		if !state.Rescan {
			if _, ok := beforeHeights[state.VaultPubKey]; ok {
				return true
			}
			continue
		}
		if height, ok := beforeHeights[state.VaultPubKey]; ok && state.ScanHeight != nil && *state.ScanHeight > height {
			return true
		}
	}
	return false
}

func (c *Client) syncTrackedVaultRegistrations(vaults ttypes.Vaults) ([]xmrFrostVaultStateEntry, error) {
	stateAvailable := true
	stateResp, err := c.frostSigner.ListVaultStates()
	if err != nil {
		if isFrostHTTPStatus(err, http.StatusNotFound) {
			stateAvailable = false
			stateResp = xmrFrostVaultStatesResponse{}
		} else {
			return nil, fmt.Errorf("failed to load xmr sidecar vault states: %w", err)
		}
	}

	stateMap := make(map[string]xmrFrostVaultStateEntry, len(stateResp.Vaults))
	for _, state := range stateResp.Vaults {
		stateMap[strings.TrimSpace(state.VaultPubKey)] = state
	}

	needsRefresh := false
	nextFingerprints := make(map[string]string, len(vaults))
	for _, vault := range vaults {
		vaultPubKey := keyImageCatchupVaultPubKey(vault)
		if vaultPubKey.IsEmpty() {
			continue
		}

		fingerprint := xmrVaultInventoryFingerprint(vault)
		nextFingerprints[vaultPubKey.String()] = fingerprint

		stateEntry, registeredInState := stateMap[vaultPubKey.String()]
		replayPending := false
		if stateAvailable {
			inventoryMatches := registeredInState &&
				strings.EqualFold(strings.TrimSpace(stateEntry.InventoryFingerprint), fingerprint)
			if inventoryMatches {
				replayPending, err = c.watchedVaultInventoryReplayPending(vault, stateEntry)
				if err != nil {
					return nil, fmt.Errorf("failed to verify watched xmr vault inventory replay state for %s: %w", vaultPubKey.String(), err)
				}
			}
			if previous, ok := c.trackedVaultSyncFingerprint(vaultPubKey.String()); ok &&
				previous == fingerprint &&
				inventoryMatches &&
				!replayPending {
				continue
			}
		}

		if replayPending {
			c.clearKeyImageCatchupState(vaultPubKey.String())
			c.logger.Info().
				Str("vault", vaultPubKey.String()).
				Int("key_images", len(vault.KeyImagesXmr)).
				Msg("xmr state sync: forcing watched vault inventory reapply because local signer state is behind thorchain")
		}

		_, regErr := ensureFrostVaultRegisteredWithInventory(
			c.logger,
			c.frostSigner,
			c.chain,
			vaultPubKey,
			c.localVaultBootstrap,
			"bifrost_state_sync",
			vault.VaultCreationHeightXmr,
			keyImageRefsFromVault(vault),
			nil,
		)
		if regErr != nil {
			return nil, fmt.Errorf("failed to register vault %s: %w", vaultPubKey.String(), regErr)
		}
		needsRefresh = true
	}

	c.replaceTrackedVaultSyncFingerprints(nextFingerprints)

	if !needsRefresh {
		return stateResp.Vaults, nil
	}

	updated, err := c.frostSigner.ListVaultStates()
	if err != nil {
		if isFrostHTTPStatus(err, http.StatusNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to load xmr sidecar vault states: %w", err)
	}
	return updated.Vaults, nil
}

func (c *Client) drivePendingScannerRescans(states []xmrFrostVaultStateEntry) ([]xmrFrostVaultStateEntry, error) {
	if len(states) == 0 || !hasPendingVaultRescans(states) {
		return states, nil
	}

	const maxRescanBatchesPerTick = 32

	current := states
	reorgWindow := int64(0)
	if c.scanner != nil {
		reorgWindow = c.scanner.maxReorgRescan
	}

	for batch := 0; batch < maxRescanBatchesPerTick; batch++ {
		chainHeight, err := c.frostSigner.GetChainHeight()
		if err != nil {
			return current, fmt.Errorf("failed to query xmr chain height while driving rescans: %w", err)
		}
		if chainHeight < 0 {
			return current, nil
		}

		block, err := c.frostSigner.GetChainBlock(chainHeight)
		if err != nil {
			if errors.Is(err, ErrFrostUnavailableBlock) {
				return current, nil
			}
			return current, fmt.Errorf("failed to query xmr chain block %d while driving rescans: %w", chainHeight, err)
		}

		resp, err := c.frostSigner.SyncTransfersByHeight(
			chainHeight,
			chainHeight,
			block.Hash,
			block.PreviousHash,
			reorgWindow,
		)
		if err != nil {
			if isFrostScannerHashMismatchErr(err) {
				return current, nil
			}
			return current, fmt.Errorf("failed to drive xmr scanner rescan at tip %d: %w", chainHeight, err)
		}
		if len(resp.Incoming)+len(resp.Outgoing) > 0 {
			c.logger.Info().
				Int("incoming", len(resp.Incoming)).
				Int("outgoing", len(resp.Outgoing)).
				Int64("height", chainHeight).
				Msg("discarded XMR birthday-rescan observations after local reconciliation")
		}

		updated, err := c.frostSigner.ListVaultStates()
		if err != nil {
			return current, fmt.Errorf("failed to refresh xmr sidecar vault states after rescan batch: %w", err)
		}
		current = updated.Vaults
		if !hasPendingVaultRescans(current) {
			return current, nil
		}
		if !vaultRescanProgressed(states, current) {
			return current, nil
		}
		states = current
	}

	return current, nil
}

func (c *Client) watchedVaultInventoryReplayPending(vault ttypes.Vault, stateEntry xmrFrostVaultStateEntry) (bool, error) {
	if c == nil || c.frostSigner == nil {
		return false, nil
	}

	vaultPubKey := keyImageCatchupVaultPubKey(vault)
	if vaultPubKey.IsEmpty() {
		return false, nil
	}

	if stateEntry.RegisteredKeyImageCount < len(vault.KeyImagesXmr) {
		return true, nil
	}

	keyImageRefs := makeApplyKeyImageRefs(vault.KeyImagesXmr)
	if len(keyImageRefs) > 0 {
		needsReplay, err := c.keyImageCatchupNeedsReplay(vaultPubKey.String(), keyImageRefs)
		if err != nil {
			return false, err
		}
		if needsReplay {
			return true, nil
		}
	}

	return false, nil
}

func xmrOutputRefKey(txHash string, outputIndex uint32) string {
	return common.XMROutputRefKey(txHash, outputIndex)
}

func makeApplyKeyImageRefs(refs []*common.XmrKeyImageRef) []xmrFrostApplyKeyImageRef {
	out := make([]xmrFrostApplyKeyImageRef, 0, len(refs))
	for _, ref := range refs {
		if ref == nil {
			continue
		}
		out = append(out, xmrFrostApplyKeyImageRef{
			TxHash:      ref.TxHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    ref.KeyImage,
		})
	}
	return out
}

func normalizeTxKeyValues(keys []string) []string {
	return common.NormalizeXMRTxKeys(keys)
}

func sameNormalizedTxKeyValues(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (c *Client) keyImageCatchupNeedsReplay(vaultPubKey string, refs []xmrFrostApplyKeyImageRef) (bool, error) {
	// Replay verification must use persisted sidecar state. The scanner's
	// in-memory view can momentarily lag behind concurrent scanner_sync work,
	// which causes false "pending" results and blocks tx-key propagation.
	outputs, err := c.frostSigner.GetAllPersistedOutputs(vaultPubKey)
	if err != nil {
		return false, err
	}

	wanted := make(map[string]string, len(refs))
	for _, ref := range refs {
		wanted[xmrOutputRefKey(ref.TxHash, ref.OutputIndex)] = strings.ToLower(strings.TrimSpace(ref.KeyImage))
	}

	seen := make(map[string]struct{}, len(refs))
	for _, output := range outputs {
		key := xmrOutputRefKey(output.TxHash, output.OutputIndex)
		wantKeyImage, wantedOutput := wanted[key]
		hasSignerKeyImage := strings.TrimSpace(output.KeyImage) != ""
		switch {
		case !wantedOutput:
			// A locally-bound key_image that isn't yet in THORChain inventory
			// is the normal post-ceremony state while the attestation and
			// consensus round-trip to the vault's key_images_xmr completes.
			// It must NOT flip sync_state to syncing_to_tip, because doing
			// so gates the backlog driver (including for this very vault)
			// and creates a deadlock where the ceremony that would populate
			// the inventory can never run.
			continue
		case !hasSignerKeyImage:
			return true, nil
		case wantKeyImage != "" && !strings.EqualFold(strings.TrimSpace(output.KeyImage), wantKeyImage):
			return true, nil
		default:
			seen[key] = struct{}{}
		}
	}

	for key := range wanted {
		if _, ok := seen[key]; !ok {
			return true, nil
		}
	}

	return false, nil
}

type xmrOutgoingAuditLookup struct {
	canonicalVaultPubKey common.PubKey
	record               *xmrFrostOutgoingTransferStateEntry
	actualFeeAtomic      uint64
}

func (c *Client) lookupXMROutgoingAudit(vaultPubKey common.PubKey, txHash string) (xmrOutgoingAuditLookup, error) {
	if c == nil || c.frostSigner == nil || strings.TrimSpace(txHash) == "" {
		return xmrOutgoingAuditLookup{canonicalVaultPubKey: vaultPubKey}, nil
	}

	canonicalVaultPubKey, err := c.canonicalizeXMROutboundVaultPubKey(vaultPubKey, txHash)
	if err != nil {
		return xmrOutgoingAuditLookup{}, err
	}

	state, err := c.frostSigner.GetOutgoingTransferState(canonicalVaultPubKey.String())
	if err != nil {
		if isFrostHTTPStatus(err, http.StatusNotFound) {
			return xmrOutgoingAuditLookup{canonicalVaultPubKey: canonicalVaultPubKey}, nil
		}
		return xmrOutgoingAuditLookup{}, err
	}

	normalizedTxHash := normalizeTxID(txHash)
	for i := range state.Outgoing {
		record := &state.Outgoing[i]
		if !strings.EqualFold(strings.TrimSpace(record.TxHash), normalizedTxHash) {
			continue
		}
		return xmrOutgoingAuditLookup{
			canonicalVaultPubKey: canonicalVaultPubKey,
			record:               record,
			actualFeeAtomic:      record.Fee,
		}, nil
	}

	return xmrOutgoingAuditLookup{canonicalVaultPubKey: canonicalVaultPubKey}, nil
}

func (c *Client) resolveTrackedXMROutboundChange(
	lookup xmrOutgoingAuditLookup,
) (bool, error) {
	if lookup.record == nil {
		return false, errXMROutgoingAuditIncomplete
	}
	if !lookup.record.ownedChangeKnown() {
		return false, errXMROutgoingAuditIncomplete
	}
	return lookup.record.hasOwnedChange(), nil
}

func xmrOutboundMetaFromObservedTx(vaultPubKey common.PubKey, tx common.ObservedTx) (xmrFrostOutboundMeta, error) {
	amountAtomic, err := xmrThorchainAmountToAtomic(tx.Tx.Coins.GetCoin(common.XMRAsset).Amount)
	if err != nil {
		return xmrFrostOutboundMeta{}, fmt.Errorf("fail to convert xmr outbound amount to atomic for %s: %w", tx.Tx.ID, err)
	}

	var feeAtomic uint64
	gasCoin := tx.Tx.Gas.ToCoins().GetCoin(common.XMRAsset)
	if !gasCoin.IsEmpty() {
		feeAtomic, err = xmrThorchainAmountToAtomic(gasCoin.Amount)
		if err != nil {
			return xmrFrostOutboundMeta{}, fmt.Errorf("fail to convert xmr outbound fee to atomic for %s: %w", tx.Tx.ID, err)
		}
	}

	fromAddress := tx.Tx.FromAddress.String()
	if common.IsXMRUnknownSenderAddress(common.Address(fromAddress)) {
		if vaultAddr, addrErr := vaultPubKey.GetAddress(common.XMRChain); addrErr == nil && !vaultAddr.IsEmpty() {
			fromAddress = vaultAddr.String()
		}
	}

	return xmrFrostOutboundMeta{
		FromAddress:    fromAddress,
		ToAddress:      tx.Tx.ToAddress.String(),
		Amount:         amountAtomic,
		Fee:            feeAtomic,
		Memo:           tx.Tx.Memo,
		ObservedHeight: tx.BlockHeight,
	}, nil
}

func (c *Client) validateXMROutgoingAuditFromObservation(
	tx common.ObservedTx,
	lookup xmrOutgoingAuditLookup,
) (xmrOutgoingAuditLookup, error) {
	if c == nil || c.frostSigner == nil {
		return lookup, errXMROutgoingAuditIncomplete
	}
	vaultPubKey := lookup.canonicalVaultPubKey
	if vaultPubKey.IsEmpty() {
		vaultPubKey = tx.ObservedPubKey
	}
	if vaultPubKey.IsEmpty() {
		return lookup, fmt.Errorf("observed xmr outbound %s is missing vault pubkey", tx.Tx.ID)
	}
	meta, err := xmrOutboundMetaFromObservedTx(vaultPubKey, tx)
	if err != nil {
		return lookup, err
	}
	if strings.TrimSpace(meta.ToAddress) == "" {
		return lookup, fmt.Errorf("observed xmr outbound %s is missing destination address", tx.Tx.ID)
	}
	txKeys := normalizeTxKeyValues(tx.TxKeys)
	if len(txKeys) == 0 {
		return lookup, fmt.Errorf("observed xmr outbound %s is missing tx keys", tx.Tx.ID)
	}

	validateResp, err := c.frostSigner.ValidateOutbound(xmrFrostValidateOutboundRequest{
		VaultPubKey: vaultPubKey.String(),
		TxHash:      tx.Tx.ID.String(),
		TxKeys:      txKeys,
		FromAddress: meta.FromAddress,
		ToAddress:   meta.ToAddress,
		Amount:      meta.Amount,
		Fee:         meta.Fee,
		Memo:        meta.Memo,
		Reconcile:   true,
	})
	if err != nil {
		return lookup, err
	}

	lookup, err = c.lookupXMROutgoingAudit(vaultPubKey, tx.Tx.ID.String())
	if err != nil {
		return lookup, err
	}
	if validateResp.ActualFee > 0 {
		lookup.actualFeeAtomic = validateResp.ActualFee
		if lookup.record != nil {
			lookup.record.Fee = validateResp.ActualFee
		}
	}
	return lookup, nil
}

func xmrObservedTxFromTxInItem(item *stypes.TxInItem) common.ObservedTx {
	if item == nil {
		return common.ObservedTx{}
	}
	return common.ObservedTx{
		Tx: common.NewTx(
			common.TxID(item.Tx),
			common.Address(item.Sender),
			common.Address(item.To),
			item.Coins,
			item.Gas,
			item.Memo,
		),
		BlockHeight:    item.BlockHeight,
		ObservedPubKey: item.ObservedVaultPubKey,
		TxKeys:         append([]string(nil), item.TxKeys...),
		XmrKeyImages:   append([]*common.XmrKeyImageProof(nil), item.XmrKeyImages...),
	}
}

func (c *Client) xmrUnauthorizedSpendAlreadyObserved(txHash string) (bool, error) {
	normalizedTxHash := normalizeTxID(txHash)
	if normalizedTxHash == "" || c.thorchainBridge == nil {
		return false, nil
	}

	voter, err := c.thorchainBridge.GetObservedTxVoter(normalizedTxHash)
	if err != nil {
		return false, err
	}
	if voter == nil {
		return false, nil
	}

	return voter.GetHeight() > 0 || voter.GetFinalisedHeight() > 0, nil
}

func (c *Client) xmrUnauthorizedSpendKnownOutgoing(vaultPubKey common.PubKey, txHash string) (bool, error) {
	normalizedTxHash := normalizeTxID(txHash)
	if c == nil || c.frostSigner == nil || c.thorchainBridge == nil || vaultPubKey.IsEmpty() || normalizedTxHash == "" {
		return false, nil
	}

	state, err := c.frostSigner.GetOutgoingTransferState(vaultPubKey.String())
	if err != nil {
		if isFrostHTTPStatus(err, http.StatusNotFound) {
			return false, nil
		}
		return false, err
	}
	for _, record := range state.Outgoing {
		if strings.EqualFold(strings.TrimSpace(record.TxHash), normalizedTxHash) {
			return true, nil
		}
	}
	return false, nil
}

func setTxInItemXMRGasFromActualFee(item *stypes.TxInItem, actualFeeAtomic uint64) bool {
	if item == nil || actualFeeAtomic == 0 {
		return false
	}
	actualFee := actualFeeAtomic / moneroAtomicToThorchain
	if actualFee == 0 {
		return false
	}
	current := item.Gas.ToCoins().GetCoin(common.XMRAsset).Amount.Uint64()
	if current == actualFee {
		return false
	}
	item.Gas = common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(actualFee))}
	return true
}

func humanizeXMROutgoingPendingReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	return strings.ReplaceAll(reason, "_", " ")
}

func (c *Client) nextKeyImageCatchupBatch(vaultPubKey string, refs []xmrFrostApplyKeyImageRef) ([]xmrFrostApplyKeyImageRef, bool) {
	c.keyImageCatchupMu.Lock()
	defer c.keyImageCatchupMu.Unlock()

	if c.keyImageCatchupState == nil {
		c.keyImageCatchupState = make(map[string]xmrKeyImageCatchupState)
	}

	state, ok := c.keyImageCatchupState[vaultPubKey]
	switch {
	case !ok:
		return cloneApplyKeyImageRefs(refs), true
	case state.pending:
		return cloneApplyKeyImageRefs(refs), true
	case sameApplyKeyImageRefs(state.refs, refs):
		return nil, false
	case hasApplyKeyImagePrefix(refs, state.refs):
		return cloneApplyKeyImageRefs(refs[len(state.refs):]), true
	default:
		return cloneApplyKeyImageRefs(refs), true
	}
}

func (c *Client) recordKeyImageCatchup(vaultPubKey string, refs []xmrFrostApplyKeyImageRef, pending bool) {
	c.keyImageCatchupMu.Lock()
	defer c.keyImageCatchupMu.Unlock()

	if c.keyImageCatchupState == nil {
		c.keyImageCatchupState = make(map[string]xmrKeyImageCatchupState)
	}
	c.keyImageCatchupState[vaultPubKey] = xmrKeyImageCatchupState{
		refs:    cloneApplyKeyImageRefs(refs),
		pending: pending,
	}
}

func (c *Client) clearKeyImageCatchupState(vaultPubKey string) {
	if vaultPubKey == "" {
		return
	}
	c.keyImageCatchupMu.Lock()
	defer c.keyImageCatchupMu.Unlock()
	delete(c.keyImageCatchupState, vaultPubKey)
}

func cloneApplyKeyImageRefs(refs []xmrFrostApplyKeyImageRef) []xmrFrostApplyKeyImageRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]xmrFrostApplyKeyImageRef, len(refs))
	copy(out, refs)
	return out
}

func sameApplyKeyImageRefs(left, right []xmrFrostApplyKeyImageRef) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func xmrVaultStateIndex(states []xmrFrostVaultStateEntry) map[string]xmrFrostVaultStateEntry {
	index := make(map[string]xmrFrostVaultStateEntry, len(states))
	for _, state := range states {
		index[strings.TrimSpace(state.VaultPubKey)] = state
	}
	return index
}

func (c *Client) xmrHasPendingReplayWork() (bool, string, error) {
	return c.xmrHasPendingReplayWorkForVault(common.EmptyPubKey)
}

func (c *Client) xmrHasPendingReplayWorkForVault(target common.PubKey) (bool, string, error) {
	if target.IsEmpty() {
		if c.hasPendingXMRKeyImageCatchup() {
			return true, "xmr key-image replay is still pending", nil
		}
	} else if c.hasPendingXMRKeyImageCatchupForVault(target.String()) {
		return true, fmt.Sprintf("xmr key-image replay pending for vault %s", target.String()), nil
	}
	if c.thorchainBridge == nil || c.frostSigner == nil {
		return false, "", nil
	}

	vaults, err := c.trackedXMRVaultsForKeyImageCatchup()
	if err != nil {
		return false, "", err
	}
	for _, vault := range vaults {
		vaultPubKey := keyImageCatchupVaultPubKey(vault)
		if vaultPubKey.IsEmpty() {
			continue
		}
		if !target.IsEmpty() && !vaultPubKey.Equals(target) {
			continue
		}

		keyImageRefs := makeApplyKeyImageRefs(vault.KeyImagesXmr)
		if len(keyImageRefs) > 0 {
			needsReplay, replayErr := c.keyImageCatchupNeedsReplay(vaultPubKey.String(), keyImageRefs)
			if replayErr != nil {
				return false, "", replayErr
			}
			if needsReplay {
				return true, fmt.Sprintf("xmr key-image replay pending for vault %s", vaultPubKey.String()), nil
			}
		}

	}

	return false, "", nil
}

func (c *Client) xmrVaultLocalStatuses(states []xmrFrostVaultStateEntry) ([]xmrVaultLocalStatus, error) {
	trackedVaults, err := c.trackedXMRVaultsForKeyImageCatchup()
	if err != nil {
		return nil, err
	}
	syncState, _, err := c.xmrLocalSyncState(states)
	if err != nil {
		return nil, err
	}
	stateIndex := xmrVaultStateIndex(states)
	pendingFinalizeCounts := make(map[string]int)
	if c.keyImageWorkStore != nil {
		workItems, workErr := c.keyImageWorkStore.list()
		if workErr != nil {
			return nil, workErr
		}
		for _, workItem := range workItems {
			if workItem.Kind != xmrWorkKindFinalizeObservation {
				continue
			}
			vaultPubKey := strings.TrimSpace(workItem.VaultPubKey)
			if vaultPubKey == "" {
				continue
			}
			pendingFinalizeCounts[vaultPubKey]++
		}
	}
	statuses := make([]xmrVaultLocalStatus, 0, len(trackedVaults))
	for _, vault := range trackedVaults {
		vaultPubKey := keyImageCatchupVaultPubKey(vault)
		if vaultPubKey.IsEmpty() {
			continue
		}
		ceremonyCtx, ctxErr := c.resolveXMRKeyImageCeremonyContext(vaultPubKey)
		if ctxErr != nil {
			return nil, ctxErr
		}
		capability, capErr := c.xmrLocalCapability(ceremonyCtx.members, syncState, vault.Status)
		if capErr != nil {
			return nil, capErr
		}

		var pendingKeyImageReplayCount int
		keyImageRefs := makeApplyKeyImageRefs(vault.KeyImagesXmr)
		if len(keyImageRefs) > 0 {
			needsReplay, replayErr := c.keyImageCatchupNeedsReplay(vaultPubKey.String(), keyImageRefs)
			if replayErr != nil {
				return nil, replayErr
			}
			if needsReplay {
				pendingKeyImageReplayCount = len(keyImageRefs)
			}
		}

		outputs, outputErr := c.frostSigner.GetAllOutputs(vaultPubKey.String())
		if outputErr != nil && !isFrostHTTPStatus(outputErr, http.StatusNotFound) {
			return nil, outputErr
		}
		pendingOutputs := xmrOutputsMissingKeyImages(outputs)
		activeWorkItem := c.getActiveXMRWorkItem()
		if activeWorkItem != nil && !strings.EqualFold(strings.TrimSpace(activeWorkItem.VaultPubKey), vaultPubKey.String()) {
			activeWorkItem = nil
		}

		stateEntry := stateIndex[vaultPubKey.String()]
		status := xmrVaultLocalStatus{
			VaultPubKey:                vaultPubKey.String(),
			SyncState:                  syncState,
			Role:                       capability.Role,
			RegisteredKeyImageCount:    stateEntry.RegisteredKeyImageCount,
			RegisteredTxKeyCount:       stateEntry.RegisteredTxKeyCount,
			PendingKeyImageReplayCount: pendingKeyImageReplayCount,
			PendingKeyImageOutputCount: len(pendingOutputs),
			PendingKeyImageTxCount:     xmrPendingKeyImageTxCount(pendingOutputs),
			PendingFinalizePhaseCount:  pendingFinalizeCounts[vaultPubKey.String()],
			ActiveWorkItem:             activeWorkItem,
		}
		statuses = append(statuses, status)
	}

	return statuses, nil
}

func (c *Client) logXMRVaultLocalStatuses(states []xmrFrostVaultStateEntry) {
	statuses, err := c.xmrVaultLocalStatuses(states)
	if err != nil {
		c.logger.Debug().Err(err).Msg("xmr state sync: failed to derive local vault status")
		return
	}

	for _, status := range statuses {
		event := c.logger.Debug()
		if status.SyncState != xmrSyncStateAtTip ||
			status.PendingKeyImageReplayCount > 0 ||
			status.PendingKeyImageOutputCount > 0 ||
			status.PendingFinalizePhaseCount > 0 ||
			status.ActiveWorkItem != nil {
			event = c.logger.Info()
		}
		event = event.
			Str("vault_pub_key", status.VaultPubKey).
			Str("sync_state", string(status.SyncState)).
			Str("role", string(status.Role)).
			Int("registered_key_images", status.RegisteredKeyImageCount).
			Int("registered_tx_keys", status.RegisteredTxKeyCount).
			Int("pending_key_image_replay", status.PendingKeyImageReplayCount).
			Int("pending_key_image_outputs", status.PendingKeyImageOutputCount).
			Int("pending_key_image_txs", status.PendingKeyImageTxCount).
			Int("pending_finalize_phases", status.PendingFinalizePhaseCount)
		if status.ActiveWorkItem != nil {
			event = event.
				Str("active_work_id", status.ActiveWorkItem.ID).
				Str("active_work_kind", string(status.ActiveWorkItem.Kind)).
				Str("active_work_phase", string(status.ActiveWorkItem.Phase)).
				Int64("active_work_anchor_height", status.ActiveWorkItem.AnchorHeight).
				Int64("active_work_leader_epoch_height", status.ActiveWorkItem.LeaderEpochHeight)
		}
		event.Msg("xmr local vault status")
	}
}

func xmrObservedTxAnchorHeight(voter *ttypes.QueryObservedTxVoter, txHash string, fallback int64) int64 {
	if voter == nil {
		return fallback
	}
	normalizedHash := normalizeTxID(txHash)
	candidates := make([]*ttypes.QueryObservedTx, 0, len(voter.Txs)+1)
	if !voter.Tx.Tx.IsEmpty() {
		candidates = append(candidates, &voter.Tx)
	}
	for i := range voter.Txs {
		candidates = append(candidates, &voter.Txs[i])
	}
	for _, candidate := range candidates {
		if candidate == nil || candidate.Tx.IsEmpty() || !candidate.Tx.Chain.Equals(common.XMRChain) {
			continue
		}
		if !strings.EqualFold(candidate.Tx.ID.String(), normalizedHash) {
			continue
		}
		if candidate.BlockHeight > 0 {
			return candidate.BlockHeight
		}
	}
	if voter.FinalisedHeight > 0 {
		return voter.FinalisedHeight
	}
	if voter.Height > 0 {
		return voter.Height
	}
	return fallback
}

func (c *Client) resolveXMRKeyImageAnchorHeight(vaultPubKey common.PubKey, txHash string, fallback int64) int64 {
	if c.thorchainBridge == nil || strings.TrimSpace(txHash) == "" {
		return fallback
	}
	voter, err := c.thorchainBridge.GetObservedTxVoter(txHash)
	if err != nil {
		return fallback
	}
	if meta, ok, metaErr := c.outgoingInventoryCanonicalOutboundMeta(vaultPubKey, txHash, voter.Tx.Tx.Memo); metaErr == nil && ok && meta.ObservedHeight > 0 {
		return meta.ObservedHeight
	}
	return xmrObservedTxAnchorHeight(voter, txHash, fallback)
}

func (c *Client) ensureXMRKeyImageWorkItem(
	vaultPubKey common.PubKey,
	txHash string,
	outputIndex uint32,
	anchorHeight int64,
	kind xmrWorkKind,
	phase xmrFinalizePhase,
) (xmrWorkItem, error) {
	item := xmrWorkItem{
		ID:           xmrKeyImageWorkID(vaultPubKey, txHash, outputIndex),
		Kind:         kind,
		Phase:        phase,
		VaultPubKey:  strings.TrimSpace(vaultPubKey.String()),
		TxHash:       normalizeTxID(txHash),
		AnchorHeight: anchorHeight,
	}
	item.OutputIndex = new(uint32)
	*item.OutputIndex = outputIndex
	if c.keyImageWorkStore == nil {
		item.LeaderEpochHeight = xmrLeaderEpochHeight(anchorHeight, xmrDefaultLeaderEpochWindowBlocks)
		return item, nil
	}

	stored, ok, err := c.keyImageWorkStore.get(item.ID)
	if err != nil {
		return xmrWorkItem{}, err
	}
	if !ok {
		item.LeaderEpochHeight = xmrLeaderEpochHeight(anchorHeight, xmrDefaultLeaderEpochWindowBlocks)
		if err := c.keyImageWorkStore.put(item); err != nil {
			return xmrWorkItem{}, err
		}
		return item, nil
	}

	if stored.AnchorHeight <= 0 && anchorHeight > 0 {
		stored.AnchorHeight = anchorHeight
	}
	if stored.LeaderEpochHeight <= 0 {
		stored.LeaderEpochHeight = xmrLeaderEpochHeight(stored.AnchorHeight, xmrDefaultLeaderEpochWindowBlocks)
	}
	stored.Kind = kind
	stored.Phase = phase
	stored.VaultPubKey = strings.TrimSpace(vaultPubKey.String())
	stored.TxHash = normalizeTxID(txHash)
	if stored.OutputIndex == nil {
		stored.OutputIndex = new(uint32)
		*stored.OutputIndex = outputIndex
	}
	if err := c.keyImageWorkStore.put(stored); err != nil {
		return xmrWorkItem{}, err
	}
	return stored, nil
}

func (c *Client) ensureXMRFinalizeWorkItem(
	vaultPubKey common.PubKey,
	txHash string,
	anchorHeight int64,
	phase xmrFinalizePhase,
) (xmrWorkItem, error) {
	item := xmrWorkItem{
		ID:           xmrFinalizeObservationWorkID(vaultPubKey, txHash, phase),
		Kind:         xmrWorkKindFinalizeObservation,
		Phase:        phase,
		VaultPubKey:  strings.TrimSpace(vaultPubKey.String()),
		TxHash:       normalizeTxID(txHash),
		AnchorHeight: anchorHeight,
	}
	if c.keyImageWorkStore == nil {
		return item, nil
	}

	stored, ok, err := c.keyImageWorkStore.get(item.ID)
	if err != nil {
		return xmrWorkItem{}, err
	}
	if !ok {
		if err := c.keyImageWorkStore.put(item); err != nil {
			return xmrWorkItem{}, err
		}
		return item, nil
	}

	if stored.AnchorHeight <= 0 && anchorHeight > 0 {
		stored.AnchorHeight = anchorHeight
	}
	stored.Kind = xmrWorkKindFinalizeObservation
	stored.Phase = phase
	stored.VaultPubKey = strings.TrimSpace(vaultPubKey.String())
	stored.TxHash = normalizeTxID(txHash)
	if err := c.keyImageWorkStore.put(stored); err != nil {
		return xmrWorkItem{}, err
	}
	return stored, nil
}

func (c *Client) rotateXMRKeyImageLeaderEpoch(item xmrWorkItem, currentHeight int64) (xmrWorkItem, error) {
	if currentHeight <= 0 {
		return item, nil
	}
	targetHeight := xmrLeaderEpochHeight(currentHeight, c.getSigningTransactionPeriod())
	if item.LeaderEpochHeight == targetHeight {
		return item, nil
	}
	if item.LeaderEpochHeight > targetHeight && isTransientXMRKeyImageCeremonyText(item.LastError) {
		return item, nil
	}
	item.LeaderEpochHeight = targetHeight
	if c.keyImageWorkStore == nil {
		return item, nil
	}
	if err := c.keyImageWorkStore.put(item); err != nil {
		return xmrWorkItem{}, err
	}
	return item, nil
}

func (c *Client) recordXMRKeyImageWorkAttempt(item xmrWorkItem, currentHeight int64, errText string) (xmrWorkItem, error) {
	item.LastAttemptHeight = currentHeight
	item.LastAttemptAt = time.Now().Unix()
	item.LastError = strings.TrimSpace(errText)
	if isTransientXMRKeyImageCeremonyText(item.LastError) {
		// Party formation failures are liveness issues, not invalid work. Keep
		// the item pending indefinitely and only move to a future leader epoch.
		item.AttemptCount = 0
		period := c.getSigningTransactionPeriod()
		if period > 0 && currentHeight > 0 {
			nextEpoch := xmrLeaderEpochHeight(currentHeight+period, period)
			if nextEpoch > item.LeaderEpochHeight {
				item.LeaderEpochHeight = nextEpoch
			}
		}
	} else {
		item.AttemptCount++
	}
	if c.keyImageWorkStore == nil {
		return item, nil
	}
	return item, c.keyImageWorkStore.put(item)
}

func xmrWorkItemRetryReady(item xmrWorkItem, now time.Time) (bool, time.Duration) {
	if item.LastAttemptAt <= 0 {
		return true, 0
	}
	elapsed := now.Sub(time.Unix(item.LastAttemptAt, 0))
	if elapsed >= xmrWorkRetryMinBackoff {
		return true, 0
	}
	return false, xmrWorkRetryMinBackoff - elapsed
}

func xmrMissingOutboundTxRetryReady(item xmrWorkItem, now time.Time) (bool, time.Duration) {
	if item.LastAttemptAt <= 0 || !isMissingXMROutboundValidationText(item.LastError) {
		return true, 0
	}
	if item.AttemptCount <= 0 {
		return true, 0
	}
	index := int(item.AttemptCount - 1)
	if index >= len(xmrMissingOutboundTxRetryBackoff) {
		return true, 0
	}
	elapsed := now.Sub(time.Unix(item.LastAttemptAt, 0))
	wait := xmrMissingOutboundTxRetryBackoff[index]
	if elapsed >= wait {
		return true, 0
	}
	return false, wait - elapsed
}

func xmrOutboundTxKeyValidationRetryReady(item xmrWorkItem, now time.Time) (bool, time.Duration) {
	if isMissingXMROutboundValidationText(item.LastError) {
		return xmrMissingOutboundTxRetryReady(item, now)
	}
	return xmrWorkItemRetryReady(item, now)
}

func (c *Client) completeXMRKeyImageWorkItem(item xmrWorkItem) error {
	if c.keyImageWorkStore == nil {
		return nil
	}
	return c.keyImageWorkStore.delete(item.ID)
}

func hasApplyKeyImagePrefix(full, prefix []xmrFrostApplyKeyImageRef) bool {
	if len(prefix) > len(full) {
		return false
	}
	for i := range prefix {
		if full[i] != prefix[i] {
			return false
		}
	}
	return true
}

func (c *Client) Stop() {
	c.blockScanner.Stop()
	close(c.stopchan)
	c.wg.Wait()
}

func (c *Client) IsBlockScannerHealthy() bool {
	if c.blockScanner == nil {
		return false
	}
	return c.blockScanner.IsHealthy()
}

func (c *Client) ensureConsolidationState() {
	c.consolidationInitOnce.Do(func() {
		if c.consolidateInProgress == nil {
			c.consolidateInProgress = atomic.NewBool(false)
		}
		if c.lastConsolidationHeight == nil {
			c.lastConsolidationHeight = atomic.NewInt64(0)
		}
		if c.lastSolvencyCheckHeight == nil {
			c.lastSolvencyCheckHeight = atomic.NewInt64(0)
		}
		if c.solvencyCheckAfterIdle == nil {
			c.solvencyCheckAfterIdle = atomic.NewBool(false)
		}
		if c.vaultLocks == nil {
			c.vaultLocks = make(map[string]*sync.Mutex)
		}
		if c.consolidationState == nil {
			c.consolidationState = make(map[string]xmrConsolidationVaultState)
		}
	})
}

func (c *Client) GetVaultLock(vaultPubKey string) *sync.Mutex {
	c.ensureConsolidationState()
	key := strings.TrimSpace(vaultPubKey)
	if key == "" {
		return &c.emptyVaultLock
	}

	c.vaultLockMu.Lock()
	defer c.vaultLockMu.Unlock()
	lock, ok := c.vaultLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		c.vaultLocks[key] = lock
	}
	return lock
}

func (c *Client) GetVaultLockKey(tx stypes.TxOutItem) string {
	vaultKey := txVaultPubKey(tx)
	return strings.TrimSpace(vaultKey.String())
}

func (c *Client) consolidationRunner() {
	defer c.wg.Done()

	ticker := time.NewTicker(constants.ThorchainBlockTime)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopchan:
			return
		case <-ticker.C:
			c.tryConsolidationPass()
		}
	}
}

func (c *Client) tryConsolidationPass() {
	c.ensureConsolidationState()
	if !c.consolidateInProgress.CompareAndSwap(false, true) {
		return
	}
	defer c.consolidateInProgress.Store(false)

	if err := c.runConsolidationPass(); err != nil {
		c.logger.Debug().Err(err).Msg("xmr consolidation pass failed")
	}
}

func (c *Client) runConsolidationPass() error {
	c.ensureConsolidationState()
	if c.thorchainBridge == nil || c.frostSigner == nil || c.tssServer == nil {
		return nil
	}
	if c.localNodePubKey.IsEmpty() {
		return nil
	}
	if c.blockScanner != nil && !c.IsBlockScannerHealthy() {
		return nil
	}

	height, err := c.thorchainBridge.GetBlockHeight()
	if err != nil {
		return fmt.Errorf("fail to get thorchain block height for xmr consolidation: %w", err)
	}
	if height <= 0 {
		return nil
	}
	if c.lastConsolidationHeight.Load() >= height {
		return nil
	}
	c.lastConsolidationHeight.Store(height)

	settings := c.loadConsolidationSettings()
	if !settings.Enabled {
		return nil
	}

	nodeStatus, err := c.thorchainBridge.FetchNodeStatus()
	if err != nil {
		return fmt.Errorf("fail to get node status for xmr consolidation: %w", err)
	}
	if nodeStatus != ttypes.NodeStatus_Active {
		return nil
	}

	vaults, err := c.thorchainBridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards for xmr consolidation: %w", err)
	}
	if len(vaults) == 0 {
		return nil
	}

	churnInterval := c.getChurnInterval()
	hasPendingXMRVaultWork := false
	for _, vault := range vaults {
		if !vault.Contains(c.localNodePubKey) {
			continue
		}
		if !vault.GetChains().Has(c.chain) {
			continue
		}
		if len(vault.PendingTxBlockHeights) > 0 {
			hasPendingXMRVaultWork = true
			break
		}
	}
	for _, vault := range vaults {
		if !vault.Contains(c.localNodePubKey) {
			continue
		}
		if !vault.GetChains().Has(c.chain) {
			continue
		}
		triggerPeriodic := settings.CheckIntervalBlocks > 0 && height%settings.CheckIntervalBlocks == 0
		triggerPreChurn := c.shouldPreChurnConsolidate(vault, height, churnInterval, settings.ChurnLookahead)
		if !triggerPeriodic && !triggerPreChurn {
			continue
		}
		if hasPendingXMRVaultWork {
			vaultPubKey := txVaultPubKey(stypes.TxOutItem{
				VaultPubKey:      vault.PubKey,
				VaultPubKeyEddsa: vault.PubKeyEddsa,
				VaultPubKeyFrost: vault.PubKeyFrost,
			})
			c.logConsolidationSkip(vault, vaultPubKey.String(), height, c.getConsolidationVaultState(vaultPubKey.String()), "pending thorchain xmr tx", func(e *zerolog.Event) {
				e.Int("pending_block_heights", len(vault.PendingTxBlockHeights))
			})
			continue
		}
		c.consolidateVault(vault, height, settings, triggerPeriodic, triggerPreChurn)
	}
	return nil
}

func (c *Client) shouldPreChurnConsolidate(vault ttypes.Vault, thorchainHeight, churnInterval, lookahead int64) bool {
	if vault.IsRetiring() {
		return false
	}
	if !vault.IsActive() || churnInterval <= 0 || lookahead <= 0 {
		return false
	}
	nextChurnHeight := vault.StatusSince + churnInterval
	return thorchainHeight+lookahead >= nextChurnHeight
}

func (c *Client) consolidateVault(
	vault ttypes.Vault,
	thorchainHeight int64,
	settings xmrConsolidationSettings,
	triggerPeriodic bool,
	triggerPreChurn bool,
) {
	vaultPubKey := txVaultPubKey(stypes.TxOutItem{
		VaultPubKey:      vault.PubKey,
		VaultPubKeyEddsa: vault.PubKeyEddsa,
		VaultPubKeyFrost: vault.PubKeyFrost,
	})
	if vaultPubKey.IsEmpty() {
		return
	}

	stateKey := vaultPubKey.String()
	state := c.getConsolidationVaultState(stateKey)
	effectiveCooldown := scaledConsolidationCooldown(settings.CooldownBlocks, state.ConsecutiveFails)
	if effectiveCooldown > 0 && state.LastAttemptHeight > 0 && thorchainHeight-state.LastAttemptHeight < effectiveCooldown {
		c.logConsolidationSkip(vault, stateKey, thorchainHeight, state, "cooldown active", func(e *zerolog.Event) {
			e.Int64("last_attempt_height", state.LastAttemptHeight).
				Int64("cooldown_blocks", effectiveCooldown)
		})
		return
	}

	vaultAddress := c.resolveVaultAddress(vault)
	if vaultAddress.IsEmpty() {
		return
	}

	lock := c.GetVaultLock(stateKey)
	lock.Lock()
	defer lock.Unlock()

	hasMempoolOutgoing, err := c.hasMempoolOutgoingForVault(vaultPubKey, vaultAddress.String())
	if err != nil {
		c.logConsolidationSkip(vault, stateKey, thorchainHeight, state, "fail to check mempool", func(e *zerolog.Event) {
			e.Err(err)
		})
		return
	}
	if hasMempoolOutgoing {
		c.logConsolidationSkip(vault, stateKey, thorchainHeight, state, "pending mempool outbound", nil)
		return
	}

	if err = c.ensureFrostVaultRegistration(vaultPubKey); err != nil {
		c.logConsolidationSkip(vault, stateKey, thorchainHeight, state, "fail to register vault", func(e *zerolog.Event) {
			e.Err(err)
		})
		return
	}

	balance, err := c.frostSigner.GetBalance(stateKey)
	if err != nil {
		c.logConsolidationSkip(vault, stateKey, thorchainHeight, state, "fail to query balance", func(e *zerolog.Event) {
			e.Err(err)
		})
		return
	}

	spendableAtomic := spendableBalanceAtomic(balance)
	outputs, err := c.frostSigner.GetOutputs(stateKey)
	if err != nil {
		c.logConsolidationSkip(vault, stateKey, thorchainHeight, state, "fail to query outputs", func(e *zerolog.Event) {
			e.Err(err)
		})
		return
	}
	currentHeight := int64(0)
	if c.scanner != nil {
		currentHeight = c.scanner.CurrentHeight()
	}
	unlockedOutputs := countUnlockedOutputs(outputs, currentHeight)
	if unlockedOutputs < settings.MinUnlockedOutputs {
		c.logConsolidationSkip(vault, stateKey, thorchainHeight, state, "output count below threshold", func(e *zerolog.Event) {
			e.Int64("unlocked_outputs", unlockedOutputs).
				Int64("min_unlocked_outputs", settings.MinUnlockedOutputs)
		})
		return
	}
	if unlockedOutputs <= settings.TargetOutputs {
		c.logConsolidationSkip(vault, stateKey, thorchainHeight, state, "already at target output count", func(e *zerolog.Event) {
			e.Int64("unlocked_outputs", unlockedOutputs).
				Int64("target_outputs", settings.TargetOutputs)
		})
		return
	}

	feeRate, err := c.frostSigner.GetFeeRate()
	if err != nil {
		c.logConsolidationSkip(vault, stateKey, thorchainHeight, state, "fail to query fee rate", func(e *zerolog.Event) {
			e.Err(err)
		})
		return
	}

	coordinationHeight := xmrLeaderEpochHeight(thorchainHeight, c.getSigningTransactionPeriod())
	maxRetries := settings.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 1
	}
	reserve := initialConsolidationReserve(spendableAtomic, settings.ReserveBps)
	lastErr := error(nil)
	for attempt := int64(1); attempt <= maxRetries; attempt++ {
		if reserve >= spendableAtomic {
			break
		}
		amountAtomic := spendableAtomic - reserve
		amountThorchain := amountAtomic / moneroAtomicToThorchain
		if amountThorchain == 0 {
			break
		}

		gasRate := int64(feeRate)
		if feeRate > uint64(math.MaxInt64) {
			gasRate = math.MaxInt64
		}
		txOutItem := stypes.TxOutItem{
			Chain:            c.chain,
			ToAddress:        vaultAddress,
			VaultPubKey:      vault.PubKey,
			VaultPubKeyEddsa: vault.PubKeyEddsa,
			VaultPubKeyFrost: vault.PubKeyFrost,
			Coins: common.Coins{
				common.NewCoin(common.XMRAsset, cosmos.NewUint(amountThorchain)),
			},
			Memo:      memo.NewConsolidateMemo().String(),
			GasRate:   gasRate,
			SigningID: xmrConsolidationSigningID(stateKey, coordinationHeight, attempt, amountAtomic),
		}

		rawTx, _, observation, signErr := c.SignTx(txOutItem, thorchainHeight)
		if signErr != nil {
			lastErr = signErr
			if !isRetryableConsolidationErr(signErr) {
				break
			}
			nextReserve := reserve
			if necessaryFee := xmrNecessaryFeeFromError(signErr); necessaryFee > 0 {
				nextReserve = consolidationReserveForNecessaryFee(reserve, necessaryFee, settings.ReserveBps)
			}
			if nextReserve <= reserve {
				nextReserve = bumpConsolidationReserve(reserve, settings.ReserveBps)
			}
			reserve = nextReserve
			continue
		}
		if len(rawTx) == 0 {
			lastErr = errors.New("signed consolidation transaction is empty")
			break
		}

		txHash, broadcastErr := c.BroadcastTx(txOutItem, rawTx)
		if broadcastErr != nil {
			lastErr = broadcastErr
			if !isRetryableConsolidationErr(broadcastErr) {
				break
			}
			reserve = bumpConsolidationReserve(reserve, settings.ReserveBps)
			continue
		}
		c.observeSignedXMRConsolidation(observation, txHash)

		c.setConsolidationVaultState(stateKey, xmrConsolidationVaultState{
			LastAttemptHeight: thorchainHeight,
			LastSuccessHeight: thorchainHeight,
			LastTxHash:        txHash,
			ConsecutiveFails:  0,
		})
		c.logger.Info().
			Str("vault_pubkey", stateKey).
			Str("tx_hash", txHash).
			Int64("unlocked_outputs", unlockedOutputs).
			Bool("periodic_trigger", triggerPeriodic).
			Bool("prechurn_trigger", triggerPreChurn).
			Msg("xmr consolidation broadcasted")
		return
	}

	state.LastAttemptHeight = thorchainHeight
	state.ConsecutiveFails++
	c.setConsolidationVaultState(stateKey, state)
	if lastErr != nil {
		c.logger.Info().
			Err(lastErr).
			Str("vault_pubkey", stateKey).
			Str("vault_status", vault.Status.String()).
			Bool("periodic_trigger", triggerPeriodic).
			Bool("prechurn_trigger", triggerPreChurn).
			Int64("thorchain_height", thorchainHeight).
			Int64("consecutive_failures", state.ConsecutiveFails).
			Msg("xmr consolidation failed")
	}
}

func (c *Client) observeSignedXMRConsolidation(item *stypes.TxInItem, txHash string) {
	if item == nil {
		return
	}
	if normalized := strings.TrimSpace(txHash); normalized != "" {
		item.Tx = normalized
	}
	if c.globalTxsQueue == nil {
		if c.scanner != nil {
			c.scanner.queueReplayTxInItems([]*stypes.TxInItem{item})
		}
		return
	}
	c.globalTxsQueue <- stypes.TxIn{
		Chain:                  c.chain,
		TxArray:                []*stypes.TxInItem{item},
		MemPool:                true,
		Filtered:               true,
		ConfirmationRequired:   1,
		AllowFutureObservation: true,
	}
}

func (c *Client) logConsolidationSkip(
	vault ttypes.Vault,
	stateKey string,
	thorchainHeight int64,
	state xmrConsolidationVaultState,
	reason string,
	fields func(*zerolog.Event),
) {
	if state.LastSkipReason == reason && thorchainHeight-state.LastSkipHeight < 20 {
		return
	}
	event := c.logger.Info().
		Str("vault_pubkey", stateKey).
		Int64("thorchain_height", thorchainHeight).
		Str("vault_status", vault.Status.String()).
		Str("reason", reason)
	if fields != nil {
		fields(event)
	}
	event.Msg("skip xmr consolidation")
	state.LastSkipReason = reason
	state.LastSkipHeight = thorchainHeight
	c.setConsolidationVaultState(stateKey, state)
}

func countUnlockedOutputs(outputs []xmrFrostOutputEntry, currentHeight int64) int64 {
	if len(outputs) == 0 {
		return 0
	}
	count := int64(0)
	for _, out := range outputs {
		if out.IsSpent() {
			continue
		}
		if currentHeight > 0 {
			if currentHeight-int64(out.Height) < 10 {
				continue
			}
		}
		count++
	}
	return count
}

func initialConsolidationReserve(availableAtomic uint64, reserveBps int64) uint64 {
	if reserveBps <= 0 {
		reserveBps = defaultXMRConsolidationReserveBps
	}
	reserve := scaleUint64ByBasisPoints(availableAtomic, reserveBps)
	if reserve < defaultXMRConsolidationMinReserveAtomic {
		reserve = defaultXMRConsolidationMinReserveAtomic
	}
	return reserve
}

func bumpConsolidationReserve(currentReserve uint64, reserveBps int64) uint64 {
	step := currentReserve / 2
	if reserveBps > 0 {
		extra := scaleUint64ByBasisPoints(currentReserve, reserveBps)
		if extra > step {
			step = extra
		}
	}
	if step < defaultXMRConsolidationMinReserveAtomic {
		step = defaultXMRConsolidationMinReserveAtomic
	}
	if currentReserve > ^uint64(0)-step {
		return ^uint64(0)
	}
	return currentReserve + step
}

func consolidationReserveForNecessaryFee(currentReserve, feeAtomic uint64, reserveBps int64) uint64 {
	if feeAtomic == 0 {
		return currentReserve
	}

	cushion := scaleUint64ByBasisPoints(feeAtomic, reserveBps)
	if cushion < defaultXMRConsolidationMinReserveAtomic {
		cushion = defaultXMRConsolidationMinReserveAtomic
	}

	required := feeAtomic
	if required > math.MaxUint64-cushion {
		required = math.MaxUint64
	} else {
		required += cushion
	}

	if required < currentReserve {
		return currentReserve
	}
	return required
}

func scaleUint64ByBasisPoints(value uint64, bps int64) uint64 {
	if value == 0 || bps <= 0 {
		return 0
	}

	scaled := new(big.Int).SetUint64(value)
	scaled.Mul(scaled, big.NewInt(bps))
	scaled.Quo(scaled, big.NewInt(10_000))
	if !scaled.IsUint64() {
		return math.MaxUint64
	}
	return scaled.Uint64()
}

func xmrConsolidationSigningID(vaultPubKey string, coordinationHeight, attempt int64, amountAtomic uint64) string {
	return fmt.Sprintf(
		"xmr-consolidation:%s:%d:%d:%d",
		strings.TrimSpace(vaultPubKey),
		coordinationHeight,
		attempt,
		amountAtomic,
	)
}

func scaledConsolidationCooldown(baseCooldown, consecutiveFails int64) int64 {
	if baseCooldown <= 0 {
		return 0
	}
	if consecutiveFails <= 1 {
		return baseCooldown
	}
	if consecutiveFails > math.MaxInt64/baseCooldown {
		return math.MaxInt64
	}
	return baseCooldown * consecutiveFails
}

func isRetryableConsolidationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "insufficient") || strings.Contains(msg, "not enough funds") {
		return true
	}
	if strings.Contains(msg, "fee") || strings.Contains(msg, "mempool") {
		return true
	}
	return false
}

func (c *Client) hasMempoolOutgoingForVault(vaultPubKey common.PubKey, vaultAddress string) (bool, error) {
	resp, err := c.frostSigner.GetMemPoolTransfers()
	if err != nil {
		return false, err
	}
	vaultKey := strings.TrimSpace(vaultPubKey.String())
	vaultAddress = strings.TrimSpace(vaultAddress)
	for _, transfer := range resp.Outgoing {
		if transfer.VaultPubKey != "" && strings.EqualFold(strings.TrimSpace(transfer.VaultPubKey), vaultKey) {
			return true, nil
		}
		if transfer.FromAddress != "" && vaultAddress != "" && strings.EqualFold(strings.TrimSpace(transfer.FromAddress), vaultAddress) {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) getConsolidationVaultState(vaultPubKey string) xmrConsolidationVaultState {
	c.ensureConsolidationState()
	c.consolidationStateMu.Lock()
	defer c.consolidationStateMu.Unlock()
	return c.consolidationState[vaultPubKey]
}

func (c *Client) setConsolidationVaultState(vaultPubKey string, state xmrConsolidationVaultState) {
	c.ensureConsolidationState()
	c.consolidationStateMu.Lock()
	c.consolidationState[vaultPubKey] = state
	c.consolidationStateMu.Unlock()
}

func (c *Client) loadConsolidationSettings() xmrConsolidationSettings {
	c.consolidationSettingsMu.RLock()
	if c.hasConsolidationConfig && time.Since(c.lastConsolidationConfig) < constants.ThorchainBlockTime {
		settings := c.consolidationSettings
		c.consolidationSettingsMu.RUnlock()
		return settings
	}
	c.consolidationSettingsMu.RUnlock()

	c.consolidationSettingsMu.Lock()
	defer c.consolidationSettingsMu.Unlock()
	if c.hasConsolidationConfig && time.Since(c.lastConsolidationConfig) < constants.ThorchainBlockTime {
		return c.consolidationSettings
	}

	enabled := c.cfg.XMRConsolidationEnabled
	if mimirEnabled, ok := c.getMimirValue(mimirXMRConsolidationEnabled); ok {
		enabled = mimirEnabled > 0
	}
	settings := xmrConsolidationSettings{
		Enabled:             enabled,
		CheckIntervalBlocks: c.getMimirInt64(mimirXMRConsolidationCheckIntervalBlocks, fallbackInt64(c.cfg.XMRConsolidationCheckIntervalBlocks, defaultXMRConsolidationCheckIntervalBlocks)),
		CooldownBlocks:      c.getMimirInt64(mimirXMRConsolidationCooldownBlocks, fallbackInt64(c.cfg.XMRConsolidationCooldownBlocks, defaultXMRConsolidationCooldownBlocks)),
		MinUnlockedOutputs:  c.getMimirInt64(mimirXMRConsolidationMinUnlockedOutputs, fallbackInt64(c.cfg.XMRConsolidationMinUnlockedOutputs, defaultXMRConsolidationMinUnlockedOutputs)),
		TargetOutputs:       c.getMimirInt64(mimirXMRConsolidationTargetOutputs, fallbackInt64(c.cfg.XMRConsolidationTargetOutputs, defaultXMRConsolidationTargetOutputs)),
		ReserveBps:          c.getMimirInt64(mimirXMRConsolidationReserveBps, fallbackInt64(c.cfg.XMRConsolidationReserveBps, defaultXMRConsolidationReserveBps)),
		ChurnLookahead:      c.getMimirInt64(mimirXMRConsolidationChurnLookahead, fallbackInt64(c.cfg.XMRConsolidationChurnLookaheadBlocks, defaultXMRConsolidationChurnLookahead)),
		MaxRetries:          c.getMimirInt64(mimirXMRConsolidationMaxRetries, fallbackInt64(c.cfg.XMRConsolidationMaxRetries, defaultXMRConsolidationMaxRetries)),
	}
	if settings.CheckIntervalBlocks <= 0 {
		settings.CheckIntervalBlocks = defaultXMRConsolidationCheckIntervalBlocks
	}
	if settings.CooldownBlocks <= 0 {
		settings.CooldownBlocks = defaultXMRConsolidationCooldownBlocks
	}
	if settings.MinUnlockedOutputs <= 0 {
		settings.MinUnlockedOutputs = defaultXMRConsolidationMinUnlockedOutputs
	}
	if settings.TargetOutputs <= 0 {
		settings.TargetOutputs = defaultXMRConsolidationTargetOutputs
	}
	if settings.ReserveBps <= 0 {
		settings.ReserveBps = defaultXMRConsolidationReserveBps
	}
	if settings.ChurnLookahead <= 0 {
		settings.ChurnLookahead = defaultXMRConsolidationChurnLookahead
	}
	if settings.MaxRetries <= 0 {
		settings.MaxRetries = defaultXMRConsolidationMaxRetries
	}
	c.consolidationSettings = settings
	c.lastConsolidationConfig = time.Now()
	c.hasConsolidationConfig = true
	return settings
}

func (c *Client) getMimirValue(key string) (int64, bool) {
	if c.thorchainBridge == nil {
		return 0, false
	}
	value, err := c.thorchainBridge.GetMimir(key)
	if err != nil {
		c.logger.Debug().Err(err).Str("mimir_key", key).Msg("fail to fetch xmr consolidation mimir value")
		return 0, false
	}
	if value < 0 {
		return 0, false
	}
	return value, true
}

func (c *Client) getMimirInt64(key string, fallback int64) int64 {
	value, ok := c.getMimirValue(key)
	if !ok {
		return fallback
	}
	if value <= 0 {
		return fallback
	}
	return value
}

func (c *Client) getMimirUint64(key string, fallback uint64) uint64 {
	value := c.getMimirInt64(key, int64(fallback))
	if value <= 0 {
		return fallback
	}
	return uint64(value)
}

func fallbackInt64(value, fallback int64) int64 {
	if value <= 0 {
		return fallback
	}
	return value
}

func fallbackUint64(value, fallback uint64) uint64 {
	if value == 0 {
		return fallback
	}
	return value
}

func (c *Client) getChurnInterval() int64 {
	consts, err := c.thorchainBridge.GetConstants()
	if err == nil {
		if interval := consts[constants.ChurnInterval.String()]; interval > 0 {
			return interval
		}
	}
	return constants.GetConstantValues(semver.MustParse("0.0.0")).GetInt64Value(constants.ChurnInterval)
}

func (c *Client) getSigningTransactionPeriod() int64 {
	if c.thorchainBridge != nil {
		if consts, err := c.thorchainBridge.GetConstants(); err == nil {
			if period := consts[constants.SigningTransactionPeriod.String()]; period > 0 {
				return period
			}
		}
	}
	return constants.GetConstantValues(semver.MustParse("0.0.0")).GetInt64Value(constants.SigningTransactionPeriod)
}

func (c *Client) currentThorHeight(fallback int64) int64 {
	if c.thorchainBridge != nil {
		if h, err := c.thorchainBridge.GetBlockHeight(); err == nil && h > 0 {
			return h
		}
	}
	return fallback
}

func (c *Client) SignTx(tx stypes.TxOutItem, height int64) ([]byte, []byte, *stypes.TxInItem, error) {
	if !tx.Chain.Equals(c.chain) {
		return nil, nil, nil, fmt.Errorf("cannot sign non-xmr tx on monero client: %s", tx.Chain)
	}
	signedTx, checkpoint, observation, err := c.signTx(tx, height)
	if err != nil {
		err = c.postKeysignFailure(tx, height, err)
	}
	return signedTx, checkpoint, observation, err
}

func (c *Client) BroadcastTx(tx stypes.TxOutItem, txBytes []byte) (string, error) {
	var payload signedTxPayload
	if err := json.Unmarshal(txBytes, &payload); err != nil {
		return "", fmt.Errorf("fail to decode monero signed tx payload: %w", err)
	}
	return c.broadcastTx(tx, payload)
}

func (c *Client) broadcastTx(tx stypes.TxOutItem, payload signedTxPayload) (string, error) {
	if payload.TxHash == "" {
		return "", fmt.Errorf("FROST signed tx payload missing tx hash")
	}
	if strings.TrimSpace(payload.TxMetadata) == "" {
		return "", fmt.Errorf("FROST signed tx payload missing tx_hex for broadcast")
	}
	msgID := strings.TrimSpace(payload.MsgID)
	if msgID == "" {
		return "", fmt.Errorf("FROST signed tx payload missing msg_id")
	}
	vaultPubKey := strings.TrimSpace(txVaultPubKey(tx).String())
	if vaultPubKey == "" {
		return "", fmt.Errorf("FROST signed tx payload missing EDDSA vault_pub_key")
	}

	if c.frostSigner == nil {
		return "", fmt.Errorf("%w: signer engine is not initialized", ErrFrostNotConfigured)
	}

	resp, err := c.frostSigner.FrostBroadcast(xmrFrostBroadcastRequest{
		TxHex:       payload.TxMetadata,
		TxHash:      payload.TxHash,
		VaultPubKey: vaultPubKey,
		MsgID:       msgID,
	})
	if err != nil {
		return "", fmt.Errorf("fail to broadcast FROST tx: %w", err)
	}

	txHash := payload.TxHash
	if strings.TrimSpace(resp.TxHash) != "" {
		txHash = resp.TxHash
	}
	if strings.TrimSpace(txHash) == "" {
		return "", fmt.Errorf("FROST broadcast returned empty tx hash")
	}

	if err := c.signerCacheManager.SetSigned(tx.CacheHash(), txCacheVault(tx, c.GetChain()), txHash); err != nil {
		c.logger.Err(err).Msg("fail to set signer cache after FROST broadcast")
	}
	return txHash, nil
}

func (c *Client) signTx(tx stypes.TxOutItem, height int64) ([]byte, []byte, *stypes.TxInItem, error) {
	if c.frostSigner == nil {
		return nil, nil, nil, fmt.Errorf("%w: signer engine is not initialized", ErrFrostNotConfigured)
	}
	if c.tssServer == nil {
		return nil, nil, nil, errors.New("tss server is not initialized")
	}
	if tx.Coins.IsEmpty() {
		return nil, nil, nil, nil
	}
	if c.signerCacheManager.HasSigned(tx.CacheHash()) {
		c.logger.Info().Interface("tx", tx).Msg("transaction already signed, ignoring")
		return nil, nil, nil, nil
	}
	signReqCtx, err := c.validateXMRSignTx(tx, height, true)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(tx.Checkpoint) > 0 {
		var payload signedTxPayload
		if err := json.Unmarshal(tx.Checkpoint, &payload); err == nil &&
			strings.TrimSpace(payload.TxHash) != "" &&
			strings.TrimSpace(payload.TxMetadata) != "" &&
			strings.TrimSpace(payload.MsgID) != "" {
			if len(payload.TxKeys) == 0 {
				return nil, nil, nil, fmt.Errorf("xmr signing checkpoint is missing tx_keys")
			}
			observation := c.buildOutboundObservation(tx, payload.TxHash, payload.AmountAtomic, payload.FeeAtomic, payload.TxKeys)
			c.logger.Info().Str("tx_hash", payload.TxHash).Msg("reusing checkpointed xmr signed payload for retry")
			return tx.Checkpoint, tx.Checkpoint, observation, nil
		}
	}

	members, memberErr := c.vaultSigningMembers(signReqCtx.vaultPubKey)
	if memberErr != nil {
		return nil, nil, nil, fmt.Errorf("fail to resolve vault signing members for FROST signing: %w", memberErr)
	}
	syncState, syncReason, syncErr := c.xmrLocalSyncStateForVault(nil, signReqCtx.vaultPubKey)
	if syncErr != nil {
		return nil, nil, nil, fmt.Errorf("fail to resolve local xmr sync state before keysign: %w", syncErr)
	}
	capability, capErr := c.xmrLocalCapability(members, syncState, signReqCtx.vaultStatus)
	if capErr != nil {
		return nil, nil, nil, fmt.Errorf("fail to resolve local xmr capability before keysign: %w", capErr)
	}
	if !capability.CanOriginate {
		c.logger.Info().
			Str("vault_pub_key", signReqCtx.vaultPubKey.String()).
			Str("memo", tx.Memo).
			Str("sync_state", string(capability.SyncState)).
			Str("role", string(capability.Role)).
			Str("reason", syncReason).
			Msg("deferring XMR keysign while local node is not allowed to originate work")
		return nil, nil, nil, &xmrSyncingToTipError{reason: syncReason}
	}
	if queued, active, activeWork := c.pendingXMRKeyImageBacklogDetails(); active && xmrKeyImageBacklogConflictsWithSign(activeWork, signReqCtx.vaultPubKey) {
		c.logger.Info().
			Str("vault_pub_key", signReqCtx.vaultPubKey.String()).
			Str("memo", tx.Memo).
			Uint64("pending_key_image_queue", queued).
			Bool("key_image_active", active).
			Str("active_key_image_work_id", activeWork.ID).
			Str("active_key_image_tx_hash", activeWork.TxHash).
			Msg("deferring XMR keysign until key-image backlog clears")
		return nil, nil, nil, &keyImageBacklogError{
			queued:     queued,
			active:     active,
			activeWork: activeWork.ID,
		}
	}
	version, err := c.thorchainBridge.GetThorchainVersion()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to resolve thorchain version: %w", err)
	}
	currentThorHeight := height
	if c.thorchainBridge != nil {
		if thorHeight, heightErr := c.thorchainBridge.GetBlockHeight(); heightErr == nil && thorHeight > 0 {
			currentThorHeight = thorHeight
		}
	}
	coordinationHeight := xmrLeaderEpochHeight(currentThorHeight, c.getSigningTransactionPeriod())

	txInHash := ""
	if !tx.InHash.IsEmpty() {
		txInHash = tx.InHash.String()
	}
	feeRate := xmrSignerFeeRateOverride(tx)
	var maxInputs int64 = 15
	if c.thorchainBridge != nil {
		m, _ := c.thorchainBridge.GetMimir("MaxUTXOsToSpend")
		if m > 0 {
			maxInputs = m
		}
	}
	dustThreshold := common.XMRChain.DustThreshold().Uint64()
	maxGasAtomic, err := xmrMaxGasToAtomic(tx.MaxGas)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to convert xmr max gas to atomic: %w", err)
	}

	frostReq := tssp.FrostKeySignRequest{
		VaultPubKey:   signReqCtx.vaultPubKey.String(),
		ToAddress:     signReqCtx.toAddress,
		AmountAtomic:  signReqCtx.amountAtomic,
		MaxGasAtomic:  maxGasAtomic,
		SweepIfFinal:  signReqCtx.sweepIfFinal,
		Memo:          tx.Memo,
		FeeRate:       feeRate,
		SignerPubKeys: members,
		BlockHeight:   coordinationHeight,
		Version:       version.String(),
		TxInHash:      txInHash,
		RequestID:     tx.SigningID,
		MaxInputs:     maxInputs,
		DustThreshold: dustThreshold,
	}
	msgID, msgIDErr := tssp.FrostKeySignMsgID(frostReq)
	if msgIDErr != nil {
		c.logger.Warn().
			Err(msgIDErr).
			Str("vault_pub_key", signReqCtx.vaultPubKey.String()).
			Str("memo", tx.Memo).
			Msg("failed to derive XMR FROST keysign msg id before ceremony")
	}
	c.logger.Info().
		Str("vault_pub_key", signReqCtx.vaultPubKey.String()).
		Str("memo", tx.Memo).
		Str("msg_id", msgID).
		Int64("txout_height", height).
		Int64("current_thor_height", currentThorHeight).
		Int64("coordination_height", coordinationHeight).
		Strs("signer_pub_keys", members).
		Msg("starting XMR FROST keysign")
	resp, err := c.tssServer.FrostKeySign(frostReq, newFrostSignerAdapter(c.frostSigner))
	if err != nil {
		if isTransientXMRFrostCoordinationText(err.Error()) {
			return nil, nil, nil, &xmrTransientFrostKeySignError{reason: err.Error()}
		}
		return nil, nil, nil, fmt.Errorf("fail to execute FROST keysign: %w", err)
	}
	if resp.Status != gotsscommon.Success {
		if isTransientXMRFrostCoordinationText(resp.Blame.FailReason) {
			return nil, nil, nil, &xmrTransientFrostKeySignError{reason: resp.Blame.FailReason}
		}
		if len(resp.Blame.BlameNodes) > 0 {
			return nil, nil, nil, bifrosttss.NewKeysignError(frostBlameToThorchainBlame(resp.Blame))
		}
		return nil, nil, nil, fmt.Errorf("FROST keysign failed: %s", resp.Blame.FailReason)
	}
	c.observeXMRActualFeeEstimate(height, feeRate, resp.FeeAtomic)
	msgID = strings.TrimSpace(resp.MsgID)
	if msgID == "" {
		return nil, nil, nil, fmt.Errorf("FROST keysign returned empty msg_id")
	}
	if validateErr := tssp.ValidateFrostEventuality(
		frostReq,
		msgID,
		resp.TxHash,
		resp.TxHex,
		resp.FeeAtomic,
		resp.Eventuality,
	); validateErr != nil {
		return nil, nil, nil, fmt.Errorf("invalid FROST eventuality: %w", validateErr)
	}

	localTxKeys := append([]string(nil), resp.TxKeys...)
	if len(localTxKeys) == 0 {
		return nil, nil, nil, fmt.Errorf("FROST keysign returned no tx_keys")
	}
	payload := signedTxPayload{
		TxHash:       resp.TxHash,
		TxMetadata:   resp.TxHex,
		AmountAtomic: resp.AmountAtomic,
		FeeAtomic:    resp.FeeAtomic,
		MsgID:        msgID,
		Eventuality:  resp.Eventuality,
		Broadcasted:  false,
		TxKeys:       localTxKeys,
	}
	signedTx, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to encode FROST signed tx payload: %w", err)
	}
	observation := c.buildOutboundObservation(tx, resp.TxHash, resp.AmountAtomic, resp.FeeAtomic, localTxKeys)
	return signedTx, signedTx, observation, nil
}

func (c *Client) validateXMRSignTx(tx stypes.TxOutItem, thorchainHeight int64, requireThorchainHeight bool) (xmrValidatedSignRequest, error) {
	if tx.ToAddress.IsEmpty() {
		return xmrValidatedSignRequest{}, fmt.Errorf("to address is empty")
	}
	if !tx.ToAddress.IsChain(c.chain) {
		return xmrValidatedSignRequest{}, fmt.Errorf("to address %s is not a valid %s address", tx.ToAddress.String(), c.chain.String())
	}
	// Accept all valid Monero address types: standard (4...), subaddress (8...), featured+guaranteed (C...)
	// The Rust FROST sidecar performs the final network-mode validation.
	// The Rust FROST sidecar performs the final network-mode validation, but
	// Bifrost mirrors the same high-level destination policy to fail fast.
	if requireThorchainHeight && thorchainHeight <= 0 {
		return xmrValidatedSignRequest{}, fmt.Errorf("thorchain height must be greater than zero")
	}
	// Checkpoints are supported — they carry the full signed payload from a
	// previous signing attempt so retries can rebroadcast the same tx.

	memoForParsing := tx.GetMemo()
	if memoForParsing != "" {
		parsedMemo, err := memo.ParseMemo(common.LatestVersion, memoForParsing)
		if err != nil {
			return xmrValidatedSignRequest{}, fmt.Errorf("fail to parse outbound memo(%s): %w", memoForParsing, err)
		}
		if parsedMemo.IsInbound() && !parsedMemo.IsType(memo.TxNoOp) {
			return xmrValidatedSignRequest{}, fmt.Errorf("inbound memo should not be used for outbound tx")
		}
	}

	if len(tx.Coins) != 1 {
		return xmrValidatedSignRequest{}, fmt.Errorf("xmr signing requires exactly one coin, got %d", len(tx.Coins))
	}
	xmrCoin := tx.Coins.GetCoin(common.XMRAsset)
	if xmrCoin.IsEmpty() {
		return xmrValidatedSignRequest{}, fmt.Errorf("xmr tx does not contain XMR coin")
	}
	if xmrCoin.Amount.IsZero() {
		return xmrValidatedSignRequest{}, fmt.Errorf("xmr outbound amount is zero")
	}
	amountAtomic, err := xmrThorchainAmountToAtomic(xmrCoin.Amount)
	if err != nil {
		return xmrValidatedSignRequest{}, fmt.Errorf("fail to convert xmr outbound amount for signing: %w", err)
	}
	if amountAtomic == 0 {
		return xmrValidatedSignRequest{}, fmt.Errorf("xmr outbound amount converted to zero atomic units")
	}

	vaultPubKey := txVaultPubKey(tx)
	if vaultPubKey.IsEmpty() {
		return xmrValidatedSignRequest{}, fmt.Errorf("vault pubkey is empty")
	}
	fromAddress := strings.TrimSpace(c.getVaultAddress(vaultPubKey))
	if fromAddress == "" {
		return xmrValidatedSignRequest{}, fmt.Errorf("fail to derive monero vault address from pubkey %s", vaultPubKey.String())
	}
	from, err := common.NewAddress(fromAddress)
	if err != nil {
		return xmrValidatedSignRequest{}, fmt.Errorf("derived monero vault address %s is invalid: %w", fromAddress, err)
	}
	if !from.IsChain(c.chain) {
		return xmrValidatedSignRequest{}, fmt.Errorf("derived monero vault address %s is not on %s", fromAddress, c.chain.String())
	}

	vaultStatus := ttypes.VaultStatus_InactiveVault
	vault := ttypes.Vault{}
	if c.thorchainBridge != nil {
		vault, err = c.getVaultByPubKey(vaultPubKey)
		if err != nil {
			return xmrValidatedSignRequest{}, fmt.Errorf("fail to resolve xmr vault %s: %w", vaultPubKey.String(), err)
		}
		vaultStatus = vault.Status
	}
	sweepIfFinal := c.shouldSweepXMRFinal(tx, xmrMaxGasToAtomicOrZero(tx.MaxGas), vaultStatus, vault)

	if err = c.ensureXMRVaultBalance(vaultPubKey, amountAtomic, tx.MaxGas, sweepIfFinal); err != nil {
		return xmrValidatedSignRequest{}, err
	}

	return xmrValidatedSignRequest{
		vaultPubKey:  vaultPubKey,
		vaultStatus:  vaultStatus,
		fromAddress:  fromAddress,
		toAddress:    tx.ToAddress.String(),
		amountAtomic: amountAtomic,
		sweepIfFinal: sweepIfFinal,
	}, nil
}

// lookupFrostPubKeyFor resolves the FROST pubkey of an asgard vault given any
// of its other identifiers (eddsa or regular pubkey). Used as a recovery path
// when callers receive an outbound that doesn't carry VaultPubKeyFrost (e.g.
// MIGRATE outbounds prior to the fix populating it from thornode).
func (c *Client) lookupFrostPubKeyFor(pk common.PubKey) common.PubKey {
	if pk.IsEmpty() {
		return common.EmptyPubKey
	}
	asgards, err := c.thorchainBridge.GetAsgards()
	if err != nil {
		return common.EmptyPubKey
	}
	target := pk.String()
	for i := range asgards {
		v := asgards[i]
		if (!v.PubKeyFrost.IsEmpty() && v.PubKeyFrost.String() == target) ||
			(!v.PubKeyEddsa.IsEmpty() && v.PubKeyEddsa.String() == target) ||
			(!v.PubKey.IsEmpty() && v.PubKey.String() == target) {
			return v.PubKeyFrost
		}
	}
	return common.EmptyPubKey
}

func (c *Client) ensureXMRVaultBalance(vaultPubKey common.PubKey, amountAtomic uint64, maxGas common.Gas, sweepIfFinal bool) error {
	if c.frostSigner == nil {
		return nil
	}

	if err := c.ensureFrostVaultRegistration(vaultPubKey); err != nil {
		return err
	}

	balanceVaultPubKey := vaultPubKey.String()
	balance, err := c.frostSigner.GetBalance(vaultPubKey.String())
	if err != nil {
		return fmt.Errorf("fail to verify xmr vault balance for %s: %w", vaultPubKey.String(), err)
	}
	// The XMR sidecar's balance endpoint is keyed on the vault's FROST pubkey.
	// If we were given an EDDSA pubkey (e.g. on a MIGRATE outbound that
	// pre-dates the fix populating VaultPubKeyFrost), the lookup returns
	// {balance:0,...}. Resolve via the Asgard set so we can re-query with the
	// FROST pubkey and avoid a false-negative insufficient-balance error.
	if balance.Balance == 0 && balance.UnlockedBalance == 0 {
		if frostPK := c.lookupFrostPubKeyFor(vaultPubKey); !frostPK.IsEmpty() && frostPK.String() != vaultPubKey.String() {
			frostBalance, frostErr := c.frostSigner.GetBalance(frostPK.String())
			if frostErr == nil && (frostBalance.Balance > 0 || frostBalance.UnlockedBalance > 0) {
				balance = frostBalance
				balanceVaultPubKey = frostPK.String()
			}
		}
	}
	availableAtomic := spendableBalanceAtomic(balance)

	maxGasAtomic, err := xmrMaxGasToAtomic(maxGas)
	if err != nil {
		return fmt.Errorf("fail to convert xmr max gas for balance guard: %w", err)
	}

	requiredAtomic := amountAtomic
	if maxGasAtomic > 0 {
		if requiredAtomic > (^uint64(0) - maxGasAtomic) {
			return fmt.Errorf("xmr outbound amount overflows uint64 when max gas is applied")
		}
		requiredAtomic += maxGasAtomic
	}

	if availableAtomic < requiredAtomic {
		if sweepIfFinal && availableAtomic > maxGasAtomic {
			return nil
		}
		totalAtomic := balance.Balance
		lockedAtomic := totalAtomic - availableAtomic
		if lockedAtomic > 0 {
			diagnostics := c.xmrLockedOutputDiagnostics(balanceVaultPubKey, availableAtomic, requiredAtomic)
			return &lockedOutputsError{
				available:                   availableAtomic,
				required:                    requiredAtomic,
				total:                       totalAtomic,
				locked:                      lockedAtomic,
				confirmationsRemaining:      diagnostics.confirmationsRemaining,
				minConfirmationsRemaining:   diagnostics.minConfirmationsRemaining,
				maxConfirmationsRemaining:   diagnostics.maxConfirmationsRemaining,
				lockedOutputCount:           diagnostics.lockedOutputCount,
				confirmationDetailsObserved: diagnostics.observed,
			}
		}
		return fmt.Errorf(
			"insufficient xmr balance for signing: available=%d required=%d (amount=%d max_gas=%d)",
			availableAtomic, requiredAtomic, amountAtomic, maxGasAtomic,
		)
	}

	return nil
}

func spendableBalanceAtomic(balance xmrFrostBalanceResponse) uint64 {
	availableAtomic := balance.UnlockedBalance
	if availableAtomic > balance.Balance {
		availableAtomic = balance.Balance
	}
	if balance.SpendableBalance != nil {
		availableAtomic = *balance.SpendableBalance
		if availableAtomic > balance.Balance {
			availableAtomic = balance.Balance
		}
	}
	return availableAtomic
}

type xmrLockedOutputDiagnostics struct {
	observed                  bool
	confirmationsRemaining    uint64
	minConfirmationsRemaining uint64
	maxConfirmationsRemaining uint64
	lockedOutputCount         int
}

type xmrLockedOutputCandidate struct {
	amount               uint64
	confirmationsToSpend uint64
}

func (c *Client) xmrLockedOutputDiagnostics(vaultPubKey string, availableAtomic, requiredAtomic uint64) xmrLockedOutputDiagnostics {
	if c.frostSigner == nil {
		return xmrLockedOutputDiagnostics{}
	}
	currentHeight, err := c.frostSigner.GetChainHeight()
	if err != nil || currentHeight < 0 {
		return xmrLockedOutputDiagnostics{}
	}
	outputs, err := c.frostSigner.GetAllOutputs(vaultPubKey)
	if err != nil {
		return xmrLockedOutputDiagnostics{}
	}
	if requiredAtomic <= availableAtomic {
		return xmrLockedOutputDiagnostics{observed: true}
	}

	requiredShortfall := requiredAtomic - availableAtomic
	candidates := make([]xmrLockedOutputCandidate, 0, len(outputs))
	for _, output := range outputs {
		if output.IsSpent() {
			continue
		}
		confirmations := uint64(0)
		if currentHeight > int64(output.Height) {
			confirmations = uint64(currentHeight - int64(output.Height))
		}
		if confirmations >= xmrSpendableConfirmations {
			continue
		}
		candidates = append(candidates, xmrLockedOutputCandidate{
			amount:               output.Amount,
			confirmationsToSpend: xmrSpendableConfirmations - confirmations,
		})
	}
	if len(candidates) == 0 {
		return xmrLockedOutputDiagnostics{observed: true}
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].confirmationsToSpend == candidates[j].confirmationsToSpend {
			return candidates[i].amount > candidates[j].amount
		}
		return candidates[i].confirmationsToSpend < candidates[j].confirmationsToSpend
	})

	diagnostics := xmrLockedOutputDiagnostics{
		observed:                  true,
		minConfirmationsRemaining: candidates[0].confirmationsToSpend,
		maxConfirmationsRemaining: candidates[0].confirmationsToSpend,
		lockedOutputCount:         len(candidates),
		confirmationsRemaining:    candidates[len(candidates)-1].confirmationsToSpend,
	}
	var unlockedByThen uint64
	for _, candidate := range candidates {
		if candidate.confirmationsToSpend > diagnostics.maxConfirmationsRemaining {
			diagnostics.maxConfirmationsRemaining = candidate.confirmationsToSpend
		}
		if unlockedByThen <= math.MaxUint64-candidate.amount {
			unlockedByThen += candidate.amount
		} else {
			unlockedByThen = math.MaxUint64
		}
		if unlockedByThen >= requiredShortfall {
			diagnostics.confirmationsRemaining = candidate.confirmationsToSpend
			break
		}
	}
	return diagnostics
}

func xmrMaxGasToAtomic(maxGas common.Gas) (uint64, error) {
	if len(maxGas) == 0 {
		return 0, nil
	}
	xmrGasCoin := common.Coins(maxGas).GetCoin(common.XMRAsset)
	if xmrGasCoin.IsEmpty() || xmrGasCoin.Amount.IsZero() {
		return 0, nil
	}
	return xmrThorchainAmountToAtomic(xmrGasCoin.Amount)
}

func xmrSignerFeeRateOverride(tx stypes.TxOutItem) uint64 {
	if tx.GasRate <= 0 {
		return 0
	}

	if parsedMemo, err := memo.ParseMemo(common.LatestVersion, tx.GetMemo()); err == nil && parsedMemo.GetType() == memo.TxConsolidate {
		return uint64(tx.GasRate)
	}

	gasRate := uint64(tx.GasRate)
	if gasRate > math.MaxUint64/moneroAtomicToThorchain {
		return math.MaxUint64
	}
	return gasRate * moneroAtomicToThorchain
}

func xmrNecessaryFeeFromError(err error) uint64 {
	if err == nil {
		return 0
	}
	return xmrNecessaryFeeFromString(err.Error())
}

func xmrNecessaryFeeFromString(s string) uint64 {
	matches := xmrNecessaryFeePattern.FindStringSubmatch(s)
	if len(matches) != 2 {
		return 0
	}
	fee, err := strconv.ParseUint(matches[1], 10, 64)
	if err != nil {
		return 0
	}
	return fee
}

func xmrWeightFromActualFee(feeAtomic, feeRatePerWeight uint64) uint64 {
	if feeAtomic == 0 || feeRatePerWeight == 0 {
		return 0
	}
	return (feeAtomic + feeRatePerWeight - 1) / feeRatePerWeight
}

func (c *Client) observeXMRActualFeeEstimate(height int64, feeRatePerWeight, feeAtomic uint64) {
	weight := xmrWeightFromActualFee(feeAtomic, feeRatePerWeight)
	if weight == 0 {
		return
	}
	c.observeXMRNetworkFeeEstimate(height, feeAtomic, weight)
}

func (c *Client) observeXMRNetworkFeeEstimate(height int64, feeAtomic, weight uint64) {
	if c == nil || c.scanner == nil || feeAtomic == 0 || weight == 0 {
		return
	}
	c.scanner.ObserveFeeEstimate(feeAtomic, weight)
	if c.thorchainBridge == nil {
		return
	}

	transactionSize, transactionRate := c.scanner.GetNetworkFee()
	currentSize, currentRate, err := c.thorchainBridge.GetNetworkFee(common.XMRChain)
	if err != nil {
		c.logger.Debug().Err(err).Msg("fail to fetch current XMR network fee before posting observed estimate")
	}
	currentTotal := currentSize * currentRate
	observedTotal := transactionSize * transactionRate
	if currentRate != 0 && observedTotal <= currentTotal {
		return
	}
	if _, postErr := c.thorchainBridge.PostNetworkFee(height, common.XMRChain, transactionSize, transactionRate); postErr != nil {
		c.logger.Warn().
			Err(postErr).
			Int64("height", height).
			Uint64("size", transactionSize).
			Uint64("rate", transactionRate).
			Msg("fail to post observed XMR network fee estimate")
		return
	}
	c.logger.Info().
		Int64("height", height).
		Uint64("size", transactionSize).
		Uint64("rate", transactionRate).
		Uint64("fee_atomic", feeAtomic).
		Msg("posted observed XMR network fee estimate")
}

func xmrMaxGasToAtomicOrZero(maxGas common.Gas) uint64 {
	v, err := xmrMaxGasToAtomic(maxGas)
	if err != nil {
		return 0
	}
	return v
}

func shouldSweepXMRMigration(tx stypes.TxOutItem, maxGasAtomic uint64, vault ttypes.Vault) bool {
	if maxGasAtomic == 0 || !isXMRMigrateMemo(tx.GetMemo()) {
		return false
	}
	xmrCoin := tx.Coins.GetCoin(common.XMRAsset)
	if xmrCoin.IsEmpty() {
		return false
	}
	maxGasCoin := tx.MaxGas.ToCoins().GetCoin(common.XMRAsset)
	if maxGasCoin.IsEmpty() {
		return false
	}
	vaultCoin := vault.GetCoin(common.XMRAsset)
	if vaultCoin.IsEmpty() {
		return false
	}
	return xmrCoin.Amount.Add(maxGasCoin.Amount).Equal(vaultCoin.Amount)
}

func (c *Client) shouldSweepXMRFinal(tx stypes.TxOutItem, maxGasAtomic uint64, vaultStatus ttypes.VaultStatus, vault ttypes.Vault) bool {
	if shouldSweepXMRMigration(tx, maxGasAtomic, vault) {
		return true
	}
	if maxGasAtomic == 0 || vaultStatus != ttypes.VaultStatus_InactiveVault {
		return false
	}
	return c.isKnownVaultAddress(tx.ToAddress.String())
}

func isXMRMigrateMemo(memoText string) bool {
	memoText = strings.TrimSpace(memoText)
	if memoText == "" {
		return false
	}
	parsedMemo, err := memo.ParseMemo(common.LatestVersion, memoText)
	if err != nil {
		return false
	}
	return parsedMemo.GetType() == memo.TxMigrate
}

func (c *Client) buildOutboundObservation(tx stypes.TxOutItem, txHash string, amountAtomic, feeAtomic uint64, txKeys []string) *stypes.TxInItem {
	vaultPubKey := txVaultPubKey(tx)
	from := c.getVaultAddress(vaultPubKey)
	height := int64(0)
	if c.scanner != nil {
		height = c.scanner.CurrentHeight()
	}
	if from == "" || tx.ToAddress.IsEmpty() {
		return nil
	}

	fee := feeAtomic / moneroAtomicToThorchain
	if fee == 0 && c.scanner != nil {
		txSize, feeRate := c.scanner.GetNetworkFee()
		fee = totalFeeAtomicToThorchain(txSize * feeRate)
	}
	coins := tx.Coins
	if amountAtomic > 0 {
		coins = common.NewCoins(
			common.NewCoin(common.XMRAsset, cosmos.NewUint(amountAtomic/moneroAtomicToThorchain)),
		)
	}
	item := stypes.NewTxInItem(
		height,
		txHash,
		tx.GetMemo(),
		from,
		tx.ToAddress.String(),
		coins,
		common.Gas{common.NewCoin(common.XMRAsset, cosmos.NewUint(fee))},
		vaultPubKey,
		"",
		"",
		nil,
	)
	if item != nil && len(txKeys) > 0 {
		item.TxKeys = append([]string(nil), txKeys...)
	}
	return item
}

func (c *Client) ValidateOutboundObservation(tx common.ObservedTx) error {
	if tx.Tx.Chain != common.XMRChain {
		return fmt.Errorf("unexpected chain for xmr outbound validation: %s", tx.Tx.Chain)
	}
	if tx.Tx.IsXMRUnauthorizedSpendObservation() {
		return nil
	}
	if c.frostSigner == nil {
		return fmt.Errorf("frost signer not configured")
	}
	if tx.Tx.ID.IsEmpty() {
		return fmt.Errorf("observed tx id is empty")
	}
	if tx.ObservedPubKey.IsEmpty() {
		return fmt.Errorf("observed tx vault pubkey is empty")
	}
	if tx.Tx.ToAddress.IsEmpty() {
		return fmt.Errorf("observed tx destination is empty")
	}
	if len(tx.TxKeys) == 0 {
		return fmt.Errorf("xmr finalized outbound is missing tx keys for %s", tx.Tx.ID)
	}

	trackedOutboundChange := len(tx.XmrKeyImages) > 0 || isXMRConsolidateSelfSendObservedTx(tx)
	outgoingAudit, err := c.lookupXMROutgoingAudit(tx.ObservedPubKey, tx.Tx.ID.String())
	if err != nil {
		return fmt.Errorf("fail to query xmr outbound audit via sidecar: %w", err)
	}
	tracked, err := c.resolveTrackedXMROutboundChange(outgoingAudit)
	if errors.Is(err, errXMROutgoingAuditIncomplete) {
		outgoingAudit, err = c.validateXMROutgoingAuditFromObservation(tx, outgoingAudit)
		if err != nil {
			return fmt.Errorf("fail to validate xmr outbound tx-key state via sidecar: %w", err)
		}
		tracked, err = c.resolveTrackedXMROutboundChange(outgoingAudit)
	}
	if err != nil {
		return fmt.Errorf("fail to resolve xmr outbound audit from signer state: %w", err)
	}
	trackedOutboundChange = trackedOutboundChange || tracked
	return c.validateObservedTxKeyImages(tx, false, trackedOutboundChange)
}

func (c *Client) ExportFrostKeyshare(groupKey common.PubKey) ([]byte, error) {
	if c.frostSigner == nil {
		return nil, fmt.Errorf("frost signer not configured")
	}
	hexKey, err := c.frostSigner.ExportFrostKeys(groupKey.String())
	if err != nil {
		return nil, fmt.Errorf("fail to export frost keys for group key %s: %w", groupKey.String(), err)
	}
	return []byte(hexKey), nil
}

func (c *Client) RestoreFrostKeyshare(vaultPubKey common.PubKey, keyBytesHex string) error {
	if c.frostSigner == nil {
		return fmt.Errorf("frost signer not configured")
	}
	restored, err := c.frostSigner.RestoreFrostKeys(vaultPubKey.String(), keyBytesHex)
	if err != nil {
		return err
	}
	if restored {
		c.logger.Info().Str("vault", vaultPubKey.String()).Msg("restored FROST keyshare from on-chain backup")
	}
	return nil
}

func (c *Client) PrepareObservedTxInItem(item *stypes.TxInItem, finalized bool, finaliseHeight int64) (*stypes.TxInItem, bool, error) {
	if item == nil || !finalized || c.frostSigner == nil || c.tssServer == nil {
		return item, item != nil, nil
	}
	isInboundToVault := c.isObservedInboundToVault(item)
	isOutboundFromVault := c.isObservedOutboundFromVault(item)
	if !isInboundToVault && !isOutboundFromVault {
		return item, true, nil
	}
	if isOutboundFromVault && (common.Tx{Chain: common.XMRChain, Memo: item.Memo}).IsXMRUnauthorizedSpendObservation() {
		alreadyObserved, err := c.xmrUnauthorizedSpendAlreadyObserved(item.Tx)
		if err != nil {
			c.logger.Warn().
				Err(err).
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("vault_pub_key", item.ObservedVaultPubKey.String()).
				Msg("failed to check existing XMR unauthorized spend observation")
		}
		if alreadyObserved {
			c.logger.Info().
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("vault_pub_key", item.ObservedVaultPubKey.String()).
				Msg("skipping already observed XMR unauthorized spend")
			return nil, false, nil
		}
		knownOutgoing, err := c.xmrUnauthorizedSpendKnownOutgoing(item.ObservedVaultPubKey, item.Tx)
		if err != nil {
			c.logger.Warn().
				Err(err).
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("vault_pub_key", item.ObservedVaultPubKey.String()).
				Msg("failed to check signer outgoing state for XMR unauthorized spend observation")
		}
		if knownOutgoing {
			c.logger.Info().
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("vault_pub_key", item.ObservedVaultPubKey.String()).
				Msg("skipping signer-owned XMR outbound reported as unauthorized spend")
			return nil, false, nil
		}
		return item, true, nil
	}
	var outgoingAudit xmrOutgoingAuditLookup
	if isOutboundFromVault {
		var auditErr error
		outgoingAudit, auditErr = c.lookupXMROutgoingAudit(item.ObservedVaultPubKey, item.Tx)
		if auditErr != nil {
			c.logger.Warn().
				Err(auditErr).
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("vault_pub_key", item.ObservedVaultPubKey.String()).
				Msg("failed to query XMR outgoing audit state from signer")
		} else {
			if !outgoingAudit.canonicalVaultPubKey.IsEmpty() &&
				!outgoingAudit.canonicalVaultPubKey.Equals(item.ObservedVaultPubKey) {
				c.logger.Info().
					Str("observed_vault_pub_key", item.ObservedVaultPubKey.String()).
					Str("canonical_vault_pub_key", outgoingAudit.canonicalVaultPubKey.String()).
					Str("tx_hash", normalizeTxID(item.Tx)).
					Msg("canonicalized XMR outbound audit vault context")
				item.ObservedVaultPubKey = outgoingAudit.canonicalVaultPubKey
			}
			if len(item.TxKeys) == 0 && outgoingAudit.record != nil {
				txKeys := normalizeTxKeyValues(outgoingAudit.record.TxKeys)
				if len(txKeys) > 0 {
					item.TxKeys = txKeys
					c.logger.Info().
						Str("tx_hash", normalizeTxID(item.Tx)).
						Str("vault_pub_key", item.ObservedVaultPubKey.String()).
						Int("tx_key_count", len(txKeys)).
						Msg("recovered missing XMR tx-keys from signer outgoing audit state")
				}
			}
		}
	}
	if isOutboundFromVault && len(item.TxKeys) == 0 && len(item.XmrKeyImages) > 0 {
		if txKeys, ok := c.registeredXMRTxKeysForTx(item.ObservedVaultPubKey, item.Tx); ok {
			item.TxKeys = txKeys
			c.logger.Info().
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("vault_pub_key", item.ObservedVaultPubKey.String()).
				Int("tx_key_count", len(txKeys)).
				Msg("recovered missing XMR tx-keys from registered vault inventory")
		}
	}
	if isInboundToVault && !isOutboundFromVault && len(item.XmrKeyImages) == 0 && c.shouldSkipObservedXMRInboundChange(item.Memo, item.Sender, item.ObservedVaultPubKey) {
		c.logger.Debug().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Str("observed_vault_pub_key", item.ObservedVaultPubKey.String()).
			Str("sender", item.Sender).
			Str("memo", item.Memo).
			Msg("skipping delayed XMR change inbound; key-images will ride on finalized outbound")
		return nil, false, nil
	}
	if isInboundToVault && isXMRMigrateMemo(item.Memo) && len(item.TxKeys) == 0 {
		if txKeys, sourceVaultPubKey, ok := c.registeredXMRTxKeysForMigrateSender(item); ok {
			c.logger.Info().
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("observed_vault_pub_key", item.ObservedVaultPubKey.String()).
				Str("source_vault_pub_key", sourceVaultPubKey.String()).
				Int("tx_key_count", len(txKeys)).
				Str("memo", item.Memo).
				Msg("found XMR migrate tx-key on spending vault; continuing finalized migrate inbound observation")
		} else {
			c.logger.Info().
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("observed_vault_pub_key", item.ObservedVaultPubKey.String()).
				Str("sender", item.Sender).
				Str("memo", item.Memo).
				Msg("waiting for XMR tx-key propagation before finalized migrate inbound observation")
			return nil, false, nil
		}
	}
	if isOutboundFromVault && len(item.TxKeys) > 0 {
		txKeysAlreadyRegistered := len(item.XmrKeyImages) > 0 && c.xmrTxKeysAlreadyRegistered(item.ObservedVaultPubKey, item.Tx)
		if !txKeysAlreadyRegistered {
			workItem, workErr := c.ensureXMRFinalizeWorkItem(
				item.ObservedVaultPubKey,
				item.Tx,
				finaliseHeight,
				xmrFinalizePhaseTxKey,
			)
			if workErr != nil {
				return nil, false, fmt.Errorf("fail to initialize XMR tx-key validation work item: %w", workErr)
			}
			if ready, wait := xmrOutboundTxKeyValidationRetryReady(workItem, time.Now()); !ready {
				c.logger.Debug().
					Str("tx_hash", normalizeTxID(item.Tx)).
					Str("vault_pub_key", item.ObservedVaultPubKey.String()).
					Int64("attempt_count", workItem.AttemptCount).
					Dur("retry_after", wait).
					Msg("waiting for XMR outbound tx-key validation retry backoff")
				return nil, false, nil
			}
			validatedAudit, validateErr := c.validateXMROutgoingAuditFromObservation(xmrObservedTxFromTxInItem(item), outgoingAudit)
			if validateErr != nil {
				if isMissingXMROutboundValidationTx(validateErr) {
					currentHeight := c.currentThorHeight(finaliseHeight)
					var recordErr error
					workItem, recordErr = c.recordXMRKeyImageWorkAttempt(workItem, currentHeight, validateErr.Error())
					if recordErr != nil {
						return nil, false, fmt.Errorf("fail to record XMR tx-key validation attempt: %w", recordErr)
					}
					if workItem.AttemptCount >= xmrMissingOutboundTxRetryLimit {
						return nil, false, fmt.Errorf("%w: xmr outbound tx-key validation failed after %d attempts: %w", types.ErrTerminalObservation, workItem.AttemptCount, validateErr)
					}
					c.logger.Info().
						Err(validateErr).
						Str("tx_hash", normalizeTxID(item.Tx)).
						Str("vault_pub_key", item.ObservedVaultPubKey.String()).
						Int64("attempt_count", workItem.AttemptCount).
						Str("memo", item.Memo).
						Msg("waiting for XMR outbound tx to become visible before tx-key validation")
					return nil, false, nil
				}
				if isTransientXMROutboundValidationError(validateErr) {
					currentHeight := c.currentThorHeight(finaliseHeight)
					var recordErr error
					workItem, recordErr = c.recordXMRKeyImageWorkAttempt(workItem, currentHeight, validateErr.Error())
					if recordErr != nil {
						return nil, false, fmt.Errorf("fail to record XMR tx-key validation attempt: %w", recordErr)
					}
					c.logger.Info().
						Err(validateErr).
						Str("tx_hash", normalizeTxID(item.Tx)).
						Str("vault_pub_key", item.ObservedVaultPubKey.String()).
						Int64("attempt_count", workItem.AttemptCount).
						Str("memo", item.Memo).
						Msg("waiting for XMR outbound tx to become visible before tx-key validation")
					return nil, false, nil
				}
				return nil, false, fmt.Errorf("fail to validate xmr outbound tx-key state via sidecar: %w", validateErr)
			}
			outgoingAudit = validatedAudit
			if setTxInItemXMRGasFromActualFee(item, outgoingAudit.actualFeeAtomic) {
				c.logger.Info().
					Str("tx_hash", normalizeTxID(item.Tx)).
					Str("vault_pub_key", item.ObservedVaultPubKey.String()).
					Uint64("actual_fee", outgoingAudit.actualFeeAtomic/moneroAtomicToThorchain).
					Msg("updated finalized XMR outbound gas from tx-key validation")
			}
		}
	}
	if isOutboundFromVault && len(item.XmrKeyImages) > 0 {
		kept, stripErr := c.stripMismatchedOutboundXMRKeyImages(item)
		if stripErr != nil {
			return nil, false, stripErr
		}
		if !kept {
			c.logger.Info().
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("vault_pub_key", item.ObservedVaultPubKey.String()).
				Str("memo", item.Memo).
				Msg("stripped XMR key-images that do not match outbound vault outputs")
		}
	}
	if len(item.XmrKeyImages) > 0 {
		if isOutboundFromVault && len(item.TxKeys) == 0 {
			c.logger.Info().
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("vault_pub_key", item.ObservedVaultPubKey.String()).
				Str("memo", item.Memo).
				Int("key_image_count", len(item.XmrKeyImages)).
				Msg("waiting for XMR tx-key propagation before finalized outbound observation")
			return nil, false, nil
		}
		c.logger.Debug().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Int("proof_count", len(item.XmrKeyImages)).
			Msg("observed tx item already has XMR key-images")
		return item, true, nil
	}

	trackedOutboundChange := len(item.XmrKeyImages) > 0 || isXMRConsolidateSelfSendItem(item)
	if isOutboundFromVault {
		if len(item.TxKeys) == 0 {
			c.logger.Info().
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("vault_pub_key", item.ObservedVaultPubKey.String()).
				Str("memo", item.Memo).
				Msg("waiting for XMR tx-key propagation before finalized outbound observation")
			return nil, false, nil
		}
		tracked, trackedErr := c.resolveTrackedXMROutboundChange(outgoingAudit)
		if trackedErr != nil {
			if errors.Is(trackedErr, errXMROutgoingAuditIncomplete) {
				c.logger.Info().
					Str("tx_hash", normalizeTxID(item.Tx)).
					Str("vault_pub_key", item.ObservedVaultPubKey.String()).
					Str("memo", item.Memo).
					Msg("waiting for signer-owned XMR outbound audit state before finalized outbound observation")
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("fail to resolve xmr outbound audit from signer state: %w", trackedErr)
		}
		trackedOutboundChange = trackedOutboundChange || tracked
		if trackedOutboundChange &&
			outgoingAudit.record != nil &&
			outgoingAudit.record.localChangeMaterializedKnown() &&
			!outgoingAudit.record.localChangeMaterialized() {
			c.logger.Info().
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("vault_pub_key", item.ObservedVaultPubKey.String()).
				Str("memo", item.Memo).
				Str("pending_reason", strings.TrimSpace(outgoingAudit.record.pendingLifecycleReason())).
				Msg("finalized XMR outbound is still waiting for tracked change outputs to materialize")
			return nil, false, nil
		}
	}

	ceremonyCtx, err := c.resolveXMRKeyImageCeremonyContext(item.ObservedVaultPubKey)
	if err != nil {
		return nil, false, fmt.Errorf("fail to resolve XMR vault signing members: %w", err)
	}
	if !ceremonyCtx.vaultPubKey.Equals(item.ObservedVaultPubKey) {
		c.logger.Info().
			Str("observed_vault_pub_key", item.ObservedVaultPubKey.String()).
			Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
			Str("tx_hash", normalizeTxID(item.Tx)).
			Msg("canonicalized XMR key-image vault context")
		item.ObservedVaultPubKey = ceremonyCtx.vaultPubKey
	}
	syncState, syncReason, syncErr := c.xmrLocalSyncStateForVault(nil, ceremonyCtx.vaultPubKey)
	if syncErr != nil {
		return nil, false, fmt.Errorf("fail to resolve local xmr sync state for finalized observation: %w", syncErr)
	}
	capability, capErr := c.xmrLocalCapability(ceremonyCtx.members, syncState, ceremonyCtx.vaultStatus)
	if capErr != nil {
		return nil, false, fmt.Errorf("fail to resolve local xmr capability for finalized observation: %w", capErr)
	}
	if !capability.CanOriginate {
		c.logger.Info().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
			Str("role", string(capability.Role)).
			Str("sync_state", string(capability.SyncState)).
			Str("reason", syncReason).
			Msg("waiting for a locally ready XMR originator before finalized key-image observation")
		return nil, false, nil
	}
	if readiness, ok := c.tssServer.(xmrKeyImagePeerReadiness); ok {
		missingPeers, readinessErr := readiness.MissingPeerAddresses(ceremonyCtx.members)
		if readinessErr != nil {
			return nil, false, fmt.Errorf("fail to inspect XMR signer peer readiness: %w", readinessErr)
		}
		if len(missingPeers) > 0 {
			c.logger.Info().
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
				Strs("missing_signer_pub_keys", missingPeers).
				Msg("waiting for XMR signer peer discovery before finalized key-image observation")
			return nil, false, nil
		}
	}

	allOutputs, err := c.frostSigner.GetAllOutputs(ceremonyCtx.vaultPubKey.String())
	if err != nil {
		return nil, false, fmt.Errorf("fail to query tracked outputs for vault %s: %w", ceremonyCtx.vaultPubKey, err)
	}
	outputs := filterVaultOutputsForTx(allOutputs, item.Tx)
	keyImageOutputs := xmrOutputsRequiringKeyImages(outputs)
	pendingOutputs := xmrOutputsMissingKeyImages(keyImageOutputs)
	backlogOutputs := xmrOutputsMissingKeyImages(allOutputs)
	backlogHeadTxHash := ""
	if len(backlogOutputs) > 0 {
		backlogHeadTxHash = normalizeTxID(backlogOutputs[0].TxHash)
	}
	if len(outputs) == 0 {
		if isOutboundFromVault {
			if trackedOutboundChange {
				c.logger.Info().
					Str("tx_hash", normalizeTxID(item.Tx)).
					Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
					Str("memo", item.Memo).
					Msg("finalized XMR outbound expects tracked vault-owned change outputs locally; retrying key-image preparation later")
				return nil, false, nil
			}
			c.logger.Info().
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
				Str("memo", item.Memo).
				Msg("finalized XMR outbound has no tracked vault-owned outputs; no key-image ceremony needed")
			return item, true, nil
		}
		c.logger.Info().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
			Msg("finalized XMR inbound has no tracked outputs locally yet; retrying key-image preparation later")
		return nil, false, nil
	}
	if len(keyImageOutputs) == 0 {
		c.logger.Info().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
			Str("memo", item.Memo).
			Msg("finalized XMR observation has only zero-amount vault-owned outputs; no key-image ceremony needed")
		return item, true, nil
	}
	if cachedProofs, ok := c.getCachedXMRKeyImageProofs(ceremonyCtx.vaultPubKey, normalizeTxID(item.Tx), keyImageOutputs); ok && len(cachedProofs) == len(keyImageOutputs) {
		item.XmrKeyImages = cachedProofs
		c.logger.Info().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
			Int("proof_count", len(cachedProofs)).
			Msg("reusing cached XMR key-images for finalized observation")
		return item, true, nil
	}
	ceremonyOutputs := pendingOutputs
	if len(ceremonyOutputs) == 0 {
		ceremonyOutputs = keyImageOutputs
	}
	if len(pendingOutputs) != 0 && len(pendingOutputs) != len(keyImageOutputs) {
		c.logger.Info().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
			Int("tracked_output_count", len(outputs)).
			Int("key_image_output_count", len(keyImageOutputs)).
			Int("missing_key_image_output_count", len(pendingOutputs)).
			Msg("waiting for coherent XMR key-image proof propagation before retrying finalized observation")
		return nil, false, nil
	}
	if backlogHeadTxHash != "" && backlogHeadTxHash != normalizeTxID(item.Tx) {
		c.logger.Info().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
			Str("backlog_head_tx_hash", backlogHeadTxHash).
			Int("pending_key_image_output_count", len(backlogOutputs)).
			Int("pending_key_image_tx_count", xmrPendingKeyImageTxCount(backlogOutputs)).
			Msg("waiting for earlier XMR key-image backlog item before finalized observation")
		return nil, false, nil
	}

	version, err := c.thorchainBridge.GetThorchainVersion()
	if err != nil {
		return nil, false, fmt.Errorf("fail to resolve thorchain version for XMR key-images: %w", err)
	}

	c.logger.Info().
		Str("tx_hash", normalizeTxID(item.Tx)).
		Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
		Bool("inbound", isInboundToVault).
		Bool("outbound", isOutboundFromVault).
		Int("tracked_output_count", len(outputs)).
		Int("key_image_output_count", len(keyImageOutputs)).
		Int("missing_key_image_output_count", len(pendingOutputs)).
		Int64("finalise_height", finaliseHeight).
		Str("memo", item.Memo).
		Msg("preparing finalized XMR observation for key-image generation")
	proofs, err := c.generateObservedTxKeyImages(
		ceremonyCtx.vaultPubKey,
		normalizeTxID(item.Tx),
		ceremonyOutputs,
		ceremonyCtx.members,
		finaliseHeight,
		xmrWorkKindFinalizeObservation,
		xmrFinalizePhaseKeyImage,
		version.String(),
	)
	if err != nil {
		if errors.Is(err, errXMRFinalizeWorkRetryBackoff) {
			c.logger.Debug().
				Err(err).
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
				Msg("waiting for finalized XMR key-image retry backoff")
			return nil, false, nil
		}
		if errors.Is(err, errXMRKeyImageNotActiveSigner) {
			c.logger.Info().
				Err(err).
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
				Msg("waiting for finalized XMR key-images from the active signer subset")
			return nil, false, nil
		}
		if isTransientXMRKeyImageCeremonyError(err) {
			c.logger.Info().
				Err(err).
				Str("tx_hash", normalizeTxID(item.Tx)).
				Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
				Msg("transient XMR key-image ceremony failure; retrying finalized observation later")
			return nil, false, nil
		}
		return nil, false, err
	}
	c.putCachedXMRKeyImageProofs(ceremonyCtx.vaultPubKey, normalizeTxID(item.Tx), keyImageOutputs, proofs)
	item.XmrKeyImages = proofs
	c.logger.Info().
		Str("tx_hash", normalizeTxID(item.Tx)).
		Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
		Int("proof_count", len(proofs)).
		Msg("attached XMR key-images to finalized observation")
	return item, true, nil
}

func (c *Client) stripMismatchedOutboundXMRKeyImages(item *stypes.TxInItem) (bool, error) {
	if item == nil || len(item.XmrKeyImages) == 0 || c.frostSigner == nil {
		return true, nil
	}
	outputs, err := c.collectVaultOutputsForTx(item.ObservedVaultPubKey, item.Tx)
	if err != nil {
		return false, err
	}
	outputs = xmrOutputsRequiringKeyImages(outputs)
	proofs := canonicalXMRKeyImageProofs(item.XmrKeyImages)
	if len(proofs) != len(outputs) {
		item.XmrKeyImages = nil
		return false, nil
	}
	for i, output := range outputs {
		proof := proofs[i]
		if !strings.EqualFold(proof.TxHash, output.TxHash) || proof.OutputIndex != output.OutputIndex {
			item.XmrKeyImages = nil
			return false, nil
		}
	}
	return true, nil
}

func (c *Client) ValidateObservedTxIn(tx common.ObservedTx) error {
	return c.validateObservedTxKeyImages(tx, true, false)
}

func (c *Client) IsDeckItemStale(item *stypes.TxInItem) bool {
	if item == nil || c.thorchainBridge == nil {
		return false
	}
	txHash := normalizeTxID(item.Tx)
	if txHash == "" {
		return false
	}
	vault, err := c.thorchainBridge.GetVault(item.ObservedVaultPubKey.String())
	if err != nil {
		return false
	}
	if len(item.XmrKeyImages) > 0 {
		if !xmrKeyImageProofsAlreadyRegistered(item.XmrKeyImages, vault.KeyImagesXmr) {
			return false
		}
		return c.ensureRegisteredXMRKeyImagesAppliedForDeckItem(item, vault)
	}
	for _, ref := range vault.TxKeysXmr {
		if ref != nil && normalizeTxID(ref.TxHash) == txHash {
			if c.isObservedInboundToVault(item) && !xmrKeyImagesRegisteredForTx(txHash, vault.KeyImagesXmr) {
				return false
			}
			return true
		}
	}
	for _, proof := range vault.KeyImagesXmr {
		if proof != nil && normalizeTxID(proof.TxHash) == txHash {
			return c.ensureRegisteredXMRKeyImagesAppliedForDeckItem(item, vault)
		}
	}
	return false
}

func (c *Client) ensureRegisteredXMRKeyImagesAppliedForDeckItem(item *stypes.TxInItem, vault ttypes.Vault) bool {
	if c == nil || item == nil || c.frostSigner == nil {
		return true
	}
	txHash := normalizeTxID(item.Tx)
	vaultPubKey := keyImageCatchupVaultPubKey(vault)
	if vaultPubKey.IsEmpty() {
		vaultPubKey = item.ObservedVaultPubKey
	}
	if vaultPubKey.IsEmpty() || txHash == "" {
		return false
	}

	refs := xmrRegisteredKeyImageRefsForTx(txHash, vault.KeyImagesXmr)
	if len(refs) == 0 {
		return true
	}

	if _, err := ensureFrostVaultRegisteredWithKeyImages(
		c.logger,
		c.frostSigner,
		c.chain,
		vaultPubKey,
		c.localVaultBootstrap,
		"deck_stale_hydration",
		vault.VaultCreationHeightXmr,
		refs,
	); err != nil {
		c.logger.Info().
			Err(err).
			Str("tx_hash", txHash).
			Str("vault_pub_key", vaultPubKey.String()).
			Msg("keeping XMR deck item because registered key-images could not be hydrated locally")
		return false
	}

	if len(item.XmrKeyImages) == 0 {
		result, err := c.frostSigner.ApplyKeyImages(vaultPubKey.String(), refs, true)
		if err != nil {
			c.logger.Info().
				Err(err).
				Str("tx_hash", txHash).
				Str("vault_pub_key", vaultPubKey.String()).
				Msg("keeping XMR deck item because registered key-images could not be applied locally")
			return false
		}
		if result.NotFound > 0 {
			c.logger.Info().
				Str("tx_hash", txHash).
				Str("vault_pub_key", vaultPubKey.String()).
				Int("not_found", result.NotFound).
				Msg("keeping XMR deck item until registered key-images have local outputs")
			return false
		}
		c.signalXMRKeyImageBacklogChanged()
		return true
	}

	tx := xmrObservedTxFromTxInItem(item)
	if tx.Tx.Chain.IsEmpty() {
		tx.Tx.Chain = common.XMRChain
	}
	tx.FinaliseHeight = tx.BlockHeight
	inbound := c.isObservedInboundToVault(item)
	trackedOutboundChange := c.isObservedOutboundFromVault(item)
	if err := c.validateObservedTxKeyImages(tx, inbound, trackedOutboundChange); err != nil {
		c.logger.Info().
			Err(err).
			Str("tx_hash", txHash).
			Str("vault_pub_key", vaultPubKey.String()).
			Msg("keeping XMR deck item until registered key-images validate locally")
		return false
	}
	return true
}

func xmrRegisteredKeyImageRefsForTx(txHash string, registered []*common.XmrKeyImageRef) []xmrFrostApplyKeyImageRef {
	txHash = normalizeTxID(txHash)
	if txHash == "" {
		return nil
	}
	refs := make([]xmrFrostApplyKeyImageRef, 0)
	for _, ref := range registered {
		if ref == nil || normalizeTxID(ref.TxHash) != txHash {
			continue
		}
		keyImage := strings.ToLower(strings.TrimSpace(ref.KeyImage))
		if keyImage == "" {
			continue
		}
		refs = append(refs, xmrFrostApplyKeyImageRef{
			TxHash:      txHash,
			OutputIndex: ref.OutputIndex,
			KeyImage:    keyImage,
		})
	}
	return refs
}

func xmrKeyImagesRegisteredForTx(txHash string, registered []*common.XmrKeyImageRef) bool {
	return common.XMRKeyImageRefsContainTx(txHash, registered)
}

func (c *Client) xmrTxKeysAlreadyRegistered(vaultPubKey common.PubKey, txHash string) bool {
	_, ok := c.registeredXMRTxKeysForTx(vaultPubKey, txHash)
	return ok
}

func (c *Client) registeredXMRTxKeysForTx(vaultPubKey common.PubKey, txHash string) ([]string, bool) {
	if c == nil || c.thorchainBridge == nil {
		return nil, false
	}
	txHash = normalizeTxID(txHash)
	if vaultPubKey.IsEmpty() || txHash == "" {
		return nil, false
	}
	vault, err := c.getVaultByPubKey(vaultPubKey)
	if err != nil {
		return nil, false
	}
	for _, ref := range vault.TxKeysXmr {
		if ref == nil || normalizeTxID(ref.TxHash) != txHash {
			continue
		}
		txKeys := normalizeTxKeyValues(ref.TxKeys)
		if len(txKeys) == 0 {
			return nil, false
		}
		return txKeys, true
	}
	return nil, false
}

func (c *Client) registeredXMRTxKeysForMigrateSender(item *stypes.TxInItem) ([]string, common.PubKey, bool) {
	if c == nil || item == nil || !isXMRMigrateMemo(item.Memo) {
		return nil, common.EmptyPubKey, false
	}
	sourceVaultPubKey, found := c.findVaultPubKeyByAddress(item.Sender)
	if !found || sourceVaultPubKey.IsEmpty() || sourceVaultPubKey.Equals(item.ObservedVaultPubKey) {
		return nil, common.EmptyPubKey, false
	}
	txKeys, ok := c.registeredXMRTxKeysForTx(sourceVaultPubKey, item.Tx)
	if !ok {
		return nil, common.EmptyPubKey, false
	}
	return txKeys, sourceVaultPubKey, true
}

func xmrKeyImageProofsAlreadyRegistered(proofs []*common.XmrKeyImageProof, registered []*common.XmrKeyImageRef) bool {
	return common.XMRKeyImageProofsRegistered(proofs, registered)
}

func (c *Client) postKeysignFailure(tx stypes.TxOutItem, height int64, signErr error) error {
	if signErr == nil || c.thorchainBridge == nil {
		return signErr
	}

	var keysignError bifrosttss.KeysignError
	if !errors.As(signErr, &keysignError) {
		return signErr
	}
	if len(keysignError.Blame.BlameNodes) == 0 {
		return signErr
	}

	txID, err := c.thorchainBridge.PostKeysignFailure(
		keysignError.Blame,
		height,
		tx.Memo,
		tx.Coins,
		txVaultPubKey(tx),
	)
	if err != nil {
		c.logger.Error().Err(err).Msg("fail to post keysign failure to thorchain")
		return multierror.Append(signErr, fmt.Errorf("fail to post keysign failure to THORChain: %w", err))
	}

	c.logger.Info().Str("tx_id", txID.String()).Msg("post keysign failure to thorchain")
	return signErr
}

func (c *Client) GetHeight() (int64, error) {
	if c.scanner == nil {
		return 0, errors.New("monero scanner is not initialized")
	}
	return c.scanner.GetHeight()
}

func (c *Client) GetAddress(poolPubKey common.PubKey) string {
	if addr := c.getVaultAddress(poolPubKey); addr != "" {
		return addr
	}
	addr, err := poolPubKey.GetAddress(c.chain)
	if err != nil {
		c.logger.Err(err).Str("pool_pub_key", poolPubKey.String()).Msg("fail to get monero address for pool pubkey")
		return ""
	}
	return addr.String()
}

func (c *Client) GetAccount(poolPubKey common.PubKey, height *big.Int) (common.Account, error) {
	if addr := c.getVaultAddress(poolPubKey); addr != "" {
		return c.GetAccountByAddress(addr, height)
	}

	addr, err := poolPubKey.GetAddress(c.chain)
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to get monero address from pubkey: %w", err)
	}
	if addr.IsEmpty() {
		return common.Account{}, fmt.Errorf("fail to resolve monero vault address for %s", poolPubKey.String())
	}
	return c.GetAccountByAddress(addr.String(), height)
}

func (c *Client) GetAccountByAddress(address string, _ *big.Int) (common.Account, error) {
	if c.frostSigner == nil {
		return common.Account{}, fmt.Errorf("xmr account query requires frost signer")
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return common.Account{}, fmt.Errorf("xmr account query requires vault address")
	}

	pubKey, found := c.findVaultPubKeyByAddress(address)
	if !found {
		return common.Account{}, fmt.Errorf("no vault pub key found for address %s", address)
	}

	if err := c.ensureFrostVaultRegistration(pubKey); err != nil {
		return common.Account{}, fmt.Errorf("fail to register monero vault for %s: %w", address, err)
	}

	balance, err := c.frostSigner.GetBalance(pubKey.String())
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to get monero wallet balance for %s: %w", address, err)
	}
	pendingSelfSend, err := c.xmrPendingSelfSendMempoolAmount(pubKey, common.Address(address))
	if err != nil {
		return common.Account{}, fmt.Errorf("fail to get monero mempool self-send balance for %s: %w", address, err)
	}
	// Account reporting (used by solvency comparison vs thornode's vault.coins)
	// must use the TOTAL on-chain balance — locked + unlocked. Locked outputs
	// (those in the 10-conf maturity window) DO exist on chain and must count
	// toward the wallet's view of "what we own", otherwise every fresh inbound
	// triggers a false-positive insolvency report during normal operation.
	// Spendability for sign-time input selection is a separate concern handled
	// by ensureXMRVaultBalance which uses spendableBalanceAtomic.
	amount := cosmos.NewUint((balance.Balance + pendingSelfSend) / moneroAtomicToThorchain)
	return common.Account{
		Sequence:      0,
		AccountNumber: 0,
		Coins: common.NewCoins(
			common.NewCoin(common.XMRAsset, amount),
		),
		HasMemoFlag: false,
	}, nil
}

func (c *Client) xmrPendingSelfSendMempoolAmount(vaultPubKey common.PubKey, vaultAddress common.Address) (uint64, error) {
	if c.frostSigner == nil || vaultPubKey.IsEmpty() || vaultAddress.IsEmpty() {
		return 0, nil
	}

	transfers, err := c.frostSigner.GetMemPoolTransfers()
	if err != nil {
		return 0, err
	}

	vaultPubKeyString := vaultPubKey.String()
	vaultAddressString := vaultAddress.String()
	seen := make(map[string]struct{})
	var total uint64
	for _, tx := range transfers.Outgoing {
		if !strings.EqualFold(strings.TrimSpace(tx.VaultPubKey), vaultPubKeyString) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(tx.FromAddress), vaultAddressString) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(tx.Address), vaultAddressString) {
			continue
		}
		txID := strings.ToLower(strings.TrimSpace(tx.TxID))
		if txID == "" {
			continue
		}
		if _, ok := seen[txID]; ok {
			continue
		}
		seen[txID] = struct{}{}
		if ^uint64(0)-total < tx.Amount {
			return 0, fmt.Errorf("pending self-send mempool balance overflow for vault %s", vaultPubKey)
		}
		total += tx.Amount
	}
	return total, nil
}

func (c *Client) findVaultPubKeyByAddress(address string) (common.PubKey, bool) {
	address = strings.TrimSpace(address)
	if address == "" {
		return common.EmptyPubKey, false
	}
	c.vaultAddressMu.RLock()
	defer c.vaultAddressMu.RUnlock()
	if pubKeyStr, ok := c.vaultAddrToKey[strings.ToLower(address)]; ok {
		pk, err := common.NewPubKey(pubKeyStr)
		if err == nil {
			return pk, true
		}
	}
	for pubKeyStr, addr := range c.vaultAddressMap {
		if strings.EqualFold(addr.String(), address) {
			pk, err := common.NewPubKey(pubKeyStr)
			if err == nil {
				return pk, true
			}
		}
	}
	return common.EmptyPubKey, false
}

func (c *Client) GetChain() common.Chain {
	return c.chain
}

func (c *Client) GetConfig() config.BifrostChainConfiguration {
	return c.cfg
}

func (c *Client) CustomKeygen() bool {
	return true
}

func (c *Client) Keygen(req types.CustomKeygenRequest) (types.CustomKeygenResult, error) {
	vaultPubKey := req.PoolPubKeyEddsa
	if vaultPubKey.IsEmpty() {
		return types.CustomKeygenResult{}, fmt.Errorf("xmr keygen requires a non-empty eddsa vault pubkey")
	}
	if c.frostSigner == nil {
		return types.CustomKeygenResult{}, fmt.Errorf("%w: signer engine is not initialized", ErrFrostNotConfigured)
	}
	if c.tssServer == nil {
		return types.CustomKeygenResult{}, fmt.Errorf("tss server is not initialized")
	}

	xmrHeight, heightErr := c.GetHeight()
	if heightErr != nil || xmrHeight <= 0 {
		return types.CustomKeygenResult{}, fmt.Errorf("xmr keygen requires synced monero node (height: %d): %w", xmrHeight, heightErr)
	}

	members := make([]string, 0, len(req.Members))
	for _, member := range req.Members {
		member = strings.TrimSpace(member)
		if member != "" {
			members = append(members, member)
		}
	}
	if len(members) == 0 {
		return types.CustomKeygenResult{}, fmt.Errorf("xmr keygen requires non-empty member set")
	}
	version := strings.TrimSpace(req.ThorchainVersion)
	if version == "" && c.thorchainBridge != nil {
		chainVersion, versionErr := c.thorchainBridge.GetThorchainVersion()
		if versionErr == nil {
			version = chainVersion.String()
		}
	}

	frostResp, err := c.tssServer.FrostKeygen(tssp.FrostKeygenRequest{
		VaultPubKey:   vaultPubKey.String(),
		SignerPubKeys: members,
		BlockHeight:   req.BlockHeight,
		Version:       version,
	}, newFrostSignerAdapter(c.frostSigner))
	if err != nil {
		c.logFailClosedCoordination("frost_tss_keygen", err)
		return types.CustomKeygenResult{}, fmt.Errorf("fail to execute FROST keygen: %w", err)
	}
	if frostResp.Status != gotsscommon.Success {
		c.logFailClosedCoordination("frost_tss_keygen", fmt.Errorf("frost keygen failed: %s", frostResp.Blame.FailReason))
		var blame []ttypes.Blame
		if len(frostResp.Blame.BlameNodes) > 0 || strings.TrimSpace(frostResp.Blame.FailReason) != "" {
			blame = append(blame, frostBlameToThorchainBlame(frostResp.Blame))
		}
		return types.CustomKeygenResult{Blame: blame}, fmt.Errorf("FROST keygen failed: %s", strings.TrimSpace(frostResp.Blame.FailReason))
	}

	frostAddress := strings.TrimSpace(frostResp.Address)
	if frostAddress == "" {
		return types.CustomKeygenResult{}, fmt.Errorf("FROST keygen succeeded without an address")
	}
	frostAddr, err := common.NewAddress(frostAddress)
	if err != nil {
		return types.CustomKeygenResult{}, fmt.Errorf("invalid FROST keygen address %q: %w", frostAddress, err)
	}
	groupKeyHex := strings.TrimSpace(frostResp.GroupKey)
	if groupKeyHex == "" {
		return types.CustomKeygenResult{}, fmt.Errorf("FROST keygen succeeded without a group key")
	}

	frostVaultPubKey, err := vaultPubKeyFromGroupKeyHex(groupKeyHex)
	if err != nil {
		return types.CustomKeygenResult{}, fmt.Errorf("fail to convert FROST group key to vault pubkey: %w", err)
	}
	if !frostVaultPubKey.Equals(vaultPubKey) {
		c.logger.Info().
			Str("requested_pool_pubkey_eddsa", vaultPubKey.String()).
			Str("frost_pool_pubkey_eddsa", frostVaultPubKey.String()).
			Msg("FROST keygen produced authoritative eddsa vault pubkey")
	}

	expectedAddress, err := frostVaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		return types.CustomKeygenResult{}, fmt.Errorf("fail to derive monero address from FROST group key %s: %w", groupKeyHex, err)
	}
	if !frostAddr.Equals(expectedAddress) {
		return types.CustomKeygenResult{}, fmt.Errorf(
			"FROST keygen returned address %s but expected derived vault address %s",
			frostAddr.String(),
			expectedAddress.String(),
		)
	}

	if _, err := c.frostSigner.RegisterVault(xmrFrostRegisterVaultRequest{
		VaultPubKey:         frostVaultPubKey.String(),
		GroupKey:            groupKeyHex,
		Address:             frostAddr.String(),
		Source:              "bifrost",
		VaultCreationHeight: frostResp.ObservedMoneroHeight,
	}); err != nil {
		c.logFailClosedCoordination("frost_tss_keygen_register", err)
		return types.CustomKeygenResult{}, fmt.Errorf("fail to register FROST vault mapping for %s: %w", frostVaultPubKey.String(), err)
	}

	c.setVaultAddressMapping(frostVaultPubKey, frostAddr)
	return types.CustomKeygenResult{
		Address:              frostAddr,
		PoolPubKeyEddsa:      frostVaultPubKey,
		FrostKeyshareHex:     frostResp.KeyBytesHex,
		ObservedMoneroHeight: frostResp.ObservedMoneroHeight,
	}, nil
}

func (c *Client) vaultSigningMembers(vaultPubKey common.PubKey) ([]string, error) {
	if vaultPubKey.IsEmpty() {
		return nil, fmt.Errorf("vault pubkey is empty")
	}
	vault, err := c.getVaultByPubKey(vaultPubKey)
	if err != nil {
		return nil, fmt.Errorf("fail to get vault membership: %w", err)
	}
	members := vault.GetMembership().Strings()
	if len(members) == 0 {
		for _, member := range vault.Membership {
			member = strings.TrimSpace(member)
			if member != "" {
				members = append(members, member)
			}
		}
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("vault %s has empty membership", vaultPubKey.String())
	}
	return members, nil
}

type xmrKeyImageCeremonyContext struct {
	vaultPubKey common.PubKey
	vaultStatus ttypes.VaultStatus
	members     []string
}

func (c *Client) resolveXMRKeyImageCeremonyContext(vaultPubKey common.PubKey) (xmrKeyImageCeremonyContext, error) {
	if vaultPubKey.IsEmpty() {
		return xmrKeyImageCeremonyContext{}, fmt.Errorf("vault pubkey is empty")
	}
	vault, err := c.getVaultByPubKey(vaultPubKey)
	if err != nil {
		return xmrKeyImageCeremonyContext{}, fmt.Errorf("fail to get vault: %w", err)
	}
	canonicalVaultPubKey, err := vault.AlgoPubKey(common.XMRChain)
	if err != nil {
		return xmrKeyImageCeremonyContext{}, fmt.Errorf("fail to resolve canonical XMR vault pubkey: %w", err)
	}
	members, err := c.vaultSigningMembers(vaultPubKey)
	if err != nil {
		return xmrKeyImageCeremonyContext{}, err
	}
	return xmrKeyImageCeremonyContext{
		vaultPubKey: canonicalVaultPubKey,
		vaultStatus: vault.Status,
		members:     members,
	}, nil
}

func (c *Client) canonicalizeXMROutboundVaultPubKey(vaultPubKey common.PubKey, txHash string) (common.PubKey, error) {
	if vaultPubKey.IsEmpty() || c.thorchainBridge == nil {
		return vaultPubKey, nil
	}

	ceremonyCtx, err := c.resolveXMRKeyImageCeremonyContext(vaultPubKey)
	if err != nil {
		return vaultPubKey, fmt.Errorf("fail to resolve xmr outbound validation context: %w", err)
	}
	if !ceremonyCtx.vaultPubKey.Equals(vaultPubKey) {
		c.logger.Info().
			Str("observed_vault_pub_key", vaultPubKey.String()).
			Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
			Str("tx_hash", txHash).
			Msg("canonicalized XMR outbound validation vault context")
	}
	return ceremonyCtx.vaultPubKey, nil
}

func frostBlameToThorchainBlame(in gotssblame.Blame) ttypes.Blame {
	out := ttypes.Blame{
		FailReason: in.FailReason,
		Round:      in.Round,
		IsUnicast:  in.IsUnicast,
		BlameNodes: make([]ttypes.Node, len(in.BlameNodes)),
	}
	for i, node := range in.BlameNodes {
		out.BlameNodes[i] = ttypes.Node{
			Pubkey:         node.Pubkey,
			BlameData:      node.BlameData,
			BlameSignature: node.BlameSignature,
		}
	}
	return out
}

func (c *Client) OnObservedTxIn(txIn stypes.TxInItem, _ int64) {
	m, err := memo.ParseMemo(common.LatestVersion, txIn.Memo)
	if err != nil {
		// ParseMemo errors are expected for non-outbound memos and THORName inbounds.
		c.logger.Debug().Err(err).Str("memo", txIn.Memo).Msg("fail to parse memo")
		return
	}
	if !m.IsOutbound() || m.GetTxID().IsEmpty() {
		return
	}

	if err = c.signerCacheManager.SetSigned(
		txIn.CacheHash(c.GetChain(), m.GetTxID().String()),
		txIn.CacheVault(c.GetChain()),
		txIn.Tx,
	); err != nil {
		c.logger.Err(err).Msg("fail to update signer cache")
	}
}

func (c *Client) GetConfirmationCount(txIn stypes.TxIn) int64 {
	if len(txIn.TxArray) == 0 || txIn.MemPool {
		return 0
	}
	if c.hasObservedXMROutbound(txIn) {
		c.logger.Debug().Msg("treating XMR outbound-bearing observation as 1-conf finalization")
		return 1
	}

	addresses, err := c.getAsgardAddress()
	if err != nil {
		c.logger.Err(err).Msg("fail to get asgard addresses")
		c.asgardMu.RLock()
		addresses = append(addresses, c.asgardAddresses...)
		c.asgardMu.RUnlock()
	}

	totalValue := txIn.GetTotalTransactionValue(common.XMRAsset, addresses)
	coinbaseThorchain := xmrCoinbaseThorchain()

	// Apply mimir-configurable confirmation multiplier (same lever as UTXO/ETH chains).
	// Default confMul = MaxBasisPts (10000) = 1x coinbase. Higher values reduce
	// required confirmations; lower values increase them.
	confMul, err := utxo.GetConfMulBasisPoint(c.GetChain().String(), c.thorchainBridge)
	if err != nil {
		c.logger.Debug().Err(err).Msg("fail to get xmr conf multiplier mimir value")
	}
	confValue := common.GetUncappedShare(confMul, cosmos.NewUint(constants.MaxBasisPts), cosmos.NewUint(coinbaseThorchain))
	if confValue.IsZero() {
		confValue = cosmos.NewUint(coinbaseThorchain)
	}
	confirm := xmrConfirmationRequired(totalValue, confValue)
	maxConfirmationCap := xmrDefaultMaxConfirmationCap
	if c.thorchainBridge != nil {
		maxConfirmations, capErr := c.thorchainBridge.GetMimirWithRef(constants.MimirTemplateMaxConfirmations, c.GetChain().String())
		if capErr != nil {
			c.logger.Debug().Err(capErr).Msg("fail to get xmr max conf adjustment")
		} else if maxConfirmations > 0 {
			maxConfirmationCap = uint64(maxConfirmations)
		}
	}
	if confirm > maxConfirmationCap {
		confirm = maxConfirmationCap
	}

	minConfirmations := uint64(0)
	if totalValue.IsZero() {
		minConfirmations = c.cfg.MinConfirmations
		if confirm < minConfirmations {
			confirm = minConfirmations
		}
	}

	c.logger.Debug().
		Str("total_tx_value", totalValue.String()).
		Uint64("coinbase_atomic", defaultCoinbaseValue).
		Uint64("coinbase_thorchain", coinbaseThorchain).
		Str("conf_multiplier", confMul.String()).
		Str("conf_value", confValue.String()).
		Uint64("max_confirmation_cap", maxConfirmationCap).
		Uint64("min_confirmations", minConfirmations).
		Uint64("confirmations", confirm).
		Msg("calculated xmr confirmation requirement")

	return int64(confirm)
}

func (c *Client) ConfirmationCountReady(txIn stypes.TxIn) bool {
	if len(txIn.TxArray) == 0 || txIn.MemPool {
		return true
	}

	blockHeight := txIn.TxArray[0].BlockHeight
	confirm := txIn.ConfirmationRequired
	return (c.scanner.CurrentHeight() - blockHeight) >= confirm
}

func (c *Client) GetBlockScannerHeight() (int64, error) {
	return c.blockScanner.PreviousHeight(), nil
}

func (c *Client) GetLatestTxForVault(vault string) (string, string, error) {
	lastObserved, err := c.signerCacheManager.GetLatestRecordedTx(stypes.InboundCacheKey(vault, c.GetChain().String()))
	if err != nil {
		return "", "", err
	}
	lastBroadcasted, err := c.signerCacheManager.GetLatestRecordedTx(stypes.BroadcastCacheKey(vault, c.GetChain().String()))
	return lastObserved, lastBroadcasted, err
}

func (c *Client) collectVaultOutputsForTx(vaultPubKey common.PubKey, txHash string) ([]xmrFrostOutputEntry, error) {
	if c.frostSigner == nil {
		return nil, fmt.Errorf("frost signer is not configured")
	}
	outputs, err := c.frostSigner.GetAllOutputs(vaultPubKey.String())
	if err != nil {
		return nil, fmt.Errorf("fail to query tracked outputs for vault %s: %w", vaultPubKey, err)
	}
	return filterVaultOutputsForTx(outputs, txHash), nil
}

func filterVaultOutputsForTx(outputs []xmrFrostOutputEntry, txHash string) []xmrFrostOutputEntry {
	normalizedTxHash := normalizeTxID(txHash)
	filtered := make([]xmrFrostOutputEntry, 0)
	for _, output := range outputs {
		if normalizeTxID(output.TxHash) != normalizedTxHash {
			continue
		}
		filtered = append(filtered, output)
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].TxHash != filtered[j].TxHash {
			return filtered[i].TxHash < filtered[j].TxHash
		}
		return filtered[i].OutputIndex < filtered[j].OutputIndex
	})
	return filtered
}

func xmrOutputsRequiringKeyImages(outputs []xmrFrostOutputEntry) []xmrFrostOutputEntry {
	required := make([]xmrFrostOutputEntry, 0, len(outputs))
	for _, output := range outputs {
		if output.Amount == 0 {
			continue
		}
		required = append(required, output)
	}
	return required
}

func xmrOutputsMissingKeyImages(outputs []xmrFrostOutputEntry) []xmrFrostOutputEntry {
	pending := make([]xmrFrostOutputEntry, 0, len(outputs))
	for _, output := range outputs {
		if output.Amount == 0 {
			continue
		}
		if strings.TrimSpace(output.KeyImage) != "" {
			continue
		}
		pending = append(pending, output)
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Height != pending[j].Height {
			return pending[i].Height < pending[j].Height
		}
		if normalizeTxID(pending[i].TxHash) != normalizeTxID(pending[j].TxHash) {
			return normalizeTxID(pending[i].TxHash) < normalizeTxID(pending[j].TxHash)
		}
		return pending[i].OutputIndex < pending[j].OutputIndex
	})
	return pending
}

func xmrOutputsWithLocalKeyImagesMissingFromVault(outputs []xmrFrostOutputEntry, refs []*common.XmrKeyImageRef) []xmrFrostOutputEntry {
	registered := make(map[string]string, len(refs))
	for _, ref := range refs {
		if ref == nil {
			continue
		}
		registered[xmrOutputRefKey(ref.TxHash, ref.OutputIndex)] = strings.ToLower(strings.TrimSpace(ref.KeyImage))
	}

	pending := make([]xmrFrostOutputEntry, 0)
	for _, output := range outputs {
		if output.Amount == 0 {
			continue
		}
		localKeyImage := strings.ToLower(strings.TrimSpace(output.KeyImage))
		if localKeyImage == "" {
			continue
		}
		registeredKeyImage, ok := registered[xmrOutputRefKey(output.TxHash, output.OutputIndex)]
		if ok && registeredKeyImage != "" && strings.EqualFold(registeredKeyImage, localKeyImage) {
			continue
		}
		pending = append(pending, output)
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Height != pending[j].Height {
			return pending[i].Height < pending[j].Height
		}
		if normalizeTxID(pending[i].TxHash) != normalizeTxID(pending[j].TxHash) {
			return normalizeTxID(pending[i].TxHash) < normalizeTxID(pending[j].TxHash)
		}
		return pending[i].OutputIndex < pending[j].OutputIndex
	})
	return pending
}

func xmrTxOutputsNeedKeyImageAttestation(outputs []xmrFrostOutputEntry, refs []*common.XmrKeyImageRef) bool {
	if len(outputs) == 0 {
		return false
	}
	return len(xmrOutputsWithLocalKeyImagesMissingFromVault(outputs, refs)) > 0
}

func xmrPendingKeyImageTxCount(outputs []xmrFrostOutputEntry) int {
	seen := make(map[string]struct{})
	for _, output := range outputs {
		txHash := normalizeTxID(output.TxHash)
		if txHash == "" {
			continue
		}
		seen[txHash] = struct{}{}
	}
	return len(seen)
}

func (c *Client) generateObservedTxKeyImages(
	vaultPubKey common.PubKey,
	txHash string,
	outputs []xmrFrostOutputEntry,
	members []string,
	finaliseHeight int64,
	workKind xmrWorkKind,
	workPhase xmrFinalizePhase,
	version string,
) ([]*common.XmrKeyImageProof, error) {
	queuedBefore, activeBefore := c.pendingXMRKeyImageBacklog()
	c.logger.Info().
		Str("vault_pub_key", vaultPubKey.String()).
		Str("tx_hash", txHash).
		Int("tracked_output_count", len(outputs)).
		Uint64("queued_before", queuedBefore).
		Bool("active_before", activeBefore).
		Msg("queueing XMR key-image generation")
	release := c.acquireXMRKeyImageCeremonySlot()
	queuedAfterAcquire, activeAfterAcquire := c.pendingXMRKeyImageBacklog()
	c.logger.Info().
		Str("vault_pub_key", vaultPubKey.String()).
		Str("tx_hash", txHash).
		Int("tracked_output_count", len(outputs)).
		Uint64("queued_after_acquire", queuedAfterAcquire).
		Bool("active_after_acquire", activeAfterAcquire).
		Msg("acquired XMR key-image generation slot")
	defer func() {
		c.setActiveXMRWorkItem(nil)
		release()
		queuedAfterRelease, activeAfterRelease := c.pendingXMRKeyImageBacklog()
		c.logger.Info().
			Str("vault_pub_key", vaultPubKey.String()).
			Str("tx_hash", txHash).
			Uint64("queued_after_release", queuedAfterRelease).
			Bool("active_after_release", activeAfterRelease).
			Msg("released XMR key-image generation slot")
	}()

	currentThorHeight := finaliseHeight
	if c.thorchainBridge != nil {
		if h, err := c.thorchainBridge.GetBlockHeight(); err == nil && h > 0 {
			currentThorHeight = h
		}
	}

	proofs := make([]*common.XmrKeyImageProof, 0, len(outputs))
	completedWorkItems := make([]xmrWorkItem, 0, len(outputs))
	for _, output := range outputs {
		workItem, workErr := c.ensureXMRKeyImageWorkItem(
			vaultPubKey,
			txHash,
			output.OutputIndex,
			finaliseHeight,
			workKind,
			workPhase,
		)
		if workErr != nil {
			return nil, fmt.Errorf("fail to initialize XMR key-image work item for %s:%d: %w", txHash, output.OutputIndex, workErr)
		}
		workItem, workErr = c.rotateXMRKeyImageLeaderEpoch(workItem, currentThorHeight)
		if workErr != nil {
			return nil, fmt.Errorf("fail to rotate XMR key-image leader epoch for %s:%d: %w", txHash, output.OutputIndex, workErr)
		}
		if ready, wait := xmrWorkItemRetryReady(workItem, time.Now()); !ready {
			c.logger.Debug().
				Str("vault_pub_key", vaultPubKey.String()).
				Str("tx_hash", txHash).
				Uint32("output_index", output.OutputIndex).
				Int64("attempt_count", workItem.AttemptCount).
				Dur("retry_after", wait).
				Msg("waiting for XMR key-image retry backoff")
			return nil, errXMRFinalizeWorkRetryBackoff
		}
		c.setActiveXMRWorkItem(&workItem)
		c.logger.Info().
			Str("vault_pub_key", vaultPubKey.String()).
			Str("tx_hash", txHash).
			Uint32("output_index", output.OutputIndex).
			Int64("requested_finalise_height", finaliseHeight).
			Int64("leader_epoch_height", workItem.LeaderEpochHeight).
			Str("work_id", workItem.ID).
			Strs("signer_pub_keys", members).
			Msg("starting XMR key-image generation")
		resp, err := c.tssServer.FrostKeyImage(tssp.FrostKeyImageRequest{
			VaultPubKey:   vaultPubKey.String(),
			TxHash:        txHash,
			OutputIndex:   output.OutputIndex,
			SignerPubKeys: members,
			BlockHeight:   workItem.LeaderEpochHeight,
			Version:       version,
		}, newFrostSignerAdapter(c.frostSigner))
		if err != nil {
			_, _ = c.recordXMRKeyImageWorkAttempt(workItem, currentThorHeight, err.Error())
			return nil, fmt.Errorf("fail to execute XMR key-image ceremony for %s:%d: %w", txHash, output.OutputIndex, err)
		}
		if resp.Status != gotsscommon.Success {
			failReason := strings.TrimSpace(resp.Blame.FailReason)
			_, _ = c.recordXMRKeyImageWorkAttempt(workItem, currentThorHeight, failReason)
			if failReason == "local signer is not in active signer set" || failReason == "local signer is not in participant set" {
				return nil, fmt.Errorf("%w for %s:%d: %s", errXMRKeyImageNotActiveSigner, txHash, output.OutputIndex, failReason)
			}
			if len(resp.Blame.BlameNodes) > 0 {
				return nil, bifrosttss.NewKeysignError(frostBlameToThorchainBlame(resp.Blame))
			}
			return nil, fmt.Errorf("XMR key-image ceremony failed for %s:%d: %s", txHash, output.OutputIndex, failReason)
		}
		c.logger.Info().
			Str("vault_pub_key", vaultPubKey.String()).
			Str("tx_hash", txHash).
			Uint32("output_index", output.OutputIndex).
			Str("key_image", strings.ToLower(strings.TrimSpace(resp.KeyImage))).
			Msg("completed XMR key-image generation for output")
		proofs = append(proofs, &common.XmrKeyImageProof{
			TxHash:      strings.ToLower(strings.TrimSpace(resp.TxHash)),
			OutputIndex: resp.OutputIndex,
			KeyImage:    strings.ToLower(strings.TrimSpace(resp.KeyImage)),
			ProofC:      strings.ToLower(strings.TrimSpace(resp.ProofC)),
			ProofS:      strings.ToLower(strings.TrimSpace(resp.ProofS)),
		})
		completedWorkItems = append(completedWorkItems, workItem)
	}
	c.logger.Info().
		Str("vault_pub_key", vaultPubKey.String()).
		Str("tx_hash", txHash).
		Int("proof_count", len(proofs)).
		Msg("completed XMR key-image generation batch")
	applyRefs := make([]xmrFrostApplyKeyImageRef, 0, len(proofs))
	for _, proof := range proofs {
		if proof == nil {
			continue
		}
		applyRefs = append(applyRefs, xmrFrostApplyKeyImageRef{
			TxHash:      proof.TxHash,
			OutputIndex: proof.OutputIndex,
			KeyImage:    proof.KeyImage,
		})
	}
	if len(applyRefs) > 0 {
		result, err := c.frostSigner.ApplyKeyImages(vaultPubKey.String(), applyRefs, false)
		if err != nil {
			return nil, fmt.Errorf("fail to apply locally generated XMR key-images: %w", err)
		}
		if result.NotFound > 0 {
			return nil, fmt.Errorf("locally generated XMR key-images missing %d outputs", result.NotFound)
		}
		c.logger.Info().
			Str("vault_pub_key", vaultPubKey.String()).
			Str("tx_hash", txHash).
			Int("applied", result.Applied).
			Int("not_found", result.NotFound).
			Msg("applied locally generated XMR key-images")
		c.signalXMRKeyImageBacklogChanged()
	}
	for _, workItem := range completedWorkItems {
		if err := c.completeXMRKeyImageWorkItem(workItem); err != nil {
			c.logger.Warn().
				Err(err).
				Str("vault_pub_key", workItem.VaultPubKey).
				Str("tx_hash", workItem.TxHash).
				Msg("failed to clear completed XMR key-image work item")
		}
	}
	return canonicalXMRKeyImageProofs(proofs), nil
}

func isOutboundObservationMemo(memoText string) bool {
	memoText = strings.TrimSpace(memoText)
	if memoText == "" {
		return false
	}
	parsedMemo, err := memo.ParseMemo(common.LatestVersion, memoText)
	if err != nil {
		return false
	}
	return parsedMemo.IsOutbound()
}

func isXMRConsolidateSelfSendItem(item *stypes.TxInItem) bool {
	if item == nil {
		return false
	}
	parsedMemo, err := memo.ParseMemo(common.LatestVersion, strings.TrimSpace(item.Memo))
	if err != nil || !parsedMemo.IsType(memo.TxConsolidate) {
		return false
	}
	return strings.TrimSpace(item.Sender) != "" && strings.EqualFold(strings.TrimSpace(item.Sender), strings.TrimSpace(item.To))
}

func isXMRConsolidateSelfSendObservedTx(tx common.ObservedTx) bool {
	if tx.IsEmpty() || !tx.Tx.Chain.Equals(common.XMRChain) {
		return false
	}
	parsedMemo, err := memo.ParseMemo(common.LatestVersion, strings.TrimSpace(tx.Tx.Memo))
	if err != nil || !parsedMemo.IsType(memo.TxConsolidate) {
		return false
	}
	return !tx.Tx.FromAddress.IsEmpty() && tx.Tx.FromAddress.Equals(tx.Tx.ToAddress)
}

func (c *Client) shouldSkipObservedXMRInboundChange(
	memoText string,
	sender string,
	observedVaultPubKey common.PubKey,
) bool {
	parsedMemo, err := memo.ParseMemo(common.LatestVersion, strings.TrimSpace(memoText))
	if err != nil {
		return false
	}

	if parsedMemo.IsOutbound() {
		return !c.isKnownVaultAddress(sender)
	}

	switch parsedMemo.GetType() {
	case memo.TxConsolidate:
		return true
	case memo.TxMigrate:
		if observedVaultPubKey.IsEmpty() || c.thorchainBridge == nil {
			return false
		}
		vault, err := c.thorchainBridge.GetVault(observedVaultPubKey.String())
		if err != nil {
			return false
		}
		return vault.IsRetiring()
	default:
		return false
	}
}

func (c *Client) hasObservedXMROutbound(txIn stypes.TxIn) bool {
	for _, item := range txIn.TxArray {
		if item == nil {
			continue
		}
		if item.ObservedVaultPubKey.IsEmpty() {
			continue
		}
		if !c.isKnownVaultAddress(item.Sender) {
			continue
		}
		return true
	}
	return false
}

func (c *Client) isKnownVaultAddress(address string) bool {
	address = strings.TrimSpace(address)
	if address == "" {
		return false
	}
	if _, found := c.findVaultPubKeyByAddress(address); found {
		return true
	}

	c.vaultAddressMu.RLock()
	defer c.vaultAddressMu.RUnlock()
	for _, mapped := range c.vaultAddressMap {
		if strings.EqualFold(mapped.String(), address) {
			return true
		}
	}
	return false
}

func canonicalXMRKeyImageProofs(proofs []*common.XmrKeyImageProof) []*common.XmrKeyImageProof {
	return common.CanonicalXMRKeyImageProofs(proofs)
}

func (c *Client) localNodeIsVaultSigner(members []string) bool {
	if c.localNodePubKey.IsEmpty() {
		return false
	}
	local := strings.TrimSpace(c.localNodePubKey.String())
	for _, member := range members {
		if strings.TrimSpace(member) == local {
			return true
		}
	}
	return false
}

func (c *Client) isObservedInboundToVault(item *stypes.TxInItem) bool {
	if item == nil {
		return false
	}
	vaultAddr, ok := observedXMRVaultAddress(item.ObservedVaultPubKey)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(item.To), vaultAddr.String())
}

func (c *Client) isObservedOutboundFromVault(item *stypes.TxInItem) bool {
	if item == nil {
		return false
	}
	vaultAddr, ok := observedXMRVaultAddress(item.ObservedVaultPubKey)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(item.Sender), vaultAddr.String())
}

func (c *Client) isObservedTxInboundToVault(tx common.ObservedTx) bool {
	vaultAddr, ok := observedXMRVaultAddress(tx.ObservedPubKey)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(tx.Tx.ToAddress.String()), vaultAddr.String())
}

func (c *Client) isObservedTxOutboundFromVault(tx common.ObservedTx) bool {
	vaultAddr, ok := observedXMRVaultAddress(tx.ObservedPubKey)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(tx.Tx.FromAddress.String()), vaultAddr.String())
}

func observedXMRVaultAddress(vaultPubKey common.PubKey) (common.Address, bool) {
	if vaultPubKey.IsEmpty() {
		return common.NoAddress, false
	}
	vaultAddr, err := vaultPubKey.GetAddress(common.XMRChain)
	if err != nil || vaultAddr.IsEmpty() {
		return common.NoAddress, false
	}
	return vaultAddr, true
}

func (c *Client) validateObservedTxKeyImages(tx common.ObservedTx, inbound, trackedOutboundChange bool) error {
	if c.frostSigner == nil || !tx.Tx.Chain.Equals(common.XMRChain) || !tx.IsFinal() {
		return nil
	}
	if inbound {
		if !c.isObservedTxInboundToVault(tx) {
			return nil
		}
		if len(tx.XmrKeyImages) == 0 && c.shouldSkipObservedXMRInboundChange(tx.Tx.Memo, tx.Tx.FromAddress.String(), tx.ObservedPubKey) {
			return nil
		}
	} else if !c.isObservedTxOutboundFromVault(tx) {
		return nil
	}

	canonicalVaultPubKey := tx.ObservedPubKey
	if c.thorchainBridge != nil {
		ceremonyCtx, ctxErr := c.resolveXMRKeyImageCeremonyContext(tx.ObservedPubKey)
		if ctxErr != nil {
			return fmt.Errorf("fail to resolve XMR key-image validation context: %w", ctxErr)
		}
		if !ceremonyCtx.vaultPubKey.Equals(tx.ObservedPubKey) {
			c.logger.Info().
				Str("observed_vault_pub_key", tx.ObservedPubKey.String()).
				Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
				Str("tx_hash", tx.Tx.ID.String()).
				Msg("canonicalized XMR key-image validation vault context")
		}
		canonicalVaultPubKey = ceremonyCtx.vaultPubKey
	}

	outputs, err := c.collectVaultOutputsForTx(canonicalVaultPubKey, tx.Tx.ID.String())
	if err != nil {
		return err
	}
	if len(outputs) == 0 {
		if !inbound && trackedOutboundChange {
			return fmt.Errorf("xmr finalized outbound is still waiting for tracked vault-owned outputs/key images for %s", tx.Tx.ID)
		}
		if len(tx.XmrKeyImages) == 0 {
			return nil
		}
		if inbound {
			return fmt.Errorf("no tracked outputs found locally for XMR inbound %s", tx.Tx.ID)
		}
		return fmt.Errorf("no tracked outputs found locally for XMR outbound %s", tx.Tx.ID)
	}
	outputs = xmrOutputsRequiringKeyImages(outputs)
	if len(outputs) == 0 {
		return nil
	}
	if len(tx.XmrKeyImages) == 0 {
		if inbound {
			return fmt.Errorf("xmr finalized inbound is missing key images for %s", tx.Tx.ID)
		}
		return fmt.Errorf("xmr finalized outbound is missing key images for %s", tx.Tx.ID)
	}

	normalizedProofs := canonicalXMRKeyImageProofs(tx.XmrKeyImages)
	if len(normalizedProofs) != len(outputs) {
		return fmt.Errorf("expected %d XMR key-images for tx %s, got %d", len(outputs), tx.Tx.ID, len(normalizedProofs))
	}
	c.logger.Info().
		Str("tx_hash", tx.Tx.ID.String()).
		Str("canonical_vault_pub_key", canonicalVaultPubKey.String()).
		Bool("inbound", inbound).
		Int("expected_output_count", len(outputs)).
		Int("provided_key_image_count", len(normalizedProofs)).
		Msg("validating finalized XMR key-images against local tracked outputs")

	applyRefs := make([]xmrFrostApplyKeyImageRef, 0, len(normalizedProofs))
	for i, output := range outputs {
		proof := normalizedProofs[i]
		if !strings.EqualFold(proof.TxHash, output.TxHash) || proof.OutputIndex != output.OutputIndex {
			return fmt.Errorf("xmr key-image proof set does not match local output set for tx %s", tx.Tx.ID)
		}
		validateResp, validateErr := c.frostSigner.FrostKeyImageValidate(xmrFrostKeyImageValidateRequest{
			VaultPubKey: canonicalVaultPubKey.String(),
			TxHash:      proof.TxHash,
			OutputIndex: proof.OutputIndex,
			KeyImage:    proof.KeyImage,
			ProofC:      proof.ProofC,
			ProofS:      proof.ProofS,
		})
		if validateErr != nil {
			return fmt.Errorf("fail to validate XMR key-image proof for %s:%d: %w", proof.TxHash, proof.OutputIndex, validateErr)
		}
		if !validateResp.Valid {
			return fmt.Errorf("XMR key-image proof invalid for %s:%d", proof.TxHash, proof.OutputIndex)
		}
		applyRefs = append(applyRefs, xmrFrostApplyKeyImageRef{
			TxHash:      proof.TxHash,
			OutputIndex: proof.OutputIndex,
			KeyImage:    proof.KeyImage,
		})
	}

	result, err := c.frostSigner.ApplyKeyImages(canonicalVaultPubKey.String(), applyRefs, false)
	if err != nil {
		return fmt.Errorf("fail to apply validated XMR key-images: %w", err)
	}
	if result.NotFound > 0 {
		return fmt.Errorf("validated XMR key-images missing %d local outputs", result.NotFound)
	}
	c.logger.Info().
		Str("tx_hash", tx.Tx.ID.String()).
		Str("canonical_vault_pub_key", canonicalVaultPubKey.String()).
		Bool("inbound", inbound).
		Int("applied", result.Applied).
		Int("not_found", result.NotFound).
		Msg("validated and applied finalized XMR key-images")
	c.signalXMRKeyImageBacklogChanged()
	return nil
}

func (c *Client) RollbackBlockScanner() error {
	return c.blockScanner.RollbackToLastObserved()
}

func (c *Client) ShouldReportSolvency(height int64) bool {
	c.ensureConsolidationState()
	if c.lastSolvencyCheckHeight == nil || c.scanner == nil {
		return false
	}
	if height <= c.lastSolvencyCheckHeight.Load() {
		return false
	}
	if c.scanner.lastFee.Load() == 0 {
		return false
	}
	if c.solvencyCheckAfterIdle != nil && c.solvencyCheckAfterIdle.Load() {
		return true
	}
	solvencyBlocks := c.cfg.SolvencyBlocks
	if solvencyBlocks <= 0 {
		solvencyBlocks = 10
	}
	return height%solvencyBlocks == 0
}

func (c *Client) hasPendingXMRKeyImageCatchup() bool {
	c.keyImageCatchupMu.Lock()
	defer c.keyImageCatchupMu.Unlock()

	for _, state := range c.keyImageCatchupState {
		if state.pending {
			return true
		}
	}
	return false
}

func (c *Client) hasPendingXMRKeyImageCatchupForVault(vaultPubKey string) bool {
	c.keyImageCatchupMu.Lock()
	defer c.keyImageCatchupMu.Unlock()

	state, ok := c.keyImageCatchupState[vaultPubKey]
	return ok && state.pending
}

func (c *Client) xmrSolvencyHasPendingOutboundWork() (bool, string, error) {
	if c.thorchainBridge == nil || c.frostSigner == nil {
		return false, "", nil
	}

	asgards, err := c.thorchainBridge.GetAsgards()
	if err != nil {
		return false, "", fmt.Errorf("fail to get asgards: %w", err)
	}
	for _, vault := range asgards {
		if len(vault.PendingTxBlockHeights) == 0 {
			continue
		}
		return true, fmt.Sprintf("thornode outbound queue not empty for vault %s", keyImageCatchupVaultPubKey(vault)), nil
	}

	trackedVaults, err := c.trackedXMRVaultsForKeyImageCatchup()
	if err != nil {
		return false, "", fmt.Errorf("fail to get tracked xmr vaults: %w", err)
	}
	for _, vault := range trackedVaults {
		vaultPubKey := keyImageCatchupVaultPubKey(vault)
		if vaultPubKey.IsEmpty() {
			continue
		}
		state, err := c.frostSigner.GetOutgoingTransferState(vaultPubKey.String())
		if err != nil {
			if isFrostHTTPStatus(err, 404) {
				continue
			}
			return false, "", fmt.Errorf("fail to get outgoing xmr state for vault %s: %w", vaultPubKey, err)
		}
		for _, record := range state.Outgoing {
			if !record.hasPendingLifecycle() {
				continue
			}
			txHash := strings.TrimSpace(record.TxHash)
			reason := humanizeXMROutgoingPendingReason(record.pendingLifecycleReason())
			if txHash == "" {
				return true, fmt.Sprintf("xmr outbound is still %s", reason), nil
			}
			return true, fmt.Sprintf("xmr outbound %s is still %s", txHash, reason), nil
		}
	}

	return false, "", nil
}

func (c *Client) xmrSolvencySystemIdle() (bool, string, error) {
	if c.scanner == nil {
		return false, "xmr scanner is not initialized", nil
	}
	if c.frostSigner == nil {
		return false, "xmr frost signer is not initialized", nil
	}
	syncState, reason, err := c.xmrLocalSyncState(nil)
	if err != nil {
		return false, "failed to resolve xmr sync state", err
	}
	if syncState != xmrSyncStateAtTip {
		return false, reason, nil
	}

	if queued, active := c.pendingXMRKeyImageBacklog(); queued > 0 || active {
		return false, fmt.Sprintf("xmr key-image queue not empty (queued=%d active=%t)", queued, active), nil
	}

	pending, reason, err := c.xmrSolvencyHasPendingOutboundWork()
	if err != nil {
		return false, "failed to inspect xmr outbound activity", err
	}
	if pending {
		return false, reason, nil
	}

	return true, "", nil
}

func (c *Client) ReportSolvency(height int64) error {
	c.ensureConsolidationState()
	if c.lastSolvencyCheckHeight == nil || c.scanner == nil {
		return nil
	}
	if height <= c.lastSolvencyCheckHeight.Load() || c.scanner.lastFee.Load() == 0 {
		return nil
	}

	idle, reason, err := c.xmrSolvencySystemIdle()
	if err != nil {
		c.logger.Info().Err(err).Int64("height", height).Str("reason", reason).Msg("skip monero solvency: idle check failed")
		return nil
	}
	if !idle {
		if c.solvencyCheckAfterIdle != nil {
			c.solvencyCheckAfterIdle.Store(true)
		}
		c.logger.Info().Int64("height", height).Str("reason", reason).Msg("skip monero solvency: xmr system not idle")
		return nil
	}

	solvencyBlocks := c.cfg.SolvencyBlocks
	if solvencyBlocks <= 0 {
		solvencyBlocks = 10
	}
	forceAfterIdle := c.solvencyCheckAfterIdle != nil && c.solvencyCheckAfterIdle.Load()
	if !forceAfterIdle && height%solvencyBlocks != 0 {
		return nil
	}

	asgardVaults, err := c.thorchainBridge.GetAsgards()
	if err != nil {
		return fmt.Errorf("fail to get asgards: %w", err)
	}

	currentGasFee := cosmos.NewUint(3 * totalFeeAtomicToThorchain(c.scanner.lastFee.Load()))
	msgs := make([]stypes.Solvency, 0, len(asgardVaults))
	solventMsgs := make([]stypes.Solvency, 0, len(asgardVaults))
	checkedVaults := 0
	for i := range asgardVaults {
		address := c.resolveVaultAddress(asgardVaults[i])
		if address.IsEmpty() {
			continue
		}

		acct, acctErr := c.GetAccountByAddress(address.String(), nil)
		if acctErr != nil {
			c.logger.Err(acctErr).Str("address", address.String()).Msg("fail to get monero account balance for solvency")
			continue
		}
		checkedVaults++

		msg := stypes.Solvency{
			Height: height,
			Chain:  c.chain,
			PubKey: asgardVaults[i].PubKey,
			Coins:  acct.Coins,
		}

		if runners.IsVaultSolvent(acct, asgardVaults[i], currentGasFee) {
			solventMsgs = append(solventMsgs, msg)
			continue
		}
		msgs = append(msgs, msg)
	}

	for i := range msgs {
		select {
		case c.globalSolvencyQueue <- msgs[i]:
		case <-time.After(constants.ThorchainBlockTime):
			c.logger.Info().Msg("fail to send monero solvency info to THORChain: timeout")
		}
	}
	c.lastSolvencyCheckHeight.Store(height)
	if c.solvencyCheckAfterIdle != nil {
		c.solvencyCheckAfterIdle.Store(false)
	}
	return nil
}

func (c *Client) logFailClosedCoordination(path string, err error) {
	if err == nil {
		return
	}
	c.logger.Error().
		Err(err).
		Str("coordination_path", path).
		Msg("xmr frost coordination failed closed")
}

func (c *Client) getAsgardAddress() ([]common.Address, error) {
	c.asgardMu.RLock()
	if time.Since(c.lastAsgard) < constants.ThorchainBlockTime && len(c.asgardAddresses) > 0 {
		result := append([]common.Address(nil), c.asgardAddresses...)
		c.asgardMu.RUnlock()
		return result, nil
	}
	c.asgardMu.RUnlock()

	c.asgardMu.Lock()
	defer c.asgardMu.Unlock()
	if time.Since(c.lastAsgard) < constants.ThorchainBlockTime && len(c.asgardAddresses) > 0 {
		return append([]common.Address(nil), c.asgardAddresses...), nil
	}

	vaults, err := c.thorchainBridge.GetAsgards()
	if err != nil {
		return nil, fmt.Errorf("fail to get asgards: %w", err)
	}

	next := make([]common.Address, 0, len(vaults))
	seen := make(map[string]struct{}, len(vaults))

	for _, vault := range vaults {
		address := c.resolveVaultAddress(vault)
		if address.IsEmpty() {
			continue
		}
		key := address.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		next = append(next, address)
	}

	if len(next) > maxAsgardAddresses {
		next = next[len(next)-maxAsgardAddresses:]
	}

	c.asgardAddresses = next
	c.lastAsgard = time.Now()
	result := append([]common.Address(nil), c.asgardAddresses...)

	return result, nil
}

func (c *Client) ensureFrostVaultRegistration(vaultPubKey common.PubKey) error {
	if c.frostSigner == nil || vaultPubKey.IsEmpty() {
		return nil
	}

	registration, err := ensureFrostVaultRegistered(c.logger, c.frostSigner, c.chain, vaultPubKey, c.localVaultBootstrap, "bifrost", 0)
	if err != nil {
		return err
	}
	if !registration.Address.IsEmpty() {
		c.setVaultAddressMapping(vaultPubKey, registration.Address)
	}
	return nil
}

func (c *Client) getVaultAddress(vaultPubKey common.PubKey) string {
	if vaultPubKey.IsEmpty() {
		return ""
	}
	address, err := vaultPubKey.GetAddress(c.chain)
	if err != nil || address.IsEmpty() {
		c.logger.Debug().Err(err).Str("vault_pubkey", vaultPubKey.String()).Msg("fail to derive monero vault address from pubkey")
		return ""
	}
	return address.String()
}

func (c *Client) resolveVaultAddress(vault ttypes.Vault) common.Address {
	vaultPubKey := vault.PubKeyFrost
	if vaultPubKey.IsEmpty() {
		vaultPubKey = vault.PubKeyEddsa
	}
	if vaultPubKey.IsEmpty() {
		return common.NoAddress
	}

	addr, err := vaultPubKey.GetAddress(c.chain)
	if err != nil || addr.IsEmpty() {
		return common.NoAddress
	}
	if !vault.PubKeyFrost.IsEmpty() {
		c.setVaultAddressMapping(vault.PubKeyFrost, addr)
	}
	return addr
}

func (c *Client) setVaultAddressMapping(vaultPubKey common.PubKey, address common.Address) {
	if vaultPubKey.IsEmpty() || address.IsEmpty() {
		return
	}

	key := vaultPubKey.String()
	c.vaultAddressMu.Lock()
	if existing, ok := c.vaultAddressMap[key]; ok && existing.Equals(address) {
		c.vaultAddressMu.Unlock()
		return
	}
	c.vaultAddressMap[key] = address
	if c.vaultAddrToKey == nil {
		c.vaultAddrToKey = make(map[string]string)
	}
	c.vaultAddrToKey[strings.ToLower(address.String())] = key
	c.vaultAddressMu.Unlock()

	if c.vaultAddressStore != nil {
		if err := c.vaultAddressStore.Set(key, address.String()); err != nil {
			c.logger.Debug().Err(err).Str("vault_pubkey", key).Msg("fail to persist monero vault address mapping")
		}
	}
	if c.scanner != nil {
		c.scanner.SetVaultAddress(vaultPubKey, address.String())
	}
}

func (c *Client) getMappedVaultAddress(vaultPubKey common.PubKey) (common.Address, bool) {
	if vaultPubKey.IsEmpty() {
		return common.NoAddress, false
	}

	c.vaultAddressMu.RLock()
	address, ok := c.vaultAddressMap[vaultPubKey.String()]
	c.vaultAddressMu.RUnlock()
	if ok && !address.IsEmpty() {
		return address, true
	}
	return common.NoAddress, false
}

func (c *Client) loadVaultAddressMap() error {
	if c.vaultAddressStore == nil {
		return nil
	}
	// Purge any v1 entries — the v1 store held stale eddsa-key mappings from a
	// prior bug that caused double registration on the sidecar.
	if err := c.vaultAddressStore.PurgeLegacyEntries(); err != nil {
		c.logger.Warn().Err(err).Msg("fail to purge legacy vault address store entries")
	}
	stored, err := c.vaultAddressStore.List()
	if err != nil {
		return err
	}
	for vaultPubKey, addressStr := range stored {
		vaultKey, keyErr := common.NewPubKey(vaultPubKey)
		if keyErr != nil {
			c.logger.Debug().Err(keyErr).Str("vault_pubkey", vaultPubKey).Msg("skip invalid persisted monero vault pubkey")
			continue
		}
		address, addrErr := common.NewAddress(addressStr)
		if addrErr != nil {
			c.logger.Debug().Err(addrErr).Str("address", addressStr).Msg("skip invalid persisted monero vault address")
			continue
		}
		derived, deriveErr := vaultKey.GetAddress(c.chain)
		if deriveErr != nil || derived.IsEmpty() {
			c.logger.Debug().Err(deriveErr).Str("vault_pubkey", vaultPubKey).Msg("skip persisted monero vault mapping: cannot derive address from vault pubkey")
			continue
		}
		if !derived.Equals(address) {
			c.logger.Warn().
				Str("vault_pubkey", vaultPubKey).
				Str("persisted_address", address.String()).
				Str("derived_address", derived.String()).
				Msg("ignoring persisted monero vault mapping that does not match derived vault address")
			continue
		}
		c.setVaultAddressMapping(vaultKey, address)
	}
	return nil
}

func txVaultPubKey(tx stypes.TxOutItem) common.PubKey {
	if !tx.VaultPubKeyFrost.IsEmpty() {
		return tx.VaultPubKeyFrost
	}
	return tx.VaultPubKeyEddsa
}

func txCacheVault(tx stypes.TxOutItem, chain common.Chain) string {
	return stypes.BroadcastCacheKey(txVaultPubKey(tx).String(), chain.String())
}

func xmrCoinbaseThorchain() uint64 {
	coinbaseThorchain := uint64(defaultCoinbaseValue / moneroAtomicToThorchain)
	if coinbaseThorchain == 0 {
		return 1
	}
	return coinbaseThorchain
}

func xmrConfirmationRequired(totalValue, confValue cosmos.Uint) uint64 {
	if confValue.IsZero() {
		return 0
	}
	if totalValue.IsZero() {
		return 0
	}

	// Round up so any non-zero external inbound gets at least one confirmation
	// and fractional multiples of the risk unit do not collapse into the lower
	// confirmation bucket.
	numerator := new(big.Int).Add(totalValue.BigInt(), confValue.BigInt())
	numerator.Sub(numerator, big.NewInt(1))
	return numerator.Div(numerator, confValue.BigInt()).Uint64()
}

func xmrThorchainAmountToAtomic(amount cosmos.Uint) (uint64, error) {
	if amount.IsNil() {
		return 0, fmt.Errorf("xmr outbound amount is nil")
	}

	amountAtomic := amount.MulUint64(moneroAtomicToThorchain)
	amountAtomicBI := amountAtomic.BigInt()
	if amountAtomicBI == nil || !amountAtomicBI.IsUint64() {
		return 0, fmt.Errorf("xmr outbound amount %s overflows uint64 after atomic conversion", amount.String())
	}

	return amountAtomicBI.Uint64(), nil
}

func (c *Client) getVaultByPubKey(vaultPubKey common.PubKey) (ttypes.Vault, error) {
	if vaultPubKey.IsEmpty() {
		return ttypes.Vault{}, fmt.Errorf("vault pubkey is empty")
	}
	vault, err := c.thorchainBridge.GetVault(vaultPubKey.String())
	if err == nil {
		return vault, nil
	}

	vaults, asgardErr := c.thorchainBridge.GetAsgards()
	if asgardErr != nil {
		return ttypes.Vault{}, asgardErr
	}
	for _, item := range vaults {
		if item.PubKeyEddsa.Equals(vaultPubKey) {
			return item, nil
		}
	}
	return ttypes.Vault{}, err
}
