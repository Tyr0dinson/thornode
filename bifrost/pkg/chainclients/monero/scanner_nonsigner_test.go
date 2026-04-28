package monero

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/rs/zerolog/log"

	stypes "gitlab.com/thorchain/thornode/v3/bifrost/thorclient/types"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/config"
	ttypes "gitlab.com/thorchain/thornode/v3/x/thorchain/types"
)

// TestNonSignerCannotObserveOutbound demonstrates the bug: a node that did not
// participate in FROST signing never receives the OutgoingTransferRecord from
// its sidecar, so it cannot observe (and therefore attest to) outbound
// transactions. In a 2-of-3 threshold setup the two signers see the outgoing
// transfer while the non-signer sees an empty outgoing list.
func TestNonSignerCannotObserveOutbound(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	outboundTxID := strings.Repeat("ab", 32)
	destAddr := testOutboundXMRDestination()
	outboundAmount := uint64(5_000_000_000_000) // 0.5 XMR in piconero
	outboundFee := uint64(30_000_000)           // typical fee

	outboundTransfer := xmrFrostTransferEntry{
		TxID:          outboundTxID,
		Address:       destAddr.String(),
		FromAddress:   vaultAddr.String(),
		VaultPubKey:   vaultPubKey.String(),
		Amount:        outboundAmount,
		Fee:           outboundFee,
		Height:        100,
		Confirmations: 5,
		Note:          "OUT:" + strings.Repeat("cc", 32),
		Locked:        false,
	}

	// signerFrost returns the outgoing transfer (signing node has the record).
	signerFrost := newMockFrostServer(t, 100, []xmrFrostTransferEntry{outboundTransfer})
	defer signerFrost.Close()

	// nonSignerFrost returns empty outgoing (non-signing node has no record).
	nonSignerFrost := newMockFrostServer(t, 100, nil)
	defer nonSignerFrost.Close()

	// --- Signer node: should observe the outbound ---
	signerScanner := newTestScanner(t, signerFrost.URL, vaultPubKey)
	signerTxIn, err := signerScanner.FetchTxs(100, 150)
	if err != nil {
		t.Fatalf("signer FetchTxs: %v", err)
	}

	var signerOutbound int
	for _, item := range signerTxIn.TxArray {
		if item.Sender == vaultAddr.String() {
			signerOutbound++
		}
	}
	if signerOutbound == 0 {
		t.Fatal("signer node should observe the outbound transfer but found none")
	}

	// --- Non-signer node: cannot observe the outbound (the bug) ---
	nonSignerScanner := newTestScanner(t, nonSignerFrost.URL, vaultPubKey)
	nonSignerTxIn, err := nonSignerScanner.FetchTxs(100, 150)
	if err != nil {
		t.Fatalf("non-signer FetchTxs: %v", err)
	}

	var nonSignerOutbound int
	for _, item := range nonSignerTxIn.TxArray {
		if item.Sender == vaultAddr.String() {
			nonSignerOutbound++
		}
	}

	// This assertion documents the bug: non-signer sees 0 outbounds while
	// signer sees 1. Once the fix lands, flip this to require equality.
	if nonSignerOutbound != 0 {
		t.Fatalf("expected non-signer to see 0 outbounds (known bug), got %d", nonSignerOutbound)
	}
	t.Logf("BUG CONFIRMED: signer observed %d outbound(s), non-signer observed %d", signerOutbound, nonSignerOutbound)
}

