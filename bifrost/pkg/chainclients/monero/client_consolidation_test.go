package monero

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blang/semver"
	"github.com/rs/zerolog/log"
	"go.uber.org/atomic"

	"gitlab.com/thorchain/thornode/v3/bifrost/thorclient"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	tsscommon "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/common"
	tssp "gitlab.com/thorchain/thornode/v3/bifrost/tss/go-tss/tss"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

type consolidationBridge struct {
	thorclient.ThorchainBridge
	nodeStatus  ttypes.NodeStatus
	blockHeight int64
	asgards     ttypes.Vaults
	mimir       map[string]int64
	constants   map[string]int64
	mu          sync.Mutex
	mimirCalls  int
	asgardCalls int
	postedFees  int
}

func (b *consolidationBridge) FetchNodeStatus() (ttypes.NodeStatus, error) {
	return b.nodeStatus, nil
}

func (b *consolidationBridge) GetAsgards() (ttypes.Vaults, error) {
	b.mu.Lock()
	b.asgardCalls++
	b.mu.Unlock()
	return b.asgards, nil
}

func (b *consolidationBridge) GetBlockHeight() (int64, error) {
	return b.blockHeight, nil
}

func (b *consolidationBridge) GetMimir(key string) (int64, error) {
	b.mu.Lock()
	b.mimirCalls++
	b.mu.Unlock()
	if b.mimir == nil {
		return -1, nil
	}
	if value, ok := b.mimir[key]; ok {
		return value, nil
	}
	return -1, nil
}

func (b *consolidationBridge) mimirCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mimirCalls
}

func (b *consolidationBridge) GetConstants() (map[string]int64, error) {
	if b.constants == nil {
		return map[string]int64{
			constants.ChurnInterval.String(): 100,
		}, nil
	}
	return b.constants, nil
}

func (b *consolidationBridge) GetVault(pubkey string) (ttypes.Vault, error) {
	for _, vault := range b.asgards {
		if vault.PubKey.String() == pubkey || vault.PubKeyEddsa.String() == pubkey {
			return vault, nil
		}
	}
	return ttypes.Vault{}, errors.New("vault not found")
}

func (b *consolidationBridge) GetThorchainVersion() (semver.Version, error) {
	return semver.MustParse("1.134.0"), nil
}

func (b *consolidationBridge) GetNetworkFee(common.Chain) (uint64, uint64, error) {
	return 0, 0, nil
}

func (b *consolidationBridge) PostNetworkFee(int64, common.Chain, uint64, uint64) (common.TxID, error) {
	b.mu.Lock()
	b.postedFees++
	b.mu.Unlock()
	return common.BlankTxID, nil
}

func (b *consolidationBridge) postNetworkFeeCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.postedFees
}

type consolidationTSSResponse struct {
	resp tssp.FrostKeySignResponse
	err  error
}

type consolidationTSS struct {
	tssp.Server

	mu       sync.Mutex
	sequence []consolidationTSSResponse
	calls    int
	lastReq  tssp.FrostKeySignRequest
	reqs     []tssp.FrostKeySignRequest
}

