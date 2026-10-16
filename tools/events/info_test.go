package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
	"gitlab.com/thorchain/thornode/v3/tools/thorscan"
)

// testKeysignFailureSeq distinguishes consensus ids across generated test
// events — the scanner dedupes records by id within the window.
var testKeysignFailureSeq int

func keysignFailureEvent(vault, round string, blame ...string) map[string]string {
	testKeysignFailureSeq++
	return map[string]string{
		"type":   "tss_keysign_failure",
		"pubkey": vault,
		"round":  round,
		"reason": "tss timeout",
		"height": "42",
		"id":     fmt.Sprintf("%064d", testKeysignFailureSeq),
		"blame":  strings.Join(blame, ", "),
	}
}

func newKeysignFailureBlock(height int64, events ...map[string]string) *thorscan.BlockResponse {
	block := &thorscan.BlockResponse{
		BlockResponse: openapi.BlockResponse{
			Header: openapi.BlockResponseHeader{Height: height},
		},
	}
	if len(events) > 0 {
		block.Txs = []thorscan.BlockTx{
			{
				BlockTx: openapi.BlockTx{
					Result: openapi.BlockTxResult{Events: events},
				},
			},
		}
	}
	return block
}

func TestTssKeysignFailure(t *testing.T) {
	// setup isolated storage
	tmpDir, err := os.MkdirTemp("", "tss_test")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.Get()
	cfg.StoragePath = tmpDir
	cfg.Thresholds.TssKeysignFailure.WindowBlocks = 10
	cfg.Thresholds.TssKeysignFailure.Sigma = 2.0
	cfg.Thresholds.TssKeysignFailure.MinSnapshots = 3
	cfg.Console = true
	config.Set(cfg)

	// capture logs — notify logs "sending notifications" with the title
	var logBuf bytes.Buffer
	log.Logger = zerolog.New(&logBuf).With().Timestamp().Logger()

	vault := "tthorpub1vaultvaultvaultvaultvaultvaultvaultAAAA"
	blamed := "tthorpub1blameblameblameblameblameblameblameBBBB"

	// Scenario 1: basic failure below threshold — recorded with round and
	// blame granularity, no alert.
	// Seed 3 snapshots with 2 failures each: mean=2, stddev=0, threshold=2.
	history := TssKeysignFailureHistory{
		VaultPastCounts:   map[string][]int{vault: {2, 2, 2}},
		NetworkPastCounts: []int{2, 2, 2},
	}
	require.NoError(t, util.Store("tss_keysign_failure", history))

	TssKeysignFailure(newKeysignFailureBlock(100, keysignFailureEvent(vault, tssMessages.KEYSIGN5, blamed)))

	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Len(t, history.VaultFailures[vault], 1)
	require.Equal(t, tssMessages.KEYSIGN5, history.VaultFailures[vault][0].Round)
	require.Equal(t, []string{blamed}, history.VaultFailures[vault][0].Blame)
	require.NotContains(t, logBuf.String(), "sending notifications")
	logBuf.Reset()

	// Scenario 2: threshold crossing — 3 failures > threshold of 2 alerts once.
	TssKeysignFailure(newKeysignFailureBlock(101, keysignFailureEvent(vault, tssMessages.KEYSIGN5, blamed)))
	require.NotContains(t, logBuf.String(), "sending notifications")

	TssKeysignFailure(newKeysignFailureBlock(102, keysignFailureEvent(vault, tssMessages.KEYSIGN1aUnicast, blamed)))
	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Len(t, history.VaultFailures[vault], 3)
	require.Contains(t, logBuf.String(), "sending notifications", "expected alert when threshold crossed")
	require.Contains(t, logBuf.String(), "High TSS Keysign Failure Rate")
	logBuf.Reset()

	// Scenario 3: moving window pruning — window is 10 blocks, all failures
	// fall out by height 115.
	TssKeysignFailure(newKeysignFailureBlock(115))
	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Empty(t, history.VaultFailures[vault])
	logBuf.Reset()

	// Scenario 4: network fallback for a vault with no snapshot history —
	// network baseline of 1 failure/window is used as the threshold.
	history = TssKeysignFailureHistory{
		VaultPastCounts:   make(map[string][]int),
		NetworkPastCounts: []int{1, 1, 1},
	}
	require.NoError(t, util.Store("tss_keysign_failure", history))

	newVault := "tthorpub1newvaultnewvaultnewvaultnewvaultnewCCCC"
	TssKeysignFailure(newKeysignFailureBlock(200, keysignFailureEvent(newVault, tssMessages.EDDSAKEYSIGN1, blamed)))
	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Len(t, history.VaultFailures[newVault], 1)
	logBuf.Reset()

	TssKeysignFailure(newKeysignFailureBlock(201, keysignFailureEvent(newVault, tssMessages.EDDSAKEYSIGN1, blamed)))
	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Len(t, history.VaultFailures[newVault], 2)
	require.Contains(t, logBuf.String(), "sending notifications", "expected alert for new vault via network threshold")
	require.Empty(t, history.VaultPastCounts[newVault], "new vault should have no snapshots yet")
	logBuf.Reset()
}

