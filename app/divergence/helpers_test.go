package divergence

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/stretchr/testify/require"

	"cosmossdk.io/log/v2"
	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// fakeCommits is an in-memory CommitInfoSource.
type fakeCommits struct {
	mu    sync.Mutex
	infos map[int64]*storetypes.CommitInfo
}

func newFakeCommits() *fakeCommits { return &fakeCommits{infos: map[int64]*storetypes.CommitInfo{}} }

func (f *fakeCommits) GetCommitInfo(v int64) (*storetypes.CommitInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ci, ok := f.infos[v]
	if !ok {
		return nil, fmt.Errorf("no commit info found")
	}
	return ci, nil
}

// set records commit info at height h with one store hash per name, derived
// from the name, the height and salt.
func (f *fakeCommits) set(h int64, salt string, names ...string) *storetypes.CommitInfo {
	ci := &storetypes.CommitInfo{Version: h}
	for _, n := range names {
		sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%s", n, h, salt)))
		ci.StoreInfos = append(ci.StoreInfos, storetypes.StoreInfo{Name: n, CommitId: storetypes.CommitID{Version: h, Hash: sum[:]}})
	}
	f.mu.Lock()
	f.infos[h] = ci
	f.mu.Unlock()
	return ci
}

// recLogger records Error calls.
type recLogger struct {
	log.Logger
	mu   sync.Mutex
	msgs []string
	kvs  [][]any
}

func newRecLogger() *recLogger { return &recLogger{Logger: log.NewNopLogger()} }

func (l *recLogger) Error(msg string, keyvals ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, msg)
	l.kvs = append(l.kvs, keyvals)
}

func (l *recLogger) With(...any) log.Logger { return l }

func (l *recLogger) find(msg string) []any {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, m := range l.msgs {
		if m == msg {
			return l.kvs[i]
		}
	}
	return nil
}

func kv(keyvals []any, key string) any {
	for i := 0; i+1 < len(keyvals); i += 2 {
		if keyvals[i] == key {
			return keyvals[i+1]
		}
	}
	return nil
}

func sdkCtxAt(h int64) sdk.Context {
	return sdk.Context{}.WithBlockHeader(cmtproto.Header{Height: h})
}

// commitBlock drives the recorder through one block like BaseApp does.
func commitBlock(t *testing.T, r *Recorder, h int64, txResults []*abci.ExecTxResult, cs []*storetypes.StoreKVPair) {
	t.Helper()
	require.NoError(t, r.ListenFinalizeBlock(context.Background(), abci.RequestFinalizeBlock{Height: h}, abci.ResponseFinalizeBlock{TxResults: txResults, AppHash: []byte{byte(h)}}))
	require.NoError(t, r.ListenCommit(sdkCtxAt(h), abci.ResponseCommit{}, cs))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, r.Flush(ctx))
}

func kvp(store, key, value string) *storetypes.StoreKVPair {
	return &storetypes.StoreKVPair{StoreKey: store, Key: []byte(key), Value: []byte(value)}
}

func del(store, key string) *storetypes.StoreKVPair {
	return &storetypes.StoreKVPair{StoreKey: store, Key: []byte(key), Delete: true}
}

var testStores = []string{"acc", "bank", "thorchain"}