func (m *consolidationTSS) FrostKeySign(req tssp.FrostKeySignRequest, _ tssp.FrostSidecarClient) (tssp.FrostKeySignResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	m.lastReq = req
	m.reqs = append(m.reqs, req)
	if len(m.sequence) == 0 {
		return tssp.FrostKeySignResponse{}, errors.New("no configured tss response")
	}
	idx := m.calls - 1
	if idx >= len(m.sequence) {
		idx = len(m.sequence) - 1
	}
	resp := m.sequence[idx].resp
	if m.sequence[idx].err != nil {
		return resp, m.sequence[idx].err
	}
	if resp.Status == tsscommon.Success {
		if strings.TrimSpace(resp.MsgID) == "" {
			msgID, err := tssp.FrostKeySignMsgID(req)
			if err != nil {
				return tssp.FrostKeySignResponse{}, err
			}
			resp.MsgID = msgID
		}
		if len(strings.TrimSpace(resp.TxHash)) != 64 {
			hashChar := string(rune('a' + ((m.calls - 1) % 26)))
			resp.TxHash = strings.Repeat(hashChar, 64)
		}
		if strings.TrimSpace(resp.TxHex) == "" {
			resp.TxHex = "deadbeef"
		}
		if resp.Eventuality.Version == 0 {
			planHash, err := tssp.FrostPlanHash(req, resp.MsgID)
			if err != nil {
				return tssp.FrostKeySignResponse{}, err
			}
			eventuality, err := tssp.BuildFrostEventuality(planHash, resp.TxHash, resp.TxHex, resp.FeeAtomic)
			if err != nil {
				return tssp.FrostKeySignResponse{}, err
			}
			resp.Eventuality = eventuality
		}
	}
	return resp, nil
}

func (m *consolidationTSS) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *consolidationTSS) request() tssp.FrostKeySignRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastReq
}

func (m *consolidationTSS) requests() []tssp.FrostKeySignRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]tssp.FrostKeySignRequest, len(m.reqs))
	copy(out, m.reqs)
	return out
}

type consolidationSidecar struct {
	mu sync.Mutex

	balanceByVault map[string]xmrFrostBalanceResponse
	outputsByVault map[string][]xmrFrostOutputEntry
	mempool        []xmrFrostTransferEntry
	feeRate        uint64
	onOutputs      func()

	broadcastCount int
	lastBroadcast  xmrFrostBroadcastRequest
}

func (s *consolidationSidecar) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/vaults/register":
		_ = json.NewEncoder(w).Encode(xmrFrostRegisterVaultResponse{Registered: true})
	case strings.HasPrefix(r.URL.Path, "/v1/monero/balance/"):
		vault := strings.TrimPrefix(r.URL.Path, "/v1/monero/balance/")
		s.mu.Lock()
		resp, ok := s.balanceByVault[vault]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(resp)
	case strings.HasPrefix(r.URL.Path, "/v1/monero/outputs/"):
		vault := strings.TrimPrefix(r.URL.Path, "/v1/monero/outputs/")
		s.mu.Lock()
		outputs := append([]xmrFrostOutputEntry(nil), s.outputsByVault[vault]...)
		onOutputs := s.onOutputs
		s.mu.Unlock()
		if onOutputs != nil {
			onOutputs()
		}
		_ = json.NewEncoder(w).Encode(xmrFrostOutputsResponse{
			Outputs: outputs,
		})
	case strings.HasPrefix(r.URL.Path, "/v1/monero/transfers"):
		s.mu.Lock()
		outgoing := append([]xmrFrostTransferEntry(nil), s.mempool...)
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(xmrFrostTransfersResponse{
			Outgoing: outgoing,
		})
	case r.URL.Path == "/v1/monero/fee-rate":
		s.mu.Lock()
		feeRate := s.feeRate
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(xmrFrostFeeRateResponse{
			FeeRatePerWeight: feeRate,
		})
	case r.URL.Path == "/v1/monero/broadcast":
		var req xmrFrostBroadcastRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.broadcastCount++
		s.lastBroadcast = req
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(xmrFrostBroadcastResponse{
			TxHash: "xmr-consolidation-hash",
		})
	default:
		http.NotFound(w, r)
	}
}

func (s *consolidationSidecar) broadcasts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.broadcastCount
}

