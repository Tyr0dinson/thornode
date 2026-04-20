package monero

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/syndtr/goleveldb/leveldb"

	"gitlab.com/thorchain/thornode/v3/common"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

const (
	xmrDefaultLeaderEpochWindowBlocks = int64(200)
	xmrKeyImageWorkStatePrefix        = "xmr-keyimage-work-v1-"
)

type xmrSyncState string

const (
	xmrSyncStateSyncingToTip xmrSyncState = "syncing_to_tip"
	xmrSyncStateAtTip        xmrSyncState = "at_tip"
)

type xmrNodeRole string

const (
	xmrNodeRoleUnknown              xmrNodeRole = "unknown"
	xmrNodeRoleActiveSelectedSigner xmrNodeRole = "active_selected_signer"
	xmrNodeRoleActiveSigner         xmrNodeRole = "active_signer"
	xmrNodeRoleActiveWatchOnly      xmrNodeRole = "active_watch_only"
	xmrNodeRoleStandbyVaultSigner   xmrNodeRole = "standby_vault_signer"
	xmrNodeRoleStandbyWatchOnly     xmrNodeRole = "standby_watch_only"
)

type xmrLocalCapability struct {
	NodeStatus    ttypes.NodeStatus
	Role          xmrNodeRole
	SyncState     xmrSyncState
	IsVaultSigner bool
	CanOriginate  bool
	CanAttest     bool
}

type xmrWorkKind string

const (
	xmrWorkKindReplayKeyImage      xmrWorkKind = "replay_key_image"
	xmrWorkKindReplayTxKey         xmrWorkKind = "replay_tx_key"
	xmrWorkKindFinalizeObservation xmrWorkKind = "finalize_observation"
)

type xmrFinalizePhase string

const (
	xmrFinalizePhaseNone     xmrFinalizePhase = ""
	xmrFinalizePhaseTxKey    xmrFinalizePhase = "txkey"
	xmrFinalizePhaseKeyImage xmrFinalizePhase = "keyimage"
)

type xmrWorkItem struct {
	ID                string           `json:"id"`
	Kind              xmrWorkKind      `json:"kind"`
	Phase             xmrFinalizePhase `json:"phase,omitempty"`
	VaultPubKey       string           `json:"vault_pub_key"`
	TxHash            string           `json:"tx_hash"`
	OutputIndex       *uint32          `json:"output_index,omitempty"`
	AnchorHeight      int64            `json:"anchor_height"`
	LeaderEpochHeight int64            `json:"leader_epoch_height,omitempty"`
	LastAttemptHeight int64            `json:"last_attempt_height,omitempty"`
	AttemptCount      int64            `json:"attempt_count,omitempty"`
	LastError         string           `json:"last_error,omitempty"`
	UpdatedAt         int64            `json:"updated_at,omitempty"`
}

type xmrVaultLocalStatus struct {
	VaultPubKey                string
	SyncState                  xmrSyncState
	Role                       xmrNodeRole
	RegisteredKeyImageCount    int
	RegisteredTxKeyCount       int
	PendingKeyImageReplayCount int
	PendingTxKeyReplayCount    int
	PendingKeyImageOutputCount int
	PendingKeyImageTxCount     int
	PendingFinalizePhaseCount  int
	ActiveWorkItem             *xmrWorkItem
}

type xmrKeyImageWorkStateStore struct {
	db *leveldb.DB
	mu sync.Mutex
}

func newXMRKeyImageWorkStateStore(db *leveldb.DB) (*xmrKeyImageWorkStateStore, error) {
	if db == nil {
		return nil, fmt.Errorf("xmr key-image work state db is nil")
	}
	return &xmrKeyImageWorkStateStore{db: db}, nil
}

func xmrKeyImageWorkID(vaultPubKey common.PubKey, txHash string, outputIndex uint32) string {
	return strings.Join([]string{
		strings.TrimSpace(vaultPubKey.String()),
		strings.ToLower(strings.TrimSpace(txHash)),
		fmt.Sprintf("%d", outputIndex),
	}, "|")
}

func xmrLeaderEpochHeight(height, window int64) int64 {
	if height <= 0 {
		return 1
	}
	if window <= 0 {
		window = xmrDefaultLeaderEpochWindowBlocks
	}
	return ((height-1)/window)*window + 1
}

func (s *xmrKeyImageWorkStateStore) get(workID string) (xmrWorkItem, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	payload, err := s.db.Get([]byte(xmrKeyImageWorkStatePrefix+workID), nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return xmrWorkItem{}, false, nil
		}
		return xmrWorkItem{}, false, err
	}

	var item xmrWorkItem
	if err := json.Unmarshal(payload, &item); err != nil {
		return xmrWorkItem{}, false, err
	}
	return item, true, nil
}

func (s *xmrKeyImageWorkStateStore) put(item xmrWorkItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	item.UpdatedAt = time.Now().Unix()
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return s.db.Put([]byte(xmrKeyImageWorkStatePrefix+item.ID), payload, nil)
}

func (s *xmrKeyImageWorkStateStore) delete(workID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Delete([]byte(xmrKeyImageWorkStatePrefix+workID), nil)
}

func (s *xmrKeyImageWorkStateStore) list() ([]xmrWorkItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	iter := s.db.NewIterator(nil, nil)
	defer iter.Release()

	items := make([]xmrWorkItem, 0)
	for iter.Next() {
		key := string(iter.Key())
		if !strings.HasPrefix(key, xmrKeyImageWorkStatePrefix) {
			continue
		}
		var item xmrWorkItem
		if err := json.Unmarshal(iter.Value(), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].AnchorHeight != items[j].AnchorHeight {
			return items[i].AnchorHeight < items[j].AnchorHeight
		}
		if items[i].TxHash != items[j].TxHash {
			return items[i].TxHash < items[j].TxHash
		}
		leftIndex, rightIndex := uint32(0), uint32(0)
		if items[i].OutputIndex != nil {
			leftIndex = *items[i].OutputIndex
		}
		if items[j].OutputIndex != nil {
			rightIndex = *items[j].OutputIndex
		}
		return leftIndex < rightIndex
	})
	return items, nil
}
