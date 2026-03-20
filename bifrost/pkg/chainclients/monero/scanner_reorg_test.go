package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rs/zerolog/log"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/storage"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

func TestScannerFetchTxsDetectsReorgAndEmitsErrata(t *testing.T) {
	t.Parallel()

	blocks := map[int64]chainBlock{
		98:  withBlockHeader(98, defaultCoinbaseValue, "h98", "h97"),
		99:  withBlockHeader(99, defaultCoinbaseValue, "new99", "h98"),
		100: withBlockHeader(100, defaultCoinbaseValue, "new100", "new99"),
		101: withBlockHeader(101, defaultCoinbaseValue, "h101", "new100"),
	}
	txExists := map[string]bool{
		"tx-keep": true,
		"tx-drop": false,
	}

	frostServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/"):
			rawHeight := strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/")
			height, err := strconv.ParseInt(rawHeight, 10, 64)
			if err != nil {
				http.Error(w, "invalid height", http.StatusBadRequest)
				return
			}
			block, ok := blocks[height]
			if !ok {
				http.Error(w, "unknown height", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(xmrFrostChainBlockResponse{
				Height:       block.Height,
				Reward:       block.Reward,
				Hash:         block.Hash,
				PreviousHash: block.PreviousHash,
				TxHashes:     block.TxHashes,
			})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/chain/tx/"):
			path := strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/tx/")
			path = strings.TrimSuffix(path, "/exists")
			path, _ = url.PathUnescape(path)
			_ = json.NewEncoder(w).Encode(xmrFrostChainTxExistsResponse{Exists: txExists[path]})
		default:
			http.NotFound(w, r)
		}
	}))
	defer frostServer.Close()

	frostSigner, err := NewFrostSignerWithEndpoint(
		config.BifrostChainConfiguration{SignerStoreSecret: testSignerStoreSecret},
		frostServer.URL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}

	memStorage := storage.NewMemStorage()
	db, err := leveldb.Open(memStorage, nil)
	if err != nil {
		t.Fatalf("open in-memory leveldb: %v", err)
	}
	defer db.Close()

	reorgStore := NewReorgStore(db)
	for _, meta := range []*reorgBlockMeta{
		{
			Height:               98,
			BlockHash:            "h98",
			PreviousHash:         "h97",
			CustomerTransactions: nil,
		},
		{
			Height:               99,
			BlockHash:            "old99",
			PreviousHash:         "h98",
			CustomerTransactions: []string{"tx-keep"},
		},
		{
			Height:               100,
			BlockHash:            "old100",
			PreviousHash:         "old99",
			CustomerTransactions: []string{"tx-drop"},
		},
	} {
		if err := reorgStore.Set(meta); err != nil {
			t.Fatalf("set reorg meta: %v", err)
		}
	}

	scanner := NewScanner(log.Logger, frostSigner, nil, common.XMRChain, common.EmptyPubKey, 72)
	scanner.SetReorgStore(reorgStore)
	errataQueue := make(chan stypes.ErrataBlock, 2)
	scanner.SetErrataQueue(errataQueue)

	txIn, err := scanner.FetchTxs(101, 101)
	if err != nil {
		t.Fatalf("fetch txs: %v", err)
	}
	if len(txIn.TxArray) != 0 {
		t.Fatalf("expected no observed transfers, got %d", len(txIn.TxArray))
	}

	select {
	case errata := <-errataQueue:
		if errata.Height != 100 {
			t.Fatalf("expected errata height 100, got %d", errata.Height)
		}
		expected := []stypes.ErrataTx{{
			TxID:  common.TxID("tx-drop"),
			Chain: common.XMRChain,
		}}
		if !reflect.DeepEqual(errata.Txs, expected) {
			t.Fatalf("unexpected errata txs: %#v", errata.Txs)
		}
	default:
		t.Fatal("expected errata for dropped tx")
	}

	select {
	case extra := <-errataQueue:
		t.Fatalf("did not expect second errata block: %#v", extra)
	default:
	}

	meta100, err := reorgStore.Get(100)
	if err != nil {
		t.Fatalf("get meta 100: %v", err)
	}
	if meta100 == nil || meta100.BlockHash != "new100" || meta100.PreviousHash != "new99" {
		t.Fatalf("unexpected updated meta 100: %#v", meta100)
	}
	meta99, err := reorgStore.Get(99)
	if err != nil {
		t.Fatalf("get meta 99: %v", err)
	}
	if meta99 == nil || meta99.BlockHash != "new99" || meta99.PreviousHash != "h98" {
		t.Fatalf("unexpected updated meta 99: %#v", meta99)
	}
}

func withBlockHeader(height int64, reward uint64, hash, prevHash string) chainBlock {
	return chainBlock{
		Height:       height,
		Reward:       reward,
		Hash:         hash,
		PreviousHash: prevHash,
	}
}