func newConsolidationClient(
	t *testing.T,
	bridge *consolidationBridge,
	tssServer *consolidationTSS,
	sidecar *consolidationSidecar,
	localNodePubKey common.PubKey,
	vaultPubKey common.PubKey,
	vaultAddr common.Address,
) *Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(sidecar.handler))
	t.Cleanup(server.Close)

	frostSigner, err := NewFrostSignerWithEndpoint(config.BifrostChainConfiguration{}, server.URL)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	c := &Client{
		logger:                  log.Logger,
		cfg:                     config.BifrostChainConfiguration{ChainID: common.XMRChain, XMRConsolidationEnabled: true, XMRConsolidationCheckIntervalBlocks: 10, XMRConsolidationCooldownBlocks: 1, XMRConsolidationMinUnlockedOutputs: 2, XMRConsolidationTargetOutputs: 1, XMRConsolidationMinUnlockedAtomic: 1, XMRConsolidationReserveBps: 50, XMRConsolidationChurnLookaheadBlocks: 50, XMRConsolidationMaxRetries: 3},
		chain:                   common.XMRChain,
		thorchainBridge:         bridge,
		localNodePubKey:         localNodePubKey,
		frostSigner:             frostSigner,
		tssServer:               tssServer,
		signerCacheManager:      newTestSignerCacheManager(t),
		vaultAddressMap:         map[string]common.Address{vaultPubKey.String(): vaultAddr},
		scanner:                 &Scanner{currentBlockHeight: atomic.NewInt64(100), lastFee: atomic.NewUint64(10)},
		vaultLocks:              make(map[string]*sync.Mutex),
		consolidationState:      make(map[string]xmrConsolidationVaultState),
		consolidateInProgress:   atomic.NewBool(false),
		lastConsolidationHeight: atomic.NewInt64(0),
	}
	return c
}

func unlockedOutputs(height uint64, n int) []xmrFrostOutputEntry {
	out := make([]xmrFrostOutputEntry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, xmrFrostOutputEntry{
			TxHash:      strings.Repeat("a", 63) + string(rune('a'+i)),
			OutputIndex: uint32(i),
			Amount:      1_000_000_000_000,
			Height:      height,
			State:       "unspent",
		})
	}
	return out
}

func TestLoadConsolidationSettingsCachesMimirReads(t *testing.T) {
	t.Parallel()

	bridge := &consolidationBridge{
		mimir: map[string]int64{
			mimirXMRConsolidationEnabled:             1,
			mimirXMRConsolidationCheckIntervalBlocks: 12,
			mimirXMRConsolidationCooldownBlocks:      3,
			mimirXMRConsolidationMinUnlockedOutputs:  4,
			mimirXMRConsolidationTargetOutputs:       2,
			mimirXMRConsolidationReserveBps:          75,
			mimirXMRConsolidationChurnLookahead:      8,
			mimirXMRConsolidationMaxRetries:          5,
		},
	}
	client := &Client{
		cfg:             config.BifrostChainConfiguration{XMRConsolidationEnabled: true},
		thorchainBridge: bridge,
	}

	first := client.loadConsolidationSettings()
	second := client.loadConsolidationSettings()

	if !first.Enabled || !second.Enabled {
		t.Fatal("expected consolidation settings to remain enabled")
	}
	if bridge.mimirCallCount() != 8 {
		t.Fatalf("expected one mimir fetch round (8 calls), got %d", bridge.mimirCallCount())
	}

	client.consolidationSettingsMu.Lock()
	client.lastConsolidationConfig = time.Now().Add(-2 * constants.ThorchainBlockTime)
	client.consolidationSettingsMu.Unlock()

	_ = client.loadConsolidationSettings()
	if bridge.mimirCallCount() != 16 {
		t.Fatalf("expected cache refresh after ttl expiry, got %d mimir calls", bridge.mimirCallCount())
	}
}