// TestTwoOfThreeSignerConsensus verifies that in a 2-of-3 setup both signers
// produce identical observations while the non-signer diverges (empty).
func TestTwoOfThreeSignerConsensus(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	outboundTxID := strings.Repeat("de", 32)
	destAddr := testOutboundXMRDestination()
	memo := "OUT:" + strings.Repeat("ff", 32)

	transfer := xmrFrostTransferEntry{
		TxID:          outboundTxID,
		Address:       destAddr.String(),
		FromAddress:   vaultAddr.String(),
		VaultPubKey:   vaultPubKey.String(),
		Amount:        10_000_000_000_000,
		Fee:           25_000_000,
		Height:        200,
		Confirmations: 3,
		Note:          memo,
		Locked:        false,
	}

	signer1Frost := newMockFrostServer(t, 200, []xmrFrostTransferEntry{transfer})
	defer signer1Frost.Close()
	signer2Frost := newMockFrostServer(t, 200, []xmrFrostTransferEntry{transfer})
	defer signer2Frost.Close()
	nonSignerFrost := newMockFrostServer(t, 200, nil)
	defer nonSignerFrost.Close()

	s1 := newTestScanner(t, signer1Frost.URL, vaultPubKey)
	s2 := newTestScanner(t, signer2Frost.URL, vaultPubKey)
	ns := newTestScanner(t, nonSignerFrost.URL, vaultPubKey)

	txIn1, err := s1.FetchTxs(200, 250)
	if err != nil {
		t.Fatalf("signer1 FetchTxs: %v", err)
	}
	txIn2, err := s2.FetchTxs(200, 250)
	if err != nil {
		t.Fatalf("signer2 FetchTxs: %v", err)
	}
	txInNS, err := ns.FetchTxs(200, 250)
	if err != nil {
		t.Fatalf("non-signer FetchTxs: %v", err)
	}

	outbound1 := countOutboundsFrom(txIn1.TxArray, vaultAddr.String())
	outbound2 := countOutboundsFrom(txIn2.TxArray, vaultAddr.String())
	outboundNS := countOutboundsFrom(txInNS.TxArray, vaultAddr.String())

	if outbound1 != outbound2 {
		t.Fatalf("signers disagree: signer1=%d signer2=%d", outbound1, outbound2)
	}
	if outbound1 == 0 {
		t.Fatal("both signers should observe the outbound")
	}

	// The non-signer should match but currently cannot.
	if outboundNS == outbound1 {
		t.Log("non-signer matches signers — bug is fixed!")
	} else {
		t.Logf("BUG: non-signer observed %d outbound(s), signers observed %d", outboundNS, outbound1)
	}
}

// TestNonSignerMissingFromAddressFailsClosed verifies that resolveOutgoingSender
// rejects outgoing transfers when from_address is empty (as returned to non-signers).
func TestNonSignerMissingFromAddressFailsClosed(t *testing.T) {
	t.Parallel()

	_, vaultAddr := testVaultPubKeyAndAddress(t)
	s := &Scanner{
		logger: log.Logger,
	}

	// Non-signer gets a transfer with empty from_address and vault_pub_key.
	sender, pk := s.resolveOutgoingSender(xmrFrostTransferEntry{
		TxID:        strings.Repeat("ab", 32),
		Address:     vaultAddr.String(),
		FromAddress: "",
		VaultPubKey: "",
		Amount:      1_000_000_000_000,
	})
	if sender != "" || !pk.IsEmpty() {
		t.Fatalf("expected fail-closed for missing metadata, got sender=%q pk=%s", sender, pk)
	}
}

func TestUnauthorizedSpendObservedBySignerAndNonSigner(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vaultAddress := vaultAddr.String()
	spendTxID := strings.Repeat("fa", 32)

	unauthorized := xmrFrostTransferEntry{
		TxID:        spendTxID,
		Address:     common.XMRUnknownSenderAddress(spendTxID, vaultAddress),
		FromAddress: vaultAddress,
		VaultPubKey: vaultPubKey.String(),
		Amount:      2 * moneroAtomicToThorchain,
		Fee:         0,
		Height:      300,
		Note:        "NOOP:UNAUTHORIZED",
	}

	signerFrost := newMockFrostServer(t, 300, []xmrFrostTransferEntry{unauthorized})
	defer signerFrost.Close()
	nonSignerFrost := newMockFrostServer(t, 300, []xmrFrostTransferEntry{unauthorized})
	defer nonSignerFrost.Close()

	signerScanner := newTestScanner(t, signerFrost.URL, vaultPubKey)
	nonSignerScanner := newTestScanner(t, nonSignerFrost.URL, vaultPubKey)

	signerTxIn, err := signerScanner.FetchTxs(300, 350)
	if err != nil {
		t.Fatalf("signer FetchTxs: %v", err)
	}
	nonSignerTxIn, err := nonSignerScanner.FetchTxs(300, 350)
	if err != nil {
		t.Fatalf("non-signer FetchTxs: %v", err)
	}

	if len(signerTxIn.TxArray) != 1 {
		t.Fatalf("expected signer to observe unauthorized spend once, got %d", len(signerTxIn.TxArray))
	}
	if len(nonSignerTxIn.TxArray) != 1 {
		t.Fatalf("expected non-signer to observe unauthorized spend once, got %d", len(nonSignerTxIn.TxArray))
	}

	for label, item := range map[string]*stypes.TxInItem{
		"signer":     signerTxIn.TxArray[0],
		"non-signer": nonSignerTxIn.TxArray[0],
	} {
		if item.Tx != spendTxID {
			t.Fatalf("%s tx mismatch: expected %s, got %s", label, spendTxID, item.Tx)
		}
		if item.Memo != "NOOP:UNAUTHORIZED" {
			t.Fatalf("%s memo mismatch: expected NOOP:UNAUTHORIZED, got %s", label, item.Memo)
		}
		if item.Sender != vaultAddress {
			t.Fatalf("%s sender mismatch: expected %s, got %s", label, vaultAddress, item.Sender)
		}
		if !item.ObservedVaultPubKey.Equals(vaultPubKey) {
			t.Fatalf("%s vault pubkey mismatch: expected %s, got %s", label, vaultPubKey, item.ObservedVaultPubKey)
		}
		if len(item.TxKeys) != 0 {
			t.Fatalf("%s unauthorized spend should not carry tx keys: %+v", label, item.TxKeys)
		}
	}
}

