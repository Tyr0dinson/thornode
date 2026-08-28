package substrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog/log"

	"gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/rpc"
	stypes "gitlab.com/thorchain/thornode/v3/bifrost/pkg/chainclients/substrate/types"
	"gitlab.com/thorchain/thornode/v3/bifrost/pubkeymanager"
	"gitlab.com/thorchain/thornode/v3/common"
	"gitlab.com/thorchain/thornode/v3/common/cosmos"
	"gitlab.com/thorchain/thornode/v3/common/crypto/ss58"
	"gitlab.com/thorchain/thornode/v3/config"
	"gitlab.com/thorchain/thornode/v3/constants"

	. "gopkg.in/check.v1"
)

func TestScannerSuite(t *testing.T) { TestingT(t) }

type ScannerSuite struct{}

var _ = Suite(&ScannerSuite{})

func mustTestExtrinsicLayout(chain common.Chain) stypes.ExtrinsicLayout {
	layout, err := stypes.NewExtrinsicLayout(ChainParams[chain].SignedExtensions)
	if err != nil {
		panic(err)
	}
	return layout
}

func mustEncodeTestExtrinsic(ext *stypes.Extrinsic, chain common.Chain) []byte {
	encoded, err := stypes.EncodeSignedExtrinsic(ext, mustTestExtrinsicLayout(chain))
	if err != nil {
		panic(err)
	}
	return encoded
}

// newTestScanner creates a minimal scanner for unit testing parse functions.
// Defaults to the no-op pubkey validator (recognizes no addresses as vaults);
// tests that exercise vault recognition can override pubkeyMgr after construction.
func newTestScanner() *Scanner {
	s := &Scanner{
		logger: log.Logger,
		params: ChainParams[common.TAOChain],
		cfg: config.BifrostBlockScannerConfiguration{
			ChainID: common.TAOChain,
		},
		pubkeyMgr:  pubkeymanager.NewMockPoolAddressValidator(),
		nonceCache: newNonceCache(),
		runtime: runtimeContext{
			layout: mustTestExtrinsicLayout(common.TAOChain),
		},
	}
	return s
}

func newAssetHubTestScanner(c *C) *Scanner {
	raw, err := os.ReadFile("types/testdata/metadata-dot-assethub-2003001.hex")
	c.Assert(err, IsNil)
	metadata, err := stypes.ParseMetadata(strings.TrimSpace(string(raw)))
	c.Assert(err, IsNil)

	return &Scanner{
		logger: log.Logger,
		params: ChainParams[common.DOTChain],
		cfg: config.BifrostBlockScannerConfiguration{
			ChainID: common.DOTChain,
		},
		pubkeyMgr:  pubkeymanager.NewMockPoolAddressValidator(),
		nonceCache: newNonceCache(),
		runtime: runtimeContext{
			metadata: metadata,
			layout:   mustTestExtrinsicLayout(common.DOTChain),
		},
	}
}

// vaultPubKeyStub recognizes a fixed set of addresses as valid vaults.
// All other PubKeyValidator methods inherit from MockPoolAddressValidator.
type vaultPubKeyStub struct {
	*pubkeymanager.MockPoolAddressValidator
	vaults map[string]bool
}

func (v *vaultPubKeyStub) IsValidPoolAddress(addr string, chain common.Chain) (bool, common.ChainPoolInfo) {
	if v.vaults[addr] {
		return true, common.ChainPoolInfo{Chain: chain}
	}
	return false, common.EmptyChainPoolInfo
}

// defaultTestSigner returns the 32-byte pubkey baked into buildTestExtrinsic.
func defaultTestSigner() []byte {
	signer := make([]byte, 32)
	for i := range signer {
		signer[i] = byte(i + 1)
	}
	return signer
}

// buildTestExtrinsic constructs a hex-encoded signed extrinsic for testing.
// Uses defaultTestSigner — only the call data varies.
func buildTestExtrinsic(callData []byte) string {
	return buildTestExtrinsicFromSigner(defaultTestSigner(), callData)
}

// buildTestExtrinsicFromSigner constructs a hex-encoded signed extrinsic with
// a caller-supplied signer pubkey. Lets tests vary txItem.Sender to exercise
// vault-vs-non-vault paths.
func buildTestExtrinsicFromSigner(signer, callData []byte) string {
	ext := &stypes.Extrinsic{
		Signer:        signer,
		SignatureType: stypes.SignatureTypeEd25519,
		Signature:     make([]byte, 64),
		Era:           stypes.ImmortalEra(),
		Nonce:         0,
		Tip:           0,
		MetadataMode:  stypes.MetadataModeDisabled,
		CallData:      callData,
	}

	return stypes.HexEncode(mustEncodeTestExtrinsic(ext, common.TAOChain))
}