func TestRunConsolidationPassBroadcastsSelfSend(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
		constants: map[string]int64{
			constants.ChurnInterval.String(): 100,
		},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				resp: tssp.FrostKeySignResponse{
					TxHash:    "xmr-sign-hash",
					TxHex:     "deadbeef",
					FeeAtomic: 1_000,
					MsgID:     "consolidation-msg-1",
					TxKeys:    testFrostTxKeys(),
					Status:    tsscommon.Success,
				},
			},
		},
	}
	sidecar := &consolidationSidecar{
		balanceByVault: map[string]xmrFrostBalanceResponse{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]xmrFrostOutputEntry{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sidecar, localNodePubKey, vaultPubKey, vaultAddr)
	observed := make(chan stypes.TxIn, 1)
	client.globalTxsQueue = observed

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 1 {
		t.Fatalf("expected one FrostKeySign call, got %d", tssServer.callCount())
	}
	if sidecar.broadcasts() != 1 {
		t.Fatalf("expected one broadcast, got %d", sidecar.broadcasts())
	}
	req := tssServer.request()
	if req.ToAddress != vaultAddr.String() {
		t.Fatalf("expected self-send address %s, got %s", vaultAddr, req.ToAddress)
	}
	if req.Memo != "consolidate" {
		t.Fatalf("expected consolidate memo, got %s", req.Memo)
	}
	if strings.TrimSpace(req.RequestID) == "" {
		t.Fatal("expected non-empty keysign request_id")
	}
	if req.AmountAtomic == 0 {
		t.Fatal("expected non-zero consolidation amount")
	}
	txIn := <-observed
	if !txIn.AllowFutureObservation {
		t.Fatal("expected consolidate observation to allow future re-observation")
	}
	if txIn.ConfirmationRequired != 1 {
		t.Fatalf("expected one confirmation for consolidate observation, got %d", txIn.ConfirmationRequired)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("expected one consolidate observation, got %d", len(txIn.TxArray))
	}
	item := txIn.TxArray[0]
	if item.Memo != "consolidate" {
		t.Fatalf("expected consolidate observation memo, got %s", item.Memo)
	}
	if item.Sender != vaultAddr.String() || item.To != vaultAddr.String() {
		t.Fatalf("expected consolidate self-send %s -> %s, got %s -> %s", vaultAddr, vaultAddr, item.Sender, item.To)
	}
	if len(item.TxKeys) == 0 {
		t.Fatal("expected consolidate observation to carry tx keys")
	}
}

func TestEnsureConsolidationStateInitializesZeroValueClient(t *testing.T) {
	t.Parallel()

	client := &Client{}
	client.ensureConsolidationState()
	if client.consolidateInProgress == nil {
		t.Fatal("expected consolidateInProgress to be initialized")
	}
	if client.lastConsolidationHeight == nil {
		t.Fatal("expected lastConsolidationHeight to be initialized")
	}
	if client.lastSolvencyCheckHeight == nil {
		t.Fatal("expected lastSolvencyCheckHeight to be initialized")
	}
	if client.vaultLocks == nil {
		t.Fatal("expected vaultLocks to be initialized")
	}
	if client.consolidationState == nil {
		t.Fatal("expected consolidationState to be initialized")
	}
	lock1 := client.GetVaultLock("")
	lock2 := client.GetVaultLock("")
	if lock1 != lock2 {
		t.Fatal("expected empty vault lock lookups to share the same mutex")
	}
}

