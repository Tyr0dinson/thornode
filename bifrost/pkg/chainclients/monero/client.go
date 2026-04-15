package monero

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
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
	mimirXMRConsolidationMinUnlockedAtomic   = "XMRConsolidationMinUnlockedAtomic"
	mimirXMRConsolidationReserveBps          = "XMRConsolidationReserveBps"
	mimirXMRConsolidationChurnLookahead      = "XMRConsolidationChurnLookaheadBlocks"
	mimirXMRConsolidationMaxRetries          = "XMRConsolidationMaxRetries"

	defaultXMRConsolidationCheckIntervalBlocks = int64(10)
	defaultXMRConsolidationCooldownBlocks      = int64(30)
	defaultXMRConsolidationMinUnlockedOutputs  = int64(40)
	defaultXMRConsolidationTargetOutputs       = int64(15)
	defaultXMRConsolidationMinUnlockedAtomic   = uint64(1_000_000_000_000) // 1 XMR (piconero)
	defaultXMRConsolidationReserveBps          = int64(50)                 // 0.50%
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
	fromAddress  string
	toAddress    string
	amountAtomic uint64
}

type lockedOutputsError struct {
	available uint64
	required  uint64
	total     uint64
	locked    uint64
}

type keyImageBacklogError struct {
	queued uint64
	active bool
}

const xmrDefaultMaxConfirmationCap uint64 = 30 // ~1 hour at 2-minute XMR blocks

func (e *lockedOutputsError) Error() string {
	return fmt.Sprintf(
		"insufficient xmr balance for signing: available=%d required=%d (total=%d locked=%d, outputs may need 10 confirmations)",
		e.available, e.required, e.total, e.locked,
	)
}

func (e *lockedOutputsError) RetryAfterNextBlock() bool { return true }

func (e *keyImageBacklogError) Error() string {
	return fmt.Sprintf("pending xmr key-image backlog: queued=%d active=%t", e.queued, e.active)
}

func (e *keyImageBacklogError) RetryAfterNextBlock() bool { return true }

type xmrConsolidationSettings struct {
	Enabled             bool
	CheckIntervalBlocks int64
	CooldownBlocks      int64
	MinUnlockedOutputs  int64
	TargetOutputs       int64
	MinUnlockedAtomic   uint64
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

type xmrTxKeyCatchupRef struct {
	txHash string
	txKeys []string
}

type xmrTxKeyCatchupState struct {
	refs    []xmrTxKeyCatchupRef
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
	keyImageCatchupMu       sync.Mutex
	keyImageCatchupState    map[string]xmrKeyImageCatchupState
	txKeyCatchupMu          sync.Mutex
	txKeyCatchupState       map[string]xmrTxKeyCatchupState
	keyImageCeremonyGateMu  sync.Mutex
	keyImageCeremonyGate    *xmrKeyImageCeremonyGate
	keyImageProofCacheMu    sync.Mutex
	keyImageProofCache      map[string]xmrKeyImageProofCacheEntry
	consolidateInProgress   *atomic.Bool
	lastConsolidationHeight *atomic.Int64
	lastSolvencyCheckHeight *atomic.Int64
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
		keyImageCatchupState:    make(map[string]xmrKeyImageCatchupState),
		consolidateInProgress:   atomic.NewBool(false),
		lastConsolidationHeight: atomic.NewInt64(0),
		signerCacheManager:      nil,
		vaultAddressMap:         make(map[string]common.Address),
		vaultAddrToKey:          make(map[string]string),
		lastSolvencyCheckHeight: atomic.NewInt64(0),
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
	errText := err.Error()
	return strings.Contains(errText, "leader not reachable") ||
		strings.Contains(errText, "fail to join party, timeout")
}

var errXMRKeyImageNotActiveSigner = errors.New("xmr key-image local signer not active")

func (c *Client) Start(
	globalTxsQueue chan stypes.TxIn,
	globalErrataQueue chan stypes.ErrataBlock,
	globalSolvencyQueue chan stypes.Solvency,
	globalNetworkFeeQueue chan common.NetworkFee,
) {
	c.ensureConsolidationState()
	c.globalSolvencyQueue = globalSolvencyQueue
	c.scanner.SetErrataQueue(globalErrataQueue)
	c.blockScanner.Start(globalTxsQueue, globalNetworkFeeQueue)
	c.wg.Add(1)
	go runners.SolvencyCheckRunner(c.GetChain(), c, c.thorchainBridge, c.stopchan, c.wg, constants.ThorchainBlockTime)
	c.wg.Add(1)
	go c.consolidationRunner()
	c.wg.Add(1)
	go c.keyImagesSyncRunner()
	c.wg.Add(1)
	go c.txKeysSyncRunner()
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

func (c *Client) keyImagesSyncRunner() {
	defer c.wg.Done()

	c.catchupKeyImages()

	ticker := time.NewTicker(constants.ThorchainBlockTime * 10)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopchan:
			return
		case <-ticker.C:
			c.catchupKeyImages()
		}
	}
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
	}
}