func buildAssetHubTestExtrinsic(callData []byte) string {
	ext := &stypes.Extrinsic{
		Signer:        defaultTestSigner(),
		SignatureType: stypes.SignatureTypeEd25519,
		Signature:     make([]byte, 64),
		Era:           stypes.ImmortalEra(),
		Nonce:         0,
		Tip:           0,
		MetadataMode:  stypes.MetadataModeDisabled,
		CallData:      callData,
	}
	encoded := mustEncodeTestExtrinsic(ext, common.DOTChain)
	_, prefixLen, _ := stypes.DecodeCompact(encoded)
	body := encoded[prefixLen:]
	optionPos := len(body) - len(callData) - 2
	tipStart := optionPos - len(stypes.EncodeCompact(ext.Tip))

	payment := []byte{0x00, 0x01, 0x00, 0x02, 0x04, 50, 0x05}
	payment = append(payment, stypes.EncodeCompact(1984)...)
	replaced := make([]byte, 0, len(body)+len(payment)-2)
	replaced = append(replaced, body[:tipStart]...)
	replaced = append(replaced, payment...)
	replaced = append(replaced, body[optionPos+1:]...)

	result := stypes.EncodeCompact(uint64(len(replaced)))
	result = append(result, replaced...)
	return stypes.HexEncode(result)
}

// ---------------------------------------------------------------------------
// isTransferCall
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestIsTransferCall(c *C) {
	tests := []struct {
		callIdx  byte
		expected bool
	}{
		{0, true}, // transferAllowDeathCallIndex
		{3, true}, // transfer_keep_alive
		{1, false},
		{2, false},
		{4, false},
		{7, false}, // force_set_balance, NOT a transfer
		{255, false},
	}
	for _, tt := range tests {
		sc := newTestScanner()
		got := sc.isTransferCall(tt.callIdx)
		c.Check(got, Equals, tt.expected,
			Commentf("isTransferCall(%d): got %v, want %v", tt.callIdx, got, tt.expected))
	}
}

// ---------------------------------------------------------------------------
// convertRaoToThorchain
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestConvertRaoToThorchain(c *C) {
	tests := []struct {
		rao      uint64
		expected uint64
	}{
		{0, 0},
		{10, 1},
		{100_000, 10_000},
		{1_000_000_000, 100_000_000}, // 1 TAO → 1e8
		{9, 0},                       // truncation
		{15, 1},                      // truncation
		{1, 0},                       // below threshold
	}
	for _, tt := range tests {
		result := common.TAOChain.NativeGasToThorchain(cosmos.NewUint(tt.rao))
		c.Check(result.Equal(cosmos.NewUint(tt.expected)), Equals, true,
			Commentf("convertRaoToThorchain(%d): got %s, want %d", tt.rao, result, tt.expected))
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — simple transfer
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_SimpleTransfer(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xAA
	amount := cosmos.NewUint(1_000_000_000)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)

	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	c.Check(txItem.BlockHeight, Equals, int64(100))
	c.Check(txItem.Tx, Not(Equals), "", Commentf("tx hash should not be empty"))
	c.Check(txItem.Sender, Not(Equals), "", Commentf("sender should not be empty"))
	c.Check(txItem.To, Not(Equals), "", Commentf("to should not be empty"))

	// 1_000_000_000 rao / 10 = 100_000_000
	expectedAmount := cosmos.NewUint(100_000_000)
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
	c.Check(len(txItem.Gas) > 0, Equals, true, Commentf("gas should not be empty"))
}

func (s *ScannerSuite) TestProcessExtrinsic_TransferAllowDeath(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	call := stypes.BalanceTransferKeepAlive(5, 0, dest, cosmos.NewUint(500_000)) // allow_death index

	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 50, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem for allow_death transfer"))
}

func (s *ScannerSuite) TestProcessExtrinsic_ForceSetBalanceIgnored(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	// Call index 7 is force_set_balance (governance-only), should be ignored
	call := stypes.BalanceTransferKeepAlive(5, 7, dest, cosmos.NewUint(500_000))

	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 50, 0)
	c.Assert(err, IsNil)
	c.Check(txItem, IsNil, Commentf("expected nil TxInItem for force_set_balance (call index 7)"))
}

func (s *ScannerSuite) TestProcessExtrinsic_ZeroAmount(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(0))

	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Check(txItem, IsNil, Commentf("expected nil TxInItem for zero/dust amount"))
}

func (s *ScannerSuite) TestProcessExtrinsic_LargeAmount(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	amount := cosmos.NewUint(21_000_000_000_000_000) // 21M TAO
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)

	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	expectedAmount := common.TAOChain.NativeGasToThorchain(amount)
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
}