func TestRunConsolidationPassUsesSpendableBalanceForAmount(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
		constants: map[string]int64{
			constants.ChurnInterval.String(): 100,
		},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				resp: tssp.FrostKeySignResponse{
					TxHash:    "xmr-sign-hash",
					TxHex:     "deadbeef",
					FeeAtomic: 1_000,
					MsgID:     "consolidation-msg-spendable",
					TxKeys:    testFrostTxKeys(),
					Status:    tsscommon.Success,
				},
			},
		},
	}
	spendableAtomic := uint64(3_000_000_000_000)
	sidecar := &consolidationSidecar{
		balanceByVault: map[string]xmrFrostBalanceResponse{
			vaultPubKey.String(): {
				Balance:          5_000_000_000_000,
				UnlockedBalance:  5_000_000_000_000,
				SpendableBalance: uint64Ptr(spendableAtomic),
			},
		},
		outputsByVault: map[string][]xmrFrostOutputEntry{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sidecar, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 1 {
		t.Fatalf("expected one FrostKeySign call, got %d", tssServer.callCount())
	}

	req := tssServer.request()
	expectedReserve := initialConsolidationReserve(spendableAtomic, client.loadConsolidationSettings().ReserveBps)
	expectedAmount := spendableAtomic - expectedReserve
	if req.AmountAtomic != expectedAmount {
		t.Fatalf("expected AmountAtomic=%d from spendable balance, got %d", expectedAmount, req.AmountAtomic)
	}
}

func TestRunConsolidationPassConsolidatesSmallBalanceWhenOutputCountIsHigh(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{resp: tssp.FrostKeySignResponse{Status: tsscommon.Success, TxHash: "x", TxHex: "deadbeef", MsgID: "consolidation-msg-threshold", TxKeys: testFrostTxKeys()}},
		},
	}
	sidecar := &consolidationSidecar{
		balanceByVault: map[string]xmrFrostBalanceResponse{
			vaultPubKey.String(): {
				Balance:          3_000_000_000,
				UnlockedBalance:  3_000_000_000,
				SpendableBalance: uint64Ptr(3_000_000_000),
			},
		},
		outputsByVault: map[string][]xmrFrostOutputEntry{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sidecar, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 1 {
		t.Fatalf("expected FrostKeySign call for high output count despite small balance, got %d", tssServer.callCount())
	}
	if sidecar.broadcasts() != 1 {
		t.Fatalf("expected broadcast for high output count despite small balance, got %d", sidecar.broadcasts())
	}
}

func TestRunConsolidationPassSkipsWhenMempoolOutgoingExists(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{resp: tssp.FrostKeySignResponse{Status: tsscommon.Success, TxHash: "x", TxHex: "deadbeef", MsgID: "consolidation-msg-skip", TxKeys: testFrostTxKeys()}},
		},
	}
	sidecar := &consolidationSidecar{
		balanceByVault: map[string]xmrFrostBalanceResponse{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]xmrFrostOutputEntry{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		mempool: []xmrFrostTransferEntry{
			{VaultPubKey: vaultPubKey.String(), FromAddress: vaultAddr.String()},
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sidecar, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 0 {
		t.Fatalf("expected no FrostKeySign call, got %d", tssServer.callCount())
	}
	if sidecar.broadcasts() != 0 {
		t.Fatalf("expected no broadcast, got %d", sidecar.broadcasts())
	}
}

func TestRunConsolidationPassRetiringVaultPreChurnTrigger(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		Status:      ttypes.VaultStatus_RetiringVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 21, // non-periodic height, retiring vaults should not pre-churn consolidate
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				resp: tssp.FrostKeySignResponse{
					TxHash:    "xmr-sign-hash",
					TxHex:     "deadbeef",
					FeeAtomic: 1_000,
					MsgID:     "consolidation-msg-retiring",
					TxKeys:    testFrostTxKeys(),
					Status:    tsscommon.Success,
				},
			},
		},
	}
	sidecar := &consolidationSidecar{
		balanceByVault: map[string]xmrFrostBalanceResponse{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]xmrFrostOutputEntry{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sidecar, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 0 {
		t.Fatalf("expected no FrostKeySign call for retiring pre-churn trigger, got %d", tssServer.callCount())
	}
}

func TestRunConsolidationPassSkipsWhenAnyLocalXMRVaultHasPendingTx(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	activeVaultPubKey, activeVaultAddr := testVaultPubKeyAndAddress(t)
	retiringVaultPubKey, _ := testVaultPubKeyAndAddress(t)

	activeVault := ttypes.Vault{
		PubKey:      activeVaultPubKey,
		PubKeyEddsa: activeVaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}
	retiringVault := ttypes.Vault{
		PubKey:                retiringVaultPubKey,
		PubKeyEddsa:           retiringVaultPubKey,
		Status:                ttypes.VaultStatus_RetiringVault,
		StatusSince:           1,
		Membership:            []string{localNodePubKey.String()},
		Chains:                []string{common.XMRChain.String()},
		PendingTxBlockHeights: []int64{12345},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{activeVault, retiringVault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{resp: tssp.FrostKeySignResponse{Status: tsscommon.Success, TxHash: "x", TxHex: "deadbeef", MsgID: "consolidation-msg-pending", TxKeys: testFrostTxKeys()}},
		},
	}
	sidecar := &consolidationSidecar{
		balanceByVault: map[string]xmrFrostBalanceResponse{
			activeVaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]xmrFrostOutputEntry{
			activeVaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sidecar, localNodePubKey, activeVaultPubKey, activeVaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 0 {
		t.Fatalf("expected no FrostKeySign call when any local XMR vault has a pending tx, got %d", tssServer.callCount())
	}
	if sidecar.broadcasts() != 0 {
		t.Fatalf("expected no broadcast when any local XMR vault has a pending tx, got %d", sidecar.broadcasts())
	}
}

func TestRunConsolidationPassSkipsWhenNodeInactive(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Standby,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{resp: tssp.FrostKeySignResponse{Status: tsscommon.Success, TxHash: "x", TxHex: "deadbeef", MsgID: "consolidation-msg-inactive", TxKeys: testFrostTxKeys()}},
		},
	}
	sidecar := &consolidationSidecar{
		balanceByVault: map[string]xmrFrostBalanceResponse{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]xmrFrostOutputEntry{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sidecar, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 0 {
		t.Fatalf("expected no FrostKeySign call, got %d", tssServer.callCount())
	}
}

func TestRunConsolidationPassRetriesOnInsufficientFunds(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				err: errors.New("insufficient funds while building tx"),
			},
			{
				resp: tssp.FrostKeySignResponse{
					TxHash:    "xmr-sign-hash",
					TxHex:     "deadbeef",
					FeeAtomic: 1_000,
					MsgID:     "consolidation-msg-retry",
					TxKeys:    testFrostTxKeys(),
					Status:    tsscommon.Success,
				},
			},
		},
	}
	sidecar := &consolidationSidecar{
		balanceByVault: map[string]xmrFrostBalanceResponse{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]xmrFrostOutputEntry{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sidecar, localNodePubKey, vaultPubKey, vaultAddr)

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 2 {
		t.Fatalf("expected two FrostKeySign calls, got %d", tssServer.callCount())
	}
	if sidecar.broadcasts() != 1 {
		t.Fatalf("expected one broadcast after retry, got %d", sidecar.broadcasts())
	}
}

func TestRunConsolidationPassResizesAmountFromNecessaryFee(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				err: errors.New("sign prepare failed: insufficient funds to cover amount (149250000000) + necessary fee: exhausted candidates (15 selected, max 15, total 150000000000): not enough funds (inputs 150000000000, outputs 149250000000, necessary_fee Some(892480000))"),
			},
			{
				resp: tssp.FrostKeySignResponse{
					TxHash:    "xmr-sign-hash",
					TxHex:     "deadbeef",
					FeeAtomic: 892_480_000,
					MsgID:     "consolidation-msg-necessary-fee",
					TxKeys:    testFrostTxKeys(),
					Status:    tsscommon.Success,
				},
			},
		},
	}
	spendableAtomic := uint64(150_000_000_000)
	sidecar := &consolidationSidecar{
		balanceByVault: map[string]xmrFrostBalanceResponse{
			vaultPubKey.String(): {
				Balance:          spendableAtomic,
				UnlockedBalance:  spendableAtomic,
				SpendableBalance: uint64Ptr(spendableAtomic),
			},
		},
		outputsByVault: map[string][]xmrFrostOutputEntry{
			vaultPubKey.String(): unlockedOutputs(50, 22),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sidecar, localNodePubKey, vaultPubKey, vaultAddr)
	client.cfg.XMRConsolidationMaxRetries = 2

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 2 {
		t.Fatalf("expected two FrostKeySign calls, got %d", tssServer.callCount())
	}
	if sidecar.broadcasts() != 1 {
		t.Fatalf("expected one broadcast after retry, got %d", sidecar.broadcasts())
	}
	if bridge.postNetworkFeeCount() == 0 {
		t.Fatal("expected fee observation from consolidation failure to post a network fee")
	}

	reqs := tssServer.requests()
	if len(reqs) != 2 {
		t.Fatalf("expected 2 recorded requests, got %d", len(reqs))
	}
	initialReserve := initialConsolidationReserve(spendableAtomic, client.loadConsolidationSettings().ReserveBps)
	if reqs[0].AmountAtomic != spendableAtomic-initialReserve {
		t.Fatalf("expected first attempt amount=%d, got %d", spendableAtomic-initialReserve, reqs[0].AmountAtomic)
	}

	expectedReserve := consolidationReserveForNecessaryFee(initialReserve, 892_480_000, client.loadConsolidationSettings().ReserveBps)
	expectedAmount := spendableAtomic - expectedReserve
	if reqs[1].AmountAtomic != expectedAmount {
		t.Fatalf("expected second attempt amount=%d after necessary_fee resize, got %d", expectedAmount, reqs[1].AmountAtomic)
	}
}