func (c *Client) txKeysSyncRunner() {
	defer c.wg.Done()

	c.catchupTxKeys()

	ticker := time.NewTicker(constants.ThorchainBlockTime * 10)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopchan:
			return
		case <-ticker.C:
			c.catchupTxKeys()
		}
	}
}

func (c *Client) catchupTxKeys() {
	if c.thorchainBridge == nil || c.frostSigner == nil {
		return
	}
	vaults, err := c.trackedXMRVaultsForKeyImageCatchup()
	if err != nil {
		c.logger.Err(err).Msg("xmr tx-key catchup: failed to get tracked xmr vaults")
		return
	}
	for _, vault := range vaults {
		vaultPubKey := keyImageCatchupVaultPubKey(vault)
		if vaultPubKey.IsEmpty() {
			c.clearTxKeyCatchupState(vaultPubKey.String())
			continue
		}

		refs := makeTxKeyCatchupRefs(vault.TxKeysXmr)
		if len(refs) == 0 {
			c.clearTxKeyCatchupState(vaultPubKey.String())
			continue
		}
		keyImageRefs := makeApplyKeyImageRefs(vault.KeyImagesXmr)
		if len(keyImageRefs) > 0 {
			keyImagesPending, verifyErr := c.keyImageCatchupNeedsReplay(vaultPubKey.String(), keyImageRefs)
			if verifyErr != nil {
				c.logger.Debug().
					Err(verifyErr).
					Str("vault", vaultPubKey.String()).
					Msg("xmr tx-key catchup: failed to verify key-image state, blocking outbound audit replay")
				c.clearTxKeyCatchupState(vaultPubKey.String())
				continue
			}
			if keyImagesPending {
				c.logger.Info().
					Str("vault", vaultPubKey.String()).
					Int("key_images", len(keyImageRefs)).
					Msg("xmr tx-key catchup: waiting for key-image reconciliation before outbound audit replay")
				c.clearTxKeyCatchupState(vaultPubKey.String())
				continue
			}
		}

		batch, shouldApply := c.nextTxKeyCatchupBatch(vaultPubKey.String(), refs)
		stateCheck, verifyErr := c.txKeyCatchupNeedsReplay(vaultPubKey.String(), refs)
		if verifyErr != nil {
			c.logger.Debug().
				Err(verifyErr).
				Str("vault", vaultPubKey.String()).
				Msg("xmr tx-key catchup: failed to verify signer state, forcing replay")
			stateCheck.needsReplay = true
		}
		if len(stateCheck.removeHashes) > 0 {
			_, pruneErr := c.frostSigner.PruneOutgoingTransfers(xmrFrostPruneOutgoingTransfersRequest{
				VaultPubKey:       vaultPubKey.String(),
				TxHashes:          append([]string(nil), stateCheck.removeHashes...),
				ObservedMinedOnly: true,
				TxKeyBackedOnly:   true,
			})
			if pruneErr != nil {
				c.logger.Err(pruneErr).
					Str("vault", vaultPubKey.String()).
					Strs("tx_hashes", stateCheck.removeHashes).
					Msg("xmr tx-key catchup: failed to prune stale signer outbounds")
				stateCheck.needsReplay = true
			}
		}
		if stateCheck.needsReplay && !shouldApply {
			c.clearTxKeyCatchupState(vaultPubKey.String())
			batch = cloneTxKeyCatchupRefs(refs)
			shouldApply = true
		}
		if !shouldApply {
			continue
		}

		type catchupOutbound struct {
			ref  xmrTxKeyCatchupRef
			meta xmrFrostOutboundMeta
		}

		ordered := make([]catchupOutbound, 0, len(batch))
		pending := false
		for _, ref := range batch {
			meta, metaErr := c.getTxKeyCatchupOutboundMeta(vaultPubKey, ref.txHash)
			if metaErr != nil {
				c.logger.Err(metaErr).
					Str("vault", vaultPubKey.String()).
					Str("tx_hash", ref.txHash).
					Msg("xmr tx-key catchup: failed to load outbound metadata")
				pending = true
				break
			}
			ordered = append(ordered, catchupOutbound{ref: ref, meta: meta})
		}

		sort.SliceStable(ordered, func(i, j int) bool {
			leftHeight := ordered[i].meta.ObservedHeight
			rightHeight := ordered[j].meta.ObservedHeight
			if leftHeight == rightHeight {
				return strings.ToLower(strings.TrimSpace(ordered[i].ref.txHash)) <
					strings.ToLower(strings.TrimSpace(ordered[j].ref.txHash))
			}
			if leftHeight <= 0 {
				return false
			}
			if rightHeight <= 0 {
				return true
			}
			return leftHeight < rightHeight
		})

		applied := 0
		for _, outbound := range ordered {
			_, applyErr := c.frostSigner.ValidateOutbound(xmrFrostValidateOutboundRequest{
				VaultPubKey: vaultPubKey.String(),
				TxHash:      outbound.ref.txHash,
				TxKeys:      append([]string(nil), outbound.ref.txKeys...),
				FromAddress: outbound.meta.FromAddress,
				ToAddress:   outbound.meta.ToAddress,
				Amount:      outbound.meta.Amount,
				Fee:         outbound.meta.Fee,
				Memo:        outbound.meta.Memo,
				Reconcile:   true,
			})
			if applyErr != nil {
				c.logger.Err(applyErr).
					Str("vault", vaultPubKey.String()).
					Str("tx_hash", outbound.ref.txHash).
					Msg("xmr tx-key catchup: failed to replay outbound audit record")
				pending = true
				break
			}
			applied++
		}

		c.recordTxKeyCatchup(vaultPubKey.String(), refs, pending)
		c.logger.Info().
			Str("vault", vaultPubKey.String()).
			Int("applied", applied).
			Int("sent", len(batch)).
			Int("total", len(refs)).
			Bool("pending", pending).
			Msg("xmr tx-key catchup: replayed outbound audit records from thorchain")
	}
}