func (s *ScannerSuite) TestProcessExtrinsic_AssetHubNonNativeFee(c *C) {
	sc := newAssetHubTestScanner(c)
	dest := bytes.Repeat([]byte{0xAB}, 32)
	amount := cosmos.NewUint(2_000_000_000_000)
	transfer := stypes.BalanceTransferKeepAlive(10, 3, dest, amount)

	txItem, err := sc.processExtrinsic(buildAssetHubTestExtrinsic(transfer), 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil)
	c.Check(txItem.Coins[0].Amount.Equal(common.DOTChain.NativeGasToThorchain(amount)), Equals, true)
	c.Check(txItem.Gas[0].Amount.Equal(cosmos.OneUint()), Equals, true)

	memo := "=:BTC.BTC:bc1qexample"
	batch := stypes.BatchAll(40, 2, [][]byte{
		transfer,
		stypes.SystemRemark(0, 0, []byte(memo)),
	})
	txItem, err = sc.processExtrinsic(buildAssetHubTestExtrinsic(batch), 101, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil)
	c.Check(txItem.Memo, Equals, memo)
	c.Check(txItem.Coins[0].Amount.Equal(common.DOTChain.NativeGasToThorchain(amount)), Equals, true)
}

// ---------------------------------------------------------------------------
// processExtrinsic — batch_all with memo
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_BatchWithMemo(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xBB
	amount := cosmos.NewUint(2_000_000_000)

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := stypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1qtest"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall})

	extHex := buildTestExtrinsic(batchCall)

	txItem, err := sc.processExtrinsic(extHex, 200, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	c.Check(txItem.Memo, Equals, "SWAP:BTC.BTC:bc1qtest")

	expectedAmount := cosmos.NewUint(200_000_000) // 2e9 / 10
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
}

func (s *ScannerSuite) TestProcessExtrinsic_BatchRemarkBeforeTransfer(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	amount := cosmos.NewUint(1_000_000_000)

	// Remark comes BEFORE transfer in the batch
	remarkCall := stypes.SystemRemark(0, 0, []byte("ADD:TAO.TAO"))
	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	batchCall := stypes.BatchAll(11, 2, [][]byte{remarkCall, transferCall})

	extHex := buildTestExtrinsic(batchCall)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	c.Check(txItem.Memo, Equals, "ADD:TAO.TAO")

	expectedAmount := cosmos.NewUint(100_000_000)
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
}

// ---------------------------------------------------------------------------
// processExtrinsic — batch edge cases
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_BatchMultiTransferRejected(c *C) {
	sc := newTestScanner()

	dest1 := make([]byte, 32)
	dest1[0] = 0x11
	dest2 := make([]byte, 32)
	dest2[0] = 0x22

	// Batch with multiple transfers must be rejected to prevent ambiguity
	transfer1 := stypes.BalanceTransferKeepAlive(5, 3, dest1, cosmos.NewUint(10_000_000_000))
	transfer2 := stypes.BalanceTransferKeepAlive(5, 3, dest2, cosmos.NewUint(100))
	remarkCall := stypes.SystemRemark(0, 0, []byte("memo"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{transfer1, transfer2, remarkCall})

	extHex := buildTestExtrinsic(batchCall)

	_, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, NotNil, Commentf("expected error for batch with multiple transfers"))
	c.Check(strings.Contains(err.Error(), "multiple transfer"), Equals, true,
		Commentf("error should mention multiple transfers, got: %v", err))
}

func (s *ScannerSuite) TestProcessExtrinsic_BatchRemarkOnlyReturnsNil(c *C) {
	sc := newTestScanner()

	remarkCall := stypes.SystemRemark(0, 0, []byte("just a remark"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{remarkCall})

	extHex := buildTestExtrinsic(batchCall)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Check(txItem, IsNil, Commentf("expected nil for batch with only remark (no transfer)"))
}

func (s *ScannerSuite) TestProcessExtrinsic_BatchMultipleRemarksRejected(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xCC
	amount := cosmos.NewUint(1_000_000_000)

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remark1 := stypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1q1"))
	remark2 := stypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1q2"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{remark1, transferCall, remark2})

	extHex := buildTestExtrinsic(batchCall)

	_, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, NotNil, Commentf("expected error for batch with multiple remarks"))
}

func (s *ScannerSuite) TestProcessExtrinsic_BatchEmptyFirstRemarkRejected(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xCC
	amount := cosmos.NewUint(1_000_000_000)

	emptyRemark := stypes.SystemRemark(0, 0, []byte{})
	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	realRemark := stypes.SystemRemark(0, 0, []byte("SWAP:BTC.BTC:bc1qattacker"))
	batchCall := stypes.BatchAll(11, 2, [][]byte{emptyRemark, transferCall, realRemark})

	extHex := buildTestExtrinsic(batchCall)

	_, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, NotNil,
		Commentf("expected error: empty first remark must not bypass the multi-remark guard"))
	c.Check(strings.Contains(err.Error(), "multiple remark"), Equals, true,
		Commentf("error should mention multiple remarks, got: %v", err))
}