func TestRunConsolidationPassHoldsVaultLockDuringPrechecks(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{
				resp: tssp.FrostKeySignResponse{
					TxHash:    "xmr-sign-hash",
					TxHex:     "deadbeef",
					FeeAtomic: 1_000,
					MsgID:     "consolidation-msg-locking",
					TxKeys:    testFrostTxKeys(),
					Status:    tsscommon.Success,
				},
			},
		},
	}
	lockObservedUnlocked := atomic.NewBool(false)
	sidecar := &consolidationSidecar{
		balanceByVault: map[string]xmrFrostBalanceResponse{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]xmrFrostOutputEntry{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sidecar, localNodePubKey, vaultPubKey, vaultAddr)
	sidecar.onOutputs = func() {
		lock := client.GetVaultLock(vaultPubKey.String())
		if lock.TryLock() {
			lockObservedUnlocked.Store(true)
			lock.Unlock()
		}
	}

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if lockObservedUnlocked.Load() {
		t.Fatalf("expected consolidation prechecks to run while the vault lock is held")
	}
}

func TestRunConsolidationPassBacksOffAcrossPasses(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{err: errors.New("insufficient funds while building tx")},
		},
	}
	sidecar := &consolidationSidecar{
		balanceByVault: map[string]xmrFrostBalanceResponse{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]xmrFrostOutputEntry{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sidecar, localNodePubKey, vaultPubKey, vaultAddr)
	client.cfg.XMRConsolidationCheckIntervalBlocks = 1
	client.cfg.XMRConsolidationCooldownBlocks = 2
	client.cfg.XMRConsolidationMaxRetries = 1

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("first runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 1 {
		t.Fatalf("expected first pass to attempt signing once, got %d", tssServer.callCount())
	}

	bridge.blockHeight = 22
	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("second runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 2 {
		t.Fatalf("expected second pass to retry after base cooldown, got %d calls", tssServer.callCount())
	}

	bridge.blockHeight = 24
	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("third runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 2 {
		t.Fatalf("expected third pass to back off due to scaled cooldown, got %d calls", tssServer.callCount())
	}
}

