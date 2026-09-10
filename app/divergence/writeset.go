package divergence

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	abci "github.com/cometbft/cometbft/abci/types"

	storetypes "github.com/cosmos/cosmos-sdk/store/v2/types"
)

// StoreHash is one store's commit hash at a height.
type StoreHash struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

// StoreStat summarizes one store's net writes in a block.
type StoreStat struct {
	// Digest is sha256 over the store's net (key, delete, value) triples sorted
	// by key; see digestPairs.
	Digest  string `json:"digest"`
	Writes  int    `json:"writes"`
	Deletes int    `json:"deletes"`
}

// TxResult holds the ExecTxResult fields that feed LastResultsHash (Code,
// GasWanted, GasUsed, and Data as its hex sha256 in DataHash), plus Codespace,
// which is informational only: CometBFT does not hash it, so a Codespace-only
// difference does not change LastResultsHash.
type TxResult struct {
	Code      uint32 `json:"code"`
	Codespace string `json:"codespace,omitempty"`
	GasWanted int64  `json:"gas_wanted"`
	GasUsed   int64  `json:"gas_used"`
	DataHash  string `json:"data_hash,omitempty"`
}

// KVPair is one net write: the last value written to Key in Store this block.
type KVPair struct {
	Store  string `json:"store"`
	Key    string `json:"key"` // hex
	Value  []byte `json:"value,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}

// WriteSet is everything recorded for one committed height. It is the format
// of writesets/<height>.json.gz and of GET /thorchain/debug/writeset/{height}.
type WriteSet struct {
	Height int64 `json:"height"`
	// AppHash is the root hash of the commit info at Height: the AppHash the
	// header of block Height+1 must carry.
	AppHash    string               `json:"app_hash"`
	CommitInfo []StoreHash          `json:"commit_info"`
	Stores     map[string]StoreStat `json:"stores"`
	// ResultsHash is CometBFT's TxResultsHash of this block's tx results: the
	// LastResultsHash the header of block Height+1 must carry.
	ResultsHash string     `json:"results_hash"`
	TxResults   []TxResult `json:"tx_results"`
	Overlap     Overlap    `json:"overlap"`
	Pairs       []KVPair   `json:"pairs"`
}

// netPairs keeps the last write to each (store, key) and sorts the result by
// store then key, so the digest only depends on the block's net effect.
func netPairs(cs []*storetypes.StoreKVPair) []*storetypes.StoreKVPair {
	idx := make(map[string]int, len(cs))
	out := make([]*storetypes.StoreKVPair, 0, len(cs))
	for _, p := range cs {
		if p == nil {
			continue
		}
		k := p.StoreKey + "\x00" + string(p.Key)
		if i, ok := idx[k]; ok {
			out[i] = p
			continue
		}
		idx[k] = len(out)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StoreKey != out[j].StoreKey {
			return out[i].StoreKey < out[j].StoreKey
		}
		return bytes.Compare(out[i].Key, out[j].Key) < 0
	})
	return out
}

// digestPairs hashes sorted net pairs of one store as
// uvarint(len(key)) || key || delete(0|1) || uvarint(len(value)) || value.
func digestPairs(pairs []*storetypes.StoreKVPair) string {
	h := sha256.New()
	var buf [binary.MaxVarintLen64]byte
	for _, p := range pairs {
		n := binary.PutUvarint(buf[:], uint64(len(p.Key)))
		h.Write(buf[:n])
		h.Write(p.Key)
		if p.Delete {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
		n = binary.PutUvarint(buf[:], uint64(len(p.Value)))
		h.Write(buf[:n])
		h.Write(p.Value)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// storeStats computes per-store digests and counts over sorted net pairs.
func storeStats(pairs []*storetypes.StoreKVPair) map[string]StoreStat {
	stats := make(map[string]StoreStat)
	for start := 0; start < len(pairs); {
		end := start
		for end < len(pairs) && pairs[end].StoreKey == pairs[start].StoreKey {
			end++
		}
		st := StoreStat{Digest: digestPairs(pairs[start:end])}
		for _, p := range pairs[start:end] {
			if p.Delete {
				st.Deletes++
			} else {
				st.Writes++
			}
		}
		stats[pairs[start].StoreKey] = st
		start = end
	}
	return stats
}

func toKVPairs(pairs []*storetypes.StoreKVPair) []KVPair {
	out := make([]KVPair, len(pairs))
	for i, p := range pairs {
		out[i] = KVPair{Store: p.StoreKey, Key: hex.EncodeToString(p.Key), Delete: p.Delete}
		if !p.Delete {
			out[i].Value = p.Value
		}
	}
	return out
}

func toTxResults(res []*abci.ExecTxResult) []TxResult {
	out := make([]TxResult, 0, len(res))
	for _, r := range res {
		if r == nil {
			out = append(out, TxResult{})
			continue
		}
		tr := TxResult{Code: r.Code, Codespace: r.Codespace, GasWanted: r.GasWanted, GasUsed: r.GasUsed}
		if len(r.Data) > 0 {
			sum := sha256.Sum256(r.Data)
			tr.DataHash = hex.EncodeToString(sum[:])
		}
		out = append(out, tr)
	}
	return out
}

// commitInfoHashes flattens commit info into name-sorted store hashes.
func commitInfoHashes(ci *storetypes.CommitInfo) []StoreHash {
	if ci == nil {
		return nil
	}
	out := make([]StoreHash, 0, len(ci.StoreInfos))
	for _, si := range ci.StoreInfos {
		out = append(out, StoreHash{Name: si.Name, Hash: strings.ToUpper(hex.EncodeToString(si.CommitId.Hash))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// writeSetFileName is the ring file name for height h.
func writeSetFileName(h int64) string { return strconv.FormatInt(h, 10) + ".json.gz" }

// parseWriteSetFileName returns the height of a ring file name.
func parseWriteSetFileName(name string) (int64, bool) {
	s, ok := strings.CutSuffix(name, ".json.gz")
	if !ok {
		return 0, false
	}
	h, err := strconv.ParseInt(s, 10, 64)
	if err != nil || h < 0 {
		return 0, false
	}
	return h, true
}

// writeGzipJSON writes v as gzipped JSON to path through a temp file and a
// rename, so a crash mid-write never leaves a truncated file at path.
func writeGzipJSON(path string, v any) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	zw, err := gzip.NewWriterLevel(tmp, gzip.BestSpeed)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if err = json.NewEncoder(zw).Encode(v); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = zw.Close(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// ReadWriteSetFile reads a writesets/<height>.json.gz file.
func ReadWriteSetFile(path string) (*WriteSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return decodeWriteSet(f, true)
}

func decodeWriteSet(r io.Reader, gzipped bool) (*WriteSet, error) {
	if gzipped {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		r = zr
	}
	ws := &WriteSet{}
	if err := json.NewDecoder(r).Decode(ws); err != nil {
		return nil, fmt.Errorf("decode write set: %w", err)
	}
	return ws, nil
}

// listWriteSetHeights returns the heights of the ring files in dir, ascending.
func listWriteSetHeights(dir string) ([]int64, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hs []int64
	for _, e := range entries {
		if h, ok := parseWriteSetFileName(e.Name()); ok && !e.IsDir() {
			hs = append(hs, h)
		}
	}
	sort.Slice(hs, func(i, j int) bool { return hs[i] < hs[j] })
	return hs, nil
}