func (s *ScannerSuite) TestProcessExtrinsic_BatchUnknownCallUsesFirstTransfer(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xDD
	amount := cosmos.NewUint(5_000_000_000) // 5 TAO

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := stypes.SystemRemark(0, 0, []byte("ADD:TAO.TAO"))
	// Unknown call from pallet 99 — parser breaks here
	unknownCall := []byte{99, 1, 0xFF, 0xFF}
	batchCall := stypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall, unknownCall})

	extHex := buildTestExtrinsic(batchCall)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	// Transfer should still be captured despite unknown call
	expectedAmount := cosmos.NewUint(500_000_000) // 5e9 / 10
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
	c.Check(txItem.Memo, Equals, "ADD:TAO.TAO")
}

// ---------------------------------------------------------------------------
// processExtrinsic — edge cases and error paths
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_UnsignedReturnsNil(c *C) {
	sc := newTestScanner()

	// Build an unsigned extrinsic (version byte 0x04, bit 7 not set)
	body := []byte{0x04, 5, 3}
	length := stypes.EncodeCompact(uint64(len(body)))
	extHex := stypes.HexEncode(append(length, body...))

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Check(txItem, IsNil, Commentf("expected nil for unsigned extrinsic"))
}

func (s *ScannerSuite) TestProcessExtrinsic_UnknownPalletReturnsNil(c *C) {
	sc := newTestScanner()

	call := []byte{99, 0, 0x00} // unknown pallet 99
	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Check(txItem, IsNil, Commentf("expected nil for unknown pallet"))
}

func (s *ScannerSuite) TestProcessExtrinsic_EmptyHex(c *C) {
	sc := newTestScanner()
	_, err := sc.processExtrinsic("", 100, 0)
	c.Check(err, NotNil, Commentf("expected error for empty hex"))
}

func (s *ScannerSuite) TestProcessExtrinsic_MalformedHex(c *C) {
	sc := newTestScanner()
	_, err := sc.processExtrinsic("0xZZZZ", 100, 0)
	c.Check(err, NotNil, Commentf("expected error for malformed hex"))
}

func (s *ScannerSuite) TestProcessExtrinsic_TruncatedExtrinsic(c *C) {
	sc := newTestScanner()

	// Valid length prefix claiming 200 bytes but only 3 bytes of body
	length := stypes.EncodeCompact(200)
	extHex := stypes.HexEncode(append(length, 0x84, 0x00, 0x01))

	_, err := sc.processExtrinsic(extHex, 100, 0)
	c.Check(err, NotNil, Commentf("expected error for truncated extrinsic"))
}

// ---------------------------------------------------------------------------
// processExtrinsic — signature type handling
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_ECDSASignature(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xEE
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(1_000_000_000))

	signer := make([]byte, 32)
	for i := range signer {
		signer[i] = byte(i + 1)
	}

	// ECDSA (type 0x02) uses 65-byte signatures
	ext := &stypes.Extrinsic{
		Signer:        signer,
		SignatureType: 0x02, // ECDSA
		Signature:     make([]byte, 65),
		Era:           stypes.ImmortalEra(),
		Nonce:         0,
		Tip:           0,
		MetadataMode:  stypes.MetadataModeDisabled,
		CallData:      call,
	}
	extHex := stypes.HexEncode(mustEncodeTestExtrinsic(ext, common.TAOChain))

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil, Commentf("processExtrinsic with ECDSA sig"))
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem for ECDSA extrinsic"))

	expectedAmount := cosmos.NewUint(100_000_000) // 1e9 / 10
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
}

func (s *ScannerSuite) TestProcessExtrinsic_UnknownSignatureTypeRejected(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(500_000))

	signer := make([]byte, 32)
	for i := range signer {
		signer[i] = byte(i + 1)
	}

	// Unknown signature type 0x03
	ext := &stypes.Extrinsic{
		Signer:        signer,
		SignatureType: 0x03,
		Signature:     make([]byte, 64),
		Era:           stypes.ImmortalEra(),
		Nonce:         0,
		Tip:           0,
		MetadataMode:  stypes.MetadataModeDisabled,
		CallData:      call,
	}
	extHex := stypes.HexEncode(mustEncodeTestExtrinsic(ext, common.TAOChain))

	_, err := sc.processExtrinsic(extHex, 100, 0)
	c.Check(err, NotNil, Commentf("expected error for unknown signature type 0x03"))
}

