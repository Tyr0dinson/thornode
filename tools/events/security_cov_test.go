package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/cosmos/cosmos-sdk/types"

	tssMessages "gitlab.com/thorchain/thornode/v3/bifrost/p2p/messages"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/notify"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
	"gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

func TestSecurityCovSecurityEvents(t *testing.T) {
	e := newInfoCovEnv(t)

	block := infoCovBlock(500, infoCovTx(nil,
		map[string]string{"type": "noise"},
		map[string]string{"type": types.SecurityEventType, "msg": "tx-level-issue"},
	))
	block.EndBlockEvents = []map[string]string{
		{"type": "noise"},
		{"type": types.SecurityEventType, "msg": "end-block-issue"},
	}
	block.FinalizeBlockEvents = []map[string]string{
		{"type": types.SecurityEventType, "msg": "finalize-issue"},
	}

	out := infoCovStdout(t, func() { SecurityEvents(block) })
	require.Equal(t, 3, strings.Count(out, "Security Event"))
	require.Equal(t, 3, strings.Count(out, "------------------------- Security -------------------------"))
	require.Contains(t, out, `"msg": "tx-level-issue"`)
	require.Contains(t, out, `"msg": "end-block-issue"`)
	require.Contains(t, out, `"msg": "finalize-issue"`)
	require.Contains(t, out, "Hash: INFOCOVHASH")
	require.Contains(t, out, "Links: https://runescan.io/tx/INFOCOVHASH")
	require.Equal(t, 3, strings.Count(e.logBuf.String(), "sending notifications"))
}

func TestSecurityCovErrataTransactions(t *testing.T) {
	newInfoCovEnv(t)
	block := infoCovBlock(501, infoCovTx(nil,
		map[string]string{"type": "noise"},
		map[string]string{"type": types.ErrataEventType, "tx_id": "ERRATATXID"},
	))
	out := infoCovStdout(t, func() { ErrataTransactions(block) })
	require.Equal(t, 1, strings.Count(out, "Errata Tx"))
	require.Contains(t, out, "Security")
	require.Contains(t, out, "/thorchain/tx/details/ERRATATXID")
}

func TestSecurityCovNetworkUsesTssV3AtHeight(t *testing.T) {
	e := newInfoCovEnv(t)
	e.set("thorchain/version?height=1", map[string]string{"current": "3.18.9"})
	e.set("thorchain/version?height=2", map[string]string{"current": "3.19.0"})
	e.set("thorchain/version?height=3", map[string]string{"current": "garbage"})

	v3, err := networkUsesTssV3AtHeight(1)
	require.NoError(t, err)
	require.False(t, v3)

	v3, err = networkUsesTssV3AtHeight(2)
	require.NoError(t, err)
	require.True(t, v3)

	_, err = networkUsesTssV3AtHeight(3)
	require.ErrorContains(t, err, "failed to parse network version")

	// no response registered -> request error
	_, err = networkUsesTssV3AtHeight(4)
	require.ErrorContains(t, err, "failed to get network version")
}

func TestSecurityCovKeysignFailureAlertDetails(t *testing.T) {
	cases := []struct {
		class keysignFailureAlertClass
		title string
		level notify.Level
	}{
		{keysignFailureAlertOperational, "TSS Keysign Failure (Round 7)", notify.Warning},
		{keysignFailureAlertTerminal, "Last Round Failure", notify.Warning},
		{keysignFailureAlertRejected, "Rejected TSS Failure Report", notify.Warning},
		{keysignFailureAlertIgnored, "", notify.Info},
	}
	for _, tc := range cases {
		title, level := keysignFailureAlertDetails(tc.class)
		require.Equal(t, tc.title, title)
		require.Equal(t, tc.level, level)
	}

	// pre-v3 round 7 is terminal, v3 round 7 operational, unknown rounds ignored
	require.Equal(t, keysignFailureAlertTerminal, classifyKeysignFailureRound(tssMessages.KEYSIGN7, false))
	require.Equal(t, keysignFailureAlertOperational, classifyKeysignFailureRound(tssMessages.KEYSIGN7, true))
	require.Equal(t, keysignFailureAlertIgnored, classifyKeysignFailureRound("SomeOtherRound", true))
}

func TestSecurityCovKeysignFailureWebhooksUsesGlobalConfig(t *testing.T) {
	newInfoCovEnv(t)
	cfg := config.Get()
	cfg.Console = false
	cfg.Notifications.Info = config.Webhooks{Category: "Info", Slack: "https://info.example"}
	cfg.Notifications.Security = config.Webhooks{Category: "Security", Slack: "https://sec.example"}
	config.Set(cfg)

	require.Equal(t, "Security", keysignFailureWebhooks(keysignFailureAlertTerminal).Category)
	require.Equal(t, "Info", keysignFailureWebhooks(keysignFailureAlertOperational).Category)
	require.Equal(t, "Info", keysignFailureWebhooks(keysignFailureAlertRejected).Category)

	// without Info webhooks, operational warnings fall back to Security
	cfg.Notifications.Info = config.Webhooks{Category: "Info"}
	config.Set(cfg)
	require.Equal(t, "Security", keysignFailureWebhooks(keysignFailureAlertOperational).Category)
	require.Equal(t, "Info", keysignFailureWebhooks(keysignFailureAlertRejected).Category)
}

func TestSecurityCovLastRoundFailuresDefaultDependencies(t *testing.T) {
	e := newInfoCovEnv(t)

	code := int64(0)
	msg := newKeysignFailureTestMsg("ABCDEF", tssMessages.KEYSIGN9, common.Coins{
		common.NewCoin(common.TOR, cosmos.NewUint(2*common.One)),
	})
	migrate := newKeysignFailureTestMsg("MIGRATEID", tssMessages.KEYSIGN9, keysignFailureTestCoins())
	migrate.Memo = "MIGRATE:100"
	block := newKeysignFailureTestBlock(&code, msg, migrate)

	out := infoCovStdout(t, func() { LastRoundFailures(block) })
	require.Equal(t, 1, strings.Count(out, "Last Round Failure"))
	require.Contains(t, out, "------------------------- Security -------------------------")
	require.Contains(t, out, "2.000000 THOR.TOR")
	require.Contains(t, out, "Memo: OUT:ABC123")

	// dedup persisted through real storage: a re-scan does not alert again
	seen := map[string]bool{}
	require.NoError(t, util.Load("keysignFailuresV2", &seen))
	require.True(t, seen["terminal|abcdef"])

	out = infoCovStdout(t, func() { LastRoundFailures(block) })
	require.NotContains(t, out, "Last Round Failure")
	require.Empty(t, e.requested(), "TOR valuation must not hit the API")
}

func TestSecurityCovScanSecurity(t *testing.T) {
	newInfoCovEnv(t)
	block := infoCovBlock(600, infoCovTx([]sdk.Msg{},
		map[string]string{"type": types.SecurityEventType},
		map[string]string{"type": types.ErrataEventType, "tx_id": "X"},
	))
	out := infoCovStdout(t, func() { ScanSecurity(block) })
	require.Contains(t, out, "Security Event")
	require.Contains(t, out, "Errata Tx")
}