func TestRunConsolidationPassStopsOnNonRetryableError(t *testing.T) {
	t.Parallel()

	localNodePubKey, _ := testVaultPubKeyAndAddress(t)
	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vault := ttypes.Vault{
		PubKey:      vaultPubKey,
		PubKeyEddsa: vaultPubKey,
		Status:      ttypes.VaultStatus_ActiveVault,
		StatusSince: 1,
		Membership:  []string{localNodePubKey.String()},
		Chains:      []string{common.XMRChain.String()},
	}

	bridge := &consolidationBridge{
		nodeStatus:  ttypes.NodeStatus_Active,
		blockHeight: 20,
		asgards:     ttypes.Vaults{vault},
	}
	tssServer := &consolidationTSS{
		sequence: []consolidationTSSResponse{
			{err: errors.New("invalid address supplied to keysign request")},
			{resp: tssp.FrostKeySignResponse{Status: tsscommon.Success, TxHash: "x", TxHex: "deadbeef", MsgID: "should-not-run", TxKeys: testFrostTxKeys()}},
		},
	}
	sidecar := &consolidationSidecar{
		balanceByVault: map[string]xmrFrostBalanceResponse{
			vaultPubKey.String(): {Balance: 5_000_000_000_000, UnlockedBalance: 5_000_000_000_000},
		},
		outputsByVault: map[string][]xmrFrostOutputEntry{
			vaultPubKey.String(): unlockedOutputs(50, 4),
		},
		feeRate: 10,
	}
	client := newConsolidationClient(t, bridge, tssServer, sidecar, localNodePubKey, vaultPubKey, vaultAddr)
	client.cfg.XMRConsolidationCheckIntervalBlocks = 1
	client.cfg.XMRConsolidationMaxRetries = 3

	if err := client.runConsolidationPass(); err != nil {
		t.Fatalf("runConsolidationPass failed: %v", err)
	}
	if tssServer.callCount() != 1 {
		t.Fatalf("expected non-retryable failure to stop after one attempt, got %d", tssServer.callCount())
	}
	if sidecar.broadcasts() != 0 {
		t.Fatalf("expected no broadcast on non-retryable signing failure, got %d", sidecar.broadcasts())
	}
}

