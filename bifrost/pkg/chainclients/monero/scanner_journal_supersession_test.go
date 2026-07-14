package monero

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/monero/sidecar"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
)

// reorgJournalMatchesCanonical is the predicate that gates the journal
// supersession branch in processReorg: a delivered/replayed journal whose
// replayed metas no longer match canonical must return false, which drives
// DeleteJournal -> rediscover. The reviewer suspected a metadata-loss bug here.
// There is none: DeleteJournal drops only journal state, and buildReorgJournal
// re-derives divergence from the still-present block metas. This test pins both
// halves — the predicate detects a second reorg, and it accepts an unchanged
// chain — so a future refactor cannot silently turn the self-healing path into
// a stale-journal resume.
func TestReorgJournalMatchesCanonicalDetectsSecondReorg(t *testing.T) {
	t.Parallel()

	anchorHash := testXMRHash("sup-anchor-98")
	replayed99 := testXMRHash("sup-99")
	replayed100 := testXMRHash("sup-100")
	next101 := testXMRHash("sup-101")
	// The second reorg: height 100's canonical hash changes out from under the
	// already-delivered journal.
	rewritten100 := testXMRHash("sup-100-rewritten")

	blockHashAt := map[int64]string{
		99:  replayed99,
		100: replayed100,
		101: next101,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/") {
			http.NotFound(w, r)
			return
		}
		height, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/"), 10, 64)
		if err != nil {
			http.Error(w, "bad height", http.StatusBadRequest)
			return
		}
		hash := blockHashAt[height]
		prev := blockHashAt[height-1]
		if height == 99 {
			prev = anchorHash
		}
		_ = json.NewEncoder(w).Encode(sidecar.ChainBlockResp(withBlockHeader(height, defaultCoinbaseValue, hash, prev)))
	}))
	defer server.Close()

	client, err := sidecar.NewClientWithEndpoint(
		config.BifrostChainConfiguration{SignerExpectedNetwork: "mocknet"},
		server.URL,
	)
	if err != nil {
		t.Fatalf("new sidecar client: %v", err)
	}

	scanner := NewScanner(log.Logger, client, nil, common.XMRChain, common.EmptyPubKey, 72)

	// A journal that has already replayed heights 99 and 100 off anchor 98.
	journal := &reorgJournal{
		Version:      2,
		Phase:        reorgPhaseDelivered,
		AnchorHeight: 98,
		AnchorHash:   anchorHash,
		Heights:      []int64{99, 100},
		ReplayedMetas: []*reorgBlockMeta{
			{Height: 99, BlockHash: replayed99, PreviousHash: anchorHash},
			{Height: 100, BlockHash: replayed100, PreviousHash: replayed99},
		},
	}
	nextBlock := withBlockHeader(101, defaultCoinbaseValue, next101, replayed100)

	// Canonical is unchanged: the journal still matches, so processReorg would
	// resume delivery rather than re-discover.
	matches, err := scanner.reorgJournalMatchesCanonical(journal, nextBlock)
	if err != nil {
		t.Fatalf("match check: %v", err)
	}
	if !matches {
		t.Fatal("an unchanged canonical chain must still match the delivered journal")
	}

	// A second reorg rewrites height 100. The predicate must now report a
	// mismatch, which is what triggers supersession (DeleteJournal + rediscover).
	blockHashAt[100] = rewritten100
	matches, err = scanner.reorgJournalMatchesCanonical(journal, nextBlock)
	if err != nil {
		t.Fatalf("match check after second reorg: %v", err)
	}
	if matches {
		t.Fatal("a journal whose replayed height was reorged again must not match; " +
			"resuming its stale delivery would publish superseded metadata")
	}
}