func (c *Client) getTxKeyCatchupOutboundMeta(vaultPubKey common.PubKey, txHash string) (xmrFrostOutboundMeta, error) {
	voter, err := c.thorchainBridge.GetObservedTxVoter(txHash)
	if err != nil {
		return xmrFrostOutboundMeta{}, fmt.Errorf("fail to get observed tx voter for %s: %w", txHash, err)
	}

	observed, err := selectObservedOutboundForVault(voter, txHash, vaultPubKey)
	if err != nil {
		return xmrFrostOutboundMeta{}, err
	}

	amountAtomic, err := xmrThorchainAmountToAtomic(observed.Tx.Coins.GetCoin(common.XMRAsset).Amount)
	if err != nil {
		return xmrFrostOutboundMeta{}, fmt.Errorf("fail to convert xmr outbound amount to atomic for %s: %w", txHash, err)
	}

	var feeAtomic uint64
	gasCoin := observed.Tx.Gas.ToCoins().GetCoin(common.XMRAsset)
	if !gasCoin.IsEmpty() {
		feeAtomic, err = xmrThorchainAmountToAtomic(gasCoin.Amount)
		if err != nil {
			return xmrFrostOutboundMeta{}, fmt.Errorf("fail to convert xmr outbound fee to atomic for %s: %w", txHash, err)
		}
	}

	if observed.Tx.ToAddress.IsEmpty() {
		return xmrFrostOutboundMeta{}, fmt.Errorf("observed xmr outbound %s is missing destination address", txHash)
	}

	if meta, ok, err := c.txKeyCatchupCanonicalOutboundMeta(vaultPubKey, txHash, observed.Tx.Memo); err != nil {
		return xmrFrostOutboundMeta{}, err
	} else if ok {
		meta.ObservedHeight = observed.BlockHeight
		return meta, nil
	}

	return xmrFrostOutboundMeta{
		FromAddress:    observed.Tx.FromAddress.String(),
		ToAddress:      observed.Tx.ToAddress.String(),
		Amount:         amountAtomic,
		Fee:            feeAtomic,
		Memo:           observed.Tx.Memo,
		ObservedHeight: observed.BlockHeight,
	}, nil
}

func (c *Client) txKeyCatchupCanonicalOutboundMeta(vaultPubKey common.PubKey, txHash, memoText string) (xmrFrostOutboundMeta, bool, error) {
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

	meta, found, err := c.txKeyCatchupMetaFromTxStatus(vaultPubKey, txHash, status)
	if err != nil {
		return xmrFrostOutboundMeta{}, false, err
	}
	return meta, found, nil
}

func (c *Client) txKeyCatchupMetaFromTxStatus(
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

	if fallback != nil && vaultPubKey.IsEmpty() {
		return fallback, nil
	}
	if fallback != nil {
		return nil, fmt.Errorf(
			"observed tx voter %s has no xmr outbound observation for vault %s",
			txHash,
			vaultPubKey.String(),
		)
	}
	return nil, fmt.Errorf("observed tx voter %s has no xmr outbound observation", txHash)
}