// TestTssKeysignFailureNoImmediateTerminalAlert pins the division of labor
// with security.go: per-report terminal-round alerting is owned by
// LastRoundFailures, so the rate scanner records terminal rounds without
// alerting on its own until a statistical threshold is crossed.
func TestTssKeysignFailureNoImmediateTerminalAlert(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "tss_test_terminal")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.Get()
	cfg.StoragePath = tmpDir
	cfg.Thresholds.TssKeysignFailure.WindowBlocks = 10
	cfg.Thresholds.TssKeysignFailure.Sigma = 2.0
	cfg.Thresholds.TssKeysignFailure.MinSnapshots = 3
	cfg.Console = true
	config.Set(cfg)

	var logBuf bytes.Buffer
	log.Logger = zerolog.New(&logBuf).With().Timestamp().Logger()

	vault := "tthorpub1vaultvaultvaultvaultvaultvaultvaultAAAA"
	blamed := "tthorpub1blameblameblameblameblameblameblameBBBB"

	for _, round := range []string{
		tssMessages.KEYSIGN9,
		tssMessages.EDDSAKEYSIGN3,
		tssMessages.FROSTKEYSIGNSHARE,
		tssMessages.FROSTKEYSIGNRESULT,
	} {
		logBuf.Reset()
		TssKeysignFailure(newKeysignFailureBlock(300, keysignFailureEvent(vault, round, blamed)))
		require.NotContains(t, logBuf.String(), "sending notifications", "round %s must not alert without a rate threshold", round)
	}

	history := TssKeysignFailureHistory{}
	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Len(t, history.VaultFailures[vault], 4, "terminal rounds must still be recorded for rate statistics")
}

// TestTssKeysignFailureReplayIsIdempotent covers block re-scans after a crash:
// history is persisted before the scan height cursor, so the same block can be
// processed twice. The consensus id dedup must keep one record and one alert.
func TestTssKeysignFailureReplayIsIdempotent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "tss_test_replay")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.Get()
	cfg.StoragePath = tmpDir
	cfg.Thresholds.TssKeysignFailure.WindowBlocks = 10
	cfg.Thresholds.TssKeysignFailure.Sigma = 2.0
	cfg.Thresholds.TssKeysignFailure.MinSnapshots = 3
	cfg.Console = true
	config.Set(cfg)

	var logBuf bytes.Buffer
	log.Logger = zerolog.New(&logBuf).With().Timestamp().Logger()

	vault := "tthorpub1vaultvaultvaultvaultvaultvaultvaultAAAA"
	blamed := "tthorpub1blameblameblameblameblameblameblameBBBB"

	// zero-failure vault baseline: any single failure crosses the vault
	// threshold; network history is left below MinSnapshots so only the
	// vault alert can fire
	history := TssKeysignFailureHistory{
		VaultPastCounts: map[string][]int{vault: {0, 0, 0}},
	}
	require.NoError(t, util.Store("tss_keysign_failure", history))

	// the same block object is processed twice, as after a crash-replay
	block := newKeysignFailureBlock(400, keysignFailureEvent(vault, tssMessages.KEYSIGN5, blamed))
	TssKeysignFailure(block)
	TssKeysignFailure(block)

	require.NoError(t, util.Load("tss_keysign_failure", &history))
	require.Len(t, history.VaultFailures[vault], 1, "replayed failure must be recorded once")
	require.Equal(t, 1, strings.Count(logBuf.String(), "sending notifications"), "replayed failure must alert once")
}