func TestUnauthorizedSpendToUnknownAddressCannotFallThroughAsNormalOutbound(t *testing.T) {
	t.Parallel()

	vaultPubKey, vaultAddr := testVaultPubKeyAndAddress(t)
	vaultAddress := vaultAddr.String()
	spendTxID := strings.Repeat("fb", 32)

	// Defensive case for replay paths: even if the sidecar response carries
	// stale tx-key fields or a missing note, xmrunknown destination means this
	// is a synthetic theft observation, not a normal outbound.
	unauthorized := xmrFrostTransferEntry{
		TxID:        spendTxID,
		Address:     common.XMRUnknownSenderAddress(spendTxID, vaultAddress),
		FromAddress: vaultAddress,
		VaultPubKey: vaultPubKey.String(),
		TxKeys:      []string{strings.Repeat("1", 64)},
		Amount:      2 * moneroAtomicToThorchain,
		Fee:         30_000_000,
		Height:      301,
		Note:        "",
	}

	frost := newMockFrostServer(t, 301, []xmrFrostTransferEntry{unauthorized})
	defer frost.Close()

	scanner := newTestScanner(t, frost.URL, vaultPubKey)
	txIn, err := scanner.FetchTxs(301, 350)
	if err != nil {
		t.Fatalf("FetchTxs: %v", err)
	}
	if len(txIn.TxArray) != 1 {
		t.Fatalf("expected one unauthorized observation, got %d", len(txIn.TxArray))
	}
	item := txIn.TxArray[0]
	if item.Memo != "NOOP:UNAUTHORIZED" {
		t.Fatalf("expected NOOP:UNAUTHORIZED memo, got %q", item.Memo)
	}
	if len(item.TxKeys) != 0 {
		t.Fatalf("unauthorized spend must not retain tx keys: %+v", item.TxKeys)
	}
	if item.Sender != vaultAddress {
		t.Fatalf("expected vault sender %s, got %s", vaultAddress, item.Sender)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func countOutboundsFrom(items []*stypes.TxInItem, sender string) int {
	count := 0
	for _, item := range items {
		if item.Sender == sender {
			count++
		}
	}
	return count
}

func newTestScanner(t *testing.T, frostURL string, vaultPubKey common.PubKey) *Scanner {
	t.Helper()
	frostSigner, err := NewFrostSignerWithEndpoint(
		config.BifrostChainConfiguration{},
		frostURL,
	)
	if err != nil {
		t.Fatalf("new frost signer: %v", err)
	}
	bridge := &scannerMemoBridge{asgards: ttypes.Vaults{}}
	return NewScanner(log.Logger, frostSigner, bridge, common.XMRChain, common.EmptyPubKey, 72)
}

func newMockFrostServer(t *testing.T, blockHeight int64, outgoing []xmrFrostTransferEntry) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/monero/chain/height":
			_ = json.NewEncoder(w).Encode(xmrFrostChainHeightResponse{Height: blockHeight + 50})

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/monero/chain/block/"):
			rawHeight := strings.TrimPrefix(r.URL.Path, "/v1/monero/chain/block/")
			height, err := strconv.ParseInt(rawHeight, 10, 64)
			if err != nil {
				http.Error(w, "invalid height", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(xmrFrostChainBlockResponse{
				Height:       height,
				Reward:       defaultCoinbaseValue + 200000,
				Hash:         fmt.Sprintf("hash-%d", height),
				PreviousHash: fmt.Sprintf("hash-%d", height-1),
				TxHashes:     []string{},
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/monero/scanner/sync":
			resp := xmrFrostTransfersResponse{}
			if outgoing != nil {
				resp.Outgoing = outgoing
			}
			_ = json.NewEncoder(w).Encode(resp)

		default:
			http.NotFound(w, r)
		}
	}))
}