func TestBumpConsolidationReserveSaturatesOnOverflow(t *testing.T) {
	t.Parallel()

	currentReserve := ^uint64(0) - defaultXMRConsolidationMinReserveAtomic + 1
	nextReserve := bumpConsolidationReserve(currentReserve, defaultXMRConsolidationReserveBps)
	if nextReserve != ^uint64(0) {
		t.Fatalf("expected reserve to saturate at MaxUint64, got %d", nextReserve)
	}
}

func TestInitialConsolidationReserveHandlesMultiplicationOverflow(t *testing.T) {
	t.Parallel()

	availableAtomic := ^uint64(0)
	reserveBps := defaultXMRConsolidationReserveBps

	expected := new(big.Int).SetUint64(availableAtomic)
	expected.Mul(expected, big.NewInt(reserveBps))
	expected.Quo(expected, big.NewInt(10_000))
	if !expected.IsUint64() {
		t.Fatal("expected scaled reserve to fit in uint64 after division")
	}

	got := initialConsolidationReserve(availableAtomic, reserveBps)
	if got != expected.Uint64() {
		t.Fatalf("expected reserve %d, got %d", expected.Uint64(), got)
	}
}

func TestBumpConsolidationReserveHandlesScaledStepOverflow(t *testing.T) {
	t.Parallel()

	currentReserve := uint64(9_500_000_000_000_000_000)
	nextReserve := bumpConsolidationReserve(currentReserve, 10_000)
	if nextReserve != ^uint64(0) {
		t.Fatalf("expected scaled reserve bump to saturate at MaxUint64, got %d", nextReserve)
	}
}

func TestScaledConsolidationCooldownCapsOnOverflow(t *testing.T) {
	t.Parallel()

	cooldown := scaledConsolidationCooldown(math.MaxInt64/2+1, 2)
	if cooldown != math.MaxInt64 {
		t.Fatalf("expected cooldown to saturate at MaxInt64, got %d", cooldown)
	}
}
