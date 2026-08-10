package core

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	openapi "gitlab.com/thorchain/thornode/v3/openapi/gen"
)

func TestMempoolSimulationEffectiveMimirValue(t *testing.T) {
	t.Parallel()

	wantDefaults := map[string]int64{
		"DollarsPerRune":           0,
		"EnableAdvSwapQueue":       1,
		"MaxOutboundAttempts":      0,
		"MempoolSwapCapTOR":        0,
		"MempoolSwapPoolCapBPS":    50,
		"SigningTransactionPeriod": 300,
	}
	if !reflect.DeepEqual(mempoolSimulationUnsetMimirDefaults, wantDefaults) {
		t.Fatalf("unexpected fresh-cluster Mimir defaults: got %v, want %v", mempoolSimulationUnsetMimirDefaults, wantDefaults)
	}
	for key, want := range wantDefaults {
		got, err := mempoolSimulationEffectiveMimirValue(key, -1)
		if err != nil || got != want {
			t.Fatalf("%s: got value %d error %v, want default %d", key, got, err, want)
		}
	}
	if got, err := mempoolSimulationEffectiveMimirValue("MempoolSwapCapTOR", 7); err != nil || got != 7 {
		t.Fatalf("custom Mimir must be preserved: got value %d error %v", got, err)
	}

	if _, err := mempoolSimulationEffectiveMimirValue("UnknownMimir", -1); err == nil {
		t.Fatal("unset unknown Mimir must not be normalized")
	}
	if _, err := mempoolSimulationEffectiveMimirValue("DollarsPerRune", -2); err == nil {
		t.Fatal("unexpected negative Mimir value must remain invalid")
	}
}

func TestMempoolQueueNotFound(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		errors.New(`HTTP: 400 => {"message":"swap with tx_id ABC not found in any queue"}`),
		errors.New("swap with tx_id ABC NOT FOUND IN ANY QUEUE"),
	} {
		if !mempoolQueueNotFound(err) {
			t.Fatalf("expected %q to be treated as absent", err)
		}
	}
	for _, err := range []error{
		errors.New("HTTP: 400 => malformed tx id"),
		errors.New("HTTP: 404 => route not found"),
		errors.New("HTTP: 500"),
	} {
		if mempoolQueueNotFound(err) {
			t.Fatalf("unrelated query failure %q must not be treated as an absent queue item", err)
		}
	}
}

func TestMempoolSwapQuoteNeedsRicherUser(t *testing.T) {
	t.Parallel()

	quoteErr := errors.New("recommended minimum swap amount exceeds spendable balance")
	spendable := cosmos.NewUint(100)
	tests := []struct {
		name         string
		quotedAmount cosmos.Uint
		err          error
		want         bool
	}{
		{name: "recommended minimum above balance", quotedAmount: cosmos.NewUint(101), err: quoteErr, want: true},
		{name: "amount equal to balance", quotedAmount: spendable, err: quoteErr},
		{name: "ordinary quote failure", quotedAmount: cosmos.NewUint(99), err: errors.New("quote unavailable")},
		{name: "successful quote", quotedAmount: cosmos.NewUint(101)},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := mempoolSwapQuoteNeedsRicherUser(test.quotedAmount, spendable, test.err); got != test.want {
				t.Fatalf("got %t, want %t", got, test.want)
			}
		})
	}
}

func TestMempoolAffiliateTHORName(t *testing.T) {
	t.Parallel()

	first := mempoolAffiliateTHORName("thor1owner")
	if first != mempoolAffiliateTHORName(" THOR1OWNER ") {
		t.Fatal("affiliate THORName derivation must normalize owner casing and whitespace")
	}
	if first == mempoolAffiliateTHORName("thor1different") {
		t.Fatal("different owners must not derive the same test THORName")
	}
	if !strings.HasPrefix(first, "mpsim") || len(first) != 15 {
		t.Fatalf("unexpected derived affiliate THORName %q", first)
	}
}

func TestParseMempoolAffiliateCollectorBalance(t *testing.T) {
	t.Parallel()

	amount, err := parseMempoolAffiliateCollectorBalance("123")
	if err != nil || !amount.Equal(cosmos.NewUint(123)) {
		t.Fatalf("got amount %s error %v, want 123", amount, err)
	}
	for _, raw := range []string{"", " ", "not-rune"} {
		if _, err = parseMempoolAffiliateCollectorBalance(raw); err == nil {
			t.Fatalf("invalid collector balance %q must fail", raw)
		}
	}
}