// ---------------------------------------------------------------------------
// processExtrinsic — mortal era parsing
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_MortalEra(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(500_000))

	// Build extrinsic with mortal era instead of immortal
	signer := make([]byte, 32)
	for i := range signer {
		signer[i] = byte(i + 1)
	}

	ext := &stypes.Extrinsic{
		Signer:        signer,
		SignatureType: stypes.SignatureTypeEd25519,
		Signature:     make([]byte, 64),
		Era:           stypes.MortalEra(64, 42),
		Nonce:         7,
		Tip:           100,
		MetadataMode:  stypes.MetadataModeDisabled,
		CallData:      call,
	}
	extHex := stypes.HexEncode(mustEncodeTestExtrinsic(ext, common.TAOChain))

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem with mortal era"))

	expectedAmount := cosmos.NewUint(50_000) // 500_000 / 10
	c.Check(txItem.Coins[0].Amount.Equal(expectedAmount), Equals, true,
		Commentf("amount: got %s, want %s", txItem.Coins[0].Amount, expectedAmount))
}

// ---------------------------------------------------------------------------
// Extrinsic hash consistency between scanner and types
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestExtrinsicHashConsistency(c *C) {
	dest := make([]byte, 32)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(1_000_000))
	raw := mustEncodeTestExtrinsic(&stypes.Extrinsic{
		Signer:        make([]byte, 32),
		SignatureType: stypes.SignatureTypeEd25519,
		Signature:     make([]byte, 64),
		Era:           stypes.ImmortalEra(),
		Nonce:         0,
		Tip:           0,
		MetadataMode:  stypes.MetadataModeDisabled,
		CallData:      call,
	}, common.TAOChain)

	// Both should produce the same hash for the same raw bytes
	hash1 := stypes.ExtrinsicHash(raw)
	hash2 := stypes.ExtrinsicHash(raw)
	c.Check(hash1, Equals, hash2, Commentf("inconsistent hashes"))
	c.Check(hash1[:2], Equals, "0x", Commentf("hash should have 0x prefix"))
}