func (c *Client) trackedXMRVaultsForKeyImageCatchup() (ttypes.Vaults, error) {
	if c.scanner != nil {
		return c.scanner.trackedXMRVaults()
	}

	vaults, err := c.thorchainBridge.GetAsgards()
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

	pubKeys, err := c.thorchainBridge.GetPubKeys()
	if err != nil {
		c.logger.Debug().Err(err).Msg("xmr key-image catchup: falling back to current asgard vaults")
		return mapVaults(lookup), nil
	}

	requested := make(map[string]struct{})
	for _, pair := range pubKeys {
		if pair.Algo != common.SigningAlgoEd25519 || !pair.Inactive || pair.PubKey.IsEmpty() {
			continue
		}
		pubKeyString := pair.PubKey.String()
		if _, ok := requested[pubKeyString]; ok {
			continue
		}
		requested[pubKeyString] = struct{}{}

		vault, loadErr := c.thorchainBridge.GetVault(pubKeyString)
		if loadErr != nil {
			c.logger.Debug().
				Err(loadErr).
				Str("vault_pubkey", pubKeyString).
				Msg("xmr key-image catchup: skip inactive vault pubkey")
			continue
		}
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

func xmrOutputRefKey(txHash string, outputIndex uint32) string {
	return strings.ToLower(strings.TrimSpace(txHash)) + ":" + strconv.FormatUint(uint64(outputIndex), 10)
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
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if normalized == "" {
			continue
		}
		out = append(out, normalized)
	}
	sort.Strings(out)
	return out
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
	outputs, err := c.frostSigner.GetAllOutputs(vaultPubKey)
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
		hasSignerState := output.Spent || strings.TrimSpace(output.KeyImage) != ""
		switch {
		case !wantedOutput && hasSignerState:
			return true, nil
		case !wantedOutput:
			continue
		case !output.Spent:
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

type xmrTxKeyCatchupStateCheck struct {
	needsReplay  bool
	removeHashes []string
}

func legacyTxKeyCatchupNeedsReplay(
	vaultPubKey string,
	refs []xmrTxKeyCatchupRef,
	transfers xmrFrostTransfersResponse,
) xmrTxKeyCatchupStateCheck {
	wanted := make(map[string][]string, len(refs))
	for _, ref := range refs {
		wanted[strings.ToLower(strings.TrimSpace(ref.txHash))] = normalizeTxKeyValues(ref.txKeys)
	}

	seen := make(map[string]struct{}, len(refs))
	needsReplay := false
	for _, transfer := range transfers.Outgoing {
		if !strings.EqualFold(strings.TrimSpace(transfer.VaultPubKey), vaultPubKey) {
			continue
		}
		txHash := strings.ToLower(strings.TrimSpace(transfer.TxID))
		wantKeys, ok := wanted[txHash]
		if !ok {
			continue
		}
		if !sameNormalizedTxKeyValues(normalizeTxKeyValues(transfer.TxKeys), wantKeys) {
			needsReplay = true
			continue
		}
		seen[txHash] = struct{}{}
	}

	for txHash := range wanted {
		if _, ok := seen[txHash]; !ok {
			needsReplay = true
		}
	}

	return xmrTxKeyCatchupStateCheck{needsReplay: needsReplay}
}

func (c *Client) txKeyCatchupNeedsReplay(vaultPubKey string, refs []xmrTxKeyCatchupRef) (xmrTxKeyCatchupStateCheck, error) {
	state, err := c.frostSigner.GetOutgoingTransferState(vaultPubKey)
	if err != nil {
		transfers, transferErr := c.frostSigner.GetTransfersByHeight(0)
		if transferErr != nil {
			return xmrTxKeyCatchupStateCheck{}, err
		}
		return legacyTxKeyCatchupNeedsReplay(vaultPubKey, refs, transfers), nil
	}

	wanted := make(map[string][]string, len(refs))
	for _, ref := range refs {
		wanted[strings.ToLower(strings.TrimSpace(ref.txHash))] = normalizeTxKeyValues(ref.txKeys)
	}

	seen := make(map[string]struct{}, len(refs))
	removeSet := make(map[string]struct{})
	needsReplay := false
	for _, record := range state.Outgoing {
		if !strings.EqualFold(strings.TrimSpace(record.Status), "observed_mined") {
			continue
		}
		txHash := strings.ToLower(strings.TrimSpace(record.TxHash))
		if txHash == "" {
			continue
		}
		localKeys := normalizeTxKeyValues(record.TxKeys)
		wantKeys, ok := wanted[txHash]
		if !ok {
			if len(localKeys) > 0 {
				removeSet[strings.TrimSpace(record.TxHash)] = struct{}{}
				needsReplay = true
			}
			continue
		}
		if len(localKeys) == 0 {
			needsReplay = true
			continue
		}
		if !sameNormalizedTxKeyValues(localKeys, wantKeys) {
			removeSet[strings.TrimSpace(record.TxHash)] = struct{}{}
			needsReplay = true
			continue
		}
		seen[txHash] = struct{}{}
	}

	for txHash := range wanted {
		if _, ok := seen[txHash]; !ok {
			needsReplay = true
		}
	}

	removeHashes := make([]string, 0, len(removeSet))
	for txHash := range removeSet {
		removeHashes = append(removeHashes, txHash)
	}
	sort.Strings(removeHashes)

	return xmrTxKeyCatchupStateCheck{
		needsReplay:  needsReplay,
		removeHashes: removeHashes,
	}, nil
}

func makeTxKeyCatchupRefs(refs []*common.XmrTxKeyRef) []xmrTxKeyCatchupRef {
	out := make([]xmrTxKeyCatchupRef, 0, len(refs))
	for _, ref := range refs {
		if ref == nil || strings.TrimSpace(ref.TxHash) == "" {
			continue
		}
		out = append(out, xmrTxKeyCatchupRef{
			txHash: ref.TxHash,
			txKeys: append([]string(nil), ref.TxKeys...),
		})
	}
	return out
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

func (c *Client) nextTxKeyCatchupBatch(vaultPubKey string, refs []xmrTxKeyCatchupRef) ([]xmrTxKeyCatchupRef, bool) {
	c.txKeyCatchupMu.Lock()
	defer c.txKeyCatchupMu.Unlock()

	if c.txKeyCatchupState == nil {
		c.txKeyCatchupState = make(map[string]xmrTxKeyCatchupState)
	}

	state, ok := c.txKeyCatchupState[vaultPubKey]
	switch {
	case !ok:
		return cloneTxKeyCatchupRefs(refs), true
	case state.pending:
		return cloneTxKeyCatchupRefs(refs), true
	case sameTxKeyCatchupRefs(state.refs, refs):
		return nil, false
	case hasTxKeyCatchupPrefix(refs, state.refs):
		return cloneTxKeyCatchupRefs(refs[len(state.refs):]), true
	default:
		return cloneTxKeyCatchupRefs(refs), true
	}
}

func (c *Client) recordTxKeyCatchup(vaultPubKey string, refs []xmrTxKeyCatchupRef, pending bool) {
	c.txKeyCatchupMu.Lock()
	defer c.txKeyCatchupMu.Unlock()

	if c.txKeyCatchupState == nil {
		c.txKeyCatchupState = make(map[string]xmrTxKeyCatchupState)
	}
	c.txKeyCatchupState[vaultPubKey] = xmrTxKeyCatchupState{
		refs:    cloneTxKeyCatchupRefs(refs),
		pending: pending,
	}
}

func (c *Client) clearTxKeyCatchupState(vaultPubKey string) {
	if vaultPubKey == "" {
		return
	}
	c.txKeyCatchupMu.Lock()
	defer c.txKeyCatchupMu.Unlock()
	delete(c.txKeyCatchupState, vaultPubKey)
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

func cloneTxKeyCatchupRefs(refs []xmrTxKeyCatchupRef) []xmrTxKeyCatchupRef {
	if len(refs) == 0 {
		return nil
	}
	out := make([]xmrTxKeyCatchupRef, len(refs))
	for i, ref := range refs {
		out[i] = xmrTxKeyCatchupRef{
			txHash: ref.txHash,
			txKeys: append([]string(nil), ref.txKeys...),
		}
	}
	return out
}

func sameTxKeyCatchupRefs(left, right []xmrTxKeyCatchupRef) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].txHash != right[i].txHash {
			return false
		}
		if len(left[i].txKeys) != len(right[i].txKeys) {
			return false
		}
		for j := range left[i].txKeys {
			if left[i].txKeys[j] != right[i].txKeys[j] {
				return false
			}
		}
	}
	return true
}

func hasTxKeyCatchupPrefix(full, prefix []xmrTxKeyCatchupRef) bool {
	if len(prefix) > len(full) {
		return false
	}
	for i := range prefix {
		if prefix[i].txHash != full[i].txHash {
			return false
		}
		if len(prefix[i].txKeys) != len(full[i].txKeys) {
			return false
		}
		for j := range prefix[i].txKeys {
			if prefix[i].txKeys[j] != full[i].txKeys[j] {
				return false
			}
		}
	}
	return true
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
	if spendableAtomic < settings.MinUnlockedAtomic {
		c.logConsolidationSkip(vault, stateKey, thorchainHeight, state, "spendable balance below threshold", func(e *zerolog.Event) {
			e.Uint64("spendable_atomic", spendableAtomic).
				Uint64("min_unlocked_atomic", settings.MinUnlockedAtomic)
		})
		return
	}

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
			SigningID: fmt.Sprintf("xmr-consolidation:%s:%d:%d:%d", stateKey, thorchainHeight, attempt, amountAtomic),
		}

		rawTx, _, _, signErr := c.SignTx(txOutItem, thorchainHeight)
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
		if out.Spent {
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
		MinUnlockedAtomic:   c.getMimirUint64(mimirXMRConsolidationMinUnlockedAtomic, fallbackUint64(c.cfg.XMRConsolidationMinUnlockedAtomic, defaultXMRConsolidationMinUnlockedAtomic)),
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
	if settings.MinUnlockedAtomic == 0 {
		settings.MinUnlockedAtomic = defaultXMRConsolidationMinUnlockedAtomic
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
	if queued, active := c.pendingXMRKeyImageBacklog(); queued > 0 || active {
		c.logger.Info().
			Str("vault_pub_key", signReqCtx.vaultPubKey.String()).
			Str("memo", tx.Memo).
			Uint64("pending_key_image_queue", queued).
			Bool("key_image_active", active).
			Msg("deferring XMR keysign until key-image backlog clears")
		return nil, nil, nil, &keyImageBacklogError{
			queued: queued,
			active: active,
		}
	}
	version, err := c.thorchainBridge.GetThorchainVersion()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fail to resolve thorchain version: %w", err)
	}

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
		SweepIfFinal:  shouldSweepXMRMigration(tx, maxGasAtomic),
		Memo:          tx.Memo,
		FeeRate:       feeRate,
		SignerPubKeys: members,
		BlockHeight:   height,
		Version:       version.String(),
		TxInHash:      txInHash,
		RequestID:     tx.SigningID,
		MaxInputs:     maxInputs,
		DustThreshold: dustThreshold,
	}
	resp, err := c.tssServer.FrostKeySign(frostReq, newFrostSignerAdapter(c.frostSigner))
	if err != nil {
		c.observeXMRActualFeeEstimate(height, feeRate, xmrNecessaryFeeFromError(err))
		return nil, nil, nil, fmt.Errorf("fail to execute FROST keysign: %w", err)
	}
	if resp.Status != gotsscommon.Success {
		c.observeXMRActualFeeEstimate(height, feeRate, xmrNecessaryFeeFromString(resp.Blame.FailReason))
		if len(resp.Blame.BlameNodes) > 0 {
			return nil, nil, nil, bifrosttss.NewKeysignError(frostBlameToThorchainBlame(resp.Blame))
		}
		return nil, nil, nil, fmt.Errorf("FROST keysign failed: %s", resp.Blame.FailReason)
	}
	c.observeXMRActualFeeEstimate(height, feeRate, resp.FeeAtomic)
	msgID := strings.TrimSpace(resp.MsgID)
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
		if parsedMemo.IsInbound() {
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

	if err = c.ensureXMRVaultBalance(vaultPubKey, amountAtomic, tx.MaxGas); err != nil {
		return xmrValidatedSignRequest{}, err
	}

	return xmrValidatedSignRequest{
		vaultPubKey:  vaultPubKey,
		fromAddress:  fromAddress,
		toAddress:    tx.ToAddress.String(),
		amountAtomic: amountAtomic,
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

func (c *Client) ensureXMRVaultBalance(vaultPubKey common.PubKey, amountAtomic uint64, maxGas common.Gas) error {
	if c.frostSigner == nil {
		return nil
	}

	if err := c.ensureFrostVaultRegistration(vaultPubKey); err != nil {
		return err
	}

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
		totalAtomic := balance.Balance
		lockedAtomic := totalAtomic - availableAtomic
		if lockedAtomic > 0 {
			return &lockedOutputsError{
				available: availableAtomic,
				required:  requiredAtomic,
				total:     totalAtomic,
				locked:    lockedAtomic,
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
	if c == nil || c.scanner == nil || feeAtomic == 0 || feeRatePerWeight == 0 {
		return
	}
	weight := xmrWeightFromActualFee(feeAtomic, feeRatePerWeight)
	if weight == 0 {
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

func shouldSweepXMRMigration(tx stypes.TxOutItem, maxGasAtomic uint64) bool {
	if maxGasAtomic == 0 {
		return false
	}
	memoForParsing := tx.GetMemo()
	if memoForParsing == "" {
		return false
	}
	parsedMemo, err := memo.ParseMemo(common.LatestVersion, memoForParsing)
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
	if c.frostSigner == nil {
		return fmt.Errorf("frost signer not configured")
	}
	if tx.Tx.Chain != common.XMRChain {
		return fmt.Errorf("unexpected chain for xmr outbound validation: %s", tx.Tx.Chain)
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

	trackedOutboundChange := len(tx.XmrKeyImages) > 0
	if len(tx.TxKeys) > 0 {
		amountAtomic, err := xmrThorchainAmountToAtomic(tx.Tx.Coins.GetCoin(common.XMRAsset).Amount)
		if err != nil {
			return fmt.Errorf("fail to convert xmr outbound amount to atomic: %w", err)
		}
		var feeAtomic uint64
		gasCoin := tx.Tx.Gas.ToCoins().GetCoin(common.XMRAsset)
		if !gasCoin.IsEmpty() {
			feeAtomic, err = xmrThorchainAmountToAtomic(gasCoin.Amount)
			if err != nil {
				return fmt.Errorf("fail to convert xmr outbound fee to atomic: %w", err)
			}
		}

		validateResp, err := c.frostSigner.ValidateOutbound(xmrFrostValidateOutboundRequest{
			VaultPubKey: tx.ObservedPubKey.String(),
			TxHash:      tx.Tx.ID.String(),
			TxKeys:      append([]string(nil), tx.TxKeys...),
			FromAddress: tx.Tx.FromAddress.String(),
			ToAddress:   tx.Tx.ToAddress.String(),
			Amount:      amountAtomic,
			Fee:         feeAtomic,
			Memo:        tx.Tx.Memo,
			Reconcile:   true,
		})
		if err != nil {
			return fmt.Errorf("fail to validate xmr outbound via sidecar: %w", err)
		}
		trackedOutboundChange = trackedOutboundChange || validateResp.Tracked
	} else {
		c.logger.Info().
			Str("tx_hash", tx.Tx.ID.String()).
			Str("vault_pub_key", tx.ObservedPubKey.String()).
			Int("key_image_count", len(tx.XmrKeyImages)).
			Msg("skipping xmr tx-key outbound audit; validating finalized key-images only")
	}
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
	if isInboundToVault && isOutboundObservationMemo(item.Memo) && !c.isKnownVaultAddress(item.Sender) {
		c.logger.Debug().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Str("observed_vault_pub_key", item.ObservedVaultPubKey.String()).
			Str("sender", item.Sender).
			Str("memo", item.Memo).
			Msg("skipping delayed XMR change inbound; key-images will ride on finalized outbound")
		return nil, false, nil
	}
	if len(item.XmrKeyImages) > 0 {
		c.logger.Debug().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Int("proof_count", len(item.XmrKeyImages)).
			Msg("observed tx item already has XMR key-images")
		return item, true, nil
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
	if !c.localNodeIsVaultSigner(ceremonyCtx.members) {
		c.logger.Debug().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
			Str("local_node_pub_key", c.localNodePubKey.String()).
			Strs("signer_pub_keys", ceremonyCtx.members).
			Msg("skipping XMR key-image generation; local node not in signer set")
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
	pendingOutputs := xmrOutputsMissingKeyImages(outputs)
	backlogOutputs := xmrOutputsMissingKeyImages(allOutputs)
	backlogHeadTxHash := ""
	if len(backlogOutputs) > 0 {
		backlogHeadTxHash = normalizeTxID(backlogOutputs[0].TxHash)
	}
	if len(outputs) == 0 {
		if isOutboundFromVault {
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
	if len(pendingOutputs) == 0 {
		c.logger.Info().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
			Int("tracked_output_count", len(outputs)).
			Msg("tracked XMR outputs already have local key-images; waiting for finalized proof propagation")
		return nil, false, nil
	}
	if len(pendingOutputs) != len(outputs) {
		c.logger.Info().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
			Int("tracked_output_count", len(outputs)).
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

	if cachedProofs, ok := c.getCachedXMRKeyImageProofs(ceremonyCtx.vaultPubKey, normalizeTxID(item.Tx), pendingOutputs); ok {
		item.XmrKeyImages = cachedProofs
		c.logger.Info().
			Str("tx_hash", normalizeTxID(item.Tx)).
			Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
			Int("proof_count", len(cachedProofs)).
			Msg("reusing cached XMR key-images for finalized observation")
		return item, true, nil
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
		Int("missing_key_image_output_count", len(pendingOutputs)).
		Int64("finalise_height", finaliseHeight).
		Str("memo", item.Memo).
		Msg("preparing finalized XMR observation for key-image generation")
	proofs, err := c.generateObservedTxKeyImages(ceremonyCtx.vaultPubKey, normalizeTxID(item.Tx), pendingOutputs, ceremonyCtx.members, finaliseHeight, version.String())
	if err != nil {
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
	c.putCachedXMRKeyImageProofs(ceremonyCtx.vaultPubKey, normalizeTxID(item.Tx), pendingOutputs, proofs)
	item.XmrKeyImages = proofs
	c.logger.Info().
		Str("tx_hash", normalizeTxID(item.Tx)).
		Str("canonical_vault_pub_key", ceremonyCtx.vaultPubKey.String()).
		Int("proof_count", len(proofs)).
		Msg("attached XMR key-images to finalized observation")
	return item, true, nil
}

func (c *Client) ValidateObservedTxIn(tx common.ObservedTx) error {
	return c.validateObservedTxKeyImages(tx, true, false)
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
	// Account reporting (used by solvency comparison vs thornode's vault.coins)
	// must use the TOTAL on-chain balance — locked + unlocked. Locked outputs
	// (those in the 10-conf maturity window) DO exist on chain and must count
	// toward the wallet's view of "what we own", otherwise every fresh inbound
	// triggers a false-positive insolvency report during normal operation.
	// Spendability for sign-time input selection is a separate concern handled
	// by ensureXMRVaultBalance which uses spendableBalanceAtomic.
	amount := cosmos.NewUint(balance.Balance / moneroAtomicToThorchain)
	return common.Account{
		Sequence:      0,
		AccountNumber: 0,
		Coins: common.NewCoins(
			common.NewCoin(common.XMRAsset, amount),
		),
		HasMemoFlag: false,
	}, nil
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
		members:     members,
	}, nil
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

func xmrOutputsMissingKeyImages(outputs []xmrFrostOutputEntry) []xmrFrostOutputEntry {
	pending := make([]xmrFrostOutputEntry, 0, len(outputs))
	for _, output := range outputs {
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
		release()
		queuedAfterRelease, activeAfterRelease := c.pendingXMRKeyImageBacklog()
		c.logger.Info().
			Str("vault_pub_key", vaultPubKey.String()).
			Str("tx_hash", txHash).
			Uint64("queued_after_release", queuedAfterRelease).
			Bool("active_after_release", activeAfterRelease).
			Msg("released XMR key-image generation slot")
	}()

	proofs := make([]*common.XmrKeyImageProof, 0, len(outputs))
	for _, output := range outputs {
		c.logger.Info().
			Str("vault_pub_key", vaultPubKey.String()).
			Str("tx_hash", txHash).
			Uint32("output_index", output.OutputIndex).
			Int64("requested_finalise_height", finaliseHeight).
			Strs("signer_pub_keys", members).
			Msg("starting XMR key-image generation")
		resp, err := c.tssServer.FrostKeyImage(tssp.FrostKeyImageRequest{
			VaultPubKey:   vaultPubKey.String(),
			TxHash:        txHash,
			OutputIndex:   output.OutputIndex,
			SignerPubKeys: members,
			BlockHeight:   finaliseHeight,
			Version:       version,
		}, newFrostSignerAdapter(c.frostSigner))
		if err != nil {
			return nil, fmt.Errorf("fail to execute XMR key-image ceremony for %s:%d: %w", txHash, output.OutputIndex, err)
		}
		if resp.Status != gotsscommon.Success {
			failReason := strings.TrimSpace(resp.Blame.FailReason)
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
	if len(proofs) == 0 {
		return nil
	}

	out := make([]*common.XmrKeyImageProof, 0, len(proofs))
	for _, proof := range proofs {
		if proof == nil {
			continue
		}
		cp := *proof
		cp.TxHash = strings.ToLower(strings.TrimSpace(cp.TxHash))
		cp.KeyImage = strings.ToLower(strings.TrimSpace(cp.KeyImage))
		cp.ProofC = strings.ToLower(strings.TrimSpace(cp.ProofC))
		cp.ProofS = strings.ToLower(strings.TrimSpace(cp.ProofS))
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TxHash != out[j].TxHash {
			return out[i].TxHash < out[j].TxHash
		}
		return out[i].OutputIndex < out[j].OutputIndex
	})
	return out
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
	if item == nil || item.ObservedVaultPubKey.IsEmpty() {
		return false
	}
	vaultAddr, err := item.ObservedVaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(item.To), vaultAddr.String())
}

func (c *Client) isObservedOutboundFromVault(item *stypes.TxInItem) bool {
	if item == nil || item.ObservedVaultPubKey.IsEmpty() {
		return false
	}
	vaultAddr, err := item.ObservedVaultPubKey.GetAddress(common.XMRChain)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(item.Sender), vaultAddr.String())
}

func (c *Client) isObservedTxInboundToVault(tx common.ObservedTx) bool {
	if tx.ObservedPubKey.IsEmpty() {
		return false
	}
	vaultAddr, err := tx.ObservedPubKey.GetAddress(common.XMRChain)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(tx.Tx.ToAddress.String()), vaultAddr.String())
}

func (c *Client) isObservedTxOutboundFromVault(tx common.ObservedTx) bool {
	if tx.ObservedPubKey.IsEmpty() {
		return false
	}
	vaultAddr, err := tx.ObservedPubKey.GetAddress(common.XMRChain)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(tx.Tx.FromAddress.String()), vaultAddr.String())
}

func (c *Client) validateObservedTxKeyImages(tx common.ObservedTx, inbound, trackedOutboundChange bool) error {
	if c.frostSigner == nil || !tx.Tx.Chain.Equals(common.XMRChain) || !tx.IsFinal() {
		return nil
	}
	if inbound {
		if !c.isObservedTxInboundToVault(tx) {
			return nil
		}
		if isOutboundObservationMemo(tx.Tx.Memo) && !c.isKnownVaultAddress(tx.Tx.FromAddress.String()) {
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
	return nil
}

func (c *Client) RollbackBlockScanner() error {
	return c.blockScanner.RollbackToLastObserved()
}

func (c *Client) ShouldReportSolvency(height int64) bool {
	if c.lastSolvencyCheckHeight == nil || c.scanner == nil {
		return false
	}
	if height <= c.lastSolvencyCheckHeight.Load() {
		return false
	}
	solvencyBlocks := c.cfg.SolvencyBlocks
	if solvencyBlocks <= 0 {
		solvencyBlocks = 10
	}
	return height%solvencyBlocks == 0 && c.scanner.lastFee.Load() > 0
}

func (c *Client) ReportSolvency(height int64) error {
	if !c.ShouldReportSolvency(height) {
		return nil
	}
	// when block scanner is not healthy, only report from auto-unhalt SolvencyCheckRunner
	if !c.IsBlockScannerHealthy() {
		if c.blockScanner != nil && height == c.blockScanner.PreviousHeight()+1 {
			return nil
		}
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

	if !c.IsBlockScannerHealthy() && checkedVaults > 0 && len(solventMsgs) == checkedVaults {
		msgs = solventMsgs
	}

	for i := range msgs {
		select {
		case c.globalSolvencyQueue <- msgs[i]:
		case <-time.After(constants.ThorchainBlockTime):
			c.logger.Info().Msg("fail to send monero solvency info to THORChain: timeout")
		}
	}
	c.lastSolvencyCheckHeight.Store(height)
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