func TestMempoolTimeoutRecoveryHeight(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		stagedHeight  int64
		signingPeriod int64
		attempts      int64
		want          int64
	}{
		{name: "already on recovery cadence", stagedHeight: 105, signingPeriod: 3, attempts: 3, want: 114},
		{name: "round up to next cadence", stagedHeight: 104, signingPeriod: 3, attempts: 3, want: 114},
		{name: "different cadence", stagedHeight: 101, signingPeriod: 5, attempts: 2, want: 115},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := mempoolTimeoutRecoveryHeight(test.stagedHeight, test.signingPeriod, test.attempts)
			if got != test.want {
				t.Fatalf("got recovery height %d, want %d", got, test.want)
			}
		})
	}
}

func TestMempoolInboundFinalisedRequiresFinalityStage(t *testing.T) {
	t.Parallel()

	confirmation := openapi.InboundConfirmationCountedStage{Completed: true}
	if mempoolInboundFinalised(openapi.TxStagesResponse{InboundConfirmationCounted: &confirmation}) {
		t.Fatal("confirmation counting completion alone must not satisfy finality")
	}
	finalised := openapi.InboundFinalisedStage{Completed: true}
	if !mempoolInboundFinalised(openapi.TxStagesResponse{InboundFinalised: &finalised}) {
		t.Fatal("completed inbound_finalised stage must satisfy finality")
	}
}

func TestAuditMempoolRollbackBlock(t *testing.T) {
	t.Parallel()

	txID := strings.Repeat("AB", 32)
	block := openapi.BlockResponse{
		Header: openapi.BlockResponseHeader{Height: 42},
		FinalizeBlockEvents: []map[string]string{{
			"type": "security", "id": txID, "msg": mempoolRollbackSecurityMsg,
		}},
		BeginBlockEvents: []map[string]string{{
			"type": "mempool_swap_reverted", "tx_id": txID, "block_height": "42",
		}},
		EndBlockEvents: []map[string]string{{
			"type": "scheduled_outbound", "in_hash": txID,
		}},
		Txs: []openapi.BlockTx{{Result: openapi.BlockTxResult{Events: []map[string]string{
			{"type": "outbound", "in_tx_id": txID},
			{"type": "errata", "in_tx_id": txID},
		}}}},
	}
	var summary mempoolRollbackEventSummary
	if err := auditMempoolRollbackBlock(block, txID, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.securityCount != 1 || summary.rollbackCount != 1 || summary.ordinaryErrataCount != 1 || summary.rollbackHeight != 42 {
		t.Fatalf("unexpected summary counts: %+v", summary)
	}
	if !reflect.DeepEqual(summary.scheduledOutboundAt, []int64{42}) || !reflect.DeepEqual(summary.observedOutboundAt, []int64{42}) {
		t.Fatalf("unexpected outbound history: %+v", summary)
	}

	block.BeginBlockEvents[0]["block_height"] = "41"
	if err := auditMempoolRollbackBlock(block, txID, &mempoolRollbackEventSummary{}); err == nil {
		t.Fatal("rollback event height different from its containing block must fail")
	}
}

func TestValidateMempoolRollbackEventSummary(t *testing.T) {
	t.Parallel()

	txID := strings.Repeat("AB", 32)
	base := mempoolRollbackEventSummary{
		securityCount:  1,
		rollbackCount:  1,
		rollbackHeight: 20,
	}
	if err := validateMempoolRollbackEventSummary(txID, mempoolRollbackEventExpectation{expectedRollbackHeight: 20}, base); err != nil {
		t.Fatal(err)
	}

	duplicate := base
	duplicate.rollbackCount = 2
	if err := validateMempoolRollbackEventSummary(txID, mempoolRollbackEventExpectation{}, duplicate); err == nil {
		t.Fatal("duplicate rollback event must fail")
	}

	preFinality := base
	preFinality.scheduledOutboundAt = []int64{21}
	if err := validateMempoolRollbackEventSummary(txID, mempoolRollbackEventExpectation{timeout: true}, preFinality); err == nil {
		t.Fatal("timeout outbound before a known finality height must fail")
	}

	postFinality := base
	postFinality.scheduledOutboundAt = []int64{30}
	postFinality.observedOutboundAt = []int64{31}
	expectation := mempoolRollbackEventExpectation{timeout: true, expectedRollbackHeight: 20, finalisedHeight: 30, requireOutboundHistory: true}
	if err := validateMempoolRollbackEventSummary(txID, expectation, postFinality); err != nil {
		t.Fatal(err)
	}
	postFinality.scheduledOutboundAt[0] = 29
	if err := validateMempoolRollbackEventSummary(txID, expectation, postFinality); err == nil {
		t.Fatal("outbound history before actual finality must fail")
	}
}