func (s *ScannerSuite) TestRuntimeValidationFailureKeepsPreviousContext(c *C) {
	raw, err := os.ReadFile("types/testdata/metadata-tao-mainnet-401.hex")
	c.Assert(err, IsNil)
	metadata, err := stypes.ParseMetadata(strings.TrimSpace(string(raw)))
	c.Assert(err, IsNil)

	h := newRPCHandler()
	h.on("state_getRuntimeVersion", func(_ json.RawMessage) (interface{}, error) {
		return rpc.RuntimeVersion{
			SpecName:           ChainParams[common.TAOChain].SpecName,
			SpecVersion:        402,
			TransactionVersion: 2,
		}, nil
	})
	h.on("state_getMetadata", func(_ json.RawMessage) (interface{}, error) {
		return strings.TrimSpace(string(raw)), nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	params := ChainParams[common.TAOChain]
	params.SignedExtensions = append([]stypes.SignedExtension(nil), params.SignedExtensions...)
	params.SignedExtensions[2].AdditionalType = "U64"
	old := runtimeContext{
		metadata:    metadata,
		layout:      mustTestExtrinsicLayout(common.TAOChain),
		specVersion: 401,
		txVersion:   1,
	}
	scanner := &Scanner{
		logger:    log.Logger,
		params:    params,
		rpcClient: rpc.NewClient(srv.URL, 5*time.Second),
		runtime:   old,
	}

	_, err = scanner.checkRuntimeVersion("0x01")
	c.Assert(err, NotNil)
	current := scanner.getRuntimeContext()
	c.Check(current.metadata, Equals, old.metadata)
	c.Check(current.specVersion, Equals, old.specVersion)
	c.Check(current.txVersion, Equals, old.txVersion)
}

// ---------------------------------------------------------------------------
// Fee cache tests
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestGetNetworkFee_Default(c *C) {
	sc := newTestScanner()
	txSize, txRate := sc.GetNetworkFee()
	c.Check(txSize, Equals, uint64(1))
	c.Check(txRate, Equals, ChainParams[common.TAOChain].DefaultFee)
}

func (s *ScannerSuite) TestGetNetworkFee(c *C) {
	sc, ch := newTestScannerWithFeeQueue(1)

	txSize, txRate := sc.GetNetworkFee()
	c.Check(txSize, Equals, uint64(1))
	c.Check(txRate, Equals, uint64(100_000)) // default fee

	for range FeeCacheSize {
		sc.updateFeeCache(80_000)
	}
	c.Assert(sc.feeCache, HasLen, FeeCacheSize)

	// currentFee not updated yet
	c.Check(txRate, Equals, uint64(100_000))

	sc.updateNetworkFee(int64(FeeUpdatePeriod))
	<-ch // drain channel

	txSize, txRate = sc.GetNetworkFee()
	c.Check(txSize, Equals, uint64(1))
	c.Check(txRate, Equals, uint64(80_000))

	// check overflow handling
	sc.updateFeeCache(80_000)
	c.Assert(sc.feeCache, HasLen, FeeCacheSize)
}

// ---------------------------------------------------------------------------
// updateNetworkFee tests
// ---------------------------------------------------------------------------

func newTestScannerWithFeeQueue(resolution int64) (*Scanner, chan common.NetworkFee) {
	ch := make(chan common.NetworkFee, 4)
	sc := &Scanner{
		logger: log.Logger,
		params: ChainParams[common.TAOChain],
		cfg: config.BifrostBlockScannerConfiguration{
			ChainID:            common.TAOChain,
			GasPriceResolution: resolution,
		},
		globalNetworkFeeQueue: ch,
		pubkeyMgr:             pubkeymanager.NewMockPoolAddressValidator(),
	}
	return sc, ch
}

func (s *ScannerSuite) TestReportNetworkFee_BootstrapsWithDefaultFee(c *C) {
	sc, ch := newTestScannerWithFeeQueue(1000)

	// Add fewer than FeeCacheSize samples
	for i := 0; i < FeeCacheSize-1; i++ {
		sc.updateFeeCache(100_000)
	}

	sc.updateNetworkFee(100)

	select {
	case fee := <-ch:
		c.Check(fee.TransactionRate, Equals, sc.params.DefaultFee,
			Commentf("fee rate: got %d, want %d (DefaultFee)", fee.TransactionRate, sc.params.DefaultFee))
	default:
		c.Error("expected DefaultFee to be reported on bootstrap with partial cache")
	}
}

func (s *ScannerSuite) TestReportNetworkFee_ReportsRoundedFee(c *C) {
	sc, ch := newTestScannerWithFeeQueue(1000)

	// Fill cache completely with 100_500 rao average → rounds up to 101_000
	for i := 0; i < FeeCacheSize; i++ {
		sc.updateFeeCache(100_500)
	}

	sc.updateNetworkFee(100)

	select {
	case fee := <-ch:
		c.Check(fee.TransactionRate, Equals, uint64(101_000),
			Commentf("fee rate: got %d, want 101000 (rounded up to resolution)", fee.TransactionRate))
		c.Check(fee.TransactionSize, Equals, uint64(1))
		c.Check(fee.Height, Equals, int64(100))
		c.Check(fee.Chain.Equals(common.TAOChain), Equals, true,
			Commentf("chain: got %s, want TAO", fee.Chain))
	default:
		c.Error("expected fee to be reported")
	}
}

func (s *ScannerSuite) TestReportNetworkFee_SkipsUnchanged(c *C) {
	sc, ch := newTestScannerWithFeeQueue(1000)

	// Fill cache — average = 100_000, rounds to 100_000 (already aligned)
	for i := 0; i < FeeCacheSize; i++ {
		sc.updateFeeCache(100_000)
	}

	// First report should send
	sc.updateNetworkFee(100)
	select {
	case <-ch:
		// expected
	default:
		c.Fatal("first report should send")
	}

	// Second report with identical fees should skip
	sc.updateNetworkFee(200)
	select {
	case <-ch:
		c.Error("should skip unchanged fee")
	default:
		// expected — fee unchanged
	}
}

func (s *ScannerSuite) TestReportNetworkFee_ReportsOnChange(c *C) {
	sc, ch := newTestScannerWithFeeQueue(1000)

	// Fill cache with 100_000
	for i := 0; i < FeeCacheSize; i++ {
		sc.updateFeeCache(100_000)
	}

	sc.updateNetworkFee(100)
	<-ch // consume first report

	// Replace cache with higher fees (200_000)
	for i := 0; i < FeeCacheSize; i++ {
		sc.updateFeeCache(200_000)
	}

	sc.updateNetworkFee(200)
	select {
	case fee := <-ch:
		c.Check(fee.TransactionRate, Equals, uint64(200_000),
			Commentf("fee rate: got %d, want 200000", fee.TransactionRate))
	default:
		c.Error("expected fee to be reported after change")
	}
}

// ---------------------------------------------------------------------------
// processExtrinsic — actual fee usage
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_UsesActualFee(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xAA
	amount := cosmos.NewUint(1_000_000_000)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	extHex := buildTestExtrinsic(call)

	// Pass actual fee of 150_000
	txItem, err := sc.processExtrinsic(extHex, 100, 150_000)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	expectedGas := cosmos.NewUint(15_000)
	c.Assert(len(txItem.Gas) > 0, Equals, true, Commentf("gas should not be empty"))
	c.Check(txItem.Gas[0].Amount.Equal(expectedGas), Equals, true,
		Commentf("gas amount (exact-conversion case): got %s, want %s",
			txItem.Gas[0].Amount, expectedGas))
}

func (s *ScannerSuite) TestProcessExtrinsic_UsesActualFee_RoundsUpOnRemainder(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xAA
	amount := cosmos.NewUint(1_000_000_000)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	extHex := buildTestExtrinsic(call)

	// 150_001 rao has a remainder when divided by 10 (the native-to-thor
	// ratio for TAO). NativeGasToThorCoinCeil must add +1 to avoid
	// under-reporting gas. 150_001 ÷ 10 = 15_000 floor → +1 = 15_001.
	txItem, err := sc.processExtrinsic(extHex, 100, 150_001)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil)

	expectedGas := cosmos.NewUint(15_001)
	c.Assert(len(txItem.Gas) > 0, Equals, true)
	c.Check(txItem.Gas[0].Amount.Equal(expectedGas), Equals, true,
		Commentf("gas amount (remainder case): got %s, want %s",
			txItem.Gas[0].Amount, expectedGas))
}

func (s *ScannerSuite) TestProcessExtrinsic_FallsBackToDefaultFee(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, cosmos.NewUint(500_000))
	extHex := buildTestExtrinsic(call)

	// Pass 0 for actualFee — gas falls back to 1 (minimum placeholder)
	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))

	// Gas uses minimum placeholder when actual fee is unavailable
	expectedGas := cosmos.NewUint(1)
	c.Assert(len(txItem.Gas) > 0, Equals, true, Commentf("gas should not be empty"))
	c.Check(txItem.Gas[0].Amount.Equal(expectedGas), Equals, true,
		Commentf("gas amount: got %s, want %s (should use minimum placeholder)", txItem.Gas[0].Amount, expectedGas))
}

// ---------------------------------------------------------------------------
// processExtrinsic — memo size validation
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessExtrinsic_OversizedMemoDropped(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xBB
	amount := cosmos.NewUint(2_000_000_000)

	// Build a memo that exceeds MaxMemoSize (250 bytes)
	longMemo := strings.Repeat("A", constants.MaxMemoSize+1)

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := stypes.SystemRemark(0, 0, []byte(longMemo))
	batchCall := stypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall})
	extHex := buildTestExtrinsic(batchCall)

	txItem, err := sc.processExtrinsic(extHex, 200, 0)
	c.Assert(err, IsNil)
	c.Check(txItem, IsNil, Commentf("expected nil TxInItem for oversized memo"))
}

func (s *ScannerSuite) TestProcessExtrinsic_MaxSizeMemoAccepted(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xCC
	amount := cosmos.NewUint(1_000_000_000)

	// Build a memo that is exactly MaxMemoSize (250 bytes) — should be accepted
	exactMemo := strings.Repeat("B", constants.MaxMemoSize)

	transferCall := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	remarkCall := stypes.SystemRemark(0, 0, []byte(exactMemo))
	batchCall := stypes.BatchAll(11, 2, [][]byte{transferCall, remarkCall})
	extHex := buildTestExtrinsic(batchCall)

	txItem, err := sc.processExtrinsic(extHex, 200, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem for max-size memo"))
	c.Check(txItem.Memo, Equals, exactMemo,
		Commentf("memo length: got %d, want %d", len(txItem.Memo), constants.MaxMemoSize))
}

func (s *ScannerSuite) TestProcessExtrinsic_NoMemoTransferAccepted(c *C) {
	sc := newTestScanner()

	dest := make([]byte, 32)
	dest[0] = 0xDD
	amount := cosmos.NewUint(1_000_000_000)

	// Direct transfer (no batch, no memo) — memo is empty, should always pass
	call := stypes.BalanceTransferKeepAlive(5, 3, dest, amount)
	extHex := buildTestExtrinsic(call)

	txItem, err := sc.processExtrinsic(extHex, 100, 0)
	c.Assert(err, IsNil)
	c.Assert(txItem, NotNil, Commentf("expected non-nil TxInItem"))
	c.Check(txItem.Memo, Equals, "", Commentf("memo: got %q, want empty", txItem.Memo))
}

// ---------------------------------------------------------------------------
// processFailedExtrinsic — gas-only observation for failed vault outbounds
// ---------------------------------------------------------------------------

func (s *ScannerSuite) TestProcessFailedExtrinsic(c *C) {
	dest := make([]byte, 32)
	dest[0] = 0xAA
	transferAmount := cosmos.NewUint(1_000_000_000)

	simpleTransfer := stypes.BalanceTransferKeepAlive(5, 3, dest, transferAmount)
	batchWithMemo := stypes.BatchAll(11, 2, [][]byte{
		stypes.BalanceTransferKeepAlive(5, 3, dest, transferAmount),
		stypes.SystemRemark(0, 0, []byte("OUT:DEAD")),
	})
	unknownPallet := []byte{99, 0}

	vaultSigner := defaultTestSigner()
	userSigner := make([]byte, 32)
	for i := range userSigner {
		userSigner[i] = 0xAA
	}

	vaultAddr, err := ss58.Encode(common.TAOChain.SS58Prefix(), vaultSigner)
	c.Assert(err, IsNil, Commentf("failed to derive vault SS58 address"))

	tests := []struct {
		name          string
		actualFee     uint64
		signer        []byte
		callData      []byte
		expectKept    bool
		expectCleanup bool
	}{
		{
			name:          "actualFee zero — cleans cache without observation",
			actualFee:     0,
			signer:        vaultSigner,
			callData:      simpleTransfer,
			expectKept:    false,
			expectCleanup: true,
		},
		{
			name:       "non-vault sender — dropped (not our outbound to account for)",
			actualFee:  150_000,
			signer:     userSigner,
			callData:   simpleTransfer,
			expectKept: false,
		},
		{
			name:          "vault sender, simple transfer — fake gas emitted",
			actualFee:     150_000,
			signer:        vaultSigner,
			callData:      simpleTransfer,
			expectKept:    true,
			expectCleanup: true,
		},
		{
			name:          "vault sender, batch with memo — fake gas replaces memo",
			actualFee:     150_000,
			signer:        vaultSigner,
			callData:      batchWithMemo,
			expectKept:    true,
			expectCleanup: true,
		},
		{
			name:       "vault sender, unknown pallet — dropped (parser yields nil)",
			actualFee:  150_000,
			signer:     vaultSigner,
			callData:   unknownPallet,
			expectKept: false,
		},
	}

	for _, tt := range tests {
		var cleanedHash string
		sc := newTestScanner()
		sc.pubkeyMgr = &vaultPubKeyStub{
			MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
			vaults:                   map[string]bool{vaultAddr: true},
		}
		sc.failHandler = func(txHash string) error {
			cleanedHash = txHash
			return nil
		}

		extHex := buildTestExtrinsicFromSigner(tt.signer, tt.callData)

		got, err := sc.processFailedExtrinsic(extHex, 100, tt.actualFee)
		c.Assert(err, IsNil, Commentf("%s: unexpected error", tt.name))
		c.Check(cleanedHash != "", Equals, tt.expectCleanup,
			Commentf("%s: cleanup callback mismatch", tt.name))

		if !tt.expectKept {
			c.Check(got, IsNil, Commentf("%s: expected nil", tt.name))
			continue
		}
		c.Assert(got, NotNil, Commentf("%s: expected non-nil TxInItem", tt.name))
		c.Assert(got.Coins, HasLen, 1)
		c.Check(got.Coins[0].Asset.Equals(common.TAOChain.GetGasAsset()), Equals, true)
		c.Check(got.Coins[0].Amount.Equal(cosmos.OneUint()), Equals, true)
		c.Check(len(got.Gas) > 0, Equals, true,
			Commentf("%s: Gas should be preserved for accounting", tt.name))
		txID, err := common.NewTxID(got.Tx)
		c.Assert(err, IsNil)
		c.Check(got.Memo, Equals, "OUT:"+txID.String(),
			Commentf("%s: memo must self-reference the failed extrinsic", tt.name))
		c.Check(got.Sender, Equals, vaultAddr,
			Commentf("%s: sender preserved", tt.name))
		c.Check(got.Tx, Not(Equals), "",
			Commentf("%s: tx hash preserved", tt.name))
	}
}

func (s *ScannerSuite) TestProcessFailedExtrinsicCleanupError(c *C) {
	sc := newTestScanner()
	vaultSigner := defaultTestSigner()
	vaultAddr, err := ss58.Encode(common.TAOChain.SS58Prefix(), vaultSigner)
	c.Assert(err, IsNil)
	sc.pubkeyMgr = &vaultPubKeyStub{
		MockPoolAddressValidator: pubkeymanager.NewMockPoolAddressValidator(),
		vaults:                   map[string]bool{vaultAddr: true},
	}
	sc.failHandler = func(string) error { return errors.New("database unavailable") }
	call := stypes.BalanceTransferKeepAlive(5, 3, bytes.Repeat([]byte{0x01}, 32), cosmos.NewUint(100))

	item, err := sc.processFailedExtrinsic(buildTestExtrinsic(call), 100, 1)
	c.Check(item, IsNil)
	c.Assert(err, NotNil)
	c.Check(errors.Is(err, errFinalizedFailureCleanup), Equals, true)
}
